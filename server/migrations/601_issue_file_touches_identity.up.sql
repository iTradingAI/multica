CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS issue_file_touches_identity ON issue_file_touches (event_key, identity_key, op, role);
