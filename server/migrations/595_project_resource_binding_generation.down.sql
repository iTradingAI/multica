-- Application rollback keeps the column and trigger. Removing the guard loses
-- generation continuity and must not reuse historical ownership snapshots.
SELECT 1;
