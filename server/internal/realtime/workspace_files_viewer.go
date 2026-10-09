package realtime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

type WorkspaceFilesViewerAuthorizer interface {
	AuthorizeWorkspaceFilesViewer(context.Context, string, string, protocol.WorkspaceFilesViewerSelect) (WorkspaceFilesSnapshot, string)
}

type filesViewerSelection struct {
	id        string
	request   protocol.WorkspaceFilesViewerSelect
	read      *filesRecord
	checking  bool
	nextCheck time.Time
}

func (h *Hub) filesCheckViewer(record *filesRecord) {
	ctx, cancel := context.WithTimeout(record.ctx, filesBudget)
	defer cancel()
	h.filesMu.Lock()
	authorizer := h.files.viewerAuth
	request := record.viewer.request
	h.filesMu.Unlock()
	var snapshot WorkspaceFilesSnapshot
	code := "unavailable"
	if authorizer != nil {
		snapshot, code = authorizer.AuthorizeWorkspaceFilesViewer(ctx, record.owner.userID, record.owner.workspaceID, request)
	}
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	if !h.files.contains(record) {
		return
	}
	record.viewer.checking = false
	record.viewer.nextCheck = h.files.now().Add(5 * time.Second)
	if code != "" || snapshot != record.snapshot {
		h.filesFinishLocked(record, "forbidden", true, true)
	}
}

func (h *Hub) ConfigureWorkspaceFilesViewer(auth WorkspaceFilesViewerAuthorizer) {
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	h.files.viewerAuth = auth
}

