package protocol

// Terminal frame contract (MAX-51 M2/M3). Frames ride the same
// protocol.Message envelope as every other WS message and flow over two
// transports:
//
//   - server ↔ daemon, on the daemon control connection (daemonws hub), and
//   - client ↔ server, on the realtime hub, where a subscriber to the
//     {scope: "terminal", id: <runtime_id>} scope may send terminal.open /
//     terminal.input / terminal.resize / terminal.kill and receives
//     terminal.data / terminal.exit / terminal.error / terminal.open_result.
//
// The server relay (realtime hub + daemonws hub) forwards frames verbatim;
// only the server enriches terminal.open with runtime_id / owner_id.
const (
	EventTerminalOpen       = "terminal.open"
	EventTerminalOpenResult = "terminal.open_result"
	EventTerminalInput      = "terminal.input"
	EventTerminalResize     = "terminal.resize"
	EventTerminalKill       = "terminal.kill"
	EventTerminalData       = "terminal.data"
	EventTerminalExit       = "terminal.exit"
	EventTerminalError      = "terminal.error"
	// EventTerminalPause is server→daemon flow control: the client's send
	// buffer crossed the high watermark, so the daemon stops draining the pty
	// (the kernel pty buffer backpressures the remote shell) until a paused
	//=false frame arrives. It never terminates the daemon connection.
	EventTerminalPause = "terminal.pause"
)

// DaemonCapabilityTerminalV1 advertises that the daemon can host pty sessions
// and understands the terminal.* frames. Declared only on platforms with a pty
// implementation; the realtime relay refuses terminal subscriptions for
// runtimes without it.
const DaemonCapabilityTerminalV1 = "terminal-v1"

// TerminalExitReasonDaemonOffline is carried by the synthesized terminal.exit
// the server delivers to a subscriber when the runtime's daemon connection
// drops. Code is meaningless in that case.
const TerminalExitReasonDaemonOffline = "daemon_offline"

// TerminalOpenPayload is the client/relay → daemon request to spawn a pty.
// ReqID correlates the terminal.open_result. OwnerID is stamped by the server
// relay (the subscribing user). RuntimeID is stamped by the relay so the
// daemonws hub can route results back; the daemon itself ignores it.
type TerminalOpenPayload struct {
	ReqID     string `json:"req_id"`
	RuntimeID string `json:"runtime_id,omitempty"`
	OwnerID   string `json:"owner_id,omitempty"`
	Shell     string `json:"shell,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

// TerminalOpenResultPayload is the daemon → relay reply to terminal.open,
// correlated by ReqID. Exactly one of SessionID / Error is meaningful.
type TerminalOpenResultPayload struct {
	ReqID     string `json:"req_id"`
	RuntimeID string `json:"runtime_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

// TerminalInputPayload forwards keystrokes. Data is base64 and capped at
// termhost.MaxWriteBytes by the daemon host.
type TerminalInputPayload struct {
	SessionID string `json:"session_id"`
	RuntimeID string `json:"runtime_id,omitempty"`
	Data      []byte `json:"data"`
}

type TerminalResizePayload struct {
	SessionID string `json:"session_id"`
	RuntimeID string `json:"runtime_id,omitempty"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

type TerminalKillPayload struct {
	SessionID string `json:"session_id"`
	RuntimeID string `json:"runtime_id,omitempty"`
}

type TerminalDataPayload struct {
	SessionID string `json:"session_id"`
	Data      []byte `json:"data"`
}

// TerminalExitPayload reports a session's end. Code is the process exit code;
// Reason is set only for server-synthesized exits such as daemon_offline.
type TerminalExitPayload struct {
	SessionID string `json:"session_id"`
	Code      int    `json:"code,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// TerminalErrorPayload reports a terminal-scoped failure that has no better
// frame. SessionID is omitted for subscribe/routing-level errors.
type TerminalErrorPayload struct {
	SessionID string `json:"session_id,omitempty"`
	RuntimeID string `json:"runtime_id,omitempty"`
	ReqID     string `json:"req_id,omitempty"`
	Error     string `json:"error"`
}

// TerminalPausePayload asks the daemon to stop or resume draining pty output.
// An empty SessionID targets every session the relay owns on that connection.
type TerminalPausePayload struct {
	SessionID string `json:"session_id,omitempty"`
	Paused    bool   `json:"paused"`
}
