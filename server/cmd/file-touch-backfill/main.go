// file-touch-backfill defaults to a read-only, versioned preview. Execution
// requires the exact preview file and the independently supplied allowlist.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/filetouch"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "backfill refused or failed; checkpoints retained")
		os.Exit(1)
	}
}
func run() error {
	workspace := flag.String("workspace", "", "explicit workspace UUID")
	runs := flag.String("runs", "", "explicit comma-separated task/run UUID allowlist")
	preview := flag.String("preview-file", "", "frozen preview JSON; required with --apply")
	apply := flag.Bool("apply", false, "write only the frozen allowlisted preview; default read-only")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	ws, err := uuid.Parse(*workspace)
	if err != nil || ws == uuid.Nil || *runs == "" {
		return errors.New("workspace and runs required")
	}
	allow := []string{}
	for _, value := range strings.Split(*runs, ",") {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil {
			return errors.New("invalid run allowlist")
		}
		if !slices.Contains(allow, id.String()) {
			allow = append(allow, id.String())
		}
	}
	slices.Sort(allow)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	if !filetouch.Ready(ctx, pool) {
		return errors.New("migrations or indexes not ready")
	}
	plan := service.FileTouchBackfillPlan{WorkspaceID: ws.String(), ProofVersion: filetouch.ProofVersion, ExtractorVersion: filetouch.ExtractorVersion, Runs: []service.FileTouchBackfillRun{}}
	if *apply {
		if *preview == "" || os.Getenv("MULTICA_FILE_TOUCH_BACKFILL_ENABLED") != "true" {
			return errors.New("apply authorization missing")
		}
		data, err := os.ReadFile(*preview)
		if err != nil {
			return err
		}
		if len(data) > 1<<20 {
			return errors.New("preview too large")
		}
		if err = json.Unmarshal(data, &plan); err != nil {
			return err
		}
		found := []string{}
		for _, run := range plan.Runs {
			found = append(found, run.RunID)
		}
		slices.Sort(found)
		if plan.WorkspaceID != ws.String() || !slices.Equal(found, allow) {
			return errors.New("preview allowlist mismatch")
		}
		if err = service.ApplyFileTouchBackfill(ctx, pool, plan); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "completed", "workspace_id": plan.WorkspaceID, "extractor_version": plan.ExtractorVersion, "runs": plan.Runs})
	}
	for _, run := range allow {
		result, err := service.PreviewFileTouchRun(ctx, pool, ws.String(), run, "")
		if err != nil {
			return err
		}
		plan.Runs = append(plan.Runs, result)
	}
	return json.NewEncoder(os.Stdout).Encode(plan)
}