func (c *Client) handleWorkspaceFilesViewerFrame(event string, raw json.RawMessage) {
	h := c.hub
	if event == protocol.EventWorkspaceFilesViewerRead {
		var request protocol.WorkspaceFilesViewerRead
		if protocol.StrictWorkspaceFilesJSON(raw, &request) != nil || !protocol.WorkspaceFilesUUID(request.ClientReqID) || !protocol.WorkspaceFilesUUID(request.SelectionID) {
			h.filesMu.Lock()
			h.filesErrorLocked(c, request.ClientReqID, "", "invalid_request")
			h.filesMu.Unlock()
			return
		}
		h.filesMu.Lock()
		var selection *filesRecord
		if socket := h.files.sockets[c]; socket != nil {
			for _, candidate := range socket.active {
				if candidate.viewer != nil && candidate.viewer.id == request.SelectionID && candidate.ctx.Err() == nil {
					selection = candidate
					break
				}
			}
		}
		if selection == nil || selection.viewer.read != nil {
			h.filesErrorLocked(c, request.ClientReqID, "", "forbidden")
			h.filesMu.Unlock()
			return
		}
		selected := selection.request
		h.filesMu.Unlock()
		body, _ := json.Marshal(protocol.WorkspaceFilesClientReadPayload{ClientReqID: request.ClientReqID, Context: selected.Context, ResourceID: selected.ResourceID, Path: selected.Path})
		c.handleWorkspaceFilesRequest(protocol.EventWorkspaceFilesRead, body, selection)
		return
	}
	var request protocol.WorkspaceFilesViewerSelect
	if protocol.StrictWorkspaceFilesJSON(raw, &request) != nil || !protocol.WorkspaceFilesUUID(request.ClientReqID) || !protocol.WorkspaceFilesUUID(request.InstallationID) || !protocol.WorkspaceFilesUUID(request.VersionID) || !protocol.WorkspaceFilesUUID(request.MountID) || request.Generation == 0 || request.Generation > 9007199254740991 || request.SurfaceKey == "" || len(request.SurfaceKey) > 160 || len(request.Digest) != 64 || request.Platform != "web" && request.Platform != "desktop" {
		h.filesMu.Lock()
		h.filesErrorLocked(c, request.ClientReqID, "", "invalid_request")
		h.filesMu.Unlock()
		return
	}
	body, _ := json.Marshal(protocol.WorkspaceFilesClientReadPayload{ClientReqID: request.ClientReqID, Context: request.Context, ResourceID: request.ResourceID, Path: request.Path, BindingGeneration: request.BindingGeneration})
	fileRequest, err := protocol.DecodeWorkspaceFilesRequest(protocol.EventWorkspaceFilesRead, body)
	if err != nil {
		h.filesMu.Lock()
		h.filesErrorLocked(c, request.ClientReqID, "", "invalid_request")
		h.filesMu.Unlock()
		return
	}
	h.filesMu.Lock()
	if !h.filesAliveLocked(c) || !h.files.enabled || h.files.viewerAuth == nil {
		h.filesErrorLocked(c, request.ClientReqID, "", "unavailable")
		h.filesMu.Unlock()
		return
	}
	h.files.sweep(h.files.now())
	var old []*filesRecord
	if socket := h.files.sockets[c]; socket != nil {
		for _, terminal := range socket.terminal {
			if terminal.viewerMount == request.MountID && terminal.viewerGeneration >= request.Generation {
				h.filesErrorLocked(c, request.ClientReqID, "", "forbidden")
				h.filesMu.Unlock()
				return
			}
		}
		for _, candidate := range socket.active {
			if candidate.viewer != nil && candidate.viewer.request.MountID == request.MountID {
				if candidate.viewer.request.Generation >= request.Generation {
					h.filesErrorLocked(c, request.ClientReqID, "", "forbidden")
					h.filesMu.Unlock()
					return
				}
				old = append(old, candidate)
			}
		}
	}
	for _, candidate := range old {
		h.filesFinishLocked(candidate, "", true, true)
	}
	// The reservation is admitted BEFORE querying authorization, in the same
	// bounded ledger as idle selections and active reads.
	record, code := h.files.accept(c, event, fileRequest, WorkspaceFilesSnapshot{})
	if code != "" {
		h.filesErrorLocked(c, request.ClientReqID, "", code)
		h.filesMu.Unlock()
		return
	}
	record.viewer = &filesViewerSelection{id: h.files.newID(), request: request}
	record.timer = time.AfterFunc(filesBudget, func() { h.filesTimeout(record) })
	authorizer := h.files.viewerAuth
	h.filesMu.Unlock()
	snapshot, code := authorizer.AuthorizeWorkspaceFilesViewer(record.ctx, c.userID, c.workspaceID, request)
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	if !h.files.contains(record) {
		return
	}
	if code != "" || !h.files.enabled || record.ctx.Err() != nil {
		if code == "" {
			code = "unavailable"
		}
		h.filesFinishLocked(record, filesAuthCode(code), true, true)
		return
	}
	record.snapshot = snapshot
	record.timer.Stop()
	record.cancel()
	record.ctx, record.cancel = context.WithTimeout(context.Background(), 2*time.Minute)
	record.timer = time.AfterFunc(2*time.Minute, func() { h.filesTimeout(record) })
	if !h.sendFilesLocked(c, protocol.EventWorkspaceFilesViewerSelectResult, protocol.WorkspaceFilesViewerSelectResult{ClientReqID: request.ClientReqID, SelectionID: record.viewer.id}) {
		h.filesFinishLocked(record, "", true, true)
	}
}

func (h *Hub) filesViewerAuthorization(record *filesRecord) (WorkspaceFilesSnapshot, string) {
	h.filesMu.Lock()
	selection := record.selection
	authorizer := h.files.viewerAuth
	if selection == nil || selection.viewer == nil || authorizer == nil || !h.files.contains(selection) || selection.ctx.Err() != nil {
		h.filesMu.Unlock()
		return WorkspaceFilesSnapshot{}, "forbidden"
	}
	request := selection.viewer.request
	h.filesMu.Unlock()
	return authorizer.AuthorizeWorkspaceFilesViewer(record.ctx, record.owner.userID, record.owner.workspaceID, request)
}
