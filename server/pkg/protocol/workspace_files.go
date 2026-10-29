package protocol

// Workspace-files frames travel over the daemon control connection. The
// browser-facing request identifier is translated by the server; the daemon
// only sees daemon_req_id so it cannot correlate work across client sockets.
const (
	DaemonCapabilityWorkspaceFilesV1   = "workspace-files-v1"
	DaemonCapabilityWorkspaceFilesV2   = "workspace-files-v2"
	EventWorkspaceFilesResources       = "workspace_files.resources"
	EventWorkspaceFilesResourcesResult = "workspace_files.resources_result"

	EventWorkspaceFilesList       = "workspace_files.list"
	EventWorkspaceFilesRead       = "workspace_files.read"
	EventWorkspaceFilesCancel     = "workspace_files.cancel"
	EventWorkspaceFilesListResult = "workspace_files.list_result"
	EventWorkspaceFilesReadChunk  = "workspace_files.read_chunk"
	EventWorkspaceFilesError      = "workspace_files.error"

	WorkspaceFilesErrorInvalidPath   = "invalid_path"
	WorkspaceFilesErrorSymlinkDenied = "symlink_denied"
	WorkspaceFilesErrorNotRegular    = "not_regular"
	WorkspaceFilesErrorNotDirectory  = "not_directory"
	WorkspaceFilesErrorTooLarge      = "too_large"
	WorkspaceFilesErrorInvalidUTF8   = "invalid_utf8"
	WorkspaceFilesErrorBinaryContent = "binary_content"
	WorkspaceFilesErrorTimeout       = "timeout"
	WorkspaceFilesErrorBusy          = "busy"
	WorkspaceFilesErrorUnsupported   = "unsupported"
	WorkspaceFilesErrorInvalidCursor = "invalid_cursor"
	WorkspaceFilesErrorUnavailable   = "unavailable"
)

// WorkspaceFilesClientListPayload is the browser-to-server contract. It never
// accepts a filesystem root; the server resolves the resource and creates a
// separate daemon request with a fresh daemon_req_id.
type WorkspaceFilesClientListPayload struct {
	ClientReqID string                `json:"client_req_id"`
	Context     WorkspaceFilesContext `json:"context"`
	ResourceID  string                `json:"resource_id"`
	Path        string                `json:"path"`
	Cursor      string                `json:"cursor,omitempty"`
	PageSize    int                   `json:"page_size,omitempty"`
}

type WorkspaceFilesClientReadPayload struct {
	BindingGeneration int64                 `json:"binding_generation,omitempty"`
	ClientReqID       string                `json:"client_req_id"`
	Context           WorkspaceFilesContext `json:"context"`
	ResourceID        string                `json:"resource_id"`
	Path              string                `json:"path"`
}

type WorkspaceFilesClientCancelPayload struct {
	ClientReqID string `json:"client_req_id"`
}

// Client result payloads replace daemon_req_id with the request ID scoped to
// the originating client connection. MAX-174 implements that mapping.
type WorkspaceFilesClientListResultPayload struct {
	ClientReqID string                `json:"client_req_id"`
	ResourceID  string                `json:"resource_id"`
	Seq         int                   `json:"seq"`
	Entries     []WorkspaceFilesEntry `json:"entries"`
	NextCursor  string                `json:"next_cursor,omitempty"`
	Skipped     int                   `json:"skipped,omitempty"`
	Final       bool                  `json:"final"`
}

type WorkspaceFilesClientReadChunkPayload struct {
	ClientReqID string `json:"client_req_id"`
	ResourceID  string `json:"resource_id"`
	Seq         int    `json:"seq"`
	Data        []byte `json:"data"`
	EOF         bool   `json:"eof"`
}

type WorkspaceFilesClientErrorPayload struct {
	ClientReqID string `json:"client_req_id"`
	ResourceID  string `json:"resource_id,omitempty"`
	Code        string `json:"code"`
}

// WorkspaceFilesListPayload is sent server-to-daemon after the server has
// authorized the resource and filled RootPath. RootPath is never returned.
type WorkspaceFilesListPayload struct {
	WorkspaceFilesGeneration
	DaemonReqID string `json:"daemon_req_id"`
	RuntimeID   string `json:"runtime_id"`
	ResourceID  string `json:"resource_id"`
	RootPath    string `json:"root_path"`
	Path        string `json:"path"`
	Cursor      string `json:"cursor,omitempty"`
	PageSize    int    `json:"page_size,omitempty"`
	DeadlineMS  int64  `json:"deadline_ms,omitempty"`
}

