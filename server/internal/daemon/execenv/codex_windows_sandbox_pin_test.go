package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseWindowsSandboxPin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw   string
		want  WindowsSandboxPin
		valid bool
	}{
		{"", WindowsSandboxPinInherit, true},
		{"inherit", WindowsSandboxPinInherit, true},
		{"INHERIT", WindowsSandboxPinInherit, true},
		{" off ", WindowsSandboxPinOff, true},
		{"unelevated", WindowsSandboxPinUnelevated, true},
		{"Elevated", WindowsSandboxPinElevated, true},
		{"full", WindowsSandboxPinInherit, false},
		{"native", WindowsSandboxPinInherit, false},
		{"0", WindowsSandboxPinInherit, false},
	}
	for _, tc := range cases {
		got, err := ParseWindowsSandboxPin(tc.raw)
		if tc.valid {
			if err != nil || got != tc.want {
				t.Errorf("ParseWindowsSandboxPin(%q) = %v, %v; want %v, nil", tc.raw, got, err, tc.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("ParseWindowsSandboxPin(%q) = %v; want error", tc.raw, got)
		}
	}
}

func TestApplyWindowsSandboxPin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		pin   WindowsSandboxPin
		folded windowsSandboxConfig
		want  windowsSandboxConfig
	}{
		{"inherit keeps absent", WindowsSandboxPinInherit, windowsSandboxAbsent, windowsSandboxAbsent},
		{"inherit keeps native", WindowsSandboxPinInherit, windowsSandboxNative, windowsSandboxNative},
		{"inherit keeps undecidable (fail closed, MUL-4957)", WindowsSandboxPinInherit, windowsSandboxUndecidable, windowsSandboxUndecidable},
		{"off forces absent over native", WindowsSandboxPinOff, windowsSandboxNative, windowsSandboxAbsent},
		{"off forces absent over undecidable", WindowsSandboxPinOff, windowsSandboxUndecidable, windowsSandboxAbsent},
		{"off keeps absent", WindowsSandboxPinOff, windowsSandboxAbsent, windowsSandboxAbsent},
		{"unelevated forces native over absent", WindowsSandboxPinUnelevated, windowsSandboxAbsent, windowsSandboxNative},
		{"elevated forces native over undecidable", WindowsSandboxPinElevated, windowsSandboxUndecidable, windowsSandboxNative},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := applyWindowsSandboxPin(tc.pin, tc.folded, testLogger()); got != tc.want {
				t.Fatalf("applyWindowsSandboxPin(%s, %v) = %v, want %v", tc.pin, tc.folded, got, tc.want)
			}
		})
	}
}

