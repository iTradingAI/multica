# Newline maintenance

The server canonicalizes new issue descriptions and comment writes to LF.
This standalone tool repairs existing `issue.description` and `comment.content`.
It never runs during migration or server startup. Default operation is a
read-only dry run; a write requires `--apply`.

Run from the repository's **server** directory in Git Bash, with `DATABASE_URL`
already set to the intended development/test database by the checkout's managed
environment. Do not copy another checkout's environment or use an assumed DB.

```bash
export PATH="/c/Users/Max/go-toolchain/go/bin:$PATH"
go run ./cmd/backfill_newlines --dry-run > newline-dry-run.jsonl
# Optional tenant scope: --workspace <workspace-uuid>
go test ./internal/newlinebackfill -run TestBatch -count=1 -v
# Apply only to an authorized database, after reviewing the dry-run report:
go run ./cmd/backfill_newlines --apply > newline-applied.jsonl
go run ./cmd/backfill_newlines --dry-run > newline-remaining.jsonl
```

Each JSONL row contains table, row/workspace IDs, original revision, CR count,
byte and LF counts before/after, and whether it was applied. No content is
printed. A final summary gives the total; the remaining report should show zero
rows once all writers have been upgraded. Dry runs use read-only transactions.
The scan includes all rows containing CR, including soft-deleted rows, and
preserves NULL descriptions. `--batch-size` defaults to 500. `--timeout` defaults
to 30 minutes and bounds waits for locks.

`CRLF`, `CRCRLF`, and longer CR runs followed by LF become **one** LF.
Standalone CR becomes LF; consecutive standalone CRs retain separate line
breaks. Existing LF, Markdown, Unicode and intentional blank lines are retained.
Already-expanded LF-only blank lines cannot be distinguished from intentional
spacing; this tool does not guess or remove them.

Apply locks current rows and commits each batch independently. It increments
each changed row's revision once, preserving timestamps and issue activity
ordering. It creates no comments, activities, notifications, or agent triggers,
and emits no WebSocket updates. Refresh affected clients after maintenance;
stale conditional edits will fail their revision check. An interrupted run may
have committed earlier batches: retain its report, dry-run again and resume.
Normalization is idempotent. Take a database backup before an authorized live
apply; JSONL reports contain no original text and are not a rollback backup.

Deploy normalized writers before applying and pause incompatible old writers.
A dry-run scan spans separate batches, so concurrent writes can change the
reported population. No production execution is authorized by this change;
production maintenance requires a separate operator approval and backup.
