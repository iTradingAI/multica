//go:build linux

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestWorkspaceFilesLinuxRejectsSymlinkAtRootAndLeaf(t *testing.T) {
	if !workspaceFilesCapabilityEnabled {
		t.Skip("openat2 with required resolve flags is unavailable")
	}
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dir-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "file-link")); err != nil {
		t.Fatal(err)
	}
	opener := newWorkspaceFilesOpener()
	h, err := opener.OpenRoot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	for _, test := range []struct {
		name string
		mode workspaceFilesOpenMode
	}{
		{name: "dir-link", mode: workspaceFilesDirectory},
		{name: "file-link", mode: workspaceFilesReadOnly},
	} {
		if _, err := h.OpenChild(context.Background(), test.name, workspaceFilesOpenRequestFor(test.mode)); workspaceFilesErrorCode(err, "") != "symlink_denied" {
			t.Fatalf("OpenChild(%q) error = %v, want symlink_denied", test.name, err)
		}
	}
	if _, err := opener.OpenRoot(context.Background(), filepath.Join(root, "dir-link")); workspaceFilesErrorCode(err, "") != "symlink_denied" {
		t.Fatalf("symlink resource root error = %v, want symlink_denied", err)
	}
}

func TestWorkspaceFilesLinuxFIFOLeafIsOpenedNonblockingAndRejected(t *testing.T) {
	if !workspaceFilesCapabilityEnabled {
		t.Skip("openat2 with required resolve flags is unavailable")
	}
	root := t.TempDir()
	fifo := filepath.Join(root, "pipe")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := newWorkspaceFilesOpener().OpenRoot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	result := make(chan error, 1)
	go func() {
		leaf, openErr := h.OpenChild(context.Background(), "pipe", workspaceFilesOpenRequestFor(workspaceFilesReadOnly))
		if openErr != nil {
			result <- openErr
			return
		}
		defer leaf.Close()
		info, statErr := leaf.Stat()
		if statErr != nil {
			result <- statErr
			return
		}
		if info.Mode().IsRegular() || info.IsDir() {
			result <- workspaceFilesError("unexpected_type")
			return
		}
		result <- nil
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("nonblocking FIFO inspection failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO open blocked waiting for a writer")
	}
}
