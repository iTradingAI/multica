package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestDetectVersion_HangingProbeLeavesNoProcessesAfterEachRound is the
// version-probe half of the 2026-09-25 MaxPc incident (MAX-144): the daemon's
// registration loop retried a wedged CLI every round, and each probe's process
// tree stayed alive on the host. DetectVersion already bounds a hanging
// `--version` (MUL-3812, TestDetectVersionTimesOutOnHang); this test asserts
// the other half of the incident's acceptance criterion — the bounded probe
// also reaps the tree, so repeated rounds do not accumulate processes.
func TestDetectVersion_HangingProbeLeavesNoProcessesAfterEachRound(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on a /bin/sh hang script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "hang.sh")
	leaderPID := filepath.Join(dir, "leader.pid")
	childPID := filepath.Join(dir, "child.pid")
	// The CLI records leader and a pipe-holding descendant, then hangs — the
	// shape the leaked `node ... --probe` processes had on the incident host.
	body := fmt.Sprintf("#!/bin/sh\nsleep 120 &\necho $! > %q\necho $$ > %q\nwait\n", childPID, leaderPID)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write hang script: %v", err)
	}
	t.Cleanup(func() { reapLeakedHangTree(leaderPID, childPID) })

	orig := detectVersionTimeout
	detectVersionTimeout = 200 * time.Millisecond
	t.Cleanup(func() { detectVersionTimeout = orig })

	for round := 1; round <= 3; round++ {
		if _, err := DetectVersion(context.Background(), Command{Path: script}); err == nil {
			t.Fatalf("round %d: expected an error from a hanging --version probe", round)
		}
		pids := readLeakPIDs(t, leaderPID, childPID)
		if len(pids) == 0 {
			t.Fatalf("round %d: stub recorded no PIDs; fixture is broken", round)
		}
		if !waitLeakPIDsGone(pids, 5*time.Second) {
			t.Fatalf("round %d: leaked processes survived the version probe: %v", round, pids)
		}
		_ = os.Remove(leaderPID)
		_ = os.Remove(childPID)
	}
}

func readLeakPIDs(t *testing.T, files ...string) []int {
	t.Helper()
	return parseLeakPIDFiles(files...)
}

func parseLeakPIDFiles(files ...string) []int {
	var pids []int
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

func waitLeakPIDsGone(pids []int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		alive := false
		for _, pid := range pids {
			if syscall.Kill(pid, 0) == nil {
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

func reapLeakedHangTree(files ...string) {
	for _, pid := range parseLeakPIDFiles(files...) {
		if syscall.Kill(pid, 0) == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}
