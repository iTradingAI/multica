package realtime

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestWorkspaceFilesRelayBinaryPreview(t *testing.T) {
	h, c, auth, _ := filesTestHub(t)
	id := uuid.NewString()
	filesReadRequest(c, auth.snapshot, id)
	h.filesMu.Lock()
	record := h.files.sockets[c].active[id]
	h.filesMu.Unlock()
	if record == nil {
		t.Fatal("missing active request")
	}
	raw, _ := json.Marshal(protocol.WorkspaceFilesReadChunkPayload{WorkspaceFilesGeneration: record.target.Generation, DaemonReqID: record.target.RequestID, RuntimeID: record.target.RuntimeID, ResourceID: record.snapshot.ResourceID, Data: []byte{'a', 0, 'b'}, EOF: true})
	h.DeliverWorkspaceFilesFromDaemon(record.target.Connection, protocol.EventWorkspaceFilesReadChunk, raw)
	select {
	case message := <-c.send:
		var frame protocol.Message
		if json.Unmarshal(message, &frame) != nil || frame.Type != protocol.EventWorkspaceFilesError {
			t.Fatal("binary data reached browser")
		}
		var payload protocol.WorkspaceFilesClientErrorPayload
		if json.Unmarshal(frame.Payload, &payload) != nil || payload.Code != "binary_content" {
			t.Fatal("missing binary error")
		}
	default:
		t.Fatal("missing response")
	}
}
