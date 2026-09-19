package daemon

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/termhost"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// fakeTermPty is an in-memory pty for termchannel tests (mirrors the fake in
// internal/termhost).
type fakeTermPty struct {
	mu      sync.Mutex
	written strings.Builder
	resizes [][2]int
	killed  bool
	closed  bool
	output  chan []byte
	exited  chan int
}

func newFakeTermPty() *fakeTermPty {
	return &fakeTermPty{output: make(chan []byte, 8), exited: make(chan int, 1)}
}

func (f *fakeTermPty) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written.Write(p)
	return len(p), nil
}

func (f *fakeTermPty) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, [2]int{cols, rows})
	return nil
}

func (f *fakeTermPty) Kill() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = true
	f.finishLocked(137)
	return nil
}

func (f *fakeTermPty) Wait() int             { return <-f.exited }
func (f *fakeTermPty) Output() <-chan []byte { return f.output }

func (f *fakeTermPty) finishLocked(code int) {
	if f.closed {
		return
	}
	f.closed = true
	close(f.output)
	f.exited <- code
}

func (f *fakeTermPty) emit(text string) { f.output <- []byte(text) }

func (f *fakeTermPty) exit(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finishLocked(code)
}

func (f *fakeTermPty) writtenText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written.String()
}

func (f *fakeTermPty) wasKilled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.killed
}

// termHarness wires a termChannel to a fake pty factory and a captured frame
// sink.
type termHarness struct {
	ch      *termChannel
	pties   map[string]*fakeTermPty
	lastPty *fakeTermPty
	nextID  int
	frames  chan capturedFrame
	mu      sync.Mutex
}

type capturedFrame struct {
	Type    string
	Payload json.RawMessage
}

func newTermHarness(t *testing.T) *termHarness {
	t.Helper()
	h := &termHarness{
		ch:     &termChannel{gates: make(map[string]*pauseGate), closed: make(chan struct{})},
		pties:  make(map[string]*fakeTermPty),
		frames: make(chan capturedFrame, 64),
	}
	h.ch.host = termhost.New(termhost.Options{
		// OpenPty runs before NewID inside Spawn; the pty it created is
		// registered under the id NewID mints, so tests can look sessions up.
		OpenPty: func(req termhost.SpawnRequest) (termhost.PtyProcess, error) {
			h.mu.Lock()
			h.lastPty = newFakeTermPty()
			h.mu.Unlock()
			return h.lastPty, nil
		},
		IsDir:            func(string) bool { return true },
		IsExecutableFile: func(string) bool { return true },
		NewID: func() string {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.nextID++
			id := "sess-" + string(rune('0'+h.nextID))
			h.pties[id] = h.lastPty
			return id
		},
	})
	h.ch.attach(func(frame []byte) bool {
		var msg protocol.Message
		if json.Unmarshal(frame, &msg) != nil {
			t.Logf("unmarshal failed: %s", frame)
			return false
		}
		select {
		case h.frames <- capturedFrame{Type: msg.Type, Payload: msg.Payload}:
		default:
		}
		return true
	})
	return h
}

func (h *termHarness) send(t *testing.T, msgType string, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	h.ch.HandleMessage(msgType, raw)
}

func (h *termHarness) waitFrame(t *testing.T, wantType string) capturedFrame {
	t.Helper()
	select {
	case f := <-h.frames:
		if f.Type != wantType {
			t.Fatalf("frame type = %q, want %q", f.Type, wantType)
		}
		return f
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %q frame", wantType)
		return capturedFrame{}
	}
}

func (h *termHarness) openSession(t *testing.T, cols, rows int) (reqID, sessionID string) {
	t.Helper()
	reqID = "req-1"
	h.send(t, protocol.EventTerminalOpen, protocol.TerminalOpenPayload{
		ReqID: reqID, Cols: cols, Rows: rows,
	})
	var resp protocol.TerminalOpenResultPayload
	if err := json.Unmarshal(h.waitFrame(t, protocol.EventTerminalOpenResult).Payload, &resp); err != nil {
		t.Fatalf("unmarshal open_result: %v", err)
	}
	if resp.ReqID != reqID || resp.Error != "" || resp.SessionID == "" {
		t.Fatalf("open_result = %+v, want req-1 with session and no error", resp)
	}
	return reqID, resp.SessionID
}

