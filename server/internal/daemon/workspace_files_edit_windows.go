package daemon

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/multica-ai/multica/server/pkg/protocol"
	"golang.org/x/sys/windows"
)

var workspaceFilesEditCapabilityEnabled = workspaceFilesCapabilityEnabled

// The four channel workers bound waiting operations even when aliases refer
// to the same parent. A single process-wide lock also serializes across
// reconnects and runtime profiles without a directory-lock registry.
var workspaceFilesSaveLock = make(chan struct{}, 1)

func performWorkspaceFilesEdit(ctx context.Context, req protocol.WorkspaceFilesEditPayload, key *workspaceFilesCursorSigner, save bool) (json.RawMessage, error) {
	return editWindowsWorkspaceFile(ctx, req, key, save, nil)
}

func editWindowsWorkspaceFile(ctx context.Context, req protocol.WorkspaceFilesEditPayload, key *workspaceFilesCursorSigner, save bool, beforeRename func()) (json.RawMessage, error) {
	if req.BindingGeneration <= 0 || !protocol.WorkspaceFilesUUID(req.Policy.VolumeGUID) {
		return nil, workspaceFilesError(protocol.WorkspaceFilesErrorReadOnly)
	}
	if save {
		if !protocol.WorkspaceFilesVersionValid(req.Version) {
			return nil, workspaceFilesError(protocol.WorkspaceFilesErrorConflict)
		}
		if code := protocol.WorkspaceFilesEditTextCode(req.Data); code != "" {
			return nil, workspaceFilesError(code)
		}
		if bytes.ContainsRune(req.Data, '\r') || bytes.HasPrefix(req.Data, workspaceFilesBOM) {
			return nil, workspaceFilesError("invalid_utf8")
		}
		select {
		case workspaceFilesSaveLock <- struct{}{}:
			defer func() { <-workspaceFilesSaveLock }()
		case <-ctx.Done():
			return nil, workspaceFilesContextError(ctx.Err())
		}
	}

	chain, leaf, err := pinWorkspaceFilesEditParent(ctx, req)
	if err != nil {
		return nil, err
	}
	defer chain.close()
	parent := chain[len(chain)-1]
	parentID, err := windowsEditIdentity(parent, true)
	if err != nil {
		return nil, err
	}
	original, err := readWindowsWorkspaceEdit(parent, leaf, parentID)
	if err != nil {
		return nil, workspaceFilesEditSourceError(err, save)
	}
	version := workspaceFilesEditVersion(req, key, original.state)
	if !save {
		original.close()
		return workspaceFilesEditResult(req, version, original.text), nil
	}
	if !hmac.Equal([]byte(req.Version), []byte(version)) {
		original.close()
		return nil, workspaceFilesError(protocol.WorkspaceFilesErrorConflict)
	}
	original.close()

	data := req.Data
	if original.crlf {
		data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
	}
	if original.bom {
		data = append(bytes.Clone(workspaceFilesBOM), data...)
	}
	if code := protocol.WorkspaceFilesEditTextCode(data); code != "" {
		return nil, workspaceFilesError(code)
	}
	stage, stagedID, err := stageWindowsWorkspaceEdit(parent, data, original.security)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			deleteWorkspaceFilesStage(stage)
		}
	}()

	current, err := readWindowsWorkspaceEdit(parent, leaf, parentID)
	if err != nil {
		return nil, workspaceFilesEditSourceError(err, true)
	}
	matches := current.state == original.state && current.security == original.security
	current.close()
	if !matches {
		return nil, workspaceFilesError(protocol.WorkspaceFilesErrorConflict)
	}
	if beforeRename != nil {
		beforeRename()
	}
	if err := ctx.Err(); err != nil {
		return nil, workspaceFilesContextError(err)
	}
	// Ordinary, same-parent NTFS name replacement. The last checked handle
	// must be closed. A concurrent local change to this name can be lost.
	if err := renameWorkspaceFilesStage(stage, leaf); err != nil {
		return nil, err
	}
	committed = true
	if err := windows.CloseHandle(stage); err != nil {
		return nil, workspaceFilesError(protocol.WorkspaceFilesErrorSaveUnconfirmed)
	}

	confirmed, err := readWindowsWorkspaceEdit(parent, leaf, parentID)
	if err != nil {
		return nil, workspaceFilesError(protocol.WorkspaceFilesErrorSaveUnconfirmed)
	}
	defer confirmed.close()
	if confirmed.state.FileID != stagedID || confirmed.state.Digest != sha256.Sum256(data) || confirmed.security != original.security {
		return nil, workspaceFilesError(protocol.WorkspaceFilesErrorSaveUnconfirmed)
	}
	return workspaceFilesEditResult(req, workspaceFilesEditVersion(req, key, confirmed.state), confirmed.text), nil
}

