package realtime

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Terminal watermark thresholds, applied against the subscriber's send
// channel length (capacity 256). Crossing the high watermark pauses the
// daemon's pty drain for the scope; dropping back to the low watermark
// resumes it. The subscriber is never evicted for terminal backpressure.
const (
	terminalSendBufferCapacity = 256
	terminalHighWatermark      = 192
	terminalLowWatermark       = 64
)

// terminalShouldPause / terminalShouldResume are the pure watermark decisions
// (unit-tested in isolation).
func terminalShouldPause(paused bool, queued int) bool {
	return !paused && queued >= terminalHighWatermark
}

func terminalShouldResume(paused bool, queued int) bool {
	return paused && queued <= terminalLowWatermark
}

// TerminalScopeAuthorizer decides whether a user may open the terminal relay
// for a runtime. Implementations enforce (in this order): the runtime exists
// in the caller's workspace, the caller IS the runtime owner (owner-only for
// MVP), and the runtime declared the terminal-v1 capability. reason is the
// subscribe_error payload on denial ("forbidden", "not_found",
// "capability_missing", "lookup_failed", ...).
type TerminalScopeAuthorizer interface {
	AuthorizeTerminalScope(ctx context.Context, userID, workspaceID, runtimeID string) (authorized bool, reason string, err error)
}

// TerminalRelay forwards terminal frames between the hub and the runtime's
// daemon connection. Implemented by the daemonws hub structurally; wiring
// happens in the router.
type TerminalRelay interface {
	// SendTerminalFrame routes a client-originated frame to the daemon.
	SendTerminalFrame(runtimeID string, frame []byte) bool
	// PauseTerminal applies client backpressure by pausing/resuming the
	// daemon's pty drain for the scope. It must never terminate the daemon
	// connection.
	PauseTerminal(runtimeID string, paused bool) bool
}

// SetTerminalAuthorizer wires the terminal scope authorizer. Nil (default)
// disables terminal subscriptions.
func (h *Hub) SetTerminalAuthorizer(a TerminalScopeAuthorizer) {
	h.mu.Lock()
	h.terminalAuthorizer = a
	h.mu.Unlock()
}

// SetTerminalRelay wires the daemon-facing relay. Nil disables forwarding.
func (h *Hub) SetTerminalRelay(r TerminalRelay) {
	h.mu.Lock()
	h.terminalRelay = r
	h.mu.Unlock()
}

func (h *Hub) terminalRelayFn() TerminalRelay {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.terminalRelay
}

// subscribeTerminal enforces the exclusive-subscriber rule and joins the
// scope room. Returns the subscribe_error reason, or "" on success.
func (h *Hub) subscribeTerminal(c *Client, runtimeID string) string {
	relay := h.terminalRelayFn()
	if relay == nil {
		return "unavailable"
	}
	auth := func() TerminalScopeAuthorizer {
		h.mu.RLock()
		defer h.mu.RUnlock()
		return h.terminalAuthorizer
	}()
	if auth == nil {
		return "unavailable"
	}
	ok, reason, err := auth.AuthorizeTerminalScope(context.Background(), c.userID, c.workspaceID, runtimeID)
	if err != nil {
		slog.Warn("terminal scope authorize failed", "runtime_id", runtimeID, "user_id", c.userID, "error", err)
		return "lookup_failed"
	}
	if !ok {
		if reason == "" {
			reason = "forbidden"
		}
		return reason
	}

	// terminalOpMu serializes occupancy + teardown transitions so exactly one
	// concurrent subscriber wins the scope.
	h.terminalOpMu.Lock()
	defer h.terminalOpMu.Unlock()

	h.mu.Lock()
	if existing := h.terminalSubs[runtimeID]; existing != nil && existing != c {
		h.mu.Unlock()
		return "in use"
	}
	if c.terminalSessions == nil {
		c.terminalSessions = map[string]map[string]bool{}
	}
	c.terminalSessions[runtimeID] = make(map[string]bool)
	h.terminalSubs[runtimeID] = c
	delete(h.terminalPaused, runtimeID)
	h.mu.Unlock()

	h.subscribe(c, ScopeTerminal, runtimeID)
	return ""
}

// unsubscribeTerminal releases the scope, killing every session the client
// opened so none leak past the unsubscribe.
func (h *Hub) unsubscribeTerminal(c *Client, runtimeID string) {
	h.terminalOpMu.Lock()
	defer h.terminalOpMu.Unlock()
	h.terminalReleaseLocked(c, runtimeID)
	h.unsubscribe(c, ScopeTerminal, runtimeID)
}

