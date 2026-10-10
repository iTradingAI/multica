package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestWorkspaceFilesValidateWirePath(t *testing.T) {
	if got, err := validateWorkspaceFilesPath(".", true); err != nil || len(got) != 0 {
		t.Fatalf("list root: parts=%v err=%v", got, err)
	}
	if _, err := validateWorkspaceFilesPath(".", false); err == nil {
		t.Fatal("read accepted the list-only root path")
	}
	for _, path := range []string{
		"", "/etc/passwd", "../secret", "a/../secret", "./secret", "a//b", `a\b`,
		"C:relative", "//server/share", `\\?\C:\device`, "file:stream", "NUL.txt", "NUL .txt", "a\x00b",
		strings.Repeat("x", workspaceFilesMaxPathBytes+1),
	} {
		t.Run(path, func(t *testing.T) {
			if _, err := validateWorkspaceFilesPath(path, true); err == nil {
				t.Fatalf("accepted unsafe wire path %q", path)
			}
		})
	}
	if _, err := validateWorkspaceFilesPath(strings.Repeat("x", workspaceFilesMaxPathBytes), false); err != nil {
		t.Fatalf("accepted path at the byte limit: %v", err)
	}
}

func TestWorkspaceFilesCursorBindsResourcePathAndLastCheckedName(t *testing.T) {
	signer, err := newWorkspaceFilesCursorSigner()
	if err != nil {
		t.Fatal(err)
	}
	const resourceID = "resource-1"
	const path = "src"
	const last = "name-200"
	token, err := signer.encode(resourceID, path, last)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) > workspaceFilesMaxCursorBytes {
		t.Fatalf("encoded cursor length %d exceeds %d", len(token), workspaceFilesMaxCursorBytes)
	}
	if got, err := signer.decode(token, resourceID, path); err != nil || got != last {
		t.Fatalf("decode = %q, %v; want %q", got, err, last)
	}
	if _, err := signer.decode(token, "resource-2", path); err == nil {
		t.Fatal("cursor was accepted for a different resource")
	}
	if _, err := signer.decode(token, resourceID, "other"); err == nil {
		t.Fatal("cursor was accepted for a different path")
	}
	tampered := map[bool]string{true: "B", false: "A"}[token[0] == 'A'] + token[1:]
	if _, err := signer.decode(tampered, resourceID, path); err == nil {
		t.Fatal("tampered cursor was accepted")
	}
	if _, err := signer.decode(strings.Repeat("A", workspaceFilesMaxCursorBytes+1), resourceID, path); err == nil {
		t.Fatal("overlong cursor was accepted")
	}
}

func TestWorkspaceFilesReadFramesStayBoundedAndPreserveUTF8(t *testing.T) {
	data := []byte(strings.Repeat("a", workspaceFilesMaxReadBytes-3) + "界")
	if len(data) != workspaceFilesMaxReadBytes {
		t.Fatalf("fixture length = %d", len(data))
	}
	frames, err := buildWorkspaceFilesReadFrames("daemon-req", "runtime", "resource", data, protocol.WorkspaceFilesGeneration{})
	if err != nil {
		t.Fatal(err)
	}
	var combined []byte
	for i, frame := range frames {
		if len(frame) > workspaceFilesMaxFrameBytes {
			t.Fatalf("frame %d is %d bytes", i, len(frame))
		}
		var message protocol.Message
		if err := json.Unmarshal(frame, &message); err != nil {
			t.Fatal(err)
		}
		if message.Type != protocol.EventWorkspaceFilesReadChunk {
			t.Fatalf("frame type = %q", message.Type)
		}
		var payload protocol.WorkspaceFilesReadChunkPayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Seq != i || payload.EOF != (i == len(frames)-1) {
			t.Fatalf("frame %d sequence/final = %d/%v", i, payload.Seq, payload.EOF)
		}
		if !utf8.Valid(payload.Data) {
			t.Fatalf("frame %d split a UTF-8 code point", i)
		}
		combined = append(combined, payload.Data...)
	}
	if !bytes.Equal(combined, data) {
		t.Fatal("chunk reconstruction differs from the validated input")
	}
	if _, err := buildWorkspaceFilesReadFrames("id", "r", "x", []byte{0xff}, protocol.WorkspaceFilesGeneration{}); err == nil {
		t.Fatal("read builder accepted invalid UTF-8")
	}
	empty, err := buildWorkspaceFilesReadFrames("id", "r", "x", nil, protocol.WorkspaceFilesGeneration{})
	if err != nil || len(empty) != 1 {
		t.Fatalf("empty file frames=%d err=%v", len(empty), err)
	}
	var emptyMessage protocol.Message
	_ = json.Unmarshal(empty[0], &emptyMessage)
	var emptyPayload protocol.WorkspaceFilesReadChunkPayload
	_ = json.Unmarshal(emptyMessage.Payload, &emptyPayload)
	if !emptyPayload.EOF || len(emptyPayload.Data) != 0 {
		t.Fatalf("empty file payload = %+v", emptyPayload)
	}
}

