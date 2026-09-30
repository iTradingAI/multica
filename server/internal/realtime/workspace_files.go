package realtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

type WorkspaceFilesRelay interface {
	SelectWorkspaceFilesTarget(string, string, string) (*protocol.WorkspaceFilesTarget, string)
	WorkspaceFilesTargetOnline(*protocol.WorkspaceFilesTarget) bool
	SendWorkspaceFilesFrame(*protocol.WorkspaceFilesTarget, []byte) bool
}

// ConfigureWorkspaceFiles requires an explicit single-API deployment assertion.
// Redis broadcast delivery cannot establish point-to-point socket affinity.
func (h *Hub) ConfigureWorkspaceFiles(enabled, singleAPI bool, auth WorkspaceFilesAuthorizer, relay WorkspaceFilesRelay) {
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	h.shutdownWorkspaceFilesLocked()
	h.files.auth, h.files.relay = auth, relay
	h.files.enabled = enabled && singleAPI && auth != nil && relay != nil
}
func (h *Hub) ShutdownWorkspaceFiles() {
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	h.shutdownWorkspaceFilesLocked()
}
func (h *Hub) shutdownWorkspaceFilesLocked() {
	s := h.files
	s.enabled = false
	for _, sock := range s.sockets {
		for _, r := range sock.active {
			h.filesFinishLocked(r, "unavailable", true, false)
		}
	}
	for e := s.terminal.Front(); e != nil; e = s.terminal.Front() {
		s.removeTombstone(e.Value.(*filesTombstone))
	}
}
func (h *Hub) filesAliveLocked(c *Client) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return !c.filesClosed && h.clients[c]
}
func (h *Hub) sendFilesLocked(c *Client, event string, payload any) bool {
	frame := marshalMessage(event, payload)
	if frame == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if c.filesClosed || !h.clients[c] {
		return false
	}
	select {
	case c.send <- frame:
		return true
	default:
		if c.conn != nil {
			_ = c.conn.Close()
		}
		return false
	}
}
func (h *Hub) filesErrorLocked(c *Client, id, resource, code string) {
	if !protocol.WorkspaceFilesUUID(id) {
		id = ""
	}
	h.sendFilesLocked(c, protocol.EventWorkspaceFilesError, protocol.WorkspaceFilesClientErrorPayload{ClientReqID: id, ResourceID: resource, Code: code})
}
func filesCancelFrame(r *filesRecord) []byte {
	if r.target == nil {
		return nil
	}
	return marshalMessage(protocol.EventWorkspaceFilesCancel, protocol.WorkspaceFilesCancelPayload{WorkspaceFilesGeneration: r.target.Generation, DaemonReqID: r.target.RequestID, RuntimeID: r.target.RuntimeID})
}
func (h *Hub) filesFinishLocked(r *filesRecord, code string, cancel, remember bool) {
	if !h.files.retire(r, remember) {
		return
	}
	if cancel && r.target != nil {
		r.relay.SendWorkspaceFilesFrame(r.target, filesCancelFrame(r))
	}
	if code != "" {
		h.filesErrorLocked(r.owner, r.request.ClientReqID, r.snapshot.ResourceID, code)
	}
}
func (h *Hub) filesTimeout(r *filesRecord) {
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	h.filesFinishLocked(r, "timeout", true, true)
}
func (h *Hub) sweepWorkspaceFiles() {
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	h.files.sweep(h.files.now())
}

