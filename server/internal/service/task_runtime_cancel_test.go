package service

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Installed daemons may still send cancellation through /fail with a catchall.
// The real transaction must persist a stop without emitting the event that
// creates an "agent errored" inbox item, or the recovery/failure comments that
// used to trigger another leader run.
func TestFailTaskRuntimeCancellationIsSilent(t *testing.T) {
	pool := sharedTestPool(t)
	ctx := context.Background()
	workspaceID, userID, agentID, _ := seedAttributionFixture(t, pool)
	f := testutil.New(pool, workspaceID, userID)
	issueID := f.Issue(t, "runtime cancellation", testutil.Cols{"assignee_type": "agent", "assignee_id": agentID})
	var runtimeID string
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id=$1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	taskID := f.Task(t, agentID, testutil.Cols{"issue_id": issueID, "status": "running", "runtime_id": runtimeID, "attempt": 1, "max_attempts": 2})
	successorID := f.Task(t, agentID, testutil.Cols{"issue_id": issueID, "status": "queued", "runtime_id": runtimeID})
	bus := events.New()
	failed, cancelled := 0, 0
	bus.Subscribe(protocol.EventTaskFailed, func(events.Event) { failed++ })
	bus.Subscribe(protocol.EventTaskCancelled, func(events.Event) { cancelled++ })
	svc := &TaskService{Queries: db.New(pool), TxStarter: pool, Bus: bus}
	task, changed, err := svc.FailTaskWithTransition(ctx, util.MustParseUUID(taskID), "execution cancelled", "session", "/workspace", "feat/saved", "agent_error.unknown", false, "", "/durable")
	if err != nil {
		t.Fatal(err)
	}
	if !changed || task.Status != "cancelled" || task.FailureReason.String != "cancelled" {
		t.Fatalf("stop not settled: changed=%v task=%+v", changed, task)
	}
	if task.SessionID.String != "session" || task.WorkDir.String != "/workspace" || task.BranchName.String != "feat/saved" || task.DurableWorkDir.String != "/durable" {
		t.Fatal("cancel lost delivery/resume metadata")
	}
	if failed != 0 || cancelled != 1 {
		t.Fatalf("events failed=%d cancelled=%d", failed, cancelled)
	}
	var comments, retries int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM comment WHERE issue_id=$1`, issueID).Scan(&comments); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE retry_of_task_id=$1`, taskID).Scan(&retries); err != nil {
		t.Fatal(err)
	}
	if comments != 0 || retries != 0 {
		t.Fatalf("stop generated comments=%d retries=%d", comments, retries)
	}
	if successor, err := svc.Queries.GetAgentTask(ctx, util.MustParseUUID(successorID)); err != nil || successor.Status != "queued" {
		t.Fatalf("stop disturbed its successor: status=%s err=%v", successor.Status, err)
	}
	if _, changed, err := svc.FailTaskWithTransition(ctx, util.MustParseUUID(taskID), "execution cancelled", "", "", "", "agent_error.unknown", false, "", ""); err != nil || changed {
		t.Fatalf("replay was not idempotent: changed=%v err=%v", changed, err)
	}
	if failed != 0 || cancelled != 1 {
		t.Fatal("replay repeated terminal event")
	}
}
