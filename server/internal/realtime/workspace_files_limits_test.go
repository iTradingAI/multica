package realtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func filesListRequest(c *Client, snap WorkspaceFilesSnapshot, id string) {
	raw, _ := json.Marshal(protocol.WorkspaceFilesClientListPayload{ClientReqID: id, Context: protocol.WorkspaceFilesContext{Kind: "project", ProjectID: snap.ProjectID}, ResourceID: snap.ResourceID, Path: ".", PageSize: 200})
	c.handleWorkspaceFilesClientFrame(protocol.EventWorkspaceFilesList, raw)
}
func filesListFrame(r *filesRecord, seq int, final bool) protocol.WorkspaceFilesListResultPayload {
	return protocol.WorkspaceFilesListResultPayload{WorkspaceFilesGeneration: r.target.Generation, DaemonReqID: r.target.RequestID, RuntimeID: r.target.RuntimeID, ResourceID: r.snapshot.ResourceID, Seq: seq, Entries: []protocol.WorkspaceFilesEntry{{Name: "file.txt", Type: "regular"}}, Final: final}
}
func TestWorkspaceFilesListProjectionAndLimits(t *testing.T) {
	h, c, a, relay := filesTestHub(t)
	id := uuid.NewString()
	filesListRequest(c, a.snapshot, id)
	r := filesRecordFor(t, h, c, id)
	for i := 0; i < 2; i++ {
		p := filesListFrame(r, i, i == 1)
		p.Skipped = 2
		if i == 1 {
			p.NextCursor = "YWJj"
		}
		raw, _ := json.Marshal(p)
		h.DeliverWorkspaceFilesFromDaemon(r.target.Connection, protocol.EventWorkspaceFilesListResult, raw)
		msg := filesMessage(t, c)
		var out protocol.WorkspaceFilesClientListResultPayload
		if json.Unmarshal(msg.Payload, &out) != nil || out.ClientReqID != id || out.Seq != i || len(out.Entries) != 1 || out.Final != (i == 1) {
			t.Fatal("list projection")
		}
		for _, secret := range []string{a.snapshot.Root, "root_path", "daemon_req_id", "runtime_id", "connection_epoch", "relay_seq"} {
			if strings.Contains(string(msg.Payload), secret) {
				t.Fatal("list leaked trusted fields")
			}
		}
	}
	if relay.sent(protocol.EventWorkspaceFilesCancel) != 0 || h.files.active != 0 {
		t.Fatal("list lifecycle")
	}
	for _, tc := range []string{"root name", "UNC name", "type", "too many entries", "cursor invalid", "nonfinal cursor", "negative skipped"} {
		t.Run(tc, func(t *testing.T) {
			h, c, a, relay := filesTestHub(t)
			id := uuid.NewString()
			filesListRequest(c, a.snapshot, id)
			r := filesRecordFor(t, h, c, id)
			p := filesListFrame(r, 0, true)
			switch tc {
			case "root name":
				p.Entries[0].Name = "/private/root"
			case "UNC name":
				p.Entries[0].Name = `\\private\share`
			case "type":
				p.Entries[0].Type = "symlink"
			case "too many entries":
				for len(p.Entries) < 201 {
					p.Entries = append(p.Entries, p.Entries[0])
				}
			case "cursor invalid":
				p.NextCursor = "/private/root"
			case "nonfinal cursor":
				p.Final = false
				p.NextCursor = "YWJj"
			case "negative skipped":
				p.Skipped = -1
			}
			raw, _ := json.Marshal(p)
			h.DeliverWorkspaceFilesFromDaemon(r.target.Connection, protocol.EventWorkspaceFilesListResult, raw)
			filesWantCode(t, c, "unavailable")
			if relay.sent(protocol.EventWorkspaceFilesCancel) != 1 {
				t.Fatal("invalid list not canceled")
			}
			filesNoMessage(t, c)
		})
	}
}
func TestWorkspaceFilesReadAndEnvelopeLimits(t *testing.T) {
	for _, tc := range []string{"chunk", "utf8", "total", "chunks", "raw root", "duplicate", "null data", "oversize envelope", "request envelope"} {
		t.Run(tc, func(t *testing.T) {
			h, c, a, relay := filesTestHub(t)
			id := uuid.NewString()
			if tc == "request envelope" {
				a.snapshot.Root = strings.Repeat("\x01", 20000)
			}
			filesReadRequest(c, a.snapshot, id)
			if tc == "request envelope" {
				filesWantCode(t, c, "unavailable")
				if relay.sent(protocol.EventWorkspaceFilesRead) != 0 {
					t.Fatal("oversize request sent")
				}
				return
			}
			r := filesRecordFor(t, h, c, id)
			p := protocol.WorkspaceFilesReadChunkPayload{WorkspaceFilesGeneration: r.target.Generation, DaemonReqID: r.target.RequestID, RuntimeID: r.target.RuntimeID, ResourceID: r.snapshot.ResourceID, Data: []byte("safe"), EOF: true}
			malformed := false
			switch tc {
			case "chunk":
				p.Data = make([]byte, 32769)
			case "utf8":
				p.Data = []byte{255}
			case "total":
				r.readBytes = 1048576
			case "chunks":
				r.nextSeq = 33
				p.Seq = 33
			case "raw root", "duplicate", "null data", "oversize envelope":
				malformed = true
			}
			raw, _ := json.Marshal(p)
			switch tc {
			case "raw root":
				raw = append(raw[:len(raw)-1], []byte(`,"root_path":"/private/root"}`)...)
			case "duplicate":
				raw = append(raw[:len(raw)-1], []byte(`,"seq":0}`)...)
			case "null data":
				raw = []byte(strings.Replace(string(raw), `"data":"c2FmZQ=="`, `"data":null`, 1))
			case "oversize envelope":
				raw = []byte(strings.Repeat(" ", 48*1024) + string(raw))
			}
			h.DeliverWorkspaceFilesFromDaemon(r.target.Connection, protocol.EventWorkspaceFilesReadChunk, raw)
			if malformed {
				filesNoMessage(t, c)
				filesChunk(h, r, 0, true)
				if filesMessage(t, c).Type != protocol.EventWorkspaceFilesReadChunk {
					t.Fatal("malformed frame corrupted record")
				}
			} else {
				filesWantCode(t, c, "unavailable")
				if relay.sent(protocol.EventWorkspaceFilesCancel) != 1 {
					t.Fatal("invalid content not canceled")
				}
			}
		})
	}
}
func TestWorkspaceFilesBusyHasNoDaemonFrame(t *testing.T) {
	h, c, a, relay := filesTestHub(t)
	for i := 0; i < 16; i++ {
		filesReadRequest(c, a.snapshot, uuid.NewString())
	}
	id := uuid.NewString()
	filesReadRequest(c, a.snapshot, id)
	filesWantCode(t, c, "busy")
	if relay.sent(protocol.EventWorkspaceFilesRead) != 16 || h.files.active != 16 || h.files.terminal.Len() != 0 || h.files.sockets[c].active[id] != nil {
		t.Fatal("busy mutated ledger or sent frame")
	}
}