func TestWorkspaceFilesListFramesStayBoundedAndMarkOnlyFinalFrame(t *testing.T) {
	entries := make([]protocol.WorkspaceFilesEntry, workspaceFilesMaxPage)
	for i := range entries {
		entries[i] = protocol.WorkspaceFilesEntry{Name: strings.Repeat("a", 230) + string(rune('a'+i%26)), Type: "regular"}
	}
	frames, err := buildWorkspaceFilesListFrames("request", "runtime", "resource", entries, "opaque-cursor", 7, protocol.WorkspaceFilesGeneration{})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) < 2 {
		t.Fatalf("long list did not split into multiple frames: %d", len(frames))
	}
	count := 0
	for i, frame := range frames {
		if len(frame) > workspaceFilesMaxFrameBytes {
			t.Fatalf("frame %d is %d bytes", i, len(frame))
		}
		var message protocol.Message
		if err := json.Unmarshal(frame, &message); err != nil {
			t.Fatal(err)
		}
		var payload protocol.WorkspaceFilesListResultPayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Seq != i || payload.Final != (i == len(frames)-1) {
			t.Fatalf("frame %d sequence/final = %d/%v", i, payload.Seq, payload.Final)
		}
		if i != len(frames)-1 && (payload.NextCursor != "" || payload.Skipped != 0) {
			t.Fatalf("non-final frame %d carries terminal metadata: %+v", i, payload)
		}
		if payload.Final && (payload.NextCursor != "opaque-cursor" || payload.Skipped != 7) {
			t.Fatalf("final metadata = %+v", payload)
		}
		count += len(payload.Entries)
	}
	if count != len(entries) {
		t.Fatalf("framed %d entries, want %d", count, len(entries))
	}
	if frame := buildWorkspaceFilesErrorFrame("req", "runtime", "resource", "C:\\secret\\path", protocol.WorkspaceFilesGeneration{}); frame == nil {
		t.Fatal("failed to build fixed error frame")
	} else {
		if len(frame) > workspaceFilesMaxFrameBytes || bytes.Contains(frame, []byte("C:\\secret")) {
			t.Fatalf("error frame is oversized or includes a path: %q", frame)
		}
	}
}

func TestWorkspaceFilesListPaginationAndSkippedProgress(t *testing.T) {
	root := fakeWorkspaceFilesDirectory()
	for i := 0; i < workspaceFilesMaxPage; i++ {
		name := fmtWorkspaceFilesIndex(i)
		root.add(name, fakeWorkspaceFilesRegular([]byte(name)))
	}
	root.add(fmtWorkspaceFilesIndex(workspaceFilesMaxPage), fakeWorkspaceFilesRegular([]byte("last")))
	ch, out := newWorkspaceFilesTestChannel(root)
	first := runWorkspaceFilesList(t, ch, out, "request-1", protocol.WorkspaceFilesListPayload{
		RuntimeID: "runtime", ResourceID: "resource", RootPath: `C:\virtual`, Path: ".", PageSize: workspaceFilesMaxPage,
	})
	if first.errCode != "" || len(first.entries) != workspaceFilesMaxPage || first.nextCursor == "" {
		t.Fatalf("first page error=%q entries=%d cursor=%q", first.errCode, len(first.entries), first.nextCursor)
	}
	second := runWorkspaceFilesList(t, ch, out, "request-2", protocol.WorkspaceFilesListPayload{
		RuntimeID: "runtime", ResourceID: "resource", RootPath: `C:\virtual`, Path: ".", PageSize: workspaceFilesMaxPage, Cursor: first.nextCursor,
	})
	if second.errCode != "" || len(second.entries) != 1 || second.entries[0].Name != fmtWorkspaceFilesIndex(workspaceFilesMaxPage) || second.nextCursor != "" {
		t.Fatalf("second page = %+v", second)
	}

	skippedRoot := fakeWorkspaceFilesDirectory()
	for i := 0; i < workspaceFilesMaxPage; i++ {
		skippedRoot.add(fmtWorkspaceFilesIndex(i), fakeWorkspaceFilesLink())
	}
	skippedRoot.add(fmtWorkspaceFilesIndex(workspaceFilesMaxPage), fakeWorkspaceFilesRegular([]byte("visible")))
	skippedChannel, skippedOut := newWorkspaceFilesTestChannel(skippedRoot)
	page1 := runWorkspaceFilesList(t, skippedChannel, skippedOut, "skip-1", protocol.WorkspaceFilesListPayload{
		RuntimeID: "runtime", ResourceID: "resource", RootPath: `C:\virtual`, Path: ".", PageSize: workspaceFilesMaxPage,
	})
	if page1.errCode != "" || len(page1.entries) != 0 || page1.skipped != workspaceFilesMaxPage || page1.nextCursor == "" {
		t.Fatalf("all-skipped page did not advance: %+v", page1)
	}
	page2 := runWorkspaceFilesList(t, skippedChannel, skippedOut, "skip-2", protocol.WorkspaceFilesListPayload{
		RuntimeID: "runtime", ResourceID: "resource", RootPath: `C:\virtual`, Path: ".", PageSize: workspaceFilesMaxPage, Cursor: page1.nextCursor,
	})
	if page2.errCode != "" || len(page2.entries) != 1 || page2.entries[0].Name != fmtWorkspaceFilesIndex(workspaceFilesMaxPage) {
		t.Fatalf("regular entry after skipped page missing: %+v", page2)
	}
}

