package repocache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// refsDirName is the reference-checkout area inside the workspaces root, a
// dot-prefixed sibling of the bare-repo cache (.repos). Dot-prefixed on
// purpose: every walk over the workspaces root (the GC's orphan scan, the
// disk-usage report) already treats dot directories as daemon-internal
// caches and skips them, so reference checkouts — which carry no task
// metadata and would otherwise look like 72h-old orphans — are exempt by the
// existing rule rather than a new one (MAX-184).
const refsDirName = ".refs"

// ReferencePath returns the daemon-owned landing spot for a workspace's
// reference checkout of a repo: <workspaces root>/.refs/<workspace-id>/<repo>.
// The caller never supplies this path — the daemon derives it, which is what
// keeps reference checkouts out of in_place project directories no matter
// where the calling task's workdir sits.
//
// The repo segment reuses the bare cache's URL-derived name (bareDirName)
// rather than the bare repo basename, so two same-named repos in one
// workspace — org-a/service.git and org-b/service.git — land in distinct
// directories instead of refreshing each other's worktree through the wrong
// bare cache's lock (MAX-184 R2).
func ReferencePath(workspacesRoot, workspaceID, repoURL string) string {
	return filepath.Join(workspacesRoot, refsDirName, workspaceID,
		strings.TrimSuffix(bareDirName(repoURL), ".git"))
}

// ReferenceParams describes a reference checkout request. Unlike
// WorktreeParams it carries no WorkDir and no branch naming: the landing path
// is daemon-derived (ReferencePath) and the checkout is detached, because a
// reference exists to be read, not to carry a task's delivery branch.
type ReferenceParams struct {
	WorkspaceID string // workspace that owns the repo
	RepoURL     string // remote URL to look up in the cache
	// Ref is the optional branch, tag, or commit the reference must sit at.
	// Empty resolves to the remote's default branch.
	Ref string
	// Path is the daemon-derived landing spot (ReferencePath).
	Path string
	// LockWaitTimeout bounds only the wait for another same-repository
	// operation, mirroring WorktreeParams.LockWaitTimeout. Zero preserves the
	// historical unbounded wait.
	LockWaitTimeout time.Duration
	// Fresh discards an existing reference checkout's local modifications and
	// untracked files before moving it to the requested ref (`multica repo
	// checkout --fresh --reference`). Without it, a dirty reference checkout
	// is kept exactly as it is and reported as such, mirroring the
	// never-destroy-work rule the task-worktree path enforces.
	Fresh bool
}

// CreateReferenceWorktree builds or refreshes the workspace's shared
// reference checkout of a repo from the existing bare cache: one detached
// worktree per (workspace, repo), reused by every task that asks, never
// inside a task's or project's directory.
//
// The checkout is shared deliberately — it is reference material, not a
// workspace for mutation, and per-task copies of whole trees were the
// directory-pollution problem this mode exists to end. Concurrent requests
// serialize on the repo lock; the last ref request wins the HEAD position,
// which is the documented contract for a shared reference area.
func (c *Cache) CreateReferenceWorktree(ctx context.Context, params ReferenceParams) (*WorktreeResult, error) {
	barePath := c.Lookup(params.WorkspaceID, params.RepoURL)
	if barePath == "" {
		return nil, fmt.Errorf("repo not found in cache: %s (workspace: %s)", params.RepoURL, params.WorkspaceID)
	}
	if strings.TrimSpace(params.Path) == "" {
		return nil, errors.New("reference checkout requires a daemon-derived landing path")
	}

	repoLock := c.lockForRepo(barePath)
	lockCtx := ctx
	cancel := func() {}
	if params.LockWaitTimeout > 0 {
		lockCtx, cancel = context.WithTimeout(ctx, params.LockWaitTimeout)
	}
	err := repoLock.LockContext(lockCtx)
	cancel()
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %s", ErrRepoBusy, params.RepoURL)
		}
		return nil, err
	}
	defer repoLock.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}

	// Same liveness semantics as the task-worktree path: asking is what makes
	// the cache wanted, whether or not this checkout succeeds.
	MarkUsed(barePath, c.logger)

	if err := gitFetchContext(ctx, barePath); err != nil {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		c.logger.Warn("repo checkout: fetch failed, reference may be stale",
			"url", params.RepoURL,
			"error", err,
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}

	baseRef, err := resolveBaseRefContext(ctx, barePath, params.Ref)
	if err != nil {
		return nil, err
	}
	if baseRef == "" {
		return nil, fmt.Errorf("cannot resolve default branch for %s: bare cache at %s has no usable refs", params.RepoURL, barePath)
	}

	if isGitWorktree(params.Path) {
		return c.refreshReferenceWorktree(ctx, barePath, params, baseRef)
	}

	// Anything else already sitting at the landing spot must be an empty
	// directory (a leftover mkdir) or nothing at all — a non-empty,
	// non-worktree path is data we refuse to clobber.
	if entries, readErr := os.ReadDir(params.Path); readErr == nil && len(entries) > 0 {
		return nil, fmt.Errorf("reference checkout path exists and is not a git worktree: %s", params.Path)
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return nil, fmt.Errorf("inspect reference checkout path %s: %w", params.Path, readErr)
	}
	if err := os.MkdirAll(filepath.Dir(params.Path), 0o755); err != nil {
		return nil, fmt.Errorf("create reference checkout parent: %w", err)
	}
	if out, err := runGitCombinedOutputContext(ctx, "-C", barePath, "worktree", "add", "--detach", params.Path, baseRef); err != nil {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		// A failed add can leave a half-registered worktree; prune so the
		// next attempt starts clean, then surface the original failure.
		_, _ = runGitCombinedOutputContext(ctx, "-C", barePath, "worktree", "prune")
		return nil, fmt.Errorf("git worktree add (reference): %s: %w", strings.TrimSpace(string(out)), err)
	}

	c.logger.Info("repo checkout: reference worktree created",
		"url", params.RepoURL,
		"path", params.Path,
		"base", baseRef,
	)
	// Detached by construction: BranchName stays empty for reference
	// checkouts, matching WorktreeResult's "kept checkout on a detached HEAD"
	// convention.
	return &WorktreeResult{Path: params.Path}, nil
}

