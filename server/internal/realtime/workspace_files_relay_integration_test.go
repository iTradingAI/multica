package realtime_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestWorkspaceFilesRelayEvidenceE01(t *testing.T) {
	for _, level := range []string{"socket", "principal", "user", "workspace"} {
		t.Run("active_"+level+"_A_busy_B_accepts", func(t *testing.T) {
			e := newRelayEvidenceEnv(t, 6)
			w := e.natives[0].wire
			w.setHold(true)
			owners := []int{0}
			perOwner := []int{16}
			rejectPrincipal, bPrincipal := 0, 5
			switch level {
			case "principal":
				perOwner = []int{64}
			case "user":
				owners = []int{0, 1, 2}
				perOwner = []int{48, 48, 32}
				rejectPrincipal = 2
				bPrincipal = 3
				for _, i := range []int{1, 2} {
					p := e.principals[i]
					p.user = e.principals[0].user
					e.db.f.Exec(t, `UPDATE member SET user_id=$1 WHERE id=$2`, p.user, p.member)
				}
			case "workspace":
				owners = []int{0, 1, 2, 3}
				perOwner = []int{64, 64, 64, 64}
				rejectPrincipal = 4
				bPrincipal = 5
				for _, i := range []int{1, 2, 3, 4} {
					p := e.principals[i]
					p.workspace = e.principals[0].workspace
					e.db.f.Exec(t, `UPDATE member SET workspace_id=$1 WHERE id=$2`, p.workspace, p.member)
					e.db.f.Exec(t, `UPDATE project SET workspace_id=$1 WHERE id=$2`, p.workspace, p.project)
					e.db.f.Exec(t, `UPDATE issue SET workspace_id=$1 WHERE id=$2`, p.workspace, p.issue)
					e.db.f.Exec(t, `UPDATE project_resource SET workspace_id=$1 WHERE id=$2`, p.workspace, p.resource)
				}
			}
			for oi, i := range owners {
				p := e.principals[i]
				for count := 0; count < perOwner[oi]; count++ {
					if count%16 == 0 {
						e.open(p)
					}
					b := p.browsers[len(p.browsers)-1]
					id := uuid.NewString()
					e.request(b, p, id, "small.txt")
					r := e.record(b, id)
					_ = w.take(t, r.Target.Generation)
				}
			}
			e.observe("A-active-" + level + "-limit")
			p := e.principals[rejectPrincipal]
			var rejected *relayEvidenceBrowser
			if level == "socket" {
				rejected = p.browsers[0]
			} else {
				rejected = e.open(p)
			}
			before := w.requestsCount()
			id := uuid.NewString()
			e.request(rejected, p, id, "small.txt")
			e.error(rejected, id, "busy")
			if w.requestsCount() != before {
				t.Fatal("busy request was forwarded to native daemon")
			}
			pb := e.principals[bPrincipal]
			b := e.open(pb)
			bid := uuid.NewString()
			e.request(b, pb, bid, "small.txt")
			r := e.record(b, bid)
			frame := w.take(t, r.Target.Generation)
			w.send(t, frame)
			pout := e.chunk(b, bid, 0)
			if !pout.EOF || string(pout.Data) != pb.content {
				t.Fatal("B was not accepted independently of A limit")
			}
			w.barrier(t)
			e.observe("A-busy-zero-forward-B-native-EOF-" + level)
			e.close()
		})
	}
	t.Run("terminal_global_reserve_exact_victims", func(t *testing.T) {
		e := newRelayEvidenceEnv(t, 18)
		for _, p := range e.principals {
			e.open(p)
			e.open(p)
		}
		for i := 0; i < 15; i++ {
			for _, b := range e.principals[i].browsers {
				for j := 0; j < 256; j++ {
					e.read(b, e.principals[i])
				}
			}
			e.observe(fmt.Sprintf("incumbent-principal-%02d-filled", i))
		}
		relayEvidenceTotals(t, e, 7680, 0, 7680)
		// A terminal socket/principal at its cap does not prevent B's real read.
		p16 := e.principals[15]
		p17 := e.principals[16]
		p18 := e.principals[17]
		for _, b := range p16.browsers {
			for j := 0; j < 256; j++ {
				e.read(b, p16)
			}
		}
		relayEvidenceTotals(t, e, 7680, 512, 8192)
		e.observe("15x512-incumbent-plus-16th-512-reserve")
		victim := relayEvidenceOldest(t, e, p16, int(realtime.RelayEvidenceReserve))
		beforeCounts := relayEvidenceSocketCounts(e, p16.browsers[0], p17.browsers[0])
		inserted := e.read(p17.browsers[0], p17)
		relayEvidenceVictim(t, e, "17th-zero-entrant", victim, p17.browsers[0], inserted, beforeCounts)
		relayEvidenceTotals(t, e, 7680, 512, 8192)
		e.observe("17th-entrant-exact-reserve-victim")
		// Reserve is full; this owner has room below socket/principal caps.
		victim = relayEvidenceOldest(t, e, p16, int(realtime.RelayEvidenceReserve))
		// Capture the actual victim socket independently of requester identity.
		vb := relayEvidenceOwnerBrowser(e, victim.Owner)
		beforeCounts = relayEvidenceSocketCounts(e, vb, p16.browsers[0])
		inserted = e.read(p16.browsers[0], p16)
		relayEvidenceVictim(t, e, "reserve-self-replace", victim, p16.browsers[0], inserted, beforeCounts)
		relayEvidenceTotals(t, e, 7680, 512, 8192)
		e.observe("reserve-owner-self-replacement")
		// Real close deletes 255 reserves. Real reads grow the other owner to
		// 256:256. Both owners share a user; workspace UUID decides the tie.
		p16.browsers[0].close()
		relayEvidenceWait(t, "closed reserve socket removed", func() bool {
			return realtime.RelayEvidenceClientGone(e.h, p16.browsers[0].owner)
		})
		for j := 0; j < 255; j++ {
			e.read(p17.browsers[0], p17)
		}
		relayEvidenceTotals(t, e, 7680, 512, 8192)
		a := realtime.RelayEvidenceReserveCount(e.h, p16.user, p16.workspace)
		b := realtime.RelayEvidenceReserveCount(e.h, p17.user, p17.workspace)
		if a != 256 || b != 256 || p16.user != p17.user || !(p17.workspace < p16.workspace) {
			t.Fatal("reserve lexical tie fixture unreachable")
		}
		victim = relayEvidenceOldest(t, e, p17, int(realtime.RelayEvidenceReserve))
		vb = relayEvidenceOwnerBrowser(e, victim.Owner)
		beforeCounts = relayEvidenceSocketCounts(e, vb, p18.browsers[0])
		inserted = e.read(p18.browsers[0], p18)
		relayEvidenceVictim(t, e, "reserve-256-256-workspace-tie", victim, p18.browsers[0], inserted, beforeCounts)
		relayEvidenceTotals(t, e, 7680, 512, 8192)
		e.observe("reserve-tie-exact-lexical-victim")
		e.close()
	})
}

