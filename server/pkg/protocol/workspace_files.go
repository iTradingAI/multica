package protocol

// Workspace-files frames travel over the daemon control connection. The
// browser-facing request identifier is translated by the server; the daemon
// only sees daemon_req_id so it cannot correlate work across client sockets.
const (
	DaemonCapabilityWorkspaceFilesV1 = "workspace-files-v1"

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
	ClientReqID string `json:"client_req_id"`
	ResourceID  string `json:"resource_id"`
	Path        string `json:"path"`
	Cursor      string `json:"cursor,omitempty"`
	PageSize    int    `json:"page_size,omitempty"`
}

type WorkspaceFilesClientReadPayload struct {
	ClientReqID string `json:"client_req_id"`
	ResourceID  string `json:"resource_id"`
	Path        string `json:"path"`
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
	DaemonReqID string `json:"daemon_req_id"`
	RuntimeID   string `json:"runtime_id"`
	ResourceID  string `json:"resource_id"`
	RootPath    string `json:"root_path"`
	Path        string `json:"path"`
	DeadlineMS  int64  `json:"deadline_ms,omitempty"`
}

// WorkspaceFilesCancelPayload cancels a server-owned daemon request.
type WorkspaceFilesCancelPayload struct {
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
	DaemonReqID string `json:"daemon_req_id"`
	RuntimeID   string `json:"runtime_id,omitempty"`
	ResourceID  string `json:"resource_id"`
	Seq         int    `json:"seq"`
	Data        []byte `json:"data"`
	EOF         bool   `json:"eof"`
}

type WorkspaceFilesErrorPayload struct {
	DaemonReqID string `json:"daemon_req_id"`
	RuntimeID   string `json:"runtime_id,omitempty"`
	ResourceID  string `json:"resource_id,omitempty"`
	Code        string `json:"code"`
}
