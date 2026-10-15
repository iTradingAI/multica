-- name: EnrollAccountDaemonIdentity :one
-- The authenticated registration principal owns the whole machine namespace,
-- not merely the provider rows it chose to submit. Legacy rows may bootstrap
-- only an unambiguous owner; ownerless/mixed registrations fail closed.
-- The unique scope index arbitrates concurrent first registrations. The
-- conflict branch never replaces the winning principal.
INSERT INTO daemon_registration_identity (workspace_id, daemon_id, owner_id, account_enrolled)
SELECT @workspace_id::uuid, @daemon_id::text, @owner_id::uuid, true
WHERE EXISTS (
    SELECT 1 FROM daemon_registration_identity AS identity
    WHERE identity.workspace_id = @workspace_id
      AND identity.daemon_id = @daemon_id
      AND identity.owner_id = @owner_id
) OR (
    NOT EXISTS (
        SELECT 1 FROM daemon_registration_identity AS identity
        WHERE identity.workspace_id = @workspace_id
          AND identity.daemon_id = @daemon_id
    ) AND NOT EXISTS (
        SELECT 1 FROM agent_runtime AS runtime
        WHERE runtime.workspace_id = @workspace_id
          AND runtime.daemon_id = @daemon_id
          AND runtime.owner_id IS DISTINCT FROM @owner_id
    )
)
ON CONFLICT (workspace_id, daemon_id) DO UPDATE
SET account_enrolled = true
WHERE daemon_registration_identity.owner_id = EXCLUDED.owner_id
RETURNING *;

-- name: EnrollTokenDaemonIdentity :one
-- A verified daemon token proves its scoped machine identity, not an account
-- owner. Never turn a token-only enrollment into an account-owned identity.
INSERT INTO daemon_registration_identity (workspace_id, daemon_id)
VALUES (@workspace_id, @daemon_id)
ON CONFLICT (workspace_id, daemon_id) DO UPDATE
SET daemon_id = daemon_registration_identity.daemon_id
RETURNING *;