func relayEvidenceTotals(t *testing.T, e *relayEvidenceEnv, i, r, g int) {
	realtime.RelayEvidenceTotals(t, e.h, i, r, g)
}
func relayEvidenceOldest(t *testing.T, e *relayEvidenceEnv, p *relayEvidencePrincipal, class int) *realtime.RelayEvidenceVictim {
	return realtime.RelayEvidenceOldest(t, e.h, p.user, p.workspace, class)
}
func relayEvidenceOwnerBrowser(e *relayEvidenceEnv, c *realtime.Client) *relayEvidenceBrowser {
	for _, b := range e.browsers {
		if b.owner == c {
			return b
		}
	}
	panic("victim is not a real browser owner")
}
func relayEvidenceSocketCounts(e *relayEvidenceEnv, a, b *relayEvidenceBrowser) map[*realtime.Client]int {
	return realtime.RelayEvidenceSocketCounts(e.h, a.owner, b.owner)
}
func relayEvidenceVictim(t *testing.T, e *relayEvidenceEnv, label string, v *realtime.RelayEvidenceVictim, b *relayEvidenceBrowser, id string, before map[*realtime.Client]int) {
	t.Logf("VICTIM %s", mustRelayEvidenceJSON(realtime.RelayEvidenceAssertVictim(t, e.h, label, v, b.owner, id, before)))
}

