package repocache

import (
	"path/filepath"
	"testing"
)

func TestSameResolvedPathRejectsMissingPath(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	for _, pair := range [][2]string{{missing, missing}, {root, missing}, {missing, root}} {
		if sameResolvedPath(pair[0], pair[1]) {
			t.Errorf("sameResolvedPath(%q, %q) accepted an unresolved path", pair[0], pair[1])
		}
	}
}
