package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
	"golang.org/x/sys/windows"
)

func newWindowsWorkspaceEditFixture(t *testing.T) (protocol.WorkspaceFilesEditPayload, string) {
	t.Helper()
	root := t.TempDir()
	editRoot := filepath.Join(root, "editable")
	if err := os.Mkdir(editRoot, 0700); err != nil {
		t.Fatal(err)
	}
	var volumeName [64]uint16
	driveRoot := windows.StringToUTF16Ptr(filepath.VolumeName(root) + "\\")
	if err := windows.GetVolumeNameForVolumeMountPoint(driveRoot, &volumeName[0], uint32(len(volumeName))); err != nil {
		t.Fatal(err)
	}
	volume := strings.TrimSuffix(strings.TrimPrefix(windows.UTF16ToString(volumeName[:]), "\\\\?\\Volume{"), "}\\")
	dir, err := windows.CreateFile(windows.StringToUTF16Ptr(editRoot), windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := windowsEditIdentity(dir, true)
	_ = windows.CloseHandle(dir)
	if err != nil {
		t.Fatal(err)
	}
	req := protocol.WorkspaceFilesEditPayload{
		WorkspaceFilesReadPayload: protocol.WorkspaceFilesReadPayload{RootPath: root, Path: "editable/note.txt", ResourceID: "fixture"},
		BindingGeneration:         1,
		Policy:                    protocol.WorkspaceFilesEditPolicy{Directory: "editable", VolumeGUID: volume, DirectoryID: id},
	}
	return req, filepath.Join(editRoot, "note.txt")
}

func TestWorkspaceFilesWindowsEditFixture(t *testing.T) {
	req, file := newWindowsWorkspaceEditFixture(t)
	original := append(bytes.Clone(workspaceFilesBOM), []byte("old\r\nline\r\n")...)
	if err := os.WriteFile(file, original, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := defaultWorkspaceFilesCursorSigner()
	read, err := performWorkspaceFilesEdit(ctx, req, key, false)
	if err != nil {
		t.Fatalf("open editable file: %v", err)
	}
	var opened protocol.WorkspaceFilesEditResultPayload
	if err := json.Unmarshal(read, &opened); err != nil || string(opened.Data) != "old\nline\n" {
		t.Fatalf("open result: %q %v", opened.Data, err)
	}
	req.Version, req.Data = opened.Version, []byte("new\nline\n")
	result, err := performWorkspaceFilesEdit(ctx, req, key, true)
	if err != nil {
		t.Fatalf("save editable file: %v", err)
	}
	var saved protocol.WorkspaceFilesEditResultPayload
	if err := json.Unmarshal(result, &saved); err != nil || saved.Version == opened.Version || !bytes.Equal(saved.Data, req.Data) {
		t.Fatalf("save result: %#v %v", saved, err)
	}
	got, err := os.ReadFile(file)
	want := append(bytes.Clone(workspaceFilesBOM), []byte("new\r\nline\r\n")...)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("disk = %q, want %q: %v", got, want, err)
	}
	if _, err := performWorkspaceFilesEdit(ctx, req, key, true); workspaceFilesErrorCode(err, "") != "conflict" {
		t.Fatalf("second save from same version: %v", err)
	}
	reopened, err := performWorkspaceFilesEdit(ctx, req, key, false)
	if err != nil || !bytes.Equal(reopened, result) {
		t.Fatalf("reopened does not match confirmed save: %v", err)
	}
}

func TestWorkspaceFilesWindowsEditPinnedParent(t *testing.T) {
	req, file := newWindowsWorkspaceEditFixture(t)
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := defaultWorkspaceFilesCursorSigner()
	read, err := performWorkspaceFilesEdit(ctx, req, key, false)
	if err != nil {
		t.Fatal(err)
	}
	var opened protocol.WorkspaceFilesEditResultPayload
	if err := json.Unmarshal(read, &opened); err != nil {
		t.Fatal(err)
	}
	req.Version, req.Data = opened.Version, []byte("submitted")
	moved := req.RootPath + "-moved"
	_, err = editWindowsWorkspaceFile(ctx, req, key, true, func() {
		if err := os.Rename(req.RootPath, moved); err == nil {
			t.Error("pinned ancestor moved")
			_ = os.Rename(moved, req.RootPath)
		}
		if err := os.WriteFile(file, []byte("local change after final compare"), 0600); err != nil {
			t.Error(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != "submitted" {
		t.Fatalf("disk = %q: %v", got, err)
	}
}

func TestWorkspaceFilesWindowsEditConflictsAndFence(t *testing.T) {
	req, file := newWindowsWorkspaceEditFixture(t)
	ctx := context.Background()
	key := defaultWorkspaceFilesCursorSigner()
	if err := os.WriteFile(file, []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := performWorkspaceFilesEdit(ctx, req, key, false)
	if err != nil {
		t.Fatal(err)
	}
	var base protocol.WorkspaceFilesEditResultPayload
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	req.Version, req.Data = base.Version, []byte("draft")
	for _, change := range []struct {
		name   string
		action func() error
	}{
		{"modify", func() error { return os.WriteFile(file, []byte("external"), 0600) }},
		{"remove", func() error { return os.Remove(file) }},
		{"replace", func() error { return os.WriteFile(file, []byte("base"), 0600) }},
	} {
		t.Run(change.name, func(t *testing.T) {
			if err := change.action(); err != nil {
				t.Fatal(err)
			}
			if _, err := performWorkspaceFilesEdit(ctx, req, key, true); workspaceFilesErrorCode(err, "") != "conflict" {
				t.Fatalf("save = %v, want conflict", err)
			}
		})
	}
	sentinel := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(sentinel, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(sentinel, file); err != nil {
		t.Fatal(err)
	}
	if _, err := performWorkspaceFilesEdit(ctx, req, key, false); workspaceFilesErrorCode(err, "") != "read_only" {
		t.Fatalf("hard link open = %v, want read_only", err)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "untouched" {
		t.Fatalf("sentinel changed: %s %v", got, err)
	}
}

func TestWorkspaceFilesWindowsEditLateLink(t *testing.T) {
	req, file := newWindowsWorkspaceEditFixture(t)
	sentinel := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(sentinel, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(req.RootPath, "probe")
	if err := os.Symlink(sentinel, probe); err != nil {
		t.Skipf("Windows fixture cannot create file symlinks: %v", err)
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := defaultWorkspaceFilesCursorSigner()
	raw, err := performWorkspaceFilesEdit(ctx, req, key, false)
	if err != nil {
		t.Fatal(err)
	}
	var base protocol.WorkspaceFilesEditResultPayload
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	req.Version, req.Data = base.Version, []byte("draft")
	_, err = editWindowsWorkspaceFile(ctx, req, key, true, func() {
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(sentinel, file); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("after late leaf change: %v", err)
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "outside" {
		t.Fatalf("outside file changed: %s %v", got, err)
	}
}
