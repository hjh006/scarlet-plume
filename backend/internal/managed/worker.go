package managed

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (m *Manager) Run(ctx context.Context) {
	idle := time.NewTicker(2 * time.Second)
	defer idle.Stop()
	for ctx.Err() == nil {
		if m.runNext(ctx) {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-idle.C:
		}
	}
}

func (m *Manager) runNext(ctx context.Context) bool {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		slog.Warn("could not generate managed collection run id")
		return false
	}
	claimCtx, cancelClaim := context.WithTimeout(ctx, 5*time.Second)
	claim, err := m.store.ClaimManagedPostgresJob(claimCtx, hex.EncodeToString(id[:]), 90*time.Second)
	cancelClaim()
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("could not claim managed collection")
		}
		return false
	}
	if claim == nil {
		return false
	}
	delivery := m.collectClaim(ctx, *claim)
	if ctx.Err() != nil {
		return false
	}

	finishTimeout := 5 * time.Second
	if claim.Config.AutoChecks || claim.Config.AutoReports {
		finishTimeout = 35 * time.Second
	}
	finishCtx, cancelFinish := context.WithTimeout(ctx, finishTimeout)
	defer cancelFinish()
	if _, err := m.store.FinishManagedPostgresJob(finishCtx, *claim, delivery); err != nil {
		if ctx.Err() == nil && !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrNotFound) {
			slog.Warn("could not record managed collection", "job", claim.JobID)
		}
		return false
	}
	return true
}

func (m *Manager) collectClaim(ctx context.Context, claim store.ManagedPostgresClaim) store.AutomationDelivery {
	c := Connection{
		Host: claim.Config.Host, Port: claim.Config.Port, Database: claim.Config.Database,
		User: claim.Config.User, Schemas: claim.Config.Schemas,
	}
	delivery := store.AutomationDelivery{RunID: claim.RunID, Provider: "postgresql", Source: claim.Source}
	collectionCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	var tables []collector.PostgreSQLTable
	err := m.Validate(c)
	if err == nil {
		var password string
		password, err = m.decryptPassword(c, claim.Config.EncryptedPassword)
		if err == nil {
			tables, err = m.inspect(collectionCtx, c, password)
		}
	}

	var encoded []byte
	assets := make([]collector.Asset, 0, len(tables))
	if err == nil {
		for _, table := range tables {
			assets = append(assets, table.Asset)
		}
		_, err = collector.EncodeSnapshot("postgresql", claim.Source, assets)
	}
	if err == nil {
		encoded, err = json.Marshal(tables)
	}
	if err != nil {
		delivery.FailureCode = "collection_failed"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(collectionCtx.Err(), context.DeadlineExceeded) {
			delivery.FailureCode = "collection_timeout"
		} else if errors.Is(err, collector.ErrPostgreSQLSchemaUnavailable) {
			delivery.FailureCode = store.ManagedPostgresSchemaUnavailableCode
		}
		encoded = []byte(delivery.FailureCode)
	} else {
		delivery.PostgresTables = tables
		delivery.Assets = make([]store.CollectedAsset, len(assets))
		for i, asset := range assets {
			delivery.Assets[i] = store.CollectedAsset{SourceRef: asset.SourceRef, Name: asset.Name}
		}
	}
	delivery.ObservedAt = time.Now().UTC()
	digest := sha256.Sum256(encoded)
	delivery.Digest = hex.EncodeToString(digest[:])
	return delivery
}
