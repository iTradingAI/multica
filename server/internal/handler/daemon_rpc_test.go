package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// TestDaemonRPCHandler_TasksClaim pins the WS-first claim binding (MUL-4257):
// a tasks.claim RPC, driven with the WS connection's identity, reuses the HTTP
// claim handler and claims a queued task for the daemon's runtime.
func TestDaemonRPCHandler_TasksClaim(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	rt := createClaimReclaimRuntime(t, ctx, "WS claim rt")
	a, i := createClaimReclaimAgentAndIssue(t, ctx, rt, "WS claim agent")
	taskID := seedQueuedIssueTask(t, ctx, a, rt, i)

	identity := daemonws.ClientIdentity{
		DaemonID:     "ws-daemon",
		UserID:       testUserID,
		WorkspaceID:  testWorkspaceID,
		WorkspaceIDs: []string{testWorkspaceID},
		RuntimeIDs:   []string{rt},
		AuthPath:     middleware.DaemonAuthPathDaemonToken,
	}
	body, _ := json.Marshal(map[string]any{
		"daemon_id":   "ws-daemon",
		"runtime_ids": []string{rt},
		"max_tasks":   5,
	})

	status, respBody, err := testHandler.DaemonRPCHandler(ctx, identity, "tasks.claim", body)
	if err != nil {
		t.Fatalf("DaemonRPCHandler: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, string(respBody))
	}
	var resp batchClaimReceiptResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Tasks) != 1 || resp.Tasks[0].ID != taskID {
		t.Fatalf("claimed %+v, want the queued task %s", resp.Tasks, taskID)
	}
	if resp.Tasks[0].RuntimeID != rt {
		t.Fatalf("claimed runtime = %s, want %s", resp.Tasks[0].RuntimeID, rt)
	}

	var dbStatus string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&dbStatus); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if dbStatus != "dispatched" {
		t.Fatalf("task status = %s, want dispatched", dbStatus)
	}
}

// TestDaemonRPCHandler_TasksClaim_AccountIdentityClaimsAcrossWorkspaces proves
// that a PAT-authenticated machine keeps its account-wide workspace scope when
// the WebSocket RPC bridge invokes the batch claim handler. The first runtime
// is intentionally idle; the queued task is only on the second workspace.
func TestDaemonRPCHandler_TasksClaim_AccountIdentityClaimsAcrossWorkspaces(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	daemonID := "rpc-account-" + uuid.NewString()
	otherWorkspaceID := dbfx.Workspace(t, "RPC account workspace", "rpc-account-"+uuid.NewString(), testutil.Cols{
		"issue_prefix": "RAC",
	})
	dbfx.Member(t, otherWorkspaceID, testUserID, "owner")
	enrollAccountDaemonForRPCClaim(t, testWorkspaceID, daemonID)
	enrollAccountDaemonForRPCClaim(t, otherWorkspaceID, daemonID)

	firstRuntimeID := dbfx.Runtime(t, "RPC account first runtime", testutil.Cols{
		"daemon_id": daemonID,
		"owner_id":  testUserID,
	})
	secondRuntimeID := dbfx.Runtime(t, "RPC account second runtime", testutil.Cols{
		"workspace_id": otherWorkspaceID,
		"daemon_id":    daemonID,
		"owner_id":     testUserID,
		"runtime_mode": "local",
	})
	secondAgentID := dbfx.Agent(t, "RPC account second agent", secondRuntimeID, testutil.Cols{
		"workspace_id": otherWorkspaceID,
		"owner_id":     testUserID,
		"runtime_mode": "local",
	})
	secondIssueID := dbfx.Issue(t, "RPC account second issue", testutil.Cols{
		"workspace_id": otherWorkspaceID,
		"creator_id":   testUserID,
	})
	secondTaskID := dbfx.Task(t, secondAgentID, testutil.Cols{
		"runtime_id": secondRuntimeID,
		"issue_id":   secondIssueID,
	})

	identity := buildPATWebSocketIdentityForRPCClaim(t, []string{firstRuntimeID, secondRuntimeID})
	if identity.DaemonID != daemonID || identity.AuthPath != middleware.DaemonAuthPathPAT {
		t.Fatalf("identity = daemon %q auth path %q, want bound PAT daemon %q", identity.DaemonID, identity.AuthPath, daemonID)
	}
	if got := identity.AuthorizedWorkspaceIDs(); len(got) != 2 || got[0] != testWorkspaceID || got[1] != otherWorkspaceID {
		t.Fatalf("authorized workspaces = %v, want [%s %s]", got, testWorkspaceID, otherWorkspaceID)
	}

	body, err := json.Marshal(map[string]any{
		"daemon_id":   daemonID,
		"runtime_ids": []string{firstRuntimeID, secondRuntimeID},
		"max_tasks":   1,
	})
	if err != nil {
		t.Fatalf("marshal claim body: %v", err)
	}
	status, responseBody, err := testHandler.DaemonRPCHandler(ctx, identity, "tasks.claim", body)
	if err != nil {
		t.Fatalf("DaemonRPCHandler: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, responseBody)
	}
	var response batchClaimReceiptResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("decode claim response: %v", err)
	}
	if len(response.Tasks) != 1 || response.Tasks[0].ID != secondTaskID || response.Tasks[0].RuntimeID != secondRuntimeID {
		t.Fatalf("claimed tasks = %+v, want queued second-workspace task %s on %s", response.Tasks, secondTaskID, secondRuntimeID)
	}
}

