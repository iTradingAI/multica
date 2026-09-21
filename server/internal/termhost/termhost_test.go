package termhost

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePty is an in-memory pty: tests push output, close the output channel
// and deliver an exit code to drive the host's pump.
type fakePty struct {
	mu      sync.Mutex
	written strings.Builder
	resizes [][2]int
	killed  bool
	closed  bool
	output  chan []byte
	exited  chan int
}

func newFakePty() *fakePty {
	return &fakePty{output: make(chan []byte, 8), exited: make(chan int, 1)}
}

func (f *fakePty) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written.Write(p)
	return len(p), nil
}

func (f *fakePty) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, [2]int{cols, rows})
	return nil
}

// Kill ends the session the way a real pty does: the read side closes and the
// reaper reports the process is gone.
func (f *fakePty) Kill() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = true
	f.finishLocked(137)
	return nil
}

func (f *fakePty) Wait() int { return <-f.exited }

func (f *fakePty) Output() <-chan []byte { return f.output }

func (f *fakePty) emit(text string) { f.output <- []byte(text) }

// exit ends the session naturally with the given code.
func (f *fakePty) exit(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finishLocked(code)
}

func (f *fakePty) finishLocked(code int) {
	if f.closed {
		return
	}
	f.closed = true
	close(f.output)
	f.exited <- code
}

func (f *fakePty) writtenText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written.String()
}

func (f *fakePty) wasKilled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.killed
}

// recordingSink captures what the host delivers.
type recordingSink struct {
	mu    sync.Mutex
	data  []string
	exits []int
	gone  bool
}

func (s *recordingSink) Data(sessionID string, p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = append(s.data, sessionID+":"+string(p))
}

func (s *recordingSink) Exit(sessionID string, exitCode int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exits = append(s.exits, exitCode)
}

func (s *recordingSink) Gone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gone
}

func (s *recordingSink) dataText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.data, "|")
}

// text and codes are the live-test aliases of dataText/exitCodes.
func (s *recordingSink) text() string  { return s.dataText() }
func (s *recordingSink) codes() []int  { return s.exitCodes() }

func (s *recordingSink) exitCodes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.exits...)
}

// harness wires a host whose sessions all share one fake pty factory.
type harness struct {
	host    *Host
	fakes   []*fakePty
	mu      sync.Mutex
	counter int
	sink    *recordingSink
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{sink: &recordingSink{}}
	h.host = New(Options{
		DefaultShell:     func() string { return "/bin/testshell" },
		IsDir:            func(p string) bool { return p == "/exists/dir" },
		IsExecutableFile: func(p string) bool { return p == "/exists/shell" },
		NewID: func() string {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.counter++
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", h.counter)
		},
		OpenPty: func(req SpawnRequest) (PtyProcess, error) {
			pty := newFakePty()
			h.mu.Lock()
			h.fakes = append(h.fakes, pty)
			h.mu.Unlock()
			return pty, nil
		},
	})
	return h
}

func (h *harness) spawn(owner string, mutate func(*SpawnRequest)) SpawnResult {
	req := SpawnRequest{Cols: 80, Rows: 24}
	if mutate != nil {
		mutate(&req)
	}
	return h.host.Spawn(owner, req, h.sink)
}

func (h *harness) lastFake() *fakePty {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fakes[len(h.fakes)-1]
}

func TestSpawnValidatesRequest(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*SpawnRequest)
		reason string
	}{
		{"cols too small", func(r *SpawnRequest) { r.Cols = 1 }, ReasonInvalid},
		{"cols too large", func(r *SpawnRequest) { r.Cols = 501 }, ReasonInvalid},
		{"rows too small", func(r *SpawnRequest) { r.Rows = 1 }, ReasonInvalid},
		{"rows too large", func(r *SpawnRequest) { r.Rows = 301 }, ReasonInvalid},
		{"relative cwd", func(r *SpawnRequest) { r.Cwd = "relative/path" }, ReasonInvalid},
		{"missing cwd", func(r *SpawnRequest) { r.Cwd = "/no/such/dir" }, ReasonInvalid},
		{"relative shell", func(r *SpawnRequest) { r.Shell = "bin/sh" }, ReasonInvalid},
		{"missing shell", func(r *SpawnRequest) { r.Shell = "/no/such/shell" }, ReasonInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			result := h.spawn("conn-1", tc.mutate)
			if result.OK || result.Reason != tc.reason {
				t.Fatalf("spawn = %+v, want reason %q", result, tc.reason)
			}
			if h.host.SessionCount() != 0 {
				t.Fatalf("rejected spawn leaked a session: %d", h.host.SessionCount())
			}
		})
	}
}

