package protocol

import "testing"

func TestWorkspaceFilesBinaryContentPolicy(t *testing.T) {
	for b := byte(0); b < 32; b++ {
		want := b != '\t' && b != '\n' && b != '\r'
		if WorkspaceFilesBinaryContent([]byte{b}) != want {
			t.Fatalf("control byte %d", b)
		}
	}
	for _, text := range []string{"", "text\twith\r\nnewlines", "中文"} {
		if WorkspaceFilesBinaryContent([]byte(text)) {
			t.Fatal("valid preview text rejected")
		}
	}
}
