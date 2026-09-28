package execenv

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// WindowsSandboxPin is the daemon-level policy for Codex's native Windows
// sandbox (`windows.sandbox`), configured through `multica config set
// codex_windows_sandbox`, MULTICA_CODEX_WINDOWS_SANDBOX, or
// --codex-windows-sandbox.
//
// It exists because the historical signal for that sandbox is the user's own
// ~/.codex/config.toml — a file Codex Desktop rewrites at will — and the
// per-task home copies that file verbatim (see prepareCodexHomeWithOpts). One
// desktop rewrite therefore flipped every subsequent daemon codex task onto
// (or off) a restricted token without any daemon operator action. The pin
// moves the decision to the daemon operator: when explicit, it overrides the
// folded per-task signals (the copied config.toml key and any `-c
// windows.sandbox=...` launch arg, both of which the daemon also neutralizes
// for explicit pins) and fails neither open nor closed on the file's state —
// the operator's intent is known, which is what the MUL-4957 fail-closed
// logic was reconstructing in its absence.
type WindowsSandboxPin int

const (
	// WindowsSandboxPinInherit keeps the historical behavior: the per-task
	// signals decide, failing closed when undecidable (MUL-4957). The zero
	// value, so an unconfigured daemon keeps its current semantics.
	WindowsSandboxPinInherit WindowsSandboxPin = iota
	// WindowsSandboxPinOff forces the native sandbox off: the managed block
	// writes danger-full-access, and the user-level windows.sandbox key is
	// stripped from the per-task config copy and from the launch args, so a
	// desktop rewrite cannot put the restricted token back.
	WindowsSandboxPinOff
	// WindowsSandboxPinUnelevated and WindowsSandboxPinElevated force the
	// native sandbox on at that tier: workspace-write stays in force and the
	// daemon injects its own `-c windows.sandbox=<tier>` (the only surviving
	// occurrence, so Codex's last-wins `-c` precedence makes it effective).
	WindowsSandboxPinUnelevated
	WindowsSandboxPinElevated
)

// ParseWindowsSandboxPin maps a configured value to a pin. Empty and
// "inherit" are the default; the tier names are Codex's own windows.sandbox
// values so operators only learn one vocabulary. Case-insensitive and
// whitespace-tolerant to survive hand-edited config files.
func ParseWindowsSandboxPin(raw string) (WindowsSandboxPin, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "inherit":
		return WindowsSandboxPinInherit, nil
	case "off":
		return WindowsSandboxPinOff, nil
	case "unelevated":
		return WindowsSandboxPinUnelevated, nil
	case "elevated":
		return WindowsSandboxPinElevated, nil
	default:
		return WindowsSandboxPinInherit, fmt.Errorf(
			"invalid codex_windows_sandbox value %q (want off, inherit, unelevated, or elevated)", raw)
	}
}

func (p WindowsSandboxPin) String() string {
	switch p {
	case WindowsSandboxPinOff:
		return "off"
	case WindowsSandboxPinUnelevated:
		return "unelevated"
	case WindowsSandboxPinElevated:
		return "elevated"
	default:
		return "inherit"
	}
}

// Explicit reports whether the pin overrides the folded per-task signals.
func (p WindowsSandboxPin) Explicit() bool {
	return p != WindowsSandboxPinInherit
}

// Tier returns the Codex windows.sandbox value an explicit native pin
// selects, or "" for inherit/off.
func (p WindowsSandboxPin) Tier() string {
	switch p {
	case WindowsSandboxPinUnelevated:
		return "unelevated"
	case WindowsSandboxPinElevated:
		return "elevated"
	default:
		return ""
	}
}

// applyWindowsSandboxPin folds an explicit pin over the per-task signals.
// "off" forces Absent even over an undecidable config — the pin states the
// operator's intent, which is the thing undecidability was hedging against —
// and a tier pin forces Native regardless of what the signals said. Inherit
// returns the folded state untouched.
func applyWindowsSandboxPin(pin WindowsSandboxPin, folded windowsSandboxConfig, logger *slog.Logger) windowsSandboxConfig {
	switch pin {
	case WindowsSandboxPinOff:
		if folded != windowsSandboxAbsent && logger != nil {
			logger.Warn("codex sandbox: codex_windows_sandbox=off pin overriding a detected native sandbox selection",
				"detected", windowsSandboxStateLabel(folded),
				"pin", pin.String())
		}
		return windowsSandboxAbsent
	case WindowsSandboxPinUnelevated, WindowsSandboxPinElevated:
		if folded != windowsSandboxNative && logger != nil {
			logger.Info("codex sandbox: codex_windows_sandbox pin forcing the native sandbox tier",
				"detected", windowsSandboxStateLabel(folded),
				"pin", pin.String())
		}
		return windowsSandboxNative
	default:
		return folded
	}
}

func windowsSandboxStateLabel(state windowsSandboxConfig) string {
	switch state {
	case windowsSandboxNative:
		return "native"
	case windowsSandboxUndecidable:
		return "undecidable"
	default:
		return "absent"
	}
}

// stripWindowsSandboxKey removes the user-level windows.sandbox selection
// from a per-task config.toml so Codex cannot act on a value the daemon's pin
// has already overruled. Both spellings are handled — the `[windows]` table
// form and the root dotted-key form — while every other key in the file,
// including unrelated members of a `[windows]` table, is preserved. A missing
// file is a no-op: there is no key to neutralize, and the managed block pass
// that follows creates the file when needed.
//
// Called for explicit pins only (see prepareCodexHomeWithOpts). A pin that
// could not rewrite the file must fail the prepare: launching with the user's
// key still live under a danger-full-access pin is exactly the silent-policy
// split this pin exists to end.
func stripWindowsSandboxKey(configPath string, logger *slog.Logger) error {
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read config.toml to apply the windows.sandbox pin: %w", err)
	}
	stripped, removed := stripWindowsSandboxKeyFromContent(string(data))
	if !removed {
		return nil
	}
	if err := os.WriteFile(configPath, []byte(stripped), 0o644); err != nil {
		return fmt.Errorf("write config.toml to apply the windows.sandbox pin: %w", err)
	}
	if logger != nil {
		logger.Info("codex sandbox: neutralized the user-level windows.sandbox key in the task config (pin)",
			"config_path", configPath)
	}
	return nil
}

// stripWindowsSandboxKeyFromContent is the pure text transformation behind
// stripWindowsSandboxKey, split out so the table/dotted-key matrix is testable
// without touching the filesystem. Returns the content and whether anything
// was removed.
func stripWindowsSandboxKeyFromContent(content string) (string, bool) {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	removed := false
	inWindowsTable := false
	atRoot := true
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			atRoot = false
			inWindowsTable = trimmed == "[windows]"
			out = append(out, line)
			continue
		}
		if inWindowsTable && isWindowsSandboxValueKey(trimmed) {
			removed = true
			continue
		}
		if atRoot && codexWindowsSandboxOverrideRe.MatchString(trimmed) {
			removed = true
			continue
		}
		out = append(out, line)
	}
	if !removed {
		return content, false
	}
	return strings.Join(out, "\n"), true
}

// isWindowsSandboxValueKey reports whether a table-body line assigns the bare
// `sandbox` key: `sandbox = "unelevated"`, `sandbox="x"`, with quotes and
// whitespace both tolerated. Only ever consulted while inside a `[windows]`
// table.
func isWindowsSandboxValueKey(trimmed string) bool {
	key, _, found := strings.Cut(trimmed, "=")
	if !found {
		return false
	}
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(key), `"'`)) == "sandbox"
}
