ALTER TABLE agent_task_queue ADD COLUMN IF NOT EXISTS file_claim_snapshot jsonb;
ALTER TABLE task_message ADD COLUMN IF NOT EXISTS file_execution_id uuid;
ALTER TABLE task_message ADD COLUMN IF NOT EXISTS source_event_id uuid;
ALTER TABLE task_message ADD COLUMN IF NOT EXISTS path_integrity jsonb;
ALTER TABLE task_message ADD COLUMN IF NOT EXISTS proof_version integer;

CREATE TABLE IF NOT EXISTS file_touch_execution (
    id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    task_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    runtime_id uuid NOT NULL,
    daemon_id text NOT NULL,
    dispatched_at timestamptz NOT NULL,
    project_id uuid,
    cwd text NOT NULL,
    path_platform text NOT NULL,
    selected_resource_id uuid,
    claim_snapshot jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS issue_file_touch_pending (
    source_message_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    run_id uuid NOT NULL,
    completed boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS issue_file_touch_event (
    event_key uuid NOT NULL,
    workspace_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    run_id uuid NOT NULL,
    signature text NOT NULL,
    uncertain boolean NOT NULL,
    conflict boolean NOT NULL DEFAULT false
);

CREATE TABLE IF NOT EXISTS issue_file_touches (
    id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    issue_id uuid NOT NULL,
    run_id uuid NOT NULL,
    execution_id uuid,
    source_message_id uuid NOT NULL,
    event_key uuid NOT NULL,
    project_id uuid,
    resource_id uuid,
    binding_generation bigint,
    relative_path text,
    identity_key text NOT NULL,
    op text NOT NULL,
    role text NOT NULL,
    tool text NOT NULL,
    observed_at timestamptz NOT NULL,
    mapping_status text NOT NULL,
    reason text NOT NULL,
    extractor_version integer NOT NULL
);

CREATE TABLE IF NOT EXISTS issue_file_touch_backfill (
    id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    run_id uuid NOT NULL,
    high_water uuid,
    checkpoint uuid,
    preview_fingerprint text NOT NULL,
    extractor_version integer NOT NULL,
    completed boolean NOT NULL DEFAULT false
);
