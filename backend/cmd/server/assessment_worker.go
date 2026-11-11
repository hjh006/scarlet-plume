package main

import (
	"context"
	"log"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

// One worker cycle claims due rows with SKIP LOCKED; multiple instances may run.
func runAssessmentWorker(ctx context.Context, st *store.Store) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cycle, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, err := st.RefreshDueAssessments(cycle, 25)
			cancel()
			if err != nil && ctx.Err() == nil {
				log.Printf("assessment facts refresh failed: %v", err)
			}
		}
	}
}
