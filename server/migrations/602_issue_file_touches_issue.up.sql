CREATE INDEX CONCURRENTLY IF NOT EXISTS issue_file_touches_issue ON issue_file_touches (workspace_id, issue_id, observed_at DESC, id);
