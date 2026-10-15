package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

const filesTestID = "00000000-0000-4000-8000-000000000001"

func TestWorkspaceFilesStrictRequests(t *testing.T) {
	base := `{"client_req_id":"` + filesTestID + `","context":{"kind":"issue","issue_id":"` + filesTestID + `"},"resource_id":"` + filesTestID + `","path":"file.txt"}`
	for _, event := range []string{EventWorkspaceFilesList, EventWorkspaceFilesRead} {
		if _, err := DecodeWorkspaceFilesRequest(event, json.RawMessage(base)); err != nil {
			t.Fatal(err)
		}
	}
	invalid := []string{
		strings.Replace(base, `"kind":"issue"`, `"kind":"unknown"`, 1),
		strings.Replace(base, `"issue_id":`, `"project_id":`, 1),
		strings.Replace(base, `"path":"file.txt"`, `"path":"../secret"`, 1),
		strings.Replace(base, `"path":"file.txt"`, `"path":"/secret"`, 1),
		strings.Replace(base, `"path":"file.txt"`, `"path":"C:\\secret"`, 1),
		strings.Replace(base, `"path":"file.txt"`, `"path":"dir\\secret"`, 1),
		strings.Replace(base, `"path":"file.txt"`, `"path":"a\u0000b"`, 1),
		strings.Replace(base, `"path":"file.txt"`, `"path":"a//b"`, 1),
		strings.Replace(base, `"path":"file.txt"`, `"path":"."`, 1),
		strings.Replace(base, `"client_req_id":"`+filesTestID+`"`, `"client_req_id":"bad"`, 1),
		strings.Replace(base, `"resource_id":"`+filesTestID+`"`, `"resource_id":"bad"`, 1),
		base + ` {}`,
		`null`,
	}
	for _, field := range []string{"root_path", "local_path", "daemon_id", "runtime_id", "workspace_id", "daemon_req_id", "connection_epoch", "relay_seq", "Context"} {
		invalid = append(invalid, strings.TrimSuffix(base, "}")+`,"`+field+`":"secret"}`)
	}
	invalid = append(invalid, strings.TrimSuffix(base, "}")+`,"client_req_id":"`+filesTestID+`"}`)
	invalid = append(invalid, strings.Replace(base, `"kind":"issue"`, `"kind":"issue","kind":"issue"`, 1))
	invalid = append(invalid, strings.Replace(base, `"kind":"issue"`, `"kind":"issue","project_id":"`+filesTestID+`"`, 1))
	invalid = append(invalid, strings.Replace(base, "file.txt", string([]byte{255}), 1))
	for i, raw := range invalid {
		if _, err := DecodeWorkspaceFilesRequest(EventWorkspaceFilesRead, json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid case %d accepted", i)
		}
	}
	list := strings.Replace(base, `"path":"file.txt"`, `"path":"."`, 1)
	if _, err := DecodeWorkspaceFilesRequest(EventWorkspaceFilesList, json.RawMessage(list)); err != nil {
		t.Fatal("valid root list", err)
	}
	for _, suffix := range []string{`,"page_size":null}`, `,"page_size":-1}`, `,"page_size":201}`, `,"page_size":1.5}`, `,"cursor":null}`, `,"cursor":"` + strings.Repeat("a", 4097) + `"}`} {
		if _, err := DecodeWorkspaceFilesRequest(EventWorkspaceFilesList, json.RawMessage(strings.TrimSuffix(list, "}")+suffix)); err == nil {
			t.Fatal("invalid list options")
		}
	}
}
func TestWorkspaceFilesResourcesCancelAndProjection(t *testing.T) {
	resources := `{"client_req_id":"` + filesTestID + `","context":{"kind":"project","project_id":"` + filesTestID + `"}}`
	if _, err := DecodeWorkspaceFilesRequest(EventWorkspaceFilesResources, json.RawMessage(resources)); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"resource_id", "path", "cursor", "page_size"} {
		raw := strings.TrimSuffix(resources, "}") + `,"` + key + `":null}`
		if _, err := DecodeWorkspaceFilesRequest(EventWorkspaceFilesResources, json.RawMessage(raw)); err == nil {
			t.Fatal(key)
		}
	}
	cancel := `{"client_req_id":"` + filesTestID + `"}`
	if _, err := DecodeWorkspaceFilesRequest(EventWorkspaceFilesCancel, json.RawMessage(cancel)); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeWorkspaceFilesRequest(EventWorkspaceFilesCancel, json.RawMessage(resources)); err == nil {
		t.Fatal("cancel accepted context")
	}
	for _, value := range []any{WorkspaceFilesClientResourcesResultPayload{}, WorkspaceFilesClientReadChunkPayload{}, WorkspaceFilesClientListResultPayload{}, WorkspaceFilesClientErrorPayload{}} {
		raw, _ := json.Marshal(value)
		for _, secret := range []string{"root_path", "local_path", "daemon_req_id", "runtime_id", "connection_epoch", "relay_seq"} {
			if strings.Contains(string(raw), secret) {
				t.Fatal(secret)
			}
		}
	}
}
