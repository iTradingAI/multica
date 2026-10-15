package daemonws

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func filesDaemonClient(h *Hub, caps string, runtimes ...string) *client {
	leases := map[string]*RuntimeLease{}
	active := map[string]struct{}{}
	for _, r := range runtimes {
		leases[r] = NewRuntimeLease("workspace", "online", time.Now(), true)
		active[r] = struct{}{}
	}
	c := &client{hub: h, identity: ClientIdentity{DaemonID: "daemon", WorkspaceID: "workspace", Capabilities: caps, RuntimeIDs: runtimes, RuntimeLeases: leases}, runtimes: active, send: make(chan []byte, 256)}
	h.register(c)
	return c
}
func TestWorkspaceFilesCapabilityScopeMatrix(t *testing.T) {
	for _, caps := range []string{"", "workspace-files-v1", "WORKSPACE-FILES-V2", "workspace-files-v20", "prefixworkspace-files-v2", "workspace-files-v2 extra"} {
		t.Run(caps, func(t *testing.T) {
			h := NewHub()
			c := filesDaemonClient(h, caps, "runtime")
			defer h.unregister(c)
			target, code := h.SelectWorkspaceFilesTarget("daemon", "workspace", "id")
			if target != nil || code != "daemon_upgrade_required" {
				t.Fatalf("got %v %s", target, code)
			}
		})
	}
	for _, caps := range []string{"workspace-files-v2", "workspace-files-v1, workspace-files-v2 ,other"} {
		t.Run(caps, func(t *testing.T) {
			h := NewHub()
			c := filesDaemonClient(h, caps, "runtime")
			defer h.unregister(c)
			target, code := h.SelectWorkspaceFilesTarget("daemon", "workspace", "id")
			if code != "" || !h.WorkspaceFilesTargetOnline(target) {
				t.Fatal(code)
			}
		})
	}
	for _, kind := range []string{"offline", "wrong daemon", "wrong workspace", "absent runtime", "absent lease", "foreign lease", "removed runtime", "unregistered"} {
		t.Run(kind, func(t *testing.T) {
			h := NewHub()
			c := filesDaemonClient(h, "workspace-files-v2", "runtime")
			defer h.unregister(c)
			daemon, ws := "daemon", "workspace"
			switch kind {
			case "offline", "unregistered":
				h.unregister(c)
			case "wrong daemon":
				daemon = "other"
			case "wrong workspace":
				ws = "other"
			case "absent runtime":
				c.removeRuntime("runtime")
			case "absent lease":
				delete(c.identity.RuntimeLeases, "runtime")
			case "foreign lease":
				c.identity.RuntimeLeases["runtime"] = NewRuntimeLease("other", "online", time.Now(), true)
			case "removed runtime":
				h.invalidateRuntime("runtime", []byte(`{}`), "")
			}
			target, code := h.SelectWorkspaceFilesTarget(daemon, ws, "id")
			if target != nil || code != "daemon_offline" {
				t.Fatalf("scope got %v %s", target, code)
			}
		})
	}
}
func TestWorkspaceFilesSelectionBindingReconnectAndExhaustion(t *testing.T) {
	h := NewHub()
	a := filesDaemonClient(h, "workspace-files-v2", "z", "b")
	b := filesDaemonClient(h, "workspace-files-v2", "b")
	c := filesDaemonClient(h, "workspace-files-v1", "a")
	defer h.unregister(a)
	defer h.unregister(b)
	defer h.unregister(c)
	target, code := h.SelectWorkspaceFilesTarget("daemon", "workspace", "same-id")
	if code != "" || target.RuntimeID != "b" || target.Connection.Identity != a.filesIdentity || target.Generation.RelaySeq != 1 {
		t.Fatal("deterministic min runtime then epoch")
	}
	next, _ := h.SelectWorkspaceFilesTarget("daemon", "workspace", "same-id")
	if next.Generation == target.Generation {
		t.Fatal("generation reused")
	}
	if !h.SendWorkspaceFilesFrame(target, []byte("safe")) {
		t.Fatal("send")
	}
	if string(<-a.send) != "safe" {
		t.Fatal("wrong target")
	}
	h.unregister(a)
	if h.WorkspaceFilesTargetOnline(target) || h.SendWorkspaceFilesFrame(target, []byte("late")) {
		t.Fatal("target reselected after disconnect")
	}
	replacement, _ := h.SelectWorkspaceFilesTarget("daemon", "workspace", "same-id")
	if replacement.Connection.Identity != b.filesIdentity || replacement.Generation.ConnectionEpoch == target.Generation.ConnectionEpoch {
		t.Fatal("reconnect identity")
	}
	h.mu.Lock()
	b.filesSeq = math.MaxUint64 - 1
	h.mu.Unlock()
	if _, code = h.SelectWorkspaceFilesTarget("daemon", "workspace", "id"); code != "unavailable" {
		t.Fatal("sequence wrapped")
	}
	h.unregister(b)
	h.unregister(c)
	h.filesEpoch = math.MaxUint64 - 1
	exhausted := filesDaemonClient(h, "workspace-files-v2", "runtime")
	defer h.unregister(exhausted)
	if _, code = h.SelectWorkspaceFilesTarget("daemon", "workspace", "id"); code != "unavailable" {
		t.Fatal("epoch wrapped")
	}
}

