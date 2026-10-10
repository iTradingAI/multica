package agent

import (
	"reflect"
	"runtime"

	"github.com/multica-ai/multica/server/pkg/filetouch"
)

// codexPatchMessage explicitly binds normalized slots back to raw source roles
// before redaction. Any skipped entry or lost move role prevents certification.
func codexPatchMessage(raw any, legacy, lossless bool) Message {
	var changes []any
	if legacy {
		changes = codexNormalizeLegacyChanges(raw)
		source, ok := raw.(map[string]any)
		lossless = lossless && ok && len(source) == len(changes)
		for _, value := range changes {
			norm := value.(map[string]any)
			path, _ := norm["path"].(string)
			original, ok := source[path].(map[string]any)
			lossless = lossless && ok && reflect.DeepEqual(original["type"], norm["kind"]) && preservedMoveRole(original, norm)
		}
	} else {
		changes = codexNormalizeRawChanges(raw)
		source, ok := raw.([]any)
		lossless = lossless && ok && len(source) == len(changes)
		if lossless {
			for i, value := range changes {
				norm := value.(map[string]any)
				original, ok := source[i].(map[string]any)
				lossless = lossless && ok && reflect.DeepEqual(original["path"], norm["path"])
				if kind, ok := original["kind"].(map[string]any); ok {
					lossless = lossless && reflect.DeepEqual(kind["type"], norm["kind"]) && preservedMoveRole(kind, norm)
				} else {
					lossless = lossless && reflect.DeepEqual(original["kind"], norm["kind"]) && preservedMoveRole(original, norm)
				}
			}
		}
	}
	before := map[string]any{"changes": changes}
	proof := filetouch.Origin("codex", "patch_apply", runtime.GOOS, before, lossless)
	after := codexPatchInput(changes)
	proof = filetouch.Transform(proof, "patch_apply", before, "patch_apply", after, true)
	return Message{Type: MessageToolUse, Tool: "patch_apply", Input: after, PathIntegrity: proof}
}

func preservedMoveRole(source, normalized map[string]any) bool {
	a, presentA := source["move_path"]
	b, presentB := normalized["move_path"]
	return presentA == presentB && reflect.DeepEqual(a, b)
}
