package realtime

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Recompute counts from entries, independently of the production counters.
func filesAssertLedger(t *testing.T, s *workspaceFilesState) {
	t.Helper()
	inc, res, active := 0, 0, 0
	principalTotals := map[filesPrincipalKey]int{}
	userTotals, wsTotals := map[string]filesCounts{}, map[string]filesCounts{}
	seen := map[*filesTombstone]bool{}
	var seq uint64
	for e := s.terminal.Front(); e != nil; e = e.Next() {
		x := e.Value.(*filesTombstone)
		if seen[x] || x.global != e || x.sequence <= seq {
			t.Fatal("global index/sequence")
		}
		seen[x] = true
		seq = x.sequence
		sock, p := s.sockets[x.owner], s.principals[filesKey(x.owner)]
		if sock == nil || p == nil || sock.terminal[x.id] != x || x.socket.Value != x || x.principal.Value != x || x.classified.Value != x {
			t.Fatal("missing secondary index")
		}
		if x.class == filesReserve {
			res++
		} else {
			inc++
		}
		principalTotals[filesKey(x.owner)]++
		u, w := userTotals[filesKey(x.owner).user], wsTotals[filesKey(x.owner).workspace]
		u.terminal++
		w.terminal++
		userTotals[filesKey(x.owner).user], wsTotals[filesKey(x.owner).workspace] = u, w
	}
	for c, sock := range s.sockets {
		if sock.order.Len() != len(sock.terminal) || sock.order.Len() > 256 || len(sock.active) > 16 || len(sock.terminal)+len(sock.active) == 0 {
			t.Fatal("socket bounds/zero owner")
		}
		for e := sock.order.Front(); e != nil; e = e.Next() {
			x := e.Value.(*filesTombstone)
			if !seen[x] || x.owner != c || x.socket != e {
				t.Fatal("socket index")
			}
		}
		for id, r := range sock.active {
			if id != r.request.ClientReqID || r.owner != c || !s.contains(r) {
				t.Fatal("active owner")
			}
			active++
			u, w := userTotals[filesKey(c).user], wsTotals[filesKey(c).workspace]
			u.active++
			w.active++
			userTotals[filesKey(c).user], wsTotals[filesKey(c).workspace] = u, w
		}
	}
	for k, p := range s.principals {
		if p.order.Len() != principalTotals[k] || p.order.Len() != p.incumbent.Len()+p.reserve.Len() || p.order.Len() > 512 || p.active > 64 || p.order.Len()+p.active == 0 {
			t.Fatal("principal bounds")
		}
		n := 0
		for c, sock := range s.sockets {
			if filesKey(c) == k {
				n += len(sock.active)
			}
		}
		if n != p.active {
			t.Fatal("principal active")
		}
		for _, cl := range []filesClass{filesIncumbent, filesReserve} {
			l := &p.incumbent
			if cl == filesReserve {
				l = &p.reserve
			}
			for e := l.Front(); e != nil; e = e.Next() {
				x := e.Value.(*filesTombstone)
				if !seen[x] || filesKey(x.owner) != k || x.class != cl || x.classified != e {
					t.Fatal("classification index")
				}
			}
		}
		if (p.reserve.Len() > 0) != (s.reserveOwners[k] == p) {
			t.Fatal("reserve owner index")
		}
	}
	for k, p := range s.reserveOwners {
		if s.principals[k] != p || p.reserve.Len() == 0 {
			t.Fatal("stale reserve owner")
		}
	}
	if len(userTotals) != len(s.users) || len(wsTotals) != len(s.workspaces) {
		t.Fatal("stale count owner")
	}
	for k, n := range userTotals {
		if s.users[k] == nil || *s.users[k] != n || n.active > 128 {
			t.Fatal("user count")
		}
	}
	for k, n := range wsTotals {
		if s.workspaces[k] == nil || *s.workspaces[k] != n || n.active > 256 {
			t.Fatal("workspace count")
		}
	}
	if active != s.active || active > 512 || inc != s.incumbent || res != s.reserve || inc > 7680 || res > 512 || len(seen) != s.terminal.Len() || len(seen) > 7680+res {
		t.Fatal("global count/invariant")
	}
	for g, r := range s.routes {
		if !s.contains(r) || r.target == nil || r.target.Generation != g {
			t.Fatal("stale route")
		}
	}
}

