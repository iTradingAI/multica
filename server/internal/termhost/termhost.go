// Package termhost hosts interactive pty sessions on behalf of remote
// clients (the desktop's floating terminal). It mirrors the security
// posture of the desktop-side host in apps/desktop/src/main/terminal-manager.ts:
// the daemon mints session ids, every field is validated before a process is
// spawned, writes and session counts are capped, commands are scoped to the
// connection that opened the session, and a session never outlives the
// connection that opened it.
//
// The package is deliberately free of WebSocket and HTTP concerns — the
// daemon wires Spawn/Write/Resize/Kill to its server connection. Tests inject
// a fake pty, exactly like the desktop host's unit tests inject a fake
// node-pty process.
package termhost

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// ErrUnsupported is set by platforms without a pty implementation (see
// pty_unsupported.go) and mapped to its own failure reason.
var ErrUnsupported = errors.New("terminal sessions are not supported on this platform")

// openPtyDefault spawns a real pty on platforms that support one; platform
// files override it in init (pty_unix.go).
var openPtyDefault = func(SpawnRequest) (PtyProcess, error) { return nil, ErrUnsupported }

const (
	// MaxSessions is the hard cap on live sessions per daemon.
	MaxSessions = 5

	MinCols = 2
	MaxCols = 500
	MinRows = 2
	MaxRows = 300

	// MaxWriteBytes caps a single write chunk. A keystroke is a few bytes; a
	// large paste can be hundreds of KB. Without the cap a runaway client
	// could pin the daemon shuttling unbounded data into a pty.
	MaxWriteBytes = 256 * 1024
)

// Spawn failure reasons. They mirror the desktop host's results so the
// renderer keeps one error-handling path for local and remote sessions.
const (
	ReasonInvalid     = "invalid_request"
	ReasonLimit       = "limit_reached"
	ReasonFailed      = "spawn_failed"
	ReasonUnsupported = "unsupported"
)

// SpawnRequest describes the session to open. Cwd and Shell are optional; an
// empty Shell resolves to the daemon user's login shell.
type SpawnRequest struct {
	Cols  int
	Rows  int
	Cwd   string
	Shell string
}

// SpawnResult mirrors the desktop host's result shape.
type SpawnResult struct {
	OK        bool
	SessionID string
	Reason    string
	Message   string
}

func okSpawn(sessionID string) SpawnResult {
	return SpawnResult{OK: true, SessionID: sessionID}
}

func failSpawn(reason, message string) SpawnResult {
	return SpawnResult{Reason: reason, Message: message}
}

// Sink receives one session's output and exit code. Implementations must not
// retain p beyond the call. Gone reports whether the owning connection is
// finished — the host tears such sessions down, because a pty must never
// outlive the client that opened it.
type Sink interface {
	Data(sessionID string, p []byte)
	Exit(sessionID string, exitCode int)
	Gone() bool
}

// PtyProcess is the daemon-side handle to one live pty. Output closes when
// the pty read side ends; Wait blocks until the child exits and returns its
// code.
type PtyProcess interface {
	Write(p []byte) (int, error)
	Resize(cols, rows int) error
	Kill() error
	Wait() int
	Output() <-chan []byte
}

// Options inject the effects the host needs. Zero values fall back to the
// real implementations, so production wiring is `termhost.New(termhost.Options{})`.
type Options struct {
	OpenPty          func(SpawnRequest) (PtyProcess, error)
	DefaultShell     func() string
	IsDir            func(string) bool
	IsExecutableFile func(string) bool
	NewID            func() string
}

type session struct {
	id     string
	owner  string
	proc   PtyProcess
	sink   Sink
	killed bool

	mu       sync.Mutex
	disposed bool
}

func (s *session) markDisposed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disposed {
		return false
	}
	s.disposed = true
	return true
}

// Host owns every live pty for one daemon process.
type Host struct {
	opts     Options
	mu       sync.Mutex
	sessions map[string]*session
}

// New builds a host, filling unset options with real implementations.
func New(opts Options) *Host {
	if opts.OpenPty == nil {
		opts.OpenPty = openPtyDefault
	}
	if opts.DefaultShell == nil {
		opts.DefaultShell = defaultShell
	}
	if opts.IsDir == nil {
		opts.IsDir = func(p string) bool { return isDirectory(p) }
	}
	if opts.IsExecutableFile == nil {
		opts.IsExecutableFile = func(p string) bool { return isExecutableFile(p) }
	}
	if opts.NewID == nil {
		opts.NewID = func() string { return uuid.NewString() }
	}
	return &Host{opts: opts, sessions: map[string]*session{}}
}

// SessionCount reports live sessions.
func (h *Host) SessionCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.sessions)
}

