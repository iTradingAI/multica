//go:build linux

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type linuxWorkspaceFilesOpener struct{}

type linuxWorkspaceFilesHandle struct {
	file *os.File
}

func newWorkspaceFilesOpener() workspaceFilesOpener { return linuxWorkspaceFilesOpener{} }

func workspaceFilesPlatformAvailable() bool {
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	child, err := unix.Openat2(fd, ".", &unix.OpenHow{
		Flags:   uint64(unix.O_PATH | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return false
	}
	_ = unix.Close(child)
	return true
}

var workspaceFilesCapabilityEnabled = workspaceFilesPlatformAvailable() && defaultWorkspaceFilesCursorSigner() != nil

func (linuxWorkspaceFilesOpener) OpenRoot(ctx context.Context, root string) (workspaceFilesHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, workspaceFilesContextError(err)
	}
	if root == "" || strings.IndexByte(root, 0) >= 0 || !filepath.IsAbs(root) {
		return nil, workspaceFilesError("invalid_path")
	}
	for _, component := range strings.Split(filepath.ToSlash(root), "/") {
		if component == ".." {
			return nil, workspaceFilesError("invalid_path")
		}
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, linuxWorkspaceFilesOpenError(err)
	}
	current := &linuxWorkspaceFilesHandle{file: os.NewFile(uintptr(fd), "workspace-root")}
	components := strings.Split(strings.Trim(filepath.ToSlash(root), "/"), "/")
	for _, component := range components {
		if component == "" || component == "." {
			continue
		}
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
		current = next.(*linuxWorkspaceFilesHandle)
	}
	return current, nil
}

func (h *linuxWorkspaceFilesHandle) Read(data []byte) (int, error) { return h.file.Read(data) }
func (h *linuxWorkspaceFilesHandle) ReadDir(n int) ([]os.DirEntry, error) {
	return h.file.ReadDir(n)
}
func (h *linuxWorkspaceFilesHandle) Stat() (os.FileInfo, error) { return h.file.Stat() }
func (h *linuxWorkspaceFilesHandle) IsReparsePoint() bool       { return false }
func (h *linuxWorkspaceFilesHandle) Close() error               { return h.file.Close() }

func (h *linuxWorkspaceFilesHandle) OpenChild(ctx context.Context, name string, request workspaceFilesOpenRequest) (workspaceFilesHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, workspaceFilesContextError(err)
	}
	if !validWorkspaceFilesEntryName(name) {
		return nil, workspaceFilesError("invalid_path")
	}
	if !request.nonBlocking || !request.closeOnExec || !request.noFollow {
		return nil, workspaceFilesError("unsupported")
	}
	flags := unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC
	switch request.mode {
	case workspaceFilesDirectory:
		flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	case workspaceFilesReadOnly:
		flags = unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	case workspaceFilesInspect:
	default:
		return nil, workspaceFilesError("unsupported")
	}
	fd, err := unix.Openat2(int(h.file.Fd()), name, &unix.OpenHow{
		Flags:   uint64(flags),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, linuxWorkspaceFilesOpenChildError(ctx, h, name, err, request.mode)
	}
	if err := ctx.Err(); err != nil {
		_ = unix.Close(fd)
		return nil, workspaceFilesContextError(err)
	}
	return &linuxWorkspaceFilesHandle{file: os.NewFile(uintptr(fd), "workspace-entry")}, nil
}

func linuxWorkspaceFilesOpenChildError(ctx context.Context, parent *linuxWorkspaceFilesHandle, name string, err error, mode workspaceFilesOpenMode) error {
	if mode != workspaceFilesDirectory || !errors.Is(err, unix.ENOTDIR) {
		return linuxWorkspaceFilesOpenError(err)
	}

	inspect, inspectErr := parent.OpenChild(ctx, name, workspaceFilesOpenRequestFor(workspaceFilesInspect))
	if ctxErr := ctx.Err(); ctxErr != nil {
		return workspaceFilesContextError(ctxErr)
	}
	if inspectErr != nil {
		if isWorkspaceFilesLinkError(inspectErr) {
			return inspectErr
		}
		return workspaceFilesError("not_directory")
	}
	defer inspect.Close()

	inspectHandle, ok := inspect.(*linuxWorkspaceFilesHandle)
	if !ok {
		return workspaceFilesError("not_directory")
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(int(inspectHandle.file.Fd()), "", &stat, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return workspaceFilesError("not_directory")
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFLNK {
		return workspaceFilesError("symlink_denied")
	}
	return workspaceFilesError("not_directory")
}

func linuxWorkspaceFilesOpenError(err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EXDEV) {
		return workspaceFilesError("symlink_denied")
	}
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.E2BIG) || errors.Is(err, unix.EINVAL) {
		return workspaceFilesError("unsupported")
	}
	if errors.Is(err, unix.ENOTDIR) {
		return workspaceFilesError("not_directory")
	}
	if errors.Is(err, unix.ENOENT) {
		return workspaceFilesError("invalid_path")
	}
	return workspaceFilesError("unavailable")
}