// TestDaemonRPCHandler_TasksClaim_RejectsDifferentBoundMachine verifies the
// connection's enrolled machine cannot relabel a claim request for a peer,
// regardless of which auth path produced the bound WebSocket identity.
func TestDaemonRPCHandler_TasksClaim_RejectsDifferentBoundMachine(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := dbfx.Runtime(t, "RPC bound machine runtime", testutil.Cols{
		"daemon_id": "rpc-bound-daemon",
	})
	agentID := dbfx.Agent(t, "RPC bound machine agent", runtimeID)
	issueID := dbfx.Issue(t, "RPC bound machine issue")
	taskID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "issue_id": issueID})
	for _, authPath := range []string{
		middleware.DaemonAuthPathPAT,
		middleware.DaemonAuthPathDaemonToken,
		"",
	} {
		t.Run(authPathOrUnknown(authPath), func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"daemon_id":   "rpc-other-daemon",
				"runtime_ids": []string{runtimeID},
				"max_tasks":   1,
			})
			if err != nil {
				t.Fatalf("marshal claim body: %v", err)
			}
			status, responseBody, err := testHandler.DaemonRPCHandler(ctx, boundRPCClaimIdentity(runtimeID, authPath), "tasks.claim", body)
			if err != nil {
				t.Fatalf("DaemonRPCHandler: %v", err)
			}
			if status != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", status, responseBody)
			}
			assertRPCClaimTaskStatus(t, ctx, taskID, "queued")
		})
	}
}

