CREATE INDEX CONCURRENTLY IF NOT EXISTS file_touch_pending_scan ON issue_file_touch_pending (workspace_id, run_id, source_message_id) WHERE NOT completed;
