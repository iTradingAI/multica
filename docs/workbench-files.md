# Workbench files and touch evidence

The shared workbench has a Files tab for issue and project entries. Directories
load on expansion and page at 200 entries. Closing or changing the context
cancels the session. The builtin preview accepts UTF-8 text up to 1 MiB and
rejects binary controls; Markdown has no raw HTML, remote images or automatic
external navigation.

A `file_viewer` surface declares lowercase ASCII `extensions`, such as `txt`,
and requires `files:read`. Match enabled installations and the current platform
before checking the grant. Exactly one match can use `files.readSelected()`;
zero, multiple or failed matches use a fresh builtin read. The guest receives
text, never a path selector, selection ID, resource ID or claim snapshot.
Server selections are socket-bound, version/digest-bound, single-use and
revalidated against membership and resource binding during delivery. Idle
selections and reads share the existing workspace-files quota ledger.

Touch records describe supported tool invocations, not successful modifications
or past file contents. Origin adapters attest path slots before normalization.
Every later transformation can only retain or downgrade that evidence.
Authenticated daemon execution evidence and the exact claim-time resource
snapshot are required for a mapping. Unsupported tools, old transcripts,
missing identities, invalid/changed slots and ambiguous ownership contribute
unknown coverage. No basename matching, current-resource guessing or content
hash dedupe upgrades old history. Clicking a touch resolves its fixed identity
and reads the current file through the authorized workspace-files channel.

## Schema and projection

Migration 567 installs the database binding-generation guard. An insert starts
at 1; binding changes increment it, including A→B→A changes from legacy SQL.
Label and position edits do not increment it. Caller-supplied generations are
ignored. Migrations 568–576 add private evidence, atomic source/pending capture,
projection, backfill checkpoints and eight standalone concurrent indexes.
No new foreign keys or cascades are added. Existing application delete
transactions explicitly remove these dependent records.

Readiness requires all columns, all valid/ready indexes and the enabled guard.
Interrupted index builds require an approved recovery procedure; retained
columns alone do not enable the capability. Down migrations retain the schema,
guard and evidence for application rollback. They do not erase captured data.

`MULTICA_FILE_TOUCHES_ENABLED=true` enables the projection worker. Pausing it
retains source/pending evidence and existing projections. New claims can still
capture evidence when the schema is ready. Each projection batch commits
event dedupe, touch rows and pending completion together; failures roll back
the batch and retain it for retry. Conflicting tool/path sets become unknown
without exposing the path that arrived first. Private claim/proof fields are
excluded from normal task-message and task JSON responses.

## Allowlisted backfill

`file-touch-backfill` defaults to a read-only preview. It requires an explicit
workspace UUID and task/run UUID allowlist. Preview JSON contains versions,
source high-water/fingerprint, closed evidence counts and proposed/existing
projection counts; it contains no paths, contents, cwd or root snapshot.
Projection counts are advisory and may change while realtime work proceeds.

Apply requires `--apply --preview-file <preview.json>`, the same independent
workspace/run allowlist and `MULTICA_FILE_TOUCH_BACKFILL_ENABLED=true`.
It rechecks the source fingerprint before writing. Batches use UUID keyset
pagination with 200 source rows; projection and checkpoint commit together.
Repeat/resume shares realtime event identities. Neither preview nor backfill
rewrites original messages or upgrades missing historical proofs.

Database/API, migration, backfill, restore and installed-client validation need
an approved integration environment. Renderer and pure adapter tests establish
local behavior only; they do not establish native installed-client acceptance.
