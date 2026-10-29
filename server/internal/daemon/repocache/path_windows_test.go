package repocache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A copy of the test executable serves as a native Git wrapper, scoped to one
// temporary fixture. No production command hook or shell dependency is needed.
func init() {
	exe, _ := os.Executable()
	if os.Getenv("MULTICA_TEST_GUARD_GIT") == "1" && strings.EqualFold(filepath.Base(exe), "git.exe") {
		os.Exit(runGuardGit())
	}
}

type guardGitRecord struct {
	Args    []string
	Exit    int
	Common  string
	Renamed bool
}

func runGuardGit() int {
	args := os.Args[1:]
	record := guardGitRecord{Args: args, Exit: -1}
	log := func() {
		f, err := os.OpenFile(os.Getenv("MULTICA_TEST_GUARD_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			panic(err)
		}
		defer f.Close()
		if err := json.NewEncoder(f).Encode(record); err != nil {
			panic(err)
		}
	}
	defer log()
	cmd := exec.Command(os.Getenv("MULTICA_TEST_GUARD_REAL_GIT"), args...)
	var stdout bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, os.Stderr
	err := cmd.Run()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			record.Exit = e.ExitCode()
			return record.Exit
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	record.Exit = 0
	query := []string{"-C", os.Getenv("MULTICA_TEST_GUARD_CHECKOUT"), "rev-parse", "--git-common-dir"}
	if os.Getenv("MULTICA_TEST_GUARD_RENAME") == "1" && reflect.DeepEqual(args, query) {
		record.Common = strings.TrimSpace(stdout.String())
		bare := os.Getenv("MULTICA_TEST_GUARD_BARE")
		if record.Common != bare || !filepath.IsAbs(bare) {
			panic("fixture did not produce the identical absolute common-dir spelling")
		}
		if err := os.Rename(bare, bare+".retained"); err != nil {
			panic(err)
		}
		record.Renamed = true
		state, err := snapshotGuardTrees(bare+".retained", query[1])
		if err != nil {
			panic(err)
		}
		data, err := json.Marshal(state)
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(os.Getenv("MULTICA_TEST_GUARD_SNAPSHOT"), data, 0o600); err != nil {
			panic(err)
		}
	}
	_, err = os.Stdout.Write(stdout.Bytes())
	if err != nil {
		panic(err)
	}
	return 0
}

func snapshotGuardTrees(roots ...string) (map[string]string, error) {
	state := make(map[string]string)
	for i, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			value := info.Mode().String()
			if !entry.IsDir() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				value += fmt.Sprintf(" %x", sha256.Sum256(data))
			}
			state[fmt.Sprintf("%d/%s", i, rel)] = value
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return state, nil
}