type filesBridgeFrame struct {
	source protocol.WorkspaceFilesConnection
	event  string
	raw    json.RawMessage
}
type filesBridgeSpy struct {
	frames  chan filesBridgeFrame
	mu      sync.Mutex
	offline []filesBridgeFrame
}

func (b *filesBridgeSpy) DeliverWorkspaceFilesFromDaemon(source protocol.WorkspaceFilesConnection, event string, raw json.RawMessage) {
	b.frames <- filesBridgeFrame{source, event, raw}
}
func (b *filesBridgeSpy) WorkspaceFilesConnectionOffline(source protocol.WorkspaceFilesConnection, runtime string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.offline = append(b.offline, filesBridgeFrame{source: source, event: runtime})
}
func TestWorkspaceFilesSourceBridgeAndRuntimeRemoval(t *testing.T) {
	h := NewHub()
	spy := &filesBridgeSpy{frames: make(chan filesBridgeFrame, 8)}
	h.SetWorkspaceFilesBridge(spy)
	c := filesDaemonClient(h, "workspace-files-v2", "runtime")
	target, _ := h.SelectWorkspaceFilesTarget("daemon", "workspace", "id")
	for _, event := range []string{protocol.EventWorkspaceFilesListResult, protocol.EventWorkspaceFilesReadChunk, protocol.EventWorkspaceFilesError} {
		raw, _ := json.Marshal(protocol.Message{Type: event, Payload: json.RawMessage(`{"connection_epoch":999}`)})
		c.handleFrame(raw)
		f := <-spy.frames
		if !f.source.Equal(target.Connection) || f.event != event {
			t.Fatal("untrusted payload supplied connection source")
		}
	}
	h.invalidateRuntime("runtime", []byte(`{}`), "")
	if h.WorkspaceFilesTargetOnline(target) {
		t.Fatal("runtime retained")
	}
	h.unregister(c)
	h.unregister(c)
	h.handleWorkspaceFilesFromDaemon(c, protocol.EventWorkspaceFilesError, json.RawMessage(`{}`))
	if len(spy.frames) != 0 {
		t.Fatal("unregistered source delivered")
	}
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.offline) != 2 || spy.offline[0].event != "runtime" || !spy.offline[1].source.Equal(target.Connection) {
		t.Fatal("offline source hooks")
	}
}
func TestWorkspaceFilesWebSocketWireBridge(t *testing.T) {
	h := NewHub()
	spy := &filesBridgeSpy{frames: make(chan filesBridgeFrame, 8)}
	h.SetWorkspaceFilesBridge(spy)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.HandleWebSocket(w, r, ClientIdentity{DaemonID: "daemon", WorkspaceID: "workspace", Capabilities: "workspace-files-v2", RuntimeIDs: []string{"runtime"}, RuntimeLeases: map[string]*RuntimeLease{"runtime": NewRuntimeLease("workspace", "online", time.Now(), true)}})
	}))
	defer server.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	// Wait for registration through an observable foreground event.
	raw, _ := json.Marshal(protocol.Message{Type: protocol.EventWorkspaceFilesReadChunk, Payload: json.RawMessage(`{}`)})
	if err = ws.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatal(err)
	}
	select {
	case f := <-spy.frames:
		if f.source.Epoch == 0 || f.source.Identity == nil {
			t.Fatal("missing authenticated source")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bridge timeout")
	}
	target, code := h.SelectWorkspaceFilesTarget("daemon", "workspace", "id")
	if code != "" {
		t.Fatal(code)
	}
	frame, _ := json.Marshal(protocol.Message{Type: protocol.EventWorkspaceFilesRead, Payload: json.RawMessage(`{"path":"file.txt"}`)})
	if !h.SendWorkspaceFilesFrame(target, frame) {
		t.Fatal("wire send")
	}
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, got, err := ws.ReadMessage()
	if err != nil || string(got) != string(frame) {
		t.Fatal("daemon wire mismatch", err)
	}
}