func TestWorkspaceFilesLedgerCanonicalIdentityQuotas(t *testing.T) {
	s := newWorkspaceFilesState()
	user, workspace := uuid.NewString(), uuid.NewString()
	records := []*filesRecord{}
	for i := 0; i < 64; i++ {
		u, w := user, workspace
		if i%2 == 0 {
			u, w = strings.ToUpper(u), strings.ToUpper(w)
		}
		c := &Client{userID: u, workspaceID: w}
		r, code := s.accept(c, "read", protocol.WorkspaceFilesRequest{ClientReqID: uuid.NewString()}, WorkspaceFilesSnapshot{})
		if code != "" {
			t.Fatal(code)
		}
		records = append(records, r)
	}
	c := &Client{userID: user, workspaceID: strings.ToUpper(workspace)}
	if r, code := s.accept(c, "read", protocol.WorkspaceFilesRequest{ClientReqID: uuid.NewString()}, WorkspaceFilesSnapshot{}); r != nil || code != "busy" {
		t.Fatal("UUID casing bypassed principal quota")
	}
	if len(s.principals) != 1 || len(s.users) != 1 || len(s.workspaces) != 1 {
		t.Fatal("canonical owners split")
	}
	filesAssertLedger(t, s)
	for _, r := range records {
		s.retire(r, true)
	}
	filesAssertLedger(t, s)
	filesLedgerCloseAll(t, s)
}
func filesLedgerTerminal(t *testing.T, s *workspaceFilesState, c *Client) *filesTombstone {
	t.Helper()
	id := uuid.NewString()
	r, code := s.accept(c, protocol.EventWorkspaceFilesRead, protocol.WorkspaceFilesRequest{ClientReqID: id}, WorkspaceFilesSnapshot{})
	if code != "" {
		t.Fatal(code)
	}
	if !s.retire(r, true) {
		t.Fatal("retire")
	}
	if sock := s.sockets[c]; sock != nil {
		return sock.terminal[id]
	}
	return nil
}
func filesLedgerFixture(t *testing.T, reserve int, expirePrefix ...int) (*workspaceFilesState, []*Client) {
	t.Helper()
	s := newWorkspaceFilesState()
	clock := time.Unix(100, 0)
	s.now = func() time.Time { return clock }
	inserted := 0
	cs := []*Client{}
	for p := 0; p < 16; p++ {
		for socket := 0; socket < 2; socket++ {
			c := &Client{userID: fmt.Sprintf("%02d", p), workspaceID: "ws"}
			cs = append(cs, c)
			n := 256
			if p == 15 {
				n = reserve - socket*256
				if n < 0 {
					n = 0
				}
				if n > 256 {
					n = 256
				}
			}
			for i := 0; i < n; i++ {
				if len(expirePrefix) > 0 && expirePrefix[0] > 0 && inserted == expirePrefix[0] {
					clock = time.Unix(101, 0)
				}
				filesLedgerTerminal(t, s, c)
				inserted++
			}
		}
	}
	if len(expirePrefix) > 0 && expirePrefix[0] > 0 {
		clock = time.Unix(160, 0)
	}
	filesAssertLedger(t, s)
	return s, cs
}
func TestWorkspaceFilesLedgerEightRows(t *testing.T) {
	t.Run("row3 incumbent", func(t *testing.T) {
		s := newWorkspaceFilesState()
		c := &Client{userID: "u", workspaceID: "w"}
		x := filesLedgerTerminal(t, s, c)
		if x.class != filesIncumbent {
			t.Fatal("class")
		}
		filesAssertLedger(t, s)
	})
	t.Run("rows6 and4 grow reserve", func(t *testing.T) {
		s, cs := filesLedgerFixture(t, 0)
		a := filesLedgerTerminal(t, s, cs[30])
		b := filesLedgerTerminal(t, s, cs[30])
		if a.class != filesReserve || b.class != filesReserve || s.incumbent != 7680 || s.reserve != 2 {
			t.Fatal("reserve growth")
		}
		filesAssertLedger(t, s)
	})
	t.Run("row1 socket inherits", func(t *testing.T) {
		s, cs := filesLedgerFixture(t, 512)
		old := filesOldest(&s.sockets[cs[30]].order)
		x := filesLedgerTerminal(t, s, cs[30])
		if x.class != filesReserve || s.sockets[cs[30]].terminal[old.id] != nil || s.terminal.Len() != 8192 {
			t.Fatal("socket victim")
		}
		filesAssertLedger(t, s)
	})
	t.Run("row2 principal transfers sockets", func(t *testing.T) {
		s, cs := filesLedgerFixture(t, 512)
		old := filesOldest(&s.principals[filesKey(cs[0])].order)
		c := &Client{userID: cs[0].userID, workspaceID: "ws"}
		x := filesLedgerTerminal(t, s, c)
		if x.class != filesIncumbent || s.sockets[old.owner].order.Len() != 255 || s.sockets[c].order.Len() != 1 || s.terminal.Len() != 8192 {
			t.Fatal("principal victim/counters")
		}
		filesAssertLedger(t, s)
		filesLedgerCloseAll(t, s, old.owner, c)
	})
	t.Run("row2 reserve class transfers sockets", func(t *testing.T) {
		s, cs := filesLedgerFixture(t, 512)
		old := filesOldest(&s.principals[filesKey(cs[30])].order)
		c := &Client{userID: cs[30].userID, workspaceID: "ws"}
		x := filesLedgerTerminal(t, s, c)
		if x.class != filesReserve || s.sockets[old.owner].order.Len() != 255 || s.sockets[c].order.Len() != 1 || s.principals[filesKey(c)].order.Len() != 512 || s.incumbent != 7680 || s.reserve != 512 {
			t.Fatal("principal reserve inheritance")
		}
		filesAssertLedger(t, s)
		filesLedgerCloseAll(t, s, old.owner, c)
	})
	for _, removed := range []int{0, 1, 512} {
		t.Run(fmt.Sprintf("row7 actual global minus %d", removed), func(t *testing.T) {
			s, cs := filesLedgerFixture(t, 512, removed)
			s.sweep(s.now())
			g, inc, res := s.terminal.Len(), s.incumbent, s.reserve
			old := filesOldest(&s.principals[filesKey(cs[30])].reserve)
			c := &Client{userID: "16", workspaceID: "ws"}
			x := filesLedgerTerminal(t, s, c)
			if x.class != filesReserve || s.terminal.Len() != g || s.incumbent != inc || s.reserve != res || s.sockets[old.owner].order.Len() != 255 || s.sockets[c].order.Len() != 1 {
				t.Fatal("row7 must preserve actual global")
			}
			filesAssertLedger(t, s)
			filesLedgerCloseAll(t, s, old.owner, c)
		})
	}
	t.Run("row5 reserve owner at full reserve", func(t *testing.T) {
		s, cs := filesLedgerFixture(t, 512)
		s.removeTombstone(filesOldest(&s.sockets[cs[30]].order))
		filesLedgerTerminal(t, s, &Client{userID: "16", workspaceID: "ws"})
		old := filesOldest(&s.principals[filesKey(cs[30])].reserve)
		c := &Client{userID: "15", workspaceID: "ws"}
		filesLedgerTerminal(t, s, c)
		if s.sockets[old.owner].terminal[old.id] != nil || s.reserve != 512 || s.principals[filesKey(c)].order.Len() != 511 {
			t.Fatal("row5")
		}
		filesAssertLedger(t, s)
		filesLedgerCloseAll(t, s, old.owner, c)
	})
	t.Run("row8 incumbent self victim", func(t *testing.T) {
		s, cs := filesLedgerFixture(t, 512)
		s.removeTombstone(filesOldest(&s.sockets[cs[0]].order))
		old := filesOldest(&s.principals[filesKey(cs[0])].incumbent)
		c := &Client{userID: "00", workspaceID: "ws"}
		x := filesLedgerTerminal(t, s, c)
		if x.class != filesIncumbent || s.sockets[old.owner].terminal[old.id] != nil || s.terminal.Len() != 8191 {
			t.Fatal("row8")
		}
		filesAssertLedger(t, s)
		filesLedgerCloseAll(t, s, old.owner, c)
	})
}
func filesLedgerCloseAll(t *testing.T, s *workspaceFilesState, first ...*Client) {
	t.Helper()
	h := NewHub()
	h.files = s
	cs := append([]*Client{}, first...)
	for c := range s.sockets {
		cs = append(cs, c)
	}
	for _, c := range cs {
		h.workspaceFilesClientGone(c)
		h.workspaceFilesClientGone(c)
		filesAssertLedger(t, s)
	}
	if len(s.sockets)+len(s.principals)+len(s.reserveOwners)+len(s.users)+len(s.workspaces)+len(s.routes)+s.terminal.Len()+s.active != 0 {
		t.Fatal("close left indices")
	}
}
func TestWorkspaceFilesLedgerMixedTieTTLAndOnce(t *testing.T) {
	s, cs := filesLedgerFixture(t, 256)
	other := &Client{userID: "16", workspaceID: "ws"}
	for i := 0; i < 256; i++ {
		filesLedgerTerminal(t, s, other)
	}
	old := filesOldest(&s.principals[filesKey(cs[30])].reserve)
	filesLedgerTerminal(t, s, &Client{userID: "17", workspaceID: "ws"})
	if s.sockets[old.owner].terminal[old.id] != nil {
		t.Fatal("lexical tie")
	}
	filesAssertLedger(t, s)
	s, cs = filesLedgerFixture(t, 1)
	s.removeTombstone(filesOldest(&s.terminal))
	s.removeTombstone(filesOldest(&s.terminal))
	inc := filesLedgerTerminal(t, s, cs[30])
	res := filesLedgerTerminal(t, s, cs[30])
	if inc.class != filesIncumbent || res.class != filesReserve {
		t.Fatal("mixed class crossing H")
	}
	filesAssertLedger(t, s)
	s.removeTombstone(inc)
	s.removeTombstone(inc)
	filesAssertLedger(t, s)
	s.sweep(time.Unix(160, 0))
	s.sweep(time.Unix(161, 0))
	filesAssertLedger(t, s)
	if len(s.sockets)+len(s.principals)+len(s.reserveOwners)+len(s.users)+len(s.workspaces)+s.terminal.Len() != 0 {
		t.Fatal("expiry did not remove all indices")
	}
	s.sequence = math.MaxUint64 - 1
	filesLedgerTerminal(t, s, &Client{userID: "x", workspaceID: "w"})
	if s.terminal.Len() != 0 || s.corruption != 1 {
		t.Fatal("insert seq exhaustion")
	}
}
func TestWorkspaceFilesLedgerActiveQuotas(t *testing.T) {
	for _, tc := range []struct {
		name    string
		n       int
		owner   func(int) *Client
		blocked func([]*Client) *Client
	}{
		{"socket", 16, func(int) *Client { return nil }, func(cs []*Client) *Client { return cs[0] }},
		{"principal", 64, func(i int) *Client { return &Client{userID: "u", workspaceID: "w"} }, func([]*Client) *Client { return &Client{userID: "u", workspaceID: "w"} }},
		{"user", 128, func(i int) *Client { return &Client{userID: "u", workspaceID: fmt.Sprint(i / 64)} }, func([]*Client) *Client { return &Client{userID: "u", workspaceID: "third"} }},
		{"workspace", 256, func(i int) *Client { return &Client{userID: fmt.Sprint(i / 64), workspaceID: "w"} }, func([]*Client) *Client { return &Client{userID: "new", workspaceID: "w"} }},
		{"global", 512, func(i int) *Client { return &Client{userID: fmt.Sprint(i / 64), workspaceID: fmt.Sprint(i / 256)} }, func([]*Client) *Client { return &Client{userID: "new", workspaceID: "new"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newWorkspaceFilesState()
			cs := []*Client{}
			rs := []*filesRecord{}
			same := &Client{userID: "u", workspaceID: "w"}
			for i := 0; i < tc.n; i++ {
				c := tc.owner(i)
				if c == nil {
					c = same
				}
				cs = append(cs, c)
				r, code := s.accept(c, "read", protocol.WorkspaceFilesRequest{ClientReqID: uuid.NewString()}, WorkspaceFilesSnapshot{})
				if code != "" {
					t.Fatal(code)
				}
				rs = append(rs, r)
			}
			c := tc.blocked(cs)
			id := uuid.NewString()
			r, code := s.accept(c, "read", protocol.WorkspaceFilesRequest{ClientReqID: id}, WorkspaceFilesSnapshot{})
			if r != nil || code != "busy" || s.terminal.Len() != 0 {
				t.Fatal("busy allocated")
			}
			filesAssertLedger(t, s)
			for _, r := range rs {
				if !s.retire(r, false) || s.retire(r, false) {
					t.Fatal("once-only retire")
				}
			}
			r, code = s.accept(c, "read", protocol.WorkspaceFilesRequest{ClientReqID: id}, WorkspaceFilesSnapshot{})
			if code != "" {
				t.Fatal("busy id not reusable")
			}
			s.retire(r, false)
			filesAssertLedger(t, s)
		})
	}
}
