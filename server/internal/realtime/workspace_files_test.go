package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type filesTestAuth struct {
	mu             sync.Mutex
	snapshot       WorkspaceFilesSnapshot
	code           string
	resourceBlocks map[string]chan struct{}
}

func (a *filesTestAuth) AuthorizeWorkspaceFiles(_ context.Context, _, _ string, _ protocol.WorkspaceFilesContext, resource string) (WorkspaceFilesSnapshot, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	snap := a.snapshot
	if resource == "" {
		snap.ResourceID = ""
		snap.ResourceType = ""
		snap.Root = ""
		snap.DaemonID = ""
	}
	return snap, a.code
}
func (a *filesTestAuth) ListWorkspaceFilesResources(ctx context.Context, _, _ string, c protocol.WorkspaceFilesContext) ([]protocol.WorkspaceFilesResource, string) {
	a.mu.Lock()
	block := a.resourceBlocks[c.ProjectID]
	id := a.snapshot.ResourceID
	a.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, "unavailable"
		}
	}
	return []protocol.WorkspaceFilesResource{{ResourceID: id, DisplayName: "C:\\private\\root", Access: "write"}}, ""
}

type filesTestRelay struct {
	mu         sync.Mutex
	connection protocol.WorkspaceFilesConnection
	seq        uint64
	code       string
	online     bool
	fail       bool
	frames     []protocol.Message
	targets    []*protocol.WorkspaceFilesTarget
}