func TestWorkspaceFilesRelayEvidenceE02(t *testing.T) {
	t.Run("forced_daemon_ID_reuse_old_seq_and_epoch", func(t *testing.T) {
		e := newRelayEvidenceEnv(t, 2)
		p := e.principals[0]
		w := e.natives[0].wire
		w.setHold(true)
		constant := uuid.NewString()
		realtime.RelayEvidenceRequestID(e.h, constant)
		b := e.open(p)
		id := uuid.NewString()
		e.request(b, p, id, "small.txt")
		old := e.record(b, id)
		oldFrame := w.take(t, old.Target.Generation)
		w.send(t, oldFrame)
		e.chunk(b, id, 0)
		w.barrier(t)
		b.close()
		relayEvidenceWait(t, "old browser removed", func() bool { return realtime.RelayEvidenceClientGone(e.h, b.owner) })
		b = e.open(p)
		e.request(b, p, id, "small.txt")
		fresh := e.record(b, id)
		freshFrame := w.take(t, fresh.Target.Generation)
		if old.Target.RequestID != fresh.Target.RequestID || old.Target.Generation.ConnectionEpoch != fresh.Target.Generation.ConnectionEpoch || old.Target.Generation.RelaySeq == fresh.Target.Generation.RelaySeq {
			t.Fatal("forced same-ID/old-seq setup failed")
		}
		w.send(t, oldFrame)
		w.barrier(t)
		relayEvidenceUnchanged(t, e, fresh, 0, 0)
		w.send(t, freshFrame)
		got := e.chunk(b, id, 0)
		if !got.EOF || string(got.Data) != p.content {
			t.Fatal("fresh record was changed by old seq")
		}
		if b.count() != 2 {
			t.Fatal("old seq reached the browser")
		}
		oldNative := e.natives[0]
		oldNative.wire.close()
		relayEvidenceWait(t, "old daemon offline", func() bool { return e.dh.RuntimeConnectionCount(oldNative.runtimes[0]) == 0 })
		n := e.startNative(e.db.daemon, true)
		b.close()
		b = e.open(p)
		e.request(b, p, id, "small.txt")
		newEpoch := e.record(b, id)
		newFrame := n.wire.take(t, newEpoch.Target.Generation)
		if newEpoch.Target.RequestID != constant || newEpoch.Target.Generation.ConnectionEpoch == old.Target.Generation.ConnectionEpoch {
			t.Fatal("epoch reuse setup failed")
		}
		// Keep the new seq while changing only epoch to the old connection.
		var stale protocol.WorkspaceFilesReadChunkPayload
		_ = json.Unmarshal(newFrame.Payload, &stale)
		stale.ConnectionEpoch = old.Target.Generation.ConnectionEpoch
		raw, _ := json.Marshal(stale)
		n.wire.send(t, protocol.Message{Type: newFrame.Type, Payload: raw})
		n.wire.barrier(t)
		relayEvidenceUnchanged(t, e, newEpoch, 0, 0)
		n.wire.send(t, newFrame)
		got = e.chunk(b, id, 0)
		if !got.EOF || string(got.Data) != p.content || b.count() != 2 {
			t.Fatal("old epoch reached/replaced new browser record")
		}
		t.Logf("GENERATION constant daemon_req_id=%s old=%s fresh=%s new_epoch=%s; old frames dropped without changing new pointer/nonce/seq/bytes", constant, mustRelayEvidenceJSON(old.Target.Generation), mustRelayEvidenceJSON(fresh.Target.Generation), mustRelayEvidenceJSON(newEpoch.Target.Generation))
		e.close()
	})
	t.Run("resources_reverse_and_disconnect_late_ID_reuse", func(t *testing.T) {
		e := newRelayEvidenceEnv(t, 2)
		p := e.principals[0]
		// Add an authorized second project in the same user's workspace.
		project := e.db.f.Insert(t, "project", testutil.Cols{"workspace_id": p.workspace})
		g1, g2 := e.auth.gate(p.project), e.auth.gate(project)
		b := e.open(p)
		id1, id2 := uuid.NewString(), uuid.NewString()
		relayEvidenceResources(b, t, id1, p.project)
		relayEvidenceSignal(t, g1.listed)
		relayEvidenceResources(b, t, id2, project)
		relayEvidenceSignal(t, g2.listed)
		close(g2.release)
		relayEvidenceResourcesResult(t, e, b, id2, project)
		close(g1.release)
		relayEvidenceResourcesResult(t, e, b, id1, p.project)
		if b.count() != 3 {
			t.Fatal("resources reverse attribution mismatch")
		}
		relayEvidenceWait(t, "reverse resource callbacks completed", func() bool { return realtime.RelayEvidenceResourceWorkers() == 0 })
		late := e.auth.gate(p.project)
		id := uuid.NewString()
		relayEvidenceResources(b, t, id, p.project)
		relayEvidenceSignal(t, late.listed)
		old := realtime.RelayEvidenceRecordFor(e.h, b.owner, id)
		if old == nil {
			t.Fatal("old resources request missing")
		}
		e.auth.mu.Lock()
		e.auth.late[p.project] = late
		e.auth.mu.Unlock()
		b.close()
		relayEvidenceWait(t, "old resources retired", func() bool { return !realtime.RelayEvidenceContains(e.h, old) })
		freshGate := e.auth.gate(p.project)
		freshB := e.open(p)
		relayEvidenceResources(freshB, t, id, p.project)
		relayEvidenceSignal(t, freshGate.listed)
		fresh := realtime.RelayEvidenceRecordFor(e.h, freshB.owner, id)
		close(late.release)
		relayEvidenceSignal(t, late.authorized)
		relayEvidenceWait(t, "old callback returned while new callback stays blocked", func() bool { return realtime.RelayEvidenceResourceWorkers() == 1 })
		relayEvidenceUnchanged(t, e, fresh, 0, 0)
		close(freshGate.release)
		relayEvidenceResourcesResult(t, e, freshB, id, p.project)
		if freshB.count() != 2 {
			t.Fatal("closed-socket resources callback was delivered to fresh owner")
		}
		t.Log("RESOURCES reverse replies match their IDs/contexts; closed old owner and reused client ID isolated; real SQL results delayed after query")
		e.close()
	})
	for _, mutation := range []string{"member", "root", "daemon", "issue-project"} {
		t.Run("after_first_chunk_"+mutation, func(t *testing.T) {
			e := newRelayEvidenceEnv(t, 2)
			p := e.principals[0]
			w := e.natives[0].wire
			w.setHold(true)
			b := e.open(p)
			id := uuid.NewString()
			e.request(b, p, id, "stream.txt")
			r := e.record(b, id)
			first := w.take(t, r.Target.Generation)
			w.send(t, first)
			out := e.chunk(b, id, 0)
			if out.EOF || len(out.Data) == 0 || !strings.HasPrefix(string(out.Data), p.content) {
				t.Fatal("first native stream frame setup")
			}
			relayEvidenceUnchanged(t, e, r, 1, len(out.Data))
			var alternate *relayEvidenceNative
			switch mutation {
			case "member":
				e.db.f.Exec(t, `DELETE FROM member WHERE id=$1`, p.member)
			case "root":
				e.db.f.Exec(t, `UPDATE project_resource SET resource_ref=$1 WHERE id=$2`, mustRelayEvidenceJSON(map[string]string{"local_path": e.principals[1].root, "daemon_id": e.db.daemon}), p.resource)
			case "daemon":
				nextDaemon := uuid.NewString()
				alternate = e.startNative(nextDaemon, true)
				e.db.f.Exec(t, `UPDATE project_resource SET resource_ref=$1 WHERE id=$2`, mustRelayEvidenceJSON(map[string]string{"local_path": p.root, "daemon_id": nextDaemon}), p.resource)
			case "issue-project":
				nextProject := e.db.f.Insert(t, "project", testutil.Cols{"workspace_id": p.workspace})
				e.db.f.Exec(t, `UPDATE issue SET project_id=$1 WHERE id=$2`, nextProject, p.issue)
			}
			beforeCancel := w.count(protocol.EventWorkspaceFilesCancel)
			next := w.take(t, r.Target.Generation)
			w.send(t, next)
			e.error(b, id, "forbidden")
			relayEvidenceWait(t, "cancel old target", func() bool { return w.count(protocol.EventWorkspaceFilesCancel) == beforeCancel+1 })
			relayEvidenceCancel(t, w, r)
			w.send(t, next)
			w.barrier(t)
			relayEvidenceTerminalOnce(t, e, b, id, 3)
			if alternate != nil && alternate.wire.requestsCount() != 0 {
				t.Fatal("rebound daemon received stale request")
			}
			t.Logf("STREAM mutation=%s first_bytes=%d next_content=0 old-target-cancel=1 terminal=1", mutation, len(out.Data))
			e.observe("stream-" + mutation + "-retired")
			e.close()
		})
	}
	t.Run("bound_disconnect_no_reselect_with_live_alternate", func(t *testing.T) {
		e := newRelayEvidenceEnv(t, 2)
		p := e.principals[0]
		old := e.natives[0]
		old.wire.setHold(true)
		b := e.open(p)
		id := uuid.NewString()
		e.request(b, p, id, "stream.txt")
		r := e.record(b, id)
		first := old.wire.take(t, r.Target.Generation)
		old.wire.send(t, first)
		e.chunk(b, id, 0)
		alternate := e.startNative(e.db.daemon, true)
		old.wire.close()
		e.error(b, id, "daemon_offline")
		relayEvidenceWait(t, "old runtime offline", func() bool { return e.dh.RuntimeConnectionCount(old.runtimes[0]) == 0 })
		if e.dh.RuntimeConnectionCount(alternate.runtimes[0]) != 1 || alternate.wire.requestsCount() != 0 {
			t.Fatal("bound request was reselected to live alternate")
		}
		alternate.wire.send(t, first)
		alternate.wire.barrier(t)
		relayEvidenceTerminalOnce(t, e, b, id, 3)
		t.Log("DISCONNECT live alternate runtime=1; alternate requests=0; old bound record terminal=1 and old frame rejected")
		e.close()
	})
	for _, order := range []string{"unsupported-first", "disconnect-first"} {
		t.Run(order, func(t *testing.T) {
			e := newRelayEvidenceEnv(t, 2)
			p := e.principals[0]
			w := e.natives[0].wire
			w.setHold(true)
			b := e.open(p)
			id := uuid.NewString()
			e.request(b, p, id, "small.txt")
			r := e.record(b, id)
			_ = w.take(t, r.Target.Generation)
			raw, _ := json.Marshal(protocol.WorkspaceFilesErrorPayload{WorkspaceFilesGeneration: r.Target.Generation, DaemonReqID: r.Target.RequestID, RuntimeID: r.Target.RuntimeID, ResourceID: p.resource, Code: "unsupported"})
			m := protocol.Message{Type: protocol.EventWorkspaceFilesError, Payload: raw}
			if order == "unsupported-first" {
				w.send(t, m)
				w.barrier(t)
				e.error(b, id, "daemon_upgrade_required")
				w.close()
				relayEvidenceWait(t, "runtime disconnect", func() bool { return e.dh.RuntimeConnectionCount(e.natives[0].runtimes[0]) == 0 })
			} else {
				e.bridge.mu.Lock()
				e.bridge.delay = true
				e.bridge.mu.Unlock()
				w.send(t, m)
				var delivery *relayEvidenceDelivery
				select {
				case delivery = <-e.bridge.delayed:
				case <-time.After(5 * time.Second):
					t.Fatal("real WS unsupported callback not captured")
				}
				w.close()
				e.error(b, id, "daemon_offline")
				close(delivery.release)
				relayEvidenceSignal(t, delivery.done)
			}
			relayEvidenceTerminalOnce(t, e, b, id, 2)
			e.observe(order + "-single-terminal")
			t.Logf("ORDER %s one error, one tombstone, active/routes=0; no path metadata", order)
			e.close()
		})
	}
}

