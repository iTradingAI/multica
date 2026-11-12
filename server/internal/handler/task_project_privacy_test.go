package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRestrictedTaskResponseWithholdsFileCredentials(t *testing.T) {
	full := AgentTaskResponse{
		ID: "task-id", AgentID: "agent-id", Status: "running",
		FileTouchDaemonToken: "private-file-credential", FileTouchProofVersion: 1,
		QuickCreatePrompt: "private prompt", WorkDir: "private-workdir",
	}
	redacted := restrictedTaskResponse(full)
	if !redacted.Restricted || redacted.ID != full.ID || redacted.AgentID != full.AgentID || redacted.Status != full.Status {
		t.Fatalf("restricted response lost public presence: %+v", redacted)
	}
	encoded, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-file-credential", "file_touch_daemon_token", "file_touch_proof_version", "private prompt", "private-workdir"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("restricted response exposed %q", secret)
		}
	}
}
