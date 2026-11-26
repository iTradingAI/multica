package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// The fixture injects project lookup and daemon identity at the same seams
// used by the routers. Both WebSocket pumps and the actual NTFS worker run.
type windowsEditFixtureAuth struct {
	user       string
	snapshot   realtime.WorkspaceFilesSnapshot
	generation atomic.Int64
}

func (a *windowsEditFixtureAuth) IsMember(_ context.Context, user, workspace string) (bool, error) {
	return user == a.user && workspace == a.snapshot.WorkspaceID, nil
}
func (a *windowsEditFixtureAuth) Members(_ context.Context, ids []realtime.MembershipIdentity) (map[realtime.MembershipIdentity]bool, error) {
	members := make(map[realtime.MembershipIdentity]bool, len(ids))
	for _, id := range ids {
		members[id] = id.UserID == a.user && id.WorkspaceID == a.snapshot.WorkspaceID
	}
	return members, nil
}
func (a *windowsEditFixtureAuth) ResolveToken(_ context.Context, token string) (string, bool) {
	return a.user, token == "mul_fixture"
}
func (a *windowsEditFixtureAuth) AuthorizeWorkspaceFiles(_ context.Context, user, workspace string, entry protocol.WorkspaceFilesContext, resource string) (realtime.WorkspaceFilesSnapshot, string) {
	if user != a.user || workspace != a.snapshot.WorkspaceID || entry.Kind != "project" ||
		entry.ProjectID != a.snapshot.ProjectID || (resource != "" && resource != a.snapshot.ResourceID) {
		return realtime.WorkspaceFilesSnapshot{}, "forbidden"
	}
	s := a.snapshot
	s.BindingGeneration = a.generation.Load()
	return s, ""
}
func (a *windowsEditFixtureAuth) ListWorkspaceFilesResources(context.Context, string, string, protocol.WorkspaceFilesContext) ([]protocol.WorkspaceFilesResource, string) {
	return []protocol.WorkspaceFilesResource{{ResourceID: a.snapshot.ResourceID}}, ""
}

