package service

import (
	"github.com/multica-ai/multica/server/pkg/filetouch"
	"testing"
)

func TestFileTouchSignatureBindsPathSet(t *testing.T) {
	a := filetouch.Touch{IdentityKey: "resource:1:a.txt", Op: "edit", Role: "file", Status: "mapped"}
	b := filetouch.Touch{IdentityKey: "resource:1:b.txt", Op: "edit", Role: "file", Status: "mapped"}
	want := fileTouchSignature("execution", "Edit", []filetouch.Touch{a, b})
	if want != fileTouchSignature("execution", "Edit", []filetouch.Touch{b, a, a}) {
		t.Fatal("reordering or duplicates changed path-set identity")
	}
	for _, changed := range []string{fileTouchSignature("execution", "Write", []filetouch.Touch{a, b}), fileTouchSignature("other", "Edit", []filetouch.Touch{a, b}), fileTouchSignature("execution", "Edit", []filetouch.Touch{a})} {
		if changed == want {
			t.Fatal("different tool, execution or path set kept signature")
		}
	}
	b.IdentityKey = "resource:2:b.txt"
	if want == fileTouchSignature("execution", "Edit", []filetouch.Touch{a, b}) {
		t.Fatal("resource generation change kept signature")
	}
}
