-- Capability merge for daemon runtime re-registration (MAX-140).
--
-- agent_runtime is keyed by the machine-scoped daemon_id, so several daemon
-- processes can legitimately hold one row: a CLI daemon under one profile and
-- an old desktop-bundled daemon on the same machine register the same
-- (workspace, daemon_id, provider). The registration upserts previously
-- replaced metadata wholesale, letting the weakest registrant erase
-- capabilities another live daemon still advertises — an old build without
-- terminal support re-registered at 16:30 and silently wiped terminal-v1
-- while the machine still served terminals, leaving it "online but no
-- terminal" with no signal anywhere.
--
-- This helper keeps metadata last-writer-wins EXCEPT the capabilities array,
-- which becomes the union of the stored and incoming sets. Rows whose stored
-- or incoming capabilities are missing or not an array (pre-capability rows,
-- writers that do not advertise) keep the plain replace. Pure SQL so both
-- registration upserts (built-in and custom profile) share one definition
-- and the merge stays atomic with the upsert itself.

CREATE OR REPLACE FUNCTION merge_runtime_capabilities(stored jsonb, incoming jsonb)
RETURNS jsonb
LANGUAGE sql
AS $$
    SELECT CASE
        WHEN jsonb_typeof(stored -> 'capabilities') = 'array'
         AND jsonb_typeof(incoming -> 'capabilities') = 'array'
        THEN jsonb_set(
            incoming,
            '{capabilities}',
            (
                SELECT COALESCE(jsonb_agg(DISTINCT cap), '[]'::jsonb)
                FROM (
                    SELECT jsonb_array_elements_text(stored -> 'capabilities') AS cap
                    UNION
                    SELECT jsonb_array_elements_text(incoming -> 'capabilities')
                ) AS caps
            )
        )
        ELSE incoming
    END
$$;