func TestWorkspaceFilesWindowsEditWebSocketFixture(t *testing.T) {
	fixture, file := newWindowsWorkspaceEditFixture(t)
	if err := os.WriteFile(file, []byte("one\r\ntwo\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := realtime.WorkspaceFilesSnapshot{WorkspaceID: uuid.NewString(), ProjectID: uuid.NewString(),
		ResourceID: uuid.NewString(), ResourceType: "local_directory", Root: fixture.RootPath, DaemonID: uuid.NewString()}
	a := &windowsEditFixtureAuth{user: uuid.NewString(), snapshot: s}
	a.generation.Store(1)
	rt := realtime.NewHub()
	rt.SetMembershipChecker(a)
	go rt.Run()
	t.Cleanup(rt.ShutdownWorkspaceFiles)
	dh := daemonws.NewHub()
	dh.SetWorkspaceFilesBridge(rt)
	rt.ConfigureWorkspaceFiles(true, true, a, dh)
	runtimeID := uuid.NewString()

	mux := http.NewServeMux()
	mux.HandleFunc("/browser", func(w http.ResponseWriter, r *http.Request) {
		realtime.HandleWebSocket(rt, a, a, nil, w, r)
	})
	mux.HandleFunc("/daemon", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		dh.HandleWebSocket(w, r, daemonws.ClientIdentity{
			DaemonID: s.DaemonID, WorkspaceID: s.WorkspaceID,
			Capabilities:  r.Header.Get("X-Client-Capabilities"),
			RuntimeIDs:    []string{runtimeID},
			RuntimeLeases: map[string]*daemonws.RuntimeLease{runtimeID: daemonws.NewRuntimeLease(s.WorkspaceID, "online", time.Now(), true)},
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	connect := func(endpoint string, headers http.Header) *websocket.Conn {
		t.Helper()
		conn, _, err := websocket.DefaultDialer.Dial(url+endpoint, headers)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	wire := connect("/daemon", http.Header{
		"Authorization":         {"Bearer fixture"},
		"X-Client-Capabilities": {daemonClientCapabilities()},
	})
	channel := newWorkspaceFilesChannel()
	var write sync.Mutex
	channel.attach(func(_ context.Context, frame []byte) bool {
		write.Lock()
		defer write.Unlock()
		return wire.WriteMessage(websocket.TextMessage, frame) == nil
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer channel.attach(nil)
		for {
			var frame protocol.Message
			if wire.ReadJSON(&frame) != nil {
				return
			}
			channel.HandleMessage(frame.Type, frame.Payload)
		}
	}()
	t.Cleanup(func() { _ = wire.Close(); <-done })
	wait := time.Now().Add(2 * time.Second)
	for {
		if _, code := dh.SelectWorkspaceFilesEditTarget(s.DaemonID, s.WorkspaceID, uuid.NewString()); code == "" {
			break
		} else if time.Now().After(wait) {
			t.Fatalf("Windows daemon edit capability unavailable: %s", code)
		}
		time.Sleep(time.Millisecond)
	}
	send := func(conn *websocket.Conn, event string, payload any) protocol.Message {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.WriteJSON(protocol.Message{Type: event, Payload: raw}); err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var response protocol.Message
		if err := conn.ReadJSON(&response); err != nil {
			t.Fatalf("%s: %v", event, err)
		}
		return response
	}
	browser := func(token, expected string) *websocket.Conn {
		t.Helper()
		conn := connect("/browser?workspace_id="+s.WorkspaceID, nil)
		if got := send(conn, "auth", map[string]string{"token": token}); got.Type != expected {
			t.Fatalf("authentication type = %s, want %s", got.Type, expected)
		}
		return conn
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": a.user, "exp": time.Now().Add(time.Minute).Unix()}).SignedString(auth.JWTSecret())
	if err != nil {
		t.Fatal(err)
	}
	member := browser(token, "auth_ack")
	pat := browser("mul_fixture", "auth_ack")
	invalid := browser("mdt_fixture", "")
	if _, _, err := invalid.ReadMessage(); err == nil {
		t.Fatal("daemon credential was not disconnected from browser endpoint")
	}

	request := func(conn *websocket.Conn, event, path string, extra map[string]any) protocol.Message {
		t.Helper()
		fields := map[string]any{"client_req_id": uuid.NewString(), "resource_id": s.ResourceID, "path": path,
			"context": protocol.WorkspaceFilesContext{Kind: "project", ProjectID: s.ProjectID}}
		for k, v := range extra {
			fields[k] = v
		}
		return send(conn, event, fields)
	}
	wantType := func(frame protocol.Message, event string) {
		t.Helper()
		if frame.Type != event {
			t.Fatalf("event = %s, want %s: %s", frame.Type, event, frame.Payload)
		}
	}
	wantCode := func(frame protocol.Message, code string) {
		t.Helper()
		wantType(frame, protocol.EventWorkspaceFilesError)
		var p protocol.WorkspaceFilesClientErrorPayload
		if err := json.Unmarshal(frame.Payload, &p); err != nil || p.Code != code {
			t.Fatalf("code = %s, want %s: %v", p.Code, code, err)
		}
	}
	open := protocol.EventWorkspaceFilesEditOpen
	save := protocol.EventWorkspaceFilesSave
	wantType(send(member, protocol.EventWorkspaceFilesResources, map[string]any{
		"client_req_id": uuid.NewString(), "context": protocol.WorkspaceFilesContext{Kind: "project", ProjectID: s.ProjectID},
	}), protocol.EventWorkspaceFilesResourcesResult)
	wantType(request(member, protocol.EventWorkspaceFilesList, ".", nil), protocol.EventWorkspaceFilesListResult)
	wantType(request(member, protocol.EventWorkspaceFilesList, "editable", nil), protocol.EventWorkspaceFilesListResult)
	wantType(request(pat, protocol.EventWorkspaceFilesRead, fixture.Path, nil), protocol.EventWorkspaceFilesReadChunk)
	wantCode(request(member, open, fixture.Path, nil), "read_only")

	raw, _ := json.Marshal([]map[string]any{{"workspace_id": s.WorkspaceID,
		"project_id": s.ProjectID, "resource_id": s.ResourceID, "binding_generation": 1,
		"directory": fixture.Policy.Directory, "volume_guid": fixture.Policy.VolumeGUID, "directory_id": fixture.Policy.DirectoryID}})
	bindings, err := realtime.ParseWorkspaceFilesEditBindings(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	rt.ConfigureWorkspaceFilesEdit(bindings)
	opened := request(member, open, fixture.Path, nil)
	wantType(opened, protocol.EventWorkspaceFilesEditResult)
	var base protocol.WorkspaceFilesClientEditResultPayload
	if err := json.Unmarshal(opened.Payload, &base); err != nil || string(base.Data) != "one\ntwo\n" {
		t.Fatalf("open text = %q: %v", base.Data, err)
	}
	update := map[string]any{"binding_generation": base.BindingGeneration, "version": base.Version, "data": []byte("changed\ntwo\n")}
	wantCode(request(pat, save, fixture.Path, update), "forbidden")
	wantCode(request(member, save, fixture.Path, map[string]any{
		"binding_generation": base.BindingGeneration, "version": base.Version, "data": update["data"],
		"context": protocol.WorkspaceFilesContext{Kind: "project", ProjectID: uuid.NewString()},
	}), "forbidden")
	wantCode(request(member, save, "other/note.txt", update), "read_only")
	wantCode(request(member, save, fixture.Path, map[string]any{
		"binding_generation": 0, "version": base.Version, "data": update["data"],
	}), "invalid_request")
	wantCode(request(member, save, fixture.Path, map[string]any{
		"binding_generation": 1, "version": base.Version, "data": make([]byte, protocol.WorkspaceFilesEditMaxBytes+1),
	}), "too_large")
	a.generation.Store(2)
	wantCode(request(member, save, fixture.Path, update), "forbidden")
	a.generation.Store(1)
	wantType(request(member, save, fixture.Path, update), protocol.EventWorkspaceFilesEditResult)

	reopened := request(browser(token, "auth_ack"), open, fixture.Path, nil)
	wantType(reopened, protocol.EventWorkspaceFilesEditResult)
	var confirmed protocol.WorkspaceFilesClientEditResultPayload
	if err := json.Unmarshal(reopened.Payload, &confirmed); err != nil || string(confirmed.Data) != "changed\ntwo\n" {
		t.Fatalf("reopen text = %q: %v", confirmed.Data, err)
	}
	if disk, err := os.ReadFile(file); err != nil || string(disk) != "changed\r\ntwo\r\n" {
		t.Fatalf("direct disk read = %q: %v", disk, err)
	}
	wantCode(request(member, save, fixture.Path, update), "conflict")
	empty := map[string]any{"binding_generation": confirmed.BindingGeneration, "version": confirmed.Version, "data": []byte{}}
	wantType(request(member, save, fixture.Path, empty), protocol.EventWorkspaceFilesEditResult)
	if disk, err := os.ReadFile(file); err != nil || len(disk) != 0 {
		t.Fatalf("empty save disk = %q: %v", disk, err)
	}
}
