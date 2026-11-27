package daemon

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (c *workspaceFilesChannel) handleWorkspaceFilesEdit(event string, raw json.RawMessage) {
	var req protocol.WorkspaceFilesEditPayload
	if len(raw) > protocol.WorkspaceFilesFrameMaxBytes || protocol.StrictWorkspaceFilesJSON(raw, &req) != nil {
		return
	}
	if !workspaceFilesEditCapabilityEnabled || c.cursors == nil {
		c.sendImmediateError(req.DaemonReqID, req.RuntimeID, req.ResourceID, "unsupported", req.WorkspaceFilesGeneration)
		return
	}
	save := event == protocol.EventWorkspaceFilesSave
	c.start(req.DaemonReqID, req.RuntimeID, req.ResourceID, req.DeadlineMS, func(p *workspaceFilesPending) {
		result, err := performWorkspaceFilesEdit(p.ctx, req, c.cursors, save)
		if err != nil {
			c.finishError(p, workspaceFilesErrorCode(err, "unavailable"))
			return
		}
		frame, err := json.Marshal(protocol.Message{Type: protocol.EventWorkspaceFilesEditResult, Payload: result})
		if err != nil || len(frame) > protocol.WorkspaceFilesFrameMaxBytes {
			c.finishError(p, "unavailable")
			return
		}
		c.finishFrames(p, [][]byte{frame})
	}, req.WorkspaceFilesGeneration)
}

type workspaceFilesEditState struct {
	ParentID string
	FileID   string
	Digest   [sha256.Size]byte
}

func workspaceFilesEditVersion(req protocol.WorkspaceFilesEditPayload, key *workspaceFilesCursorSigner, state workspaceFilesEditState) string {
	// Length-delimited JSON avoids ambiguity between adjacent scope fields.
	scope := struct {
		Workspace, Project, Resource, Root, Path string
		BindingGeneration                        int64
		Policy                                   protocol.WorkspaceFilesEditPolicy
		State                                    workspaceFilesEditState
	}{req.WorkspaceID, req.ProjectID, req.ResourceID, req.RootPath, req.Path, req.BindingGeneration, req.Policy, state}
	raw, _ := json.Marshal(scope)
	mac := hmac.New(sha256.New, key.key[:])
	mac.Write([]byte("workspace-files-edit-v1\x00"))
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func workspaceFilesEditResult(req protocol.WorkspaceFilesEditPayload, version string, data []byte) json.RawMessage {
	if data == nil {
		data = []byte{}
	}
	result, _ := json.Marshal(protocol.WorkspaceFilesEditResultPayload{
		WorkspaceFilesGeneration: req.WorkspaceFilesGeneration,
		DaemonReqID:              req.DaemonReqID, RuntimeID: req.RuntimeID, ResourceID: req.ResourceID,
		BindingGeneration: req.BindingGeneration, Version: version, Data: data,
	})
	return result
}
