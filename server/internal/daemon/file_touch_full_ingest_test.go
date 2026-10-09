package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/filetouch"
)

// Runs as a foreground child of the native provider adapter, not a daemon.
// It supplies original JSON bytes, including a legal JSON NUL escape.
func TestPathIntegritySourceFixtureProcess(t *testing.T) {
	if os.Getenv("MULTICA_PATH_SOURCE_FIXTURE") != "true" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	fmt.Fprintln(os.Stdout, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"illegal-source","name":"Write","input":{"file_path":"safe\u0000.txt","content":"fixture"}}]}}`)
	fmt.Fprintln(os.Stdout, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"valid-source","name":"Write","input":{"file_path":"safe.txt","content":"fixture"}}]}}`)
	fmt.Fprintln(os.Stdout, `{"type":"result","subtype":"success","result":"fixture completed","is_error":false}`)
	os.Exit(0)
}

func TestPathIntegrityFullIngestRejectsSanitizedAlias(t *testing.T) {
	if os.Getenv("MULTICA_FILE_TOUCH_TESTS_APPROVED") != "true" || os.Getenv("DATABASE_URL") == "" {
		t.Skip("C-only: approved dev window and explicit dedicated database required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if !filetouch.Ready(ctx, pool) {
		t.Fatal("migration readiness missing")
	}
	t.Setenv("MULTICA_FILE_TOUCHES_ENABLED", "true")
	t.Setenv("MULTICA_WORKSPACE_FILES_ENABLED", "true")
	t.Setenv("MULTICA_WORKSPACE_FILES_SINGLE_API", "true")
	fixture := testutil.New(pool, "", "")
	suffix := uuid.NewString()
	user := fixture.User(t, "File touch fixture", "file-touch-"+suffix+"@example.test")
	workspace := fixture.Workspace(t, "File touch fixture", "file-touch-"+suffix)
	fixture = testutil.New(pool, workspace, user)
	fixture.Member(t, workspace, user, "owner")
	daemon := uuid.NewString()
	runtimeID := fixture.Runtime(t, "File touch fixture", testutil.Cols{"daemon_id": daemon, "provider": "claude"})
	project := fixture.Project(t, "File touch fixture")
	root := t.TempDir()
	if err = os.WriteFile(filepath.Join(root, "safe.txt"), []byte("independent valid alias fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	ref, _ := json.Marshal(map[string]string{"local_path": root, "daemon_id": daemon, "execution_mode": "in_place"})
	resource := fixture.Insert(t, "project_resource", testutil.Cols{"project_id": project, "workspace_id": workspace, "resource_type": "local_directory", "resource_ref": string(ref), "label": "fixture", "position": 0})
	issue := fixture.Issue(t, "File touch fixture", testutil.Cols{"project_id": project})
	actor := fixture.Agent(t, "File touch fixture", runtimeID)
	dispatched := time.Now().UTC().Truncate(time.Microsecond)
	claim := filetouch.Claim{ProofVersion: 1, WorkspaceID: workspace, ProjectID: project, RuntimeID: runtimeID, DaemonID: daemon, Provider: "claude", DispatchedAt: dispatched.Format(time.RFC3339Nano), Bindings: []filetouch.Binding{{ResourceID: resource, Generation: 1, Root: root, DaemonID: daemon, Mode: "in_place"}}}
	snapshot, _ := json.Marshal(claim)
	task := fixture.Task(t, actor, testutil.Cols{"runtime_id": runtimeID, "issue_id": issue, "status": "running", "started_at": dispatched, "dispatched_at": dispatched, "file_claim_snapshot": string(snapshot)})
	token, err := auth.GenerateDaemonToken()
	if err != nil {
		t.Fatal(err)
	}
	fixture.Insert(t, "daemon_token", testutil.Cols{"workspace_id": workspace, "daemon_id": daemon, "token_hash": auth.HashToken(token), "expires_at": time.Now().Add(time.Hour)})
	t.Cleanup(func() {
		for _, table := range []string{"issue_file_touches", "issue_file_touch_event", "issue_file_touch_pending", "issue_file_touch_backfill"} {
			if _, err := pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE run_id=$1`, task); err != nil {
				t.Error(err)
			}
		}
		if _, err := pool.Exec(context.Background(), `DELETE FROM file_touch_execution WHERE task_id=$1`, task); err != nil {
			t.Error(err)
		}
	})
	queries := db.New(pool)
	h := handler.New(queries, pool, nil, events.New(), nil, nil, nil, nil, handler.Config{})
	router := chi.NewRouter()
	router.Use(middleware.DaemonAuth(queries, nil, nil, nil))
	router.Post("/api/daemon/tasks/{taskId}/file-executions", h.RegisterTaskFileExecution)
	router.Post("/api/daemon/tasks/{taskId}/messages", h.ReportTaskMessages)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient(server.URL)
	client.SetToken(token)
	d := &Daemon{client: client, logger: logger}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	backend, err := agent.New("claude", agent.Config{ExecutablePath: executable, LaunchPrefix: []string{"-test.run=^TestPathIntegritySourceFixtureProcess$", "--"}, Env: map[string]string{"MULTICA_PATH_SOURCE_FIXTURE": "true", "IS_SANDBOX": "1"}, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = d.executeAndDrain(ctx, backend, "source fixture", agent.ExecOptions{Cwd: root, FilePathProvider: "claude", FileTouchExecution: &filetouch.Execution{DispatchedAt: claim.DispatchedAt, Cwd: root, Platform: runtime.GOOS, ResourceID: resource}, Timeout: 10 * time.Second}, logger, task, "", new(atomic.Int32))
	if err != nil {
		t.Fatal(err)
	}
	messages, err := queries.ListTaskMessages(ctx, util.MustParseUUID(task))
	if err != nil {
		t.Fatal(err)
	}
	toolUses := 0
	for _, message := range messages {
		if message.Type != "tool_use" {
			continue
		}
		toolUses++
		var input map[string]any
		json.Unmarshal(message.Input, &input)
		var proof filetouch.Integrity
		json.Unmarshal(message.PathIntegrity, &proof)
		if len(proof.Slots) != 1 {
			t.Fatal("terminal proof missing")
		}
		if input["file_path"] == "safe.txt" {
			if proof.Slots[0].State != filetouch.Verified {
				t.Fatal("valid source lost verification")
			}
		} else if input["file_path"] != filetouch.UnverifiedPath || proof.Slots[0].State != filetouch.Invalid {
			t.Fatal("illegal source alias survived full chain")
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = service.ProjectFileTouch(ctx, tx, util.UUIDToString(message.ID)); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if toolUses != 2 {
		t.Fatal("source adapter fixture did not emit both tool calls")
	}
	// An old daemon reports the unsafe input without protection or proof. It
	// still enters the transcript, but must not join the independent valid file.
	old := TaskMessageData{Seq: 100, Type: "tool_use", Tool: "Write", CallID: uuid.NewString(), Input: map[string]any{"file_path": "safe\x00.txt"}, CreatedAt: time.Now()}
	if err = client.ReportTaskMessages(ctx, task, []TaskMessageData{old}); err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewFileTouchRun(ctx, pool, workspace, task, "")
	if err != nil {
		t.Fatal(err)
	}
	plan := service.FileTouchBackfillPlan{WorkspaceID: workspace, ProofVersion: 1, ExtractorVersion: 1, Runs: []service.FileTouchBackfillRun{preview}}
	for i := 0; i < 2; i++ {
		if err = service.ApplyFileTouchBackfill(ctx, pool, plan); err != nil {
			t.Fatal(err)
		}
	}
	var mapped, unknown int
	if err = pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE mapping_status='mapped' AND relative_path='safe.txt'),count(*) FILTER(WHERE mapping_status='unknown') FROM issue_file_touches WHERE run_id=$1`, task).Scan(&mapped, &unknown); err != nil {
		t.Fatal(err)
	}
	if mapped != 1 || unknown != 2 {
		t.Fatal("illegal/old aliases joined valid file or repeat backfill double counted")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/issues/"+issue+"/file-touches", nil)
	req.Header.Set("X-User-ID", user)
	req.Header.Set("X-Workspace-ID", workspace)
	route := chi.NewRouter()
	route.Get("/api/issues/{id}/file-touches", h.ListIssueFileTouches)
	w := httptest.NewRecorder()
	route.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("touch API: %d", w.Code)
	}
	var response struct {
		Files []struct {
			Path  string `json:"path"`
			Count int    `json:"call_count"`
		}
	}
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Files) != 1 || response.Files[0].Path != "safe.txt" || response.Files[0].Count != 1 {
		t.Fatal("touch API exposed unverified alias")
	}
}