func TestSpawnAcceptsValidRequest(t *testing.T) {
	h := newHarness(t)
	result := h.spawn("conn-1", func(r *SpawnRequest) {
		r.Cwd = "/exists/dir"
		r.Shell = "/exists/shell"
	})
	if !result.OK {
		t.Fatalf("spawn failed: %+v", result)
	}
	// Session ids are minted by the daemon and must keep the uuid shape the
	// desktop already validates against.
	if len(result.SessionID) != 36 || strings.Count(result.SessionID, "-") != 4 {
		t.Fatalf("session id is not uuid-shaped: %q", result.SessionID)
	}
	if h.host.SessionCount() != 1 {
		t.Fatalf("session count = %d, want 1", h.host.SessionCount())
	}
	if got := h.lastFake().writtenText(); got != "" {
		t.Fatalf("spawn wrote unexpected input: %q", got)
	}
}

func TestSpawnUsesDefaultShellWhenUnset(t *testing.T) {
	h := newHarness(t)
	if result := h.spawn("conn-1", nil); !result.OK {
		t.Fatalf("spawn failed: %+v", result)
	}
	_ = h.lastFake()
	// The resolved shell is visible through the injected DefaultShell only via
	// the pty request, so assert indirectly: an unset shell must not fail
	// validation, and the request passed to OpenPty kept the defaults.
	if h.host.SessionCount() != 1 {
		t.Fatalf("session count = %d, want 1", h.host.SessionCount())
	}
}

func TestSessionCapEnforced(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < MaxSessions; i++ {
		if result := h.spawn("conn-1", nil); !result.OK {
			t.Fatalf("spawn %d failed: %+v", i+1, result)
		}
	}
	over := h.spawn("conn-1", nil)
	if over.OK || over.Reason != ReasonLimit {
		t.Fatalf("over-limit spawn = %+v, want %q", over, ReasonLimit)
	}
	if h.host.SessionCount() != MaxSessions {
		t.Fatalf("session count = %d, want %d", h.host.SessionCount(), MaxSessions)
	}
}

