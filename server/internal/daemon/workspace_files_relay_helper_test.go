package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/url"
	"os"
	"runtime"
	"testing"
	"time"
)

// This test-only process runs the native daemon channel for the realtime
// integration suite. Its stdin lifetime belongs to that suite, not a service.
func TestWorkspaceFilesRelayNativeProcess(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("MULTICA_FILES_NATIVE_HELPER") != "1" {
		t.Skip("run only as the explicitly configured Linux relay test subprocess")
	}
	var cfg struct {
		URL, Token, Daemon, State string
		Runtimes                  []string
	}
	if json.Unmarshal([]byte(os.Getenv("MULTICA_FILES_NATIVE_CONFIG")), &cfg) != nil {
		t.Fatal("invalid native helper fixture configuration")
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") || len(cfg.Runtimes) == 0 {
		t.Fatal("native helper requires a loopback fixture server and runtime")
	}
	if !workspaceFilesCapabilityEnabled {
		t.Fatal("Linux native v2 opener unavailable")
	}
	d := New(Config{ServerBaseURL: cfg.URL, DaemonID: cfg.Daemon, WorkspacesRoot: cfg.State, HeartbeatInterval: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.client.SetToken(cfg.Token)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := d.runTaskWakeupConnection(ctx, cfg.Runtimes, make(chan taskWakeup, 16), make(chan struct{}))
		done <- err
	}()
	stdinDone := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); close(stdinDone) }()
	select {
	case <-stdinDone:
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("native channel did not join after fixture shutdown")
		}
	case <-done: // A deliberate wire disconnect can end the native channel first.
		cancel()
	}
	if d.workspaceFilesChannel == nil {
		t.Fatal("native files channel was never initialized")
	}
	d.workspaceFilesChannel.mu.Lock()
	defer d.workspaceFilesChannel.mu.Unlock()
	if len(d.workspaceFilesChannel.pending)+len(d.workspaceFilesChannel.active)+len(d.workspaceFilesChannel.slots) != 0 {
		t.Fatal("native pending/active/worker slots did not reach zero")
	}
	t.Log("native pending=0 active=0 worker_slots=0; subprocess joined")
}