func (c *Client) handleWorkspaceFilesClientFrame(event string, raw json.RawMessage) {
	h := c.hub
	req, err := protocol.DecodeWorkspaceFilesRequest(event, raw)
	if err != nil {
		h.filesMu.Lock()
		h.filesErrorLocked(c, req.ClientReqID, "", "invalid_request")
		h.filesMu.Unlock()
		return
	}
	h.filesMu.Lock()
	if !h.filesAliveLocked(c) {
		h.filesMu.Unlock()
		return
	}
	if event == protocol.EventWorkspaceFilesCancel {
		if sock := h.files.sockets[c]; sock != nil {
			if r := sock.active[req.ClientReqID]; r != nil {
				h.filesFinishLocked(r, "", true, true)
			}
		}
		h.filesMu.Unlock()
		return
	}
	h.files.sweep(h.files.now())
	if sock := h.files.sockets[c]; sock != nil && (sock.active[req.ClientReqID] != nil || sock.terminal[req.ClientReqID] != nil) {
		h.filesErrorLocked(c, req.ClientReqID, "", "invalid_request")
		h.filesMu.Unlock()
		return
	}
	auth := h.files.auth
	h.filesMu.Unlock()
	if auth == nil {
		h.filesMu.Lock()
		h.filesErrorLocked(c, req.ClientReqID, "", "unavailable")
		h.filesMu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), filesBudget)
	snapshot, code := auth.AuthorizeWorkspaceFiles(ctx, c.userID, c.workspaceID, req.Context, req.ResourceID)
	cancel()
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	if !h.filesAliveLocked(c) {
		return
	}
	if code != "" {
		h.filesErrorLocked(c, req.ClientReqID, "", filesAuthCode(code))
		return
	}
	if !h.files.enabled {
		h.filesErrorLocked(c, req.ClientReqID, "", "unavailable")
		return
	}
	var target *protocol.WorkspaceFilesTarget
	if event != protocol.EventWorkspaceFilesResources {
		target, code = h.files.relay.SelectWorkspaceFilesTarget(snapshot.DaemonID, snapshot.WorkspaceID, h.files.newID())
		if code != "" {
			h.filesErrorLocked(c, req.ClientReqID, snapshot.ResourceID, filesRouteCode(code))
			return
		}
	}
	r, code := h.files.accept(c, event, req, snapshot)
	if code != "" {
		h.filesErrorLocked(c, req.ClientReqID, snapshot.ResourceID, code)
		return
	}
	r.timer = time.AfterFunc(filesBudget, func() { h.filesTimeout(r) })
	if event == protocol.EventWorkspaceFilesResources {
		go h.filesResources(r)
		return
	}
	r.target = target
	if target == nil || target.Generation.ConnectionEpoch == 0 || target.Generation.RelaySeq == 0 || h.files.routes[target.Generation] != nil {
		h.filesFinishLocked(r, "unavailable", false, true)
		return
	}
	h.files.routes[target.Generation] = r
	deadline, _ := r.ctx.Deadline()
	var payload any
	if event == protocol.EventWorkspaceFilesList {
		payload = protocol.WorkspaceFilesListPayload{WorkspaceFilesGeneration: target.Generation, DaemonReqID: target.RequestID, RuntimeID: target.RuntimeID, ResourceID: snapshot.ResourceID, RootPath: snapshot.Root, Path: req.Path, Cursor: req.Cursor, PageSize: req.PageSize, DeadlineMS: deadline.UnixMilli()}
	} else {
		payload = protocol.WorkspaceFilesReadPayload{WorkspaceFilesGeneration: target.Generation, DaemonReqID: target.RequestID, RuntimeID: target.RuntimeID, ResourceID: snapshot.ResourceID, RootPath: snapshot.Root, Path: req.Path, DeadlineMS: deadline.UnixMilli()}
	}
	frame := marshalMessage(event, payload)
	if frame == nil || len(frame) > 48*1024 {
		h.filesFinishLocked(r, "unavailable", false, true)
		return
	}
	if !r.relay.SendWorkspaceFilesFrame(target, frame) {
		h.filesFinishLocked(r, "daemon_offline", true, true)
	}
}
func filesAuthCode(code string) string {
	if code == "forbidden" {
		return code
	}
	return "unavailable"
}
func filesRouteCode(code string) string {
	switch code {
	case "daemon_offline", "daemon_upgrade_required":
		return code
	default:
		return "unavailable"
	}
}
func filesDaemonCode(code string) string {
	switch code {
	case "unsupported":
		return "daemon_upgrade_required"
	case "invalid_path", "symlink_denied", "not_regular", "not_directory", "too_large", "invalid_utf8", "timeout", "busy", "invalid_cursor":
		return code
	default:
		return "unavailable"
	}
}
func (h *Hub) filesResources(r *filesRecord) {
	resources, code := r.auth.ListWorkspaceFilesResources(r.ctx, r.owner.userID, r.owner.workspaceID, r.request.Context)
	snapshot, authorization := r.auth.AuthorizeWorkspaceFiles(r.ctx, r.owner.userID, r.owner.workspaceID, r.request.Context, "")
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	if !h.files.contains(r) {
		return
	}
	if authorization != "" {
		h.filesFinishLocked(r, filesAuthCode(authorization), false, true)
		return
	}
	if snapshot != r.snapshot {
		h.filesFinishLocked(r, "forbidden", false, true)
		return
	}
	if code != "" {
		h.filesFinishLocked(r, filesAuthCode(code), false, true)
		return
	}
	// Rebuild the projection even for alternate authorizers; labels and roots
	// never become output fields, and resource discovery remains bounded.
	if len(resources) > 512 {
		h.filesFinishLocked(r, "unavailable", false, true)
		return
	}
	safe := make([]protocol.WorkspaceFilesResource, 0, len(resources))
	for _, res := range resources {
		if !protocol.WorkspaceFilesUUID(res.ResourceID) {
			h.filesFinishLocked(r, "unavailable", false, true)
			return
		}
		id := strings.ToLower(res.ResourceID)
		safe = append(safe, protocol.WorkspaceFilesResource{ResourceID: id, DisplayName: "local-directory-" + id[:8], Access: "read_only"})
	}
	h.sendFilesLocked(r.owner, protocol.EventWorkspaceFilesResourcesResult, protocol.WorkspaceFilesClientResourcesResultPayload{ClientReqID: r.request.ClientReqID, Context: r.request.Context, Resources: safe})
	h.filesFinishLocked(r, "", false, true)
}

