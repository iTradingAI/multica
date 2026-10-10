package filetouch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/redact"
)

func TestPathIntegrityLosslessSourceJSON(t *testing.T) {
	for _, raw := range []string{`{"file_path":"safe.txt"}`, `{"file_path":"\u4e2d.txt"}`, `{"file_path":"\ud83d\ude00.txt"}`, `{"file_path":"literal\\ud800.txt"}`} {
		if !LosslessJSON([]byte(raw)) {
			t.Fatal("valid source rejected")
		}
	}
	for _, raw := range []string{`{"file_path":"\ud800.txt"}`, `{"file_path":"\udc00.txt"}`, `{"file_path":"a","file_path":"b"}`, `{"changes":[{"path":"a","p\u0061th":"b"}]}`, "{\"path\":\"\xff\"}"} {
		if LosslessJSON([]byte(raw)) {
			t.Fatal("lossy source accepted")
		}
	}
}

func decodeInput(t *testing.T, raw string) map[string]any {
	t.Helper()
	var input map[string]any
	if json.Unmarshal([]byte(raw), &input) != nil {
		t.Fatal("fixture decode")
	}
	return input
}

func TestPathIntegrityRejectsSanitizedAliasAtTransformBoundary(t *testing.T) {
	for _, tool := range []string{"Write", "Edit", "MultiEdit"} {
		raw := `{"file_path":"safe\u0000.txt"}`
		input := decodeInput(t, raw)
		proof := Origin("claude", tool, "windows", input, LosslessJSON([]byte(raw)))
		cleaned := util.SanitizeJSONForPostgres(redact.InputMap(input)).(map[string]any)
		if cleaned["file_path"] != "safe.txt" {
			t.Fatal("fixture did not exercise the lossy sanitation boundary")
		}
		final := Transform(proof, tool, input, tool, cleaned, true)
		Protect(tool, cleaned, final)
		if len(final.Slots) != 1 || final.Slots[0].State != Invalid || cleaned["file_path"] != UnverifiedPath {
			t.Fatal("illegal origin acquired a sanitized alias")
		}
		if strings.Contains(mustJSON(t, final), "safe") {
			t.Fatal("proof retained a path")
		}
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPathIntegrityRedactionAndSlotBinding(t *testing.T) {
	input := decodeInput(t, `{"changes":[{"path":"source.txt","kind":"update","move_path":"destination.txt","diff":"secret content"},{"path":"other.txt","kind":"add"}]}`)
	proof := Origin("codex", "patch_apply", "linux", input, true)
	for _, change := range []string{"content_only", "move", "kind", "missing", "version", "slot", "source", "duplicate"} {
		t.Run(change, func(t *testing.T) {
			after := decodeInput(t, mustJSON(t, input))
			candidate := Integrity{ProofVersion: proof.ProofVersion, Provider: proof.Provider, Slots: append([]Slot(nil), proof.Slots...)}
			trusted := true
			first := after["changes"].([]any)[0].(map[string]any)
			switch change {
			case "content_only":
				first["diff"] = "[REDACTED]"
			case "move":
				first["move_path"] = "different.txt"
			case "kind":
				first["kind"] = "delete"
			case "missing":
				candidate.Slots = nil
			case "version":
				candidate.ProofVersion++
			case "slot":
				candidate.Slots[0].Slot = "changes[99].path"
			case "source":
				trusted = false
			case "duplicate":
				candidate.Slots[1] = candidate.Slots[0]
			}
			final := Transform(candidate, "patch_apply", input, "patch_apply", after, trusted)
			Protect("patch_apply", after, final)
			if change == "content_only" && first["path"] != "source.txt" {
				t.Fatal("content-only transformation invalidated identity")
			}
			if change == "move" && (first["move_path"] != UnverifiedPath || first["path"] != "source.txt") {
				t.Fatal("move evidence was not isolated to its path slot")
			}
			if change != "content_only" && change != "move" && first["path"] != UnverifiedPath {
				t.Fatal("unproven identity survived")
			}
		})
	}
	// Even the provider's own Input cannot certify a path.
	forged := decodeInput(t, `{"file_path":"safe.txt","path_integrity":{"proof_version":1,"valid":true}}`)
	Protect("Write", forged, Transform(Integrity{}, "Write", forged, "Write", forged, true))
	if forged["file_path"] != UnverifiedPath {
		t.Fatal("Input self-certification accepted")
	}
}

func TestPathIntegrityNeverPromotesAndProtectsSanitizedKeys(t *testing.T) {
	before := decodeInput(t, `{"file_path\u0000":"private"}`)
	after := util.SanitizeJSONForPostgres(before).(map[string]any)
	proof := Transform(Integrity{}, "Write", before, "Write", after, false)
	Protect("Write", after, proof)
	if after["file_path"] != UnverifiedPath {
		t.Fatal("sanitized alias was not protected")
	}
	for _, state := range []State{Unknown, Changed, Invalid} {
		input := map[string]any{"file_path": "safe.txt"}
		proof := Origin("claude", "Write", "linux", input, false)
		proof.Slots[0].State = state
		proof.Slots[0].Reason = map[State]string{Unknown: "missing_origin", Changed: "transformed", Invalid: "invalid_path"}[state]
		final := Transform(proof, "Write", input, "Write", input, true)
		if final.Slots[0].State == Verified {
			t.Fatal("evidence upgraded")
		}
	}
}
