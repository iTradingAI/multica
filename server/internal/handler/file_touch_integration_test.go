package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/filetouch"
)

// C-only entry: never run the handler suite locally. Its existing TestMain
// seeds and cleans a dedicated DB fixture before tests run.
func requireFileTouchIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("MULTICA_FILE_TOUCH_TESTS_APPROVED") != "true" || testHandler == nil {
		t.Skip("C window and dedicated database required")
	}
	if !filetouch.Ready(context.Background(), testPool) {
		t.Fatal("full migration readiness required")
	}
	t.Setenv("MULTICA_FILE_TOUCHES_ENABLED", "true")
	t.Setenv("MULTICA_WORKSPACE_FILES_ENABLED", "true")
	t.Setenv("MULTICA_WORKSPACE_FILES_SINGLE_API", "true")
}

type touchFixture struct {
	task, issue, project, resource, runtime, daemon, execution string
	claim                                                      filetouch.Claim
}

func seedFileTouchFixture(t *testing.T) touchFixture {
	t.Helper()
	ctx := context.Background()
	f := touchFixture{daemon: uuid.NewString(), execution: uuid.NewString()}
	f.runtime = dbfx.Runtime(t, "File touch runtime", testutil.Cols{"daemon_id": f.daemon, "provider": "claude"})
	f.project = dbfx.Project(t, "File touch project")
	f.resource = dbfx.Insert(t, "project_resource", testutil.Cols{"workspace_id": testWorkspaceID, "project_id": f.project, "resource_type": "local_directory", "resource_ref": `{"local_path":"C:\\fixture","daemon_id":"` + f.daemon + `"}`, "label": "fixture", "position": 0})
	f.issue = dbfx.Issue(t, "File touch issue", testutil.Cols{"project_id": f.project})
	agent := dbfx.Agent(t, "File touch agent", f.runtime)
	dispatched := time.Now().UTC().Truncate(time.Microsecond)
	f.claim = filetouch.Claim{ProofVersion: 1, WorkspaceID: testWorkspaceID, ProjectID: f.project, RuntimeID: f.runtime, DaemonID: f.daemon, Provider: "claude", DispatchedAt: dispatched.Format(time.RFC3339Nano), Bindings: []filetouch.Binding{{ResourceID: f.resource, Generation: 1, Root: `C:\fixture`, DaemonID: f.daemon, Mode: "in_place"}}}
	snapshot, _ := json.Marshal(f.claim)
	f.task = dbfx.Task(t, agent, testutil.Cols{"runtime_id": f.runtime, "issue_id": f.issue, "status": "running", "dispatched_at": dispatched, "started_at": dispatched, "file_claim_snapshot": string(snapshot)})
	evidence := filetouch.Execution{ID: f.execution, DispatchedAt: f.claim.DispatchedAt, Cwd: `C:\fixture`, Platform: "windows", ResourceID: f.resource}
	req := withURLParam(newRequest("POST", "/file-executions", evidence), "taskId", f.task)
	req = req.WithContext(middleware.WithDaemonContext(req.Context(), testWorkspaceID, f.daemon))
	w := httptest.NewRecorder()
	testHandler.RegisterTaskFileExecution(w, req)
	if w.Code != 200 {
		t.Fatalf("register evidence: %d %s", w.Code, w.Body.String())
	}
	// No FK/cascades are installed for derived evidence. Cleanup is explicitly
	// scoped to this generated run and its generated execution, never workspace-wide.
	t.Cleanup(func() {
		for _, table := range []string{"issue_file_touches", "issue_file_touch_event", "issue_file_touch_pending", "issue_file_touch_backfill"} {
			if _, err := testPool.Exec(ctx, `DELETE FROM `+table+` WHERE run_id=$1`, f.task); err != nil {
				t.Error(err)
			}
		}
		if _, err := testPool.Exec(ctx, `DELETE FROM file_touch_execution WHERE id=$1`, f.execution); err != nil {
			t.Error(err)
		}
	})
	return f
}