// TestTermChannel_HappyPath covers the open → data → resize → exit → kill
// matrix against the daemon-side channel.
func TestTermChannel_HappyPath(t *testing.T) {
	h := newTermHarness(t)

	_, sess := h.openSession(t, 80, 24)
	if h.ch.SessionCount() != 1 {
		t.Fatalf("session count = %d, want 1", h.ch.SessionCount())
	}

	// pty output streams back as terminal.data.
	h.mu.Lock()
	pty := h.pties[sess]
	h.mu.Unlock()
	if pty == nil {
		t.Fatalf("no fake pty for session %q", sess)
	}
	pty.emit("hello")
	var data protocol.TerminalDataPayload
	if err := json.Unmarshal(h.waitFrame(t, protocol.EventTerminalData).Payload, &data); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if data.SessionID != sess || string(data.Data) != "hello" {
		t.Fatalf("data = %+v, want session %q with %q", data, sess, "hello")
	}

	// Input and resize reach the pty.
	h.send(t, protocol.EventTerminalInput, protocol.TerminalInputPayload{SessionID: sess, Data: []byte("ls\n")})
	h.send(t, protocol.EventTerminalResize, protocol.TerminalResizePayload{SessionID: sess, Cols: 100, Rows: 40})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (pty.writtenText() != "ls\n" || len(pty.resizes) == 0) {
		time.Sleep(10 * time.Millisecond)
	}
	if pty.writtenText() != "ls\n" {
		t.Fatalf("pty written = %q, want %q", pty.writtenText(), "ls\n")
	}
	if len(pty.resizes) != 1 || pty.resizes[0] != [2]int{100, 40} {
		t.Fatalf("resizes = %v, want [[100 40]]", pty.resizes)
	}

	// Natural exit surfaces as terminal.exit and clears the session.
	pty.exit(0)
	var exit protocol.TerminalExitPayload
	if err := json.Unmarshal(h.waitFrame(t, protocol.EventTerminalExit).Payload, &exit); err != nil {
		t.Fatalf("unmarshal exit: %v", err)
	}
	if exit.SessionID != sess || exit.Code != 0 {
		t.Fatalf("exit = %+v, want session %q code 0", exit, sess)
	}
	if h.ch.SessionCount() != 0 {
		t.Fatalf("session count after exit = %d, want 0", h.ch.SessionCount())
	}

	// A kill on a live session tears it down without an exit frame.
	_, sess2 := h.openSession(t, 80, 24)
	h.mu.Lock()
	pty2 := h.pties[sess2]
	h.mu.Unlock()
	h.send(t, protocol.EventTerminalKill, protocol.TerminalKillPayload{SessionID: sess2})
	if !pty2.wasKilled() {
		t.Fatal("pty2 was not killed")
	}
	if h.ch.SessionCount() != 0 {
		t.Fatalf("session count after kill = %d, want 0", h.ch.SessionCount())
	}
	select {
	case f := <-h.frames:
		t.Fatalf("unexpected frame after kill: %s", f.Type)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestTermChannel_OpenFailures pins the open validation matrix.
func TestTermChannel_OpenFailures(t *testing.T) {
	cases := []struct {
		name string
		req  protocol.TerminalOpenPayload
	}{
		{"bad size", protocol.TerminalOpenPayload{ReqID: "r1", Cols: 1, Rows: 24}},
		{"limit ok but bad shell", protocol.TerminalOpenPayload{ReqID: "r2", Cols: 80, Rows: 24, Shell: "sh"}},
	}
	h := newTermHarness(t)
	for _, tc := range cases {
		h.send(t, protocol.EventTerminalOpen, tc.req)
		var resp protocol.TerminalOpenResultPayload
		if err := json.Unmarshal(h.waitFrame(t, protocol.EventTerminalOpenResult).Payload, &resp); err != nil {
			t.Fatalf("%s: unmarshal: %v", tc.name, err)
		}
		if resp.Error == "" || resp.SessionID != "" {
			t.Fatalf("%s: resp = %+v, want error without session", tc.name, resp)
		}
		if resp.ReqID != tc.req.ReqID {
			t.Fatalf("%s: req_id = %q, want %q", tc.name, resp.ReqID, tc.req.ReqID)
		}
	}
}

// TestTermChannel_PauseBlocksPtyDrain verifies the flow-control gate: while
// paused, the pty pump stops draining (blocking in Sink.Data); resume
// releases the buffered chunk. This is the daemon end of the client
// backpressure path.
func TestTermChannel_PauseBlocksPtyDrain(t *testing.T) {
	h := newTermHarness(t)
	_, sess := h.openSession(t, 80, 24)
	h.mu.Lock()
	pty := h.pties[sess]
	h.mu.Unlock()

	h.send(t, protocol.EventTerminalPause, protocol.TerminalPausePayload{SessionID: sess, Paused: true})
	pty.emit("blocked")
	select {
	case f := <-h.frames:
		t.Fatalf("frame %s delivered while paused", f.Type)
	case <-time.After(300 * time.Millisecond):
	}

	h.send(t, protocol.EventTerminalPause, protocol.TerminalPausePayload{SessionID: sess, Paused: false})
	var data protocol.TerminalDataPayload
	if err := json.Unmarshal(h.waitFrame(t, protocol.EventTerminalData).Payload, &data); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if string(data.Data) != "blocked" {
		t.Fatalf("data = %q, want %q", data.Data, "blocked")
	}
}

// TestTermChannel_DetachKillsSessions pins the connection-lifecycle contract:
// detaching the WS connection (nil send) kills every session owned by the
// relay so no shell outlives the connection.
func TestTermChannel_DetachKillsSessions(t *testing.T) {
	h := newTermHarness(t)
	_, sess := h.openSession(t, 80, 24)
	h.mu.Lock()
	pty := h.pties[sess]
	h.mu.Unlock()

	h.ch.attach(nil)
	if !pty.wasKilled() {
		t.Fatal("session not killed on detach")
	}
	if h.ch.SessionCount() != 0 {
		t.Fatalf("session count after detach = %d, want 0", h.ch.SessionCount())
	}
	// (The killed pty's output side is closed by Kill, so nothing further can
	// be emitted; the contract under test is that no stale frames appear.)
	select {
	case f := <-h.frames:
		t.Fatalf("frame %s delivered after detach", f.Type)
	case <-time.After(200 * time.Millisecond):
	}
}