func TestStripWindowsSandboxKeyFromContent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		wantOut string
	}{
		{
			name:    "no key is untouched",
			in:      "model = \"gpt\"\n\n[windows]\nother = 1\n",
			wantOut: "model = \"gpt\"\n\n[windows]\nother = 1\n",
		},
		{
			name:    "table form strips only the sandbox key",
			in:      "model = \"gpt\"\n\n[windows]\nsandbox = \"unelevated\"\nother = 1\n",
			wantOut: "model = \"gpt\"\n\n[windows]\nother = 1\n",
		},
		{
			name:    "table form with quoted key",
			in:      "[windows]\nsandbox=\"elevated\"\n",
			wantOut: "[windows]\n",
		},
		{
			// MAX-184 R1: quoted table headers address the same TOML table.
			name:    "double-quoted table header (R1)",
			in:      "model = \"gpt\"\n\n[\"windows\"]\nsandbox = \"unelevated\"\nother = 1\n",
			wantOut: "model = \"gpt\"\n\n[\"windows\"]\nother = 1\n",
		},
		{
			name:    "single-quoted table header (R1)",
			in:      "['windows']\nsandbox = \"unelevated\"\n",
			wantOut: "['windows']\n",
		},
		{
			name:    "quoted bare key inside the windows table (R1)",
			in:      "[windows]\n\"sandbox\" = \"unelevated\"\n",
			wantOut: "[windows]\n",
		},
		{
			name:    "root dotted with quoted first segment (R1)",
			in:      "\"windows\".sandbox = \"unelevated\"\nmodel = \"gpt\"\n",
			wantOut: "model = \"gpt\"\n",
		},
		{
			name:    "root dotted with quoted second segment (R1)",
			in:      "windows.'sandbox' = \"unelevated\"\n",
			wantOut: "",
		},
		{
			name:    "root dotted with both segments quoted (R1)",
			in:      "'windows'.\"sandbox\"=\"elevated\"\n",
			wantOut: "",
		},
		{
			// Bare keys are case-sensitive in TOML: WINDOWS is a different table.
			name:    "differently-cased table is preserved",
			in:      "[WINDOWS]\nsandbox = \"nope\"\n",
			wantOut: "[WINDOWS]\nsandbox = \"nope\"\n",
		},
		{
			// A nested table under windows is not the windows table; its keys
			// address windows.<name>.* — a different setting entirely.
			name:    "nested windows table is preserved",
			in:      "[windows.display]\nsandbox = \"nope\"\n",
			wantOut: "[windows.display]\nsandbox = \"nope\"\n",
		},
		{
			name:    "root dotted form",
			in:      "windows.sandbox = \"unelevated\"\nmodel = \"gpt\"\n",
			wantOut: "model = \"gpt\"\n",
		},
		{
			name:    "root dotted form with loose spacing",
			in:      "windows .  sandbox =\"elevated\"\n",
			wantOut: "",
		},
		{
			name:    "sandbox key inside another table is preserved",
			in:      "[profile.x]\nsandbox = \"nope\"\n",
			wantOut: "[profile.x]\nsandbox = \"nope\"\n",
		},
		{
			name:    "bare sandbox at root is preserved (not a windows.sandbox selection)",
			in:      "sandbox = \"not-the-codex-key\"\n",
			wantOut: "sandbox = \"not-the-codex-key\"\n",
		},
		{
			name:    "managed block is preserved",
			in:      multicaManagedBeginMarker + "\nsandbox_mode = \"danger-full-access\"\n" + multicaManagedEndMarker + "\n[windows]\nsandbox = \"unelevated\"\n",
			wantOut: multicaManagedBeginMarker + "\nsandbox_mode = \"danger-full-access\"\n" + multicaManagedEndMarker + "\n[windows]\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, removed := stripWindowsSandboxKeyFromContent(tc.in)
			wantRemoved := tc.in != tc.wantOut
			if removed != wantRemoved {
				t.Fatalf("removed = %v, want %v", removed, wantRemoved)
			}
			if got != tc.wantOut {
				t.Fatalf("strip result:\n%q\nwant:\n%q", got, tc.wantOut)
			}
		})
	}
}

// TestPrepareCodexHomeWindowsSandboxPinOffNeutralizesDesktopRewrite is the
// MAX-184 recurrence drill at unit level: the shared ~/.codex/config.toml has
// been rewritten (as Codex Desktop does) to select the native sandbox, the
// daemon pins codex_windows_sandbox=off, and the prepared per-task home must
// come out unsandboxed with the user-level key gone from the copy.
func TestPrepareCodexHomeWindowsSandboxPinOffNeutralizesDesktopRewrite(t *testing.T) {
	for _, tc := range []struct {
		name         string
		sharedConfig string
	}{
		{"table form", "[windows]\nsandbox = \"unelevated\"\n"},
		{"dotted form", "windows.sandbox = \"unelevated\"\n"},
		// MAX-184 R1: the quoted spellings address the same TOML key and must
		// be neutralized just like the bare forms.
		{"quoted table form", "[\"windows\"]\nsandbox = \"unelevated\"\n"},
		{"quoted dotted form", "'windows'.\"sandbox\" = \"unelevated\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sharedHome := t.TempDir()
			if err := os.WriteFile(filepath.Join(sharedHome, "config.toml"), []byte(tc.sharedConfig), 0o644); err != nil {
				t.Fatalf("write shared config: %v", err)
			}
			t.Setenv("CODEX_HOME", sharedHome)

			codexHome := filepath.Join(t.TempDir(), "codex-home")
			if err := prepareCodexHomeWithOpts(codexHome, CodexHomeOptions{
				GOOS:              "windows",
				WindowsSandboxPin: WindowsSandboxPinOff,
			}, testLogger()); err != nil {
				t.Fatalf("prepareCodexHomeWithOpts: %v", err)
			}

			data, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
			if err != nil {
				t.Fatalf("read per-task config: %v", err)
			}
			config := string(data)
			if strings.Contains(config, "windows.sandbox") {
				t.Errorf("per-task config still selects the native sandbox:\n%s", config)
			}
			if !strings.Contains(config, `sandbox_mode = "danger-full-access"`) {
				t.Errorf("managed block did not pin danger-full-access:\n%s", config)
			}
			// The shared home the desktop rewrote must itself be untouched:
			// the daemon neutralizes the COPY, never the user's file.
			shared, err := os.ReadFile(filepath.Join(sharedHome, "config.toml"))
			if err != nil {
				t.Fatalf("read shared config: %v", err)
			}
			if string(shared) != tc.sharedConfig {
				t.Errorf("shared config was modified by the pin:\n%s", shared)
			}
		})
	}
}

