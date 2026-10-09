package handler

import (
	"context"
	"crypto/subtle"
	"encoding/json"

	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) AuthorizeWorkspaceFilesViewer(ctx context.Context, user, workspace string, request protocol.WorkspaceFilesViewerSelect) (realtime.WorkspaceFilesSnapshot, string) {
	empty := realtime.WorkspaceFilesSnapshot{}
	if !h.pluginsV1Enabled(ctx) || h.PluginService == nil || h.DB == nil {
		return empty, "unavailable"
	}
	snapshot, code := (realtime.WorkspaceFilesStore{DB: h.DB}).AuthorizeWorkspaceFiles(ctx, user, workspace, request.Context, request.ResourceID)
	if code != "" {
		return empty, code
	}
	if request.BindingGeneration != 0 && snapshot.BindingGeneration != request.BindingGeneration {
		return empty, "forbidden"
	}
	wsID, err := util.ParseUUID(workspace)
	if err != nil {
		return empty, "forbidden"
	}
	installations, err := h.Queries.ListWorkspacePluginInstallations(ctx, wsID)
	if err != nil {
		return empty, "unavailable"
	}
	matches := 0
	var selected bool
	for _, installation := range installations {
		if !installation.Enabled {
			continue
		}
		manifest, err := service.ParseInstallationManifest(installation)
		if err != nil {
			return empty, "unavailable"
		}
		for _, surface := range manifest.Contributes.Surfaces {
			if !surface.MatchesFile(request.Path, request.Platform) {
				continue
			}
			matches++
			if uuidToString(installation.ID) != request.InstallationID || uuidToString(installation.PackageVersionID) != request.VersionID || surface.Key != request.SurfaceKey {
				continue
			}
			var scopes []string
			if json.Unmarshal(installation.GrantedScopes, &scopes) != nil {
				continue
			}
			granted := false
			for _, scope := range scopes {
				granted = granted || scope == plugincontract.ScopeFilesRead
			}
			if !granted {
				continue
			}
			script, err := h.PluginService.SurfaceScript(ctx, installation, surface.Key)
			if err != nil || subtle.ConstantTimeCompare([]byte(script.Digest), []byte(request.Digest)) != 1 {
				continue
			}
			selected = true
		}
	}
	if matches != 1 || !selected {
		return empty, "forbidden"
	}
	// Viewer identities require the DB guard; ordinary transcript compatibility
	// does not grant new file identity without migration readiness.
	var ready bool
	err = h.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_trigger t JOIN pg_proc p ON p.oid=t.tgfoid WHERE t.tgrelid='project_resource'::regclass AND t.tgname='project_resource_binding_generation' AND t.tgenabled IN ('O','A') AND p.proname='project_resource_binding_generation_guard')`).Scan(&ready)
	if err != nil || !ready || snapshot.BindingGeneration <= 0 {
		return empty, "unavailable"
	}
	return snapshot, ""
}
