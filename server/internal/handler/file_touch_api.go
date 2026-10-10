package handler

import (
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/pkg/filetouch"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type issueFileTouchResponse struct {
	TouchID      string    `json:"touch_id"`
	ResourceID   string    `json:"resource_id"`
	Path         string    `json:"path"`
	CallCount    int64     `json:"call_count"`
	LastObserved time.Time `json:"last_observed"`
	Operations   []string  `json:"operations"`
}
type fileTouchCoverage struct {
	Unknown     int64 `json:"unknown"`
	Unavailable int64 `json:"unavailable"`
	Uncertain   int64 `json:"uncertain"`
	Conflicting int64 `json:"conflicting"`
	Pending     int64 `json:"pending"`
}

func (h *Handler) fileTouchReadReady(w http.ResponseWriter, r *http.Request) bool {
	if os.Getenv("MULTICA_WORKSPACE_FILES_ENABLED") != "true" || os.Getenv("MULTICA_WORKSPACE_FILES_SINGLE_API") != "true" || !filetouch.Ready(r.Context(), h.DB) {
		writeError(w, 503, "file touches unavailable")
		return false
	}
	return true
}

func (h *Handler) ListIssueFileTouches(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok || !h.fileTouchReadReady(w, r) {
		return
	}
	workspace, issueID, project := uuidToString(issue.WorkspaceID), uuidToString(issue.ID), uuidToString(issue.ProjectID)
	cursor := r.URL.Query().Get("cursor")
	if cursor != "" {
		if _, err := uuid.Parse(cursor); err != nil {
			writeError(w, 400, "invalid cursor")
			return
		}
	}
	coverage := fileTouchCoverage{}
	err := h.DB.QueryRow(r.Context(), `SELECT count(DISTINCT t.event_key) FILTER (WHERE t.mapping_status<>'mapped'),
 count(DISTINCT t.event_key) FILTER (WHERE t.mapping_status='mapped' AND (t.project_id IS DISTINCT FROM i.project_id OR pr.id IS NULL OR pr.binding_generation IS DISTINCT FROM t.binding_generation OR pr.resource_type<>'local_directory')),
 count(DISTINCT e.event_key) FILTER (WHERE e.uncertain),count(DISTINCT e.event_key) FILTER (WHERE e.conflict),
 (SELECT count(*) FROM issue_file_touch_pending p WHERE p.workspace_id=$1 AND p.issue_id=$2 AND NOT p.completed)
 FROM issue i LEFT JOIN issue_file_touches t ON t.issue_id=i.id AND t.workspace_id=i.workspace_id
 LEFT JOIN issue_file_touch_event e ON e.event_key=t.event_key
 LEFT JOIN project_resource pr ON pr.id=t.resource_id AND pr.workspace_id=i.workspace_id AND pr.project_id=i.project_id
 WHERE i.workspace_id=$1 AND i.id=$2`, workspace, issueID).Scan(&coverage.Unknown, &coverage.Unavailable, &coverage.Uncertain, &coverage.Conflicting, &coverage.Pending)
	if err != nil {
		writeError(w, 503, "file touches unavailable")
		return
	}
	rows, err := h.DB.Query(r.Context(), `SELECT min(t.id::text) AS identity,t.resource_id::text,t.binding_generation,t.relative_path,
 count(DISTINCT t.event_key),max(t.observed_at),array_agg(DISTINCT t.op ORDER BY t.op)
 FROM issue_file_touches t JOIN project_resource pr ON pr.id=t.resource_id AND pr.workspace_id=t.workspace_id AND pr.project_id=t.project_id AND pr.binding_generation=t.binding_generation AND pr.resource_type='local_directory'
 WHERE t.workspace_id=$1 AND t.issue_id=$2 AND t.mapping_status='mapped' AND t.project_id=NULLIF($3,'')::uuid
 GROUP BY t.resource_id,t.binding_generation,t.relative_path
 HAVING min(t.id::text)>$4 ORDER BY identity LIMIT 201`, workspace, issueID, project, cursor)
	if err != nil {
		writeError(w, 503, "file touches unavailable")
		return
	}
	type candidate struct {
		response   issueFileTouchResponse
		generation int64
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.response.TouchID, &c.response.ResourceID, &c.generation, &c.response.Path, &c.response.CallCount, &c.response.LastObserved, &c.response.Operations); err != nil {
			break
		}
		candidates = append(candidates, c)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		writeError(w, 503, "file touches unavailable")
		return
	}
	next := ""
	if len(candidates) > 200 {
		candidates = candidates[:200]
		next = candidates[len(candidates)-1].response.TouchID
	}
	files := []issueFileTouchResponse{}
	snapshots := map[string]realtime.WorkspaceFilesSnapshot{}
	for _, c := range candidates {
		snapshot, present := snapshots[c.response.ResourceID]
		if !present {
			snapshot, _ = (realtime.WorkspaceFilesStore{DB: h.DB}).AuthorizeWorkspaceFiles(r.Context(), requestUserID(r), workspace, protocol.WorkspaceFilesContext{Kind: "issue", IssueID: issueID}, c.response.ResourceID)
			snapshots[c.response.ResourceID] = snapshot
		}
		if snapshot.BindingGeneration != c.generation {
			continue
		}
		files = append(files, c.response)
	}
	writeJSON(w, 200, map[string]any{"files": files, "coverage": coverage, "next_cursor": next})
}

// Resolves a fixed projection identity. The caller cannot substitute its path,
// resource or generation. The actual read still uses the existing user channel.
func (h *Handler) ResolveIssueFileTouch(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok || !h.fileTouchReadReady(w, r) {
		return
	}
	touch, err := uuid.Parse(chi.URLParam(r, "touchId"))
	if err != nil {
		writeError(w, 404, "file touch unavailable")
		return
	}
	var resource, path string
	var generation int64
	err = h.DB.QueryRow(r.Context(), `SELECT t.resource_id::text,t.binding_generation,t.relative_path
 FROM issue_file_touches t JOIN project_resource pr ON pr.id=t.resource_id AND pr.workspace_id=t.workspace_id AND pr.project_id=t.project_id AND pr.binding_generation=t.binding_generation
 WHERE t.id=$1 AND t.workspace_id=$2 AND t.issue_id=$3 AND t.project_id=$4 AND t.mapping_status='mapped' AND pr.resource_type='local_directory'`, touch.String(), issue.WorkspaceID, issue.ID, issue.ProjectID).Scan(&resource, &generation, &path)
	if err != nil {
		writeError(w, 404, "file touch unavailable")
		return
	}
	snapshot, code := (realtime.WorkspaceFilesStore{DB: h.DB}).AuthorizeWorkspaceFiles(r.Context(), requestUserID(r), uuidToString(issue.WorkspaceID), protocol.WorkspaceFilesContext{Kind: "issue", IssueID: uuidToString(issue.ID)}, resource)
	if code != "" || snapshot.BindingGeneration != generation {
		writeError(w, 404, "file touch unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"resource_id": resource, "path": path, "binding_generation": generation})
}