func TestFileTouchIngestR2(t *testing.T) {
	requireFileTouchIntegration(t)
	cases := []struct {
		name, input    string
		trusted, proof bool
		version        int
		wantMapped     bool
	}{
		{"valid", `{"file_path":"safe.txt","content":"safe"}`, true, true, 1, true},
		{"nul_alias", `{"file_path":"safe\u0000.txt"}`, true, true, 1, false},
		{"key_alias", `{"file_path\u0000":"safe.txt"}`, true, true, 1, false},
		{"lone_surrogate", `{"file_path":"safe\ud800.txt"}`, true, true, 1, false},
		{"duplicate_identity", `{"file_path":"private.txt","file_path":"safe.txt"}`, true, true, 1, false},
		{"literal_escape", `{"file_path":"literal\\ud800.txt"}`, true, true, 1, true},
		{"missing_proof", `{"file_path":"safe.txt"}`, true, false, 1, false},
		{"old_version", `{"file_path":"safe.txt"}`, true, true, 0, false},
		{"pat_self_proof", `{"file_path":"safe.txt","path_integrity":{"state":"verified"}}`, false, true, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := seedFileTouchFixture(t)
			var input map[string]any
			if json.Unmarshal([]byte(tc.input), &input) != nil {
				t.Fatal("fixture JSON")
			}
			// Deliberately forge verified metadata to ensure the HTTP raw-envelope gate
			// independently rejects surrogate/duplicate aliases before persistence.
			proof := filetouch.Integrity{ProofVersion: tc.version, Provider: "claude", Slots: []filetouch.Slot{{Slot: "file_path", State: filetouch.Verified}}}
			encoded, _ := json.Marshal(proof)
			sidecar := ""
			if tc.proof {
				sidecar = `,"path_integrity":` + string(encoded)
			}
			body := `{"messages":[{"seq":1,"type":"tool_use","tool":"Write","call_id":"` + uuid.NewString() + `","file_execution_id":"` + f.execution + `","input":` + tc.input + sidecar + `}]}`
			req := httptest.NewRequest("POST", "/messages", strings.NewReader(body))
			req = withURLParam(req, "taskId", f.task)
			if tc.trusted {
				req = req.WithContext(middleware.WithDaemonContext(req.Context(), testWorkspaceID, f.daemon))
			} else {
				req.Header.Set("X-User-ID", testUserID)
				req.Header.Set("X-Workspace-ID", testWorkspaceID)
			}
			w := httptest.NewRecorder()
			testHandler.ReportTaskMessages(w, req)
			if w.Code != 200 {
				t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
			}
			messages, err := testHandler.Queries.ListTaskMessages(context.Background(), util.MustParseUUID(f.task))
			if err != nil || len(messages) != 1 {
				t.Fatal("source not persisted")
			}
			var stored map[string]any
			json.Unmarshal(messages[0].Input, &stored)
			if !tc.wantMapped && stored["file_path"] != filetouch.UnverifiedPath {
				t.Fatal("lossy/unauthenticated path alias persisted")
			}
			var pending int
			dbfx.QueryRow(t, `SELECT count(*) FROM issue_file_touch_pending WHERE run_id=$1 AND NOT completed`, f.task).Scan(&pending)
			if pending != 1 {
				t.Fatal("pending source not atomic")
			}
			tx, err := testPool.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err = service.ProjectFileTouch(context.Background(), tx, uuidToString(messages[0].ID)); err != nil {
				tx.Rollback(context.Background())
				t.Fatal(err)
			}
			tx.Rollback(context.Background())
			dbfx.QueryRow(t, `SELECT count(*) FROM issue_file_touch_pending WHERE run_id=$1 AND NOT completed`, f.task).Scan(&pending)
			if pending != 1 {
				t.Fatal("rollback lost pending source")
			}
			tx, err = testPool.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err = service.ProjectFileTouch(context.Background(), tx, uuidToString(messages[0].ID)); err != nil {
				tx.Rollback(context.Background())
				t.Fatal(err)
			}
			if err = tx.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			var mapped int
			dbfx.QueryRow(t, `SELECT count(*) FROM issue_file_touches WHERE run_id=$1 AND mapping_status='mapped'`, f.task).Scan(&mapped)
			if (mapped == 1) != tc.wantMapped {
				t.Fatal("ingest-to-projection identity mismatch")
			}
			response := taskMessageToPayload(messages[0], f.task, f.issue)
			wire, _ := json.Marshal(response)
			var payload map[string]any
			json.Unmarshal(wire, &payload)
			if payload["path_integrity"] != nil || payload["proof_version"] != nil {
				t.Fatal("proof leaked in transcript")
			}
			preview, err := service.PreviewFileTouchRun(context.Background(), testPool, testWorkspaceID, f.task, "")
			if err != nil {
				t.Fatal(err)
			}
			plan := service.FileTouchBackfillPlan{WorkspaceID: testWorkspaceID, ProofVersion: 1, ExtractorVersion: 1, Runs: []service.FileTouchBackfillRun{preview}}
			for i := 0; i < 2; i++ {
				if err = service.ApplyFileTouchBackfill(context.Background(), testPool, plan); err != nil {
					t.Fatal(err)
				}
			}
			var count int
			dbfx.QueryRow(t, `SELECT count(*) FROM issue_file_touch_event WHERE run_id=$1`, f.task).Scan(&count)
			if count != 1 {
				t.Fatal("realtime/backfill double counted")
			}
		})
	}
}

func TestFileTouchGenerationGuard(t *testing.T) {
	requireFileTouchIntegration(t)
	f := seedFileTouchFixture(t)
	check := func(want int64) {
		var generation int64
		dbfx.QueryRow(t, `SELECT binding_generation FROM project_resource WHERE id=$1`, f.resource).Scan(&generation)
		if generation != want {
			t.Fatalf("generation %d want %d", generation, want)
		}
	}
	check(1)
	dbfx.Exec(t, `UPDATE project_resource SET label='changed',position=9,binding_generation=999 WHERE id=$1`, f.resource)
	check(1)
	dbfx.Exec(t, `UPDATE project_resource SET resource_ref=jsonb_set(resource_ref,'{local_path}','"C:\\other"') WHERE id=$1`, f.resource)
	check(2)
	dbfx.Exec(t, `UPDATE project_resource SET resource_ref=jsonb_set(resource_ref,'{local_path}','"C:\\fixture"') WHERE id=$1`, f.resource)
	check(3)
	dbfx.Exec(t, `UPDATE project_resource SET resource_ref=jsonb_set(resource_ref,'{execution_mode}','"in_place"') WHERE id=$1`, f.resource)
	check(3)
	dbfx.Exec(t, `UPDATE project_resource SET resource_ref=jsonb_set(resource_ref,'{execution_mode}','"worktree"') WHERE id=$1`, f.resource)
	check(4)
}
