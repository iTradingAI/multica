package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/filetouch"
)

type FileTouchBackfillRun struct {
	RunID       string                      `json:"run_id"`
	HighWater   string                      `json:"high_water"`
	Count       int64                       `json:"count"`
	Fingerprint string                      `json:"fingerprint"`
	Evidence    FileTouchBackfillEvidence   `json:"evidence"`
	Projection  FileTouchBackfillProjection `json:"projection_preview"`
}

type FileTouchBackfillProjection struct {
	Supported   int64 `json:"supported_source_messages"`
	Unsupported int64 `json:"unsupported_source_messages"`
	Mappable    int64 `json:"mappable_source_messages"`
	Unknown     int64 `json:"unknown_source_messages"`
	Conflicting int64 `json:"conflicting_source_messages"`
	Proposed    int64 `json:"proposed_source_messages"`
	Existing    int64 `json:"existing_source_messages"`
}

type FileTouchBackfillEvidence struct {
	Verified int64 `json:"verified_slots"`
	Invalid  int64 `json:"invalid_slots"`
	Changed  int64 `json:"changed_slots"`
	Unknown  int64 `json:"unknown_slots"`
}
type FileTouchBackfillPlan struct {
	WorkspaceID      string                 `json:"workspace_id"`
	ProofVersion     int                    `json:"proof_version"`
	ExtractorVersion int                    `json:"extractor_version"`
	Runs             []FileTouchBackfillRun `json:"runs"`
}
type backfillStore interface {
	Begin(context.Context) (pgx.Tx, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Preview never writes or emits source content, paths, cwd or claim snapshots.
// A supplied high-water rechecks an existing frozen plan, excluding later rows.
func PreviewFileTouchRun(ctx context.Context, db backfillStore, workspace, run, highWater string) (FileTouchBackfillRun, error) {
	result := FileTouchBackfillRun{RunID: run, HighWater: highWater}
	var exists bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_task_queue t JOIN issue i ON i.id=t.issue_id WHERE t.id=$1 AND i.workspace_id=$2)`, run, workspace).Scan(&exists); err != nil {
		return result, err
	}
	if !exists {
		return result, errors.New("allowlisted run not in workspace")
	}
	if highWater == "" {
		if err := db.QueryRow(ctx, `SELECT COALESCE(max(id::text),'') FROM task_message WHERE task_id=$1 AND type='tool_use'`, run).Scan(&result.HighWater); err != nil {
			return result, err
		}
	}
	hash := sha256.New()
	header, _ := json.Marshal(struct {
		Workspace, Run, High string
		Proof, Extractor     int
	}{workspace, run, result.HighWater, filetouch.ProofVersion, filetouch.ExtractorVersion})
	hash.Write(header)
	cursor := ""
	for result.HighWater != "" {
		rows, err := db.Query(ctx, `SELECT m.id::text,jsonb_build_array(m.id,m.seq,m.type,m.tool,m.call_id,m.input,m.path_integrity,m.proof_version,m.file_execution_id,m.source_event_id,e.claim_snapshot,e.cwd,e.path_platform,e.dispatched_at,e.selected_resource_id)::text,
 m.path_integrity,COALESCE(m.proof_version,0),COALESCE(e.id::text,''),COALESCE(e.claim_snapshot->>'provider',''),COALESCE(m.tool,''),m.input,
 COALESCE(m.call_id,''),COALESCE(m.source_event_id::text,''),e.claim_snapshot,COALESCE(e.cwd,''),COALESCE(e.path_platform,''),e.dispatched_at,COALESCE(e.selected_resource_id::text,'')
  FROM task_message m LEFT JOIN file_touch_execution e ON e.id=m.file_execution_id AND e.task_id=m.task_id AND e.workspace_id=$4
  WHERE m.task_id=$1 AND m.type='tool_use' AND m.id::text>$2 AND m.id::text<=$3 ORDER BY m.id LIMIT 200`, run, cursor, result.HighWater, workspace)
		if err != nil {
			return result, err
		}
		n := 0
		type candidate struct{ event, signature string }
		batch := []candidate{}
		events := []string{}
		for rows.Next() {
			var id, body string
			var proofJSON, inputJSON []byte
			var version int
			var executionID, provider, tool string
			var callID, sourceID string
			var claimJSON []byte
			var execution filetouch.Execution
			var dispatched pgtype.Timestamptz
			if err = rows.Scan(&id, &body, &proofJSON, &version, &executionID, &provider, &tool, &inputJSON, &callID, &sourceID, &claimJSON, &execution.Cwd, &execution.Platform, &dispatched, &execution.ResourceID); err != nil {
				break
			}
			hash.Write([]byte(body))
			hash.Write([]byte{0})
			cursor = id
			n++
			result.Count++
			var proof filetouch.Integrity
			var input map[string]any
			json.Unmarshal(proofJSON, &proof)
			json.Unmarshal(inputJSON, &input)
			proof = filetouch.Transform(proof, tool, input, tool, input, version == filetouch.ProofVersion && executionID != "" && provider == proof.Provider)
			if len(proof.Slots) == 0 {
				result.Evidence.Unknown++
			}
			for _, slot := range proof.Slots {
				switch slot.State {
				case filetouch.Verified:
					result.Evidence.Verified++
				case filetouch.Invalid:
					result.Evidence.Invalid++
				case filetouch.Changed:
					result.Evidence.Changed++
				default:
					result.Evidence.Unknown++
				}
			}
			if tool == "Write" || tool == "Edit" || tool == "MultiEdit" || tool == "patch_apply" {
				result.Projection.Supported++
			} else {
				result.Projection.Unsupported++
			}
			var claim filetouch.Claim
			json.Unmarshal(claimJSON, &claim)
			execution.ID = executionID
			if dispatched.Valid {
				execution.DispatchedAt = dispatched.Time.UTC().Format(time.RFC3339Nano)
			}
			touches := filetouch.Extract(tool, input, proof, claim, execution)
			mappable := false
			for _, touch := range touches {
				if touch.Status == "mapped" && (callID != "" || sourceID != "") {
					mappable = true
				}
			}
			if mappable {
				result.Projection.Mappable++
			} else {
				result.Projection.Unknown++
			}
			identity := run + "\x00call:" + callID
			if callID == "" {
				if sourceID != "" {
					identity = run + "\x00source:" + sourceID
				} else {
					identity = run + "\x00message:" + id
				}
			}
			event := uuid.NewSHA1(uuid.MustParse(workspace), []byte(identity)).String()
			batch = append(batch, candidate{event: event, signature: fileTouchSignature(executionID, tool, touches)})
			events = append(events, event)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return result, err
		}
		// Advisory counts can change with realtime projection. Frozen identity
		// binds only the original source and evidence, so repeat/resume is safe.
		if len(events) > 0 {
			projected, queryErr := db.Query(ctx, `SELECT event_key::text,signature,conflict FROM issue_file_touch_event WHERE workspace_id=$1 AND run_id=$2 AND event_key=ANY($3::uuid[])`, workspace, run, events)
			if queryErr != nil {
				return result, queryErr
			}
			type existing struct {
				signature string
				conflict  bool
			}
			seen := map[string]existing{}
			for projected.Next() {
				var event string
				var value existing
				if queryErr = projected.Scan(&event, &value.signature, &value.conflict); queryErr != nil {
					break
				}
				seen[event] = value
			}
			if queryErr == nil {
				queryErr = projected.Err()
			}
			projected.Close()
			if queryErr != nil {
				return result, queryErr
			}
			for _, value := range batch {
				if prior, ok := seen[value.event]; ok {
					result.Projection.Existing++
					if prior.conflict || prior.signature != value.signature {
						result.Projection.Conflicting++
					}
				} else {
					result.Projection.Proposed++
				}
			}
		}
		if n < 200 {
			break
		}
	}
	result.Fingerprint = hex.EncodeToString(hash.Sum(nil))
	return result, nil
}

func ApplyFileTouchBackfill(ctx context.Context, db backfillStore, plan FileTouchBackfillPlan) error {
	if plan.ProofVersion != filetouch.ProofVersion || plan.ExtractorVersion != filetouch.ExtractorVersion || len(plan.Runs) == 0 {
		return errors.New("invalid backfill version or allowlist")
	}
	// Validate every run before the first write, so a later allowlist error does
	// not partially authorize a different scope.
	for _, run := range plan.Runs {
		frozen, err := PreviewFileTouchRun(ctx, db, plan.WorkspaceID, run.RunID, run.HighWater)
		if err != nil {
			return err
		}
		if frozen.RunID != run.RunID || frozen.HighWater != run.HighWater || frozen.Count != run.Count || frozen.Fingerprint != run.Fingerprint || frozen.Evidence != run.Evidence {
			return errors.New("preview fingerprint changed; preview again")
		}
	}
	for _, run := range plan.Runs {
		if err := applyFileTouchRun(ctx, db, plan.WorkspaceID, run); err != nil {
			return err
		}
	}
	return nil
}

func applyFileTouchRun(ctx context.Context, db backfillStore, workspace string, run FileTouchBackfillRun) error {
	job := uuid.NewSHA1(uuid.MustParse(workspace), []byte(fmt.Sprintf("file-touch:%d:%s:%s:%s", filetouch.ExtractorVersion, run.RunID, run.HighWater, run.Fingerprint))).String()
	for {
		done, err := fileTouchBackfillBatch(ctx, db, workspace, run, job)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

func fileTouchBackfillBatch(ctx context.Context, db backfillStore, workspace string, run FileTouchBackfillRun, job string) (bool, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO issue_file_touch_backfill(id,workspace_id,run_id,high_water,preview_fingerprint,extractor_version)
 VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,$6) ON CONFLICT (id) DO NOTHING`, job, workspace, run.RunID, run.HighWater, run.Fingerprint, filetouch.ExtractorVersion)
	if err != nil {
		return false, err
	}
	var cursor string
	var completed bool
	err = tx.QueryRow(ctx, `SELECT COALESCE(checkpoint::text,''),completed FROM issue_file_touch_backfill WHERE id=$1 AND workspace_id=$2 AND run_id=$3 AND preview_fingerprint=$4 AND extractor_version=$5 FOR UPDATE`, job, workspace, run.RunID, run.Fingerprint, filetouch.ExtractorVersion).Scan(&cursor, &completed)
	if err != nil {
		return false, err
	}
	if completed {
		return true, tx.Commit(ctx)
	}
	rows, err := tx.Query(ctx, `SELECT m.id::text FROM task_message m WHERE m.task_id=$1 AND m.type='tool_use' AND m.id::text>$2 AND m.id::text<=$3 ORDER BY m.id LIMIT 200`, run.RunID, cursor, run.HighWater)
	if err != nil {
		return false, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		_, err = tx.Exec(ctx, `INSERT INTO issue_file_touch_pending(source_message_id,workspace_id,issue_id,run_id)
  SELECT m.id,i.workspace_id,i.id,t.id FROM task_message m JOIN agent_task_queue t ON t.id=m.task_id JOIN issue i ON i.id=t.issue_id WHERE m.id=$1 AND i.workspace_id=$2 AND t.id=$3 ON CONFLICT (source_message_id) DO NOTHING`, id, workspace, run.RunID)
		if err != nil {
			return false, err
		}
		if err = ProjectFileTouch(ctx, tx, id); err != nil {
			return false, err
		}
		cursor = id
	}
	done := len(ids) < 200
	_, err = tx.Exec(ctx, `UPDATE issue_file_touch_backfill SET checkpoint=NULLIF($2,'')::uuid,completed=$3 WHERE id=$1`, job, cursor, done)
	if err != nil {
		return false, err
	}
	return done, tx.Commit(ctx)
}
