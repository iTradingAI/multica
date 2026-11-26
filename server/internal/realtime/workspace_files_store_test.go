package realtime

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type filesDBFixture struct {
	f                                                                       *testutil.Fixture
	store                                                                   WorkspaceFilesStore
	user, workspace, member, project, otherProject, issue, resource, daemon string
}

func filesNativeDB(t *testing.T) *filesDBFixture {
	t.Helper()
	dsn := os.Getenv("MULTICA_WORKSPACE_FILES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MULTICA_WORKSPACE_FILES_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	if cfg.ConnConfig.Database != "max174_test" && !strings.HasPrefix(cfg.ConnConfig.Database, "multica_test_") {
		t.Fatal("workspace-files tests require a dedicated test database")
	}
	ctx := context.Background()
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	schema := "files_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	scoped := cfg.Copy()
	scoped.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
		if err != nil {
			t.Error("test schema cleanup failed", err)
		}
	})
	// The authorization query is tested against real PostgreSQL in an isolated
	// schema. Only columns it touches are needed; fixture rows use the shared API.
	ddl := `CREATE TABLE member(id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL,user_id uuid NOT NULL,role text NOT NULL DEFAULT 'member');
 CREATE TABLE project(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),workspace_id uuid NOT NULL,visibility text NOT NULL DEFAULT 'workspace');
 CREATE TABLE project_member(project_id uuid NOT NULL, user_id uuid NOT NULL);
 CREATE TABLE issue(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),workspace_id uuid NOT NULL,project_id uuid);
 CREATE TABLE project_resource(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),workspace_id uuid NOT NULL,project_id uuid NOT NULL,resource_type text NOT NULL,resource_ref jsonb NOT NULL,label text,position int);`
	if _, err = pool.Exec(ctx, ddl); err != nil {
		t.Fatal(err)
	}
	guard, err := os.ReadFile("../../migrations/595_project_resource_binding_generation.up.sql")
	if err != nil {
		t.Fatal("read binding guard fixture", err)
	}
	if _, err = pool.Exec(ctx, string(guard)); err != nil {
		t.Fatal("install binding guard fixture", err)
	}
	d := &filesDBFixture{user: uuid.NewString(), workspace: uuid.NewString(), daemon: uuid.NewString()}
	d.f = testutil.New(pool, d.workspace, d.user)
	d.store = WorkspaceFilesStore{DB: pool}
	d.member = d.f.Insert(t, "member", testutil.Cols{"workspace_id": d.workspace, "user_id": d.user})
	d.project = d.f.Insert(t, "project", testutil.Cols{"workspace_id": d.workspace})
	d.otherProject = d.f.Insert(t, "project", testutil.Cols{"workspace_id": d.workspace})
	d.issue = d.f.Insert(t, "issue", testutil.Cols{"workspace_id": d.workspace, "project_id": d.project})
	d.resource = d.addResource(t, d.project, d.workspace, "local_directory", `C:\private\fixture-root`)
	return d
}
func (d *filesDBFixture) addResource(t *testing.T, project, workspace, kind, root string) string {
	ref, _ := json.Marshal(map[string]string{"local_path": root, "daemon_id": d.daemon, "label": root})
	return d.f.Insert(t, "project_resource", testutil.Cols{"workspace_id": workspace, "project_id": project, "resource_type": kind, "resource_ref": ref, "label": root, "position": 1})
}
func (d *filesDBFixture) projectContext() protocol.WorkspaceFilesContext {
	return protocol.WorkspaceFilesContext{Kind: "project", ProjectID: d.project}
}
func TestWorkspaceFilesDatabaseAuthorizationMatrix(t *testing.T) {
	d := filesNativeDB(t)
	ctx := context.Background()
	pc := d.projectContext()
	ic := protocol.WorkspaceFilesContext{Kind: "issue", IssueID: d.issue}
	snap, code := d.store.AuthorizeWorkspaceFiles(ctx, d.user, d.workspace, pc, d.resource)
	if code != "" || snap.Root != `C:\private\fixture-root` || snap.ProjectID != d.project || snap.DaemonID != d.daemon {
		t.Fatal("valid project join", code)
	}
	issueSnap, code := d.store.AuthorizeWorkspaceFiles(ctx, d.user, d.workspace, ic, d.resource)
	if code != "" || issueSnap != snap {
		t.Fatal("issue join", code)
	}
	d.f.Exec(t, "UPDATE project SET visibility='private' WHERE id=$1", d.project)
	for _, context := range []protocol.WorkspaceFilesContext{pc, ic} {
		if _, code := d.store.AuthorizeWorkspaceFiles(ctx, d.user, d.workspace, context, d.resource); code != "forbidden" {
			t.Fatal("private resource requires project membership", code)
		}
	}
	d.f.Exec(t, "INSERT INTO project_member(project_id,user_id) VALUES($1,$2)", d.project, d.user)
	if _, code := d.store.AuthorizeWorkspaceFiles(ctx, d.user, d.workspace, ic, d.resource); code != "" {
		t.Fatal("project member lost access", code)
	}
	crossProject := d.addResource(t, d.otherProject, d.workspace, "local_directory", "/private/cross-project")
	crossWorkspace := d.addResource(t, d.project, uuid.NewString(), "local_directory", `\\private-server\share\directory`)
	wrongType := d.addResource(t, d.project, d.workspace, "github_repo", "/private/type")
	unlinkedIssue := d.f.Insert(t, "issue", testutil.Cols{"workspace_id": d.workspace})
	for _, tc := range []struct {
		name, user, workspace, resource string
		context                         protocol.WorkspaceFilesContext
	}{
		{"nonmember", uuid.NewString(), d.workspace, d.resource, pc}, {"foreign workspace", d.user, uuid.NewString(), d.resource, pc}, {"cross project", d.user, d.workspace, crossProject, pc}, {"cross workspace resource", d.user, d.workspace, crossWorkspace, pc}, {"wrong type", d.user, d.workspace, wrongType, pc}, {"deleted resource", d.user, d.workspace, uuid.NewString(), pc}, {"unlinked issue", d.user, d.workspace, d.resource, protocol.WorkspaceFilesContext{Kind: "issue", IssueID: unlinkedIssue}}, {"issue cross project", d.user, d.workspace, crossProject, ic},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, code := d.store.AuthorizeWorkspaceFiles(ctx, tc.user, tc.workspace, tc.context, tc.resource)
			if code != "forbidden" || got != (WorkspaceFilesSnapshot{}) {
				t.Fatal("denial leaked snapshot", code)
			}
		})
	}
	for _, root := range []string{"/private/posix-name", `C:\private\windows-name`, `\\private-server\share\unc-name`} {
		d.addResource(t, d.project, d.workspace, "local_directory", root)
	}
	resources, code := d.store.ListWorkspaceFilesResources(ctx, d.user, d.workspace, pc)
	if code != "" || len(resources) != 4 {
		t.Fatal("discovery scope", code, len(resources))
	}
	raw, _ := json.Marshal(resources)
	for _, secret := range []string{"private", "fixture-root", "server", "share", "local_path", "daemon_id", "label", "resource_ref"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("discovery leaked trusted metadata", secret)
		}
	}
	for _, r := range resources {
		if r.DisplayName != "local-directory-"+r.ResourceID[:8] || r.Access != "read_only" {
			t.Fatal("unsafe projection")
		}
	}
	h := NewHub()
	relay := newFilesTestRelay()
	h.ConfigureWorkspaceFiles(true, true, d.store, relay)
	c := filesTestClient(h, d.user, d.workspace)
	t.Cleanup(func() { h.workspaceFilesClientGone(c); h.ShutdownWorkspaceFiles() })
	bad := snap
	bad.ResourceID = crossProject
	filesReadRequest(c, bad, uuid.NewString())
	filesWantCode(t, c, "forbidden")
	if relay.sent(protocol.EventWorkspaceFilesRead) != 0 {
		t.Fatal("cross-project query reached daemon")
	}
	d.f.Exec(t, `DELETE FROM member WHERE id=$1`, d.member)
	filesReadRequest(c, snap, uuid.NewString())
	filesWantCode(t, c, "forbidden")
	if relay.sent(protocol.EventWorkspaceFilesRead) != 0 {
		t.Fatal("nonmember reached daemon")
	}
}