func (h *Hub) workspaceFilesClientGone(c *Client) {
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	c.filesClosed = true
	sock := h.files.sockets[c]
	if sock == nil {
		return
	}
	for _, r := range sock.active {
		h.filesFinishLocked(r, "", true, false)
	}
	for _, t := range sock.terminal {
		h.files.removeTombstone(t)
	}
	h.files.prune(c)
}
func (h *Hub) WorkspaceFilesConnectionOffline(source protocol.WorkspaceFilesConnection, runtime string) {
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	for _, r := range h.files.routes {
		if r.target.Connection.Equal(source) && (runtime == "" || runtime == r.target.RuntimeID) {
			h.filesFinishLocked(r, "daemon_offline", false, true)
		}
	}
}

type filesDaemonFrame struct {
	protocol.WorkspaceFilesGeneration
	DaemonReqID string                         `json:"daemon_req_id"`
	RuntimeID   string                         `json:"runtime_id"`
	ResourceID  string                         `json:"resource_id"`
	Seq         int                            `json:"seq"`
	Entries     []protocol.WorkspaceFilesEntry `json:"entries"`
	NextCursor  string                         `json:"next_cursor,omitempty"`
	Skipped     int                            `json:"skipped,omitempty"`
	Final       bool                           `json:"final"`
	Data        []byte                         `json:"data"`
	EOF         bool                           `json:"eof"`
	Code        string                         `json:"code"`
}

func decodeFilesDaemonFrame(event string, raw json.RawMessage) (filesDaemonFrame, bool) {
	var p filesDaemonFrame
	if len(raw) > 48*1024 || protocol.StrictWorkspaceFilesJSON(raw, &p) != nil {
		return p, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return p, false
	}
	allowed := map[string]bool{"daemon_req_id": true, "runtime_id": true, "resource_id": true, "connection_epoch": true, "relay_seq": true}
	required := []string{"daemon_req_id", "runtime_id", "resource_id", "connection_epoch", "relay_seq"}
	switch event {
	case protocol.EventWorkspaceFilesListResult:
		for _, key := range []string{"seq", "entries", "next_cursor", "skipped", "final"} {
			allowed[key] = true
		}
		required = append(required, "seq", "entries", "final")
	case protocol.EventWorkspaceFilesReadChunk:
		for _, key := range []string{"seq", "data", "eof"} {
			allowed[key] = true
		}
		required = append(required, "seq", "data", "eof")
	case protocol.EventWorkspaceFilesError:
		allowed["code"] = true
		required = append(required, "code")
	default:
		return p, false
	}
	for key := range fields {
		if !allowed[key] {
			return p, false
		}
	}
	for _, key := range required {
		if fields[key] == nil || string(fields[key]) == "null" {
			return p, false
		}
	}
	return p, p.ConnectionEpoch != 0 && p.RelaySeq != 0
}
func filesValidName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 4096 && utf8.ValidString(name) && !strings.ContainsAny(name, "/\\:\x00")
}