func newFilesTestRelay() *filesTestRelay {
	return &filesTestRelay{connection: protocol.WorkspaceFilesConnection{Identity: &protocol.WorkspaceFilesConnectionIdentity{}, Epoch: 1}, online: true}
}
func (r *filesTestRelay) SelectWorkspaceFilesTarget(daemon, workspace, id string) (*protocol.WorkspaceFilesTarget, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.code != "" {
		return nil, r.code
	}
	r.seq++
	target := &protocol.WorkspaceFilesTarget{Connection: r.connection, Generation: protocol.WorkspaceFilesGeneration{ConnectionEpoch: r.connection.Epoch, RelaySeq: r.seq}, RuntimeID: "runtime", RequestID: id, WorkspaceID: workspace, DaemonID: daemon}
	r.targets = append(r.targets, target)
	return target, ""
}
func (r *filesTestRelay) WorkspaceFilesTargetOnline(*protocol.WorkspaceFilesTarget) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.online
}
func (r *filesTestRelay) SendWorkspaceFilesFrame(_ *protocol.WorkspaceFilesTarget, raw []byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.online || r.fail {
		return false
	}
	var frame protocol.Message
	if json.Unmarshal(raw, &frame) != nil {
		return false
	}
	r.frames = append(r.frames, frame)
	return true
}
func (r *filesTestRelay) sent(event string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, f := range r.frames {
		if f.Type == event {
			n++
		}
	}
	return n
}
func filesTestClient(h *Hub, user, workspace string) *Client {
	c := &Client{hub: h, userID: user, workspaceID: workspace, send: make(chan []byte, 256)}
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
	return c
}
func filesTestHub(t *testing.T) (*Hub, *Client, *filesTestAuth, *filesTestRelay) {
	t.Helper()
	h := NewHub()
	user, workspace := uuid.NewString(), uuid.NewString()
	relay := newFilesTestRelay()
	auth := &filesTestAuth{snapshot: WorkspaceFilesSnapshot{WorkspaceID: workspace, ProjectID: uuid.NewString(), ResourceID: uuid.NewString(), ResourceType: "local_directory", Root: "C:\\private\\fixture-root", DaemonID: uuid.NewString()}}
	h.ConfigureWorkspaceFiles(true, true, auth, relay)
	c := filesTestClient(h, user, workspace)
	t.Cleanup(func() { h.workspaceFilesClientGone(c); h.ShutdownWorkspaceFiles() })
	return h, c, auth, relay
}
func filesReadRequest(c *Client, snap WorkspaceFilesSnapshot, id string) {
	raw, _ := json.Marshal(protocol.WorkspaceFilesClientReadPayload{ClientReqID: id, Context: protocol.WorkspaceFilesContext{Kind: "project", ProjectID: snap.ProjectID}, ResourceID: snap.ResourceID, Path: "file.txt"})
	c.handleWorkspaceFilesClientFrame(protocol.EventWorkspaceFilesRead, raw)
}
func filesRecordFor(t *testing.T, h *Hub, c *Client, id string) *filesRecord {
	t.Helper()
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	if h.files.sockets[c] == nil || h.files.sockets[c].active[id] == nil {
		t.Fatal("missing active record")
	}
	return h.files.sockets[c].active[id]
}
func filesChunk(h *Hub, r *filesRecord, seq int, eof bool) {
	raw, _ := json.Marshal(protocol.WorkspaceFilesReadChunkPayload{WorkspaceFilesGeneration: r.target.Generation, DaemonReqID: r.target.RequestID, RuntimeID: r.target.RuntimeID, ResourceID: r.snapshot.ResourceID, Seq: seq, Data: []byte("safe text"), EOF: eof})
	h.DeliverWorkspaceFilesFromDaemon(r.target.Connection, protocol.EventWorkspaceFilesReadChunk, raw)
}
func filesMessage(t *testing.T, c *Client) protocol.Message {
	t.Helper()
	select {
	case raw := <-c.send:
		var msg protocol.Message
		if json.Unmarshal(raw, &msg) != nil {
			t.Fatal("bad response")
		}
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("response timeout")
	}
	return protocol.Message{}
}
func filesWantCode(t *testing.T, c *Client, code string) {
	t.Helper()
	msg := filesMessage(t, c)
	var p protocol.WorkspaceFilesClientErrorPayload
	if json.Unmarshal(msg.Payload, &p) != nil || msg.Type != protocol.EventWorkspaceFilesError || p.Code != code {
		t.Fatalf("got %s %s; want %s", msg.Type, msg.Payload, code)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(msg.Payload, &fields) != nil {
		t.Fatal("invalid error JSON")
	}
	for key := range fields {
		if key != "client_req_id" && key != "resource_id" && key != "code" {
			t.Fatal("error leaked field", key)
		}
	}
	if p.ResourceID != "" && !protocol.WorkspaceFilesUUID(p.ResourceID) {
		t.Fatal("error leaked non-resource identifier")
	}
}
func filesNoMessage(t *testing.T, c *Client) {
	t.Helper()
	select {
	case msg := <-c.send:
		t.Fatalf("unexpected frame %s", msg)
	default:
	}
}

func TestWorkspaceFilesBrowserGateMatrix(t *testing.T) {
	for _, code := range []string{"forbidden", "unavailable", "daemon_upgrade_required", "daemon_offline"} {
		t.Run(code, func(t *testing.T) {
			h, c, a, relay := filesTestHub(t)
			if code == "forbidden" {
				a.code = code
			} else if code == "unavailable" {
				h.ConfigureWorkspaceFiles(true, false, a, relay)
			} else {
				relay.code = code
			}
			filesReadRequest(c, a.snapshot, uuid.NewString())
			filesWantCode(t, c, code)
			if relay.sent(protocol.EventWorkspaceFilesRead) != 0 || h.files.active != 0 {
				t.Fatal("denial reached daemon or allocated record")
			}
		})
	}
	h, c, a, r := filesTestHub(t)
	payload := fmt.Sprintf(`{"client_req_id":%q,"context":{"kind":"project","project_id":%q},"resource_id":%q,"path":"../secret","root_path":"secret"}`, uuid.NewString(), a.snapshot.ProjectID, a.snapshot.ResourceID)
	c.handleWorkspaceFilesClientFrame(protocol.EventWorkspaceFilesRead, json.RawMessage(payload))
	filesWantCode(t, c, "invalid_request")
	if r.sent(protocol.EventWorkspaceFilesRead) != 0 || h.files.active != 0 {
		t.Fatal("invalid input reached daemon")
	}
}

func TestWorkspaceFilesMultiSocketCancelAndTerminal(t *testing.T) {
	h, c, a, relay := filesTestHub(t)
	other := filesTestClient(h, c.userID, c.workspaceID)
	t.Cleanup(func() { h.workspaceFilesClientGone(other) })
	id := uuid.NewString()
	filesReadRequest(c, a.snapshot, id)
	first := filesRecordFor(t, h, c, id)
	filesReadRequest(other, a.snapshot, id)
	second := filesRecordFor(t, h, other, id)
	cancel, _ := json.Marshal(protocol.WorkspaceFilesClientCancelPayload{ClientReqID: uuid.NewString()})
	other.handleWorkspaceFilesClientFrame(protocol.EventWorkspaceFilesCancel, cancel)
	if relay.sent(protocol.EventWorkspaceFilesCancel) != 0 {
		t.Fatal("nonowner cancellation reached daemon")
	}
	filesChunk(h, first, 0, false)
	filesMessage(t, c)
	filesNoMessage(t, other)
	filesChunk(h, second, 0, true)
	filesMessage(t, other)
	filesNoMessage(t, c)
	cancel, _ = json.Marshal(protocol.WorkspaceFilesClientCancelPayload{ClientReqID: id})
	other.handleWorkspaceFilesClientFrame(protocol.EventWorkspaceFilesCancel, cancel)
	if relay.sent(protocol.EventWorkspaceFilesCancel) != 0 {
		t.Fatal("terminal cancellation not no-op")
	}
	c.handleWorkspaceFilesClientFrame(protocol.EventWorkspaceFilesCancel, cancel)
	filesChunk(h, first, 1, true)
	filesNoMessage(t, c)
	if relay.sent(protocol.EventWorkspaceFilesCancel) != 1 || h.files.active != 0 || len(h.files.routes) != 0 {
		t.Fatal("owner cancel did not retire exactly once")
	}
}

func TestWorkspaceFilesGenerationSpoofSequenceAndReuse(t *testing.T) {
	h, c, a, _ := filesTestHub(t)
	h.files.newID = func() string { return "forced-identical-daemon-id" }
	id := uuid.NewString()
	filesReadRequest(c, a.snapshot, id)
	old := filesRecordFor(t, h, c, id)
	filesChunk(h, old, 0, true)
	filesMessage(t, c)
	filesReadRequest(c, a.snapshot, id)
	filesWantCode(t, c, "invalid_request")
	h.filesMu.Lock()
	h.files.sweep(h.files.now().Add(filesTTL))
	h.filesMu.Unlock()
	filesReadRequest(c, a.snapshot, id)
	current := filesRecordFor(t, h, c, id)
	if current.target.RequestID != old.target.RequestID || current.target.Generation == old.target.Generation {
		t.Fatal("forced ID fixture ineffective")
	}
	filesChunk(h, old, 0, true)
	filesNoMessage(t, c)
	valid := protocol.WorkspaceFilesReadChunkPayload{WorkspaceFilesGeneration: current.target.Generation, DaemonReqID: current.target.RequestID, RuntimeID: current.target.RuntimeID, ResourceID: current.snapshot.ResourceID, Data: []byte("safe"), EOF: true}
	cases := []func(*protocol.WorkspaceFilesReadChunkPayload){
		func(p *protocol.WorkspaceFilesReadChunkPayload) {
			p.ConnectionEpoch = old.target.Generation.ConnectionEpoch + 1
		},
		func(p *protocol.WorkspaceFilesReadChunkPayload) { p.RelaySeq = old.target.Generation.RelaySeq },
		func(p *protocol.WorkspaceFilesReadChunkPayload) { p.ConnectionEpoch = 0 },
		func(p *protocol.WorkspaceFilesReadChunkPayload) { p.RelaySeq = 0 },
		func(p *protocol.WorkspaceFilesReadChunkPayload) { p.RuntimeID = "other" },
		func(p *protocol.WorkspaceFilesReadChunkPayload) { p.ResourceID = uuid.NewString() },
		func(p *protocol.WorkspaceFilesReadChunkPayload) { p.DaemonReqID = "other" },
		func(p *protocol.WorkspaceFilesReadChunkPayload) { p.Seq = 1 },
	}
	for _, change := range cases {
		p := valid
		change(&p)
		raw, _ := json.Marshal(p)
		h.DeliverWorkspaceFilesFromDaemon(current.target.Connection, protocol.EventWorkspaceFilesReadChunk, raw)
		filesNoMessage(t, c)
	}
	raw, _ := json.Marshal(valid)
	source := protocol.WorkspaceFilesConnection{Identity: &protocol.WorkspaceFilesConnectionIdentity{}, Epoch: current.target.Connection.Epoch}
	h.DeliverWorkspaceFilesFromDaemon(source, protocol.EventWorkspaceFilesReadChunk, raw)
	filesNoMessage(t, c)
	h.DeliverWorkspaceFilesFromDaemon(current.target.Connection, protocol.EventWorkspaceFilesListResult, raw)
	filesNoMessage(t, c)
	filesChunk(h, current, 0, false)
	filesMessage(t, c)
	filesChunk(h, current, 0, false)
	filesNoMessage(t, c)
	filesChunk(h, current, 1, true)
	filesMessage(t, c)
	filesChunk(h, current, 2, true)
	filesNoMessage(t, c)
}

func TestWorkspaceFilesSnapshotRevalidation(t *testing.T) {
	for _, mutation := range []string{"root", "daemon", "project", "type", "resource", "workspace", "member", "query"} {
		t.Run(mutation, func(t *testing.T) {
			h, c, a, relay := filesTestHub(t)
			id := uuid.NewString()
			filesReadRequest(c, a.snapshot, id)
			r := filesRecordFor(t, h, c, id)
			filesChunk(h, r, 0, false)
			filesMessage(t, c)
			a.mu.Lock()
			switch mutation {
			case "root":
				a.snapshot.Root += "/"
			case "daemon":
				a.snapshot.DaemonID = uuid.NewString()
			case "project":
				a.snapshot.ProjectID = uuid.NewString()
			case "type":
				a.snapshot.ResourceType = "github_repo"
			case "resource":
				a.snapshot.ResourceID = uuid.NewString()
			case "workspace":
				a.snapshot.WorkspaceID = uuid.NewString()
			case "member":
				a.code = "forbidden"
			case "query":
				a.code = "unavailable"
			}
			a.mu.Unlock()
			filesChunk(h, r, 1, true)
			code := "forbidden"
			if mutation == "query" {
				code = "unavailable"
			}
			filesWantCode(t, c, code)
			filesChunk(h, r, 1, true)
			filesNoMessage(t, c)
			if relay.sent(protocol.EventWorkspaceFilesCancel) != 1 || h.files.active != 0 {
				t.Fatal("revalidation did not cancel once")
			}
		})
	}
}

func TestWorkspaceFilesResourcesReverseAndLate(t *testing.T) {
	h, c, a, relay := filesTestHub(t)
	blocks := map[string]chan struct{}{}
	a.resourceBlocks = blocks
	first, second := uuid.NewString(), uuid.NewString()
	blocks[first] = make(chan struct{})
	blocks[second] = make(chan struct{})
	send := func(project, id string) {
		raw, _ := json.Marshal(protocol.WorkspaceFilesClientResourcesPayload{ClientReqID: id, Context: protocol.WorkspaceFilesContext{Kind: "project", ProjectID: project}})
		c.handleWorkspaceFilesClientFrame(protocol.EventWorkspaceFilesResources, raw)
	}
	idA, idB := uuid.NewString(), uuid.NewString()
	send(first, idA)
	send(second, idB)
	close(blocks[second])
	msg := filesMessage(t, c)
	var result protocol.WorkspaceFilesClientResourcesResultPayload
	json.Unmarshal(msg.Payload, &result)
	if result.ClientReqID != idB || result.Context.ProjectID != second || result.Resources[0].DisplayName != "local-directory-"+a.snapshot.ResourceID[:8] || result.Resources[0].Access != "read_only" {
		t.Fatalf("wrong projection %s", msg.Payload)
	}
	close(blocks[first])
	msg = filesMessage(t, c)
	json.Unmarshal(msg.Payload, &result)
	if result.ClientReqID != idA || result.Context.ProjectID != first {
		t.Fatal("reversed result mixed contexts")
	}
	if relay.sent(protocol.EventWorkspaceFilesRead) != 0 || strings.Contains(string(msg.Payload), a.snapshot.Root) {
		t.Fatal("resources routed or leaked root")
	}
	late := uuid.NewString()
	block := make(chan struct{})
	a.mu.Lock()
	a.resourceBlocks[late] = block
	a.mu.Unlock()
	oldID := uuid.NewString()
	send(late, oldID)
	h.workspaceFilesClientGone(c)
	close(block)
	newClient := filesTestClient(h, c.userID, c.workspaceID)
	t.Cleanup(func() { h.workspaceFilesClientGone(newClient) })
	raw, _ := json.Marshal(protocol.WorkspaceFilesClientResourcesPayload{ClientReqID: oldID, Context: protocol.WorkspaceFilesContext{Kind: "project", ProjectID: late}})
	newClient.handleWorkspaceFilesClientFrame(protocol.EventWorkspaceFilesResources, raw)
	filesMessage(t, newClient)
	filesNoMessage(t, c)
}

func TestWorkspaceFilesOfflineUnsupportedAndLifecycle(t *testing.T) {
	for _, first := range []string{"offline", "unsupported"} {
		t.Run(first, func(t *testing.T) {
			h, c, a, relay := filesTestHub(t)
			id := uuid.NewString()
			filesReadRequest(c, a.snapshot, id)
			r := filesRecordFor(t, h, c, id)
			offline := func() { h.WorkspaceFilesConnectionOffline(r.target.Connection, "") }
			unsupported := func() {
				raw, _ := json.Marshal(protocol.WorkspaceFilesErrorPayload{WorkspaceFilesGeneration: r.target.Generation, DaemonReqID: r.target.RequestID, RuntimeID: r.target.RuntimeID, ResourceID: r.snapshot.ResourceID, Code: "unsupported"})
				h.DeliverWorkspaceFilesFromDaemon(r.target.Connection, protocol.EventWorkspaceFilesError, raw)
			}
			code := "daemon_offline"
			if first == "offline" {
				offline()
				unsupported()
			} else {
				unsupported()
				offline()
				code = "daemon_upgrade_required"
			}
			filesWantCode(t, c, code)
			filesNoMessage(t, c)
			if len(relay.targets) != 1 || h.files.active != 0 {
				t.Fatal("terminal reselected or retained")
			}
		})
	}
	for _, event := range []string{"timeout", "shutdown", "send_failure", "unknown_error"} {
		t.Run(event, func(t *testing.T) {
			h, c, a, relay := filesTestHub(t)
			if event == "send_failure" {
				relay.fail = true
			}
			id := uuid.NewString()
			filesReadRequest(c, a.snapshot, id)
			code := "unavailable"
			if event == "send_failure" {
				code = "daemon_offline"
			} else {
				r := filesRecordFor(t, h, c, id)
				switch event {
				case "timeout":
					h.filesTimeout(r)
					code = "timeout"
				case "shutdown":
					h.ShutdownWorkspaceFiles()
				case "unknown_error":
					raw, _ := json.Marshal(protocol.WorkspaceFilesErrorPayload{WorkspaceFilesGeneration: r.target.Generation, DaemonReqID: r.target.RequestID, RuntimeID: r.target.RuntimeID, ResourceID: r.snapshot.ResourceID, Code: a.snapshot.Root})
					h.DeliverWorkspaceFilesFromDaemon(r.target.Connection, protocol.EventWorkspaceFilesError, raw)
				}
			}
			filesWantCode(t, c, code)
			filesNoMessage(t, c)
			if h.files.active != 0 || len(h.files.routes) != 0 {
				t.Fatal("terminal route leak")
			}
		})
	}
}

func TestWorkspaceFilesConcurrentCloseSweepZero(t *testing.T) {
	for i := 0; i < 30; i++ {
		h, c, a, _ := filesTestHub(t)
		id := uuid.NewString()
		filesReadRequest(c, a.snapshot, id)
		r := filesRecordFor(t, h, c, id)
		var wg sync.WaitGroup
		for _, op := range []func(){func() { h.removeClient(c) }, func() { h.workspaceFilesClientGone(c) }, func() { h.filesTimeout(r) }, func() { filesChunk(h, r, 0, true) }, func() { h.sweepWorkspaceFiles() }, func() { h.WorkspaceFilesConnectionOffline(r.target.Connection, "") }} {
			wg.Add(1)
			go func(fn func()) { defer wg.Done(); fn() }(op)
		}
		wg.Wait()
		h.filesMu.Lock()
		filesAssertLedger(t, h.files)
		if h.files.active != 0 || h.files.terminal.Len() != 0 || len(h.files.sockets) != 0 || len(h.files.principals) != 0 || len(h.files.users) != 0 || len(h.files.workspaces) != 0 || len(h.files.routes) != 0 {
			t.Fatal("close/sweep left state")
		}
		h.filesMu.Unlock()
	}
}

func TestWorkspaceFilesRecordGenerationExhaustion(t *testing.T) {
	s := newWorkspaceFilesState()
	s.generation = math.MaxUint64 - 1
	c := &Client{userID: "a", workspaceID: "b"}
	if r, code := s.accept(c, "", protocol.WorkspaceFilesRequest{ClientReqID: uuid.NewString()}, WorkspaceFilesSnapshot{}); r != nil || code != "unavailable" || s.active != 0 {
		t.Fatal("generation wrapped")
	}
}

func TestWorkspaceFilesDefaultGateAndBrowserDispatch(t *testing.T) {
	h := NewHub()
	c := filesTestClient(h, uuid.NewString(), uuid.NewString())
	id := uuid.NewString()
	payload, _ := json.Marshal(protocol.WorkspaceFilesClientResourcesPayload{ClientReqID: id, Context: protocol.WorkspaceFilesContext{Kind: "project", ProjectID: uuid.NewString()}})
	raw, _ := json.Marshal(protocol.Message{Type: protocol.EventWorkspaceFilesResources, Payload: payload})
	c.handleFrame(raw)
	filesWantCode(t, c, "unavailable")
	if h.files.enabled || h.files.active != 0 {
		t.Fatal("default gate open")
	}
	h.workspaceFilesClientGone(c)
	h, c, a, relay := filesTestHub(t)
	h.ConfigureWorkspaceFiles(false, true, a, relay)
	filesReadRequest(c, a.snapshot, uuid.NewString())
	filesWantCode(t, c, "unavailable")
	if relay.sent(protocol.EventWorkspaceFilesRead) != 0 {
		t.Fatal("disabled gate reached daemon")
	}
}

func TestWorkspaceFilesUsesCanonicalAuthorizedWorkspace(t *testing.T) {
	h, c, a, relay := filesTestHub(t)
	c.workspaceID = strings.ToUpper(c.workspaceID)
	id := uuid.NewString()
	filesReadRequest(c, a.snapshot, id)
	r := filesRecordFor(t, h, c, id)
	if r.target.WorkspaceID != a.snapshot.WorkspaceID || r.target.WorkspaceID != strings.ToLower(c.workspaceID) {
		t.Fatal("URL UUID casing changed daemon workspace route")
	}
	filesChunk(h, r, 0, true)
	filesMessage(t, c)
	if relay.sent(protocol.EventWorkspaceFilesRead) != 1 {
		t.Fatal("request did not reach canonical target")
	}
	filesAssertLedger(t, h.files)
}