type workspaceFilesEditChain []windows.Handle

func (c workspaceFilesEditChain) close() {
	for i := len(c) - 1; i >= 0; i-- {
		_ = windows.CloseHandle(c[i])
	}
}

func pinWorkspaceFilesEditParent(ctx context.Context, req protocol.WorkspaceFilesEditPayload) (workspaceFilesEditChain, string, error) {
	badPath := workspaceFilesError("invalid_path")
	root := filepath.ToSlash(req.RootPath)
	drive, tail, ok := strings.Cut(root, "/")
	if !ok || len(drive) != 2 || drive[1] != ':' || !filepath.IsAbs(req.RootPath) {
		return nil, "", badPath
	}
	rootParts := strings.Split(strings.TrimSuffix(tail, "/"), "/")
	fileParts := strings.Split(req.Path, "/")
	approvedDepth := len(rootParts)
	if req.Policy.Directory != "." {
		if !strings.HasPrefix(req.Path, req.Policy.Directory+"/") {
			return nil, "", workspaceFilesError(protocol.WorkspaceFilesErrorReadOnly)
		}
		approvedDepth += strings.Count(req.Policy.Directory, "/") + 1
	}
	parts := append(rootParts, fileParts...)
	for _, part := range parts {
		if !protocol.WorkspaceFilesEditName(part) {
			return nil, "", badPath
		}
	}

	driveRoot := windows.StringToUTF16Ptr(drive + "\\")
	if windows.GetDriveType(driveRoot) != 3 { // DRIVE_FIXED
		return nil, "", workspaceFilesError(protocol.WorkspaceFilesErrorReadOnly)
	}
	handle, err := windows.CreateFile(driveRoot, windows.FILE_TRAVERSE|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, "", windowsWorkspaceFilesEditError(err)
	}
	chain := workspaceFilesEditChain{handle}
	ok = false
	defer func() {
		if !ok {
			chain.close()
		}
	}()

	var fs [32]uint16
	err = windows.GetVolumeInformationByHandle(handle, nil, 0, nil, nil, nil, &fs[0], uint32(len(fs)))
	volume := "\\\\?\\Volume{" + req.Policy.VolumeGUID + "}\\"
	if err != nil || windows.UTF16ToString(fs[:]) != "NTFS" {
		return nil, "", workspaceFilesError(protocol.WorkspaceFilesErrorReadOnly)
	}
	name, err := windowsEditFinalName(handle)
	if err != nil || !strings.EqualFold(name, volume) {
		return nil, "", workspaceFilesError(protocol.WorkspaceFilesErrorReadOnly)
	}
	if _, err := windowsEditIdentity(handle, true); err != nil {
		return nil, "", err
	}
	for i, part := range parts[:len(parts)-1] {
		if err := ctx.Err(); err != nil {
			return nil, "", workspaceFilesContextError(err)
		}
		h, err := openWindowsWorkspaceEditChild(chain[len(chain)-1], part, windows.FILE_TRAVERSE|windows.FILE_READ_ATTRIBUTES,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE)
		if err != nil {
			return nil, "", err
		}
		chain = append(chain, h)
		id, err := windowsEditIdentity(h, true)
		if err != nil {
			return nil, "", err
		}
		if i == 0 {
			name, err := windowsEditFinalName(h)
			if err != nil || len(name) <= len(volume) || !strings.EqualFold(name[:len(volume)], volume) || protectedWorkspaceFilesDirectory(name[len(volume):]) {
				return nil, "", workspaceFilesError(protocol.WorkspaceFilesErrorReadOnly)
			}
		}
		if i+1 == approvedDepth && !strings.EqualFold(id, req.Policy.DirectoryID) {
			return nil, "", workspaceFilesError(protocol.WorkspaceFilesErrorReadOnly)
		}
	}
	ok = true
	return chain, parts[len(parts)-1], nil
}

