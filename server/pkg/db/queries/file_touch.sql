-- name: SetTaskFileClaimSnapshot :execrows
UPDATE agent_task_queue SET file_claim_snapshot = sqlc.arg('snapshot')::jsonb
WHERE id = sqlc.arg('task_id') AND runtime_id = sqlc.arg('runtime_id')
AND dispatched_at = sqlc.arg('dispatched_at') AND status IN ('dispatched','waiting_local_directory');
