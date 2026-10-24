package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestResolveTextFlagWarnsForCarriageReturns(t *testing.T) {
	for _, flagName := range []string{"description", "content"} {
		for _, body := range []string{"中文\nnext\n", "中文\r\nnext\n", "中文\r\r\nnext\n", "中文\rnext\n"} {
			t.Run(flagName+"/"+strings.ReplaceAll(body, "\n", "LF"), func(t *testing.T) {
				t.Chdir(t.TempDir())
				if err := os.WriteFile("body.md", []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				cmd := newFlagTestCmd(flagName)
				var stderr, stdout bytes.Buffer
				cmd.SetErr(&stderr)
				cmd.SetOut(&stdout)
				_ = cmd.Flags().Set(flagName+"-file", "body.md")
				got, present, err := resolveTextFlag(cmd, flagName)
				if err != nil || !present || got != strings.TrimSuffix(body, "\n") {
					t.Fatalf("body was not preserved: %q, %v, %v", got, present, err)
				}
				if strings.Contains(body, "\r") {
					if !strings.Contains(stderr.String(), "Warning:") || !strings.Contains(stderr.String(), "--"+flagName+"-file") || !strings.Contains(stderr.String(), "LF") {
						t.Fatalf("missing actionable stderr warning: %q", stderr.String())
					}
				} else if stderr.Len() != 0 {
					t.Fatalf("LF-only input warned: %q", stderr.String())
				}
				if stdout.Len() != 0 {
					t.Fatalf("warning polluted stdout: %q", stdout.String())
				}
			})
		}
	}
}

func TestResolveTextFlagTrimsCompleteFinalLineEnding(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"LF", "body\n", "body"},
		{"CRLF", "body\r\n", "body"},
		{"CRCRLF", "body\r\r\n", "body"},
		{"keep intentional blank line", "body\r\r\n\r\r\n", "body\r\r\n"},
		{"standalone CR preserved", "body\r", "body\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, source := range []string{"file", "stdin"} {
				t.Run(source, func(t *testing.T) {
					t.Chdir(t.TempDir())
					cmd := newFlagTestCmd("content")
					cmd.SetErr(&bytes.Buffer{})
					_ = cmd.Flags().Set("content-"+source, "true")
					if source == "file" {
						if err := os.WriteFile("body.md", []byte(tc.input), 0o600); err != nil {
							t.Fatal(err)
						}
						_ = cmd.Flags().Set("content-file", "body.md")
					}
					check := func() {
						got, present, err := resolveTextFlag(cmd, "content")
						if err != nil || !present || got != tc.want {
							t.Fatalf("body = %q, %v, %v; want %q", got, present, err, tc.want)
						}
					}
					if source == "stdin" {
						pipeStdin(t, tc.input, check)
					} else {
						check()
					}
				})
			}
		})
	}
}
