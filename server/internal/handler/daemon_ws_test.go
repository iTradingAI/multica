package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
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
