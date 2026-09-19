//go:build unix

package termhost

import (
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests run the real pty path (creack/pty + a real shell). They are the
// daemon-side counterpart of the desktop's fake-pty unit matrix: everything
// the fake cannot prove — real byte flow, kernel winsize, real exit codes —
// is proven here on the platform the daemon targets first.
//
// The suite is unix-only by build tag; the daemon ships for Windows too, where
// these tests are skipped until the ConPTY host lands.

func liveHost(t *testing.T) (*Host, *recordingSink) {
	t.Helper()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}
	sink := &recordingSink{}
	host := New(Options{DefaultShell: func() string { return "/bin/sh" }})
	return host, sink
}

func waitForCondition(t *testing.T, done func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}

type collector struct {
	mu   sync.Mutex
	data strings.Builder
	exits []int
}

func (c *collector) Data(_ string, p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.Write(p)
}

func (c *collector) Exit(_ string, code int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.exits = append(c.exits, code)
}

func (c *collector) Gone() bool { return false }

func (c *collector) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data.String()
}

func (c *collector) codes() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.exits...)
}

func TestLiveSessionRoundTrip(t *testing.T) {
	host, sink := liveHost(t)
	// Capture once: t.TempDir() hands out a fresh subdirectory per call.
	cwd := t.TempDir()
	spawn := host.Spawn("conn-1", SpawnRequest{Cols: 80, Rows: 24, Cwd: cwd}, sink)
	if !spawn.OK {
		t.Fatalf("spawn failed: %+v", spawn)
	}

	// Real byte flow through the pty.
	if !host.Write("conn-1", spawn.SessionID, []byte("printf 'LIVE_MARK\\n'\n")) {
		t.Fatal("write failed")
	}
	waitForCondition(t, func() bool { return strings.Contains(sink.text(), "LIVE_MARK") }, "command output")

	// Resize reaches the kernel winsize (stty reads it back).
	if !host.Resize("conn-1", spawn.SessionID, 120, 40) {
		t.Fatal("resize failed")
	}
	if !host.Write("conn-1", spawn.SessionID, []byte("stty size\n")) {
		t.Fatal("write failed")
	}
	waitForCondition(t, func() bool { return strings.Contains(sink.text(), "40 120") }, "stty size")

	// cwd honoured: pwd prints the directory we asked for.
	if !host.Write("conn-1", spawn.SessionID, []byte("pwd\n")) {
		t.Fatal("write failed")
	}
	waitForCondition(t, func() bool { return strings.Contains(sink.text(), cwd) }, "pwd output: "+sink.text())

	// Real exit code from a real shell.
	if !host.Write("conn-1", spawn.SessionID, []byte("exit 7\n")) {
		t.Fatal("write failed")
	}
	waitForCondition(t, func() bool { return len(sink.codes()) > 0 }, "exit event")
	if code := sink.codes(); len(code) != 1 || code[0] != 7 {
		t.Fatalf("exit codes = %v, want [7]", code)
	}
	if host.SessionCount() != 0 {
		t.Fatalf("session count = %d, want 0", host.SessionCount())
	}
}

func TestLiveKillTerminatesProcess(t *testing.T) {
	host, sink := liveHost(t)
	spawn := host.Spawn("conn-1", SpawnRequest{Cols: 80, Rows: 24}, sink)
	if !spawn.OK {
		t.Fatalf("spawn failed: %+v", spawn)
	}
	if !host.Write("conn-1", spawn.SessionID, []byte("echo SHELLPID=$$\n")) {
		t.Fatal("write failed")
	}
	waitForCondition(t, func() bool { return strings.Contains(sink.text(), "SHELLPID=") }, "pid line")

	// The echoed command also contains the marker, so parse the last one —
	// that is the shell's actual output.
	pidLine := sink.text()
	idx := strings.LastIndex(pidLine, "SHELLPID=")
	rest := pidLine[idx+len("SHELLPID="):]
	digits := rest
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			digits = digits[:i]
			break
		}
	}
	if digits == "" {
		t.Fatalf("could not parse shell pid from %q", pidLine)
	}

	if !host.Kill("conn-1", spawn.SessionID) {
		t.Fatal("kill failed")
	}
	// The pty master closed; the shell gets SIGHUP and dies. We observe that
	// indirectly: the session is gone and no further writes are accepted.
	time.Sleep(200 * time.Millisecond)
	if host.SessionCount() != 0 {
		t.Fatalf("session count = %d, want 0", host.SessionCount())
	}
	if host.Write("conn-1", spawn.SessionID, []byte("echo nope\n")) {
		t.Fatal("write after kill accepted")
	}
}

func TestLiveOwnerScopedAgainstSecondConnection(t *testing.T) {
	host, sinkA := liveHost(t)
	a := host.Spawn("conn-A", SpawnRequest{Cols: 80, Rows: 24}, sinkA)
	b := host.Spawn("conn-B", SpawnRequest{Cols: 80, Rows: 24}, &collector{})
	if !a.OK || !b.OK {
		t.Fatalf("spawns failed: %+v / %+v", a, b)
	}

	// Connection B must not be able to write into A's session.
	if host.Write("conn-B", a.SessionID, []byte("echo CROSS_WINDOW\n")) {
		t.Fatal("foreign owner wrote to the session")
	}
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(sinkA.text(), "CROSS_WINDOW") {
		t.Fatal("cross-session write reached the owning shell (MAX-50 regression)")
	}
	if host.Kill("conn-B", a.SessionID) {
		t.Fatal("foreign owner killed the session")
	}
	if host.SessionCount() != 2 {
		t.Fatalf("foreign commands disturbed sessions: %d", host.SessionCount())
	}

	// The owner's own path still works.
	if !host.Write("conn-A", a.SessionID, []byte("printf 'OWNER_ONLY\\n'\n")) {
		t.Fatal("owner write failed")
	}
	waitForCondition(t, func() bool { return strings.Contains(sinkA.text(), "OWNER_ONLY") }, "owner output")
}
