package realtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// All records and tombstones in this fixture come from browser WebSocket
// requests and native daemon replies. Hooks only observe state or schedule
// actual SQL results / frames captured from an authenticated daemon socket.
type relayEvidencePrincipal struct {
	user, workspace, member, project, issue, resource, root, content string
	browsers                                                         []*relayEvidenceBrowser
}
type relayEvidenceBrowser struct {
	c        *websocket.Conn
	owner    *realtime.Client
	frames   chan protocol.Message
	done     chan struct{}
	once     sync.Once
	framesMu sync.Mutex
	seen     []protocol.Message
}

func (b *relayEvidenceBrowser) close() { b.once.Do(func() { _ = b.c.Close(); <-b.done }) }
func (b *relayEvidenceBrowser) send(t *testing.T, event string, p any) {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.c.WriteJSON(protocol.Message{Type: event, Payload: raw}); err != nil {
		t.Fatal(err)
	}
}
func (b *relayEvidenceBrowser) next(t *testing.T) protocol.Message {
	t.Helper()
	select {
	case m := <-b.frames:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("browser frame timeout")
		return protocol.Message{}
	}
}
func (b *relayEvidenceBrowser) count() int {
	b.framesMu.Lock()
	defer b.framesMu.Unlock()
	return len(b.seen)
}

type relayEvidenceGate struct {
	listed     chan struct{}
	release    chan struct{}
	authorized chan struct{}
	authOnce   sync.Once
}

func newRelayEvidenceGate() *relayEvidenceGate {
	return &relayEvidenceGate{listed: make(chan struct{}), release: make(chan struct{}), authorized: make(chan struct{})}
}

type relayEvidenceAuth struct {
	store  realtime.WorkspaceFilesStore
	tokens map[string]string
	mu     sync.Mutex
	gates  map[string][]*relayEvidenceGate
	late   map[string]*relayEvidenceGate
	stop   chan struct{}
}