func TestWorkspaceFilesReadLimitUTF8AndNoEarlyData(t *testing.T) {
	root := fakeWorkspaceFilesDirectory()
	root.add("exact.txt", fakeWorkspaceFilesRegular([]byte(strings.Repeat("x", workspaceFilesMaxReadBytes))))
	root.add("oversize.txt", fakeWorkspaceFilesRegular([]byte(strings.Repeat("x", workspaceFilesMaxReadBytes+1))))
	root.add("invalid.txt", fakeWorkspaceFilesRegular([]byte{0xff}))
	root.children["oversize.txt"].reportedSize = 0 // force the max+1 reader guard to decide.
	ch, out := newWorkspaceFilesTestChannel(root)

	exact := runWorkspaceFilesRead(t, ch, out, "read-exact", "exact.txt")
	if exact.errCode != "" || len(exact.data) != workspaceFilesMaxReadBytes {
		t.Fatalf("exact limit read error=%q bytes=%d", exact.errCode, len(exact.data))
	}
	for _, path := range []string{"oversize.txt", "invalid.txt"} {
		result := runWorkspaceFilesRead(t, ch, out, "read-"+path, path)
		wantCode := protocol.WorkspaceFilesErrorTooLarge
		if path == "invalid.txt" {
			wantCode = protocol.WorkspaceFilesErrorInvalidUTF8
		}
		if result.errCode != wantCode || len(result.data) != 0 || result.dataFrames != 0 {
			t.Fatalf("%s result = %+v, want %s and zero data frames", path, result, wantCode)
		}
	}
}

func TestWorkspaceFilesOnlyListsDirectoriesAndRegularFiles(t *testing.T) {
	root := fakeWorkspaceFilesDirectory()
	root.add("folder", fakeWorkspaceFilesDirectory())
	root.add("plain.txt", fakeWorkspaceFilesRegular([]byte("text")))
	special := fakeWorkspaceFilesRegular(nil)
	special.mode = fs.ModeNamedPipe | 0o600
	root.add("pipe", special)
	ch, out := newWorkspaceFilesTestChannel(root)
	page := runWorkspaceFilesList(t, ch, out, "types", protocol.WorkspaceFilesListPayload{
		RuntimeID: "runtime", ResourceID: "resource", RootPath: `C:\virtual`, Path: ".",
	})
	if page.errCode != "" || len(page.entries) != 2 || page.skipped != 1 {
		t.Fatalf("list type filter = %+v", page)
	}
	if page.entries[0].Name != "folder" || page.entries[0].Type != "directory" || page.entries[1].Name != "plain.txt" || page.entries[1].Type != "regular" {
		t.Fatalf("entry types/order = %+v", page.entries)
	}
	read := runWorkspaceFilesRead(t, ch, out, "directory-read", "folder")
	if read.errCode != protocol.WorkspaceFilesErrorNotRegular || read.dataFrames != 0 {
		t.Fatalf("directory read = %+v", read)
	}
}

func TestWorkspaceFilesRequestsNonblockingNoFollowLeafOpen(t *testing.T) {
	root := fakeWorkspaceFilesDirectory()
	nested := fakeWorkspaceFilesDirectory()
	nested.add("leaf.txt", fakeWorkspaceFilesRegular([]byte("contents")))
	root.add("nested", nested)
	ch, out := newWorkspaceFilesTestChannel(root)
	result := runWorkspaceFilesRead(t, ch, out, "flags", "nested/leaf.txt")
	if result.errCode != "" || string(result.data) != "contents" {
		t.Fatalf("flag assertion read result = %+v", result)
	}
	root.trace.mu.Lock()
	requests := append([]fakeWorkspaceFilesOpenRecord(nil), root.trace.requests...)
	root.trace.mu.Unlock()
	for _, record := range requests {
		if record.name != "leaf.txt" || record.request.mode != workspaceFilesReadOnly {
			continue
		}
		if !record.request.nonBlocking || !record.request.closeOnExec || !record.request.noFollow {
			t.Fatalf("final leaf open request omitted a required flag: %+v", record.request)
		}
		return
	}
	t.Fatal("fake opener did not observe the final leaf open request")
}

