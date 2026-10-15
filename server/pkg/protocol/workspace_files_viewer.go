package protocol

const (
	EventWorkspaceFilesViewerSelect       = "workspace_files.viewer_select"
	EventWorkspaceFilesViewerSelectResult = "workspace_files.viewer_select_result"
	EventWorkspaceFilesViewerRead         = "workspace_files.viewer_read"
)

type WorkspaceFilesViewerSelect struct {
	BindingGeneration int64                 `json:"binding_generation,omitempty"`
	ClientReqID       string                `json:"client_req_id"`
	Context           WorkspaceFilesContext `json:"context"`
	ResourceID        string                `json:"resource_id"`
	Path              string                `json:"path"`
	InstallationID    string                `json:"installation_id"`
	VersionID         string                `json:"version_id"`
	SurfaceKey        string                `json:"surface_key"`
	Digest            string                `json:"digest"`
	Platform          string                `json:"platform"`
	MountID           string                `json:"mount_id"`
	Generation        uint64                `json:"generation"`
}

type WorkspaceFilesViewerRead struct {
	ClientReqID string `json:"client_req_id"`
	SelectionID string `json:"selection_id"`
}

type WorkspaceFilesViewerSelectResult struct {
	ClientReqID string `json:"client_req_id"`
	SelectionID string `json:"selection_id"`
}
