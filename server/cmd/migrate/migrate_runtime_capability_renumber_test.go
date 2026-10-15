package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The fork's unchanged function migration moves out of upstream's 564 slot.
// Full filenames, not numeric prefixes, are recorded in schema_migrations:
// upgrades must preserve historical records and safely apply the new identity.
func TestRuntimeCapabilityRenumberPreservesAppliedFunction(t *testing.T) {
	t.Parallel()
	const version = "565_runtime_capability_merge"
	var completed atomic.Int32
	// The shared DB helper may skip when a developer has no database. CI has
	// PostgreSQL configured and must prove that every regression case ran.
	t.Cleanup(func() {
		if os.Getenv("CI") == "true" && completed.Load() != 4 {
			t.Errorf("expected all 4 database cases to complete in CI, got %d", completed.Load())
		}
	})
	for _, legacy := range []string{"", "548_runtime_capability_merge", "551_runtime_capability_merge", "564_runtime_capability_merge"} {
		name := legacy
		if name == "" {
			name = "fresh"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			admin := openTestPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			schema := fmt.Sprintf("runtime_cap_renumber_%d_%d", time.Now().UnixNano(), rand.Uint32())
			ident := pgx.Identifier{schema}.Sanitize()
			if _, err := admin.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+ident+" CASCADE"); err != nil {
					t.Errorf("cleanup: %v", err)
				}
			})
			pool := openTestPoolWithSearchPath(t, schema)
			if _, err := pool.Exec(ctx, `CREATE TABLE schema_migrations (
				version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`); err != nil {
				t.Fatal(err)
			}
			files := realMigrationFiles(t, []string{version}, "up")
			function := schema + ".merge_runtime_capabilities(jsonb,jsonb)"
			var originalOID uint32
			var originalDefinition string
			legacyAppliedAt := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
			if legacy != "" {
				// The renumbered SQL is byte-for-byte unchanged from all three
				// prior fork names. Model a database that already applied it.
				sql, err := os.ReadFile(files[0])
				if err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, string(sql)); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, "INSERT INTO schema_migrations (version, applied_at) VALUES ($1, $2)", legacy, legacyAppliedAt); err != nil {
					t.Fatal(err)
				}
				if err := pool.QueryRow(ctx, "SELECT $1::regprocedure::oid, pg_get_functiondef($1::regprocedure)", function).Scan(&originalOID, &originalDefinition); err != nil {
					t.Fatal(err)
				}
			}
			options := runOptions{
				Direction: "up", Files: files,
				SchemaMigrationsTable: schema + ".schema_migrations",
				AdvisoryLockKey:       int64(rand.Uint64()&0x7fffffffffffffff) | 1,
				Hooks:                 hooksForDirection("up"),
			}
			var firstAppliedAt time.Time
			for attempt := range 2 {
				if err := runMigrations(ctx, pool, options); err != nil {
					t.Fatal(err)
				}
				var appliedAt time.Time
				if err := pool.QueryRow(ctx, "SELECT applied_at FROM schema_migrations WHERE version = $1", version).Scan(&appliedAt); err != nil {
					t.Fatal(err)
				}
				if attempt == 0 {
					firstAppliedAt = appliedAt
				} else if !appliedAt.Equal(firstAppliedAt) {
					t.Fatal("repeat run changed migration history")
				}
			}
			if legacy != "" {
				var currentOID uint32
				var currentDefinition string
				if err := pool.QueryRow(ctx, "SELECT $1::regprocedure::oid, pg_get_functiondef($1::regprocedure)", function).Scan(&currentOID, &currentDefinition); err != nil {
					t.Fatal(err)
				}
				if currentOID != originalOID || currentDefinition != originalDefinition {
					t.Fatal("renumbering changed or recreated the existing function")
				}
				var recordedAt time.Time
				if err := pool.QueryRow(ctx, "SELECT applied_at FROM schema_migrations WHERE version = $1", legacy).Scan(&recordedAt); err != nil || !recordedAt.Equal(legacyAppliedAt) {
					t.Fatalf("legacy record was changed: applied_at=%v error=%v", recordedAt, err)
				}
			}
			// Preserve the fork's union semantics and incoming metadata priority.
			var correct bool
			if err := pool.QueryRow(ctx, `WITH result AS (
				SELECT merge_runtime_capabilities(
					'{"capabilities":["terminal-v1","shared"],"status":"old","stored_only":true}'::jsonb,
					'{"capabilities":["shared","new"],"status":"new"}'::jsonb
				) AS metadata
			) SELECT (metadata - 'capabilities') = '{"status":"new"}'::jsonb
				AND metadata->'capabilities' @> '["terminal-v1","shared","new"]'::jsonb
				AND jsonb_array_length(metadata->'capabilities') = 3 FROM result`).Scan(&correct); err != nil || !correct {
				t.Fatalf("capability union/incoming metadata changed: correct=%t error=%v", correct, err)
			}
			if err := pool.QueryRow(ctx, `SELECT
				merge_runtime_capabilities('{"status":"old"}'::jsonb, '{"status":"new"}'::jsonb) = '{"status":"new"}'::jsonb
				AND merge_runtime_capabilities('{"capabilities":["terminal-v1"]}'::jsonb, '{"capabilities":null}'::jsonb) = '{"capabilities":null}'::jsonb`).Scan(&correct); err != nil || !correct {
				t.Fatalf("legacy fallback changed: correct=%t error=%v", correct, err)
			}
			completed.Add(1)
		})
	}
}
