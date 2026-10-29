// Package newlinebackfill repairs CR-contaminated issue and comment text in
// bounded transactions. Dry runs report metadata only, never document content.
package newlinebackfill

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
)

const DefaultBatchSize = 500

type Options struct {
	// Table must be issue or comment; identifiers are never caller-supplied SQL.
	Table       string
	BatchSize   int
	AfterID     *string
	WorkspaceID *string
	Apply       bool
}

type Row struct {
	Table       string `json:"table"`
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	BeforeBytes int    `json:"before_bytes"`
	AfterBytes  int    `json:"after_bytes"`
	CRCount     int    `json:"cr_count"`
	BeforeLF    int    `json:"before_lf"`
	AfterLF     int    `json:"after_lf"`
	Revision    int64  `json:"revision"`
	Applied     bool   `json:"applied"`
}

// Batch reports every matching row in keyset order. Apply locks each selected
// row before reading its current text, so a concurrent editor is never replaced
// by a stale scan. No SKIP LOCKED: a pass must not silently miss polluted rows.
// Updates increment revision but preserve activity/timestamps and do not enqueue
// agents. Operators must refresh clients after maintenance.
func Batch(ctx context.Context, pool *pgxpool.Pool, opts Options) ([]Row, error) {
	column := ""
	switch opts.Table {
	case "issue":
		column = "description"
	case "comment":
		column = "content"
	default:
		return nil, fmt.Errorf("table must be issue or comment")
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	txOpts := pgx.TxOptions{AccessMode: pgx.ReadOnly}
	lock := ""
	if opts.Apply {
		txOpts.AccessMode = pgx.ReadWrite
		lock = " FOR UPDATE"
	}
	tx, err := pool.BeginTx(ctx, txOpts)
	if err != nil {
		return nil, fmt.Errorf("begin newline batch: %w", err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, fmt.Sprintf(`
SELECT id::text, workspace_id::text, %s, revision
FROM %s
WHERE strpos(%s, chr(13)) > 0
  AND ($1::uuid IS NULL OR id > $1::uuid)
  AND ($2::uuid IS NULL OR workspace_id = $2::uuid)
ORDER BY id LIMIT $3%s`, column, opts.Table, column, lock), opts.AfterID, opts.WorkspaceID, opts.BatchSize)
	if err != nil {
		return nil, fmt.Errorf("scan %s newlines: %w", opts.Table, err)
	}
	type candidate struct {
		report Row
		text   string
	}
	var candidates []candidate
	for rows.Next() {
		c := candidate{report: Row{Table: opts.Table}}
		if err := rows.Scan(&c.report.ID, &c.report.WorkspaceID, &c.text, &c.report.Revision); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read newline row: %w", err)
		}
		c.report.BeforeBytes = len(c.text)
		c.report.CRCount = strings.Count(c.text, "\r")
		c.report.BeforeLF = strings.Count(c.text, "\n")
		c.text = util.NormalizeNewlines(c.text)
		c.report.AfterBytes = len(c.text)
		c.report.AfterLF = strings.Count(c.text, "\n")
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("read newline batch: %w", err)
	}
	reports := make([]Row, 0, len(candidates))
	for _, c := range candidates {
		if opts.Apply {
			result, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s = $1, revision = revision + 1
WHERE id = $2::uuid AND workspace_id = $3::uuid AND revision = $4`, opts.Table, column),
				c.text, c.report.ID, c.report.WorkspaceID, c.report.Revision)
			if err != nil {
				return nil, fmt.Errorf("update %s newlines: %w", opts.Table, err)
			}
			if result.RowsAffected() != 1 {
				return nil, fmt.Errorf("%s row changed during locked batch", opts.Table)
			}
			c.report.Applied = true
		}
		reports = append(reports, c.report)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit newline batch: %w", err)
	}
	return reports, nil
}
