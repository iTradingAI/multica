-- Rolling back the capability merge restores whole-metadata replace on
-- re-registration; stored capabilities a co-holding daemon still advertises
-- can then be silently erased again. No data depends on the function, so the
-- drop is safe once the queries in pkg/db/queries/runtime.sql stop calling it.
DROP FUNCTION IF EXISTS merge_runtime_capabilities(jsonb, jsonb);
