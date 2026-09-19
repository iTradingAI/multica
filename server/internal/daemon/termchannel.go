package daemon

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/multica-ai/multica/server/internal/termhost"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// termSessionOwner is the termhost ownership key for every session opened
// through the server relay. All relayed sessions share one connection (the
// daemon's WS control connection), so owner-based isolation collapses to a
// single key; a session never outlives that connection because detach kills
// the whole owner set when the WS drops.
const termSessionOwner = "server-relay"

// termSendFunc queues one frame on the daemon's WS writer. false means the
// connection is gone or its queue is full — the frame is dropped, never
// blocked on, so terminal output cannot starve the heartbeat sender.
type termSendFunc func(frame []byte) bool

// pauseGate implements the relay's flow control on the daemon side. While
// paused, the termhost pump blocks inside Sink.Data, which stops it reading
// the pty; the kernel pty buffer then backpressures the remote shell.
// Resume releases every blocked waiter.
type pauseGate struct {
	mu      sync.Mutex
	paused  bool
	waiters []chan struct{}
}

func (g *pauseGate) setPaused(paused bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused == paused {
		return
	}
	g.paused = paused
	if !paused {
		for _, ch := range g.waiters {
			close(ch)
		}
		g.waiters = nil
	}
}

// wait blocks while the gate is paused and returns true once unpaused, or
// false when the gate was closed (session teardown) instead.
func (g *pauseGate) wait(closed <-chan struct{}) bool {
	g.mu.Lock()
	if !g.paused {
		g.mu.Unlock()
		return true
	}
	wake := make(chan struct{})
	g.waiters = append(g.waiters, wake)
	g.mu.Unlock()

	select {
	case <-wake:
		return true
	case <-closed:
		return false
	}
}

// termSink adapts one session's termhost output to the WS transport. One
// instance per session, handed to termhost.Host.Spawn.
type termSink struct {
	ch   *termChannel
	gate *pauseGate
}

// Data is called by the termhost pump for every chunk of pty output. It
// blocks while the relay has this session paused (flow control) and drops
// frames once the connection is detached.
func (s termSink) Data(sessionID string, p []byte) {
	if !s.gate.wait(s.ch.doneCh()) {
		return
	}
	frame, err := json.Marshal(protocol.Message{
		Type: protocol.EventTerminalData,
		Payload: marshalRaw(protocol.TerminalDataPayload{
			SessionID: sessionID,
			Data:      p,
		}),
	})
	if err != nil {
		return
	}
	s.ch.sendFrame(frame)
}

func (s termSink) Exit(sessionID string, exitCode int) {
	s.ch.removeGate(sessionID)
	frame, err := json.Marshal(protocol.Message{
		Type: protocol.EventTerminalExit,
		Payload: marshalRaw(protocol.TerminalExitPayload{
			SessionID: sessionID,
			Code:      exitCode,
		}),
	})
	if err != nil {
		return
	}
	s.ch.sendFrame(frame)
}

func (s termSink) Gone() bool { return s.ch.detached() }

// termChannel converts terminal.* frames on the daemon WS control connection
// into termhost calls and streams pty output back as terminal.data /
// terminal.exit frames. Sessions are torn down when the WS connection drops
// (detach, called from the connection teardown path in wakeup.go).
type termChannel struct {
	mu     sync.Mutex
	host   *termhost.Host
	send   termSendFunc
	gates  map[string]*pauseGate
	closed chan struct{}
}

func newTermChannel() *termChannel {
	return &termChannel{
		host:   termhost.New(termhost.Options{}),
		gates:  make(map[string]*pauseGate),
		closed: make(chan struct{}),
	}
}

// attach binds a live connection's frame sender. Passing nil detaches: all
// sessions opened over the connection are killed so no shell outlives it.
func (c *termChannel) attach(send termSendFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.send = send
	if send != nil {
		return
	}
	select {
	case <-c.closed:
		// already closed
	default:
		close(c.closed)
	}
	for _, g := range c.gates {
		g.setPaused(false)
	}
	c.gates = make(map[string]*pauseGate)
	c.host.KillOwner(termSessionOwner)
}