// WorkspaceFilesReadPayload is sent server-to-daemon after authorization.
type WorkspaceFilesReadPayload struct {
	WorkspaceFilesGeneration
	DaemonReqID string `json:"daemon_req_id"`
	RuntimeID   string `json:"runtime_id"`
	ResourceID  string `json:"resource_id"`
	RootPath    string `json:"root_path"`
	Path        string `json:"path"`
	DeadlineMS  int64  `json:"deadline_ms,omitempty"`
}

// WorkspaceFilesCancelPayload cancels a server-owned daemon request.
type WorkspaceFilesCancelPayload struct {
	WorkspaceFilesGeneration
	DaemonReqID string `json:"daemon_req_id"`
	RuntimeID   string `json:"runtime_id"`
}

type WorkspaceFilesEntry struct {
	Name string `json:"name"`
	Type string `json:"type"` // "regular" or "directory"
}

// WorkspaceFilesListResultPayload is one frame of a list response. A page may
// be split into several frames to keep the complete JSON envelope below the
// daemon control-channel frame limit.
type WorkspaceFilesListResultPayload struct {
	WorkspaceFilesGeneration
	DaemonReqID string                `json:"daemon_req_id"`
	RuntimeID   string                `json:"runtime_id,omitempty"`
	ResourceID  string                `json:"resource_id"`
	Seq         int                   `json:"seq"`
	Entries     []WorkspaceFilesEntry `json:"entries"`
	NextCursor  string                `json:"next_cursor,omitempty"`
	Skipped     int                   `json:"skipped,omitempty"`
	Final       bool                  `json:"final"`
}

// WorkspaceFilesReadChunkPayload is one UTF-8-boundary-preserving chunk. Data
// is base64 encoded by encoding/json, so frame builders must account for the
// encoded size rather than the raw text size.
type WorkspaceFilesReadChunkPayload struct {
	WorkspaceFilesGeneration
	DaemonReqID string `json:"daemon_req_id"`
	RuntimeID   string `json:"runtime_id,omitempty"`
	ResourceID  string `json:"resource_id"`
	Seq         int    `json:"seq"`
	Data        []byte `json:"data"`
	EOF         bool   `json:"eof"`
}

type WorkspaceFilesErrorPayload struct {
	WorkspaceFilesGeneration
	DaemonReqID string `json:"daemon_req_id"`
	RuntimeID   string `json:"runtime_id,omitempty"`
	ResourceID  string `json:"resource_id,omitempty"`
	Code        string `json:"code"`
}

// WorkspaceFilesGeneration is server-owned and echoed by v2 daemons. Zero is
// reserved for the v1 wire format and is never accepted by the v2 relay.
type WorkspaceFilesGeneration struct {
	ConnectionEpoch uint64 `json:"connection_epoch,omitempty"`
	RelaySeq        uint64 `json:"relay_seq,omitempty"`
}

// WorkspaceFilesConnectionIdentity is a nonzero-sized server-only socket
// token. Browser input can never manufacture this pointer identity.
type WorkspaceFilesConnectionIdentity struct{ reserved byte }
type WorkspaceFilesConnection struct {
	Identity *WorkspaceFilesConnectionIdentity
	Epoch    uint64
}

func (c WorkspaceFilesConnection) Equal(other WorkspaceFilesConnection) bool {
	return c.Identity != nil && c.Identity == other.Identity && c.Epoch == other.Epoch
}

type WorkspaceFilesTarget struct {
	Connection                                  WorkspaceFilesConnection
	Generation                                  WorkspaceFilesGeneration
	RuntimeID, RequestID, WorkspaceID, DaemonID string
}
type WorkspaceFilesContext struct {
	Kind      string `json:"kind"`
	IssueID   string `json:"issue_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
}
type WorkspaceFilesClientResourcesPayload struct {
	ClientReqID string                `json:"client_req_id"`
	Context     WorkspaceFilesContext `json:"context"`
}
type WorkspaceFilesResource struct {
	ResourceID  string `json:"resource_id"`
	DisplayName string `json:"display_name"`
	Access      string `json:"access"`
}
type WorkspaceFilesClientResourcesResultPayload struct {
	ClientReqID string                   `json:"client_req_id"`
	Context     WorkspaceFilesContext    `json:"context"`
	Resources   []WorkspaceFilesResource `json:"resources"`
}