// TestPrepareCodexHomeWindowsSandboxPinTierForcesWorkspaceWrite covers the
// explicit native-tier pin: the managed block keeps workspace-write and the
// user-level key is neutralized in the copy (the tier itself travels as the
// daemon's `-c windows.sandbox=<tier>` launch arg; see
// applyCodexWindowsSandboxPinArgs in the daemon package).
func TestPrepareCodexHomeWindowsSandboxPinTierForcesWorkspaceWrite(t *testing.T) {
	sharedHome := t.TempDir()
	// The shared config selects a DIFFERENT tier than the pin: the pin wins.
	if err := os.WriteFile(filepath.Join(sharedHome, "config.toml"), []byte("[windows]\nsandbox = \"elevated\"\n"), 0o644); err != nil {
		t.Fatalf("write shared config: %v", err)
	}
	t.Setenv("CODEX_HOME", sharedHome)

	codexHome := filepath.Join(t.TempDir(), "codex-home")
	if err := prepareCodexHomeWithOpts(codexHome, CodexHomeOptions{
		GOOS:              "windows",
		WindowsSandboxPin: WindowsSandboxPinUnelevated,
	}, testLogger()); err != nil {
		t.Fatalf("prepareCodexHomeWithOpts: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		t.Fatalf("read per-task config: %v", err)
	}
	if strings.Contains(string(data), "windows.sandbox") {
		t.Errorf("user-level tier survived into the per-task config:\n%s", data)
	}
	if !strings.Contains(string(data), `sandbox_mode = "workspace-write"`) {
		t.Errorf("managed block did not keep workspace-write:\n%s", data)
	}
}

// TestPrepareCodexHomeWindowsSandboxPinInheritKeepsFailClosed locks the
// default: without a pin, a shared config whose windows.sandbox value Codex
// would reject (valid TOML, invalid tier) still fails closed to
// workspace-write — the pin feature must not weaken MUL-4957.
func TestPrepareCodexHomeWindowsSandboxPinInheritKeepsFailClosed(t *testing.T) {
	sharedHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(sharedHome, "config.toml"), []byte("[windows]\nsandbox = \"sometimes\"\n"), 0o644); err != nil {
		t.Fatalf("write shared config: %v", err)
	}
	t.Setenv("CODEX_HOME", sharedHome)

	codexHome := filepath.Join(t.TempDir(), "codex-home")
	if err := prepareCodexHomeWithOpts(codexHome, CodexHomeOptions{
		GOOS: "windows",
	}, testLogger()); err != nil {
		t.Fatalf("prepareCodexHomeWithOpts: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		t.Fatalf("read per-task config: %v", err)
	}
	if !strings.Contains(string(data), `sandbox_mode = "workspace-write"`) {
		t.Errorf("undecidable config loosened without a pin:\n%s", data)
	}
}
