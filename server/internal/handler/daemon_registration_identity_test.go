package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// These DB-backed handler tests supplement the real authenticated HTTP/WS
// adversarial test. Their X-User-ID requests alone do not prove authentication.
func TestDaemonRegisterMachineEnrollmentCannotTransfer(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	other := dbfx.Insert(t, "user", testutil.Cols{"email": "enrollment-" + uuid.NewString() + "@example.test", "name": "Other member"})
	dbfx.Insert(t, "member", testutil.Cols{"workspace_id": testWorkspaceID, "user_id": other, "role": "member"})
	for _, variant := range []string{"new-provider", "existing-provider", "failed-profile"} {
		t.Run(variant, func(t *testing.T) {
			daemonID := uuid.NewString()
			body := map[string]any{"workspace_id": testWorkspaceID, "daemon_id": daemonID,
				"runtimes": []map[string]string{{"type": "enrollment-a"}}}
			first := testutil.Call(t, testHandler.DaemonRegister, newRequest("POST", "/api/daemon/register", body)).Want(http.StatusOK)
			var response struct {
				Runtimes []struct {
					ID string `json:"id"`
				} `json:"runtimes"`
			}
			first.JSON(&response)
			switch variant {
			case "new-provider":
				body["runtimes"] = []map[string]string{{"type": "enrollment-b"}}
			case "failed-profile":
				delete(body, "runtimes")
				body["failed_profiles"] = []map[string]string{{"type": "enrollment-b", "error": "controlled"}}
			}
			testutil.Call(t, testHandler.DaemonRegister, newRequestAs(other, "POST", "/api/daemon/register", body)).Want(http.StatusConflict)
			var owner string
			var enrolled bool
			dbfx.QueryRow(t, `SELECT owner_id::text, account_enrolled FROM daemon_registration_identity WHERE workspace_id=$1 AND daemon_id=$2`, testWorkspaceID, daemonID).Scan(&owner, &enrolled)
			if owner != testUserID || !enrolled {
				t.Fatalf("machine enrollment changed: owner=%s enrolled=%v", owner, enrolled)
			}
			var count, ownCount int
			dbfx.QueryRow(t, `SELECT count(*), count(*) FILTER (WHERE owner_id=$3) FROM agent_runtime WHERE workspace_id=$1 AND daemon_id=$2`, testWorkspaceID, daemonID, testUserID).Scan(&count, &ownCount)
			if count != 1 || ownCount != 1 {
				t.Fatalf("provider rows changed: count=%d owned=%d", count, ownCount)
			}
			// Moving a provider does not release the canonical machine namespace.
			dbfx.Exec(t, `UPDATE agent_runtime SET daemon_id=$2 WHERE id=$1`, response.Runtimes[0].ID, uuid.NewString())
			testutil.Call(t, testHandler.DaemonRegister, newRequestAs(other, "POST", "/api/daemon/register", body)).Want(http.StatusConflict)
		})
	}
}

