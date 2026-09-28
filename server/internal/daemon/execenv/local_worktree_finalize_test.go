package execenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The named worktree-finalize regressions from MAX-184's independent review:
// normal completion, git reporting success while the directory survives,
// occupied-directory retries, cleanup failure classified separately from
// delivery failure, and the two branch-integrity violations on a task-scoped
// branch (which carries no recorded state, so nothing but the verify step
// itself would catch them).

// stubGitWorktreeRemoveSuccess simulates the Windows failure shape where
// `git worktree remove --force` exits 0 but the directory is still on disk:
// it reports success without doing anything, and defers everything else to
// the real git. Tests swap it into worktreeRemoveGit and restore it.
func stubGitWorktreeRemoveSuccess() (restore func()) {
	prev := worktreeRemoveGit
	worktreeRemoveGit = func(dir string, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "worktree" && args[1] == "remove" {
			return "", nil
		}
		return runGit(dir, args...)
	}
	return func() { worktreeRemoveGit = prev }
}

func stubRemoveRetryDelay() (restore func()) {
	prev := worktreeRemoveRetryDelay
	worktreeRemoveRetryDelay = time.Millisecond
	return func() { worktreeRemoveRetryDelay = prev }
}

func TestFinalizeNormalCompletionRemovesWorktreeAndDeliversBranch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	wt := prepareForTest(t, repo)

	writeFile(t, filepath.Join(wt.WorkDir, "agent.txt"), "the delivered work\n")

	outcome, err := wt.Finalize(worktreeTestLogger())
	if err != nil {
		t.Fatalf("Finalize on a normal completion: %v", err)
	}
	if outcome.Branch == "" {
		t.Fatal("outcome named no branch for a task that produced work")
	}
	if outcome.PreservedPath != "" {
		t.Errorf("normal completion reported a preserved path: %q", outcome.PreservedPath)
	}
	if !worktreeDirGone(wt.Path) {
		t.Error("worktree directory still exists after a normal finalize")
	}
	if list := gitRun(t, repo, "worktree", "list"); strings.Contains(filepath.ToSlash(list), filepath.ToSlash(wt.Path)) {
		t.Errorf("worktree registration survived a normal finalize:\n%s", list)
	}
	if body := gitRun(t, repo, "show", outcome.Branch+":agent.txt"); body != "the delivered work" {
		t.Errorf("delivered branch does not carry the agent's work: %q", body)
	}
}

// TestRemoveLocalWorktreeDirRecoversWhenGitReportsSuccessButLeavesTheDirectory
// reproduces the Windows shape the old code missed: git exits 0, the directory
// survives, and nothing ran the fallback. The bounded sequence must notice the
// surviving directory regardless of git's exit status and remove it directly.
func TestRemoveLocalWorktreeDirRecoversWhenGitReportsSuccessButLeavesTheDirectory(t *testing.T) {
	repo := newTestRepo(t)
	wt := prepareForTest(t, repo)
	restoreGit := stubGitWorktreeRemoveSuccess()
	defer restoreGit()

	if err := removeLocalWorktreeDir(repo, wt.Path, worktreeTestLogger()); err != nil {
		t.Fatalf("removeLocalWorktreeDir: %v", err)
	}
	if !worktreeDirGone(wt.Path) {
		t.Error("directory survived despite the fallback")
	}
	if list := gitRun(t, repo, "worktree", "list"); strings.Contains(filepath.ToSlash(list), filepath.ToSlash(wt.Path)) {
		t.Errorf("dangling registration survived the fallback:\n%s", list)
	}
}

// TestRemoveLocalWorktreeDirRetriesWhileTheDirectoryIsOccupied covers the
// bounded-retry contract: a directory whose deletion fails twice (the
// Windows "file in use" shape) is retried, and succeeds once the handle is
// released — without exceeding the attempt bound.
func TestRemoveLocalWorktreeDirRetriesWhileTheDirectoryIsOccupied(t *testing.T) {
	repo := newTestRepo(t)
	wt := prepareForTest(t, repo)
	restoreGit := stubGitWorktreeRemoveSuccess()
	defer restoreGit()
	restoreDelay := stubRemoveRetryDelay()
	defer restoreDelay()

	const failFirst = 2
	realRemove := worktreeRemoveAll
	calls := 0
	worktreeRemoveAll = func(path string) error {
		calls++
		if calls <= failFirst {
			return fmt.Errorf("rm of %s: Access is denied (simulated)", path)
		}
		return realRemove(path)
	}
	defer func() { worktreeRemoveAll = realRemove }()

	if err := removeLocalWorktreeDir(repo, wt.Path, worktreeTestLogger()); err != nil {
		t.Fatalf("removeLocalWorktreeDir should have succeeded after retrying, got: %v", err)
	}
	if calls != failFirst+1 {
		t.Errorf("remove attempts = %d, want %d failures then one success", calls, failFirst+1)
	}
	if !worktreeDirGone(wt.Path) {
		t.Error("directory survived a removal the retries completed")
	}
}

