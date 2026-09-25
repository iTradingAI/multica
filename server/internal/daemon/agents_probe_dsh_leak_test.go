//go:build unix

// The shell fixture and the syscall.Kill liveness checks below are Unix-only;
// Windows does not compile this file at all — its tree kill is exercised by the
// job-object controller tests — so the runtime GOOS skip inside the test is
// unreachable belt-and-braces.
package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The 2026-09-25 MaxPc incident (MAX-144): the daemon's periodic
// re-registration probed a dsh CLI whose `--profile multica --probe` hung
// forever, and every round left the probe process — and the node tree under it
// — alive on the host. 452 leaked processes and ~48.6 GB of committed memory
// later, the machine stalled. The probe path now runs the CLI through
// processtree, so cancellation terminates the whole tree, not just the leader.
//
// This test encodes the incident's acceptance criterion in a CI-bounded form:
// a stub CLI whose --probe hangs forever is probed round after round, and every
// process the stub created must be gone by the time each round returns. A
// regression that lets even one round's tree survive fails here, without
// waiting out the real 15-second timeout or a one-hour soak.
func TestProbeDshMulticaProfile_HangingProbeLeavesNoProcessesAfterEachRound(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture; the Windows tree kill is covered by the job-object controller")
	}
	pidDir := t.TempDir()
	pidFile := func(name string) string { return filepath.Join(pidDir, name) }
	// The manifest is present, matching the incident: the profile existed and
	// the probe still hung, which classifies as transient — the case that kept
	// the registration loop retrying every round. The stub records its whole
	// tree (leader plus a backgrounded descendant holding the stdout pipe open,
	// the shape of the leaked `node .../bin.js --probe` processes) and hangs.
	// `--version` still answers, because the incident's dsh resolved, answered
	// `--version`, and only hung on the profile probe.
	path := filepath.Join(t.TempDir(), "dsh")
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *--probe*)\n" +
		"    echo $$ > " + shQuote(pidFile("leader.pid")) + "\n" +
		"    sleep 120 &\n" +
		"    echo $! > " + shQuote(pidFile("child.pid")) + "\n" +
		"    wait\n" +
		"    ;;\n" +
		"  *) printf '%s\\n' '0.1.2-rc.1' ;;\n" +
		"esac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("DSH_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "profiles", dshMulticaProfileName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "profiles", dshMulticaProfileName, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	origTimeout := dshProbeTimeout
	t.Cleanup(func() { dshProbeTimeout = origTimeout })
	dshProbeTimeout = 500 * time.Millisecond

	// Every round must end with every tree member gone: this is "child
	// process count does not grow", checked per round instead of after a
	// one-hour soak. The pid files are cleared between rounds so a leaked tree
	// is attributed to the round that leaked it.
	for round := 1; round <= 3; round++ {
		if got := probeDshMulticaProfile(context.Background(), path); got != dshProbeUnavailable {
			t.Fatalf("round %d: probeDshMulticaProfile() = %v, want %v", round, got, dshProbeUnavailable)
		}
		pids := recordedLeakPIDs(t, pidDir)
		if len(pids) == 0 {
			t.Fatalf("round %d: stub recorded no PIDs; fixture is broken", round)
		}
		if !waitForLeakPIDsGone(pids, 5*time.Second) {
			t.Fatalf("round %d: leaked processes survived the probe: %v", round, pids)
		}
		for _, name := range []string{"leader.pid", "child.pid"} {
			_ = os.Remove(pidFile(name))
		}
	}
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// recordedLeakPIDs reads the stub's pid files. Missing files mean the tree
// never got far enough to record itself; that is not a leak, so they are
// skipped.
func recordedLeakPIDs(t *testing.T, pidDir string) []int {
	t.Helper()
	var pids []int
	for _, name := range []string{"leader.pid", "child.pid"} {
		data, err := os.ReadFile(filepath.Join(pidDir, name))
		if err != nil {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

func leakPIDAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func waitForLeakPIDsGone(pids []int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		alive := false
		for _, pid := range pids {
			if leakPIDAlive(pid) {
				alive = true
				break
			}
		}
		if !alive {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
