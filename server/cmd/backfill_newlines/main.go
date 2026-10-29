// backfill_newlines scans issue.description and comment.content for CR bytes.
// It defaults to a read-only dry run. See README.md before using --apply.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/newlinebackfill"
	"github.com/multica-ai/multica/server/internal/util"
)

func main() {
	if err := run(); err != nil {
		// Connection errors can contain DATABASE_URL; do not echo credentials.
		fmt.Fprintln(os.Stderr, "newline backfill failed:", err)
		os.Exit(1)
	}
}

func run() error {
	apply := flag.Bool("apply", false, "write changes (default is a read-only dry run)")
	dryRun := flag.Bool("dry-run", false, "explicitly select a read-only dry run")
	batchSize := flag.Int("batch-size", newlinebackfill.DefaultBatchSize, "maximum rows per transaction")
	workspace := flag.String("workspace", "", "restrict scanning to this workspace UUID (default: all)")
	timeout := flag.Duration("timeout", 30*time.Minute, "maximum duration, including waits for row locks")
	flag.Parse()
	if *apply && *dryRun {
		return fmt.Errorf("--apply and --dry-run are mutually exclusive")
	}
	if *batchSize < 1 || *timeout <= 0 {
		return fmt.Errorf("--batch-size and --timeout must be positive")
	}
	var workspaceID *string
	if *workspace != "" {
		if _, err := util.ParseUUID(*workspace); err != nil {
			return fmt.Errorf("--workspace must be a UUID")
		}
		workspaceID = workspace
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return fmt.Errorf("DATABASE_URL is required; select the intended maintenance database explicitly")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("invalid database configuration (details omitted to protect credentials)")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("database unavailable (details omitted to protect credentials)")
	}
	encoder := json.NewEncoder(os.Stdout)
	var total int
	for _, table := range []string{"issue", "comment"} {
		var afterID *string
		for {
			rows, err := newlinebackfill.Batch(ctx, pool, newlinebackfill.Options{
				Table: table, BatchSize: *batchSize, AfterID: afterID, WorkspaceID: workspaceID, Apply: *apply,
			})
			if err != nil {
				return err
			}
			for _, row := range rows {
				if err := encoder.Encode(row); err != nil {
					return fmt.Errorf("write row report: %w", err)
				}
			}
			total += len(rows)
			if len(rows) < *batchSize {
				break
			}
			lastID := rows[len(rows)-1].ID
			afterID = &lastID
		}
	}
	return encoder.Encode(struct {
		Type    string `json:"type"`
		Rows    int    `json:"rows"`
		Applied bool   `json:"applied"`
	}{"summary", total, *apply})
}
