package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/filetouch"
)

func (h *Handler) fileTouchCollectionEnabled() bool {
	return os.Getenv("MULTICA_FILE_TOUCHES_ENABLED") == "true"
}

func (h *Handler) fileTouchClaim(task db.AgentTaskQueue, runtime db.AgentRuntime, response AgentTaskResponse) filetouch.Claim {
	claim := filetouch.Claim{ProofVersion: filetouch.ProofVersion, WorkspaceID: response.WorkspaceID, ProjectID: response.ProjectID, RuntimeID: uuidToString(runtime.ID), DaemonID: runtime.DaemonID.String, Provider: runtime.Provider, DispatchedAt: task.DispatchedAt.Time.UTC().Format(time.RFC3339Nano)}
	for _, resource := range response.ProjectResources {
		if resource.ResourceType != "local_directory" || resource.BindingGeneration <= 0 {
			continue
		}
		var ref struct {
			Root   string `json:"local_path"`
			Daemon string `json:"daemon_id"`
			Mode   string `json:"execution_mode"`
		}
		if json.Unmarshal(resource.ResourceRef, &ref) != nil || ref.Daemon != claim.DaemonID || ref.Root == "" {
			continue
		}
		if ref.Mode == "" {
			ref.Mode = "in_place"
		}
		claim.Bindings = append(claim.Bindings, filetouch.Binding{ResourceID: resource.ID, Generation: resource.BindingGeneration, Root: ref.Root, DaemonID: ref.Daemon, Mode: ref.Mode})
	}
	return claim
}

// Only the authenticated machine identity can report execution evidence. PAT,
// cloud PAT and JWT transcript compatibility does not grant this capability.
func (h *Handler) RegisterTaskFileExecution(w http.ResponseWriter, r *http.Request) {
	task, ws, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	daemon := middleware.DaemonIDFromContext(r.Context())
	if daemon == "" || !filetouch.Ready(r.Context(), h.DB) {
		writeError(w, http.StatusServiceUnavailable, "file evidence unavailable")
		return
	}
	var execution filetouch.Execution
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&execution) != nil {
		writeError(w, 400, "invalid execution evidence")
		return
	}
	id, err := uuid.Parse(execution.ID)
	var claim filetouch.Claim
	if err != nil || id == uuid.Nil || json.Unmarshal(task.FileClaimSnapshot, &claim) != nil || claim.ProofVersion != filetouch.ProofVersion || claim.WorkspaceID != ws || claim.RuntimeID != uuidToString(task.RuntimeID) || claim.DaemonID != daemon || claim.DispatchedAt != execution.DispatchedAt || !filetouch.ValidExecution(execution) || task.Status != "running" {
		writeError(w, 409, "execution claim changed")
		return
	}
	selected := execution.ResourceID == ""
	for _, binding := range claim.Bindings {
		selected = selected || binding.ResourceID == execution.ResourceID
	}
	if !selected {
		writeError(w, 400, "invalid execution resource")
		return
	}
	// INSERT and lease/runtime verification share the statement snapshot. A
	// retry of the exact evidence is idempotent; an ID collision cannot replace it.
	tag, err := h.DB.Exec(r.Context(), `INSERT INTO file_touch_execution
 (id,workspace_id,task_id,issue_id,runtime_id,daemon_id,dispatched_at,project_id,cwd,path_platform,selected_resource_id,claim_snapshot)
 SELECT $1,$2,t.id,t.issue_id,t.runtime_id,$3,t.dispatched_at,NULLIF($4,'')::uuid,$5,$6,NULLIF($7,'')::uuid,t.file_claim_snapshot
 FROM agent_task_queue t JOIN agent_runtime rt ON rt.id=t.runtime_id
 WHERE t.id=$8 AND t.status='running' AND rt.daemon_id=$3 AND t.dispatched_at=$9 AND t.file_claim_snapshot=$10::jsonb AND t.issue_id IS NOT NULL
 ON CONFLICT (id) DO NOTHING`, id.String(), ws, daemon, claim.ProjectID, execution.Cwd, execution.Platform, execution.ResourceID, uuidToString(task.ID), task.DispatchedAt, task.FileClaimSnapshot)
	if err != nil {
		writeError(w, 500, "failed to persist execution evidence")
		return
	}
	if tag.RowsAffected() == 0 {
		var same bool
		err = h.DB.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM file_touch_execution WHERE id=$1 AND workspace_id=$2 AND task_id=$3 AND daemon_id=$4 AND dispatched_at=$5 AND cwd=$6 AND path_platform=$7 AND COALESCE(selected_resource_id::text,'')=$8 AND claim_snapshot=$9::jsonb)`, id.String(), ws, uuidToString(task.ID), daemon, task.DispatchedAt, execution.Cwd, execution.Platform, execution.ResourceID, task.FileClaimSnapshot).Scan(&same)
		if err != nil || !same {
			writeError(w, 409, "execution claim changed")
			return
		}
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (h *Handler) terminalFileTouchProof(ctx context.Context, task db.AgentTaskQueue, ws string, msg TaskMessageRequest, beforeTool string, beforeInput map[string]any, lossless bool) (filetouch.Integrity, string, string) {
	proof := filetouch.Integrity{}
	if msg.PathIntegrity != nil {
		proof = *msg.PathIntegrity
	}
	trusted := false
	executionID, sourceID := "", ""
	daemon := middleware.DaemonIDFromContext(ctx)
	if msg.Type == "tool_use" && daemon != "" && lossless && h.DB != nil && msg.PathIntegrity != nil {
		id, err := uuid.Parse(msg.FileExecutionID)
		if err == nil && id != uuid.Nil {
			var provider string
			err = h.DB.QueryRow(ctx, `SELECT e.claim_snapshot->>'provider' FROM file_touch_execution e
   JOIN agent_runtime rt ON rt.id=e.runtime_id
   WHERE e.id=$1 AND e.workspace_id=$2 AND e.task_id=$3 AND e.runtime_id=$4 AND e.daemon_id=$5
   AND e.dispatched_at=$6 AND e.claim_snapshot=$7::jsonb AND rt.daemon_id=e.daemon_id`, id.String(), ws, uuidToString(task.ID), uuidToString(task.RuntimeID), daemon, task.DispatchedAt, task.FileClaimSnapshot).Scan(&provider)
			trusted = err == nil && provider == proof.Provider
			if trusted {
				executionID = id.String()
				if msg.CallID == "" {
					sid, parseErr := uuid.Parse(msg.SourceEventID)
					if parseErr == nil && sid != uuid.Nil {
						sourceID = sid.String()
					}
				}
			}
		}
	}
	return filetouch.Transform(proof, beforeTool, beforeInput, msg.Tool, msg.Input, trusted), executionID, sourceID
}

// Avoid an untrusted tool name in coverage outputs.
func fileTouchTool(tool string) string {
	switch strings.TrimSpace(tool) {
	case "Write", "Edit", "MultiEdit", "patch_apply":
		return tool
	}
	return "unsupported"
}
