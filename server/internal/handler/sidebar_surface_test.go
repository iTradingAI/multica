package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

func TestSidebarSurfaceDisableAndExpiredURLRecovery(t *testing.T) {
	withPluginsV1Flag(t, testHandler, true)
	cleanupPluginInstallations(t)
	previousHost := testHandler.PluginService.Host
	testHandler.PluginService.Host = plugincontract.HostCapabilities()
	t.Cleanup(func() { testHandler.PluginService.Host = previousHost })
	previousOrigin, previousTokens := testHandler.cfg.PluginSurfaceOrigin, testHandler.PluginSurfaceTokens
	testHandler.cfg.PluginSurfaceOrigin = "https://plugin-content.example.test"
	testHandler.PluginSurfaceTokens, _ = NewPluginSurfaceTokenBox(bytes.Repeat([]byte{7}, 32))
	t.Cleanup(func() {
		testHandler.cfg.PluginSurfaceOrigin = previousOrigin
		testHandler.PluginSurfaceTokens = previousTokens
	})
	manifest := strings.Replace(packageManifest("1.0.0"), `"type": "issue_panel"`, `"type": "sidebar_panel"`, 1)
	// A mixed installation must be disabled as a whole. No surface identity
	// exists in the current Action authorization contract.
	manifest = strings.Replace(manifest, `"surfaces": [`, `"surfaces": [{"key":"issue","type":"issue_panel","name":"Issue","entry":"ui/main.js"},`, 1)
	published := publishUploadedBundle(t, manifest, "console.log('sidebar fixture');")
	installationID := installPublishedVersion(t, published.Versions[0].ID)
	unaffected := publishUploadedBundle(t, strings.Replace(packageManifest("1.0.0"), "com.example.published", "com.example.unaffected", 1), "console.log('unaffected');")
	unaffectedID := installPublishedVersion(t, unaffected.Versions[0].ID)
	launch := func(id, key string) (*httptest.ResponseRecorder, pluginSurfaceLaunch) {
		t.Helper()
		response := httptest.NewRecorder()
		testHandler.GetPluginSurfaceLaunch(response, pluginHandlerRequest(http.MethodGet, "/launch", nil, map[string]string{"id": testWorkspaceID, "installationId": id, "surfaceKey": key}))
		var result pluginSurfaceLaunch
		if response.Code == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return response, result
	}
	serve := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		request := pluginHandlerRequest(http.MethodGet, "/plugin-surfaces/fixture", nil, map[string]string{"token": token})
		request.Host = "plugin-content.example.test"
		testHandler.ServePluginSurface(response, request)
		return response
	}
	response, initial := launch(installationID, "hello")
	if response.Code != http.StatusOK {
		t.Fatalf("production sidebar launch status=%d", response.Code)
	}
	token := initial.URL[strings.LastIndex(initial.URL, "/")+1:]
	if response := serve(token); response.Code != http.StatusOK {
		t.Fatalf("initial Serve status=%d", response.Code)
	}
	params := map[string]string{"id": testWorkspaceID, "installationId": installationID}
	disabled := httptest.NewRecorder()
	testHandler.DisablePlugin(disabled, pluginHandlerRequest(http.MethodPost, "/disable", nil, params))
	if disabled.Code != http.StatusOK {
		t.Fatalf("disable status=%d", disabled.Code)
	}
	for _, key := range []string{"hello", "issue"} {
		if response, _ := launch(installationID, key); response.Code != http.StatusForbidden {
			t.Fatalf("disabled %s launch status=%d", key, response.Code)
		}
	}
	if response, _ := launch(unaffectedID, "hello"); response.Code != http.StatusOK {
		t.Fatalf("unaffected installation launch status=%d", response.Code)
	}
	if response := serve(token); response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "multica-surface-code") {
		t.Fatalf("old URL after disable status=%d", response.Code)
	}
	issueID := createTestIssue(t, "Sidebar disabled Action", "todo", "none")
	action := httptest.NewRecorder()
	testHandler.GetPluginIssue(action, pluginActionRequest(http.MethodGet, "/issues", installationID, nil, map[string]string{"issue_ref": issueID}))
	if action.Code != http.StatusForbidden {
		t.Fatalf("existing bridge Action status=%d", action.Code)
	}
	claims, err := testHandler.openPluginSurfaceToken(token)
	if err != nil {
		t.Fatal("could not open fixture token")
	}
	claims.ExpiresAt = time.Now().Add(-time.Second).Unix()
	expired, err := testHandler.mintPluginSurfaceToken(claims)
	if err != nil {
		t.Fatal("could not mint expired fixture")
	}
	enabled := httptest.NewRecorder()
	testHandler.EnablePlugin(enabled, pluginHandlerRequest(http.MethodPost, "/enable", nil, params))
	if enabled.Code != http.StatusOK {
		t.Fatalf("enable status=%d", enabled.Code)
	}
	if response := serve(expired); response.Code != http.StatusNotFound {
		t.Fatalf("expired URL after recovery status=%d", response.Code)
	}
	response, fresh := launch(installationID, "hello")
	if response.Code != http.StatusOK {
		t.Fatalf("recovered launch status=%d", response.Code)
	}
	if fresh.BridgeToken == initial.BridgeToken {
		t.Fatal("recovery reused the old bridge proof")
	}
	if response := serve(fresh.URL[strings.LastIndex(fresh.URL, "/")+1:]); response.Code != http.StatusOK {
		t.Fatalf("recovered Serve status=%d", response.Code)
	}
}

func TestSidebarActionRechecksMembershipAndProjectContext(t *testing.T) {
	installationID := installPluginForAction(t, []string{"issues:read"})
	userID := dbfx.Insert(t, "user", testutil.Cols{"name": "Sidebar member", "email": "sidebar-member-" + testWorkspaceID + "@example.test"})
	memberID := dbfx.Insert(t, "member", testutil.Cols{"workspace_id": testWorkspaceID, "user_id": userID, "role": "member"})
	request := pluginActionRequest(http.MethodGet, "/context", installationID, nil, nil)
	request.Header.Set("X-User-ID", userID)
	response := httptest.NewRecorder()
	testHandler.GetPluginContext(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("project-entry context status=%d", response.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["issue"] != nil || payload["project"] != nil {
		t.Fatal("project entry fabricated an Issue or expanded project authority")
	}
	dbfx.Exec(t, "DELETE FROM member WHERE id = $1", memberID)
	response = httptest.NewRecorder()
	testHandler.GetPluginContext(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("revoked member context status=%d", response.Code)
	}
}