func guardTreeState(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	state, err := snapshotGuardTrees(roots...)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func guardGitWrapper(t *testing.T, bare, checkout string, rename bool) (string, string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git.exe"), data, 0o700); err != nil {
		t.Fatal(err)
	}
	log, snapshot := filepath.Join(dir, "commands.jsonl"), filepath.Join(dir, "after-rename.json")
	t.Setenv("MULTICA_TEST_GUARD_GIT", "1")
	t.Setenv("MULTICA_TEST_GUARD_REAL_GIT", realGit)
	t.Setenv("MULTICA_TEST_GUARD_LOG", log)
	t.Setenv("MULTICA_TEST_GUARD_SNAPSHOT", snapshot)
	t.Setenv("MULTICA_TEST_GUARD_BARE", bare)
	t.Setenv("MULTICA_TEST_GUARD_CHECKOUT", checkout)
	t.Setenv("MULTICA_TEST_GUARD_RENAME", "0")
	if rename {
		t.Setenv("MULTICA_TEST_GUARD_RENAME", "1")
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log, snapshot
}

func readGuardCommands(t *testing.T, path string) []guardGitRecord {
	t.Helper()
	var records []guardGitRecord
	for _, line := range strings.Split(strings.TrimSpace(readFile(t, path)), "\n") {
		var record guardGitRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func guardJunction(t *testing.T, target string) string {
	t.Helper()
	link := filepath.Join(t.TempDir(), "alias")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("create junction: %s: %v", out, err)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil {
			t.Errorf("remove fixture junction: %v", err)
		}
	})
	return link
}

func TestLinkedIdentityJunctionGuardWindows(t *testing.T) {
	testLinkedJunctionGuard(t, false)
}

func TestLinkedRemovalJunctionGuardWindows(t *testing.T) {
	testLinkedJunctionGuard(t, true)
}

func testLinkedJunctionGuard(t *testing.T, removal bool) {
	for _, name := range []string{"same-middle", "same-end", "foreign", "unresolvable-identical"} {
		t.Run(name, func(t *testing.T) {
			identityTestEnvironment(t)
			gitIdentityCommand(t, "config", "--global", "core.longpaths", "true")
			f := newExistingCheckoutFixture(t, false)
			polluteCacheIdentity(t, f.cache.Lookup("ws-1", f.source))
			linked := f.checkout(t, firstTaskID, false)
			bare := f.cache.Lookup("ws-1", f.source)
			neighbor := filepath.Join(t.TempDir(), "neighbor")
			gitIdentityCommand(t, "-C", bare, "worktree", "add", "-b", "neighbor", neighbor, "HEAD")
			neighborAdmin := gitIdentityCommand(t, "-C", neighbor, "rev-parse", "--absolute-git-dir")
			admin := gitIdentityCommand(t, "-C", linked.Path, "rev-parse", "--absolute-git-dir")
			if err := os.WriteFile(filepath.Join(admin, "multica-identity.config"), []byte("[user]\n\temail = stale@example.com\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			neighborBefore := guardTreeState(t, neighbor, neighborAdmin)
			configBefore := readFile(t, filepath.Join(bare, "config"))
			actualBare := bare
			switch name {
			case "same-middle":
				actualBare = filepath.Join(guardJunction(t, filepath.Dir(bare)), filepath.Base(bare))
			case "same-end":
				actualBare = guardJunction(t, bare)
			case "foreign":
				other := newExistingCheckoutFixture(t, false)
				other.checkout(t, firstTaskID, false)
				actualBare = other.cache.Lookup("ws-1", other.source)
			case "unresolvable-identical":
				actualBare = gitIdentityCommand(t, "-C", linked.Path, "rev-parse", "--git-common-dir")
				t.Cleanup(func() {
					if _, err := os.Stat(actualBare + ".retained"); err == nil {
						if err := os.Rename(actualBare+".retained", actualBare); err != nil {
							t.Errorf("restore fixture bare: %v", err)
						}
					}
				})
			}
			var before map[string]string
			if !strings.HasPrefix(name, "same-") {
				before = guardTreeState(t, bare, linked.Path, actualBare)
			}
			log, snapshot := guardGitWrapper(t, actualBare, linked.Path, name == "unresolvable-identical")
			var err error
			if strings.HasPrefix(name, "same-") && removal {
				// Fresh is confined to this disposable fixture and prevents a keep
				// result from bypassing the linked removal guard.
				var result *WorktreeResult
				result, err = f.cache.createOrUpdateIsolatedCheckout(actualBare, f.source, linked.Path, "agent/migrated", "HEAD", true)
				if err == nil && (result.Kept != "" || !isIsolatedCheckout(result.Path)) {
					t.Fatalf("migration did not establish independent metadata: %+v", result)
				}
			} else if removal {
				err = removeLinkedWorktreeContext(context.Background(), actualBare, linked.Path)
			} else {
				err = isolateWorktreeIdentityContext(context.Background(), actualBare, linked.Path)
			}
			records := readGuardCommands(t, log)
			if strings.HasPrefix(name, "same-") {
				if err != nil {
					t.Fatal(err)
				}
				if removal {
					if _, err := os.Stat(admin); !os.IsNotExist(err) {
						t.Fatalf("old admin not removed: %v", err)
					}
					removed := false
					for _, record := range records {
						if strings.Contains(strings.Join(record.Args, " "), "worktree remove --force") && record.Exit == 0 {
							removed = true
						}
					}
					if !removed {
						t.Fatal("migration did not execute linked removal")
					}
				} else {
					if !strings.Contains(readFile(t, filepath.Join(admin, "multica-identity.config")), "user@example.com") || !strings.HasPrefix(readFile(t, filepath.Join(admin, "config.worktree")), identityConfigInclude) {
						t.Fatal("worktree identity was not isolated")
					}
				}
				if got := readFile(t, filepath.Join(bare, "config")); got != configBefore {
					t.Fatal("shared config changed")
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "does not match cache") {
					t.Fatalf("want ownership guard rejection, got %v", err)
				}
				if len(records) != 1 || records[0].Exit != 0 || !reflect.DeepEqual(records[0].Args, []string{"-C", linked.Path, "rev-parse", "--git-common-dir"}) {
					t.Fatalf("commands crossed guard or real query failed: %+v", records)
				}
				var after map[string]string
				if name == "unresolvable-identical" {
					if !records[0].Renamed || records[0].Common != actualBare {
						t.Fatalf("fault did not reach identical unresolved paths: %+v", records[0])
					}
					before = nil
					if err := json.Unmarshal([]byte(readFile(t, snapshot)), &before); err != nil {
						t.Fatal(err)
					}
					after = guardTreeState(t, actualBare+".retained", linked.Path)
				} else {
					after = guardTreeState(t, bare, linked.Path, actualBare)
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatal("guard rejection changed config, identity, checkout, or admin data")
				}
				t.Logf("guard rejected after real Git exit=%d rename=%v common=%q; only command=%v", records[0].Exit, records[0].Renamed, records[0].Common, records[0].Args)
			}
			if name == "unresolvable-identical" {
				neighborAdmin = filepath.Join(actualBare+".retained", "worktrees", filepath.Base(neighborAdmin))
			}
			if !reflect.DeepEqual(neighborBefore, guardTreeState(t, neighbor, neighborAdmin)) {
				t.Fatal("neighbor worktree or its registration changed")
			}
			t.Logf("guard=%s removal=%v real Git records=%d; config/identity/checkout/admin invariants verified", name, removal, len(records))
		})
	}
}
