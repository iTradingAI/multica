package agent

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

// TestStripCodexWindowsSandboxOverrides covers the argv-level half of the
// codex_windows_sandbox pin (MAX-184): every `-c` / `--config` spelling of the
// key — inline, two-token, shell-quoted values — is removed while neighboring
// args, including other `-c` overrides, survive untouched.
func TestStripCodexWindowsSandboxOverrides(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "two-token form",
			in:   []string{"-c", "windows.sandbox=unelevated", "--verbose"},
			want: []string{"--verbose"},
		},
		{
			name: "inline form",
			in:   []string{"--config=windows.sandbox=elevated", "keep"},
			want: []string{"keep"},
		},
		{
			name: "loose spacing in the dotted key",
			in:   []string{"-c", "windows . sandbox =\"unelevated\""},
			want: []string{},
		},
		{
			// MAX-184 R1: quoted key segments are legal TOML spellings of the
			// same key; none may survive the pin's strip.
			name: "quoted first segment",
			in:   []string{"-c", `"windows".sandbox=unelevated`, "keep"},
			want: []string{"keep"},
		},
		{
			name: "quoted second segment",
			in:   []string{"--config", `windows.'sandbox'=elevated`},
			want: []string{},
		},
		{
			name: "both segments quoted",
			in:   []string{"-c", `'windows'."sandbox" = off`},
			want: []string{},
		},
		{
			name: "other -c overrides survive",
			in:   []string{"-c", "model=gpt-5", "-c", "windows.sandbox=off", "-c", "theme=dark"},
			want: []string{"-c", "model=gpt-5", "-c", "theme=dark"},
		},
		{
			name: "trailing -c without a value is preserved",
			in:   []string{"-c"},
			want: []string{"-c"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripCodexWindowsSandboxOverrides(tc.in, logger)
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