// Every content frame rechecks the same authorization join and exact security
// snapshot. The per-record operation mutex serializes DB lookups; filesMu is
// never held while querying, and lifecycle teardown can cancel a stalled query.
func (h *Hub) DeliverWorkspaceFilesFromDaemon(source protocol.WorkspaceFilesConnection, event string, raw json.RawMessage) {
	p, ok := decodeFilesDaemonFrame(event, raw)
	if !ok {
		return
	}
	h.filesMu.Lock()
	r := h.files.routes[p.WorkspaceFilesGeneration]
	h.filesMu.Unlock()
	if r == nil {
		return
	}
	r.opMu.Lock()
	defer r.opMu.Unlock()
	h.filesMu.Lock()
	if !h.files.contains(r) || !r.target.Connection.Equal(source) || r.target.Generation != p.WorkspaceFilesGeneration || r.target.RequestID != p.DaemonReqID || r.target.RuntimeID != p.RuntimeID || r.snapshot.ResourceID != p.ResourceID {
		h.filesMu.Unlock()
		return
	}
	if !r.relay.WorkspaceFilesTargetOnline(r.target) {
		h.filesFinishLocked(r, "daemon_offline", false, true)
		h.filesMu.Unlock()
		return
	}
	if event != protocol.EventWorkspaceFilesError && (p.Seq != r.nextSeq || (event == protocol.EventWorkspaceFilesListResult && r.event != protocol.EventWorkspaceFilesList) || (event == protocol.EventWorkspaceFilesReadChunk && r.event != protocol.EventWorkspaceFilesRead)) {
		h.filesMu.Unlock()
		return
	}
	h.filesMu.Unlock()
	snapshot, code := r.auth.AuthorizeWorkspaceFiles(r.ctx, r.owner.userID, r.owner.workspaceID, r.request.Context, r.snapshot.ResourceID)
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	if !h.files.contains(r) {
		return
	}
	if code != "" {
		h.filesFinishLocked(r, filesAuthCode(code), true, true)
		return
	}
	if snapshot != r.snapshot {
		h.filesFinishLocked(r, "forbidden", true, true)
		return
	}
	if !r.relay.WorkspaceFilesTargetOnline(r.target) {
		h.filesFinishLocked(r, "daemon_offline", false, true)
		return
	}
	final := false
	var payload any
	switch event {
	case protocol.EventWorkspaceFilesError:
		h.filesFinishLocked(r, filesDaemonCode(p.Code), false, true)
		return
	case protocol.EventWorkspaceFilesListResult:
		if p.Skipped < 0 || len(p.NextCursor) > 4096 || r.entries+len(p.Entries) > 200 || (!p.Final && p.NextCursor != "") {
			h.filesFinishLocked(r, "unavailable", true, true)
			return
		}
		if p.NextCursor != "" {
			if _, err := base64.RawURLEncoding.DecodeString(p.NextCursor); err != nil {
				h.filesFinishLocked(r, "unavailable", true, true)
				return
			}
		}
		for _, entry := range p.Entries {
			if !filesValidName(entry.Name) || (entry.Type != "regular" && entry.Type != "directory") {
				h.filesFinishLocked(r, "unavailable", true, true)
				return
			}
		}
		r.entries += len(p.Entries)
		final = p.Final
		payload = protocol.WorkspaceFilesClientListResultPayload{ClientReqID: r.request.ClientReqID, ResourceID: r.snapshot.ResourceID, Seq: p.Seq, Entries: p.Entries, NextCursor: p.NextCursor, Skipped: p.Skipped, Final: p.Final}
	case protocol.EventWorkspaceFilesReadChunk:
		if len(p.Data) > 32*1024 || !utf8.Valid(p.Data) || r.readBytes+len(p.Data) > 1048576 || r.nextSeq >= 33 {
			h.filesFinishLocked(r, "unavailable", true, true)
			return
		}
		r.readBytes += len(p.Data)
		final = p.EOF
		payload = protocol.WorkspaceFilesClientReadChunkPayload{ClientReqID: r.request.ClientReqID, ResourceID: r.snapshot.ResourceID, Seq: p.Seq, Data: p.Data, EOF: p.EOF}
	}
	if !h.sendFilesLocked(r.owner, event, payload) {
		h.filesFinishLocked(r, "", true, true)
		return
	}
	r.nextSeq++
	if final {
		h.filesFinishLocked(r, "", false, true)
	}
}