func TestWorkspaceFilesTimeoutCancelDuplicateAndConcurrency(t *testing.T) {
	newBlockingChannel := func() (*workspaceFilesChannel, <-chan []byte, *fakeWorkspaceFilesHandle) {
		root := fakeWorkspaceFilesDirectory()
		blocking := fakeWorkspaceFilesRegular(nil)
		blocking.blockRead = true
		root.add("blocked.txt", blocking)
		ch, out := newWorkspaceFilesTestChannel(root)
		return ch, out, blocking
	}

	ch, out, blocking := newBlockingChannel()

	deadline := time.Now().Add(100 * time.Millisecond).UnixMilli()
	ch.start("timeout-id", "runtime", "resource", deadline, func(p *workspaceFilesPending) {
		ch.runRead(p, protocol.WorkspaceFilesReadPayload{DaemonReqID: p.id, RuntimeID: p.runtimeID, ResourceID: p.resourceID, RootPath: `C:\virtual`, Path: "blocked.txt"})
	}, protocol.WorkspaceFilesGeneration{})
	if _, err := waitFakeSignal(blocking.readStarted, time.Second); err != nil {
		t.Fatal(err)
	}
	timeoutFrame := waitWorkspaceFilesFrame(t, out)
	if got := parseWorkspaceFilesError(t, timeoutFrame); got != protocol.WorkspaceFilesErrorTimeout {
		t.Fatalf("timeout error code = %q", got)
	}
	if _, err := waitFakeSignal(blocking.closedSignal, time.Second); err != nil {
		t.Fatalf("timeout did not close active file: %v", err)
	}

	cancelCh, cancelOut, cancelBlocking := newBlockingChannel()
	cancelCh.start("cancel-id", "runtime", "resource", 0, func(p *workspaceFilesPending) {
		cancelCh.runRead(p, protocol.WorkspaceFilesReadPayload{DaemonReqID: p.id, RuntimeID: p.runtimeID, ResourceID: p.resourceID, RootPath: `C:\virtual`, Path: "blocked.txt"})
	}, protocol.WorkspaceFilesGeneration{})
	if _, err := waitFakeSignal(cancelBlocking.readStarted, time.Second); err != nil {
		t.Fatal(err)
	}
	cancelCh.cancelPending("cancel-id")
	if _, err := waitFakeSignal(cancelBlocking.closedSignal, time.Second); err != nil {
		t.Fatalf("cancel did not close active file: %v", err)
	}
	cancelCh.mu.Lock()
	pendingAfterCancel := len(cancelCh.pending)
	cancelCh.mu.Unlock()
	if pendingAfterCancel != 0 {
		t.Fatalf("pending after cancel = %d", pendingAfterCancel)
	}
	wrongRuntimeRoot := fakeWorkspaceFilesDirectory()
	wrongRuntimeBlocking := fakeWorkspaceFilesRegular(nil)
	wrongRuntimeBlocking.blockRead = true
	wrongRuntimeRoot.add("blocked.txt", wrongRuntimeBlocking)
	wrongRuntimeCh, wrongRuntimeOut := newWorkspaceFilesTestChannel(wrongRuntimeRoot)
	wrongRuntimeCh.start("runtime-bound-id", "runtime-a", "resource", 0, func(p *workspaceFilesPending) {
		wrongRuntimeCh.runRead(p, protocol.WorkspaceFilesReadPayload{DaemonReqID: p.id, RuntimeID: p.runtimeID, ResourceID: p.resourceID, RootPath: `C:\virtual`, Path: "blocked.txt"})
	}, protocol.WorkspaceFilesGeneration{})
	if _, err := waitFakeSignal(wrongRuntimeBlocking.readStarted, time.Second); err != nil {
		t.Fatal(err)
	}
	wrongRuntimeCh.cancelPendingForRuntime("runtime-bound-id", "runtime-b", protocol.WorkspaceFilesGeneration{})
	wrongRuntimeCh.mu.Lock()
	stillPending := wrongRuntimeCh.pending["runtime-bound-id"] != nil
	wrongRuntimeCh.mu.Unlock()
	if !stillPending {
		t.Fatal("cancel from another runtime canceled the request")
	}
	wrongRuntimeCh.cancelPendingForRuntime("runtime-bound-id", "runtime-a", protocol.WorkspaceFilesGeneration{})
	if _, err := waitFakeSignal(wrongRuntimeBlocking.closedSignal, time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-wrongRuntimeOut:
		t.Fatalf("cancel emitted a terminal response: %s", frame)
	case <-time.After(25 * time.Millisecond):
	}
	select {
	case frame := <-cancelOut:
		t.Fatalf("cancel emitted a terminal response: %s", frame)
	case <-time.After(50 * time.Millisecond):
	}

	// Four blocked operations are admitted; the fifth is rejected immediately,
	// and an identical daemon request ID never replaces the original map entry.
	concurrentCh, concurrentOut, concurrentBlocking := newBlockingChannel()
	for i := 0; i < workspaceFilesMaxInFlight; i++ {
		id := fmtWorkspaceFilesIndex(i)
		concurrentCh.start(id, "runtime", "resource", 0, func(p *workspaceFilesPending) {
			concurrentCh.runRead(p, protocol.WorkspaceFilesReadPayload{DaemonReqID: p.id, RuntimeID: p.runtimeID, ResourceID: p.resourceID, RootPath: `C:\virtual`, Path: "blocked.txt"})
		}, protocol.WorkspaceFilesGeneration{})
	}
	if _, err := waitFakeSignal(concurrentBlocking.readStarted, time.Second); err != nil {
		t.Fatal(err)
	}
	concurrentCh.mu.Lock()
	first := concurrentCh.pending[fmtWorkspaceFilesIndex(0)]
	concurrentCh.mu.Unlock()
	concurrentCh.start(fmtWorkspaceFilesIndex(0), "other-runtime", "other-resource", 0, func(*workspaceFilesPending) {
		t.Fatal("duplicate internal ID started a second worker")
	}, protocol.WorkspaceFilesGeneration{})
	concurrentCh.mu.Lock()
	gotFirst := concurrentCh.pending[fmtWorkspaceFilesIndex(0)]
	concurrentCh.mu.Unlock()
	if gotFirst != first || first.runtimeID != "runtime" {
		t.Fatal("duplicate internal ID replaced the existing pending request")
	}
	concurrentCh.start("overflow", "runtime", "resource", 0, func(*workspaceFilesPending) { t.Fatal("busy request started a worker") }, protocol.WorkspaceFilesGeneration{})
	if got := parseWorkspaceFilesError(t, waitWorkspaceFilesFrame(t, concurrentOut)); got != protocol.WorkspaceFilesErrorBusy {
		t.Fatalf("fifth request error code = %q", got)
	}
	concurrentCh.attach(nil)
	concurrentCh.mu.Lock()
	pendingAfterDetach := len(concurrentCh.pending)
	concurrentCh.mu.Unlock()
	if pendingAfterDetach != 0 {
		t.Fatalf("pending after detach = %d", pendingAfterDetach)
	}
}