// terminalReleaseLocked kills the client's sessions on runtimeID and clears
// the scope state. Caller holds terminalOpMu.
func (h *Hub) terminalReleaseLocked(c *Client, runtimeID string) {
	h.mu.Lock()
	sessions := c.terminalSessions[runtimeID]
	delete(c.terminalSessions, runtimeID)
	if h.terminalSubs[runtimeID] == c {
		delete(h.terminalSubs, runtimeID)
	}
	delete(h.terminalPaused, runtimeID)
	h.mu.Unlock()

	relay := h.terminalRelayFn()
	if relay == nil || len(sessions) == 0 {
		return
	}
	for sessionID := range sessions {
		frame := marshalMessage(protocol.EventTerminalKill, protocol.TerminalKillPayload{
			SessionID: sessionID,
			RuntimeID: runtimeID,
		})
		if frame == nil {
			continue
		}
		relay.SendTerminalFrame(runtimeID, frame)
	}
}

// terminalClientGone is the disconnect/eviction path: release every terminal
// scope the client held.
func (h *Hub) terminalClientGone(c *Client) {
	h.mu.RLock()
	runtimes := make([]string, 0, len(c.terminalSessions))
	for runtimeID := range c.terminalSessions {
		runtimes = append(runtimes, runtimeID)
	}
	h.mu.RUnlock()
	for _, runtimeID := range runtimes {
		h.unsubscribeTerminal(c, runtimeID)
	}
}

// deliverTerminalFrameToSubscriber sends frame to the scope's single
// subscriber without ever evicting it. Returns the subscriber (nil when no
// active subscriber).
func (h *Hub) deliverTerminalFrameToSubscriber(runtimeID string, frame []byte) *Client {
	h.mu.RLock()
	sub := h.terminalSubs[runtimeID]
	if sub == nil {
		h.mu.RUnlock()
		return nil
	}
	select {
	case sub.send <- frame:
		h.mu.RUnlock()
	default:
		h.mu.RUnlock()
		slog.Debug("terminal frame dropped: subscriber send buffer full",
			"runtime_id", runtimeID)
	}
	return sub
}

// DeliverTerminalFromDaemon delivers one daemon-originated terminal frame to
// the runtime's single subscriber, maintaining the session ledger used for
// kill-on-unsubscribe and applying the send-buffer watermark: at the high
// watermark the daemon's pty drain is paused (never the connection); at the
// low watermark it resumes. While paused, terminal.data frames are dropped —
// the terminal stream is inherently lossy under extreme backpressure, and
// this beats growing the buffer or evicting the client.
func (h *Hub) DeliverTerminalFromDaemon(runtimeID string, frame []byte) {
	if h == nil || runtimeID == "" {
		return
	}
	var msg protocol.Message
	if err := json.Unmarshal(frame, &msg); err != nil {
		return
	}
	// Session ledger for open_result/exit. Control frames bypass the
	// watermark so open results and exits are never dropped.
	switch msg.Type {
	case protocol.EventTerminalOpenResult:
		var p protocol.TerminalOpenResultPayload
		if json.Unmarshal(msg.Payload, &p) == nil && p.Error == "" && p.SessionID != "" {
			h.recordTerminalSession(runtimeID, p.SessionID)
		}
	case protocol.EventTerminalExit:
		var p protocol.TerminalExitPayload
		if json.Unmarshal(msg.Payload, &p) == nil {
			h.forgetTerminalSession(runtimeID, p.SessionID)
		}
	}

	sub := h.deliverTerminalFrameToSubscriber(runtimeID, frame)
	if sub == nil {
		return
	}
	// Watermark check happens after delivery, against the post-send queue
	// depth.
	h.terminalOpMu.Lock()
	queued := len(sub.send)
	h.mu.RLock()
	paused := h.terminalPaused[runtimeID]
	h.mu.RUnlock()
	relay := h.terminalRelayFn()
	switch {
	case terminalShouldPause(paused, queued):
		h.mu.Lock()
		h.terminalPaused[runtimeID] = true
		h.mu.Unlock()
		h.terminalOpMu.Unlock()
		if relay != nil {
			relay.PauseTerminal(runtimeID, true)
		}
		slog.Debug("terminal scope paused (high watermark)", "runtime_id", runtimeID)
	case terminalShouldResume(paused, queued):
		h.mu.Lock()
		delete(h.terminalPaused, runtimeID)
		h.mu.Unlock()
		h.terminalOpMu.Unlock()
		if relay != nil {
			relay.PauseTerminal(runtimeID, false)
		}
		slog.Debug("terminal scope resumed (low watermark)", "runtime_id", runtimeID)
	default:
		h.terminalOpMu.Unlock()
	}
}

func (h *Hub) recordTerminalSession(runtimeID, sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sub := h.terminalSubs[runtimeID]
	if sub == nil {
		return
	}
	if sub.terminalSessions == nil {
		sub.terminalSessions = map[string]map[string]bool{}
	}
	if sub.terminalSessions[runtimeID] == nil {
		sub.terminalSessions[runtimeID] = map[string]bool{}
	}
	sub.terminalSessions[runtimeID][sessionID] = true
}