func protectedWorkspaceFilesDirectory(name string) bool {
	switch strings.ToLower(name) {
	case "windows", "program files", "program files (x86)", "programdata", "system volume information",
		"$recycle.bin", "recovery", "boot", "perflogs", "windows.old":
		return true
	default:
		return false
	}
}

func windowsEditFinalName(handle windows.Handle) (string, error) {
	var name [windows.MAX_LONG_PATH]uint16
	count, err := windows.GetFinalPathNameByHandle(handle, &name[0], uint32(len(name)), 1) // VOLUME_NAME_GUID
	if err != nil || count == 0 || count >= uint32(len(name)) {
		return "", workspaceFilesError(protocol.WorkspaceFilesErrorReadOnly)
	}
	return windows.UTF16ToString(name[:count]), nil
}

func windowsEditIdentity(handle windows.Handle, directory bool) (string, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return "", windowsWorkspaceFilesEditError(err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return "", workspaceFilesError("symlink_denied")
	}
	if directory {
		if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
			return "", workspaceFilesError("not_directory")
		}
	} else if info.NumberOfLinks != 1 || info.FileAttributes & ^uint32(windows.FILE_ATTRIBUTE_ARCHIVE|windows.FILE_ATTRIBUTE_NORMAL) != 0 {
		return "", workspaceFilesError(protocol.WorkspaceFilesErrorReadOnly)
	}
	return fmt.Sprintf("%08x%08x", info.FileIndexHigh, info.FileIndexLow), nil
}

func openWindowsWorkspaceEditChild(parent windows.Handle, name string, access, share, disposition, options uint32) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, workspaceFilesError("invalid_path")
	}
	object := windows.OBJECT_ATTRIBUTES{RootDirectory: parent, ObjectName: objectName, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE}
	object.Length = uint32(unsafe.Sizeof(object))
	var status windows.IO_STATUS_BLOCK
	var child windows.Handle
	err = windows.NtCreateFile(&child, access|windows.SYNCHRONIZE, &object, &status, nil, 0, share, disposition,
		options|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return 0, windowsWorkspaceFilesEditError(err)
	}
	return child, nil
}

func windowsWorkspaceFilesEditError(err error) error {
	if status, ok := err.(windows.NTStatus); ok {
		if status == windows.STATUS_REPARSE_POINT_ENCOUNTERED || status == windows.STATUS_STOPPED_ON_SYMLINK {
			return workspaceFilesError("symlink_denied")
		}
		err = status.Errno()
	}
	switch {
	case errors.Is(err, windows.ERROR_SHARING_VIOLATION), errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return workspaceFilesError("busy")
	case errors.Is(err, windows.ERROR_FILE_NOT_FOUND), errors.Is(err, windows.ERROR_PATH_NOT_FOUND), errors.Is(err, windows.ERROR_DELETE_PENDING):
		return workspaceFilesError("invalid_path")
	default:
		return windowsWorkspaceFilesOpenError(err)
	}
}

func workspaceFilesEditSourceError(err error, save bool) error {
	if save && workspaceFilesErrorCode(err, "") == "invalid_path" {
		return workspaceFilesError(protocol.WorkspaceFilesErrorConflict)
	}
	return err
}

var workspaceFilesBOM = []byte{0xef, 0xbb, 0xbf}

