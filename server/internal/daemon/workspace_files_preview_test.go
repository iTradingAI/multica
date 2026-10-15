package daemon

import (
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestWorkspaceFilesBinaryPreviewNoEarlyData(t *testing.T) {
	for _, data := range [][]byte{{0}, {'a', 1, 'b'}, {31}} {
		root := fakeWorkspaceFilesDirectory()
		root.add("binary", fakeWorkspaceFilesRegular(data))
		ch, out := newWorkspaceFilesTestChannel(root)
		result := runWorkspaceFilesRead(t, ch, out, "binary-read", "binary")
		if result.errCode != protocol.WorkspaceFilesErrorBinaryContent || result.dataFrames != 0 || len(result.data) != 0 {
			t.Fatalf("binary preview must return only an error: code=%s, frames=%d, bytes=%d", result.errCode, result.dataFrames, len(result.data))
		}
	}
}
