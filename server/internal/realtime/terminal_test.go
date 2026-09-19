package realtime

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// fakeTerminalAuthorizer returns canned decisions per runtime id.
type fakeTerminalAuthorizer struct {
	results map[string]struct {
		ok     bool
		reason string
		err    error
	}
}

func (a fakeTerminalAuthorizer) AuthorizeTerminalScope(_ context.Context, _, _, runtimeID string) (bool, string, error) {
	r, ok := a.results[runtimeID]
	if !ok {
		return true, "", nil
	}
	return r.ok, r.reason, r.err
}

// fakeTerminalRelay records frames sent toward the daemon and pause flips.
type fakeTerminalRelay struct {
	mu     sync.Mutex
	sent   [][]byte
	pauses map[string]bool
}

func newFakeTerminalRelay() *fakeTerminalRelay {
	return &fakeTerminalRelay{pauses: make(map[string]bool)}
}

func (r *fakeTerminalRelay) SendTerminalFrame(runtimeID string, frame []byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, frame)
	return true
}

func (r *fakeTerminalRelay) PauseTerminal(runtimeID string, paused bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pauses[runtimeID] = paused
	return true
}

func (r *fakeTerminalRelay) sentFrames() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.sent...)
}

func (r *fakeTerminalRelay) paused(runtimeID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pauses[runtimeID]
}

const testRuntimeID = "0196e148-0000-7000-8000-000000000001"

func terminalTestHub(t *testing.T, auth TerminalScopeAuthorizer, relay TerminalRelay) (*Hub, *httptest.Server) {
	t.Helper()
	hub, server := newTestHub(t)
	hub.SetTerminalAuthorizer(auth)
	hub.SetTerminalRelay(relay)
	return hub, server
}

