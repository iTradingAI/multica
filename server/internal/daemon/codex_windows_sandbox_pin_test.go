package daemon

import (
	"log/slog"
	"io"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// TestWindowsSandboxPinFromEnv covers the env leg of the pin resolution:
// unset → inherit, each valid tier, and an invalid token erroring (a typo must
// fail startup, not quietly unpin the policy).
func TestWindowsSandboxPinFromEnv(t *testing.T) {
	t.Setenv("MULTICA_CODEX_WINDOWS_SANDBOX", "")
	if pin, err := windowsSandboxPinFromEnv("MULTICA_CODEX_WINDOWS_SANDBOX"); err != nil || pin != execenv.WindowsSandboxPinInherit {
		t.Fatalf("unset env: got %v, %v; want inherit, nil", pin, err)
	}
	for raw, want := range map[string]execenv.WindowsSandboxPin{
		"off":        execenv.WindowsSandboxPinOff,
		"unelevated": execenv.WindowsSandboxPinUnelevated,
		"elevated":   execenv.WindowsSandboxPinElevated,
		" Inherit ":  execenv.WindowsSandboxPinInherit,
	} {
		t.Setenv("MULTICA_CODEX_WINDOWS_SANDBOX", raw)
		pin, err := windowsSandboxPinFromEnv("MULTICA_CODEX_WINDOWS_SANDBOX")
		if err != nil || pin != want {
			t.Fatalf("env %q: got %v, %v; want %v, nil", raw, pin, err, want)
		}
	}
	t.Setenv("MULTICA_CODEX_WINDOWS_SANDBOX", "yes")
	if _, err := windowsSandboxPinFromEnv("MULTICA_CODEX_WINDOWS_SANDBOX"); err == nil {
		t.Fatal("invalid pin token accepted from env")
	}
}

// TestLoadConfigCodexWindowsSandboxPinResolution locks the precedence:
// env applies when no override carries a flag/config value, an explicit
// override wins over env, and an invalid override fails LoadConfig.
func TestLoadConfigCodexWindowsSandboxPinResolution(t *testing.T) {
	stageFakeAgent(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/false")
	t.Setenv("MULTICA_CODEX_WINDOWS_SANDBOX", "")

	overrides := Overrides{
		ServerURL:      "http://localhost:0",
		WorkspacesRoot: t.TempDir(),
	}

	t.Setenv("MULTICA_CODEX_WINDOWS_SANDBOX", "off")
	cfg, err := LoadConfig(overrides)
	if err != nil {
		t.Fatalf("LoadConfig with env pin: %v", err)
	}
	if cfg.CodexWindowsSandboxPin != execenv.WindowsSandboxPinOff {
		t.Fatalf("env pin: got %v, want off", cfg.CodexWindowsSandboxPin)
	}

	// The flag/config.json-carried override outranks the env value.
	overrides.CodexWindowsSandbox = "elevated"
	cfg, err = LoadConfig(overrides)
	if err != nil {
		t.Fatalf("LoadConfig with override pin: %v", err)
	}
	if cfg.CodexWindowsSandboxPin != execenv.WindowsSandboxPinElevated {
		t.Fatalf("override pin: got %v, want elevated", cfg.CodexWindowsSandboxPin)
	}

	overrides.CodexWindowsSandbox = "sometimes"
	if _, err := LoadConfig(overrides); err == nil || !strings.Contains(err.Error(), "codex_windows_sandbox") {
		t.Fatalf("invalid override pin: got %v, want a codex_windows_sandbox error", err)
	}
}

// TestApplyCodexWindowsSandboxPinArgs covers the argv enforcement: an explicit
// pin strips the key from every channel it can arrive through, a tier pin
// appends the daemon's own override into the default args, and inherit leaves
// everything untouched.
func TestApplyCodexWindowsSandboxPinArgs(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	extra := []string{"--flag", "-c", "windows.sandbox=unelevated"}
	custom := []string{"-c=windows.sandbox=elevated", "--verbose"}

	// Inherit: byte-for-byte unchanged.
	gotExtra, gotCustom := applyCodexWindowsSandboxPinArgs(execenv.WindowsSandboxPinInherit, extra, custom, logger)
	if len(gotExtra) != len(extra) || len(gotCustom) != len(custom) {
		t.Fatalf("inherit modified the args: %v %v", gotExtra, gotCustom)
	}

	// Off: both channels lose the key, nothing is appended.
	gotExtra, gotCustom = applyCodexWindowsSandboxPinArgs(execenv.WindowsSandboxPinOff, extra, custom, logger)
	for _, args := range [][]string{gotExtra, gotCustom} {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "windows.sandbox") {
			t.Errorf("off pin left a windows.sandbox token: %v", args)
		}
	}
	if strings.Contains(strings.Join(gotExtra, " "), "-c") {
		t.Errorf("off pin appended a config override: %v", gotExtra)
	}

	// Unelevated: the daemon's own override is the only surviving occurrence,
	// appended to the default args.
	gotExtra, gotCustom = applyCodexWindowsSandboxPinArgs(execenv.WindowsSandboxPinUnelevated, extra, custom, logger)
	joined := strings.Join(gotExtra, " ") + " " + strings.Join(gotCustom, " ")
	if got := strings.Count(joined, "windows.sandbox"); got != 1 {
		t.Errorf("tier pin left %d windows.sandbox occurrences, want exactly the daemon's: %v / %v", got, gotExtra, gotCustom)
	}
	if !strings.HasSuffix(strings.Join(gotExtra, " "), "-c windows.sandbox=unelevated") {
		t.Errorf("tier pin did not append its override last in the default args: %v", gotExtra)
	}
}