// TestFinalizeClassifiesCleanupFailureSeparatelyFromDelivery is the core
// distinction the review demanded: when the delivery (commit, verification,
// record) succeeded and ONLY the directory removal failed, Finalize reports
// the delivered branch plus a WorktreeCleanupError — never a generic error
// that the caller could mistake for the run or provider having failed.
func TestFinalizeClassifiesCleanupFailureSeparatelyFromDelivery(t *testing.T) {
	repo := newTestRepo(t)
	wt := prepareForTest(t, repo)
	writeFile(t, filepath.Join(wt.WorkDir, "agent.txt"), "delivered despite the mess\n")

	restoreGit := stubGitWorktreeRemoveSuccess()
	defer restoreGit()
	restoreDelay := stubRemoveRetryDelay()
	defer restoreDelay()
	realRemove := worktreeRemoveAll
	worktreeRemoveAll = func(path string) error {
		return fmt.Errorf("rm of %s: Access is denied (simulated permanent lock)", path)
	}
	defer func() { worktreeRemoveAll = realRemove }()

	outcome, err := wt.Finalize(worktreeTestLogger())
	var cleanup *WorktreeCleanupError
	if !errors.As(err, &cleanup) {
		t.Fatalf("want WorktreeCleanupError, got %T: %v", err, err)
	}
	if cleanup.Branch == "" || cleanup.Path != wt.Path {
		t.Errorf("cleanup error is not locatable: branch=%q path=%q", cleanup.Branch, cleanup.Path)
	}
	if !strings.Contains(cleanup.Error(), cleanup.Branch) || !strings.Contains(cleanup.Error(), wt.Path) {
		t.Errorf("cleanup error message hides the evidence: %v", cleanup)
	}
	if outcome.Branch == "" {
		t.Error("outcome hid the delivered branch on a cleanup-only failure")
	}
	if outcome.PreservedPath != "" {
		t.Errorf("cleanup failure reported a preserved path (%q) as if the work lived only in the worktree", outcome.PreservedPath)
	}
	if body := gitRun(t, repo, "show", outcome.Branch+":agent.txt"); body != "delivered despite the mess" {
		t.Errorf("delivered branch does not carry the agent's work: %q", body)
	}
	if worktreeDirGone(wt.Path) {
		t.Error("the lingering directory disappeared, so there was nothing to classify")
	}
}

// TestFinalizeFailsWhenATaskScopedRunDeviatesFromItsBranch: a task-scoped
// branch carries no recorded state, so before MAX-184 nothing verified its
// delivery. A run that ends on a detached HEAD must still fail the task
// instead of naming a branch whose tip is not the work that ran.
func TestFinalizeFailsWhenATaskScopedRunDeviatesFromItsBranch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	head := gitRun(t, repo, "rev-parse", "HEAD")

	wt := prepareForTest(t, repo) // no conversation: task-scoped branch, tracksState=false
	writeFile(t, filepath.Join(wt.WorkDir, "agent.txt"), "off-branch work\n")
	gitRun(t, wt.Path, "add", "-A")
	gitRun(t, wt.Path, "commit", "-m", "on the branch")
	// The run wanders off its own branch before it ends.
	gitRun(t, wt.Path, "checkout", "--quiet", "--detach", head)

	_, err := wt.Finalize(worktreeTestLogger())
	if err == nil {
		t.Fatal("Finalize accepted a task-scoped delivery from off the branch")
	}
	if !strings.Contains(err.Error(), "did not deliver onto its own branch") {
		t.Errorf("error does not name the violation: %v", err)
	}
	var cleanup *WorktreeCleanupError
	if errors.As(err, &cleanup) {
		t.Errorf("an integrity failure was misclassified as cleanup: %v", err)
	}
	if _, statErr := os.Stat(wt.Path); statErr != nil {
		t.Errorf("worktree removed despite refusing the delivery: %v", statErr)
	}
}

// TestFinalizeFailsWhenATaskScopedRunResetsPastItsBaseline: the companion
// violation — the run resets its branch back past the baseline commit this
// turn started from, "delivering" a tip that contains none of the turn's
// work. Must fail even with no recorded state to check it against.
func TestFinalizeFailsWhenATaskScopedRunResetsPastItsBaseline(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	head := gitRun(t, repo, "rev-parse", "HEAD")

	wt := prepareForTest(t, repo)
	if wt.BaseCommit == head {
		t.Fatal("prepare left the task branch on the user's own HEAD, with no commit of its own")
	}
	writeFile(t, filepath.Join(wt.WorkDir, "agent.txt"), "work that a reset will throw away\n")
	// The run throws its own history away and lands back on the user's HEAD.
	gitRun(t, wt.Path, "reset", "--hard", head)

	_, err := wt.Finalize(worktreeTestLogger())
	if err == nil {
		t.Fatal("Finalize accepted a task-scoped delivery that reset past its baseline")
	}
	if !strings.Contains(err.Error(), "no longer contains") {
		t.Errorf("error does not name the violation: %v", err)
	}
	var cleanup *WorktreeCleanupError
	if errors.As(err, &cleanup) {
		t.Errorf("an integrity failure was misclassified as cleanup: %v", err)
	}
	if _, statErr := os.Stat(wt.Path); statErr != nil {
		t.Errorf("worktree removed despite refusing the delivery: %v", statErr)
	}
}
