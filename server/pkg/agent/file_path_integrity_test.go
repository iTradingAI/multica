package agent

import (
	"encoding/json"
	"github.com/multica-ai/multica/server/pkg/filetouch"
	"log/slog"
	"testing"
)

func TestClaudePathIntegrityThreeTools(t *testing.T) {
	for _, tool := range []string{"Write", "Edit", "MultiEdit"} {
		for _, path := range []string{`safe.txt`, `safe\u0000.txt`, `safe\ud800.txt`} {
			raw := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"fixture","name":"` + tool + `","input":{"file_path":"` + path + `","content":"fixture"}}]}}`
			var event claudeSDKMessage
			event.pathJSONLossless = filetouch.LosslessJSON([]byte(raw))
			lossless := event.pathJSONLossless
			if json.Unmarshal([]byte(raw), &event) != nil {
				t.Fatal("fixture decode")
			}
			event.pathJSONLossless = lossless
			messages := make(chan Message, 8)
			backend := &claudeBackend{cfg: Config{Logger: slog.Default()}}
			backend.handleAssistant(event, messages, map[string]TokenUsage{}, map[string]struct{}{})
			select {
			case message := <-messages:
				if message.Type != MessageToolUse || len(message.PathIntegrity.Slots) != 1 {
					t.Fatal("origin proof missing")
				}
				verified := message.PathIntegrity.Slots[0].State == filetouch.Verified
				if verified != (path == "safe.txt") {
					t.Fatal("lossy source acquired verification")
				}
			default:
				t.Fatal("missing tool use")
			}
		}
	}
}

func TestCodexPathIntegrityRawLegacyRoles(t *testing.T) {
	fixtures := []struct {
		raw    string
		legacy bool
	}{
		{`[{"path":"source.txt","kind":{"type":"update","move_path":"destination.txt"},"diff":"fixture"}]`, false},
		{`{"source.txt":{"type":"update","move_path":"destination.txt","unified_diff":"fixture"}}`, true},
	}
	for _, fixture := range fixtures {
		var raw any
		if json.Unmarshal([]byte(fixture.raw), &raw) != nil {
			t.Fatal("fixture decode")
		}
		message := codexPatchMessage(raw, fixture.legacy, true)
		if len(message.PathIntegrity.Slots) != 2 || message.PathIntegrity.Slots[0].State != filetouch.Verified || message.PathIntegrity.Slots[1].State != filetouch.Verified {
			t.Fatal("move source/destination role lost")
		}
		for _, slot := range codexPatchMessage(raw, fixture.legacy, false).PathIntegrity.Slots {
			if slot.State == filetouch.Verified {
				t.Fatal("lossy JSON upgraded")
			}
		}
	}
	var flat any
	json.Unmarshal([]byte(`[{"path":"source.txt","kind":"update","move_path":"destination.txt"}]`), &flat)
	for _, slot := range codexPatchMessage(flat, false, true).PathIntegrity.Slots {
		if slot.State == filetouch.Verified {
			t.Fatal("discarded flattened move acquired verification")
		}
	}
}