func (c *termChannel) sendFrame(frame []byte) {
	c.mu.Lock()
	send := c.send
	c.mu.Unlock()
	if send != nil {
		send(frame)
	}
}

func (c *termChannel) detached() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *termChannel) removeGate(sessionID string) {
	c.mu.Lock()
	delete(c.gates, sessionID)
	c.mu.Unlock()
}

// gateFor returns the gate for a session, creating one when create is set.
func (c *termChannel) gateFor(sessionID string, create bool) *pauseGate {
	c.mu.Lock()
	defer c.mu.Unlock()
	g, ok := c.gates[sessionID]
	if !ok && create {
		g = &pauseGate{}
		c.gates[sessionID] = g
	}
	return g
}

// HandleMessage processes one inbound server→daemon terminal frame. The
// msg.Type is one of the protocol.EventTerminal* server-sent constants.
func (c *termChannel) HandleMessage(msgType string, payload json.RawMessage) {
	switch msgType {
	case protocol.EventTerminalOpen:
		c.handleOpen(payload)
	case protocol.EventTerminalInput:
		var p protocol.TerminalInputPayload
		if json.Unmarshal(payload, &p) != nil || p.SessionID == "" {
			return
		}
		c.host.Write(termSessionOwner, p.SessionID, p.Data)
	case protocol.EventTerminalResize:
		var p protocol.TerminalResizePayload
		if json.Unmarshal(payload, &p) != nil || p.SessionID == "" {
			return
		}
		c.host.Resize(termSessionOwner, p.SessionID, p.Cols, p.Rows)
	case protocol.EventTerminalKill:
		var p protocol.TerminalKillPayload
		if json.Unmarshal(payload, &p) != nil || p.SessionID == "" {
			return
		}
		c.removeGate(p.SessionID)
		c.host.Kill(termSessionOwner, p.SessionID)
	case protocol.EventTerminalPause:
		var p protocol.TerminalPausePayload
		if json.Unmarshal(payload, &p) != nil {
			return
		}
		if p.SessionID == "" {
			c.mu.Lock()
			gates := make([]*pauseGate, 0, len(c.gates))
			for _, g := range c.gates {
				gates = append(gates, g)
			}
			c.mu.Unlock()
			for _, g := range gates {
				g.setPaused(p.Paused)
			}
			return
		}
		if g := c.gateFor(p.SessionID, false); g != nil {
			g.setPaused(p.Paused)
		}
	}
}

func (c *termChannel) handleOpen(payload json.RawMessage) {
	var p protocol.TerminalOpenPayload
	if json.Unmarshal(payload, &p) != nil || p.ReqID == "" {
		return
	}
	// The gate exists before Spawn so the pump (started inside Spawn) can
	// never hit a missing gate; map registration below only enables
	// session-targeted pause lookups.
	gate := &pauseGate{}
	result := c.host.Spawn(termSessionOwner, termhost.SpawnRequest{
		Shell: p.Shell,
		Cwd:   p.Cwd,
		Cols:  p.Cols,
		Rows:  p.Rows,
	}, termSink{ch: c, gate: gate})
	if result.OK {
		c.mu.Lock()
		c.gates[result.SessionID] = gate
		c.mu.Unlock()
	}
	resp := protocol.TerminalOpenResultPayload{ReqID: p.ReqID, RuntimeID: p.RuntimeID}
	if result.OK {
		resp.SessionID = result.SessionID
	} else {
		resp.Error = result.Message
		slog.Debug("terminal open failed", "req_id", p.ReqID, "reason", result.Reason)
	}
	frame, err := json.Marshal(protocol.Message{
		Type:    protocol.EventTerminalOpenResult,
		Payload: marshalRaw(resp),
	})
	if err != nil {
		return
	}
	c.sendFrame(frame)
}

// SessionCount reports live relayed sessions (test/debug convenience).
func (c *termChannel) SessionCount() int { return c.host.SessionCount() }

// doneCh exposes the closed-on-detach signal to sinks.
func (c *termChannel) doneCh() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}
