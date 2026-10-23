CREATE INDEX CONCURRENTLY IF NOT EXISTS file_touch_source_scan ON task_message (task_id, id) WHERE type = 'tool_use';