func TestDaemonRegisterTokenCannotChangeMachineScopeOrGrantAccountProof(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	daemonID := uuid.NewString()
	body := map[string]any{"workspace_id": testWorkspaceID, "daemon_id": daemonID,
		"runtimes": []map[string]string{{"type": "token-enrollment"}}}
	testutil.Call(t, testHandler.DaemonRegister, newDaemonTokenRequest("POST", "/api/daemon/register", body, testWorkspaceID, uuid.NewString())).Want(http.StatusNotFound)
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM daemon_registration_identity WHERE workspace_id=$1 AND daemon_id=$2`, testWorkspaceID, daemonID).Scan(&count)
	if count != 0 {
		t.Fatal("mismatched token enrolled a machine")
	}
	w := testutil.Call(t, testHandler.DaemonRegister, newDaemonTokenRequest("POST", "/api/daemon/register", body, testWorkspaceID, daemonID)).Want(http.StatusOK)
	var response struct {
		Runtimes []struct {
			ID string `json:"id"`
		} `json:"runtimes"`
	}
	w.JSON(&response)
	testutil.Call(t, testHandler.DaemonRegister, newRequest("POST", "/api/daemon/register", body)).Want(http.StatusConflict)
	identity, ok := testHandler.buildDaemonWebSocketIdentity(httptest.NewRecorder(), newDaemonTokenRequest("GET", "/api/daemon/ws", nil, testWorkspaceID, daemonID), []string{response.Runtimes[0].ID}, "")
	if !ok || identity.DaemonID != daemonID {
		t.Fatal("valid daemon-token machine no longer binds")
	}
}

func TestDaemonRegisterLegacyReservationRequiresAuthenticatedEnrollment(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	daemonID := uuid.NewString()
	runtimeID := dbfx.Runtime(t, "Pre-upgrade enrollment", testutil.Cols{"daemon_id": daemonID, "owner_id": testUserID, "provider": "legacy-enrollment"})
	// Match migration 565: reserve the old scope, without granting WS proof.
	dbfx.Exec(t, `INSERT INTO daemon_registration_identity (workspace_id,daemon_id,owner_id) VALUES ($1,$2,$3)`, testWorkspaceID, daemonID, testUserID)
	before, ok := testHandler.buildDaemonWebSocketIdentity(httptest.NewRecorder(), newRequest("GET", "/api/daemon/ws", nil), []string{runtimeID}, testUserID)
	if !ok || before.DaemonID != "" {
		t.Fatal("legacy runtime snapshot became machine proof without registration")
	}
	body := map[string]any{"workspace_id": testWorkspaceID, "daemon_id": daemonID, "runtimes": []map[string]string{{"type": "legacy-enrollment"}}}
	testutil.Call(t, testHandler.DaemonRegister, newRequest("POST", "/api/daemon/register", body)).Want(http.StatusOK)
	after, ok := testHandler.buildDaemonWebSocketIdentity(httptest.NewRecorder(), newRequest("GET", "/api/daemon/ws", nil), []string{runtimeID}, testUserID)
	if !ok || after.DaemonID != daemonID {
		t.Fatal("authenticated owner could not activate reserved machine")
	}
	for _, reserved := range []bool{false, true} {
		ambiguousID := uuid.NewString()
		dbfx.Runtime(t, "Ownerless legacy", testutil.Cols{"daemon_id": ambiguousID, "owner_id": nil, "provider": "legacy-ambiguous"})
		if reserved {
			dbfx.Exec(t, `INSERT INTO daemon_registration_identity (workspace_id,daemon_id) VALUES ($1,$2)`, testWorkspaceID, ambiguousID)
		}
		body["daemon_id"] = ambiguousID
		testutil.Call(t, testHandler.DaemonRegister, newRequest("POST", "/api/daemon/register", body)).Want(http.StatusConflict)
	}
}

func TestDaemonRegisterLegacyHintCannotMergeForeignRuntime(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	other := dbfx.Insert(t, "user", testutil.Cols{"email": "legacy-owner-" + uuid.NewString() + "@example.test", "name": "Foreign owner"})
	legacyID, newID := uuid.NewString(), uuid.NewString()
	foreign := dbfx.Runtime(t, "Foreign legacy", testutil.Cols{"daemon_id": legacyID, "owner_id": other, "provider": "claude"})
	body := map[string]any{"workspace_id": testWorkspaceID, "daemon_id": newID, "legacy_daemon_ids": []string{legacyID}, "runtimes": []map[string]string{{"type": "claude"}}}
	testutil.Call(t, testHandler.DaemonRegister, newRequest("POST", "/api/daemon/register", body)).Want(http.StatusOK)
	var actualOwner, actualDaemon string
	dbfx.QueryRow(t, `SELECT owner_id::text, daemon_id FROM agent_runtime WHERE id=$1`, foreign).Scan(&actualOwner, &actualDaemon)
	if actualOwner != other || actualDaemon != legacyID {
		t.Fatal("client legacy hint changed a foreign runtime")
	}
}
