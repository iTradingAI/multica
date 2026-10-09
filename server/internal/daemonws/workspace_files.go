package daemonws

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

type WorkspaceFilesBridge interface {
	DeliverWorkspaceFilesFromDaemon(protocol.WorkspaceFilesConnection, string, json.RawMessage)
	WorkspaceFilesConnectionOffline(protocol.WorkspaceFilesConnection, string)
}

func (h *Hub) SetWorkspaceFilesBridge(b WorkspaceFilesBridge) {
	h.mu.Lock()
	h.filesBridge = b
	h.mu.Unlock()
}

func exactFilesCapability(raw string) bool {
	for _, token := range strings.Split(raw, ",") {
		if strings.TrimSpace(token) == protocol.DaemonCapabilityWorkspaceFilesV2 {
			return true
		}
	}
	return false
}

// SelectWorkspaceFilesTarget binds a single active authenticated connection.
// Capability is never inferred from DB metadata or heartbeat timestamps.
func (h *Hub) SelectWorkspaceFilesTarget(daemonID, workspaceID, requestID string) (*protocol.WorkspaceFilesTarget, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var chosen *client
	var runtime string
	scoped := false
	for c := range h.byWorkspace[workspaceID] {
		if !h.clients[c] || c.identity.DaemonID != daemonID || !c.identity.AllowsWorkspace(workspaceID) {
			continue
		}
		c.runtimeMu.RLock()
		for id := range c.runtimes {
			lease := c.identity.RuntimeLeases[id]
			if lease == nil || lease.Snapshot().WorkspaceID != workspaceID {
				continue
			}
			scoped = true
			if !exactFilesCapability(c.identity.Capabilities) {
				continue
			}
			if chosen == nil || id < runtime || (id == runtime && c.filesEpoch < chosen.filesEpoch) {
				chosen, runtime = c, id
			}
		}
		c.runtimeMu.RUnlock()
	}
	if chosen == nil {
		if scoped {
			return nil, "daemon_upgrade_required"
		}
		return nil, "daemon_offline"
	}
	// Zero epochs and the final uint64 value are never issued or reused.
	if chosen.filesEpoch == 0 || chosen.filesSeq >= math.MaxUint64-1 {
		return nil, "unavailable"
	}
	chosen.filesSeq++
	return &protocol.WorkspaceFilesTarget{Connection: protocol.WorkspaceFilesConnection{Identity: chosen.filesIdentity, Epoch: chosen.filesEpoch}, Generation: protocol.WorkspaceFilesGeneration{ConnectionEpoch: chosen.filesEpoch, RelaySeq: chosen.filesSeq}, RuntimeID: runtime, RequestID: requestID, WorkspaceID: workspaceID, DaemonID: daemonID}, ""
}

func (h *Hub) filesTargetOnlineLocked(t *protocol.WorkspaceFilesTarget) bool {
	if t == nil || t.Connection.Identity == nil {
		return false
	}
	c := h.filesConnections[t.Connection.Identity]
	if c == nil {
		return false
	}
	lease := c.identity.RuntimeLeases[t.RuntimeID]
	return h.clients[c] && c.filesEpoch == t.Connection.Epoch && c.identity.DaemonID == t.DaemonID && c.identity.AllowsWorkspace(t.WorkspaceID) && lease != nil && lease.Snapshot().WorkspaceID == t.WorkspaceID && c.allowsRuntime(t.RuntimeID)
}
func (h *Hub) WorkspaceFilesTargetOnline(t *protocol.WorkspaceFilesTarget) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.filesTargetOnlineLocked(t)
}
func (h *Hub) SendWorkspaceFilesFrame(t *protocol.WorkspaceFilesTarget, frame []byte) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.filesTargetOnlineLocked(t) && h.filesConnections[t.Connection.Identity].trySend(frame)
}

func (h *Hub) handleWorkspaceFilesFromDaemon(c *client, event string, payload json.RawMessage) {
	h.mu.RLock()
	b := h.filesBridge
	source := protocol.WorkspaceFilesConnection{Identity: c.filesIdentity, Epoch: c.filesEpoch}
	live := h.clients[c]
	h.mu.RUnlock()
	if live && b != nil {
		b.DeliverWorkspaceFilesFromDaemon(source, event, payload)
	}
}
func (h *Hub) workspaceFilesOffline(c *client, runtimeID string) {
	h.mu.RLock()
	b := h.filesBridge
	source := protocol.WorkspaceFilesConnection{Identity: c.filesIdentity, Epoch: c.filesEpoch}
	h.mu.RUnlock()
	if b != nil {
		b.WorkspaceFilesConnectionOffline(source, runtimeID)
	}
}
