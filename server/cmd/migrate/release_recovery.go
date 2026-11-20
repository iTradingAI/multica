package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// New concurrent builds commit separately from the ledger. Before retrying
// one, compare the existing index with the definition PostgreSQL would create
// on an empty copy of the target table, using this server's deparser.
type releaseIndex struct {
	table, name, keys string
	unique            bool
}

var releaseIndexes = map[string]releaseIndex{
	"682_chat_task_supplement_message":          {"chat_task_supplement", "uq_chat_task_supplement_message", `(message_id)`, true},
	"683_chat_task_supplement_task":             {"chat_task_supplement", "idx_chat_task_supplement_task", `(task_id, created_at, message_id)`, false},
	"684_chat_task_supplement_workspace":        {"chat_task_supplement", "idx_chat_task_supplement_workspace", `(workspace_id, chat_session_id)`, false},
	"686_weixin_login_expiry_idx":               {"weixin_login_session", "weixin_login_expiry_idx", `(expires_at)`, false},
	"689_weixin_login_session_unique_index":     {"weixin_login_session", "weixin_login_session_pkey", `(id)`, true},
	"691_weixin_account_state_unique_index":     {"weixin_account_state", "weixin_account_state_pkey", `(installation_id)`, true},
	"696_chat_wakeup_due_idx":                   {"chat_wakeup", "idx_chat_wakeup_due", `(next_eval_at) WHERE status = 'active'`, false},
	"697_chat_wakeup_session_idx":               {"chat_wakeup", "idx_chat_wakeup_session", `(chat_session_id, created_at DESC)`, false},
	"698_chat_wakeup_requester_idx":             {"chat_wakeup", "idx_chat_wakeup_requester", `(workspace_id, requester_id, created_at DESC)`, false},
	"700_entitlement_limit_overage_scope_idx":   {"entitlement_limit_overage", "uq_entitlement_limit_overage_scope", `(workspace_id, gate, period_start, period_end)`, true},
	"702_project_folder_name_idx":               {"project_folder", "uq_project_folder_workspace_name", `(workspace_id, LOWER(name))`, true},
	"704_entitlement_feature_attempt_scope_idx": {"entitlement_feature_attempt", "uq_entitlement_feature_attempt_scope", `(workspace_id, feature, day)`, true},
	"706_doc_folder_pkey_index":                 {"doc_folder", "doc_folder_pkey_idx", `(id)`, true},
	"708_doc_pkey_index":                        {"doc", "doc_pkey_idx", `(id)`, true},
	"710_doc_version_pkey_index":                {"doc_version", "doc_version_pkey_idx", `(doc_id, number)`, true},
	"712_doc_folder_workspace_idx":              {"doc_folder", "doc_folder_workspace_idx", `(workspace_id)`, false},
	"713_doc_workspace_idx":                     {"doc", "doc_workspace_idx", `(workspace_id)`, false},
	"714_doc_version_workspace_idx":             {"doc_version", "doc_version_workspace_idx", `(workspace_id)`, false},
	"716_inbox_item_active_shown_idx":           {"inbox_item", "idx_inbox_active_shown", `(workspace_id, recipient_type, recipient_id, (GREATEST(created_at, returned_at)) DESC, id DESC) WHERE archived = false`, false},
}

var attachedKeys = map[string]struct{ indexVersion, constraint string }{
	"690_weixin_login_session_primary_key": {"689_weixin_login_session_unique_index", "weixin_login_session_pkey"},
	"692_weixin_account_state_primary_key": {"691_weixin_account_state_unique_index", "weixin_account_state_pkey"},
	"707_doc_folder_pkey":                  {"706_doc_folder_pkey_index", "doc_folder_pkey"},
	"709_doc_pkey":                         {"708_doc_pkey_index", "doc_pkey"},
	"711_doc_version_pkey":                 {"710_doc_version_pkey_index", "doc_version_pkey"},
}

