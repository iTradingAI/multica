package daemonws

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// fakeTerminalBridge records the frames and offline notifications a hub
// relays to the realtime hub.
type fakeTerminalBridge struct {
	mu      sync.Mutex
	frames  map[string][][]byte // runtimeID -> frames
	offline []string
}

func newFakeTerminalBridge() *fakeTerminalBridge {
	return &fakeTerminalBridge{frames: make(map[string][][]byte)}
}

func (b *fakeTerminalBridge) DeliverTerminalFromDaemon(runtimeID string, frame []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.frames[runtimeID] = append(b.frames[runtimeID], append([]byte(nil), frame...))
}

func (b *fakeTerminalBridge) TerminalRuntimeOffline(runtimeID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.offline = append(b.offline, runtimeID)
}

func (b *fakeTerminalBridge) framesFor(runtimeID string) [][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.frames[runtimeID]
}

// readDaemonTerminalFrame reads one protocol.Message from the daemon-side
// test connection.
func readDaemonTerminalFrame(t *testing.T, conn *websocket.Conn) protocol.Message {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetReadDeadline(time.Time{})
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("daemon conn read: %v", err)
	}
	var msg protocol.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	return msg
}

// TestTerminalRelay_HappyPath drives open → data → resize → exit → kill
// through the server side of the daemon WS: client frames route to the
// daemon connection, daemon frames route back to the bridge keyed by the
// runtime resolved from the open correlation and the session ledger.
func TestTerminalRelay_HappyPath(t *testing.T) {
	hub := NewHub()
	bridge := newFakeTerminalBridge()
	hub.SetTerminalBridge(bridge)
	conn := dialRPCTestConn(t, hub, ClientIdentity{DaemonID: "daemon-1", RuntimeIDs: []string{"rt-1"}})

	// open: relay → daemon.
	openFrame, err := json.Marshal(protocol.Message{
		Type: protocol.EventTerminalOpen,
		Payload: marshalRawJSON(protocol.TerminalOpenPayload{
			ReqID: "req-1", Cols: 80, Rows: 24,
		}),
	})
	if err != nil {
		t.Fatalf("marshal open: %v", err)
	}
	if !hub.SendTerminalFrame("rt-1", openFrame) {
		t.Fatal("SendTerminalFrame(open) = false, want true")
	}
	got := readDaemonTerminalFrame(t, conn)
	if got.Type != protocol.EventTerminalOpen {
		t.Fatalf("daemon got %q, want terminal.open", got.Type)
	}

	// open_result: daemon → bridge, and the session gets tracked.
	resultFrame, err := json.Marshal(protocol.Message{
		Type: protocol.EventTerminalOpenResult,
		Payload: marshalRawJSON(protocol.TerminalOpenResultPayload{
			ReqID: "req-1", SessionID: "sess-1",
		}),
	})
	if err != nil {
		t.Fatalf("marshal open_result: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, resultFrame); err != nil {
		t.Fatalf("write open_result: %v", err)
	}
	waitFor(t, func() bool { return len(bridge.framesFor("rt-1")) == 1 })

	// data: routed by the session ledger.
	dataFrame, err := json.Marshal(protocol.Message{
		Type: protocol.EventTerminalData,
		Payload: marshalRawJSON(protocol.TerminalDataPayload{
			SessionID: "sess-1", Data: []byte("hi"),
		}),
	})
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, dataFrame); err != nil {
		t.Fatalf("write data: %v", err)
	}
	waitFor(t, func() bool { return len(bridge.framesFor("rt-1")) == 2 })

	// resize from the relay reaches the daemon.
	resizeFrame, err := json.Marshal(protocol.Message{
		Type: protocol.EventTerminalResize,
		Payload: marshalRawJSON(protocol.TerminalResizePayload{
			SessionID: "sess-1", Cols: 100, Rows: 40,
		}),
	})
	if err != nil {
		t.Fatalf("marshal resize: %v", err)
	}
	if !hub.SendTerminalFrame("rt-1", resizeFrame) {
		t.Fatal("SendTerminalFrame(resize) = false, want true")
	}
	got = readDaemonTerminalFrame(t, conn)
	if got.Type != protocol.EventTerminalResize {
		t.Fatalf("daemon got %q, want terminal.resize", got.Type)
	}

	// exit clears the session ledger; the frame still reaches the bridge.
	exitFrame, err := json.Marshal(protocol.Message{
		Type: protocol.EventTerminalExit,
		Payload: marshalRawJSON(protocol.TerminalExitPayload{
			SessionID: "sess-1", Code: 0,
		}),
	})
	if err != nil {
		t.Fatalf("marshal exit: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, exitFrame); err != nil {
		t.Fatalf("write exit: %v", err)
	}
	waitFor(t, func() bool { return len(bridge.framesFor("rt-1")) == 3 })

	// With the ledger cleared, a data frame for the dead session cannot be
	// routed (no runtime resolution) and the bridge sees nothing new.
	if err := conn.WriteMessage(websocket.TextMessage, dataFrame); err != nil {
		t.Fatalf("write stale data: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(bridge.framesFor("rt-1")); n != 3 {
		t.Fatalf("bridge frames after stale data = %d, want 3", n)
	}
}

// TestTerminalRelay_DaemonDisconnectNotifiesSubscriber pins the offline
// contract: when the daemon connection drops, every live session's
// subscriber gets a synthesized terminal.exit with reason daemon_offline and
// the bridge's scope state is cleared.
func TestTerminalRelay_DaemonDisconnectNotifiesSubscriber(t *testing.T) {
	hub := NewHub()
	bridge := newFakeTerminalBridge()
	hub.SetTerminalBridge(bridge)
	conn := dialRPCTestConn(t, hub, ClientIdentity{DaemonID: "daemon-1", RuntimeIDs: []string{"rt-1"}})

	openFrame, err := json.Marshal(protocol.Message{
		Type: protocol.EventTerminalOpen,
		Payload: marshalRawJSON(protocol.TerminalOpenPayload{
			ReqID: "req-1", Cols: 80, Rows: 24,
		}),
	})
	if err != nil {
		t.Fatalf("marshal open: %v", err)
	}
	if !hub.SendTerminalFrame("rt-1", openFrame) {
		t.Fatal("SendTerminalFrame(open) = false, want true")
	}
	readDaemonTerminalFrame(t, conn)

	resultFrame, err := json.Marshal(protocol.Message{
		Type: protocol.EventTerminalOpenResult,
		Payload: marshalRawJSON(protocol.TerminalOpenResultPayload{
			ReqID: "req-1", SessionID: "sess-1",
		}),
	})
	if err != nil {
		t.Fatalf("marshal open_result: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, resultFrame); err != nil {
		t.Fatalf("write open_result: %v", err)
	}
	waitFor(t, func() bool { return len(bridge.framesFor("rt-1")) == 1 })

	// Drop the daemon connection.
	conn.Close()
	waitFor(t, func() bool {
		bridge.mu.Lock()
		defer bridge.mu.Unlock()
		return len(bridge.offline) > 0
	})
	frames := bridge.framesFor("rt-1")
	if len(frames) != 2 {
		t.Fatalf("bridge frames = %d, want 2 (open_result + synthesized exit)", len(frames))
	}
	var msg protocol.Message
	if err := json.Unmarshal(frames[1], &msg); err != nil {
		t.Fatalf("unmarshal synthesized exit: %v", err)
	}
	if msg.Type != protocol.EventTerminalExit {
		t.Fatalf("synthesized frame type = %q, want terminal.exit", msg.Type)
	}
	var exit protocol.TerminalExitPayload
	if err := json.Unmarshal(msg.Payload, &exit); err != nil {
		t.Fatalf("unmarshal exit payload: %v", err)
	}
	if exit.SessionID != "sess-1" || exit.Reason != protocol.TerminalExitReasonDaemonOffline {
		t.Fatalf("exit = %+v, want sess-1/daemon_offline", exit)
	}

	// Routing state is cleared: sending to the runtime fails now.
	if hub.SendTerminalFrame("rt-1", openFrame) {
		t.Fatal("SendTerminalFrame after disconnect = true, want false")
	}
}

// TestTerminalRelay_NoDaemonForRuntime covers the miss path.
func TestTerminalRelay_NoDaemonForRuntime(t *testing.T) {
	hub := NewHub()
	frame, err := json.Marshal(protocol.Message{
		Type:    protocol.EventTerminalInput,
		Payload: marshalRawJSON(protocol.TerminalInputPayload{SessionID: "s", Data: []byte("x")}),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if hub.SendTerminalFrame("rt-missing", frame) {
		t.Fatal("SendTerminalFrame for unknown runtime = true, want false")
	}
}

// TestTerminalRelay_PauseTerminal covers the watermark control frame reaching
// the daemon connection.
func TestTerminalRelay_PauseTerminal(t *testing.T) {
	hub := NewHub()
	hub.SetTerminalBridge(newFakeTerminalBridge())
	conn := dialRPCTestConn(t, hub, ClientIdentity{DaemonID: "daemon-1", RuntimeIDs: []string{"rt-1"}})
	if !hub.PauseTerminal("rt-1", true) {
		t.Fatal("PauseTerminal = false, want true")
	}
	got := readDaemonTerminalFrame(t, conn)
	if got.Type != protocol.EventTerminalPause {
		t.Fatalf("daemon got %q, want terminal.pause", got.Type)
	}
	var p protocol.TerminalPausePayload
	if err := json.Unmarshal(got.Payload, &p); err != nil || !p.Paused {
		t.Fatalf("pause payload = %+v err=%v, want paused=true", p, err)
	}
	if strings.TrimSpace(string(got.Payload)) == "" {
		t.Fatal("empty payload")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached within deadline")
}
