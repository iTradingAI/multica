package daemon

import (
	"bytes"
	"container/heap"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	workspaceFilesMaxFrameBytes         = 48 * 1024
	workspaceFilesMaxReadBytes          = 1_048_576
	workspaceFilesMaxPathBytes          = 4096
	workspaceFilesMaxCursorBytes        = 4096
	workspaceFilesDefaultPage           = 100
	workspaceFilesMaxPage               = 200
	workspaceFilesMaxInFlight           = 4
	workspaceFilesMaxReadChunks         = 33
	workspaceFilesOutboundQueueHeadroom = 28
	workspaceFilesOutboundQueueMin      = workspaceFilesMaxInFlight*workspaceFilesMaxReadChunks + workspaceFilesOutboundQueueHeadroom
	workspaceFilesTimeout               = 10 * time.Second
)

type workspaceFilesOpenMode uint8

const (
	workspaceFilesInspect workspaceFilesOpenMode = iota
	workspaceFilesDirectory
	workspaceFilesReadOnly
)

type workspaceFilesOpenRequest struct {
	mode        workspaceFilesOpenMode
	nonBlocking bool
	closeOnExec bool
	noFollow    bool
}

func workspaceFilesOpenRequestFor(mode workspaceFilesOpenMode) workspaceFilesOpenRequest {
	return workspaceFilesOpenRequest{
		mode:        mode,
		nonBlocking: true,
		closeOnExec: true,
		noFollow:    true,
	}
}

// The opener only follows already-open directory handles. Implementations must
// open each child without following symlinks/reparse points and must never
// create or modify filesystem objects.
type workspaceFilesHandle interface {
	Read([]byte) (int, error)
	ReadDir(int) ([]os.DirEntry, error)
	Stat() (os.FileInfo, error)
	IsReparsePoint() bool
	OpenChild(context.Context, string, workspaceFilesOpenRequest) (workspaceFilesHandle, error)
	Close() error
}

type workspaceFilesOpener interface {
	OpenRoot(context.Context, string) (workspaceFilesHandle, error)
}

type workspaceFilesCodedError struct{ code string }

func (e workspaceFilesCodedError) Error() string              { return e.code }
func (e workspaceFilesCodedError) WorkspaceFilesCode() string { return e.code }

func workspaceFilesError(code string) error { return workspaceFilesCodedError{code: code} }

func workspaceFilesContextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return workspaceFilesError(protocol.WorkspaceFilesErrorTimeout)
	}
	return workspaceFilesError(protocol.WorkspaceFilesErrorUnavailable)
}

type workspaceFilesSendFunc func(context.Context, []byte) bool

type workspaceFilesPending struct {
	id         string
	runtimeID  string
	resourceID string
	ctx        context.Context
	cancel     context.CancelFunc

	mu       sync.Mutex
	active   []io.Closer
	canceled bool
	done     sync.Once
}

func (p *workspaceFilesPending) setActive(closer io.Closer) bool {
	p.mu.Lock()
	if p.canceled || p.ctx.Err() != nil {
		p.mu.Unlock()
		_ = closer.Close()
		return false
	}
	p.active = append(p.active, closer)
	p.mu.Unlock()
	return true
}

func (p *workspaceFilesPending) clearActive(closer io.Closer) {
	p.mu.Lock()
	for i, active := range p.active {
		if active == closer {
			p.active = append(p.active[:i], p.active[i+1:]...)
			break
		}
	}
	p.mu.Unlock()
	_ = closer.Close()
}

func (p *workspaceFilesPending) closeActive() {
	p.mu.Lock()
	active := p.active
	p.active = nil
	p.mu.Unlock()
	for _, closer := range active {
		_ = closer.Close()
	}
}

type workspaceFilesChannel struct {
	mu      sync.Mutex
	send    workspaceFilesSendFunc
	opening workspaceFilesOpener
	cursors *workspaceFilesCursorSigner
	pending map[string]*workspaceFilesPending
	active  map[string]struct{}
	slots   chan struct{}
}

