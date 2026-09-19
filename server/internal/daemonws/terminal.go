package daemonws

import (
	"encoding/json"
	"log/slog"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// TerminalBridge is the daemonws → realtime half of the terminal relay
// (MAX-51 M3). The realtime hub implements it structurally; wiring happens in
// the router. Terminal relay state is in-process per node: terminal.data is
// forwarded directly to the subscribing client and is NOT published through
// the Redis relay, which streams broadcasts (workspace/task/chat scopes), not
// point-to-point sessions. A deployment with multiple API nodes must keep a
// client and its runtime's daemon on the same node (or extend the relay with
// a point-to-point channel) before lifting this limitation.
type TerminalBridge interface {
	// DeliverTerminalFromDaemon forwards one daemon-originated terminal frame
	// (terminal.data / terminal.exit / terminal.error / terminal.open_result)
	// to the single subscriber of the runtime's terminal scope.
	DeliverTerminalFromDaemon(runtimeID string, frame []byte)
	// TerminalRuntimeOffline clears the runtime's terminal scope state after
	// the offline exit frames (if any) have been delivered.
	TerminalRuntimeOffline(runtimeID string)
}

// SetTerminalBridge installs the relay target for daemon→client terminal
// frames. A nil bridge disables terminal forwarding.
func (h *Hub) SetTerminalBridge(b TerminalBridge) {
	if h == nil {
		return
	}
	h.termMu.Lock()
	h.terminalBridge = b
	h.termMu.Unlock()
}

func (h *Hub) terminalBridgeFn() TerminalBridge {
	h.termMu.RLock()
	defer h.termMu.RUnlock()
	return h.terminalBridge
}

// SendTerminalFrame routes one client-originated terminal frame
// (terminal.open / input / resize / kill) to a daemon connection that serves
// runtimeID. terminal.open frames are remembered so the correlated
// terminal.open_result can be routed back to the right runtime. Returns false
// when no daemon connection serves the runtime.
func (h *Hub) SendTerminalFrame(runtimeID string, frame []byte) bool {
	if h == nil || runtimeID == "" {
		return false
	}
	var msg protocol.Message
	if err := json.Unmarshal(frame, &msg); err != nil {
		return false
	}
	if msg.Type == protocol.EventTerminalOpen {
		var p protocol.TerminalOpenPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil || p.ReqID == "" {
			return false
		}
		h.termMu.Lock()
		h.terminalPending[p.ReqID] = runtimeID
		h.termMu.Unlock()
	}
	return h.sendTerminalToRuntime(runtimeID, frame)
}

// PauseTerminal tells the runtime's daemon to stop (or resume) draining pty
// output for the relay's sessions, applying the client send-buffer watermark
// backpressure end to end. Never terminates the daemon connection.
func (h *Hub) PauseTerminal(runtimeID string, paused bool) bool {
	frame, err := json.Marshal(protocol.Message{
		Type: protocol.EventTerminalPause,
		Payload: marshalRawJSON(protocol.TerminalPausePayload{
			Paused: paused,
		}),
	})
	if err != nil {
		return false
	}
	return h.sendTerminalToRuntime(runtimeID, frame)
}

func (h *Hub) sendTerminalToRuntime(runtimeID string, frame []byte) bool {
	h.mu.RLock()
	var target *client
	for c := range h.byRuntime[runtimeID] {
		target = c
		break
	}
	h.mu.RUnlock()
	if target == nil {
		return false
	}
	return target.trySend(frame)
}

// handleTerminalFromDaemon routes one daemon-originated terminal frame to the
// bridge, resolving the owning runtime from the open correlation or the
// session map.
func (h *Hub) handleTerminalFromDaemon(msgType string, payload json.RawMessage, rawFrame []byte) {
	var runtimeID string
	var sessionID string
	switch msgType {
	case protocol.EventTerminalOpenResult:
		var p protocol.TerminalOpenResultPayload
		if json.Unmarshal(payload, &p) != nil {
			return
		}
		h.termMu.Lock()
		runtimeID = h.terminalPending[p.ReqID]
		delete(h.terminalPending, p.ReqID)
		if runtimeID != "" && p.Error == "" && p.SessionID != "" {
			h.trackTerminalSessionLocked(runtimeID, p.SessionID)
		}
		h.termMu.Unlock()
	case protocol.EventTerminalData, protocol.EventTerminalExit:
		var p protocol.TerminalDataPayload
		if json.Unmarshal(payload, &p) != nil {
			return
		}
		sessionID = p.SessionID
		h.termMu.Lock()
		runtimeID = h.terminalSessions[p.SessionID]
		if msgType == protocol.EventTerminalExit && runtimeID != "" {
			h.untrackTerminalSessionLocked(runtimeID, p.SessionID)
		}
		h.termMu.Unlock()
	case protocol.EventTerminalError:
		var p protocol.TerminalErrorPayload
		if json.Unmarshal(payload, &p) != nil {
			return
		}
		h.termMu.Lock()
		if p.SessionID != "" {
			sessionID = p.SessionID
			runtimeID = h.terminalSessions[p.SessionID]
		}
		if runtimeID == "" && p.ReqID != "" {
			runtimeID = h.terminalPending[p.ReqID]
		}
		h.termMu.Unlock()
	default:
		return
	}
	if runtimeID == "" {
		slog.Debug("daemon websocket terminal frame without routing",
			"type", msgType, "session_id", sessionID)
		return
	}
	bridge := h.terminalBridgeFn()
	if bridge == nil {
		return
	}
	bridge.DeliverTerminalFromDaemon(runtimeID, rawFrame)
}

// trackTerminalSessionLocked records sessionID under runtimeID. Caller holds
// termMu.
func (h *Hub) trackTerminalSessionLocked(runtimeID, sessionID string) {
	h.terminalSessions[sessionID] = runtimeID
	if h.terminalRuntimeSessions[runtimeID] == nil {
		h.terminalRuntimeSessions[runtimeID] = make(map[string]bool)
	}
	h.terminalRuntimeSessions[runtimeID][sessionID] = true
}

func (h *Hub) untrackTerminalSessionLocked(runtimeID, sessionID string) {
	delete(h.terminalSessions, sessionID)
	if set := h.terminalRuntimeSessions[runtimeID]; set != nil {
		delete(set, sessionID)
		if len(set) == 0 {
			delete(h.terminalRuntimeSessions, runtimeID)
		}
	}
}

// terminalHandleDisconnect synthesizes terminal.exit frames (reason
// daemon_offline) for every live session of the dropped connection's runtimes
// so subscribers learn their shells died, and clears the relay's routing
// state. Called from unregister.
func (h *Hub) terminalHandleDisconnect(runtimeIDs []string) {
	bridge := h.terminalBridgeFn()
	h.termMu.Lock()
	var exits []terminalOfflineExit
	for _, runtimeID := range runtimeIDs {
		set, ok := h.terminalRuntimeSessions[runtimeID]
		if !ok {
			continue
		}
		for sessionID := range set {
			exits = append(exits, terminalOfflineExit{runtimeID: runtimeID, sessionID: sessionID})
			delete(h.terminalSessions, sessionID)
		}
		delete(h.terminalRuntimeSessions, runtimeID)
	}
	h.termMu.Unlock()
	if bridge == nil {
		return
	}
	for _, e := range exits {
		frame, err := json.Marshal(protocol.Message{
			Type: protocol.EventTerminalExit,
			Payload: marshalRawJSON(protocol.TerminalExitPayload{
				SessionID: e.sessionID,
				Reason:    protocol.TerminalExitReasonDaemonOffline,
			}),
		})
		if err != nil {
			continue
		}
		bridge.DeliverTerminalFromDaemon(e.runtimeID, frame)
	}
	// Always clear the bridge's scope state — a subscriber may hold the scope
	// with zero live sessions.
	for _, runtimeID := range runtimeIDs {
		bridge.TerminalRuntimeOffline(runtimeID)
	}
}

type terminalOfflineExit struct {
	runtimeID string
	sessionID string
}

// marshalRawJSON marshals v, returning null on failure (never happens for the
// payload shapes above).
func marshalRawJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return data
}
