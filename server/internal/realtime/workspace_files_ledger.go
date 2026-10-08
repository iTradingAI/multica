package realtime

import (
	"container/list"
	"context"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	filesSocketActive      = 16
	filesPrincipalActive   = 64
	filesUserActive        = 128
	filesWorkspaceActive   = 256
	filesGlobalActive      = 512
	filesSocketTerminal    = 256
	filesPrincipalTerminal = 512
	filesGlobalTerminal    = 8192
	filesHighWater         = filesGlobalTerminal - filesPrincipalTerminal
	filesTTL               = 60 * time.Second
	filesBudget            = 10 * time.Second
)

type filesPrincipalKey struct{ user, workspace string }
type filesCounts struct{ active, terminal int }
type filesSocket struct {
	active   map[string]*filesRecord
	terminal map[string]*filesTombstone
	order    list.List
}
type filesPrincipal struct {
	active                    int
	order, incumbent, reserve list.List
}
type filesClass uint8

const (
	filesIncumbent filesClass = iota
	filesReserve
)

type filesTombstone struct {
	owner                                 *Client
	id, nonce                             string
	generation, sequence                  uint64
	expires                               time.Time
	class                                 filesClass
	global, socket, principal, classified *list.Element
}
type filesRecord struct {
	opMu                        sync.Mutex
	owner                       *Client
	nonce                       string
	generation                  uint64
	request                     protocol.WorkspaceFilesRequest
	event                       string
	snapshot                    WorkspaceFilesSnapshot
	target                      *protocol.WorkspaceFilesTarget
	auth                        WorkspaceFilesAuthorizer
	relay                       WorkspaceFilesRelay
	ctx                         context.Context
	cancel                      context.CancelFunc
	timer                       *time.Timer
	nextSeq, readBytes, entries int
}
type workspaceFilesState struct {
	enabled              bool
	auth                 WorkspaceFilesAuthorizer
	relay                WorkspaceFilesRelay
	now                  func() time.Time
	newID                func() string
	generation, sequence uint64
	sockets              map[*Client]*filesSocket
	principals           map[filesPrincipalKey]*filesPrincipal
	reserveOwners        map[filesPrincipalKey]*filesPrincipal
	users, workspaces    map[string]*filesCounts
	routes               map[protocol.WorkspaceFilesGeneration]*filesRecord
	active               int
	terminal             list.List
	incumbent, reserve   int
	// corruption counts fail-closed invariant violations without recording data.
	corruption uint64
}

func newWorkspaceFilesState() *workspaceFilesState {
	return &workspaceFilesState{now: time.Now, newID: func() string { return uuid.NewString() }, sockets: map[*Client]*filesSocket{}, principals: map[filesPrincipalKey]*filesPrincipal{}, reserveOwners: map[filesPrincipalKey]*filesPrincipal{}, users: map[string]*filesCounts{}, workspaces: map[string]*filesCounts{}, routes: map[protocol.WorkspaceFilesGeneration]*filesRecord{}}
}
func filesKey(c *Client) filesPrincipalKey {
	return filesPrincipalKey{strings.ToLower(c.userID), strings.ToLower(c.workspaceID)}
}
func (s *workspaceFilesState) principal(c *Client) *filesPrincipal {
	key := filesKey(c)
	p := s.principals[key]
	if p == nil {
		p = &filesPrincipal{}
		s.principals[key] = p
	}
	return p
}
func filesCounter(m map[string]*filesCounts, key string) *filesCounts {
	n := m[key]
	if n == nil {
		n = &filesCounts{}
		m[key] = n
	}
	return n
}
func (s *workspaceFilesState) prune(c *Client) {
	sock := s.sockets[c]
	if sock != nil && len(sock.active) == 0 && len(sock.terminal) == 0 {
		delete(s.sockets, c)
	}
	p := s.principals[filesKey(c)]
	if p != nil && p.active == 0 && p.order.Len() == 0 {
		delete(s.principals, filesKey(c))
	}
	for _, x := range []struct {
		m   map[string]*filesCounts
		key string
	}{{s.users, filesKey(c).user}, {s.workspaces, filesKey(c).workspace}} {
		if v := x.m[x.key]; v != nil && v.active == 0 && v.terminal == 0 {
			delete(x.m, x.key)
		}
	}
}

