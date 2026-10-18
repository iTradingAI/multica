package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestDaemonWebSocketMissingHubIsInternalError(t *testing.T) {
	h := &Handler{}
	w := httptest.NewRecorder()
	h.DaemonWebSocket(w, httptest.NewRequest(http.MethodGet, "/api/daemon/ws", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["code"] != "daemon_websocket_misconfigured" {
		t.Fatalf("code = %q, want daemon_websocket_misconfigured", body["code"])
	}
}

func TestBuildDaemonWebSocketIdentitySeedsBatchRuntimeLeases(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	const daemonID = "lease-daemon"
	runtimeIDs := []string{
		dbfx.Runtime(t, "WS lease runtime 1", testutil.Cols{
			"workspace_id": testWorkspaceID,
			"daemon_id":    daemonID,
			"provider":     "ws-lease-1",
			"device_info":  "WS lease runtime 1",
		}),
		dbfx.Runtime(t, "WS lease runtime 2", testutil.Cols{
			"workspace_id": testWorkspaceID,
			"daemon_id":    daemonID,
			"provider":     "ws-lease-2",
			"device_info":  "WS lease runtime 2",
		}),
	}
	req := newDaemonTokenRequest(http.MethodGet, "/api/daemon/ws", nil, testWorkspaceID, daemonID)
	req.Header.Set("X-Client-Version", "0.9.0")
	w := httptest.NewRecorder()

	identity, ok := testHandler.buildDaemonWebSocketIdentity(w, req, runtimeIDs, "")
	if !ok {
		t.Fatalf("buildDaemonWebSocketIdentity rejected valid runtimes: %d %s", w.Code, w.Body.String())
	}
	if identity.DaemonID != daemonID || identity.WorkspaceID != testWorkspaceID {
		t.Fatalf("identity scope = daemon %q workspace %q", identity.DaemonID, identity.WorkspaceID)
	}
	if identity.ClientVersion != "0.9.0" {
		t.Fatalf("client version = %q, want 0.9.0", identity.ClientVersion)
	}
	if len(identity.RuntimeLeases) != len(runtimeIDs) {
		t.Fatalf("runtime leases = %d, want %d", len(identity.RuntimeLeases), len(runtimeIDs))
	}
	for _, runtimeID := range runtimeIDs {
		lease := identity.RuntimeLeases[runtimeID]
		if lease == nil {
			t.Fatalf("missing lease for runtime %s", runtimeID)
		}
		state := lease.Snapshot()
		if state.WorkspaceID != testWorkspaceID || state.Status != "online" || !state.LastSeenAtValid {
			t.Fatalf("lease %s = %+v", runtimeID, state)
		}
		if time.Since(state.LastSeenAt) > time.Minute {
			t.Fatalf("lease %s last_seen_at unexpectedly stale: %s", runtimeID, state.LastSeenAt)
		}
	}
}

func TestBuildDaemonWebSocketIdentityFailsClosedForMissingRuntime(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	req := newDaemonTokenRequest(http.MethodGet, "/api/daemon/ws", nil, testWorkspaceID, "lease-daemon")
	w := httptest.NewRecorder()

	if _, ok := testHandler.buildDaemonWebSocketIdentity(w, req, []string{uuid.NewString()}, ""); ok {
		t.Fatal("missing runtime unexpectedly authorized")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

// TestBuildDaemonWebSocketIdentityCapturesDaemonAuthPath ensures the WebSocket
// identity preserves the credential kind that DaemonAuth actually accepted.
func TestBuildDaemonWebSocketIdentityCapturesDaemonAuthPath(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	const daemonID = "ws-auth-path-daemon"
	runtimeID := dbfx.Runtime(t, "WS auth path runtime", testutil.Cols{
		"daemon_id": daemonID,
	})
	rawDaemonToken := "mdt_ws_auth_path_" + uuid.NewString()
	dbfx.Insert(t, "daemon_token", testutil.Cols{
		"token_hash":   auth.HashToken(rawDaemonToken),
		"workspace_id": testWorkspaceID,
		"daemon_id":    daemonID,
		"expires_at":   testutil.Raw("now() + interval '1 hour'"),
	})

	for _, credential := range []struct {
		name     string
		token    string
		userID   string
		wantPath string
	}{
		{name: "daemon token", token: rawDaemonToken, wantPath: middleware.DaemonAuthPathDaemonToken},
		{name: "PAT", token: mustInsertDaemonWebSocketPAT(t), userID: testUserID, wantPath: middleware.DaemonAuthPathPAT},
	} {
		t.Run(credential.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/daemon/ws", nil)
			req.Header.Set("Authorization", "Bearer "+credential.token)
			w := httptest.NewRecorder()
			var identity daemonws.ClientIdentity
			var ok bool
			middleware.DaemonAuth(testHandler.Queries, nil, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				identity, ok = testHandler.buildDaemonWebSocketIdentity(w, r, []string{runtimeID}, requestUserID(r))
			})).ServeHTTP(w, req)
			if w.Code != http.StatusOK || !ok {
				t.Fatalf("build identity: status=%d body=%s", w.Code, w.Body.String())
			}
			if identity.AuthPath != credential.wantPath {
				t.Fatalf("auth path = %q, want %q", identity.AuthPath, credential.wantPath)
			}
			if credential.userID != "" && identity.UserID != credential.userID {
				t.Fatalf("identity user = %q, want %q", identity.UserID, credential.userID)
			}
		})
	}
}

// TestBuildDaemonWebSocketIdentityRejectsDaemonTokenCrossWorkspaceRuntime
// exercises a real mdt_ middleware path: a token for workspace A cannot
// authorize a runtime in workspace B, even when both name the same machine.
func TestBuildDaemonWebSocketIdentityRejectsDaemonTokenCrossWorkspaceRuntime(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	otherWorkspaceID := dbfx.Workspace(t, "WS token foreign workspace", "ws-token-foreign-"+uuid.NewString(), testutil.Cols{
		"issue_prefix": "WTF",
	})
	const daemonID = "ws-token-cross-workspace-daemon"
	foreignRuntimeID := dbfx.Runtime(t, "WS token foreign runtime", testutil.Cols{
		"workspace_id": otherWorkspaceID,
		"daemon_id":    daemonID,
	})
	rawDaemonToken := "mdt_ws_cross_workspace_" + uuid.NewString()
	dbfx.Insert(t, "daemon_token", testutil.Cols{
		"token_hash":   auth.HashToken(rawDaemonToken),
		"workspace_id": testWorkspaceID,
		"daemon_id":    daemonID,
		"expires_at":   testutil.Raw("now() + interval '1 hour'"),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/daemon/ws", nil)
	req.Header.Set("Authorization", "Bearer "+rawDaemonToken)
	w := httptest.NewRecorder()
	accepted := false
	middleware.DaemonAuth(testHandler.Queries, nil, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, accepted = testHandler.buildDaemonWebSocketIdentity(w, r, []string{foreignRuntimeID}, requestUserID(r))
	})).ServeHTTP(w, req)
	if accepted {
		t.Fatal("daemon token accepted a cross-workspace runtime")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

// TestBuildDaemonWebSocketIdentityRejectsPATWorkspaceWithoutMembership keeps
// account credentials bound to memberships rather than to their runtime owner.
func TestBuildDaemonWebSocketIdentityRejectsPATWorkspaceWithoutMembership(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	otherWorkspaceID := dbfx.Workspace(t, "WS PAT foreign workspace", "ws-pat-foreign-"+uuid.NewString(), testutil.Cols{
		"issue_prefix": "WPF",
	})
	foreignRuntimeID := dbfx.Runtime(t, "WS PAT foreign runtime", testutil.Cols{
		"workspace_id": otherWorkspaceID,
		"daemon_id":    "ws-pat-foreign-daemon",
		"owner_id":     testUserID,
	})
	rawPAT := mustInsertDaemonWebSocketPAT(t)
	req := httptest.NewRequest(http.MethodGet, "/api/daemon/ws", nil)
	req.Header.Set("Authorization", "Bearer "+rawPAT)
	w := httptest.NewRecorder()
	accepted := false
	middleware.DaemonAuth(testHandler.Queries, nil, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, accepted = testHandler.buildDaemonWebSocketIdentity(w, r, []string{foreignRuntimeID}, requestUserID(r))
	})).ServeHTTP(w, req)
	if accepted {
		t.Fatal("PAT accepted a runtime in an unjoined workspace")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func mustInsertDaemonWebSocketPAT(t *testing.T) string {
	t.Helper()
	rawPAT, _ := insertTestPAT(t, time.Now().Add(time.Hour))
	return rawPAT
}

func TestBuildDaemonWebSocketIdentityBindsOnlyOwnedUnambiguousAccountRuntimes(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	otherUser := dbfx.Insert(t, "user", testutil.Cols{"name": "Other daemon owner", "email": "ws-owner-" + uuid.NewString() + "@example.test"})
	for _, tc := range []struct {
		name       string
		daemons    []any
		owners     []any
		want       string
		unenrolled bool
	}{
		{"owned single daemon", []any{"pat-daemon", "pat-daemon"}, []any{testUserID, testUserID}, "pat-daemon", false},
		{"other member runtime", []any{"pat-daemon"}, []any{otherUser}, "", false},
		{"mixed ownership", []any{"pat-daemon", "pat-daemon"}, []any{testUserID, otherUser}, "", false},
		{"mixed daemon identities", []any{"pat-daemon", "other-daemon"}, []any{testUserID, testUserID}, "", false},
		{"missing daemon identity", []any{nil}, []any{testUserID}, "", false},
		{"missing owner", []any{"pat-daemon"}, []any{nil}, "", false},
		{"owned rows without enrollment", []any{"pat-daemon"}, []any{testUserID}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			suffix := "-" + uuid.NewString()
			if tc.want != "" {
				tc.want += suffix
			}
			for i, raw := range tc.daemons {
				if raw == nil {
					continue
				}
				id := raw.(string) + suffix
				tc.daemons[i] = id
				if tc.unenrolled {
					continue
				}
				w := httptest.NewRecorder()
				testHandler.DaemonRegister(w, newRequest(http.MethodPost, "/api/daemon/register", map[string]any{
					"workspace_id": testWorkspaceID, "daemon_id": id,
					"runtimes": []map[string]string{{"type": "enrollment-proof"}},
				}))
				if w.Code != http.StatusOK {
					t.Fatalf("enrollment status=%d: %s", w.Code, w.Body.String())
				}
			}
			var ids []string
			for i, daemonID := range tc.daemons {
				ids = append(ids, dbfx.Runtime(t, "Account WS binding", testutil.Cols{
					"workspace_id": testWorkspaceID, "daemon_id": daemonID, "owner_id": tc.owners[i],
					"provider": "ws-binding-" + uuid.NewString(),
				}))
			}
			req := newRequest(http.MethodGet, "/api/daemon/ws", nil)
			// A client-supplied daemon hint is never proof of its identity.
			req.Header.Set("X-Daemon-ID", "forged-daemon")
			w := httptest.NewRecorder()
			identity, ok := testHandler.buildDaemonWebSocketIdentity(w, req, ids, testUserID)
			if !ok {
				t.Fatalf("legacy account connection rejected: %d %s", w.Code, w.Body.String())
			}
			if identity.DaemonID != tc.want {
				t.Fatalf("bound daemon = %q, want %q", identity.DaemonID, tc.want)
			}
		})
	}
	identity, ok := testHandler.buildDaemonWebSocketIdentity(httptest.NewRecorder(), newRequest(http.MethodGet, "/api/daemon/ws", nil), nil, testUserID)
	if !ok || identity.DaemonID != "" {
		t.Fatal("account-only connection gained a daemon identity")
	}
}

func TestBuildDaemonWebSocketIdentityRejectsWrongDaemon(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	runtimeID := dbfx.Runtime(t, "WS lease daemon scope", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"daemon_id":    "owner-daemon",
		"device_info":  "WS lease daemon scope",
	})
	req := newDaemonTokenRequest(http.MethodGet, "/api/daemon/ws", nil, testWorkspaceID, "other-daemon")
	w := httptest.NewRecorder()

	if _, ok := testHandler.buildDaemonWebSocketIdentity(w, req, []string{runtimeID}, ""); ok {
		t.Fatal("runtime owned by another daemon unexpectedly authorized")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestBuildDaemonWebSocketIdentityRejectsCrossWorkspaceRuntime(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	workspaceID := dbfx.Insert(t, "workspace", testutil.Cols{
		"name":         "WS Lease Other Workspace",
		"slug":         "ws-lease-" + uuid.NewString(),
		"description":  "Cross-workspace lease test",
		"issue_prefix": "HWL",
	})
	runtimeID := dbfx.Runtime(t, "WS lease cross workspace", testutil.Cols{
		"workspace_id": workspaceID,
		"daemon_id":    "lease-daemon",
		"device_info":  "WS lease cross workspace",
	})
	req := newDaemonTokenRequest(http.MethodGet, "/api/daemon/ws", nil, testWorkspaceID, "lease-daemon")
	w := httptest.NewRecorder()

	if _, ok := testHandler.buildDaemonWebSocketIdentity(w, req, []string{runtimeID}, ""); ok {
		t.Fatal("cross-workspace runtime unexpectedly authorized")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}
