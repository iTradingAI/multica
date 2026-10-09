package filetouch

import (
	"path"
	"strconv"
	"strings"
)

const ExtractorVersion = 1

// Claims and execution evidence are private. They are never serialized in
// touches or ordinary transcript responses.
type Binding struct {
	ResourceID string `json:"resource_id"`
	Generation int64  `json:"generation"`
	Root       string `json:"root"`
	DaemonID   string `json:"daemon_id"`
	Mode       string `json:"mode"`
}

type Claim struct {
	ProofVersion int       `json:"proof_version"`
	WorkspaceID  string    `json:"workspace_id"`
	ProjectID    string    `json:"project_id"`
	RuntimeID    string    `json:"runtime_id"`
	DaemonID     string    `json:"daemon_id"`
	Provider     string    `json:"provider"`
	DispatchedAt string    `json:"dispatched_at"`
	Bindings     []Binding `json:"bindings"`
}

type Execution struct {
	ID           string `json:"execution_id"`
	DispatchedAt string `json:"dispatched_at"`
	Cwd          string `json:"cwd"`
	Platform     string `json:"path_platform"`
	ResourceID   string `json:"resource_id,omitempty"`
}

func ValidExecution(execution Execution) bool {
	_, ok := absolute(execution.Platform, execution.Cwd)
	return ok
}

func absolute(platform, value string) (string, bool) {
	if !ValidSourcePath(platform, value) {
		return "", false
	}
	if platform == "linux" {
		return path.Clean(value), strings.HasPrefix(value, "/")
	}
	value = strings.ReplaceAll(value, `\`, "/")
	if len(value) > 2 && value[1] == ':' && value[2] == '/' {
		return value[:2] + path.Clean(value[2:]), true
	}
	if strings.HasPrefix(value, "//") {
		parts := strings.Split(value[2:], "/")
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
			return "", false
		}
		return "//" + path.Clean(value[2:]), true
	}
	return "", false
}

// RelativeIdentity interprets paths in the execution's source platform. It
// never searches current resources or guesses a root from a repository name.
func RelativeIdentity(claim Claim, execution Execution, source string) (Binding, string, bool) {
	if claim.ProofVersion != ProofVersion || execution.DispatchedAt != claim.DispatchedAt || !ValidSourcePath(execution.Platform, source) {
		return Binding{}, "", false
	}
	cwd, ok := absolute(execution.Platform, execution.Cwd)
	if !ok {
		return Binding{}, "", false
	}
	file, isAbsolute := absolute(execution.Platform, source)
	if !isAbsolute {
		if execution.Platform == "windows" && (strings.Contains(source, ":") || strings.HasPrefix(source, "/") || strings.HasPrefix(source, `\`)) {
			return Binding{}, "", false
		}
		file = path.Clean(cwd + "/" + strings.ReplaceAll(source, `\`, "/"))
		// path.Clean collapses the UNC double slash; keep its volume identity.
		if strings.HasPrefix(cwd, "//") {
			file = "/" + file
		}
	}
	var selected Binding
	var relative string
	count := 0
	for _, binding := range claim.Bindings {
		if binding.DaemonID != claim.DaemonID || binding.Generation <= 0 || execution.ResourceID != "" && binding.ResourceID != execution.ResourceID {
			continue
		}
		root, valid := absolute(execution.Platform, binding.Root)
		if !valid {
			continue
		}
		// Exact case is intentionally conservative on Windows. Do not merge
		// aliases the execution evidence cannot prove refer to the same file.
		prefix := strings.TrimSuffix(root, "/") + "/"
		if cwd != root && !strings.HasPrefix(cwd, prefix) || !strings.HasPrefix(file, prefix) {
			continue
		}
		candidate := strings.TrimPrefix(file, prefix)
		if candidate == "" || candidate == "." || strings.HasPrefix(candidate, "../") || strings.ContainsAny(candidate, "\\:\x00") {
			continue
		}
		valid = true
		for _, part := range strings.Split(candidate, "/") {
			if part == "" || part == "." || part == ".." {
				valid = false
			}
		}
		if !valid {
			continue
		}
		count++
		selected = binding
		relative = candidate
	}
	return selected, relative, count == 1
}

type Touch struct {
	ResourceID   string
	Generation   int64
	RelativePath string
	IdentityKey  string
	Op           string
	Role         string
	Status       string
	Reason       string
}

func Extract(tool string, input map[string]any, proof Integrity, claim Claim, execution Execution) []Touch {
	ids, shape := identities(tool, input)
	if tool != "Write" && tool != "Edit" && tool != "MultiEdit" && tool != "patch_apply" {
		return []Touch{{IdentityKey: "unsupported", Op: "unknown", Role: "unknown", Status: "unknown", Reason: "unsupported_tool"}}
	}
	states := map[string]State{}
	counts := map[string]int{}
	for _, slot := range proof.Slots {
		counts[slot.Slot]++
		if validSlot(slot) {
			states[slot.Slot] = slot.State
		}
	}
	if !shape || len(ids) == 0 {
		return []Touch{{IdentityKey: "shape", Op: "unknown", Role: "unknown", Status: "unknown", Reason: "missing_shape"}}
	}
	out := []Touch{}
	seen := map[string]bool{}
	for _, id := range ids {
		op, role := "edit", "file"
		if tool == "Write" {
			op = "write"
		}
		if tool == "patch_apply" {
			op = "unknown"
			if id.kind == "add" || id.kind == "update" || id.kind == "delete" {
				op = id.kind
			}
			if id.role == "destination" {
				op = "move_to"
				role = "destination"
			} else if _, present := input["changes"].([]any)[slotIndex(id.slot)].(map[string]any)["move_path"]; present {
				op = "move_from"
				role = "source"
			}
		}
		touch := Touch{IdentityKey: "unknown:" + id.slot, Op: op, Role: role, Status: "unknown", Reason: "missing_origin"}
		value, isString := id.value.(string)
		if proof.ProofVersion == ProofVersion && supported(proof.Provider, tool) && proof.Provider == claim.Provider && counts[id.slot] == 1 && states[id.slot] == Verified && isString {
			binding, relative, ok := RelativeIdentity(claim, execution, value)
			if ok {
				touch.ResourceID = binding.ResourceID
				touch.Generation = binding.Generation
				touch.RelativePath = relative
				touch.IdentityKey = binding.ResourceID + ":" + strconv.FormatInt(binding.Generation, 10) + ":" + relative
				touch.Status = "mapped"
				touch.Reason = ""
			} else {
				touch.Reason = "missing_ownership"
			}
		}
		key := touch.IdentityKey + "\x00" + touch.Op + "\x00" + touch.Role
		if !seen[key] {
			seen[key] = true
			out = append(out, touch)
		}
	}
	return out
}

func slotIndex(slot string) int {
	i := strings.Index(slot, "]")
	n := 0
	for _, b := range []byte(slot[8:i]) {
		n = n*10 + int(b-'0')
	}
	return n
}
