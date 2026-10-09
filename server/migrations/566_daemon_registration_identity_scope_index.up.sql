CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS daemon_registration_identity_scope_idx ON daemon_registration_identity (workspace_id, daemon_id);