var workspaceFilesProcessCursor struct {
	once   sync.Once
	signer *workspaceFilesCursorSigner
}

func defaultWorkspaceFilesCursorSigner() *workspaceFilesCursorSigner {
	workspaceFilesProcessCursor.once.Do(func() {
		workspaceFilesProcessCursor.signer, _ = newWorkspaceFilesCursorSigner()
	})
	return workspaceFilesProcessCursor.signer
}

func newWorkspaceFilesChannel() *workspaceFilesChannel {
	signer := defaultWorkspaceFilesCursorSigner()
	if signer == nil {
		return &workspaceFilesChannel{}
	}
	return &workspaceFilesChannel{
		opening: newWorkspaceFilesOpener(),
		cursors: signer,
		pending: make(map[string]*workspaceFilesPending),
		active:  make(map[string]struct{}),
		slots:   make(chan struct{}, workspaceFilesMaxInFlight),
	}
}

func newWorkspaceFilesChannelForTest(opener workspaceFilesOpener, send workspaceFilesSendFunc) *workspaceFilesChannel {
	signer, _ := newWorkspaceFilesCursorSigner()
	return &workspaceFilesChannel{
		opening: opener,
		cursors: signer,
		pending: make(map[string]*workspaceFilesPending),
		active:  make(map[string]struct{}),
		slots:   make(chan struct{}, workspaceFilesMaxInFlight),
		send:    send,
	}
}

func (c *workspaceFilesChannel) attach(send workspaceFilesSendFunc) {
	c.mu.Lock()
	c.send = send
	if send != nil {
		c.mu.Unlock()
		return
	}
	requests := make([]*workspaceFilesPending, 0, len(c.pending))
	for _, p := range c.pending {
		requests = append(requests, p)
	}
	c.mu.Unlock()
	for _, p := range requests {
		c.cancelPendingRequest(p)
	}
}

func (c *workspaceFilesChannel) HandleMessage(messageType string, raw json.RawMessage) {
	if c == nil {
		return
	}
	if !workspaceFilesCapabilityEnabled || c.opening == nil {
		var id, runtimeID, resourceID string
		switch messageType {
		case protocol.EventWorkspaceFilesList:
			var payload protocol.WorkspaceFilesListPayload
			if json.Unmarshal(raw, &payload) == nil {
				id, runtimeID, resourceID = payload.DaemonReqID, payload.RuntimeID, payload.ResourceID
			}
		case protocol.EventWorkspaceFilesRead:
			var payload protocol.WorkspaceFilesReadPayload
			if json.Unmarshal(raw, &payload) == nil {
				id, runtimeID, resourceID = payload.DaemonReqID, payload.RuntimeID, payload.ResourceID
			}
		}
		if id != "" {
			c.sendImmediateError(id, runtimeID, resourceID, protocol.WorkspaceFilesErrorUnsupported)
		}
		return
	}
	switch messageType {
	case protocol.EventWorkspaceFilesList:
		var payload protocol.WorkspaceFilesListPayload
		if json.Unmarshal(raw, &payload) != nil {
			return
		}
		c.start(payload.DaemonReqID, payload.RuntimeID, payload.ResourceID, payload.DeadlineMS, func(p *workspaceFilesPending) {
			c.runList(p, payload)
		})
	case protocol.EventWorkspaceFilesRead:
		var payload protocol.WorkspaceFilesReadPayload
		if json.Unmarshal(raw, &payload) != nil {
			return
		}
		c.start(payload.DaemonReqID, payload.RuntimeID, payload.ResourceID, payload.DeadlineMS, func(p *workspaceFilesPending) {
			c.runRead(p, payload)
		})
	case protocol.EventWorkspaceFilesCancel:
		var payload protocol.WorkspaceFilesCancelPayload
		if json.Unmarshal(raw, &payload) == nil {
			c.cancelPendingForRuntime(payload.DaemonReqID, payload.RuntimeID)
		}
	}
}