func (a *relayEvidenceAuth) ResolveToken(_ context.Context, token string) (string, bool) {
	u, ok := a.tokens[token]
	return u, ok
}
func (a *relayEvidenceAuth) IsMember(ctx context.Context, u, w string) bool {
	var yes bool
	return a.store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM member WHERE user_id=$1::uuid AND workspace_id=$2::uuid)`, u, w).Scan(&yes) == nil && yes
}
func (a *relayEvidenceAuth) AuthorizeWorkspaceFiles(ctx context.Context, u, w string, c protocol.WorkspaceFilesContext, r string) (realtime.WorkspaceFilesSnapshot, string) {
	s, code := a.store.AuthorizeWorkspaceFiles(ctx, u, w, c, r)
	if ctx.Err() != nil {
		a.mu.Lock()
		g := a.late[c.ProjectID]
		a.mu.Unlock()
		if g != nil {
			g.authOnce.Do(func() { close(g.authorized) })
		}
	}
	return s, code
}
func (a *relayEvidenceAuth) ListWorkspaceFilesResources(ctx context.Context, u, w string, c protocol.WorkspaceFilesContext) ([]protocol.WorkspaceFilesResource, string) {
	rows, code := a.store.ListWorkspaceFilesResources(ctx, u, w, c)
	a.mu.Lock()
	var g *relayEvidenceGate
	if q := a.gates[c.ProjectID]; len(q) > 0 {
		g = q[0]
		a.gates[c.ProjectID] = q[1:]
	}
	a.mu.Unlock()
	if g != nil {
		close(g.listed)
		select {
		case <-g.release:
		case <-a.stop:
		}
	}
	return rows, code
}
func (a *relayEvidenceAuth) gate(project string) *relayEvidenceGate {
	g := newRelayEvidenceGate()
	a.mu.Lock()
	a.gates[project] = append(a.gates[project], g)
	a.mu.Unlock()
	return g
}

type relayEvidenceDelivery struct {
	source        protocol.WorkspaceFilesConnection
	event         string
	raw           json.RawMessage
	release, done chan struct{}
}
type relayEvidenceBridge struct {
	h       *realtime.Hub
	mu      sync.Mutex
	delay   bool
	delayed chan *relayEvidenceDelivery
	stop    chan struct{}
}

func (b *relayEvidenceBridge) DeliverWorkspaceFilesFromDaemon(s protocol.WorkspaceFilesConnection, event string, raw json.RawMessage) {
	b.mu.Lock()
	delay := b.delay
	b.mu.Unlock()
	if delay {
		d := &relayEvidenceDelivery{s, event, append(json.RawMessage(nil), raw...), make(chan struct{}), make(chan struct{})}
		b.delayed <- d
		go func() {
			defer close(d.done)
			select {
			case <-d.release:
			case <-b.stop:
			}
			b.h.DeliverWorkspaceFilesFromDaemon(d.source, d.event, d.raw)
		}()
		return
	}
	b.h.DeliverWorkspaceFilesFromDaemon(s, event, raw)
}
func (b *relayEvidenceBridge) WorkspaceFilesConnectionOffline(s protocol.WorkspaceFilesConnection, r string) {
	b.h.WorkspaceFilesConnectionOffline(s, r)
}

type relayEvidenceWire struct {
	mu, write   sync.Mutex
	front, back *websocket.Conn
	hold        bool
	responses   map[protocol.WorkspaceFilesGeneration][]protocol.Message
	requests    []protocol.Message
	pongs       map[string]chan struct{}
	closed      bool
}

func (w *relayEvidenceWire) send(t *testing.T, m protocol.Message) {
	t.Helper()
	w.write.Lock()
	defer w.write.Unlock()
	if err := w.back.WriteJSON(m); err != nil {
		t.Fatal("daemon wire injection:", err)
	}
}
func (w *relayEvidenceWire) barrier(t *testing.T) {
	t.Helper()
	id := uuid.NewString()
	done := make(chan struct{})
	w.mu.Lock()
	w.pongs[id] = done
	w.mu.Unlock()
	w.write.Lock()
	err := w.back.WriteControl(websocket.PingMessage, []byte(id), time.Now().Add(5*time.Second))
	w.write.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon WS processing barrier timeout")
	}
}
func (w *relayEvidenceWire) take(t *testing.T, g protocol.WorkspaceFilesGeneration) protocol.Message {
	t.Helper()
	var m protocol.Message
	relayEvidenceWait(t, "native response", func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		q := w.responses[g]
		if len(q) == 0 {
			return false
		}
		m = q[0]
		w.responses[g] = q[1:]
		return true
	})
	return m
}
func (w *relayEvidenceWire) count(event string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, m := range w.requests {
		if m.Type == event {
			n++
		}
	}
	return n
}
func (w *relayEvidenceWire) requestsCount() int {
	return w.count(protocol.EventWorkspaceFilesRead) + w.count(protocol.EventWorkspaceFilesList)
}
func (w *relayEvidenceWire) setHold(v bool) { w.mu.Lock(); w.hold = v; w.mu.Unlock() }
func (w *relayEvidenceWire) close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()
	_ = w.back.Close()
	_ = w.front.Close()
}

type relayEvidenceNative struct {
	wire     *relayEvidenceWire
	cmd      *exec.Cmd
	stdin    interface{ Close() error }
	done     chan error
	output   bytes.Buffer
	runtimes []string
	closed   bool
}
type relayEvidenceEnv struct {
	t            *testing.T
	db           *relayEvidenceDB
	h            *realtime.Hub
	dh           *daemonws.Hub
	auth         *relayEvidenceAuth
	bridge       *relayEvidenceBridge
	server       *httptest.Server
	backend      *httptest.Server
	principals   []*relayEvidencePrincipal
	browsers     []*relayEvidenceBrowser
	natives      []*relayEvidenceNative
	roots        []string
	workspaceIDs []string
	identityMu   sync.Mutex
	identities   map[string]daemonws.ClientIdentity
	wires        map[string]chan *relayEvidenceWire
	closed       bool
}

func relayEvidenceWait(t *testing.T, what string, p func() bool) {
	t.Helper()
	end := time.Now().Add(8 * time.Second)
	for !p() {
		if time.Now().After(end) {
			t.Fatal("timeout:", what)
		}
		time.Sleep(time.Millisecond)
	}
}
func relayEvidenceSignal(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture synchronization timeout")
	}
}
func newRelayEvidenceEnv(t *testing.T, n int) *relayEvidenceEnv {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("real native relay evidence runs on Linux")
	}
	if os.Getenv("MULTICA_WORKSPACE_FILES_NATIVE_TEST_BINARY") == "" {
		t.Skip("set the explicitly built native daemon test binary")
	}
	dbf, store, daemonID := realtime.RelayEvidenceDatabase(t)
	e := &relayEvidenceEnv{t: t, db: &relayEvidenceDB{dbf, store, daemonID}, h: realtime.NewHub(), dh: daemonws.NewHub(), identities: map[string]daemonws.ClientIdentity{}, wires: map[string]chan *relayEvidenceWire{}}
	e.auth = &relayEvidenceAuth{store: e.db.store, tokens: map[string]string{}, gates: map[string][]*relayEvidenceGate{}, late: map[string]*relayEvidenceGate{}, stop: make(chan struct{})}
	e.bridge = &relayEvidenceBridge{h: e.h, delayed: make(chan *relayEvidenceDelivery, 16), stop: make(chan struct{})}
	e.h.ConfigureWorkspaceFiles(true, true, e.auth, e.dh)
	e.dh.SetWorkspaceFilesBridge(e.bridge)
	// Freeze only expiry time while thousands of real requests populate the
	// ledger; no counters, records, routes or tombstones are manufactured.
	realtime.RelayEvidenceClock(e.h)
	e.workspaceIDs = []string{uuid.NewString(), uuid.NewString()}
	sort.Strings(e.workspaceIDs)
	if n == 6 {
		e.workspaceIDs = append(e.workspaceIDs, uuid.NewString(), uuid.NewString())
		sort.Strings(e.workspaceIDs)
	}
	root := t.TempDir()
	users := make([]string, n)
	for i := range users {
		users[i] = uuid.NewString()
	}
	sort.Strings(users)
	for i := 0; i < n; i++ {
		u, ws := users[i], e.workspaceIDs[i%len(e.workspaceIDs)]
		if n >= 18 && i == 15 {
			ws = e.workspaceIDs[1]
		}
		if n >= 18 && i == 16 {
			u = users[15]
			ws = e.workspaceIDs[0]
		}
		p := &relayEvidencePrincipal{user: u, workspace: ws, root: filepath.Join(root, fmt.Sprintf("private-%02d", i)), content: fmt.Sprintf("principal-%02d-content", i)}
		if err := os.MkdirAll(p.root, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p.root, "small.txt"), []byte(p.content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p.root, "stream.txt"), []byte(strings.Repeat(p.content, 6000)), 0600); err != nil {
			t.Fatal(err)
		}
		p.member = e.db.f.Insert(t, "member", testutil.Cols{"user_id": u, "workspace_id": ws})
		p.project = e.db.f.Insert(t, "project", testutil.Cols{"workspace_id": ws})
		p.issue = e.db.f.Insert(t, "issue", testutil.Cols{"workspace_id": ws, "project_id": p.project})
		ref, _ := json.Marshal(map[string]string{"daemon_id": e.db.daemon, "local_path": p.root})
		p.resource = e.db.f.Insert(t, "project_resource", testutil.Cols{"workspace_id": ws, "project_id": p.project, "resource_type": "local_directory", "resource_ref": ref, "label": p.root, "position": 1})
		e.auth.tokens["mul_fixture_"+u] = u
		e.principals = append(e.principals, p)
		e.roots = append(e.roots, p.root)
	}
	e.backend = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.identityMu.Lock()
		id, ok := e.identities[r.Header.Get("Authorization")]
		e.identityMu.Unlock()
		if !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		id.Capabilities = r.Header.Get("X-Client-Capabilities")
		e.dh.HandleWebSocket(w, r, id)
	}))
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) { realtime.HandleWebSocket(e.h, e.auth, e.auth, nil, w, r) })
	mux.HandleFunc("/api/daemon/ws", e.proxy)
	e.server = httptest.NewServer(mux)
	go e.h.Run()
	t.Cleanup(func() { e.close() })
	e.startNative(e.db.daemon, false)
	return e
}
func (e *relayEvidenceEnv) proxy(w http.ResponseWriter, r *http.Request) {
	e.identityMu.Lock()
	ch := e.wires[r.Header.Get("Authorization")]
	e.identityMu.Unlock()
	if ch == nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	front, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	headers := http.Header{"Authorization": r.Header.Values("Authorization"), "X-Client-Capabilities": r.Header.Values("X-Client-Capabilities")}
	back, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(e.backend.URL, "http")+r.URL.String(), headers)
	if err != nil {
		_ = front.Close()
		return
	}
	wire := &relayEvidenceWire{front: front, back: back, responses: map[protocol.WorkspaceFilesGeneration][]protocol.Message{}, pongs: map[string]chan struct{}{}}
	back.SetPongHandler(func(s string) error {
		wire.mu.Lock()
		if c := wire.pongs[s]; c != nil {
			close(c)
			delete(wire.pongs, s)
		}
		wire.mu.Unlock()
		return nil
	})
	ch <- wire
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer wire.close()
		for {
			var m protocol.Message
			if front.ReadJSON(&m) != nil {
				return
			}
			if strings.HasPrefix(m.Type, "workspace_files.") {
				var p protocol.WorkspaceFilesGeneration
				_ = json.Unmarshal(m.Payload, &p)
				wire.mu.Lock()
				hold := wire.hold
				if hold {
					wire.responses[p] = append(wire.responses[p], m)
				}
				wire.mu.Unlock()
				if hold {
					continue
				}
			}
			wire.write.Lock()
			err := back.WriteJSON(m)
			wire.write.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		defer wire.close()
		for {
			var m protocol.Message
			if back.ReadJSON(&m) != nil {
				return
			}
			wire.mu.Lock()
			wire.requests = append(wire.requests, m)
			wire.mu.Unlock()
			if front.WriteJSON(m) != nil {
				return
			}
		}
	}()
	wg.Wait()
}
func (e *relayEvidenceEnv) startNative(daemon string, hold bool) *relayEvidenceNative {
	t := e.t
	token := "fixture-daemon-" + uuid.NewString()
	leases := map[string]*daemonws.RuntimeLease{}
	ids := []string{}
	for _, ws := range e.workspaceIDs {
		rid := uuid.NewString()
		ids = append(ids, rid)
		leases[rid] = daemonws.NewRuntimeLease(ws, "online", time.Now(), true)
	}
	ch := make(chan *relayEvidenceWire, 1)
	e.identityMu.Lock()
	e.identities["Bearer "+token] = daemonws.ClientIdentity{DaemonID: daemon, UserID: e.principals[0].user, WorkspaceIDs: e.workspaceIDs, RuntimeIDs: ids, RuntimeLeases: leases}
	e.wires["Bearer "+token] = ch
	e.identityMu.Unlock()
	n := &relayEvidenceNative{runtimes: ids, done: make(chan error, 1)}
	bin := os.Getenv("MULTICA_WORKSPACE_FILES_NATIVE_TEST_BINARY")
	if filepath.Base(bin) != "max174-native.test" {
		t.Fatal("native fixture must be the explicitly built max174-native.test")
	}
	n.cmd = exec.Command(bin, "-test.run=^TestWorkspaceFilesRelayNativeProcess$", "-test.v", "-test.timeout=10m")
	cfg, _ := json.Marshal(map[string]any{"URL": e.server.URL, "Token": token, "Daemon": daemon, "State": filepath.Join(e.roots[0], "daemon-state-"+uuid.NewString()), "Runtimes": ids})
	n.cmd.Env = append(os.Environ(), "MULTICA_FILES_NATIVE_HELPER=1", "MULTICA_FILES_NATIVE_CONFIG="+string(cfg))
	n.cmd.Stdout = &n.output
	n.cmd.Stderr = &n.output
	stdin, err := n.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	n.stdin = stdin
	if err := n.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { n.done <- n.cmd.Wait() }()
	e.natives = append(e.natives, n)
	select {
	case n.wire = <-ch:
	case err := <-n.done:
		n.closed = true
		t.Fatal("native helper failed:", err, n.output.String())
	case <-time.After(8 * time.Second):
		t.Fatal("native connection timeout")
	}
	n.wire.setHold(hold)
	for _, rid := range ids {
		relayEvidenceWait(t, "native registered", func() bool { return e.dh.RuntimeConnectionCount(rid) == 1 })
	}
	return n
}
func (e *relayEvidenceEnv) open(p *relayEvidencePrincipal) *relayEvidenceBrowser {
	t := e.t
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(e.server.URL, "http")+"/ws?workspace_id="+p.workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	b := &relayEvidenceBrowser{c: c, frames: make(chan protocol.Message, 1024), done: make(chan struct{})}
	e.browsers = append(e.browsers, b)
	p.browsers = append(p.browsers, b)
	go func() {
		defer close(b.done)
		for {
			var m protocol.Message
			if c.ReadJSON(&m) != nil {
				return
			}
			b.framesMu.Lock()
			b.seen = append(b.seen, m)
			b.framesMu.Unlock()
			b.frames <- m
		}
	}()
	b.send(t, "auth", map[string]string{"token": "mul_fixture_" + p.user})
	if b.next(t).Type != "auth_ack" {
		t.Fatal("fixture browser auth failed")
	}
	relayEvidenceWait(t, "authenticated browser registered", func() bool {
		b.owner = realtime.RelayEvidenceFindClient(e.h, c.LocalAddr().String())
		return b.owner != nil
	})
	return b
}
func (e *relayEvidenceEnv) request(b *relayEvidenceBrowser, p *relayEvidencePrincipal, id, path string) {
	b.send(e.t, protocol.EventWorkspaceFilesRead, protocol.WorkspaceFilesClientReadPayload{ClientReqID: id, Context: protocol.WorkspaceFilesContext{Kind: "issue", IssueID: p.issue}, ResourceID: p.resource, Path: path})
}
func (e *relayEvidenceEnv) record(b *relayEvidenceBrowser, id string) *realtime.RelayEvidenceRecord {
	var r *realtime.RelayEvidenceRecord
	relayEvidenceWait(e.t, "accepted relay record", func() bool { r = realtime.RelayEvidenceRecordFor(e.h, b.owner, id); return r != nil && r.Target != nil })
	return r
}
func (e *relayEvidenceEnv) safe(m protocol.Message) {
	t := e.t
	var f map[string]json.RawMessage
	if json.Unmarshal(m.Payload, &f) != nil {
		t.Fatal("invalid browser payload")
	}
	for _, k := range []string{"root_path", "local_path", "resource_ref", "daemon_id", "daemon_req_id", "runtime_id", "connection_epoch", "relay_seq"} {
		if f[k] != nil {
			t.Fatal("browser trusted metadata:", k)
		}
	}
	for _, root := range e.roots {
		if strings.Contains(string(m.Payload), root) {
			t.Fatal("browser absolute root leak")
		}
	}
	if m.Type == protocol.EventWorkspaceFilesError {
		for k := range f {
			if k != "client_req_id" && k != "resource_id" && k != "code" {
				t.Fatal("error field outside whitelist", k)
			}
		}
	}
}
func (e *relayEvidenceEnv) error(b *relayEvidenceBrowser, id, code string) {
	m := b.next(e.t)
	e.safe(m)
	var p protocol.WorkspaceFilesClientErrorPayload
	if m.Type != protocol.EventWorkspaceFilesError || json.Unmarshal(m.Payload, &p) != nil || p.ClientReqID != id || p.Code != code {
		e.t.Fatalf("error got %s %s want %s", m.Type, m.Payload, code)
	}
	e.t.Logf("safe browser error: %s", m.Payload)
}
func (e *relayEvidenceEnv) chunk(b *relayEvidenceBrowser, id string, seq int) protocol.WorkspaceFilesClientReadChunkPayload {
	m := b.next(e.t)
	e.safe(m)
	if seq == 0 && strings.Contains(e.t.Name(), "RelayEvidenceE02") {
		e.t.Logf("SAFE_BROWSER_FRAME %s", mustRelayEvidenceJSON(m))
	}
	var p protocol.WorkspaceFilesClientReadChunkPayload
	if m.Type != protocol.EventWorkspaceFilesReadChunk || json.Unmarshal(m.Payload, &p) != nil || p.ClientReqID != id || p.Seq != seq {
		e.t.Fatal("unexpected browser content frame")
	}
	return p
}
func (e *relayEvidenceEnv) read(b *relayEvidenceBrowser, p *relayEvidencePrincipal) string {
	id := uuid.NewString()
	e.request(b, p, id, "small.txt")
	got := e.chunk(b, id, 0)
	if !got.EOF || string(got.Data) != p.content || got.ResourceID != p.resource {
		e.t.Fatal("native EOF ownership/content mismatch")
	}
	relayEvidenceWait(e.t, "terminal recorded", func() bool {
		return realtime.RelayEvidenceHasTerminal(e.h, b.owner, id)
	})
	return id
}
func (e *relayEvidenceEnv) observe(label string) {
	e.t.Logf("LEDGER %s", mustRelayEvidenceJSON(realtime.RelayEvidenceSnapshot(e.t, e.h, label)))
}
func mustRelayEvidenceJSON(p any) string { b, _ := json.Marshal(p); return string(b) }
func (e *relayEvidenceEnv) close() {
	if e.closed {
		return
	}
	e.closed = true
	for _, b := range e.browsers {
		b.close()
	}
	close(e.auth.stop)
	close(e.bridge.stop)
	relayEvidenceWait(e.t, "all browser ledger indices zero", func() bool {
		return realtime.RelayEvidenceZero(e.h)
	})
	e.observe("all-browser-close-zero")
	for _, n := range e.natives {
		if n.wire != nil {
			n.wire.close()
		}
		_ = n.stdin.Close()
		if !n.closed {
			select {
			case err := <-n.done:
				if err != nil {
					e.t.Error("native helper exit", err, n.output.String())
				}
			case <-time.After(8 * time.Second):
				_ = n.cmd.Process.Kill()
				<-n.done
				e.t.Error("native helper shutdown timeout")
			}
			n.closed = true
		}
		e.t.Log(strings.TrimSpace(n.output.String()))
		for _, rid := range n.runtimes {
			relayEvidenceWait(e.t, "daemon runtime zero", func() bool { return e.dh.RuntimeConnectionCount(rid) == 0 })
		}
	}
	e.h.ShutdownWorkspaceFiles()
	e.server.Close()
	e.backend.Close()
	e.t.Log("CLEANUP browser ledger/socket/principal/user/workspace/route/reserve-owner=0; daemon runtimes=0")
}

type relayEvidenceDB struct {
	f      *testutil.Fixture
	store  realtime.WorkspaceFilesStore
	daemon string
}
