package util

import "strings"

// NormalizeNewlines canonicalizes text line endings to LF. A run of CR bytes
// followed by LF represents one newline, including CRCRLF produced when a
// Windows text-mode writer translates already-CRLF input again. Standalone
// CR bytes each represent a newline. Existing LF and intentional blank lines
// are preserved; normalization is idempotent.
func NormalizeNewlines(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '\r' {
			b.WriteByte(s[i])
			i++
			continue
		}
		start := i
		for i < len(s) && s[i] == '\r' {
			i++
		}
		if i < len(s) && s[i] == '\n' {
			b.WriteByte('\n')
			i++
		} else {
			for j := start; j < i; j++ {
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}