// removeTombstone is the sole terminal deletion path. All four list indices,
// ID lookup and aggregate counts are removed together, exactly once.
func (s *workspaceFilesState) removeTombstone(t *filesTombstone) {
	sock := s.sockets[t.owner]
	if sock == nil || sock.terminal[t.id] != t {
		return
	}
	p := s.principals[filesKey(t.owner)]
	delete(sock.terminal, t.id)
	sock.order.Remove(t.socket)
	p.order.Remove(t.principal)
	if t.class == filesReserve {
		p.reserve.Remove(t.classified)
		if p.reserve.Len() == 0 {
			delete(s.reserveOwners, filesKey(t.owner))
		}
		s.reserve--
	} else {
		p.incumbent.Remove(t.classified)
		s.incumbent--
	}
	s.terminal.Remove(t.global)
	s.users[filesKey(t.owner).user].terminal--
	s.workspaces[filesKey(t.owner).workspace].terminal--
	s.prune(t.owner)
}
func (s *workspaceFilesState) sweep(now time.Time) {
	// Insertions use the monotonic clock; TTL is identical for every entry.
	// Expiry removal is O(expired entries), never a scan of the 8192 ledger.
	for e := s.terminal.Front(); e != nil; e = s.terminal.Front() {
		t := e.Value.(*filesTombstone)
		if now.Before(t.expires) {
			break
		}
		s.removeTombstone(t)
	}
}
func filesOldest(l *list.List) *filesTombstone {
	if e := l.Front(); e != nil {
		return e.Value.(*filesTombstone)
	}
	return nil
}
func (s *workspaceFilesState) fairReserveVictim() *filesTombstone {
	var best *filesTombstone
	count := 0
	var key filesPrincipalKey
	// Only row 7 scans principals; at most 512 reserve entries/owners exist.
	for k, p := range s.reserveOwners {
		n := p.reserve.Len()
		if n == 0 {
			continue
		}
		t := filesOldest(&p.reserve)
		if best == nil || n > count || (n == count && (k.user < key.user || (k.user == key.user && k.workspace < key.workspace))) {
			best, count, key = t, n, k
		}
	}
	return best
}
func (s *workspaceFilesState) addTombstone(r *filesRecord) {
	now := s.now()
	s.sweep(now)
	sock := s.sockets[r.owner]
	p := s.principals[filesKey(r.owner)]
	var sl, pl, rl int
	if sock != nil {
		sl = sock.order.Len()
	}
	if p != nil {
		pl = p.order.Len()
		rl = p.reserve.Len()
	}
	g := s.terminal.Len()
	if s.sequence >= math.MaxUint64-1 || g != s.incumbent+s.reserve || s.incumbent < 0 || s.reserve < 0 || s.incumbent > filesHighWater || s.reserve > filesPrincipalTerminal || g > filesHighWater+s.reserve || sl > filesSocketTerminal || pl > filesPrincipalTerminal {
		s.corruption++
		return
	}
	var victim *filesTombstone
	class := filesClass(255)
	// Frozen v3.7 first-match table: no default victim or classification.
	switch {
	case sl == filesSocketTerminal:
		victim = filesOldest(&sock.order)
		class = victim.class
	case pl == filesPrincipalTerminal:
		victim = filesOldest(&p.order)
		class = victim.class
	case g < filesHighWater:
		class = filesIncumbent
	case g >= filesHighWater && rl > 0 && s.reserve < filesPrincipalTerminal:
		class = filesReserve
	case g >= filesHighWater && rl > 0 && s.reserve == filesPrincipalTerminal:
		victim = filesOldest(&p.reserve)
		class = filesReserve
	case g >= filesHighWater && pl == 0 && s.reserve < filesPrincipalTerminal:
		class = filesReserve
	case g >= filesHighWater && pl == 0 && s.reserve == filesPrincipalTerminal:
		victim = s.fairReserveVictim()
		class = filesReserve
		if victim == nil {
			s.corruption++
			return
		}
	case g >= filesHighWater && rl == 0 && pl > 0:
		victim = filesOldest(&p.incumbent)
		class = filesIncumbent
	default:
		s.corruption++
		return
	}
	if class != filesIncumbent && class != filesReserve {
		s.corruption++
		return
	}
	if victim != nil {
		s.removeTombstone(victim)
	}
	sock = s.sockets[r.owner]
	if sock == nil {
		sock = &filesSocket{active: map[string]*filesRecord{}, terminal: map[string]*filesTombstone{}}
		s.sockets[r.owner] = sock
	}
	p = s.principal(r.owner)
	s.sequence++
	t := &filesTombstone{owner: r.owner, id: r.request.ClientReqID, nonce: r.nonce, generation: r.generation, sequence: s.sequence, expires: now.Add(filesTTL), class: class}
	sock.terminal[t.id] = t
	t.socket = sock.order.PushBack(t)
	t.principal = p.order.PushBack(t)
	t.global = s.terminal.PushBack(t)
	if class == filesReserve {
		t.classified = p.reserve.PushBack(t)
		s.reserveOwners[filesKey(r.owner)] = p
		s.reserve++
	} else {
		t.classified = p.incumbent.PushBack(t)
		s.incumbent++
	}
	filesCounter(s.users, filesKey(r.owner).user).terminal++
	filesCounter(s.workspaces, filesKey(r.owner).workspace).terminal++
}
func (s *workspaceFilesState) contains(r *filesRecord) bool {
	sock := s.sockets[r.owner]
	return sock != nil && sock.active[r.request.ClientReqID] == r && r.nonce == r.owner.filesNonce
}
func (s *workspaceFilesState) accept(c *Client, event string, req protocol.WorkspaceFilesRequest, snap WorkspaceFilesSnapshot) (*filesRecord, string) {
	s.sweep(s.now())
	sock := s.sockets[c]
	if sock != nil && (sock.active[req.ClientReqID] != nil || sock.terminal[req.ClientReqID] != nil) {
		return nil, "invalid_request"
	}
	active := 0
	if sock != nil {
		active = len(sock.active)
	}
	p := s.principals[filesKey(c)]
	pa := 0
	if p != nil {
		pa = p.active
	}
	ua, wa := 0, 0
	if n := s.users[filesKey(c).user]; n != nil {
		ua = n.active
	}
	if n := s.workspaces[filesKey(c).workspace]; n != nil {
		wa = n.active
	}
	if active >= filesSocketActive || pa >= filesPrincipalActive || ua >= filesUserActive || wa >= filesWorkspaceActive || s.active >= filesGlobalActive {
		return nil, "busy"
	}
	if s.generation >= math.MaxUint64-1 {
		return nil, "unavailable"
	}
	if c.filesNonce == "" {
		c.filesNonce = uuid.NewString()
	}
	if sock == nil {
		sock = &filesSocket{active: map[string]*filesRecord{}, terminal: map[string]*filesTombstone{}}
		s.sockets[c] = sock
	}
	s.generation++
	ctx, cancel := context.WithTimeout(context.Background(), filesBudget)
	r := &filesRecord{owner: c, nonce: c.filesNonce, generation: s.generation, request: req, event: event, snapshot: snap, auth: s.auth, relay: s.relay, ctx: ctx, cancel: cancel}
	sock.active[req.ClientReqID] = r
	s.principal(c).active++
	filesCounter(s.users, filesKey(c).user).active++
	filesCounter(s.workspaces, filesKey(c).workspace).active++
	s.active++
	return r, ""
}
func (s *workspaceFilesState) retire(r *filesRecord, remember bool) bool {
	if !s.contains(r) {
		return false
	}
	delete(s.sockets[r.owner].active, r.request.ClientReqID)
	if r.target != nil && s.routes[r.target.Generation] == r {
		delete(s.routes, r.target.Generation)
	}
	s.principals[filesKey(r.owner)].active--
	s.users[filesKey(r.owner).user].active--
	s.workspaces[filesKey(r.owner).workspace].active--
	s.active--
	if r.timer != nil {
		r.timer.Stop()
	}
	r.cancel()
	if remember && !r.owner.filesClosed {
		s.addTombstone(r)
	}
	s.prune(r.owner)
	return true
}
