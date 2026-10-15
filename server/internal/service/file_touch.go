package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/filetouch"
)

// ProjectFileTouch is called with the pending row locked. Source identity,
// event dedupe, projection rows and pending completion commit together.
func ProjectFileTouch(ctx context.Context, tx pgx.Tx, messageID string) error {
	var source, workspace, issue, run, executionID, callID, sourceID, tool string
	var inputJSON, proofJSON, claimJSON []byte
	var execution filetouch.Execution
	var observed time.Time
	var dispatched pgtype.Timestamptz
	var seq int
	var proofVersion int
	err := tx.QueryRow(ctx, `SELECT m.id::text,p.workspace_id::text,p.issue_id::text,p.run_id::text,
 COALESCE(e.id::text,''),COALESCE(m.call_id,''),COALESCE(m.source_event_id::text,''),COALESCE(m.tool,''),m.input,m.path_integrity,
	 e.claim_snapshot,COALESCE(e.cwd,''),COALESCE(e.path_platform,''),COALESCE(e.selected_resource_id::text,''),e.dispatched_at,m.created_at,m.seq,COALESCE(m.proof_version,0)
 FROM issue_file_touch_pending p JOIN task_message m ON m.id=p.source_message_id
 LEFT JOIN file_touch_execution e ON e.id=m.file_execution_id AND e.task_id=m.task_id AND e.workspace_id=p.workspace_id
	 WHERE p.source_message_id=$1 AND NOT p.completed FOR UPDATE OF p`, messageID).Scan(&source, &workspace, &issue, &run, &executionID, &callID, &sourceID, &tool, &inputJSON, &proofJSON, &claimJSON, &execution.Cwd, &execution.Platform, &execution.ResourceID, &dispatched, &observed, &seq, &proofVersion)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var claim filetouch.Claim
	var proof filetouch.Integrity
	var input map[string]any
	json.Unmarshal(claimJSON, &claim)
	json.Unmarshal(proofJSON, &proof)
	if proofVersion != filetouch.ProofVersion {
		proof = filetouch.Integrity{}
	}
	json.Unmarshal(inputJSON, &input)
	if dispatched.Valid {
		execution.DispatchedAt = dispatched.Time.UTC().Format(time.RFC3339Nano)
	}
	execution.ID = executionID
	touches := filetouch.Extract(tool, input, proof, claim, execution)
	uncertain := false
	identity := run + "\x00call:" + callID
	if callID == "" {
		if sourceID != "" {
			identity = run + "\x00source:" + sourceID
		} else {
			identity = run + "\x00message:" + source
			uncertain = true
		}
	}
	event := uuid.NewSHA1(uuid.MustParse(workspace), []byte(identity)).String()
	signature := fileTouchSignature(executionID, tool, touches)
	if tool != "Write" && tool != "Edit" && tool != "MultiEdit" && tool != "patch_apply" {
		tool = "unsupported"
	}
	inserted, err := tx.Exec(ctx, `INSERT INTO issue_file_touch_event (event_key,workspace_id,issue_id,run_id,signature,uncertain)
 VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (event_key) DO NOTHING`, event, workspace, issue, run, signature, uncertain)
	if err != nil {
		return err
	}
	if inserted.RowsAffected() == 0 {
		var previous string
		if err = tx.QueryRow(ctx, `SELECT signature FROM issue_file_touch_event WHERE event_key=$1 FOR UPDATE`, event).Scan(&previous); err != nil {
			return err
		}
		if previous != signature {
			if _, err = tx.Exec(ctx, `UPDATE issue_file_touch_event SET conflict=true WHERE event_key=$1`, event); err != nil {
				return err
			}
			// A conflicting duplicate cannot expose whichever path happened to arrive
			// first. Keep one event count, mark coverage uncertain and erase identities.
			if _, err = tx.Exec(ctx, `UPDATE issue_file_touches SET mapping_status='unknown',reason='event_conflict',relative_path=NULL,resource_id=NULL,binding_generation=NULL,identity_key='conflict:'||id::text WHERE event_key=$1`, event); err != nil {
				return err
			}
		}
	} else {
		if uncertain {
			for i := range touches {
				touches[i].Status = "unknown"
				touches[i].Reason = "missing_event_identity"
				touches[i].RelativePath = ""
				touches[i].ResourceID = ""
				touches[i].Generation = 0
				touches[i].IdentityKey = fmt.Sprintf("unknown:event:%d", i)
			}
			var duplicate bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM task_message WHERE task_id=$1 AND seq=$2 AND id<>$3)`, run, seq, source).Scan(&duplicate); err != nil {
				return err
			}
			if duplicate {
				for i := range touches {
					touches[i].Status = "unknown"
					touches[i].Reason = "legacy_duplicate_sequence"
					touches[i].RelativePath = ""
					touches[i].ResourceID = ""
					touches[i].Generation = 0
				}
			}
		}
		for _, touch := range touches {
			id := uuid.NewSHA1(uuid.MustParse(event), []byte(touch.IdentityKey+"\x00"+touch.Op+"\x00"+touch.Role)).String()
			_, err = tx.Exec(ctx, `INSERT INTO issue_file_touches
   (id,workspace_id,issue_id,run_id,execution_id,source_message_id,event_key,project_id,resource_id,binding_generation,relative_path,identity_key,op,role,tool,observed_at,mapping_status,reason,extractor_version)
   VALUES ($1,$2,$3,$4,NULLIF($5,'')::uuid,$6,$7,NULLIF($8,'')::uuid,NULLIF($9,'')::uuid,NULLIF($10,0),NULLIF($11,''),$12,$13,$14,$15,$16,$17,$18,$19)
   ON CONFLICT (event_key,identity_key,op,role) DO NOTHING`, id, workspace, issue, run, executionID, source, event, claim.ProjectID, touch.ResourceID, touch.Generation, touch.RelativePath, touch.IdentityKey, touch.Op, touch.Role, tool, observed, touch.Status, touch.Reason, filetouch.ExtractorVersion)
			if err != nil {
				return fmt.Errorf("persist file touch projection: %w", err)
			}
		}
	}
	_, err = tx.Exec(ctx, `UPDATE issue_file_touch_pending SET completed=true WHERE source_message_id=$1`, source)
	return err
}

// Conflicts concern the tool/path set, never edits to non-path content. Keep
// this digest private; the proof sidecar contains only closed slot metadata.
func fileTouchSignature(executionID, tool string, touches []filetouch.Touch) string {
	paths := make([]string, 0, len(touches))
	for _, touch := range touches {
		body, _ := json.Marshal(struct{ Identity, Op, Role, Status, Reason string }{touch.IdentityKey, touch.Op, touch.Role, touch.Status, touch.Reason})
		paths = append(paths, string(body))
	}
	sort.Strings(paths)
	unique := paths[:0]
	for _, value := range paths {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	body, _ := json.Marshal(struct {
		Execution, Tool string
		Paths           []string
	}{executionID, tool, unique})
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

func ProjectPendingFileTouches(ctx context.Context, starter interface {
	Begin(context.Context) (pgx.Tx, error)
}) (int, error) {
	tx, err := starter.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT source_message_id::text FROM issue_file_touch_pending WHERE NOT completed ORDER BY source_message_id LIMIT 200 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err = ProjectFileTouch(ctx, tx, id); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(ids), nil
}
