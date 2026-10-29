package realtime

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// The late resources test leaves exactly one newer callback blocked. Waiting
// for this worker count proves the older callback has actually returned before
// inspecting the newer record. No production callback hook is required.
func RelayEvidenceResourceWorkers() int {
	buf := make([]byte, 32768)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), "realtime.(*Hub).filesResources(")
		}
		buf = make([]byte, 2*len(buf))
	}
}

// These exports exist only in the augmented package built by go test. The
// external integration package can import daemonws without creating a cycle.
const RelayEvidenceReserve = filesReserve
const RelayEvidenceIncumbent = filesIncumbent

type RelayEvidenceRecord struct {
	r      *filesRecord
	Target *protocol.WorkspaceFilesTarget
}
type RelayEvidenceVictim struct {
	x        *filesTombstone
	Owner    *Client
	ID       string
	Sequence uint64
	Class    filesClass
}

func RelayEvidenceDatabase(t *testing.T) (*testutil.Fixture, WorkspaceFilesStore, string) {
	d := filesNativeDB(t)
	return d.f, d.store, d.daemon
}
func RelayEvidenceClock(h *Hub) {
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	h.files.now = func() time.Time { return time.Unix(1000, 0) }
}
func RelayEvidenceRequestID(h *Hub, id string) {
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	h.files.newID = func() string { return id }
}
func RelayEvidenceFindClient(h *Hub, addr string) *Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.conn.RemoteAddr().String() == addr {
			return c
		}
	}
	return nil
}
func RelayEvidenceRecordFor(h *Hub, c *Client, id string) *RelayEvidenceRecord {
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	s := h.files.sockets[c]
	if s == nil || s.active[id] == nil {
		return nil
	}
	r := s.active[id]
	return &RelayEvidenceRecord{r: r, Target: r.target}
}
func RelayEvidenceClientGone(h *Hub, c *Client) bool {
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	return h.files.sockets[c] == nil
}
func RelayEvidenceContains(h *Hub, r *RelayEvidenceRecord) bool {
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	return r != nil && h.files.contains(r.r)
}
func RelayEvidenceHasTerminal(h *Hub, c *Client, id string) bool {
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	s := h.files.sockets[c]
	return s != nil && s.terminal[id] != nil
}
func RelayEvidenceUnchanged(t *testing.T, h *Hub, r *RelayEvidenceRecord, seq, n int) {
	t.Helper()
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	if r == nil || !h.files.contains(r.r) || r.r.nextSeq != seq || r.r.readBytes != n || r.r.nonce != r.r.owner.filesNonce {
		t.Fatal("late event changed fresh pointer/nonce/seq/bytes")
	}
	filesAssertLedger(t, h.files)
}
func RelayEvidenceTotals(t *testing.T, h *Hub, i, r, g int) {
	t.Helper()
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	s := h.files
	filesAssertLedger(t, s)
	if s.incumbent != i || s.reserve != r || s.terminal.Len() != g || s.active != 0 || len(s.routes) != 0 || s.corruption != 0 {
		t.Fatalf("totals I/R/G/active/routes/corruption=%d/%d/%d/%d/%d/%d", s.incumbent, s.reserve, s.terminal.Len(), s.active, len(s.routes), s.corruption)
	}
}
func RelayEvidenceOldest(t *testing.T, h *Hub, u, w string, class int) *RelayEvidenceVictim {
	t.Helper()
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	p := h.files.principals[filesPrincipalKey{u, w}]
	if p == nil {
		t.Fatal("missing victim principal")
	}
	l := &p.reserve
	if class == int(filesIncumbent) {
		l = &p.incumbent
	}
	x := filesOldest(l)
	if x == nil {
		t.Fatal("missing expected victim")
	}
	return &RelayEvidenceVictim{x: x, Owner: x.owner, ID: x.id, Sequence: x.sequence, Class: x.class}
}
func RelayEvidenceReserveCount(h *Hub, u, w string) int {
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	p := h.files.principals[filesPrincipalKey{u, w}]
	if p == nil {
		return 0
	}
	return p.reserve.Len()
}
func RelayEvidenceSocketCounts(h *Hub, cs ...*Client) map[*Client]int {
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	m := map[*Client]int{}
	for _, c := range cs {
		n := 0
		if s := h.files.sockets[c]; s != nil {
			n = len(s.terminal)
		}
		m[c] = n
	}
	return m
}
func RelayEvidenceAssertVictim(t *testing.T, h *Hub, label string, v *RelayEvidenceVictim, c *Client, id string, before map[*Client]int) map[string]any {
	t.Helper()
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	s := h.files
	if sock := s.sockets[v.Owner]; sock != nil && sock.terminal[v.ID] != nil {
		t.Fatal("wrong victim: expected ID survived")
	}
	x := s.sockets[c].terminal[id]
	if x == nil || x.class != v.Class {
		t.Fatal("wrong inserted class")
	}
	for owner, n := range before {
		expected := n
		if owner == v.Owner {
			expected--
		}
		if owner == c {
			expected++
		}
		actual := 0
		if sock := s.sockets[owner]; sock != nil {
			actual = len(sock.terminal)
		}
		if actual != expected {
			t.Fatal("victim/requester socket transfer")
		}
	}
	filesAssertLedger(t, s)
	return map[string]any{"scenario": label, "victim_client_req_id": v.ID, "victim_sequence": v.Sequence, "victim_user": v.Owner.userID, "victim_workspace": v.Owner.workspaceID, "victim_class": v.Class, "inserted_client_req_id": id, "inserted_sequence": x.sequence, "inserted_class": x.class, "same_socket": v.Owner == c, "I": s.incumbent, "R": s.reserve, "G": s.terminal.Len()}
}
func RelayEvidenceTerminalOnce(t *testing.T, h *Hub, c *Client, id string) {
	t.Helper()
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	s := h.files
	sock := s.sockets[c]
	if sock == nil || sock.active[id] != nil || sock.terminal[id] == nil || len(sock.terminal) != 1 || s.active != 0 || len(s.routes) != 0 {
		t.Fatal("late event changed single terminal record")
	}
	filesAssertLedger(t, s)
}
func RelayEvidenceZero(h *Hub) bool {
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	s := h.files
	return s.active == 0 && s.terminal.Len() == 0 && s.incumbent == 0 && s.reserve == 0 && len(s.routes)+len(s.sockets)+len(s.principals)+len(s.reserveOwners)+len(s.users)+len(s.workspaces) == 0
}
func RelayEvidenceSnapshot(t *testing.T, h *Hub, label string) map[string]any {
	t.Helper()
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	s := h.files
	filesAssertLedger(t, s)
	users, workspaces := map[string]filesCounts{}, map[string]filesCounts{}
	for k, v := range s.users {
		users[k] = *v
	}
	for k, v := range s.workspaces {
		workspaces[k] = *v
	}
	userRows, workspaceRows := map[string]map[string]int{}, map[string]map[string]int{}
	for k, v := range users {
		userRows[k] = map[string]int{"active": v.active, "terminal": v.terminal}
	}
	for k, v := range workspaces {
		workspaceRows[k] = map[string]int{"active": v.active, "terminal": v.terminal}
	}
	principals := []map[string]any{}
	for k, p := range s.principals {
		principals = append(principals, map[string]any{"user": k.user, "workspace": k.workspace, "active": p.active, "terminal": p.order.Len(), "incumbent": p.incumbent.Len(), "reserve": p.reserve.Len()})
	}
	sockets := []map[string]any{}
	for c, q := range s.sockets {
		sockets = append(sockets, map[string]any{"user": c.userID, "workspace": c.workspaceID, "socket_nonce": c.filesNonce, "active": len(q.active), "terminal": len(q.terminal)})
	}
	return map[string]any{"checkpoint": label, "active": s.active, "incumbent": s.incumbent, "reserve": s.reserve, "global": s.terminal.Len(), "routes": len(s.routes), "user_counts": userRows, "workspace_counts": workspaceRows, "principals": principals, "sockets": sockets, "reserve_owners": len(s.reserveOwners), "corruption": s.corruption}
}
