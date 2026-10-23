-- Enrollment authority survives mutable/deleted provider rows. No API can
-- transfer an enrolled daemon ID to a different account principal.
CREATE TABLE daemon_registration_identity (
    workspace_id UUID NOT NULL,
    daemon_id TEXT NOT NULL,
    owner_id UUID,
    account_enrolled BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Reserve every pre-upgrade namespace before providers can be removed or
-- rewritten. Mixed/ownerless legacy machines remain token-only. This is a
-- restriction, not account proof: a real matching registration must activate
-- an account enrollment before its socket can route workspace files.
INSERT INTO daemon_registration_identity (workspace_id, daemon_id, owner_id)
SELECT workspace_id, daemon_id,
       CASE WHEN count(*) = count(owner_id) AND count(DISTINCT owner_id) = 1
            THEN min(owner_id::text)::uuid ELSE NULL END
FROM agent_runtime
WHERE daemon_id IS NOT NULL AND btrim(daemon_id) <> ''
GROUP BY workspace_id, daemon_id;