func relayEvidenceUnchanged(t *testing.T, e *relayEvidenceEnv, r *realtime.RelayEvidenceRecord, seq, n int) {
	realtime.RelayEvidenceUnchanged(t, e.h, r, seq, n)
}
func relayEvidenceResources(b *relayEvidenceBrowser, t *testing.T, id, project string) {
	b.send(t, protocol.EventWorkspaceFilesResources, protocol.WorkspaceFilesClientResourcesPayload{ClientReqID: id, Context: protocol.WorkspaceFilesContext{Kind: "project", ProjectID: project}})
}
func relayEvidenceResourcesResult(t *testing.T, e *relayEvidenceEnv, b *relayEvidenceBrowser, id, project string) {
	t.Helper()
	m := b.next(t)
	e.safe(m)
	var p protocol.WorkspaceFilesClientResourcesResultPayload
	if m.Type != protocol.EventWorkspaceFilesResourcesResult || json.Unmarshal(m.Payload, &p) != nil || p.ClientReqID != id || p.Context.ProjectID != project {
		t.Fatal("resources ID/context attribution")
	}
	t.Logf("safe resources frame: %s", m.Payload)
}
func relayEvidenceCancel(t *testing.T, w *relayEvidenceWire, r *realtime.RelayEvidenceRecord) {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, m := range w.requests {
		if m.Type != protocol.EventWorkspaceFilesCancel {
			continue
		}
		var p protocol.WorkspaceFilesCancelPayload
		_ = json.Unmarshal(m.Payload, &p)
		if p.DaemonReqID == r.Target.RequestID && p.WorkspaceFilesGeneration == r.Target.Generation && p.RuntimeID == r.Target.RuntimeID {
			n++
		}
	}
	if n != 1 {
		t.Fatal("cancel did not bind exactly once to original target generation/runtime")
	}
	t.Logf("CANCEL original_target generation=%s runtime=%s daemon_req_id=%s count=1", mustRelayEvidenceJSON(r.Target.Generation), r.Target.RuntimeID, r.Target.RequestID)
}
func relayEvidenceTerminalOnce(t *testing.T, e *relayEvidenceEnv, b *relayEvidenceBrowser, id string, frames int) {
	t.Helper()
	realtime.RelayEvidenceTerminalOnce(t, e.h, b.owner, id)
	if b.count() != frames {
		t.Fatalf("content/duplicate terminal frame count=%d want=%d", b.count(), frames)
	}
}