func (c *workspaceFilesChannel) start(id, runtimeID, resourceID string, deadlineMS int64, run func(*workspaceFilesPending)) {
	if id == "" || len(id) > 128 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), workspaceFilesRequestBudget(deadlineMS))
	p := &workspaceFilesPending{id: id, runtimeID: runtimeID, resourceID: resourceID, ctx: ctx, cancel: cancel}
	c.mu.Lock()
	if _, exists := c.active[id]; exists {
		c.mu.Unlock()
		cancel()
		return
	}
	select {
	case c.slots <- struct{}{}:
	default:
		c.mu.Unlock()
		cancel()
		c.sendImmediateError(id, runtimeID, resourceID, protocol.WorkspaceFilesErrorBusy)
		return
	}
	c.pending[id] = p
	c.active[id] = struct{}{}
	c.mu.Unlock()

	context.AfterFunc(ctx, p.closeActive)
	go func() {
		<-ctx.Done()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			c.finishError(p, protocol.WorkspaceFilesErrorTimeout)
		}
	}()
	go func() {
		defer c.workerFinished(id)
		run(p)
	}()
}

func workspaceFilesRequestBudget(deadlineMS int64) time.Duration {
	budget := workspaceFilesTimeout
	if deadlineMS == 0 {
		return budget
	}
	remaining := time.Until(time.UnixMilli(deadlineMS))
	if remaining < budget {
		budget = remaining
	}
	if budget < 0 {
		return 0
	}
	return budget
}

func (c *workspaceFilesChannel) cancelPending(id string) {
	c.mu.Lock()
	p := c.pending[id]
	c.mu.Unlock()
	if p == nil {
		return
	}
	c.cancelPendingRequest(p)
}

func (c *workspaceFilesChannel) cancelPendingRequest(p *workspaceFilesPending) {
	p.done.Do(func() {
		p.mu.Lock()
		p.canceled = true
		p.mu.Unlock()
		p.closeActive()
		p.cancel()
		c.removePending(p)
	})
}

func (c *workspaceFilesChannel) cancelPendingForRuntime(id, runtimeID string) {
	if id == "" || runtimeID == "" {
		return
	}
	c.mu.Lock()
	p := c.pending[id]
	c.mu.Unlock()
	if p == nil || p.runtimeID != runtimeID {
		return
	}
	c.cancelPendingRequest(p)
}

func (c *workspaceFilesChannel) finishFrames(p *workspaceFilesPending, frames [][]byte) {
	if len(frames) == 0 {
		c.finishError(p, protocol.WorkspaceFilesErrorUnavailable)
		return
	}
	for _, frame := range frames[:len(frames)-1] {
		if p.ctx.Err() != nil || !c.sendFrame(p.ctx, frame) {
			if errors.Is(p.ctx.Err(), context.DeadlineExceeded) {
				c.finishError(p, protocol.WorkspaceFilesErrorTimeout)
			} else {
				c.cancelPendingRequest(p)
			}
			return
		}
	}
	completed := false
	p.done.Do(func() {
		completed = true
		p.closeActive()
		p.cancel()
		c.removePending(p)
	})
	if completed {
		// Claim the terminal result before queueing the final frame so a timer
		// racing this write cannot emit a second terminal response.
		_ = c.sendFrame(context.Background(), frames[len(frames)-1])
	}
}

func (c *workspaceFilesChannel) finishError(p *workspaceFilesPending, code string) {
	frame := buildWorkspaceFilesErrorFrame(p.id, p.runtimeID, p.resourceID, code)
	p.done.Do(func() {
		p.closeActive()
		p.cancel()
		c.removePending(p)
		if frame != nil {
			_ = c.sendFrame(context.Background(), frame)
		}
	})
}