type windowsWorkspaceEditSource struct {
	file      *os.File
	state     workspaceFilesEditState
	security  string
	text      []byte
	crlf, bom bool
}

func (s windowsWorkspaceEditSource) close() { _ = s.file.Close() }

func readWindowsWorkspaceEdit(parent windows.Handle, leaf, parentID string) (windowsWorkspaceEditSource, error) {
	var source windowsWorkspaceEditSource
	handle, err := openWindowsWorkspaceEditChild(parent, leaf,
		windows.FILE_READ_DATA|windows.FILE_WRITE_DATA|windows.FILE_READ_ATTRIBUTES|windows.FILE_READ_EA|windows.READ_CONTROL,
		windows.FILE_SHARE_READ, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE)
	if err != nil {
		return source, err
	}
	file := os.NewFile(uintptr(handle), leaf)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return source, workspaceFilesError(protocol.WorkspaceFilesErrorReadOnly)
	}
	verified := false
	defer func() {
		if !verified {
			_ = file.Close()
		}
	}()

	id, err := windowsEditIdentity(handle, false)
	if err != nil {
		return source, err
	}
	name, err := windowsEditFinalName(handle)
	if err != nil || filepath.Base(name) != leaf {
		return source, workspaceFilesError(protocol.WorkspaceFilesErrorReadOnly)
	}
	if !workspaceFilesDefaultStreamOnly(handle) {
		return source, workspaceFilesError(protocol.WorkspaceFilesErrorReadOnly)
	}
	if source.security, err = workspaceFilesEditACL(handle); err != nil {
		return source, err
	}
	data, err := io.ReadAll(io.LimitReader(file, protocol.WorkspaceFilesEditMaxBytes+1))
	if err != nil {
		return source, windowsWorkspaceFilesEditError(err)
	}
	if code := protocol.WorkspaceFilesEditTextCode(data); code != "" {
		return source, workspaceFilesError(code)
	}
	source.state = workspaceFilesEditState{ParentID: parentID, FileID: id, Digest: sha256.Sum256(data)}
	source.bom = bytes.HasPrefix(data, workspaceFilesBOM)
	text := bytes.TrimPrefix(data, workspaceFilesBOM)
	source.crlf = bytes.ContainsRune(text, '\r')
	if source.crlf {
		normalized := bytes.ReplaceAll(text, []byte("\r\n"), []byte("\n"))
		if bytes.ContainsRune(normalized, '\r') || bytes.Count(text, []byte("\r\n")) != bytes.Count(normalized, []byte("\n")) {
			return source, workspaceFilesError(protocol.WorkspaceFilesErrorReadOnly)
		}
		text = normalized
	}
	source.text, source.file = text, file
	verified = true
	return source, nil
}

func workspaceFilesDefaultStreamOnly(handle windows.Handle) bool {
	// FILE_STREAM_INFO is variable-length with its UTF-16 name at byte 24.
	// The unnamed $DATA stream must be the only record. Anything larger or
	// unavailable (including metadata we cannot safely preserve) fails closed.
	var streamBuffer [8]uint64
	streams := unsafe.Slice((*byte)(unsafe.Pointer(&streamBuffer[0])), int(unsafe.Sizeof(streamBuffer)))
	err := windows.GetFileInformationByHandleEx(handle, windows.FileStreamInfo, &streams[0], uint32(len(streams)))
	if err != nil || binary.LittleEndian.Uint32(streams[0:4]) != 0 {
		return false
	}
	nameSize := binary.LittleEndian.Uint32(streams[4:8])
	if nameSize != uint32(2*len("::$DATA")) {
		return false
	}
	var name [len("::$DATA")]uint16
	for i := range name {
		name[i] = binary.LittleEndian.Uint16(streams[24+i*2:])
	}
	var status windows.IO_STATUS_BLOCK
	var eaSize uint32
	err = windows.NtQueryInformationFile(handle, &status, (*byte)(unsafe.Pointer(&eaSize)), uint32(unsafe.Sizeof(eaSize)), windows.FileEaInformation)
	return windows.UTF16ToString(name[:]) == "::$DATA" && err == nil && eaSize == 0
}