type filesEditTestRelay struct{ *filesTestRelay }

func (r filesEditTestRelay) SelectWorkspaceFilesEditTarget(daemon, workspace, id string) (*protocol.WorkspaceFilesTarget, string) {
	return r.SelectWorkspaceFilesTarget(daemon, workspace, id)
}

func TestWorkspaceFilesDatabaseRelayRoleDowngrade(t *testing.T) {
	d := filesNativeDB(t)
	h := NewHub()
	relay := filesEditTestRelay{newFilesTestRelay()}
	h.ConfigureWorkspaceFiles(true, true, d.store, relay)
	c := filesTestClient(h, d.user, d.workspace)
	c.filesSessionExpiresAt = time.Now().Add(time.Hour)
	t.Cleanup(func() { h.workspaceFilesClientGone(c); h.ShutdownWorkspaceFiles() })

	// A shared private project remains visible to the guest after the demotion.
	d.f.Exec(t, "UPDATE project SET visibility='private' WHERE id=$1", d.project)
	d.f.Exec(t, "INSERT INTO project_member(project_id,user_id) VALUES($1,$2)", d.project, d.user)
	otherResource := d.addResource(t, d.otherProject, d.workspace, "local_directory", "/fixture/other")
	bindings := WorkspaceFilesEditBindings{}
	for _, binding := range []struct{ project, resource string }{{d.project, d.resource}, {d.otherProject, otherResource}} {
		bindings[filesEditKey{d.workspace, binding.project, binding.resource, 1}] = protocol.WorkspaceFilesEditPolicy{
			Directory: ".", VolumeGUID: uuid.NewString(), DirectoryID: "0000000000000001",
		}
	}
	h.ConfigureWorkspaceFilesEdit(bindings)

	const version = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	request := func(event string, entry protocol.WorkspaceFilesContext, resource string) string {
		t.Helper()
		id := uuid.NewString()
		fields := map[string]any{"client_req_id": id, "context": entry, "resource_id": resource, "path": "file.txt", "binding_generation": 1}
		if event == protocol.EventWorkspaceFilesSave {
			fields["version"], fields["data"] = version, []byte("fixture draft")
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		c.handleWorkspaceFilesClientFrame(event, raw)
		return id
	}
	for _, event := range []string{protocol.EventWorkspaceFilesRead, protocol.EventWorkspaceFilesSave} {
		id := request(event, d.projectContext(), d.resource)
		if relay.sent(event) != 1 {
			t.Fatalf("member %s did not reach relay", event)
		}
		r := filesRecordFor(t, h, c, id)
		want := protocol.EventWorkspaceFilesReadChunk
		if event == protocol.EventWorkspaceFilesSave {
			want = protocol.EventWorkspaceFilesEditResult
			raw, _ := json.Marshal(protocol.WorkspaceFilesEditResultPayload{
				WorkspaceFilesGeneration: r.target.Generation, DaemonReqID: r.target.RequestID,
				RuntimeID: r.target.RuntimeID, ResourceID: r.snapshot.ResourceID,
				BindingGeneration: r.snapshot.BindingGeneration, Version: version, Data: []byte{},
			})
			h.DeliverWorkspaceFilesFromDaemon(r.target.Connection, want, raw)
		} else {
			filesChunk(h, r, 0, true)
		}
		if got := filesMessage(t, c); got.Type != want {
			t.Fatalf("member %s response = %s; want %s", event, got.Type, want)
		}
	}
	assertDenied := func(reason string, entry protocol.WorkspaceFilesContext, resource string) {
		t.Helper()
		for _, event := range []string{protocol.EventWorkspaceFilesRead, protocol.EventWorkspaceFilesSave} {
			t.Run(reason+"/"+entry.Kind+"/"+event, func(t *testing.T) {
				before := len(relay.targets)
				sent := relay.sent(event)
				request(event, entry, resource)
				if len(relay.targets) != before || relay.sent(event) != sent {
					t.Fatal("denied request selected or reached a daemon")
				}
				filesWantCode(t, c, "forbidden")
			})
		}
	}
	issue := protocol.WorkspaceFilesContext{Kind: "issue", IssueID: d.issue}
	assertDenied("cross-project", d.projectContext(), otherResource)
	assertDenied("cross-project", issue, otherResource)

	// Keep the original registered client and membership row. The file check
	// must deny the request even if socket disconnection has not run yet.
	d.f.Exec(t, "UPDATE member SET role='guest' WHERE id=$1", d.member)
	assertDenied("guest", d.projectContext(), d.resource)
	assertDenied("guest", issue, d.resource)
}

func TestWorkspaceFilesDatabaseStreamingRevalidation(t *testing.T) {
	for _, mutation := range []string{"member removed", "issue moved", "root changed", "daemon changed", "type changed", "resource deleted", "mode changed", "metadata only"} {
		t.Run(mutation, func(t *testing.T) {
			d := filesNativeDB(t)
			h := NewHub()
			relay := newFilesTestRelay()
			h.ConfigureWorkspaceFiles(true, true, d.store, relay)
			c := filesTestClient(h, d.user, d.workspace)
			t.Cleanup(func() { h.workspaceFilesClientGone(c); h.ShutdownWorkspaceFiles() })
			id := uuid.NewString()
			raw, _ := json.Marshal(protocol.WorkspaceFilesClientReadPayload{ClientReqID: id, Context: protocol.WorkspaceFilesContext{Kind: "issue", IssueID: d.issue}, ResourceID: d.resource, Path: "file.txt"})
			c.handleWorkspaceFilesClientFrame(protocol.EventWorkspaceFilesRead, raw)
			r := filesRecordFor(t, h, c, id)
			filesChunk(h, r, 0, false)
			filesMessage(t, c)
			switch mutation {
			case "member removed":
				d.f.Exec(t, `DELETE FROM member WHERE id=$1`, d.member)
			case "issue moved":
				d.f.Exec(t, `UPDATE issue SET project_id=$2 WHERE id=$1`, d.issue, d.otherProject)
			case "root changed":
				d.f.Exec(t, `UPDATE project_resource SET resource_ref=jsonb_set(resource_ref,'{local_path}','"/changed/private"') WHERE id=$1`, d.resource)
			case "daemon changed":
				d.f.Exec(t, `UPDATE project_resource SET resource_ref=jsonb_set(resource_ref,'{daemon_id}',to_jsonb($2::text)) WHERE id=$1`, d.resource, uuid.NewString())
			case "type changed":
				d.f.Exec(t, `UPDATE project_resource SET resource_type='github_repo' WHERE id=$1`, d.resource)
			case "resource deleted":
				d.f.Exec(t, `DELETE FROM project_resource WHERE id=$1`, d.resource)
			case "mode changed":
				d.f.Exec(t, `UPDATE project_resource SET resource_ref=jsonb_set(resource_ref,'{execution_mode}','"worktree"') WHERE id=$1`, d.resource)
			case "metadata only":
				d.f.Exec(t, `UPDATE project_resource SET label='renamed',position=99 WHERE id=$1`, d.resource)
			}
			filesChunk(h, r, 1, true)
			if mutation == "metadata only" {
				msg := filesMessage(t, c)
				if msg.Type != protocol.EventWorkspaceFilesReadChunk {
					t.Fatal("non-authority metadata invalidated")
				}
				if relay.sent(protocol.EventWorkspaceFilesCancel) != 0 {
					t.Fatal("metadata canceled")
				}
			} else {
				filesWantCode(t, c, "forbidden")
				if relay.sent(protocol.EventWorkspaceFilesCancel) != 1 {
					t.Fatal("original target not canceled once")
				}
			}
			filesChunk(h, r, 2, true)
			filesNoMessage(t, c)
			filesAssertLedger(t, h.files)
		})
	}
}