func (c *workspaceFilesChannel) removePending(p *workspaceFilesPending) {
	c.mu.Lock()
	if c.pending[p.id] == p {
		delete(c.pending, p.id)
	}
	c.mu.Unlock()
}

func (c *workspaceFilesChannel) workerFinished(id string) {
	c.mu.Lock()
	delete(c.active, id)
	c.mu.Unlock()
	<-c.slots
}

func (c *workspaceFilesChannel) sendFrame(ctx context.Context, frame []byte) bool {
	c.mu.Lock()
	send := c.send
	c.mu.Unlock()
	return send != nil && send(ctx, frame)
}

func (c *workspaceFilesChannel) sendImmediateError(id, runtimeID, resourceID, code string) {
	frame := buildWorkspaceFilesErrorFrame(id, runtimeID, resourceID, code)
	if frame == nil {
		return
	}
	_ = c.sendFrame(context.Background(), frame)
}

func (c *workspaceFilesChannel) runList(p *workspaceFilesPending, payload protocol.WorkspaceFilesListPayload) {
	if payload.RuntimeID == "" || payload.ResourceID == "" || len(payload.RuntimeID) > 128 || len(payload.ResourceID) > 128 || payload.RootPath == "" || len(payload.RootPath) > 32768 {
		c.finishError(p, protocol.WorkspaceFilesErrorInvalidPath)
		return
	}
	parts, err := validateWorkspaceFilesPath(payload.Path, true)
	if err != nil {
		c.finishError(p, protocol.WorkspaceFilesErrorInvalidPath)
		return
	}
	pageSize := payload.PageSize
	if pageSize == 0 {
		pageSize = workspaceFilesDefaultPage
	}
	if pageSize < 1 || pageSize > workspaceFilesMaxPage {
		c.finishError(p, protocol.WorkspaceFilesErrorInvalidPath)
		return
	}
	lastChecked := ""
	if payload.Cursor != "" {
		if len(payload.Cursor) > workspaceFilesMaxCursorBytes {
			c.finishError(p, protocol.WorkspaceFilesErrorInvalidCursor)
			return
		}
		lastChecked, err = c.cursors.decode(payload.Cursor, payload.ResourceID, payload.Path)
		if err != nil {
			c.finishError(p, protocol.WorkspaceFilesErrorInvalidCursor)
			return
		}
	}
	dir, err := c.openResolved(p, payload.RootPath, parts, workspaceFilesDirectory)
	if err != nil {
		c.finishError(p, workspaceFilesErrorCode(err, protocol.WorkspaceFilesErrorNotDirectory))
		return
	}
	defer p.clearActive(dir)
	info, err := dir.Stat()
	if err != nil || dir.IsReparsePoint() || !info.IsDir() {
		c.finishError(p, protocol.WorkspaceFilesErrorNotDirectory)
		return
	}
	candidates, hasMore, err := workspaceFilesPageCandidates(p.ctx, dir, lastChecked, pageSize)
	if err != nil {
		c.finishError(p, protocol.WorkspaceFilesErrorUnavailable)
		return
	}
	entries := make([]protocol.WorkspaceFilesEntry, 0, len(candidates))
	skipped := 0
	for _, candidate := range candidates {
		if !utf8.ValidString(candidate) || !validWorkspaceFilesEntryName(candidate) {
			skipped++
			continue
		}
		child, openErr := dir.OpenChild(p.ctx, candidate, workspaceFilesOpenRequestFor(workspaceFilesInspect))
		if openErr != nil {
			if isWorkspaceFilesLinkError(openErr) {
				skipped++
				continue
			}
			if p.ctx.Err() != nil {
				return
			}
			skipped++
			continue
		}
		if !p.setActive(child) {
			return
		}
		info, statErr := child.Stat()
		isReparse := child.IsReparsePoint()
		p.clearActive(child)
		if statErr != nil || isReparse {
			skipped++
			continue
		}
		switch {
		case info.Mode().IsRegular():
			entries = append(entries, protocol.WorkspaceFilesEntry{Name: candidate, Type: "regular"})
		case info.IsDir():
			entries = append(entries, protocol.WorkspaceFilesEntry{Name: candidate, Type: "directory"})
		default:
			skipped++
		}
	}
	nextCursor := ""
	if hasMore && len(candidates) > 0 {
		nextCursor, err = c.cursors.encode(payload.ResourceID, payload.Path, candidates[len(candidates)-1])
		if err != nil {
			c.finishError(p, protocol.WorkspaceFilesErrorUnavailable)
			return
		}
	}
	frames, err := buildWorkspaceFilesListFrames(payload.DaemonReqID, payload.RuntimeID, payload.ResourceID, entries, nextCursor, skipped)
	if err != nil {
		c.finishError(p, protocol.WorkspaceFilesErrorUnavailable)
		return
	}
	c.finishFrames(p, frames)
}