// refreshReferenceWorktree moves an existing reference checkout to baseRef,
// or keeps it untouched when it holds local modifications and fresh is unset.
func (c *Cache) refreshReferenceWorktree(ctx context.Context, barePath string, params ReferenceParams, baseRef string) (*WorktreeResult, error) {
	if !params.Fresh {
		dirty, uncommitted, err := referenceWorktreeStatus(ctx, params.Path)
		if err != nil {
			return nil, err
		}
		if dirty {
			c.logger.Info("repo checkout: reference checkout kept — it holds local modifications",
				"url", params.RepoURL,
				"path", params.Path,
				"uncommitted_files", uncommitted,
			)
			return &WorktreeResult{
				Path:             params.Path,
				Kept:             KeptLocalWork,
				UncommittedFiles: uncommitted,
			}, nil
		}
	} else {
		// Fresh discards only the reference copy's own drift — it is
		// daemon-owned scratch, never a user's working tree.
		for _, args := range [][]string{
			{"reset", "--hard"},
			{"clean", "-fdq"},
		} {
			full := append([]string{"-C", params.Path}, args...)
			if out, err := runGitCombinedOutputContext(ctx, full...); err != nil {
				if ctx.Err() != nil {
					return nil, context.Cause(ctx)
				}
				return nil, fmt.Errorf("git %s (reference %s): %s: %w", args[0], params.Path, strings.TrimSpace(string(out)), err)
			}
		}
	}
	if out, err := runGitCombinedOutputContext(ctx, "-C", params.Path, "checkout", "--quiet", "--detach", baseRef); err != nil {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		return nil, fmt.Errorf("git checkout (reference %s): %s: %w", params.Path, strings.TrimSpace(string(out)), err)
	}
	c.logger.Info("repo checkout: reference worktree refreshed",
		"url", params.RepoURL,
		"path", params.Path,
		"base", baseRef,
		"fresh", params.Fresh,
	)
	return &WorktreeResult{Path: params.Path}, nil
}

// referenceWorktreeStatus reports whether the reference checkout holds
// uncommitted or untracked changes, and how many entries `git status` lists.
func referenceWorktreeStatus(ctx context.Context, path string) (dirty bool, entries int, err error) {
	out, err := runGitOutputContext(ctx, "-C", path, "status", "--porcelain")
	if err != nil {
		return false, 0, fmt.Errorf("git status (reference %s): %w", path, err)
	}
	status := strings.TrimSpace(string(out))
	if status == "" {
		return false, 0, nil
	}
	return true, len(strings.Split(status, "\n")), nil
}
