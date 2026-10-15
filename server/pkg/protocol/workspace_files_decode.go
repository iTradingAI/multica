package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// WorkspaceFilesRequest contains only browser-controlled, validated fields.
type WorkspaceFilesRequest struct {
	ClientReqID string                `json:"client_req_id"`
	Context     WorkspaceFilesContext `json:"context"`
	ResourceID  string                `json:"resource_id"`
	Path        string                `json:"path"`
	Cursor      string                `json:"cursor,omitempty"`
	PageSize    int                   `json:"page_size,omitempty"`
}

// StrictWorkspaceFilesJSON rejects duplicate keys as well as unknown fields.
// Duplicates are rejected recursively, before encoding/json can overwrite them.
func StrictWorkspaceFilesJSON(raw []byte, out any) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var visit func() error
	visit = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delimiter, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate field")
				}
				seen[name] = true
				if err := visit(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := visit(); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := visit(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

func WorkspaceFilesUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && strings.EqualFold(value, id.String())
}

func DecodeWorkspaceFilesRequest(event string, raw json.RawMessage) (WorkspaceFilesRequest, error) {
	var req WorkspaceFilesRequest
	invalid := errors.New("invalid request")
	if err := StrictWorkspaceFilesJSON(raw, &req); err != nil {
		return req, invalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return req, invalid
	}
	allowed := map[string]bool{"client_req_id": true}
	switch event {
	case EventWorkspaceFilesResources:
		allowed["context"] = true
	case EventWorkspaceFilesList:
		for _, key := range []string{"context", "resource_id", "path", "cursor", "page_size"} {
			allowed[key] = true
		}
	case EventWorkspaceFilesRead:
		for _, key := range []string{"context", "resource_id", "path"} {
			allowed[key] = true
		}
	case EventWorkspaceFilesCancel:
	default:
		return req, invalid
	}
	for key := range fields {
		if !allowed[key] || string(fields[key]) == "null" {
			return req, invalid
		}
	}
	if !WorkspaceFilesUUID(req.ClientReqID) {
		req.ClientReqID = ""
		return req, invalid
	}
	req.ClientReqID = strings.ToLower(req.ClientReqID)
	if event == EventWorkspaceFilesCancel {
		return req, nil
	}
	var ctx map[string]json.RawMessage
	if json.Unmarshal(fields["context"], &ctx) != nil || ctx == nil {
		return req, invalid
	}
	idField := ""
	switch req.Context.Kind {
	case "issue":
		idField = "issue_id"
		if !WorkspaceFilesUUID(req.Context.IssueID) {
			return req, invalid
		}
		req.Context.IssueID = strings.ToLower(req.Context.IssueID)
	case "project":
		idField = "project_id"
		if !WorkspaceFilesUUID(req.Context.ProjectID) {
			return req, invalid
		}
		req.Context.ProjectID = strings.ToLower(req.Context.ProjectID)
	default:
		return req, invalid
	}
	if len(ctx) != 2 || ctx["kind"] == nil || ctx[idField] == nil {
		return req, invalid
	}
	if event == EventWorkspaceFilesResources {
		return req, nil
	}
	if !WorkspaceFilesUUID(req.ResourceID) {
		return req, invalid
	}
	req.ResourceID = strings.ToLower(req.ResourceID)
	if len(req.Path) > 4096 || !utf8.ValidString(req.Path) || strings.ContainsAny(req.Path, "\\:\x00") || strings.HasPrefix(req.Path, "/") || req.Path == "" {
		return req, invalid
	}
	if req.Path != "." || event != EventWorkspaceFilesList {
		for _, part := range strings.Split(req.Path, "/") {
			if part == "" || part == "." || part == ".." {
				return req, invalid
			}
		}
	}
	if req.PageSize < 0 || req.PageSize > 200 || len(req.Cursor) > 4096 {
		return req, invalid
	}
	return req, nil
}
