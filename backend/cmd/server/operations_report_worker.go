package main

import (
	"context"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"log"
	"time"
)

func runOperationsReportWorker(ctx context.Context, st *store.Store) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cycle, cancel := context.WithTimeout(ctx, 20*time.Second)
			_, e := st.RunDueOperationsReportSchedules(cycle, 10)
			cancel()
			if e != nil && ctx.Err() == nil {
				log.Printf("operations report schedule failed: %v", e)
			}
		}
	}
}