func TestWorkspaceFilesTimedOutStuckWorkersRemainBounded(t *testing.T) {
	root := fakeWorkspaceFilesDirectory()
	blocked := make([]*fakeWorkspaceFilesHandle, workspaceFilesMaxInFlight)
	for i := range blocked {
		blocked[i] = fakeWorkspaceFilesRegular(nil)
		blocked[i].blockRead = true
		blocked[i].ignoreClose = true
		blocked[i].releaseRead = make(chan struct{})
		root.add(fmtWorkspaceFilesIndex(i), blocked[i])
	}
	ch, out := newWorkspaceFilesTestChannel(root)
	deadline := time.Now().Add(125 * time.Millisecond).UnixMilli()
	for i := range blocked {
		id := "stuck-" + fmtWorkspaceFilesIndex(i)
		path := fmtWorkspaceFilesIndex(i)
		ch.start(id, "runtime", "resource", deadline, func(p *workspaceFilesPending) {
			ch.runRead(p, protocol.WorkspaceFilesReadPayload{DaemonReqID: p.id, RuntimeID: p.runtimeID, ResourceID: p.resourceID, RootPath: `C:\virtual`, Path: path})
		}, protocol.WorkspaceFilesGeneration{})
	}
	for _, file := range blocked {
		if _, err := waitFakeSignal(file.readStarted, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	for range blocked {
		if got := parseWorkspaceFilesError(t, waitWorkspaceFilesFrame(t, out)); got != protocol.WorkspaceFilesErrorTimeout {
			t.Fatalf("stuck worker timeout code = %q", got)
		}
	}
	ch.mu.Lock()
	activeCount := len(ch.active)
	pendingCount := len(ch.pending)
	ch.mu.Unlock()
	if activeCount != workspaceFilesMaxInFlight || pendingCount != 0 || len(ch.slots) != workspaceFilesMaxInFlight {
		t.Fatalf("after timeouts active=%d pending=%d slots=%d", activeCount, pendingCount, len(ch.slots))
	}
	ch.start("after-timeouts", "runtime", "resource", 0, func(*workspaceFilesPending) {
		t.Fatal("request started while all bounded workers remain stuck")
	}, protocol.WorkspaceFilesGeneration{})
	if got := parseWorkspaceFilesError(t, waitWorkspaceFilesFrame(t, out)); got != protocol.WorkspaceFilesErrorBusy {
		t.Fatalf("request after stuck timeouts error = %q", got)
	}
	for _, file := range blocked {
		close(file.releaseRead)
	}
	deadlineWait := time.Now().Add(time.Second)
	for time.Now().Before(deadlineWait) {
		ch.mu.Lock()
		activeCount = len(ch.active)
		ch.mu.Unlock()
		if activeCount == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	ch.mu.Lock()
	activeCount = len(ch.active)
	ch.mu.Unlock()
	if activeCount != 0 || len(ch.slots) != 0 {
		t.Fatalf("released stuck workers remain active=%d slots=%d", activeCount, len(ch.slots))
	}
}

func TestWorkspaceFilesLocalOperationsDoNotWrite(t *testing.T) {
	if !workspaceFilesCapabilityEnabled {
		t.Skip("safe workspace-files opener is unavailable on this platform")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "note.txt"), []byte("hello 世界"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := workspaceFilesTreeSnapshot(t, root)
	out := make(chan []byte, 256)
	ch := newWorkspaceFilesChannelForTest(newWorkspaceFilesOpener(), func(ctx context.Context, frame []byte) bool {
		select {
		case out <- append([]byte(nil), frame...):
			return true
		case <-ctx.Done():
			return false
		}
	})
	page := runWorkspaceFilesList(t, ch, out, "real-list", protocol.WorkspaceFilesListPayload{
		RuntimeID: "runtime", ResourceID: "resource", RootPath: root, Path: "nested",
	})
	if page.errCode != "" || len(page.entries) != 1 || page.entries[0].Name != "note.txt" || page.entries[0].Type != "regular" {
		t.Fatalf("real list result = %+v", page)
	}
	read := runWorkspaceFilesRead(t, ch, out, "real-read", "nested/note.txt", root)
	if read.errCode != "" || string(read.data) != "hello 世界" {
		t.Fatalf("real read result error=%q data=%q", read.errCode, read.data)
	}
	after := workspaceFilesTreeSnapshot(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("list/read changed the workspace tree: before=%v after=%v", before, after)
	}
}

type workspaceFilesTestResult struct {
	entries    []protocol.WorkspaceFilesEntry
	nextCursor string
	skipped    int
	errCode    string
	data       []byte
	dataFrames int
}

func newWorkspaceFilesTestChannel(root *fakeWorkspaceFilesHandle) (*workspaceFilesChannel, <-chan []byte) {
	out := make(chan []byte, 256)
	var opener workspaceFilesOpener
	if root == nil {
		opener = newWorkspaceFilesOpener()
	} else {
		opener = &fakeWorkspaceFilesOpener{root: root}
	}
	ch := newWorkspaceFilesChannelForTest(opener, func(ctx context.Context, frame []byte) bool {
		select {
		case out <- append([]byte(nil), frame...):
			return true
		case <-ctx.Done():
			return false
		}
	})
	return ch, out
}

func runWorkspaceFilesList(t *testing.T, ch *workspaceFilesChannel, out <-chan []byte, id string, payload protocol.WorkspaceFilesListPayload) workspaceFilesTestResult {
	t.Helper()
	payload.DaemonReqID = id
	ch.start(id, payload.RuntimeID, payload.ResourceID, payload.DeadlineMS, func(p *workspaceFilesPending) { ch.runList(p, payload) }, protocol.WorkspaceFilesGeneration{})
	result := workspaceFilesTestResult{}
	for {
		frame := waitWorkspaceFilesFrame(t, out)
		var message protocol.Message
		_ = json.Unmarshal(frame, &message)
		if message.Type == protocol.EventWorkspaceFilesError {
			result.errCode = parseWorkspaceFilesError(t, frame)
			return result
		}
		var payload protocol.WorkspaceFilesListResultPayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		result.entries = append(result.entries, payload.Entries...)
		result.nextCursor = payload.NextCursor
		result.skipped += payload.Skipped
		if payload.Final {
			return result
		}
	}
}

func runWorkspaceFilesRead(t *testing.T, ch *workspaceFilesChannel, out <-chan []byte, id, path string, rootOverride ...string) workspaceFilesTestResult {
	t.Helper()
	rootPath := `C:\virtual`
	if len(rootOverride) > 0 {
		rootPath = rootOverride[0]
	}
	payload := protocol.WorkspaceFilesReadPayload{DaemonReqID: id, RuntimeID: "runtime", ResourceID: "resource", RootPath: rootPath, Path: path}
	ch.start(id, payload.RuntimeID, payload.ResourceID, 0, func(p *workspaceFilesPending) { ch.runRead(p, payload) }, protocol.WorkspaceFilesGeneration{})
	result := workspaceFilesTestResult{}
	for {
		frame := waitWorkspaceFilesFrame(t, out)
		var message protocol.Message
		_ = json.Unmarshal(frame, &message)
		if message.Type == protocol.EventWorkspaceFilesError {
			result.errCode = parseWorkspaceFilesError(t, frame)
			return result
		}
		var payload protocol.WorkspaceFilesReadChunkPayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		result.data = append(result.data, payload.Data...)
		result.dataFrames++
		if payload.EOF {
			return result
		}
	}
}

func waitWorkspaceFilesFrame(t *testing.T, out <-chan []byte) []byte {
	t.Helper()
	select {
	case frame := <-out:
		return frame
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for workspace-files response")
		return nil
	}
}

func parseWorkspaceFilesError(t *testing.T, frame []byte) string {
	t.Helper()
	var message protocol.Message
	if err := json.Unmarshal(frame, &message); err != nil {
		t.Fatal(err)
	}
	if message.Type != protocol.EventWorkspaceFilesError {
		t.Fatalf("event = %q, want error", message.Type)
	}
	var payload protocol.WorkspaceFilesErrorPayload
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Code
}

func workspaceFilesTreeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		snapshot[rel] = info.Mode().String() + ":" + fmtWorkspaceFilesIndex(int(info.Size()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func fmtWorkspaceFilesIndex(value int) string {
	return fmt.Sprintf("%08d", value)
}

type fakeWorkspaceFilesOpener struct {
	root *fakeWorkspaceFilesHandle
}

type fakeWorkspaceFilesOpenRecord struct {
	name    string
	request workspaceFilesOpenRequest
}

type fakeWorkspaceFilesTrace struct {
	mu       sync.Mutex
	requests []fakeWorkspaceFilesOpenRecord
}

func (o *fakeWorkspaceFilesOpener) OpenRoot(ctx context.Context, _ string) (workspaceFilesHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, workspaceFilesContextError(err)
	}
	return o.root.clone(), nil
}

type fakeWorkspaceFilesHandle struct {
	mu             sync.Mutex
	name           string
	mode           fs.FileMode
	data           []byte
	reportedSize   int64
	children       map[string]*fakeWorkspaceFilesHandle
	readDirEntries []string
	readDirOffset  int
	reparse        bool
	openError      string
	blockRead      bool
	ignoreClose    bool
	releaseRead    chan struct{}
	readStarted    chan struct{}
	readStartOnce  *sync.Once
	closedSignal   chan struct{}
	closeOnce      *sync.Once
	trace          *fakeWorkspaceFilesTrace
	closed         bool
}

func fakeWorkspaceFilesDirectory() *fakeWorkspaceFilesHandle {
	return &fakeWorkspaceFilesHandle{mode: fs.ModeDir | 0o755, children: make(map[string]*fakeWorkspaceFilesHandle), readStarted: make(chan struct{}), readStartOnce: &sync.Once{}, closedSignal: make(chan struct{}), closeOnce: &sync.Once{}, trace: &fakeWorkspaceFilesTrace{}}
}

func fakeWorkspaceFilesRegular(data []byte) *fakeWorkspaceFilesHandle {
	return &fakeWorkspaceFilesHandle{name: "file", mode: 0o600, data: append([]byte(nil), data...), reportedSize: -1, readStarted: make(chan struct{}), readStartOnce: &sync.Once{}, closedSignal: make(chan struct{}), closeOnce: &sync.Once{}}
}

func fakeWorkspaceFilesLink() *fakeWorkspaceFilesHandle {
	h := fakeWorkspaceFilesDirectory()
	h.reparse = true
	return h
}

func (h *fakeWorkspaceFilesHandle) add(name string, child *fakeWorkspaceFilesHandle) {
	h.mu.Lock()
	defer h.mu.Unlock()
	child.name = name
	h.children[name] = child
	child.setTrace(h.trace)
	h.readDirEntries = append(h.readDirEntries, name)
	sort.Strings(h.readDirEntries)
}

func (h *fakeWorkspaceFilesHandle) setTrace(trace *fakeWorkspaceFilesTrace) {
	h.trace = trace
	for _, child := range h.children {
		child.setTrace(trace)
	}
}

func (h *fakeWorkspaceFilesHandle) clone() *fakeWorkspaceFilesHandle {
	h.mu.Lock()
	defer h.mu.Unlock()
	clone := &fakeWorkspaceFilesHandle{
		name: h.name, mode: h.mode, data: append([]byte(nil), h.data...), reportedSize: h.reportedSize,
		children: make(map[string]*fakeWorkspaceFilesHandle, len(h.children)), readDirEntries: append([]string(nil), h.readDirEntries...),
		reparse: h.reparse, openError: h.openError, blockRead: h.blockRead, ignoreClose: h.ignoreClose, releaseRead: h.releaseRead,
		readStarted: h.readStarted, readStartOnce: h.readStartOnce, closedSignal: h.closedSignal, closeOnce: h.closeOnce,
		trace: h.trace,
	}
	for name, child := range h.children {
		clone.children[name] = child.clone()
	}
	return clone
}

func (h *fakeWorkspaceFilesHandle) Read(data []byte) (int, error) {
	h.readStartOnce.Do(func() { close(h.readStarted) })
	if h.blockRead {
		if h.ignoreClose {
			<-h.releaseRead
			return 0, io.EOF
		}
		<-h.closedSignal
		return 0, os.ErrClosed
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0, os.ErrClosed
	}
	if h.readDirOffset >= len(h.data) {
		return 0, io.EOF
	}
	n := copy(data, h.data[h.readDirOffset:])
	h.readDirOffset += n
	return n, nil
}

func (h *fakeWorkspaceFilesHandle) ReadDir(n int) ([]os.DirEntry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, os.ErrClosed
	}
	start := h.readDirOffset
	end := len(h.readDirEntries)
	if n > 0 && end-start > n {
		end = start + n
	}
	entries := make([]os.DirEntry, 0, end-start)
	for _, name := range h.readDirEntries[start:end] {
		entries = append(entries, fakeWorkspaceFilesDirEntry(name))
	}
	h.readDirOffset = end
	if end == len(h.readDirEntries) {
		return entries, io.EOF
	}
	return entries, nil
}

func (h *fakeWorkspaceFilesHandle) Stat() (os.FileInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, os.ErrClosed
	}
	size := int64(len(h.data))
	if h.reportedSize >= 0 {
		size = h.reportedSize
	}
	mode := h.mode
	if h.reparse {
		mode |= fs.ModeSymlink
	}
	return fakeWorkspaceFilesInfo{name: h.name, size: size, mode: mode}, nil
}

func (h *fakeWorkspaceFilesHandle) IsReparsePoint() bool { return h.reparse }

func (h *fakeWorkspaceFilesHandle) OpenChild(ctx context.Context, name string, request workspaceFilesOpenRequest) (workspaceFilesHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, workspaceFilesContextError(err)
	}
	h.trace.mu.Lock()
	h.trace.requests = append(h.trace.requests, fakeWorkspaceFilesOpenRecord{name: name, request: request})
	h.trace.mu.Unlock()
	h.mu.Lock()
	child := h.children[name]
	h.mu.Unlock()
	if child == nil {
		return nil, workspaceFilesError(protocol.WorkspaceFilesErrorInvalidPath)
	}
	if child.openError != "" {
		return nil, workspaceFilesError(child.openError)
	}
	if child.reparse {
		return nil, workspaceFilesError(protocol.WorkspaceFilesErrorSymlinkDenied)
	}
	if request.mode == workspaceFilesDirectory && !child.mode.IsDir() {
		return nil, workspaceFilesError(protocol.WorkspaceFilesErrorNotDirectory)
	}
	if request.mode == workspaceFilesReadOnly && !child.mode.IsRegular() {
		return nil, workspaceFilesError(protocol.WorkspaceFilesErrorNotRegular)
	}
	return child.clone(), nil
}

func (h *fakeWorkspaceFilesHandle) Close() error {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	h.closeOnce.Do(func() { close(h.closedSignal) })
	return nil
}

type fakeWorkspaceFilesInfo struct {
	name string
	size int64
	mode fs.FileMode
}

func (i fakeWorkspaceFilesInfo) Name() string       { return i.name }
func (i fakeWorkspaceFilesInfo) Size() int64        { return i.size }
func (i fakeWorkspaceFilesInfo) Mode() fs.FileMode  { return i.mode }
func (i fakeWorkspaceFilesInfo) ModTime() time.Time { return time.Time{} }
func (i fakeWorkspaceFilesInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fakeWorkspaceFilesInfo) Sys() any           { return nil }

type fakeWorkspaceFilesEntry string

func fakeWorkspaceFilesDirEntry(name string) os.DirEntry { return fakeWorkspaceFilesEntry(name) }
func (e fakeWorkspaceFilesEntry) Name() string           { return string(e) }
func (e fakeWorkspaceFilesEntry) IsDir() bool            { return false }
func (e fakeWorkspaceFilesEntry) Type() fs.FileMode      { return 0 }
func (e fakeWorkspaceFilesEntry) Info() (os.FileInfo, error) {
	return fakeWorkspaceFilesInfo{name: string(e)}, nil
}

func waitFakeSignal(ch <-chan struct{}, timeout time.Duration) (struct{}, error) {
	select {
	case <-ch:
		return struct{}{}, nil
	case <-time.After(timeout):
		return struct{}{}, errors.New("signal timeout")
	}
}

func TestWorkspaceFilesStableErrorFramesNeverEchoUnderlyingErrors(t *testing.T) {
	frame := buildWorkspaceFilesErrorFrame("req", "runtime", "resource", protocol.WorkspaceFilesErrorSymlinkDenied, protocol.WorkspaceFilesGeneration{})
	if frame == nil || bytes.Contains(frame, []byte("C:\\private")) || bytes.Contains(frame, []byte("/home/secret")) {
		t.Fatalf("error frame contains a path or is empty: %q", frame)
	}
	if code := parseWorkspaceFilesError(t, frame); code != protocol.WorkspaceFilesErrorSymlinkDenied {
		t.Fatalf("error code = %q", code)
	}
}

func TestWorkspaceFilesPageCandidateHeapIsByteSortedAndBounded(t *testing.T) {
	root := fakeWorkspaceFilesDirectory()
	for _, name := range []string{"z", "a", "m", "b", "c", "é"} {
		root.add(name, fakeWorkspaceFilesRegular(nil))
	}
	h, err := root.clone().ReadDir(100)
	if err != io.EOF || len(h) != 6 {
		t.Fatalf("ReadDir fixture len=%d err=%v", len(h), err)
	}
	page, more, err := workspaceFilesPageCandidates(context.Background(), root.clone(), "", 3)
	if err != nil || !more || len(page) != 3 || page[0] != "a" || page[1] != "b" || page[2] != "c" {
		t.Fatalf("page=%q more=%v err=%v", page, more, err)
	}
}

func TestWorkspaceFilesOnlyReturnsStableErrorCodes(t *testing.T) {
	if got := workspaceFilesErrorCode(errors.New(`open C:\secret\file: permission denied`), protocol.WorkspaceFilesErrorUnavailable); got != protocol.WorkspaceFilesErrorUnavailable {
		t.Fatalf("unknown OS error mapping = %q", got)
	}
	if frame := buildWorkspaceFilesErrorFrame("req", "runtime", "resource", `open C:\secret\file`, protocol.WorkspaceFilesGeneration{}); bytes.Contains(frame, []byte("secret")) {
		t.Fatalf("arbitrary error text escaped into frame: %q", frame)
	}
}
