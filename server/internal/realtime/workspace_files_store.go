package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// WorkspaceFilesSnapshot is server-only authorization state. Root is compared
// byte-for-byte on every frame and is never serialized to a browser or log.
type WorkspaceFilesSnapshot struct {
	WorkspaceID, ProjectID, ResourceID, ResourceType, Root, DaemonID string
}
type WorkspaceFilesAuthorizer interface {
	AuthorizeWorkspaceFiles(context.Context, string, string, protocol.WorkspaceFilesContext, string) (WorkspaceFilesSnapshot, string)
	ListWorkspaceFilesResources(context.Context, string, string, protocol.WorkspaceFilesContext) ([]protocol.WorkspaceFilesResource, string)
}
type WorkspaceFilesStore struct{ DB db.DBTX }

// Both entry contexts join to the project in the authenticated workspace and
// require current membership. Issue contexts cannot supply a second project.
const filesAuthorizedProjectSQL = `WITH authorized_project AS (
 SELECT p.id, p.workspace_id FROM project p
 JOIN member m ON m.workspace_id=p.workspace_id AND m.user_id=$1::uuid
 WHERE p.workspace_id=$2::uuid AND (
  ($3::text='project' AND p.id=$4::uuid) OR
  ($3::text='issue' AND EXISTS (
   SELECT 1 FROM issue i WHERE i.id=$4::uuid
   AND i.workspace_id=p.workspace_id AND i.project_id=p.id
  ))
 )
) `

func filesStoreArgs(user, workspace string, context protocol.WorkspaceFilesContext) ([]any, bool) {
	id := context.ProjectID
	if context.Kind == "issue" {
		id = context.IssueID
		if context.ProjectID != "" {
			return nil, false
		}
	} else if context.Kind != "project" || context.IssueID != "" {
		return nil, false
	}
	if !protocol.WorkspaceFilesUUID(user) || !protocol.WorkspaceFilesUUID(workspace) || !protocol.WorkspaceFilesUUID(id) {
		return nil, false
	}
	return []any{user, workspace, context.Kind, id}, true
}
func filesStoreCode(err error) string {
	if errors.Is(err, pgx.ErrNoRows) {
		return "forbidden"
	}
	return "unavailable"
}

func (s WorkspaceFilesStore) AuthorizeWorkspaceFiles(ctx context.Context, user, workspace string, context protocol.WorkspaceFilesContext, resource string) (WorkspaceFilesSnapshot, string) {
	var snap WorkspaceFilesSnapshot
	args, ok := filesStoreArgs(user, workspace, context)
	if !ok {
		return snap, "forbidden"
	}
	if s.DB == nil {
		return snap, "unavailable"
	}
	if resource == "" {
		err := s.DB.QueryRow(ctx, filesAuthorizedProjectSQL+`SELECT id::text,workspace_id::text FROM authorized_project`, args...).Scan(&snap.ProjectID, &snap.WorkspaceID)
		if err != nil {
			return WorkspaceFilesSnapshot{}, filesStoreCode(err)
		}
		return snap, ""
	}
	if !protocol.WorkspaceFilesUUID(resource) {
		return snap, "forbidden"
	}
	args = append(args, resource)
	var ref []byte
	err := s.DB.QueryRow(ctx, filesAuthorizedProjectSQL+`
 SELECT p.id::text,p.workspace_id::text,r.id::text,r.resource_type,r.resource_ref
 FROM authorized_project p JOIN project_resource r
 ON r.project_id=p.id AND r.workspace_id=p.workspace_id
 WHERE r.id=$5::uuid AND r.resource_type='local_directory'`, args...).Scan(&snap.ProjectID, &snap.WorkspaceID, &snap.ResourceID, &snap.ResourceType, &ref)
	if err != nil {
		return WorkspaceFilesSnapshot{}, filesStoreCode(err)
	}
	var local struct {
		LocalPath string `json:"local_path"`
		DaemonID  string `json:"daemon_id"`
	}
	if json.Unmarshal(ref, &local) != nil || !protocol.WorkspaceFilesUUID(local.DaemonID) || local.LocalPath == "" || len(local.LocalPath) > 32768 || !utf8.ValidString(local.LocalPath) || strings.ContainsRune(local.LocalPath, 0) {
		return WorkspaceFilesSnapshot{}, "forbidden"
	}
	snap.Root = local.LocalPath
	snap.DaemonID = strings.ToLower(local.DaemonID)
	return snap, ""
}
func (s WorkspaceFilesStore) ListWorkspaceFilesResources(ctx context.Context, user, workspace string, context protocol.WorkspaceFilesContext) ([]protocol.WorkspaceFilesResource, string) {
	args, ok := filesStoreArgs(user, workspace, context)
	if !ok {
		return nil, "forbidden"
	}
	if s.DB == nil {
		return nil, "unavailable"
	}
	rows, err := s.DB.Query(ctx, filesAuthorizedProjectSQL+`
 SELECT r.id::text FROM authorized_project p JOIN project_resource r
 ON r.project_id=p.id AND r.workspace_id=p.workspace_id
 WHERE r.resource_type='local_directory' ORDER BY r.id LIMIT 513`, args...)
	if err != nil {
		return nil, "unavailable"
	}
	defer rows.Close()
	resources := []protocol.WorkspaceFilesResource{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil || !protocol.WorkspaceFilesUUID(id) {
			return nil, "unavailable"
		}
		resources = append(resources, protocol.WorkspaceFilesResource{ResourceID: id, DisplayName: "local-directory-" + id[:8], Access: "read_only"})
	}
	if rows.Err() != nil || len(resources) > 512 {
		return nil, "unavailable"
	}
	return resources, ""
}