// subscribeTerminalOverWS performs the subscribe handshake and returns the
// subscribe_ack / subscribe_error type.
func subscribeTerminalOverWS(t *testing.T, conn *websocket.Conn, runtimeID string) string {
	t.Helper()
	frame, err := json.Marshal(map[string]any{
		"type":    "subscribe",
		"payload": map[string]string{"scope": ScopeTerminal, "id": runtimeID},
	})
	if err != nil {
		t.Fatalf("marshal subscribe: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetReadDeadline(time.Time{})
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read subscribe reply: %v", err)
	}
	var resp struct {
		Type    string `json:"type"`
		Payload struct {
			Error string `json:"error"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	if resp.Type == "subscribe_error" {
		return "subscribe_error:" + resp.Payload.Error
	}
	return resp.Type
}

func TestTerminalWatermarkDecisions(t *testing.T) {
	cases := []struct {
		name    string
		paused  bool
		queued  int
		wantFns string // "pause", "resume", or ""
	}{
		{"below high not paused", false, terminalHighWatermark - 1, ""},
		{"at high pauses", false, terminalHighWatermark, "pause"},
		{"above high still pause only", false, terminalSendBufferCapacity + 10, "pause"},
		{"paused stays paused above low", true, terminalLowWatermark + 1, ""},
		{"paused resumes at low", true, terminalLowWatermark, "resume"},
		{"paused resumes below low", true, 0, "resume"},
		{"not paused never resumes", false, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pause := terminalShouldPause(tc.paused, tc.queued)
			resume := terminalShouldResume(tc.paused, tc.queued)
			switch tc.wantFns {
			case "pause":
				if !pause || resume {
					t.Fatalf("pause=%v resume=%v, want pause only", pause, resume)
				}
			case "resume":
				if !resume || pause {
					t.Fatalf("pause=%v resume=%v, want resume only", pause, resume)
				}
			default:
				if pause || resume {
					t.Fatalf("pause=%v resume=%v, want neither", pause, resume)
				}
			}
		})
	}
}

// TestTerminalSubscribeAuthorizationMatrix covers owner-only, capability and
// occupancy enforcement at the subscribe boundary.
func TestTerminalSubscribeAuthorizationMatrix(t *testing.T) {
	relay := newFakeTerminalRelay()
	auth := fakeTerminalAuthorizer{results: map[string]struct {
		ok     bool
		reason string
		err    error
	}{
		testRuntimeID: {ok: true},
		"rt-second":   {ok: false, reason: "forbidden"},
		"rt-cap":      {ok: false, reason: "capability_missing"},
		"rt-err":      {ok: false, reason: "", err: context.DeadlineExceeded},
	}}
	_, server := terminalTestHub(t, auth, relay)

	cases := []struct {
		name      string
		runtimeID string
		want      string // subscribe_ack or the subscribe_error reason
	}{
		{"owner allowed", testRuntimeID, "subscribe_ack"},
		{"non-owner rejected", "rt-second", "subscribe_error:forbidden"},
		{"capability missing", "rt-cap", "subscribe_error:capability_missing"},
		{"lookup failure", "rt-err", "subscribe_error:lookup_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := connectWS(t, server)
			got := subscribeTerminalOverWS(t, conn, tc.runtimeID)
			if tc.want == "subscribe_ack" {
				if got != "subscribe_ack" {
					t.Fatalf("reply = %q, want subscribe_ack", got)
				}
				return
			}
			if !strings.HasPrefix(got, "subscribe_error") {
				t.Fatalf("reply = %q, want subscribe_error", got)
			}
		})
	}

	// The successful subscription occupies the scope: a second concurrent
	// subscriber is rejected with "in use".
	conn2 := connectWS(t, server)
	got := subscribeTerminalOverWS(t, conn2, testRuntimeID)
	if !strings.Contains(got, "in use") {
		t.Fatalf("second subscriber reply = %q, want in use", got)
	}
}

// TestTerminalClientFrameForwarding covers runtime resolution, subscription
// validation and owner stamping on client → daemon frames.
func TestTerminalClientFrameForwarding(t *testing.T) {
	relay := newFakeTerminalRelay()
	_, server := terminalTestHub(t, fakeTerminalAuthorizer{}, relay)
	conn := connectWS(t, server)
	if got := subscribeTerminalOverWS(t, conn, testRuntimeID); got != "subscribe_ack" {
		t.Fatalf("subscribe reply = %q", got)
	}

	sendClientFrame := func(t *testing.T, frameType string, payload map[string]any) {
		t.Helper()
		frame, err := json.Marshal(map[string]any{"type": frameType, "payload": payload})
		if err != nil {
			t.Fatalf("marshal frame: %v", err)
		}
		if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
			t.Fatalf("write frame: %v", err)
		}
	}

	// open without runtime_id resolves the sole scope and stamps owner/req.
	sendClientFrame(t, protocol.EventTerminalOpen, map[string]any{"cols": 80, "rows": 24})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(relay.sentFrames()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	sent := relay.sentFrames()
	if len(sent) != 1 {
		t.Fatalf("relay frames = %d, want 1", len(sent))
	}
	var msg protocol.Message
	if err := json.Unmarshal(sent[0], &msg); err != nil {
		t.Fatalf("unmarshal relayed frame: %v", err)
	}
	if msg.Type != protocol.EventTerminalOpen {
		t.Fatalf("relayed type = %q", msg.Type)
	}
	var open protocol.TerminalOpenPayload
	if err := json.Unmarshal(msg.Payload, &open); err != nil {
		t.Fatalf("unmarshal open payload: %v", err)
	}
	if open.RuntimeID != testRuntimeID || open.OwnerID != testUserID || open.ReqID == "" {
		t.Fatalf("open = %+v, want runtime/owner/req stamped", open)
	}

	// input with an explicit runtime_id forwards.
	sendClientFrame(t, protocol.EventTerminalInput, map[string]any{
		"session_id": "s1", "runtime_id": testRuntimeID,
		"data": []byte("ls"),
	})
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(relay.sentFrames()) == 1 {
		time.Sleep(10 * time.Millisecond)
	}
	sent = relay.sentFrames()
	if len(sent) != 2 {
		t.Fatalf("relay frames = %d, want 2", len(sent))
	}

	// A non-subscriber cannot drive the scope.
	conn2 := connectWS(t, server)
	before := len(relay.sentFrames())
	sendFromConn2 := func() {
		frame, err := json.Marshal(map[string]any{
			"type":    protocol.EventTerminalInput,
			"payload": map[string]any{"session_id": "s1", "data": []byte("x")},
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := conn2.WriteMessage(websocket.TextMessage, frame); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	sendFromConn2()
	conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn2.ReadMessage()
	if err != nil {
		t.Fatalf("read error frame: %v", err)
	}
	if !strings.Contains(string(raw), "not_subscribed") {
		t.Fatalf("reply = %s, want not_subscribed error", raw)
	}
	if len(relay.sentFrames()) != before {
		t.Fatal("non-subscriber frame was forwarded")
	}
}

// TestTerminalUnsubscribeKillsSessions covers kill-on-unsubscribe: sessions
// the client opened are killed toward the daemon when the scope is released.
func TestTerminalUnsubscribeKillsSessions(t *testing.T) {
	relay := newFakeTerminalRelay()
	hub, server := terminalTestHub(t, fakeTerminalAuthorizer{}, relay)
	conn := connectWS(t, server)
	if got := subscribeTerminalOverWS(t, conn, testRuntimeID); got != "subscribe_ack" {
		t.Fatalf("subscribe reply = %q", got)
	}

	// The daemon confirms an open: the ledger records the session.
	hub.DeliverTerminalFromDaemon(testRuntimeID, marshalMessage(protocol.EventTerminalOpenResult,
		protocol.TerminalOpenResultPayload{ReqID: "r", SessionID: "sess-9"}))
	waitForCond(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return hub.terminalSubs[testRuntimeID].terminalSessions[testRuntimeID]["sess-9"]
	})

	// Unsubscribe kills the session toward the daemon.
	frame, err := json.Marshal(map[string]any{
		"type":    "unsubscribe",
		"payload": map[string]string{"scope": ScopeTerminal, "id": testRuntimeID},
	})
	if err != nil {
		t.Fatalf("marshal unsubscribe: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
		t.Fatalf("write unsubscribe: %v", err)
	}
	waitForCond(t, func() bool { return len(relay.sentFrames()) == 1 })
	sent := relay.sentFrames()
	var msg protocol.Message
	if err := json.Unmarshal(sent[0], &msg); err != nil {
		t.Fatalf("unmarshal kill: %v", err)
	}
	if msg.Type != protocol.EventTerminalKill {
		t.Fatalf("relayed type = %q, want terminal.kill", msg.Type)
	}
	var kill protocol.TerminalKillPayload
	if err := json.Unmarshal(msg.Payload, &kill); err != nil {
		t.Fatalf("unmarshal kill payload: %v", err)
	}
	if kill.SessionID != "sess-9" || kill.RuntimeID != testRuntimeID {
		t.Fatalf("kill = %+v, want sess-9 on %s", kill, testRuntimeID)
	}
}

// TestTerminalClientDisconnectKillsSessions covers kill-on-disconnect.
func TestTerminalClientDisconnectKillsSessions(t *testing.T) {
	relay := newFakeTerminalRelay()
	hub, server := terminalTestHub(t, fakeTerminalAuthorizer{}, relay)
	conn := connectWS(t, server)
	if got := subscribeTerminalOverWS(t, conn, testRuntimeID); got != "subscribe_ack" {
		t.Fatalf("subscribe reply = %q", got)
	}
	hub.DeliverTerminalFromDaemon(testRuntimeID, marshalMessage(protocol.EventTerminalOpenResult,
		protocol.TerminalOpenResultPayload{ReqID: "r", SessionID: "sess-1"}))
	waitForCond(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		sub := hub.terminalSubs[testRuntimeID]
		return sub != nil && sub.terminalSessions[testRuntimeID]["sess-1"]
	})

	conn.Close()
	waitForCond(t, func() bool { return len(relay.sentFrames()) == 1 })
	var msg protocol.Message
	if err := json.Unmarshal(relay.sentFrames()[0], &msg); err != nil {
		t.Fatalf("unmarshal kill: %v", err)
	}
	if msg.Type != protocol.EventTerminalKill {
		t.Fatalf("type = %q, want terminal.kill", msg.Type)
	}
	waitForCond(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return hub.terminalSubs[testRuntimeID] == nil
	})
}

// TestTerminalDeliverySlowClientPauseResume drives the watermark end to end:
// a stalled subscriber's buffer crossing the high watermark pauses the
// daemon pty drain (PauseTerminal(true)) without evicting the client, and
// draining below the low watermark resumes it.
func TestTerminalDeliverySlowClientPauseResume(t *testing.T) {
	relay := newFakeTerminalRelay()
	hub := NewHub()
	hub.SetTerminalRelay(relay)
	hub.SetTerminalAuthorizer(fakeTerminalAuthorizer{})

	// A client with no writePump draining its send channel: the stalled
	// reader of the watermark test.
	stalled := &Client{hub: hub, send: make(chan []byte, terminalSendBufferCapacity), userID: testUserID}
	hub.mu.Lock()
	hub.clients[stalled] = true
	stalled.subscriptions = map[scopeKey]bool{}
	hub.mu.Unlock()
	if reason := hub.subscribeTerminal(stalled, testRuntimeID); reason != "" {
		t.Fatalf("subscribeTerminal = %q, want success", reason)
	}

	dataFrame := marshalMessage(protocol.EventTerminalData, protocol.TerminalDataPayload{
		SessionID: "s1", Data: []byte("x"),
	})
	for i := 0; i < terminalHighWatermark; i++ {
		hub.DeliverTerminalFromDaemon(testRuntimeID, dataFrame)
	}
	if !relay.paused(testRuntimeID) {
		t.Fatal("scope not paused at high watermark")
	}
	// The client is NOT evicted.
	hub.mu.RLock()
	stillConnected := hub.clients[stalled]
	hub.mu.RUnlock()
	if !stillConnected {
		t.Fatal("slow terminal client was evicted")
	}
	// While paused, data frames are dropped instead of growing the buffer.
	for i := 0; i < 40; i++ {
		hub.DeliverTerminalFromDaemon(testRuntimeID, dataFrame)
	}
	if len(stalled.send) > terminalSendBufferCapacity {
		t.Fatalf("send buffer = %d, over capacity", len(stalled.send))
	}

	// Drain below the low watermark, then one more delivery flips the gate.
	for len(stalled.send) >= terminalLowWatermark {
		<-stalled.send
	}
	hub.DeliverTerminalFromDaemon(testRuntimeID, dataFrame)
	waitForCond(t, func() bool { return !relay.paused(testRuntimeID) })
}

// TestTerminalRuntimeOfflineClearsScope pins that a daemon drop releases the
// scope state so frames stop flowing and the runtime can be re-subscribed.
func TestTerminalRuntimeOfflineClearsScope(t *testing.T) {
	relay := newFakeTerminalRelay()
	hub := NewHub()
	hub.SetTerminalRelay(relay)
	hub.SetTerminalAuthorizer(fakeTerminalAuthorizer{})
	c := &Client{hub: hub, send: make(chan []byte, 16), userID: testUserID}
	hub.mu.Lock()
	hub.clients[c] = true
	c.subscriptions = map[scopeKey]bool{}
	hub.mu.Unlock()
	if reason := hub.subscribeTerminal(c, testRuntimeID); reason != "" {
		t.Fatalf("subscribeTerminal = %q", reason)
	}
	hub.TerminalRuntimeOffline(testRuntimeID)
	hub.DeliverTerminalFromDaemon(testRuntimeID, marshalMessage(protocol.EventTerminalData,
		protocol.TerminalDataPayload{SessionID: "s", Data: []byte("x")}))
	select {
	case <-c.send:
		t.Fatal("frame delivered after runtime offline")
	default:
	}
}

func waitForCond(t *testing.T, cond func() bool) {
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
