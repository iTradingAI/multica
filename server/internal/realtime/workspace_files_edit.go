package realtime

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type filesEditKey struct {
	WorkspaceID       string `json:"workspace_id"`
	ProjectID         string `json:"project_id"`
	ResourceID        string `json:"resource_id"`
	BindingGeneration int64  `json:"binding_generation"`
}

type WorkspaceFilesEditBindings map[filesEditKey]protocol.WorkspaceFilesEditPolicy

var workspaceFilesDirectoryID = regexp.MustCompile("^[0-9a-fA-F]{16}$")

func ParseWorkspaceFilesEditBindings(raw string) (WorkspaceFilesEditBindings, error) {
	if raw == "" {
		return nil, nil
	}
	invalid := errors.New("invalid workspace files edit registration")
	var entries []struct {
		filesEditKey
		protocol.WorkspaceFilesEditPolicy
	}
	if len(raw) > 64*1024 || protocol.StrictWorkspaceFilesJSON([]byte(raw), &entries) != nil || entries == nil {
		return nil, invalid
	}
	result := make(WorkspaceFilesEditBindings, len(entries))
	for _, entry := range entries {
		key, policy := entry.filesEditKey, entry.WorkspaceFilesEditPolicy
		if !protocol.WorkspaceFilesUUID(key.WorkspaceID) || !protocol.WorkspaceFilesUUID(key.ProjectID) ||
			!protocol.WorkspaceFilesUUID(key.ResourceID) || key.BindingGeneration <= 0 || key.BindingGeneration > 9007199254740991 ||
			!protocol.WorkspaceFilesUUID(policy.VolumeGUID) || !workspaceFilesDirectoryID.MatchString(policy.DirectoryID) ||
			policy.DirectoryID == "0000000000000000" || len(policy.Directory) > 4096 {
			return nil, invalid
		}
		if policy.Directory != "." {
			for _, part := range strings.Split(policy.Directory, "/") {
				if !protocol.WorkspaceFilesEditName(part) {
					return nil, invalid
				}
			}
		}
		key.WorkspaceID = strings.ToLower(key.WorkspaceID)
		key.ProjectID = strings.ToLower(key.ProjectID)
		key.ResourceID = strings.ToLower(key.ResourceID)
		if _, exists := result[key]; exists {
			return nil, invalid
		}
		result[key] = policy
	}
	return result, nil
}

func (h *Hub) ConfigureWorkspaceFilesEdit(bindings WorkspaceFilesEditBindings) {
	h.filesMu.Lock()
	defer h.filesMu.Unlock()
	h.files.editBindings = bindings
}

func (s *workspaceFilesState) editPolicy(snapshot WorkspaceFilesSnapshot, path string) (protocol.WorkspaceFilesEditPolicy, bool) {
	key := filesEditKey{snapshot.WorkspaceID, snapshot.ProjectID, snapshot.ResourceID, snapshot.BindingGeneration}
	policy, ok := s.editBindings[key]
	return policy, ok && (policy.Directory == "." || strings.HasPrefix(path, policy.Directory+"/"))
}

func filesSessionExpiry(raw string) time.Time {
	claims, err := auth.ParseSessionToken(raw)
	if err != nil {
		return time.Time{}
	}
	return auth.SessionExpiry(claims)
}

type workspaceFilesEditRelay interface {
	SelectWorkspaceFilesEditTarget(string, string, string) (*protocol.WorkspaceFilesTarget, string)
}
