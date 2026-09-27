//go:build windows

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsWorkspaceFilesOpener struct{}

type windowsWorkspaceFilesHandle struct {
	file      *os.File
	handle    windows.Handle
	isReparse bool
}

func newWorkspaceFilesOpener() workspaceFilesOpener { return windowsWorkspaceFilesOpener{} }

func workspaceFilesPlatformAvailable() bool {
	root := os.Getenv("SystemRoot")
	if root == "" {
		return false
	}
	h, err := (windowsWorkspaceFilesOpener{}).OpenRoot(context.Background(), root)
	if err != nil {
		return false
	}
	_ = h.Close()
	return true
}

var workspaceFilesCapabilityEnabled = workspaceFilesPlatformAvailable() && defaultWorkspaceFilesCursorSigner() != nil

func (windowsWorkspaceFilesOpener) OpenRoot(ctx context.Context, root string) (workspaceFilesHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, workspaceFilesContextError(err)
	}
	if root == "" || strings.IndexByte(root, 0) >= 0 || !filepath.IsAbs(root) || strings.HasPrefix(root, `\\`) {
		return nil, workspaceFilesError("invalid_path")
	}
	volume := filepath.VolumeName(root)
	if len(volume) != 2 || volume[1] != ':' {
		return nil, workspaceFilesError("unsupported")
	}
	relative := strings.TrimLeft(strings.TrimPrefix(root, volume), `\/`)
	components := make([]string, 0)
	if relative != "" {
		for _, component := range strings.FieldsFunc(relative, func(r rune) bool { return r == '\\' || r == '/' }) {
			if component == ".." || component == "." || !validWorkspaceFilesEntryName(component) {
				return nil, workspaceFilesError("invalid_path")
			}
			components = append(components, component)
		}
	}
	volumeRoot := volume + `\`
	handle, err := windows.CreateFile(
		windows.StringToUTF16Ptr(volumeRoot),
		windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, windowsWorkspaceFilesOpenError(err)
	}
	current, err := newWindowsWorkspaceFilesHandle(handle)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if current.isReparse {
		_ = current.Close()
		return nil, workspaceFilesError("symlink_denied")
	}
	info, err := current.Stat()
	if err != nil || !info.IsDir() {
		_ = current.Close()
		return nil, workspaceFilesError("not_directory")
	}
	for _, component := range components {
		next, openErr := current.OpenChild(ctx, component, workspaceFilesOpenRequestFor(workspaceFilesDirectory))
		if openErr != nil {
			_ = current.Close()
			return nil, openErr
		}
		info, statErr := next.Stat()
		if statErr != nil || !info.IsDir() || next.IsReparsePoint() {
			_ = next.Close()
			_ = current.Close()
			return nil, workspaceFilesError("not_directory")
		}
		_ = current.Close()
		current = next.(*windowsWorkspaceFilesHandle)
	}
	return current, nil
}

func newWindowsWorkspaceFilesHandle(handle windows.Handle) (*windowsWorkspaceFilesHandle, error) {
	info := windows.ByHandleFileInformation{}
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return nil, windowsWorkspaceFilesOpenError(err)
	}
	file := os.NewFile(uintptr(handle), "workspace-files")
	if file == nil {
		return nil, workspaceFilesError("unavailable")
	}
	return &windowsWorkspaceFilesHandle{
		file:      file,
		handle:    handle,
		isReparse: info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0,
	}, nil
}

func (h *windowsWorkspaceFilesHandle) Read(data []byte) (int, error) { return h.file.Read(data) }
func (h *windowsWorkspaceFilesHandle) ReadDir(n int) ([]os.DirEntry, error) {
	return h.file.ReadDir(n)
}
func (h *windowsWorkspaceFilesHandle) Stat() (os.FileInfo, error) { return h.file.Stat() }
func (h *windowsWorkspaceFilesHandle) IsReparsePoint() bool       { return h.isReparse }
func (h *windowsWorkspaceFilesHandle) Close() error               { return h.file.Close() }

func (h *windowsWorkspaceFilesHandle) OpenChild(ctx context.Context, name string, request workspaceFilesOpenRequest) (workspaceFilesHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, workspaceFilesContextError(err)
	}
	if !validWorkspaceFilesEntryName(name) {
		return nil, workspaceFilesError("invalid_path")
	}
	if !request.noFollow || !request.closeOnExec {
		return nil, workspaceFilesError("unsupported")
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, workspaceFilesError("invalid_path")
	}
	oa := &windows.OBJECT_ATTRIBUTES{
		RootDirectory: h.handle,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE,
	}
	oa.Length = uint32(unsafe.Sizeof(*oa))
	access := uint32(windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE)
	options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	switch request.mode {
	case workspaceFilesInspect:
	case workspaceFilesDirectory:
		access |= windows.FILE_LIST_DIRECTORY
		options |= windows.FILE_DIRECTORY_FILE
	case workspaceFilesReadOnly:
		access |= windows.FILE_READ_DATA
		options |= windows.FILE_NON_DIRECTORY_FILE
	default:
		return nil, workspaceFilesError("unsupported")
	}
	var ioStatus windows.IO_STATUS_BLOCK
	var child windows.Handle
	err = windows.NtCreateFile(
		&child,
		access,
		oa,
		&ioStatus,
		nil,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		options,
		0,
		0,
	)
	if err != nil {
		openErr := windowsWorkspaceFilesOpenError(err)
		if request.mode == workspaceFilesReadOnly && workspaceFilesErrorCode(openErr, "") == "not_directory" {
			return nil, workspaceFilesError("not_regular")
		}
		return nil, openErr
	}
	if err := ctx.Err(); err != nil {
		_ = windows.CloseHandle(child)
		return nil, workspaceFilesContextError(err)
	}
	opened, err := newWindowsWorkspaceFilesHandle(child)
	if err != nil {
		_ = windows.CloseHandle(child)
		return nil, err
	}
	if opened.isReparse {
		_ = opened.Close()
		return nil, workspaceFilesError("symlink_denied")
	}
	return opened, nil
}

func windowsWorkspaceFilesOpenError(err error) error {
	var status windows.NTStatus
	if errors.As(err, &status) {
		if status == windows.STATUS_STOPPED_ON_SYMLINK || status == windows.STATUS_REPARSE_POINT_ENCOUNTERED || status == windows.STATUS_IO_REPARSE_TAG_NOT_HANDLED {
			return workspaceFilesError("symlink_denied")
		}
		if status == windows.STATUS_FILE_IS_A_DIRECTORY || status == windows.STATUS_NOT_A_DIRECTORY {
			return workspaceFilesError("not_directory")
		}
	}
	if errors.Is(err, windows.ERROR_STOPPED_ON_SYMLINK) || errors.Is(err, windows.ERROR_CANT_ACCESS_FILE) {
		return workspaceFilesError("symlink_denied")
	}
	if errors.Is(err, windows.ERROR_PATH_NOT_FOUND) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return workspaceFilesError("invalid_path")
	}
	if errors.Is(err, windows.ERROR_DIRECTORY) {
		return workspaceFilesError("not_directory")
	}
	return workspaceFilesError("unavailable")
}