func (c *workspaceFilesChannel) runRead(p *workspaceFilesPending, payload protocol.WorkspaceFilesReadPayload) {
	if payload.RuntimeID == "" || payload.ResourceID == "" || len(payload.RuntimeID) > 128 || len(payload.ResourceID) > 128 || payload.RootPath == "" || len(payload.RootPath) > 32768 {
		c.finishError(p, protocol.WorkspaceFilesErrorInvalidPath)
		return
	}
	parts, err := validateWorkspaceFilesPath(payload.Path, false)
	if err != nil || len(parts) == 0 {
		c.finishError(p, protocol.WorkspaceFilesErrorInvalidPath)
		return
	}
	file, err := c.openResolved(p, payload.RootPath, parts, workspaceFilesReadOnly)
	if err != nil {
		c.finishError(p, workspaceFilesErrorCode(err, protocol.WorkspaceFilesErrorNotRegular))
		return
	}
	defer p.clearActive(file)
	info, err := file.Stat()
	if err != nil || file.IsReparsePoint() || !info.Mode().IsRegular() {
		c.finishError(p, protocol.WorkspaceFilesErrorNotRegular)
		return
	}
	if info.Size() > workspaceFilesMaxReadBytes {
		c.finishError(p, protocol.WorkspaceFilesErrorTooLarge)
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, workspaceFilesMaxReadBytes+1))
	if err != nil {
		if errors.Is(p.ctx.Err(), context.DeadlineExceeded) {
			c.finishError(p, protocol.WorkspaceFilesErrorTimeout)
			return
		}
		if p.ctx.Err() != nil {
			return
		}
		c.finishError(p, protocol.WorkspaceFilesErrorUnavailable)
		return
	}
	if len(data) > workspaceFilesMaxReadBytes {
		c.finishError(p, protocol.WorkspaceFilesErrorTooLarge)
		return
	}
	if !utf8.Valid(data) {
		c.finishError(p, protocol.WorkspaceFilesErrorInvalidUTF8)
		return
	}
	frames, err := buildWorkspaceFilesReadFrames(payload.DaemonReqID, payload.RuntimeID, payload.ResourceID, data)
	if err != nil {
		c.finishError(p, protocol.WorkspaceFilesErrorUnavailable)
		return
	}
	c.finishFrames(p, frames)
}

func (c *workspaceFilesChannel) openResolved(p *workspaceFilesPending, root string, parts []string, finalMode workspaceFilesOpenMode) (workspaceFilesHandle, error) {
	h, err := c.opening.OpenRoot(p.ctx, root)
	if err != nil {
		return nil, err
	}
	if !p.setActive(h) {
		return nil, p.ctx.Err()
	}
	steps := parts
	if len(steps) == 0 {
		return h, nil
	}
	for i, component := range steps {
		mode := workspaceFilesInspect
		if i < len(steps)-1 {
			mode = workspaceFilesDirectory
		} else {
			mode = finalMode
		}
		next, openErr := h.OpenChild(p.ctx, component, workspaceFilesOpenRequestFor(mode))
		if openErr != nil {
			p.clearActive(h)
			return nil, openErr
		}
		if !p.setActive(next) {
			p.clearActive(h)
			return nil, p.ctx.Err()
		}
		p.clearActive(h)
		h = next
	}
	return h, nil
}

