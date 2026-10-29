package filetouch

import (
	"context"
	"github.com/jackc/pgx/v5"
)

type Reader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Readiness includes valid concurrent indexes and the non-bypassable guard.
// Retaining columns alone after an interrupted migration is insufficient.
func Ready(ctx context.Context, db Reader) bool {
	if db == nil {
		return false
	}
	var ready bool
	err := db.QueryRow(ctx, `SELECT
 (SELECT count(*)=8 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
 WHERE i.indisvalid AND i.indisready AND c.relnamespace=current_schema()::regnamespace AND c.relname IN
 ('file_touch_execution_identity','file_touch_pending_identity','file_touch_pending_scan',
 'file_touch_event_identity','issue_file_touches_identity','issue_file_touches_issue','file_touch_backfill_identity','file_touch_source_scan'))
 AND EXISTS (SELECT 1 FROM pg_trigger t JOIN pg_proc p ON p.oid=t.tgfoid
 WHERE t.tgrelid=to_regclass('project_resource') AND t.tgname='project_resource_binding_generation'
 AND t.tgenabled IN ('O','A') AND p.proname='project_resource_binding_generation_guard')
 AND NOT EXISTS (SELECT 1 FROM (VALUES
 ('agent_task_queue','file_claim_snapshot'),
 ('project_resource','binding_generation'),
 ('task_message','file_execution_id'),
 ('task_message','source_event_id'),
 ('task_message','path_integrity'),
 ('task_message','proof_version'),
 ('file_touch_execution','id'),
 ('file_touch_execution','workspace_id'),
 ('file_touch_execution','task_id'),
 ('file_touch_execution','issue_id'),
 ('file_touch_execution','runtime_id'),
 ('file_touch_execution','daemon_id'),
 ('file_touch_execution','dispatched_at'),
 ('file_touch_execution','project_id'),
 ('file_touch_execution','cwd'),
 ('file_touch_execution','path_platform'),
 ('file_touch_execution','selected_resource_id'),
 ('file_touch_execution','claim_snapshot'),
 ('file_touch_execution','created_at'),
 ('issue_file_touch_pending','source_message_id'),
 ('issue_file_touch_pending','workspace_id'),
 ('issue_file_touch_pending','issue_id'),
 ('issue_file_touch_pending','run_id'),
 ('issue_file_touch_pending','completed'),
 ('issue_file_touch_pending','created_at'),
 ('issue_file_touch_event','event_key'),
 ('issue_file_touch_event','workspace_id'),
 ('issue_file_touch_event','issue_id'),
 ('issue_file_touch_event','run_id'),
 ('issue_file_touch_event','signature'),
 ('issue_file_touch_event','uncertain'),
 ('issue_file_touch_event','conflict'),
 ('issue_file_touches','id'),
 ('issue_file_touches','workspace_id'),
 ('issue_file_touches','issue_id'),
 ('issue_file_touches','run_id'),
 ('issue_file_touches','execution_id'),
 ('issue_file_touches','source_message_id'),
 ('issue_file_touches','event_key'),
 ('issue_file_touches','project_id'),
 ('issue_file_touches','resource_id'),
 ('issue_file_touches','binding_generation'),
 ('issue_file_touches','relative_path'),
 ('issue_file_touches','identity_key'),
 ('issue_file_touches','op'),
 ('issue_file_touches','role'),
 ('issue_file_touches','tool'),
 ('issue_file_touches','observed_at'),
 ('issue_file_touches','mapping_status'),
 ('issue_file_touches','reason'),
 ('issue_file_touches','extractor_version'),
 ('issue_file_touch_backfill','id'),
 ('issue_file_touch_backfill','workspace_id'),
 ('issue_file_touch_backfill','run_id'),
 ('issue_file_touch_backfill','high_water'),
 ('issue_file_touch_backfill','checkpoint'),
 ('issue_file_touch_backfill','preview_fingerprint'),
 ('issue_file_touch_backfill','extractor_version'),
 ('issue_file_touch_backfill','completed')) AS required(table_name,column_name)
 WHERE NOT EXISTS (SELECT 1 FROM information_schema.columns actual
 WHERE actual.table_schema=current_schema() AND actual.table_name=required.table_name
 AND actual.column_name=required.column_name))`).Scan(&ready)
	return err == nil && ready
}
