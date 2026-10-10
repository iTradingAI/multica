// Package filetouch defines file identity evidence without retaining raw paths
// or accepting claims embedded in provider tool inputs.
package filetouch

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

const ProofVersion = 1
const UnverifiedPath = "[UNVERIFIED FILE PATH]"

type State string

const (
	Verified State = "verified"
	Invalid  State = "invalid"
	Changed  State = "changed"
	Unknown  State = "unknown"
)

type Slot struct {
	Slot   string `json:"slot"`
	State  State  `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// Integrity is internal ingest metadata, never a transcript response field.
// Its vocabulary is closed and contains neither path values nor path hashes.
type Integrity struct {
	ProofVersion int    `json:"proof_version"`
	Provider     string `json:"provider"`
	Slots        []Slot `json:"slots"`
}

// Bad or future metadata downgrades evidence, not transcript availability.
// In particular, a path-bearing self-proof cannot silently become valid when
// encoding/json discards its unknown fields.
func (proof *Integrity) UnmarshalJSON(raw []byte) error {
	type wire Integrity
	var value wire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil {
		*proof = Integrity{}
		return nil
	}
	*proof = Integrity(value)
	return nil
}

type identity struct {
	slot, kind, role string
	value            any
	set              func(any)
}

func identities(tool string, input map[string]any) ([]identity, bool) {
	if tool == "Write" || tool == "Edit" || tool == "MultiEdit" {
		if input == nil {
			return nil, false
		}
		return []identity{{slot: "file_path", role: "file", value: input["file_path"], set: func(value any) { input["file_path"] = value }}}, true
	}
	if tool != "patch_apply" {
		return nil, true
	}
	changes, ok := input["changes"].([]any)
	if !ok || len(changes) == 0 || len(changes) > 4096 {
		return nil, false
	}
	out := make([]identity, 0, len(changes))
	for i, value := range changes {
		entry, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		kind, _ := entry["kind"].(string)
		prefix := "changes[" + strconv.Itoa(i) + "]"
		out = append(out, identity{slot: prefix + ".path", kind: kind, role: "source", value: entry["path"], set: func(value any) { entry["path"] = value }})
		if _, present := entry["move_path"]; present {
			out = append(out, identity{slot: prefix + ".move_path", kind: kind, role: "destination", value: entry["move_path"], set: func(value any) { entry["move_path"] = value }})
		}
	}
	return out, true
}

func supported(provider, tool string) bool {
	return provider == "claude" && (tool == "Write" || tool == "Edit" || tool == "MultiEdit") || provider == "codex" && tool == "patch_apply"
}

// Origin runs before the adapter's first lossy operation. lossless refers to
// the original enclosing event, not re-marshaled decoded tool input.
func Origin(provider, tool, platform string, input map[string]any, lossless bool) Integrity {
	proof := Integrity{ProofVersion: ProofVersion, Provider: provider}
	ids, shape := identities(tool, input)
	for _, id := range ids {
		slot := Slot{Slot: id.slot, State: Unknown, Reason: "missing_origin"}
		if shape && lossless && supported(provider, tool) {
			path, ok := id.value.(string)
			if !ok || !ValidSourcePath(platform, path) || tool == "patch_apply" && id.kind != "add" && id.kind != "update" && id.kind != "delete" {
				slot.State, slot.Reason = Invalid, "invalid_path"
			} else {
				slot.State, slot.Reason = Verified, ""
			}
		}
		proof.Slots = append(proof.Slots, slot)
	}
	return proof
}

func ValidSourcePath(platform, path string) bool {
	if path == "" || len(path) > 4096 || !utf8.ValidString(path) || path == UnverifiedPath || strings.ContainsRune(path, 0) {
		return false
	}
	for _, r := range path {
		if r < 32 {
			return false
		}
	}
	if platform == "linux" {
		return !strings.Contains(path, `\`)
	}
	if platform != "windows" || strings.ContainsAny(path, `<>"|?*`) {
		return false
	}
	if len(path) >= 2 && path[1] == ':' {
		if len(path) < 3 || path[2] != '/' && path[2] != '\\' || !asciiLetter(path[0]) {
			return false
		}
		path = path[2:]
	}
	if strings.Contains(path, ":") {
		return false
	}
	for _, part := range strings.Split(strings.ReplaceAll(path, `\`, "/"), "/") {
		if part != "." && part != ".." && (strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ")) {
			return false
		}
	}
	return true
}

func asciiLetter(b byte) bool { return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' }

// Transform computes the conjunction of the incoming evidence and identity
// preservation at this boundary. It cannot promote any unverified state.
func Transform(proof Integrity, beforeTool string, before map[string]any, afterTool string, after map[string]any, trusted bool) Integrity {
	old, oldShape := identities(beforeTool, before)
	next, nextShape := identities(afterTool, after)
	known := map[string]Slot{}
	validProof := trusted && proof.ProofVersion == ProofVersion && supported(proof.Provider, beforeTool)
	for _, slot := range proof.Slots {
		if _, duplicate := known[slot.Slot]; duplicate || !validSlot(slot) {
			validProof = false
		}
		known[slot.Slot] = slot
	}
	if len(proof.Slots) != len(old) {
		validProof = false
	}
	out := Integrity{ProofVersion: ProofVersion, Provider: proof.Provider}
	if out.Provider != "claude" && out.Provider != "codex" {
		out.Provider = ""
	}
	for i, id := range next {
		slot, exists := known[id.slot]
		if !validProof || !exists {
			slot = Slot{Slot: id.slot, State: Unknown, Reason: "missing_origin"}
		}
		if slot.State == Verified && (beforeTool != afterTool || !oldShape || !nextShape || len(old) != len(next) || i >= len(old) || old[i].slot != id.slot || old[i].kind != id.kind || old[i].role != id.role || !reflect.DeepEqual(old[i].value, id.value)) {
			slot.State, slot.Reason = Changed, "transformed"
		}
		out.Slots = append(out.Slots, slot)
	}
	return out
}

func validSlot(slot Slot) bool {
	if slot.Slot != "file_path" {
		if !strings.HasPrefix(slot.Slot, "changes[") {
			return false
		}
		close := strings.Index(slot.Slot, "]")
		if close < 8 {
			return false
		}
		n, err := strconv.Atoi(slot.Slot[8:close])
		if err != nil || n < 0 || n >= 4096 || strconv.Itoa(n) != slot.Slot[8:close] || slot.Slot[close:] != "].path" && slot.Slot[close:] != "].move_path" {
			return false
		}
	}
	switch slot.State {
	case Verified:
		return slot.Reason == ""
	case Invalid:
		return slot.Reason == "invalid_path"
	case Changed:
		return slot.Reason == "transformed" || slot.Reason == "shape_changed"
	case Unknown:
		return slot.Reason == "missing_origin" || slot.Reason == "untrusted_source" || slot.Reason == "shape_changed"
	}
	return false
}

// Protect re-enumerates the transformed identity slots, including aliases
// created by key sanitization. Missing evidence never leaves a path intact.
func Protect(tool string, input map[string]any, proof Integrity) {
	ids, shape := identities(tool, input)
	if !shape && tool == "patch_apply" && input != nil {
		input["changes"] = []any{map[string]any{"path": UnverifiedPath}}
		return
	}
	counts := map[string]int{}
	states := map[string]State{}
	for _, slot := range proof.Slots {
		counts[slot.Slot]++
		if validSlot(slot) {
			states[slot.Slot] = slot.State
		}
	}
	for _, id := range ids {
		if proof.ProofVersion != ProofVersion || !supported(proof.Provider, tool) || counts[id.slot] != 1 || states[id.slot] != Verified {
			id.set(UnverifiedPath)
		}
	}
}

// LosslessJSON rejects invalid encoding, duplicate keys and lone surrogate
// escapes before encoding/json's replacement decoding can erase their origin.
func LosslessJSON(raw []byte) bool {
	if len(raw) > 32*1024*1024 || !utf8.Valid(raw) || !json.Valid(raw) {
		return false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '"' {
			continue
		}
		for i++; i < len(raw) && raw[i] != '"'; i++ {
			if raw[i] != '\\' {
				continue
			}
			i++
			if raw[i] != 'u' {
				continue
			}
			n, _ := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
			i += 4
			if n >= 0xDC00 && n <= 0xDFFF {
				return false
			}
			if n >= 0xD800 && n <= 0xDBFF {
				if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
					return false
				}
				low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
				if err != nil || low < 0xDC00 || low > 0xDFFF {
					return false
				}
				i += 6
			}
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var visit func(int) bool
	visit = func(depth int) bool {
		if depth > 128 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, compound := token.(json.Delim)
		if !compound {
			return true
		}
		seen := map[string]bool{}
		for d.More() {
			if delim == '{' {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] {
					return false
				}
				seen[name] = true
			}
			if !visit(depth + 1) {
				return false
			}
		}
		_, err = d.Token()
		return err == nil
	}
	if !visit(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
