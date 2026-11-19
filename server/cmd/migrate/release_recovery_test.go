package main

import (
	"os"
	"testing"
)

func TestReleaseDDLRecovery(t *testing.T) {
	for _, tc := range []struct {
		version, setup, object, data string
		drift                        bool
	}{
		{"682_chat_task_supplement_message", "CREATE TABLE chat_task_supplement(message_id int); INSERT INTO chat_task_supplement VALUES (42)", "uq_chat_task_supplement_message", "SELECT message_id FROM chat_task_supplement", false},
		{"707_doc_folder_pkey", "CREATE TABLE doc_folder(id int NOT NULL); INSERT INTO doc_folder VALUES (42); CREATE UNIQUE INDEX doc_folder_pkey_idx ON doc_folder(id)", "doc_folder_pkey", "SELECT id FROM doc_folder", false},
		{"682_chat_task_supplement_message", "CREATE TABLE chat_task_supplement(message_id int, other int); CREATE UNIQUE INDEX uq_chat_task_supplement_message ON chat_task_supplement(other)", "uq_chat_task_supplement_message", "", true},
	} {
		t.Run(tc.version, func(t *testing.T) {
			ctx := t.Context()
			admin := openTestPool(t)
			schema := createScratchSchema(t, ctx, admin, "release_recover_")
			pool := openTestPoolWithSearchPath(t, schema)
			if _, err := pool.Exec(ctx, tc.setup); err != nil {
				t.Fatal(err)
			}
			files := realMigrationFiles(t, []string{tc.version}, "up")
			if !tc.drift {
				ddl, err := os.ReadFile(files[0])
				if err != nil {
					t.Fatal(err)
				}
				if _, err = pool.Exec(ctx, string(ddl)); err != nil {
					t.Fatal(err)
				}
			}
			var original uint32
			if err := pool.QueryRow(ctx, "SELECT $1::regclass::oid", tc.object).Scan(&original); err != nil {
				t.Fatal(err)
			}
			options := runOptions{Direction: "up", Files: files, SchemaMigrationsTable: schema + ".schema_migrations", Hooks: hooksForDirection("up")}
			for range 2 {
				err := runMigrations(ctx, pool, options)
				if tc.drift != (err != nil) {
					t.Fatalf("drift=%v error=%v", tc.drift, err)
				}
			}
			var oid uint32
			var count int
			if err := pool.QueryRow(ctx, "SELECT $1::regclass::oid, count(*) FROM schema_migrations WHERE version=$2", tc.object, tc.version).Scan(&oid, &count); err != nil {
				t.Fatal(err)
			}
			if oid != original || (tc.drift && count != 0) || (!tc.drift && count != 1) {
				t.Fatalf("oid %d -> %d, ledger rows %d", original, oid, count)
			}
			if !tc.drift {
				var data int
				if err := pool.QueryRow(ctx, tc.data).Scan(&data); err != nil || data != 42 {
					t.Fatalf("data=%d error=%v", data, err)
				}
			}
		})
	}
}

func TestReleaseMigrationRollsBackWhenLedgerInsertFails(t *testing.T) {
	ctx := t.Context()
	admin := openTestPool(t)
	schema := createScratchSchema(t, ctx, admin, "release_atomic_")
	pool := openTestPoolWithSearchPath(t, schema)
	if _, err := pool.Exec(ctx, "CREATE TABLE inbox_item(id int); CREATE TABLE schema_migrations(version text CHECK(false))"); err != nil {
		t.Fatal(err)
	}
	files := realMigrationFiles(t, []string{"715_inbox_item_returned_at"}, "up")
	if err := runMigrations(ctx, pool, runOptions{Direction: "up", Files: files, SchemaMigrationsTable: schema + ".schema_migrations"}); err == nil {
		t.Fatal("expected ledger failure")
	}
	var found bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='inbox_item'::regclass AND attname='returned_at')").Scan(&found); err != nil || found {
		t.Fatalf("column persisted=%v err=%v", found, err)
	}
}
