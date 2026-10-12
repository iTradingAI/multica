package newlinebackfill

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func fixture(t *testing.T) (*pgxpool.Pool, *testutil.Fixture) {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; use an explicit development/test database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatal("test database unavailable")
	}
	schema := fmt.Sprintf("newline_backfill_%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA %q`, schema)); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP SCHEMA %q CASCADE`, schema))
		pool.Close()
	})
	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	isolated, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(isolated.Close)
	fx := testutil.New(isolated, "00000000-0000-0000-0000-000000000001", "")
	for _, table := range []string{"issue", "comment"} {
		column := "description"
		if table == "comment" {
			column = "content"
		}
		fx.Exec(t, fmt.Sprintf(`CREATE TABLE %s (
id uuid PRIMARY KEY, workspace_id uuid NOT NULL, %s text,
revision bigint NOT NULL DEFAULT 31,
updated_at timestamptz NOT NULL DEFAULT '2026-01-01T00:00:00Z',
last_activity_at timestamptz NOT NULL DEFAULT '2026-01-01T00:00:00Z')`, table, column))
	}
	return isolated, fx
}

func TestBatchDryRunApplyAndIdempotence(t *testing.T) {
	pool, fx := fixture(t)
	ctx := context.Background()
	var clean strings.Builder
	for _, group := range []struct{ gap, count int }{{2, 2}, {3, 13}, {4, 2}, {6, 28}, {12, 20}} {
		for i := 0; i < group.count; i++ {
			clean.WriteString("台账：`code` [link](https://example.com)")
			clean.WriteString(strings.Repeat("\n", group.gap))
		}
	}
	wantLedger := clean.String() + "追加一\n追加二\n"
	pollutedLedger := strings.ReplaceAll(clean.String(), "\n", "\r\r\n") + "追加一\r\n追加二\r\n"
	for _, tc := range []struct{ table, column, text, want string }{
		{"issue", "description", pollutedLedger, wantLedger},
		{"comment", "content", "中文\r\r\nnext\r\n\r\nparagraph\rend", "中文\nnext\n\nparagraph\nend"},
	} {
		t.Run(tc.table, func(t *testing.T) {
			id := fx.Insert(t, tc.table, testutil.Cols{
				"id": "00000000-0000-0000-0000-000000000010", "workspace_id": fx.WorkspaceID, tc.column: tc.text,
			})
			cleanID := fx.Insert(t, tc.table, testutil.Cols{
				"id": "00000000-0000-0000-0000-000000000011", "workspace_id": fx.WorkspaceID, tc.column: "clean\n\ntext",
			})
			fx.Insert(t, tc.table, testutil.Cols{
				"id": "00000000-0000-0000-0000-000000000012", "workspace_id": fx.WorkspaceID, tc.column: nil,
			})
			otherWorkspace := "00000000-0000-0000-0000-000000000002"
			foreignID := fx.Insert(t, tc.table, testutil.Cols{
				"id": "00000000-0000-0000-0000-000000000013", "workspace_id": otherWorkspace, tc.column: "foreign\r\ntext",
			})
			opts := Options{Table: tc.table, BatchSize: 1, WorkspaceID: &fx.WorkspaceID}
			reports, err := Batch(ctx, pool, opts)
			if err != nil || len(reports) != 1 || reports[0].ID != id || reports[0].Applied {
				t.Fatalf("dry run = %+v, %v", reports, err)
			}
			if tc.table == "issue" && (reports[0].CRCount != 920 || reports[0].BeforeLF != 461 || reports[0].AfterLF != 461) {
				t.Fatalf("unexpected ledger byte report: %+v", reports[0])
			}
			var stored string
			var revision int64
			var updated, activity time.Time
			query := fmt.Sprintf(`SELECT %s, revision, updated_at, last_activity_at FROM %s WHERE id = $1`, tc.column, tc.table)
			fx.QueryRow(t, query, id).Scan(&stored, &revision, &updated, &activity)
			if stored != tc.text || revision != 31 {
				t.Fatal("dry run mutated the row")
			}
			opts.Apply = true
			reports, err = Batch(ctx, pool, opts)
			if err != nil || len(reports) != 1 || !reports[0].Applied {
				t.Fatalf("apply = %+v, %v", reports, err)
			}
			var newUpdated, newActivity time.Time
			fx.QueryRow(t, query, id).Scan(&stored, &revision, &newUpdated, &newActivity)
			if stored != tc.want || revision != 32 || !newUpdated.Equal(updated) || !newActivity.Equal(activity) {
				t.Fatalf("apply changed content/history incorrectly: %q revision=%d", stored, revision)
			}
			reports, err = Batch(ctx, pool, opts)
			if err != nil || len(reports) != 0 {
				t.Fatalf("repeat apply = %+v, %v", reports, err)
			}
			fx.QueryRow(t, query, cleanID).Scan(&stored, &revision, &newUpdated, &newActivity)
			if stored != "clean\n\ntext" || revision != 31 {
				t.Fatal("clean row changed")
			}
			fx.QueryRow(t, query, foreignID).Scan(&stored, &revision, &newUpdated, &newActivity)
			if stored != "foreign\r\ntext" || revision != 31 {
				t.Fatal("foreign workspace changed")
			}
			// The unscoped scan must include the foreign workspace too.
			opts.WorkspaceID = nil
			opts.Apply = false
			reports, err = Batch(ctx, pool, opts)
			if err != nil || len(reports) != 1 || reports[0].ID != foreignID {
				t.Fatalf("unscoped scan = %+v, %v", reports, err)
			}
			opts.AfterID = &foreignID
			reports, err = Batch(ctx, pool, opts)
			if err != nil || len(reports) != 0 {
				t.Fatalf("keyset tail = %+v, %v", reports, err)
			}
		})
	}
}

func TestBatchRejectsUntrustedTable(t *testing.T) {
	if _, err := Batch(context.Background(), nil, Options{Table: "issue; DROP TABLE issue"}); err == nil {
		t.Fatal("accepted arbitrary SQL identifier")
	}
}
