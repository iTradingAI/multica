# Workbench files and touch evidence

File management starts collapsed at the bottom of the project and issue
right sidebars. The file tree loads on expansion after the WebSocket is ready.
Double-click a file or press Enter on its row to open the built-in preview.
The preview accepts UTF-8 text up to 1 MiB and rejects binary controls.
Markdown has no raw HTML, remote images or automatic external navigation.

## Windows text editing

Editing is disabled by default. Read-only browsing keeps the existing
`resources`, `list` and `read` protocol and requires the two single-API
relay switches documented in `SELF_HOSTING_ADVANCED.md`. New Windows daemons
advertise `workspace-files-edit-v1` separately.

The API accepts edits from a logged-in user session with current access to
the selected project. It resolves an issue's project and the directory
resource on the server. The API process takes a JSON array in
`MULTICA_WORKSPACE_FILES_EDIT_BINDINGS` with these fields for each exact
registration:

| Field | Value |
| --- | --- |
| `workspace_id`, `project_id`, `resource_id` | UUIDs from the registered local-directory binding |
| `binding_generation` | Current nonzero binding generation |
| `directory` | Slash-separated path relative to the resource root, or `.` for the entire approved root |
| `volume_guid` | Windows volume GUID without the braces |
| `directory_id` | 16-digit hexadecimal NTFS file index of the approved directory, excluding the volume root |

An unset or malformed registration enables no writes. Use a dedicated
development fixture to validate a registration. Before approving a real
binding, confirm its daemon, fixed local NTFS volume and exact directory.
A change to the binding generation requires a fresh registration.

Windows pins every ancestor via native handles and rejects reparse points,
multiple hard links, device names, extra data streams and extended
attributes. Editing is limited to 16 KiB of UTF-8 text, including the
preserved BOM and LF or CRLF encoding. Mixed line endings and files whose
owner, group, DACL or mandatory label cannot be matched by the replacement
stay read-only. Use a directory with ordinary inherited security metadata.

The daemon serializes app saves, stages the text privately in the original
parent and rechecks the original file ID and content immediately before
renaming. A local program can still edit or replace that name after the
last comparison. A local write in this interval can be lost, or the
original name can reappear after a local move. The daemon confirms a save
by reading the staged file back at the intended name.

A conflict or unconfirmed save keeps the draft. Read the current disk
version and explicitly choose its version before another save. The UI
never sends a second save on reconnect. Closing the modal with changes
requires a discard decision.

### Windows development fixture

From the repository root in Git Bash:

```bash
cd server
"$USERPROFILE/go-toolchain/go/bin/go.exe" test ./internal/daemon -run 'WorkspaceFilesWindowsEdit' -count=1 -v
cd ..
corepack pnpm --filter @multica/core test workspace-files/
corepack pnpm --filter @multica/views test workbench/
```

The Windows fixture creates temporary NTFS directories. It exercises the
native write path, both WebSocket pumps, a second authenticated client
connection and direct disk readback. It injects the project lookup and
daemon identity at the router seam. Run installed-client and real binding
checks separately and record the client, API and daemon build IDs,
selected directory, request IDs and error codes. Sample the existing
read path before and after the change with `resources → list → read`.

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
