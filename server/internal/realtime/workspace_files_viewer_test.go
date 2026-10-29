package realtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type viewerTestAuth struct{ files *filesTestAuth }

func (a viewerTestAuth) AuthorizeWorkspaceFilesViewer(ctx context.Context, u, w string, r protocol.WorkspaceFilesViewerSelect) (WorkspaceFilesSnapshot, string) {
	return a.files.AuthorizeWorkspaceFiles(ctx, u, w, r.Context, r.ResourceID)
}
func viewerSelection(t *testing.T, h *Hub, c *Client, auth *filesTestAuth, mount string, generation uint64) (protocol.WorkspaceFilesViewerSelect, string) {
	t.Helper()
	h.ConfigureWorkspaceFilesViewer(viewerTestAuth{auth})
	request := protocol.WorkspaceFilesViewerSelect{ClientReqID: uuid.NewString(), Context: protocol.WorkspaceFilesContext{Kind: "project", ProjectID: auth.snapshot.ProjectID}, ResourceID: auth.snapshot.ResourceID, Path: "file.txt", InstallationID: uuid.NewString(), VersionID: uuid.NewString(), SurfaceKey: "viewer", Digest: strings.Repeat("a", 64), Platform: "web", MountID: mount, Generation: generation}
	raw, _ := json.Marshal(request)
	c.handleWorkspaceFilesClientFrame(protocol.EventWorkspaceFilesViewerSelect, raw)
	frame := filesMessage(t, c)
	if frame.Type != protocol.EventWorkspaceFilesViewerSelectResult {
		t.Fatal("selection refused")
	}
	var result protocol.WorkspaceFilesViewerSelectResult
	if json.Unmarshal(frame.Payload, &result) != nil {
		t.Fatal("invalid selection")
	}
	return request, result.SelectionID
}
func viewerRead(c *Client, selection string) string {
	id := uuid.NewString()
	raw, _ := json.Marshal(protocol.WorkspaceFilesViewerRead{ClientReqID: id, SelectionID: selection})
	c.handleWorkspaceFilesClientFrame(protocol.EventWorkspaceFilesViewerRead, raw)
	return id
}

func TestWorkspaceFilesViewerSocketAndMinimalRead(t *testing.T) {
	h, c, auth, _ := filesTestHub(t)
	_, selection := viewerSelection(t, h, c, auth, uuid.NewString(), 1)
	other := filesTestClient(h, c.userID, c.workspaceID)
	t.Cleanup(func() { h.workspaceFilesClientGone(other) })
	viewerRead(other, selection)
	filesWantCode(t, other, "forbidden")
	raw, _ := json.Marshal(map[string]any{"client_req_id": uuid.NewString(), "selection_id": selection, "path": "secret.txt"})
	c.handleWorkspaceFilesClientFrame(protocol.EventWorkspaceFilesViewerRead, raw)
	filesWantCode(t, c, "invalid_request")
	id := viewerRead(c, selection)
	record := filesRecordFor(t, h, c, id)
	viewerRead(c, selection)
	filesWantCode(t, c, "forbidden")
	filesChunk(h, record, 0, true)
	if frame := filesMessage(t, c); frame.Type != protocol.EventWorkspaceFilesReadChunk {
		t.Fatal("missing fixed selection read")
	}
}
func TestWorkspaceFilesViewerRevocationAndGeneration(t *testing.T) {
	h, c, auth, relay := filesTestHub(t)
	mount := uuid.NewString()
	request, selection := viewerSelection(t, h, c, auth, mount, 1)
	id := viewerRead(c, selection)
	record := filesRecordFor(t, h, c, id)
	_, next := viewerSelection(t, h, c, auth, mount, 2)
	if relay.sent(protocol.EventWorkspaceFilesCancel) != 1 {
		t.Fatal("switch did not cancel read")
	}
	filesChunk(h, record, 0, true)
	select {
	case <-c.send:
		t.Fatal("late retired data leaked")
	default:
	}
	raw, _ := json.Marshal(request)
	c.handleWorkspaceFilesClientFrame(protocol.EventWorkspaceFilesViewerSelect, raw)
	filesWantCode(t, c, "forbidden")
	id = viewerRead(c, next)
	record = filesRecordFor(t, h, c, id)
	auth.mu.Lock()
	auth.snapshot.BindingGeneration++
	auth.mu.Unlock()
	filesChunk(h, record, 0, true)
	filesWantCode(t, c, "forbidden")
}
func TestWorkspaceFilesViewerIdleQuotaAndCancel(t *testing.T) {
	h, c, auth, _ := filesTestHub(t)
	requests := []protocol.WorkspaceFilesViewerSelect{}
	for i := 0; i < 16; i++ {
		request, _ := viewerSelection(t, h, c, auth, uuid.NewString(), 1)
		requests = append(requests, request)
	}
	raw, _ := json.Marshal(protocol.WorkspaceFilesViewerSelect{ClientReqID: uuid.NewString(), Context: requests[0].Context, ResourceID: requests[0].ResourceID, Path: "file.txt", InstallationID: requests[0].InstallationID, VersionID: requests[0].VersionID, SurfaceKey: "viewer", Digest: requests[0].Digest, Platform: "web", MountID: uuid.NewString(), Generation: 1})
	c.handleWorkspaceFilesClientFrame(protocol.EventWorkspaceFilesViewerSelect, raw)
	filesWantCode(t, c, "busy")
	cancel, _ := json.Marshal(protocol.WorkspaceFilesClientCancelPayload{ClientReqID: requests[0].ClientReqID})
	c.handleWorkspaceFilesClientFrame(protocol.EventWorkspaceFilesCancel, cancel)
	viewerSelection(t, h, c, auth, uuid.NewString(), 1)
}