// TestDaemonRPCHandler_TasksClaim_BoundMachineKeepsMissingDaemonIDAsBadRequest
// keeps malformed and empty daemon IDs on the canonical handler's 400 path;
// only a supplied, different ID is a connection-binding violation.
func TestDaemonRPCHandler_TasksClaim_BoundMachineKeepsMissingDaemonIDAsBadRequest(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := dbfx.Runtime(t, "RPC malformed daemon runtime", testutil.Cols{
		"daemon_id": "rpc-bound-daemon",
	})
	agentID := dbfx.Agent(t, "RPC malformed daemon agent", runtimeID)
	issueID := dbfx.Issue(t, "RPC malformed daemon issue")
	taskID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "issue_id": issueID})

	for _, tc := range []struct {
		name string
		body json.RawMessage
	}{
		{name: "missing", body: json.RawMessage(`{"runtime_ids":["` + runtimeID + `"],"max_tasks":1}`)},
		{name: "empty", body: json.RawMessage(`{"daemon_id":"","runtime_ids":["` + runtimeID + `"],"max_tasks":1}`)},
		{name: "null", body: json.RawMessage(`{"daemon_id":null,"runtime_ids":["` + runtimeID + `"],"max_tasks":1}`)},
		{name: "invalid JSON", body: json.RawMessage(`{"daemon_id":`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, responseBody, err := testHandler.DaemonRPCHandler(ctx, boundRPCClaimIdentity(runtimeID, middleware.DaemonAuthPathPAT), "tasks.claim", tc.body)
			if err != nil {
				t.Fatalf("DaemonRPCHandler: %v", err)
			}
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", status, responseBody)
			}
			assertRPCClaimTaskStatus(t, ctx, taskID, "queued")
		})
	}
}

func boundRPCClaimIdentity(runtimeID, authPath string) daemonws.ClientIdentity {
	return daemonws.ClientIdentity{
		DaemonID:     "rpc-bound-daemon",
		UserID:       testUserID,
		WorkspaceID:  testWorkspaceID,
		WorkspaceIDs: []string{testWorkspaceID},
		RuntimeIDs:   []string{runtimeID},
		AuthPath:     authPath,
	}
}

func authPathOrUnknown(authPath string) string {
	if authPath == "" {
		return "unknown auth path"
	}
	return authPath
}

func enrollAccountDaemonForRPCClaim(t *testing.T, workspaceID, daemonID string) {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.DaemonRegister(w, newRequest(http.MethodPost, "/api/daemon/register", map[string]any{
		"workspace_id": workspaceID,
		"daemon_id":    daemonID,
		"runtimes":     []map[string]string{{"type": "rpc-claim-enrollment"}},
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("enroll account daemon in workspace %s: status=%d body=%s", workspaceID, w.Code, w.Body.String())
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE workspace_id = $1 AND daemon_id = $2`, workspaceID, daemonID)
		testPool.Exec(context.Background(), `DELETE FROM daemon_registration_identity WHERE workspace_id = $1 AND daemon_id = $2`, workspaceID, daemonID)
	})
}

func buildPATWebSocketIdentityForRPCClaim(t *testing.T, runtimeIDs []string) daemonws.ClientIdentity {
	t.Helper()
	rawPAT, _ := insertTestPAT(t, time.Now().Add(time.Hour))
	req := newRequest(http.MethodGet, "/api/daemon/ws", nil)
	req.Header.Set("Authorization", "Bearer "+rawPAT)
	w := httptest.NewRecorder()
	var identity daemonws.ClientIdentity
	var ok bool
	middleware.DaemonAuth(testHandler.Queries, nil, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok = testHandler.buildDaemonWebSocketIdentity(w, r, runtimeIDs, requestUserID(r))
	})).ServeHTTP(w, req)
	if w.Code != http.StatusOK || !ok {
		t.Fatalf("build PAT websocket identity: status=%d body=%s", w.Code, w.Body.String())
	}
	return identity
}

func assertRPCClaimTaskStatus(t *testing.T, ctx context.Context, taskID, want string) {
	t.Helper()
	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	if status != want {
		t.Fatalf("task status = %q, want %q", status, want)
	}
}

// TestDaemonRPCHandler_UnknownMethod returns 404 for an unknown method.
func TestDaemonRPCHandler_UnknownMethod(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	status, _, err := testHandler.DaemonRPCHandler(context.Background(),
		daemonws.ClientIdentity{DaemonID: "ws-daemon"}, "does.not.exist", nil)
	if status != http.StatusNotFound || err == nil {
		t.Fatalf("status=%d err=%v, want 404 + error", status, err)
	}
}
