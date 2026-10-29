CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS file_touch_pending_identity ON issue_file_touch_pending (source_message_id);