func workspaceFilesErrorCode(err error, fallback string) string {
	var coded interface{ WorkspaceFilesCode() string }
	if errors.As(err, &coded) {
		return coded.WorkspaceFilesCode()
	}
	return fallback
}

func isWorkspaceFilesLinkError(err error) bool {
	return workspaceFilesErrorCode(err, "") == protocol.WorkspaceFilesErrorSymlinkDenied
}

func validateWorkspaceFilesPath(wirePath string, allowRoot bool) ([]string, error) {
	if len(wirePath) > workspaceFilesMaxPathBytes || !utf8.ValidString(wirePath) || strings.IndexByte(wirePath, 0) >= 0 || strings.Contains(wirePath, `\`) || strings.HasPrefix(wirePath, "/") {
		return nil, errors.New("invalid path")
	}
	if wirePath == "." && allowRoot {
		return nil, nil
	}
	if wirePath == "" {
		return nil, errors.New("invalid path")
	}
	parts := strings.Split(wirePath, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || !validWorkspaceFilesEntryName(part) {
			return nil, errors.New("invalid path")
		}
	}
	return parts, nil
}

func validWorkspaceFilesEntryName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") || !utf8.ValidString(name) || strings.IndexByte(name, 0) >= 0 || strings.ContainsAny(name, `/\:`) {
		return false
	}
	base := strings.ToUpper(strings.TrimRight(name, ". "))
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = strings.TrimRight(base[:dot], ". ")
	}
	switch base {
	case "CON", "CONIN$", "CONOUT$", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9", "COM¹", "COM²", "COM³", "LPT¹", "LPT²", "LPT³":
		return false
	default:
		return true
	}
}

type workspaceFilesNameHeap []string

func (h workspaceFilesNameHeap) Len() int { return len(h) }
func (h workspaceFilesNameHeap) Less(i, j int) bool {
	return bytes.Compare([]byte(h[i]), []byte(h[j])) > 0
}
func (h workspaceFilesNameHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *workspaceFilesNameHeap) Push(v any)   { *h = append(*h, v.(string)) }
func (h *workspaceFilesNameHeap) Pop() any {
	old := *h
	n := len(old)
	v := old[n-1]
	*h = old[:n-1]
	return v
}

func workspaceFilesPageCandidates(ctx context.Context, dir workspaceFilesHandle, lastChecked string, pageSize int) ([]string, bool, error) {
	maxCandidates := pageSize + 1
	h := make(workspaceFilesNameHeap, 0, maxCandidates)
	heap.Init(&h)
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		batch, err := dir.ReadDir(256)
		for _, entry := range batch {
			name := entry.Name()
			if bytes.Compare([]byte(name), []byte(lastChecked)) <= 0 {
				continue
			}
			if len(h) < maxCandidates {
				heap.Push(&h, name)
			} else if bytes.Compare([]byte(name), []byte(h[0])) < 0 {
				heap.Pop(&h)
				heap.Push(&h, name)
			}
		}
		if errors.Is(err, io.EOF) || len(batch) == 0 && err == nil {
			break
		}
		if err != nil {
			return nil, false, err
		}
	}
	sort.Slice(h, func(i, j int) bool { return bytes.Compare([]byte(h[i]), []byte(h[j])) < 0 })
	hasMore := len(h) > pageSize
	if hasMore {
		h = h[:pageSize]
	}
	return h, hasMore, nil
}

type workspaceFilesCursorSigner struct{ key [32]byte }

func newWorkspaceFilesCursorSigner() (*workspaceFilesCursorSigner, error) {
	signer := &workspaceFilesCursorSigner{}
	if _, err := rand.Read(signer.key[:]); err != nil {
		return nil, err
	}
	return signer, nil
}

func (s *workspaceFilesCursorSigner) encode(resourceID, path, lastChecked string) (string, error) {
	if len(lastChecked) > 65535 {
		return "", errors.New("cursor name too long")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	payload := make([]byte, 18+len(lastChecked))
	binary.BigEndian.PutUint16(payload[:2], uint16(len(lastChecked)))
	copy(payload[2:2+len(lastChecked)], lastChecked)
	copy(payload[2+len(lastChecked):], nonce[:])
	mac := s.mac(resourceID, path, payload)
	return base64.RawURLEncoding.EncodeToString(append(payload, mac...)), nil
}

func (s *workspaceFilesCursorSigner) decode(token, resourceID, path string) (string, error) {
	if len(token) == 0 || len(token) > workspaceFilesMaxCursorBytes {
		return "", errors.New("invalid cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) < 2+16+sha256.Size {
		return "", errors.New("invalid cursor")
	}
	payload := raw[:len(raw)-sha256.Size]
	mac := raw[len(raw)-sha256.Size:]
	nameLen := int(binary.BigEndian.Uint16(payload[:2]))
	if len(payload) != 2+nameLen+16 || !hmac.Equal(mac, s.mac(resourceID, path, payload)) {
		return "", errors.New("invalid cursor")
	}
	return string(payload[2 : 2+nameLen]), nil
}

func (s *workspaceFilesCursorSigner) mac(resourceID, path string, payload []byte) []byte {
	h := hmac.New(sha256.New, s.key[:])
	_, _ = io.WriteString(h, resourceID)
	_, _ = h.Write([]byte{0})
	_, _ = io.WriteString(h, path)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(payload)
	return h.Sum(nil)
}

func buildWorkspaceFilesListFrames(reqID, runtimeID, resourceID string, entries []protocol.WorkspaceFilesEntry, nextCursor string, skipped int) ([][]byte, error) {
	frames := make([][]byte, 0, 1)
	current := make([]protocol.WorkspaceFilesEntry, 0, len(entries))
	for _, entry := range entries {
		candidate := append(current, entry)
		frame, err := marshalWorkspaceFilesFrame(protocol.EventWorkspaceFilesListResult, protocol.WorkspaceFilesListResultPayload{
			DaemonReqID: reqID, RuntimeID: runtimeID, ResourceID: resourceID, Seq: len(frames), Entries: candidate,
			NextCursor: nextCursor, Skipped: skipped, Final: true,
		})
		if err != nil {
			return nil, err
		}
		if len(frame) > workspaceFilesMaxFrameBytes {
			if len(current) == 0 {
				return nil, errors.New("entry exceeds frame limit")
			}
			frames = append(frames, mustMarshalWorkspaceFilesList(reqID, runtimeID, resourceID, len(frames), current, "", 0, false))
			current = []protocol.WorkspaceFilesEntry{entry}
		} else {
			current = candidate
		}
	}
	if current == nil {
		current = []protocol.WorkspaceFilesEntry{}
	}
	last, err := marshalWorkspaceFilesFrame(protocol.EventWorkspaceFilesListResult, protocol.WorkspaceFilesListResultPayload{
		DaemonReqID: reqID, RuntimeID: runtimeID, ResourceID: resourceID, Seq: len(frames), Entries: current,
		NextCursor: nextCursor, Skipped: skipped, Final: true,
	})
	if err != nil || len(last) > workspaceFilesMaxFrameBytes {
		return nil, errors.New("list result exceeds frame limit")
	}
	frames = append(frames, last)
	return frames, nil
}

func mustMarshalWorkspaceFilesList(reqID, runtimeID, resourceID string, seq int, entries []protocol.WorkspaceFilesEntry, cursor string, skipped int, final bool) []byte {
	frame, _ := marshalWorkspaceFilesFrame(protocol.EventWorkspaceFilesListResult, protocol.WorkspaceFilesListResultPayload{
		DaemonReqID: reqID, RuntimeID: runtimeID, ResourceID: resourceID, Seq: seq, Entries: entries,
		NextCursor: cursor, Skipped: skipped, Final: final,
	})
	return frame
}

func buildWorkspaceFilesReadFrames(reqID, runtimeID, resourceID string, data []byte) ([][]byte, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("invalid utf-8")
	}
	if len(data) == 0 {
		frame, err := marshalWorkspaceFilesFrame(protocol.EventWorkspaceFilesReadChunk, protocol.WorkspaceFilesReadChunkPayload{
			DaemonReqID: reqID, RuntimeID: runtimeID, ResourceID: resourceID, Data: []byte{}, EOF: true,
		})
		if err != nil || len(frame) > workspaceFilesMaxFrameBytes {
			return nil, errors.New("empty read chunk exceeds frame limit")
		}
		return [][]byte{frame}, nil
	}
	frames := make([][]byte, 0, (len(data)+30*1024-1)/(30*1024))
	for offset := 0; offset < len(data); {
		maxEnd := offset + 32*1024
		if maxEnd > len(data) {
			maxEnd = len(data)
		}
		end := maxEnd
		for end > offset && end < len(data) && !utf8.RuneStart(data[end]) {
			end--
		}
		if end == offset {
			return nil, errors.New("invalid chunk boundary")
		}
		for {
			chunk := data[offset:end]
			payload := protocol.WorkspaceFilesReadChunkPayload{
				DaemonReqID: reqID, RuntimeID: runtimeID, ResourceID: resourceID, Seq: len(frames), Data: chunk,
				EOF: end == len(data),
			}
			frame, err := marshalWorkspaceFilesFrame(protocol.EventWorkspaceFilesReadChunk, payload)
			if err != nil {
				return nil, err
			}
			if len(frame) <= workspaceFilesMaxFrameBytes {
				frames = append(frames, frame)
				break
			}
			end -= 4
			for end > offset && end < len(data) && !utf8.RuneStart(data[end]) {
				end--
			}
			if end == offset {
				return nil, errors.New("read chunk exceeds frame limit")
			}
		}
		offset = end
	}
	return frames, nil
}

func buildWorkspaceFilesErrorFrame(reqID, runtimeID, resourceID, code string) []byte {
	if !workspaceFilesStableError(code) {
		code = protocol.WorkspaceFilesErrorUnavailable
	}
	frame, err := marshalWorkspaceFilesFrame(protocol.EventWorkspaceFilesError, protocol.WorkspaceFilesErrorPayload{
		DaemonReqID: reqID, RuntimeID: runtimeID, ResourceID: resourceID, Code: code,
	})
	if err != nil || len(frame) > workspaceFilesMaxFrameBytes {
		return nil
	}
	return frame
}

func workspaceFilesStableError(code string) bool {
	switch code {
	case protocol.WorkspaceFilesErrorInvalidPath, protocol.WorkspaceFilesErrorSymlinkDenied,
		protocol.WorkspaceFilesErrorNotRegular, protocol.WorkspaceFilesErrorNotDirectory,
		protocol.WorkspaceFilesErrorTooLarge, protocol.WorkspaceFilesErrorInvalidUTF8,
		protocol.WorkspaceFilesErrorTimeout, protocol.WorkspaceFilesErrorBusy,
		protocol.WorkspaceFilesErrorUnsupported, protocol.WorkspaceFilesErrorInvalidCursor,
		protocol.WorkspaceFilesErrorUnavailable:
		return true
	default:
		return false
	}
}

func marshalWorkspaceFilesFrame(event string, payload any) ([]byte, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(protocol.Message{Type: event, Payload: payloadBytes})
}
