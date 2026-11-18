package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func runGatewayCacheMaintenance(ctx context.Context, st *store.Store) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for ctx.Err() == nil {
		cycle, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, err := st.PruneExpiredGatewayCache(cycle)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("AI gateway expired cache cleanup failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
