package util

import (
	"strings"
	"testing"
)

func TestNormalizeNewlines(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"empty", "", ""},
		{"LF unchanged", "中文\n\n`literal \\r\\n`\n", "中文\n\n`literal \\r\\n`\n"},
		{"CRLF", "a\r\nb\r\n\r\nc", "a\nb\n\nc"},
		{"Windows double translation", "a\r\r\nb\r\r\n\r\r\nc", "a\nb\n\nc"},
		{"repeated translation", "a\r\r\r\r\nb", "a\nb"},
		{"standalone CR", "a\rb\r\rc\r", "a\nb\n\nc\n"},
		{"mixed", "a\r\nb\r\r\nc\rd\n\nend", "a\nb\nc\nd\n\nend"},
		{"trailing contaminated newline", "a\r\r\n", "a\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeNewlines(tc.input)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if NormalizeNewlines(got) != got {
				t.Fatal("normalization is not idempotent")
			}
		})
	}
}

func TestNormalizeNewlinesContaminatedLedger(t *testing.T) {
	// A repeatedly rewritten ledger: retain intentional 2/3/4/6/12 newline
	// gaps, removing the additional CR before every original line ending.
	var clean strings.Builder
	for _, group := range []struct{ gap, count int }{{2, 2}, {3, 13}, {4, 2}, {6, 28}, {12, 20}} {
		for i := 0; i < group.count; i++ {
			clean.WriteString("台账条目：`code` [link](https://example.com)")
			clean.WriteString(strings.Repeat("\n", group.gap))
		}
	}
	want := clean.String() + "追加一\n追加二\n"
	polluted := strings.ReplaceAll(clean.String(), "\n", "\r\r\n") + "追加一\r\n追加二\r\n"
	if cr, lf := strings.Count(polluted, "\r"), strings.Count(polluted, "\n"); cr != 920 || lf != 461 {
		t.Fatalf("unexpected ledger fixture bytes: CR=%d LF=%d", cr, lf)
	}
	if got := NormalizeNewlines(polluted); got != want {
		t.Fatalf("ledger changed: got %q, want %q", got, want)
	}
}
