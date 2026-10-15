package filetouch

import (
	"encoding/json"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"strings"
	"testing"
)

func TestFileTouchOwnershipEvidence(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		root, cwd, source := "/fixture", "/fixture/sub", "nested/file.txt"
		if platform == "windows" {
			root = `C:\fixture`
			cwd = `C:\fixture\sub`
			source = `nested\file.txt`
		}
		claim := Claim{ProofVersion: 1, Provider: "claude", DaemonID: "daemon", DispatchedAt: "claim", Bindings: []Binding{{ResourceID: "resource", Generation: 3, Root: root, DaemonID: "daemon", Mode: "in_place"}}}
		execution := Execution{DispatchedAt: "claim", Platform: platform, Cwd: cwd}
		binding, relative, ok := RelativeIdentity(claim, execution, source)
		if !ok || binding.Generation != 3 || relative != "sub/nested/file.txt" {
			t.Fatalf("%s mapping failed", platform)
		}
		for _, bad := range []string{"../../../outside.txt", "[UNVERIFIED FILE PATH]", "safe\x00.txt"} {
			if _, _, ok := RelativeIdentity(claim, execution, bad); ok {
				t.Fatal("invalid ownership accepted")
			}
		}
		stale := execution
		stale.DispatchedAt = "new-claim"
		if _, _, ok := RelativeIdentity(claim, stale, source); ok {
			t.Fatal("stale claim accepted")
		}
		ambiguous := claim
		ambiguous.Bindings = append(append([]Binding{}, claim.Bindings...), claim.Bindings[0])
		ambiguous.Bindings[1].ResourceID = "second"
		if _, _, ok := RelativeIdentity(ambiguous, execution, source); ok {
			t.Fatal("ambiguous resource guessed")
		}
		execution.ResourceID = "resource"
		if _, _, ok := RelativeIdentity(ambiguous, execution, source); !ok {
			t.Fatal("reported selection ignored")
		}
	}
}

func TestFileTouchMoveDedupAndNoHistoricalUpgrade(t *testing.T) {
	input := decodeInput(t, `{"changes":[{"kind":"update","path":"source.txt","move_path":"destination.txt"},{"kind":"update","path":"source.txt","move_path":"destination.txt"}]}`)
	claim := Claim{ProofVersion: 1, Provider: "codex", DispatchedAt: "claim", DaemonID: "daemon", Bindings: []Binding{{ResourceID: "resource", Generation: 1, Root: "/fixture", DaemonID: "daemon"}}}
	execution := Execution{DispatchedAt: "claim", Cwd: "/fixture", Platform: "linux"}
	proof := Origin("codex", "patch_apply", "linux", input, true)
	touches := Extract("patch_apply", input, proof, claim, execution)
	if len(touches) != 2 || touches[0].Op != "move_from" || touches[1].Op != "move_to" || touches[0].Status != "mapped" {
		t.Fatal("move sides or distinct paths not retained")
	}
	for _, bad := range []Integrity{{}, Origin("codex", "patch_apply", "linux", input, false)} {
		for _, touch := range Extract("patch_apply", input, bad, claim, execution) {
			if touch.Status == "mapped" || touch.RelativePath != "" {
				t.Fatal("legacy proof upgraded")
			}
		}
	}
	if touch := Extract("Bash", map[string]any{"command": "touch file.txt"}, proof, claim, execution); len(touch) != 1 || touch[0].Reason != "unsupported_tool" || touch[0].RelativePath != "" {
		t.Fatal("shell heuristic enabled")
	}
}

func TestFileTouchProofStaysOutOfDBModelJSON(t *testing.T) {
	rows := []any{db.TaskMessage{PathIntegrity: []byte(`{"raw":"private"}`)}, db.AgentTaskQueue{FileClaimSnapshot: []byte(`{"root":"private"}`)}}
	for _, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"path_integrity", "file_claim_snapshot", "file_execution_id", "source_event_id", "proof_version"} {
			if strings.Contains(string(encoded), field) {
				t.Fatalf("internal %s leaked", field)
			}
		}
	}
}
