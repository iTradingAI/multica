//go:build windows

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestWorkspaceFilesWindowsRejectsJunctionsAtRootIntermediateAndLeaf(t *testing.T) {
	if !workspaceFilesCapabilityEnabled {
		t.Skip("safe NtCreateFile opener is unavailable")
	}
	workspaceRoot := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	createJunction(t, outside, filepath.Join(workspaceRoot, "jump"))
	rootHandle, err := newWorkspaceFilesOpener().OpenRoot(context.Background(), workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer rootHandle.Close()
	child, err := rootHandle.OpenChild(context.Background(), "jump", workspaceFilesInspect)
	if err == nil {
		_ = child.Close()
		t.Fatal("leaf junction opened for inspection")
	}
	if got := workspaceFilesErrorCode(err, ""); got != protocol.WorkspaceFilesErrorSymlinkDenied {
		t.Fatalf("leaf junction error = %q", got)
	}

	ch, out := newWorkspaceFilesTestChannel(nil)
	page := runWorkspaceFilesList(t, ch, out, "junction-list", protocol.WorkspaceFilesListPayload{
		RuntimeID: "runtime", ResourceID: "resource", RootPath: workspaceRoot, Path: ".",
	})
	if page.errCode != "" || page.skipped != 1 || len(page.entries) != 0 {
		t.Fatalf("junction list result = %+v", page)
	}
	read := runWorkspaceFilesRead(t, ch, out, "junction-read", "jump/secret.txt", workspaceRoot)
	if read.errCode != protocol.WorkspaceFilesErrorSymlinkDenied || read.dataFrames != 0 {
		t.Fatalf("intermediate junction read = %+v", read)
	}

	rootLink := filepath.Join(t.TempDir(), "root-junction")
	createJunction(t, outside, rootLink)
	if _, err := newWorkspaceFilesOpener().OpenRoot(context.Background(), rootLink); workspaceFilesErrorCode(err, "") != protocol.WorkspaceFilesErrorSymlinkDenied {
		t.Fatalf("junction resource root error = %v, want symlink_denied", err)
	}
}