func (idx releaseIndex) inspect(ctx context.Context, conn *pgxpool.Conn, name string) (present, valid bool, err error) {
	var definition string
	var matchesTable, unique bool
	err = conn.QueryRow(ctx, `
		SELECT COALESCE(ix.indrelid = to_regclass($2), false),
		       COALESCE(ix.indisunique, false),
		       COALESCE(ix.indisvalid AND ix.indisready AND ix.indislive, false),
		       coalesce(split_part(pg_get_indexdef(ix.indexrelid), ' USING ', 2), '')
		FROM pg_class c LEFT JOIN pg_index ix ON ix.indexrelid = c.oid
		WHERE c.oid = to_regclass($1)
	`, name, idx.table).Scan(&matchesTable, &unique, &valid, &definition)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !matchesTable || unique != idx.unique {
		return true, false, fmt.Errorf("index %s table or uniqueness differs", name)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return true, false, err
	}
	defer tx.Rollback(ctx)
	// No rows or indexes are copied. keys and table come from the fixed
	// migration specifications below. Rollback removes both probe objects.
	if _, err := tx.Exec(ctx, "CREATE TEMP TABLE multica_index_probe (LIKE "+pgx.Identifier{idx.table}.Sanitize()+")"); err != nil {
		return true, false, err
	}
	if _, err := tx.Exec(ctx, "CREATE INDEX multica_expected_index ON pg_temp.multica_index_probe "+idx.keys); err != nil {
		return true, false, err
	}
	var expected string
	err = tx.QueryRow(ctx, "SELECT split_part(pg_get_indexdef('pg_temp.multica_expected_index'::regclass), ' USING ', 2)").Scan(&expected)
	if err != nil {
		return true, false, err
	}
	if expected != definition {
		return true, false, fmt.Errorf("index %s: found %s, expected %s", name, definition, expected)
	}
	return true, valid, nil
}

// The caller still runs the registered invalid-index cleanup after identity
// has been checked. A valid matching object only needs its ledger row.
func checkReleaseDDL(ctx context.Context, conn *pgxpool.Conn, version string) (skip bool, err error) {
	if idx, ok := releaseIndexes[version]; ok {
		_, valid, err := idx.inspect(ctx, conn, idx.name)
		return valid, err
	}
	key, ok := attachedKeys[version]
	if !ok {
		return false, nil
	}
	idx := releaseIndexes[key.indexVersion]
	var matches bool
	err = conn.QueryRow(ctx, `
		SELECT con.contype = 'p' AND con.conname = $2 AND con.conindid = to_regclass($2)
		       AND con.convalidated AND NOT con.condeferrable
		FROM pg_constraint con
		WHERE con.conrelid = to_regclass($1) AND (con.contype = 'p' OR con.conname = $2)
	`, idx.table, key.constraint).Scan(&matches)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	attached := err == nil
	if attached && !matches {
		return false, fmt.Errorf("unexpected constraint on %s", idx.table)
	}
	name := idx.name
	if attached {
		name = key.constraint
	}
	_, valid, err := idx.inspect(ctx, conn, name)
	if err != nil {
		return false, err
	}
	if !valid {
		return false, fmt.Errorf("expected usable index %s for primary key on %s", name, idx.table)
	}
	return attached, nil
}

// Verified transaction-safe up migrations from the frozen upstream batch.
var atomicReleaseMigrations = map[string]bool{
	"676_runtime_profile_add_amp":             true,
	"677_inbox_item_snoozed_until":            true,
	"678_project_archive":                     true,
	"679_issue_view_preference_projects_page": true,
	"680_inbox_item_snoozed_at":               true,
	"681_chat_task_supplement":                true,
	"685_weixin_state":                        true,
	"687_issue_origin_weixin_chat":            true,
	"688_issue_origin_weixin_chat_validate":   true,
	"690_weixin_login_session_primary_key":    true,
	"692_weixin_account_state_primary_key":    true,
	"693_runtime_rate_limit":                  true,
	"694_runtime_profile_daemon":              true,
	"695_chat_wakeup":                         true,
	"699_entitlement_limit_overage":           true,
	"701_project_folder":                      true,
	"703_entitlement_feature_attempt":         true,
	"705_docs":                                true,
	"707_doc_folder_pkey":                     true,
	"709_doc_pkey":                            true,
	"711_doc_version_pkey":                    true,
	"715_inbox_item_returned_at":              true,
}
