package handler

import (
	"context"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/filetouch"
	"log/slog"
	"time"
)

func (h *Handler) RunFileTouchProjection(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !h.fileTouchCollectionEnabled() || h.TxStarter == nil || !filetouch.Ready(ctx, h.DB) {
				continue
			}
			batchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			_, err := service.ProjectPendingFileTouches(batchCtx, h.TxStarter)
			cancel()
			if err != nil {
				// Database details can contain private path identities. Log a
				// closed reason while retaining the retryable pending batch.
				slog.Error("file touch projection failed; pending batch retained", "reason", "projection_storage")
			}
		}
	}
}