func workspaceFilesEditACL(handle windows.Handle) (string, error) {
	const access = windows.OWNER_SECURITY_INFORMATION | windows.GROUP_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION | windows.LABEL_SECURITY_INFORMATION
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, access)
	if err != nil {
		return "", workspaceFilesError(protocol.WorkspaceFilesErrorReadOnly)
	}
	sddl := descriptor.String()
	if sddl == "" {
		return "", workspaceFilesError(protocol.WorkspaceFilesErrorReadOnly)
	}
	return sddl, nil
}

func stageWindowsWorkspaceEdit(parent windows.Handle, data []byte, security string) (windows.Handle, string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return 0, "", workspaceFilesError("unavailable")
	}
	name := ".multica-save-" + hex.EncodeToString(random[:])
	// A new unpredictable name and exclusive handle keep the contents
	// private while staging. Only replacement with identical inherited
	// owner, group, DACL and mandatory label is supported.
	stage, err := openWindowsWorkspaceEditChild(parent, name, windows.DELETE|windows.FILE_WRITE_DATA|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		0, windows.FILE_CREATE, windows.FILE_NON_DIRECTORY_FILE)
	if err != nil {
		return 0, "", err
	}
	ok := false
	defer func() {
		if !ok {
			deleteWorkspaceFilesStage(stage)
		}
	}()
	stagedSecurity, err := workspaceFilesEditACL(stage)
	if err != nil || stagedSecurity != security {
		return 0, "", workspaceFilesError(protocol.WorkspaceFilesErrorReadOnly)
	}
	if len(data) != 0 {
		var written uint32
		if err := windows.WriteFile(stage, data, &written, nil); err != nil || int(written) != len(data) {
			return 0, "", workspaceFilesError("unavailable")
		}
	}
	if err := windows.FlushFileBuffers(stage); err != nil {
		return 0, "", windowsWorkspaceFilesEditError(err)
	}
	id, err := windowsEditIdentity(stage, false)
	if err != nil {
		return 0, "", err
	}
	ok = true
	return stage, id, nil
}

func deleteWorkspaceFilesStage(stage windows.Handle) {
	// Delete the exact still-open staging object by handle. Never resolve its
	// temporary name again during cleanup.
	remove := byte(1)
	var status windows.IO_STATUS_BLOCK
	_ = windows.NtSetInformationFile(stage, &status, &remove, 1, windows.FileDispositionInformation)
	_ = windows.CloseHandle(stage)
}

func renameWorkspaceFilesStage(stage windows.Handle, leaf string) error {
	if !protocol.WorkspaceFilesEditName(leaf) {
		return workspaceFilesError("invalid_path")
	}
	name, err := windows.UTF16FromString(leaf)
	if err != nil {
		return workspaceFilesError("invalid_path")
	}
	// With a NULL RootDirectory and one segment, the destination is relative
	// to the staging file's existing parent. The kernel replaces the final
	// directory entry without opening a path through that entry.
	type fileRenameInformation struct {
		ReplaceIfExists uint32
		RootDirectory   windows.Handle
		FileNameLength  uint32
		FileName        uint16
	}
	var header fileRenameInformation
	name = name[:len(name)-1]
	size := int(unsafe.Offsetof(header.FileName)) + len(name)*2
	buffer := make([]byte, max(size, int(unsafe.Sizeof(header))))
	info := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.ReplaceIfExists = windows.FILE_RENAME_REPLACE_IF_EXISTS
	info.FileNameLength = uint32(len(name) * 2)
	copy(unsafe.Slice(&info.FileName, len(name)), name)
	var status windows.IO_STATUS_BLOCK
	if err := windows.NtSetInformationFile(stage, &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation); err != nil {
		return windowsWorkspaceFilesEditError(err)
	}
	return nil
}