func (h *Hub) forgetTerminalSession(runtimeID, sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sub := h.terminalSubs[runtimeID]
	if sub == nil {
		return
	}
	if set := sub.terminalSessions[runtimeID]; set != nil {
		delete(set, sessionID)
	}
}

// TerminalRuntimeOffline clears the scope state after the daemon connection
// for runtimeID dropped (daemonws synthesizes the terminal.exit
// daemon_offline frames itself).
func (h *Hub) TerminalRuntimeOffline(runtimeID string) {
	h.terminalOpMu.Lock()
	defer h.terminalOpMu.Unlock()
	h.mu.Lock()
	delete(h.terminalSubs, runtimeID)
	delete(h.terminalPaused, runtimeID)
	h.mu.Unlock()
}

// handleTerminalClientFrame validates and forwards one client-originated
// terminal frame (terminal.open/input/resize/kill) to the daemon relay. The
// runtime is taken from the payload's runtime_id, or — for clients
// subscribed to exactly one terminal scope — from that scope.
func (c *Client) handleTerminalClientFrame(frameType string, payload json.RawMessage) {
	h := c.hub
	var envelope struct {
		RuntimeID string `json:"runtime_id"`
		SessionID string `json:"session_id"`
		ReqID     string `json:"req_id"`
	}
	_ = json.Unmarshal(payload, &envelope)

	runtimeID := envelope.RuntimeID
	if runtimeID == "" {
		runtimeID = h.soleTerminalScope(c)
	}
	if runtimeID == "" || !h.isTerminalSubscriber(c, runtimeID) {
		c.sendJSON(map[string]any{
			"type": protocol.EventTerminalError,
			"payload": protocol.TerminalErrorPayload{
				SessionID: envelope.SessionID,
				RuntimeID: runtimeID,
				ReqID:     envelope.ReqID,
				Error:     "not_subscribed",
			},
		})
		return
	}

	relay := h.terminalRelayFn()
	if relay == nil {
		return
	}
	var outFrame []byte
	switch frameType {
	case protocol.EventTerminalOpen:
		var p protocol.TerminalOpenPayload
		if json.Unmarshal(payload, &p) != nil {
			return
		}
		if p.ReqID == "" {
			p.ReqID = uuid.NewString()
		}
		p.RuntimeID = runtimeID
		p.OwnerID = c.userID
		outFrame = marshalMessage(frameType, p)
	case protocol.EventTerminalInput:
		var p protocol.TerminalInputPayload
		if json.Unmarshal(payload, &p) != nil || p.SessionID == "" {
			return
		}
		p.RuntimeID = runtimeID
		outFrame = marshalMessage(frameType, p)
	case protocol.EventTerminalResize:
		var p protocol.TerminalResizePayload
		if json.Unmarshal(payload, &p) != nil || p.SessionID == "" {
			return
		}
		p.RuntimeID = runtimeID
		outFrame = marshalMessage(frameType, p)
	case protocol.EventTerminalKill:
		var p protocol.TerminalKillPayload
		if json.Unmarshal(payload, &p) != nil || p.SessionID == "" {
			return
		}
		p.RuntimeID = runtimeID
		outFrame = marshalMessage(frameType, p)
	default:
		return
	}
	if outFrame == nil {
		return
	}
	if !relay.SendTerminalFrame(runtimeID, outFrame) {
		// No daemon connection serves this runtime right now. For open,
		// synthesize the correlated failure; other frames are pointless
		// without a daemon.
		if frameType == protocol.EventTerminalOpen {
			c.sendJSON(map[string]any{
				"type": protocol.EventTerminalOpenResult,
				"payload": protocol.TerminalOpenResultPayload{
					ReqID:     envelope.ReqID,
					RuntimeID: runtimeID,
					Error:     "runtime_offline",
				},
			})
		}
	}
}

// soleTerminalScope returns the client's terminal scope when exactly one is
// held, so clients may omit runtime_id on input/resize/kill frames.
func (h *Hub) soleTerminalScope(c *Client) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var only string
	for runtimeID := range c.terminalSessions {
		if only != "" {
			return ""
		}
		only = runtimeID
	}
	return only
}

func (h *Hub) isTerminalSubscriber(c *Client, runtimeID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.terminalSubs[runtimeID] == c
}

// marshalMessage encodes a protocol.Message envelope (same JSON shape the
// realtime hub speaks: {"type": ..., "payload": ...}).
func marshalMessage(frameType string, payload any) []byte {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	data, err := json.Marshal(protocol.Message{Type: frameType, Payload: raw})
	if err != nil {
		return nil
	}
	return data
}