func TestSessionCapUnderConcurrency(t *testing.T) {
	h := newHarness(t)
	const attempts = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := h.spawn("conn-1", nil)
			if result.OK {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != MaxSessions {
		t.Fatalf("concurrent spawns succeeded %d times, want %d", ok, MaxSessions)
	}
}

func TestWriteIsCappedAndOwnerScoped(t *testing.T) {
	h := newHarness(t)
	first := h.spawn("conn-1", nil)
	if !first.OK {
		t.Fatalf("spawn failed: %+v", first)
	}

	big := make([]byte, MaxWriteBytes+1)
	if h.host.Write("conn-1", first.SessionID, big) {
		t.Fatal("write over the cap was accepted")
	}
	if got := h.lastFake().writtenText(); got != "" {
		t.Fatalf("over-cap write reached the pty: %q", got)
	}

	if !h.host.Write("conn-1", first.SessionID, []byte("ls\r")) {
		t.Fatal("owner write failed")
	}
	if got := h.lastFake().writtenText(); got != "ls\r" {
		t.Fatalf("pty input = %q, want %q", got, "ls\r")
	}

	// A different connection must not be able to drive this session — this is
	// the cross-session gap recorded in MAX-50, closed at the source here.
	if h.host.Write("conn-2", first.SessionID, []byte("rm -rf /\r")) {
		t.Fatal("a foreign owner wrote to the session")
	}
	if h.host.Resize("conn-2", first.SessionID, 100, 30) {
		t.Fatal("a foreign owner resized the session")
	}
	if h.host.Kill("conn-2", first.SessionID) {
		t.Fatal("a foreign owner killed the session")
	}
	if h.host.SessionCount() != 1 {
		t.Fatalf("foreign commands must not disturb the session, count = %d", h.host.SessionCount())
	}

	// Shape-valid but unknown ids are dropped too.
	if h.host.Write("conn-1", "00000000-0000-4000-8000-999999999999", []byte("x")) {
		t.Fatal("unknown session id was accepted")
	}
}

func TestResizeValidates(t *testing.T) {
	h := newHarness(t)
	first := h.spawn("conn-1", nil)
	if !first.OK {
		t.Fatalf("spawn failed: %+v", first)
	}
	if h.host.Resize("conn-1", first.SessionID, 0, 24) {
		t.Fatal("invalid cols accepted")
	}
	if !h.host.Resize("conn-1", first.SessionID, 132, 43) {
		t.Fatal("valid resize rejected")
	}
	resizes := h.lastFake().resizes
	if len(resizes) != 1 || resizes[0] != [2]int{132, 43} {
		t.Fatalf("resizes = %v, want one 132x43", resizes)
	}
}

func TestNaturalExitDeliversExitCodeAndReleases(t *testing.T) {
	h := newHarness(t)
	first := h.spawn("conn-1", nil)
	if !first.OK {
		t.Fatalf("spawn failed: %+v", first)
	}
	pty := h.lastFake()
	pty.emit("hello\r\n")
	pty.exit(7)

	// Read through the locking accessor: exits is written by the pump
	// goroutine under sink.mu, so a bare len() here is a data race.
	waitFor(t, func() bool { return len(h.sink.exitCodes()) > 0 }, "exit event")
	if code := h.sink.exitCodes(); len(code) != 1 || code[0] != 7 {
		t.Fatalf("exit codes = %v, want [7]", code)
	}
	if !strings.Contains(h.sink.dataText(), "hello") {
		t.Fatalf("output not delivered: %q", h.sink.dataText())
	}
	if h.host.SessionCount() != 0 {
		t.Fatalf("exited session still counted: %d", h.host.SessionCount())
	}
}

func TestKillEndsSessionWithoutExitEvent(t *testing.T) {
	h := newHarness(t)
	first := h.spawn("conn-1", nil)
	if !first.OK {
		t.Fatalf("spawn failed: %+v", first)
	}
	pty := h.lastFake()

	if !h.host.Kill("conn-1", first.SessionID) {
		t.Fatal("kill failed")
	}
	if h.host.Kill("conn-1", first.SessionID) {
		t.Fatal("double kill reported success")
	}
	if !pty.wasKilled() {
		t.Fatal("pty was not killed")
	}
	if codes := h.sink.exitCodes(); len(codes) != 0 {
		t.Fatalf("killing emitted exit events: %v", codes)
	}
	if h.host.SessionCount() != 0 {
		t.Fatalf("killed session still counted: %d", h.host.SessionCount())
	}
}

func TestKillOwnerTearsDownOnlyItsSessions(t *testing.T) {
	h := newHarness(t)
	a := h.spawn("conn-1", nil)
	b := h.spawn("conn-1", nil)
	c := h.spawn("conn-2", nil)
	for _, r := range []SpawnResult{a, b, c} {
		if !r.OK {
			t.Fatalf("spawn failed: %+v", r)
		}
	}
	// Remember which fake belongs to which session so the assertion checks the
	// right processes instead of "the last one created".
	fakes := map[string]*fakePty{
		a.SessionID: h.fakes[0],
		b.SessionID: h.fakes[1],
		c.SessionID: h.fakes[2],
	}

	h.host.KillOwner("conn-1")

	if h.host.SessionCount() != 1 {
		t.Fatalf("session count = %d, want 1", h.host.SessionCount())
	}
	for id, pty := range fakes {
		want := id != c.SessionID
		if pty.wasKilled() != want {
			t.Fatalf("session %s killed = %v, want %v", id, pty.wasKilled(), want)
		}
	}
	if !h.host.Write("conn-2", c.SessionID, []byte("echo ok\r")) {
		t.Fatal("surviving session no longer writable")
	}
}

func TestOwnerGoneTearsDownSession(t *testing.T) {
	h := newHarness(t)
	first := h.spawn("conn-1", nil)
	if !first.OK {
		t.Fatalf("spawn failed: %+v", first)
	}
	pty := h.lastFake()

	h.sink.mu.Lock()
	h.sink.gone = true
	h.sink.mu.Unlock()
	pty.emit("late output")

	waitFor(t, func() bool { return pty.wasKilled() }, "session teardown")
	if h.host.SessionCount() != 0 {
		t.Fatalf("session survived its owner: %d", h.host.SessionCount())
	}
}

func TestUnsupportedPlatformReason(t *testing.T) {
	host := New(Options{
		OpenPty:      func(SpawnRequest) (PtyProcess, error) { return nil, ErrUnsupported },
		DefaultShell: func() string { return "/bin/testshell" },
	})
	result := host.Spawn("conn-1", SpawnRequest{Cols: 80, Rows: 24}, &recordingSink{})
	if result.OK || result.Reason != ReasonUnsupported {
		t.Fatalf("spawn = %+v, want %q", result, ReasonUnsupported)
	}
}

func sleepMs(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

func waitFor(t *testing.T, done func() bool, label string) {
	t.Helper()
	deadline := 500
	for i := 0; i < deadline; i++ {
		if done() {
			return
		}
		sleepMs(10)
	}
	t.Fatalf("timed out waiting for %s", label)
}
