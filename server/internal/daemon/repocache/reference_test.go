package repocache

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// seedReferenceCache builds a two-branch source repo, a repo cache, and a
// short-lived workspaces root under ONE compact temp directory. The single
// short root matters on Windows: the bare cache flattens the source URL into
// its directory name and the worktree registration stores absolute paths, so
// nested t.TempDir() roots (which embed the test name several times) blow
// past MAX_PATH and fail with git's '$GIT_DIR' too big, exercising path
// arithmetic instead of the behavior under test.
func seedReferenceCache(t *testing.T) (cache *Cache, sourceRepo, refsRoot string) {
	t.Helper()
	root, err := os.MkdirTemp("", "mref")
	if err != nil {
		t.Fatalf("mkdir temp root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	sourceRepo = createTestRepoAt(t, filepath.Join(root, "src"))
	// A second branch with its own commit, so moving the reference between
	// refs is observable in the checked-out tree.
	runTestGit(t, sourceRepo, "switch", "-c", "feature")
	writeReferenceFile(t, sourceRepo, "feature.txt", "feature work\n")
	runTestGit(t, sourceRepo, "add", "-A")
	runTestGit(t, sourceRepo, "commit", "-m", "feature commit")
	runTestGit(t, sourceRepo, "switch", "-")

	cache = New(filepath.Join(root, "cache"), testLogger())
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if err := cache.Fetch(cache.Lookup("ws-1", sourceRepo)); err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	return cache, sourceRepo, filepath.Join(root, "ws")
}

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func writeReferenceFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestReferencePathIsDaemonDerived(t *testing.T) {
	t.Parallel()
	got := ReferencePath("/srv/workspaces", "ws-uuid", "https://github.com/org/repo.git")
	want := filepath.Join("/srv/workspaces", refsDirName, "ws-uuid", "github.com+org+repo")
	if got != want {
		t.Fatalf("ReferencePath = %q, want %q", got, want)
	}
}

// TestReferencePathDisambiguatesSameBasenameRepos pins MAX-184 R2: two repos
// whose URLs differ only above the basename must land in distinct reference
// directories, so one can never refresh the other's worktree through the
// wrong bare cache's lock.
func TestReferencePathDisambiguatesSameBasenameRepos(t *testing.T) {
	t.Parallel()
	a := ReferencePath("/srv/ws", "ws-1", "https://github.com/org-a/service.git")
	b := ReferencePath("/srv/ws", "ws-1", "https://github.com/org-b/service.git")
	if a == b {
		t.Fatalf("same-basename repos collide at %q", a)
	}
	// Distinct workspaces stay isolated too.
	c := ReferencePath("/srv/ws", "ws-2", "https://github.com/org-a/service.git")
	if a == c {
		t.Fatalf("same repo across workspaces collides at %q", a)
	}
}

func TestCreateReferenceWorktreeCreatesDetachedSharedCheckout(t *testing.T) {
	t.Parallel()
	cache, sourceRepo, refsRoot := seedReferenceCache(t)
	target := ReferencePath(refsRoot, "ws-1", sourceRepo)

	result, err := cache.CreateReferenceWorktree(context.Background(), ReferenceParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		Path:        target,
	})
	if err != nil {
		t.Fatalf("CreateReferenceWorktree: %v", err)
	}
	if result.Path != target {
		t.Errorf("result path = %q, want %q", result.Path, target)
	}
	if result.BranchName != "" {
		t.Errorf("reference checkout reported branch %q; it must be detached", result.BranchName)
	}
	if head := runTestGit(t, target, "branch", "--show-current"); head != "" {
		t.Errorf("reference checkout is on branch %q, want detached HEAD", head)
	}
	// Worktree registrations live in the bare cache, not the source repo.
	barePath := cache.Lookup("ws-1", sourceRepo)
	if out := runTestGit(t, barePath, "worktree", "list"); !strings.Contains(filepath.ToSlash(out), filepath.ToSlash(target)) {
		t.Errorf("reference worktree not registered:\n%s", out)
	}

	// A second call for the same workspace and repo reuses the SAME shared
	// checkout rather than materializing another tree.
	again, err := cache.CreateReferenceWorktree(context.Background(), ReferenceParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		Path:        ReferencePath(refsRoot, "ws-1", sourceRepo),
	})
	if err != nil {
		t.Fatalf("second CreateReferenceWorktree: %v", err)
	}
	if again.Path != target {
		t.Errorf("second checkout landed at %q, want the shared %q", again.Path, target)
	}
}