// Spawn opens a pty for owner (the client connection key) and starts pumping
// its output into sink. The session id is minted here, never supplied by the
// client.
func (h *Host) Spawn(owner string, req SpawnRequest, sink Sink) SpawnResult {
	if owner == "" || sink == nil {
		return failSpawn(ReasonInvalid, "missing owner or sink")
	}
	if reason := validateSize(req.Cols, req.Rows); reason != "" {
		return failSpawn(ReasonInvalid, reason)
	}
	// Unlike the desktop host, paths are checked with filepath.IsAbs so
	// Windows daemons can accept C:\... directories.
	if req.Cwd != "" && (!filepath.IsAbs(req.Cwd) || !h.opts.IsDir(req.Cwd)) {
		return failSpawn(ReasonInvalid, "cwd is not an existing directory")
	}
	if req.Shell != "" && (!filepath.IsAbs(req.Shell) || !h.opts.IsExecutableFile(req.Shell)) {
		return failSpawn(ReasonInvalid, "shell is not an existing executable file")
	}
	if req.Shell == "" {
		req.Shell = h.opts.DefaultShell()
	}

	h.mu.Lock()
	if len(h.sessions) >= MaxSessions {
		h.mu.Unlock()
		return failSpawn(ReasonLimit, "at most 5 terminal sessions can be open at once")
	}
	// Kept inside the lock so two concurrent opens cannot exceed the cap.
	proc, err := h.opts.OpenPty(req)
	if err != nil {
		h.mu.Unlock()
		if errors.Is(err, ErrUnsupported) {
			return failSpawn(ReasonUnsupported, err.Error())
		}
		return failSpawn(ReasonFailed, err.Error())
	}
	id := h.opts.NewID()
	s := &session{id: id, owner: owner, proc: proc, sink: sink}
	h.sessions[id] = s
	h.mu.Unlock()

	go h.pump(s)
	return okSpawn(id)
}

// Write forwards client keystrokes. It is scoped to owner: a connection may
// only write to sessions it opened itself.
func (h *Host) Write(owner, sessionID string, p []byte) bool {
	if len(p) > MaxWriteBytes {
		return false
	}
	s := h.lookup(owner, sessionID)
	if s == nil {
		return false
	}
	_, err := s.proc.Write(p)
	return err == nil
}

// Resize forwards a client resize. Scoped to owner like Write.
func (h *Host) Resize(owner, sessionID string, cols, rows int) bool {
	if reason := validateSize(cols, rows); reason != "" {
		return false
	}
	s := h.lookup(owner, sessionID)
	if s == nil {
		return false
	}
	return s.proc.Resize(cols, rows) == nil
}

// Kill ends one session. The killing connection already knows the session is
// gone, so no exit event is emitted — the same contract as the desktop host.
func (h *Host) Kill(owner, sessionID string) bool {
	s := h.drop(owner, sessionID)
	if s == nil {
		return false
	}
	_ = s.proc.Kill()
	return true
}

// KillOwner tears down every session of one connection. Called when that
// connection drops, so a shell never outlives the client that opened it.
func (h *Host) KillOwner(owner string) {
	h.mu.Lock()
	ids := make([]string, 0, len(h.sessions))
	for id, s := range h.sessions {
		if s.owner == owner {
			ids = append(ids, id)
		}
	}
	sessions := make([]*session, 0, len(ids))
	for _, id := range ids {
		s := h.sessions[id]
		if s.markDisposed() {
			delete(h.sessions, id)
			sessions = append(sessions, s)
		}
	}
	h.mu.Unlock()

	for _, s := range sessions {
		_ = s.proc.Kill()
	}
}

// lookup enforces ownership: a connection can only touch its own sessions.
// This closes the gap recorded in MAX-50, where the desktop host resolves a
// session by id alone.
func (h *Host) lookup(owner, sessionID string) *session {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sessionID]
	if !ok || s.owner != owner {
		return nil
	}
	return s
}

func (h *Host) drop(owner, sessionID string) *session {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sessionID]
	if !ok || s.owner != owner {
		return nil
	}
	if !s.markDisposed() {
		return nil
	}
	delete(h.sessions, sessionID)
	return s
}

// pump forwards pty output to the sink until the read side closes, then
// reports the exit code. Output is read one chunk at a time and handed to the
// sink synchronously, so a slow relay is the transport's backpressure problem
// and never grows an unbounded queue here.
func (h *Host) pump(s *session) {
	for chunk := range s.proc.Output() {
		if h.sinkDone(s) {
			return
		}
		s.sink.Data(s.id, chunk)
	}
	code := s.proc.Wait()

	s.mu.Lock()
	disposed := s.disposed
	if !disposed {
		s.disposed = true
		delete(h.sessions, s.id)
	}
	s.mu.Unlock()
	if disposed {
		// Killed or torn down while we were reading: the killer already knows.
		return
	}
	s.sink.Exit(s.id, code)
}

func (h *Host) sinkDone(s *session) bool {
	if s.sink.Gone() {
		h.KillOwner(s.owner)
		return true
	}
	return false
}

func validateSize(cols, rows int) string {
	if cols < MinCols || cols > MaxCols {
		return "cols must be between 2 and 500"
	}
	if rows < MinRows || rows > MaxRows {
		return "rows must be between 2 and 300"
	}
	return ""
}

func isDirectory(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func isExecutableFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

// defaultShell mirrors the desktop host: $SHELL when set, /bin/bash on unix
// otherwise.
func defaultShell() string {
	if shell := os.Getenv("SHELL"); strings.TrimSpace(shell) != "" {
		return shell
	}
	return "/bin/bash"
}

// shellEnv builds the environment for a hosted shell. process.env-style maps
// can carry empty entries, which a pty child has no use for; TERM is pinned so
// prompt and tooling output match what the desktop renderer draws.
func shellEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if entry == "" {
			continue
		}
		if strings.HasPrefix(entry, "TERM=") {
			continue
		}
		out = append(out, entry)
	}
	return append(out, "TERM=xterm-256color")
}
