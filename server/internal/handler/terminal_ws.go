package handler

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	util "github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// AuthorizeTerminalScope implements realtime.TerminalScopeAuthorizer for the
// {scope: "terminal", id: <runtime_id>} subscription (MAX-51 M3). Owner-only
// for MVP: the caller must be the runtime's owner, the runtime must live in
// the caller's workspace, and the runtime must have declared the terminal-v1
// capability at registration (platforms without a pty never declare it, so
// the gate doubles as the platform check).
func (h *Handler) AuthorizeTerminalScope(ctx context.Context, userID, workspaceID, runtimeID string) (bool, string, error) {
	if userID == "" || workspaceID == "" || runtimeID == "" {
		return false, "not_found", nil
	}
	rtUUID, err := util.ParseUUID(runtimeID)
	if err != nil {
		return false, "not_found", nil
	}
	rt, err := h.getAgentRuntime(ctx, obsmetrics.RuntimeLookupSourceRuntimeAPI, rtUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, "not_found", nil
		}
		return false, "lookup_failed", err
	}
	if uuidToString(rt.WorkspaceID) != workspaceID {
		return false, "forbidden", nil
	}
	// Owner-only relay: workspace membership is necessary but not sufficient.
	if !rt.OwnerID.Valid || uuidToString(rt.OwnerID) != userID {
		return false, "forbidden", nil
	}
	if !runtimeHasCapability(rt.Metadata, protocol.DaemonCapabilityTerminalV1) {
		return false, "capability_missing", nil
	}
	return true, "", nil
}