func TestCreateReferenceWorktreeMovesToRequestedRef(t *testing.T) {
	t.Parallel()
	cache, sourceRepo, refsRoot := seedReferenceCache(t)
	target := ReferencePath(refsRoot, "ws-1", sourceRepo)

	if _, err := cache.CreateReferenceWorktree(context.Background(), ReferenceParams{
		WorkspaceID: "ws-1", RepoURL: sourceRepo, Path: target,
	}); err != nil {
		t.Fatalf("initial checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "feature.txt")); err == nil {
		t.Fatal("default-branch checkout unexpectedly contains the feature branch's file")
	}

	if _, err := cache.CreateReferenceWorktree(context.Background(), ReferenceParams{
		WorkspaceID: "ws-1", RepoURL: sourceRepo, Path: target, Ref: "feature",
	}); err != nil {
		t.Fatalf("move to feature: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(target, "feature.txt")); err != nil || string(body) != "feature work\n" {
		t.Errorf("reference did not move to the requested ref (feature.txt: %q, %v)", body, err)
	}
}

func TestCreateReferenceWorktreeKeepsDirtyCheckout(t *testing.T) {
	t.Parallel()
	cache, sourceRepo, refsRoot := seedReferenceCache(t)
	target := ReferencePath(refsRoot, "ws-1", sourceRepo)

	if _, err := cache.CreateReferenceWorktree(context.Background(), ReferenceParams{
		WorkspaceID: "ws-1", RepoURL: sourceRepo, Path: target,
	}); err != nil {
		t.Fatalf("initial checkout: %v", err)
	}
	writeReferenceFile(t, target, "local-notes.txt", "uncommitted notes\n")

	result, err := cache.CreateReferenceWorktree(context.Background(), ReferenceParams{
		WorkspaceID: "ws-1", RepoURL: sourceRepo, Path: target, Ref: "feature",
	})
	if err != nil {
		t.Fatalf("dirty re-checkout: %v", err)
	}
	if result.Kept != KeptLocalWork {
		t.Errorf("Kept = %q, want %q", result.Kept, KeptLocalWork)
	}
	if result.UncommittedFiles == 0 {
		t.Error("Kept result reported no uncommitted files")
	}
	if body, err := os.ReadFile(filepath.Join(target, "local-notes.txt")); err != nil || string(body) != "uncommitted notes\n" {
		t.Errorf("dirty reference was not kept as-is: %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(target, "feature.txt")); err == nil {
		t.Error("a kept dirty checkout must not have moved to the requested ref")
	}

	// Fresh discards the drift and moves.
	freshResult, err := cache.CreateReferenceWorktree(context.Background(), ReferenceParams{
		WorkspaceID: "ws-1", RepoURL: sourceRepo, Path: target, Ref: "feature", Fresh: true,
	})
	if err != nil {
		t.Fatalf("fresh re-checkout: %v", err)
	}
	if freshResult.Kept != "" {
		t.Errorf("fresh re-checkout still reported Kept=%q", freshResult.Kept)
	}
	if body, err := os.ReadFile(filepath.Join(target, "feature.txt")); err != nil || string(body) != "feature work\n" {
		t.Errorf("fresh checkout did not land on feature: %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(target, "local-notes.txt")); !os.IsNotExist(err) {
		t.Error("fresh checkout did not discard the uncommitted file")
	}
}

func TestCreateReferenceWorktreeRefusesForeignDirectory(t *testing.T) {
	t.Parallel()
	cache, sourceRepo, refsRoot := seedReferenceCache(t)
	target := ReferencePath(refsRoot, "ws-1", sourceRepo)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeReferenceFile(t, target, "precious.txt", "not ours to delete\n")

	_, err := cache.CreateReferenceWorktree(context.Background(), ReferenceParams{
		WorkspaceID: "ws-1", RepoURL: sourceRepo, Path: target,
	})
	if err == nil || !strings.Contains(err.Error(), "not a git worktree") {
		t.Fatalf("want a refusal over foreign directory contents, got: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(target, "precious.txt")); err != nil || string(body) != "not ours to delete\n" {
		t.Errorf("foreign directory was modified: %q, %v", body, err)
	}
}

// TestCreateReferenceWorktreeSeparatesSameBasenameRepos is the functional half
// of MAX-184 R2: two same-basename repos in one workspace each get their own
// reference checkout whose content matches its own source, not the sibling's.
func TestCreateReferenceWorktreeSeparatesSameBasenameRepos(t *testing.T) {
	t.Parallel()
	root, err := os.MkdirTemp("", "mref2")
	if err != nil {
		t.Fatalf("mkdir temp root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	repoA := createTestRepoAt(t, filepath.Join(root, "org-a", "service"))
	repoB := createTestRepoAt(t, filepath.Join(root, "org-b", "service"))
	writeReferenceFile(t, repoA, "who.txt", "org-a\n")
	runTestGit(t, repoA, "add", "-A")
	runTestGit(t, repoA, "commit", "-m", "a marker")
	writeReferenceFile(t, repoB, "who.txt", "org-b\n")
	runTestGit(t, repoB, "add", "-A")
	runTestGit(t, repoB, "commit", "-m", "b marker")

	cache := New(filepath.Join(root, "cache"), testLogger())
	if err := cache.Sync("ws-1", []RepoInfo{{URL: repoA}, {URL: repoB}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	refsRoot := filepath.Join(root, "ws")
	pathA := ReferencePath(refsRoot, "ws-1", repoA)
	pathB := ReferencePath(refsRoot, "ws-1", repoB)
	if pathA == pathB {
		t.Fatalf("reference paths collide: %q", pathA)
	}
	for _, tc := range []struct{ url, path, want string }{
		{repoA, pathA, "org-a\n"},
		{repoB, pathB, "org-b\n"},
	} {
		result, err := cache.CreateReferenceWorktree(context.Background(), ReferenceParams{
			WorkspaceID: "ws-1", RepoURL: tc.url, Path: tc.path,
		})
		if err != nil {
			t.Fatalf("CreateReferenceWorktree(%s): %v", tc.url, err)
		}
		if result.Path != tc.path {
			t.Errorf("checkout for %s landed at %q, want %q", tc.url, result.Path, tc.path)
		}
		if got, readErr := os.ReadFile(filepath.Join(tc.path, "who.txt")); readErr != nil || string(got) != tc.want {
			t.Errorf("reference at %s carries the wrong repo's content: %q, %v", tc.path, got, readErr)
		}
	}
}
