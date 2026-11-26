package protocol

import (
	"encoding/base64"
	"strings"
	"unicode/utf8"
)

const (
	DaemonCapabilityWorkspaceFilesEdit = "workspace-files-edit-v1"
	EventWorkspaceFilesEditOpen        = "workspace_files.edit_open"
	EventWorkspaceFilesSave            = "workspace_files.save"
	EventWorkspaceFilesEditResult      = "workspace_files.edit_result"
	WorkspaceFilesEditMaxBytes         = 16 * 1024
	WorkspaceFilesFrameMaxBytes        = 48 * 1024
	WorkspaceFilesErrorConflict        = "conflict"
	WorkspaceFilesErrorReadOnly        = "read_only"
	WorkspaceFilesErrorSaveUnconfirmed = "save_unconfirmed"
)

func WorkspaceFilesIsEdit(event string) bool {
	return event == EventWorkspaceFilesEditOpen || event == EventWorkspaceFilesSave
}

// Edit data is base64 on the wire to bound JSON expansion independently of
// the submitted characters. The stored BOM and line endings are handled by the daemon.
func WorkspaceFilesEditTextCode(data []byte) string {
	switch {
	case len(data) > WorkspaceFilesEditMaxBytes:
		return WorkspaceFilesErrorTooLarge
	case !utf8.Valid(data):
		return WorkspaceFilesErrorInvalidUTF8
	case WorkspaceFilesBinaryContent(data):
		return WorkspaceFilesErrorBinaryContent
	default:
		return ""
	}
}

type WorkspaceFilesEditInputError string

func (e WorkspaceFilesEditInputError) Error() string { return string(e) }

// An opaque MAC. It binds file state and edit scope without serializing local
// filesystem identifiers or paths to the client.
func WorkspaceFilesVersionValid(version string) bool {
	if len(version) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.Strict().DecodeString(version)
	return err == nil
}

// Windows aliases and temp-file names are excluded for every path component.
func WorkspaceFilesEditName(name string) bool {
	if name == "" || strings.TrimRight(name, ". ") != name ||
		strings.ContainsAny(name, "\\/:\x00<>\"|?*") ||
		strings.IndexFunc(name, func(r rune) bool { return r < 32 }) >= 0 ||
		strings.HasPrefix(strings.ToLower(name), ".multica-save-") {
		return false
	}
	base, _, _ := strings.Cut(name, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return false
	}
	return !(len([]rune(base)) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
		strings.ContainsRune("0123456789¹²³", []rune(base)[3]))
}

type WorkspaceFilesEditPolicy struct {
	Directory   string `json:"directory"`
	VolumeGUID  string `json:"volume_guid"`
	DirectoryID string `json:"directory_id"`
}

type WorkspaceFilesEditPayload struct {
	WorkspaceFilesReadPayload
	WorkspaceID       string                   `json:"workspace_id"`
	ProjectID         string                   `json:"project_id"`
	BindingGeneration int64                    `json:"binding_generation"`
	Policy            WorkspaceFilesEditPolicy `json:"policy"`
	Version           string                   `json:"version,omitempty"`
	Data              []byte                   `json:"data"`
}

type WorkspaceFilesEditResultPayload struct {
	WorkspaceFilesGeneration
	DaemonReqID       string `json:"daemon_req_id"`
	RuntimeID         string `json:"runtime_id"`
	ResourceID        string `json:"resource_id"`
	BindingGeneration int64  `json:"binding_generation"`
	Version           string `json:"version"`
	Data              []byte `json:"data"`
}

type WorkspaceFilesClientEditResultPayload struct {
	ClientReqID       string `json:"client_req_id"`
	ResourceID        string `json:"resource_id"`
	BindingGeneration int64  `json:"binding_generation"`
	Version           string `json:"version"`
	Data              []byte `json:"data"`
}
