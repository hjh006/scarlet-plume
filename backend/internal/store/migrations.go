package store

import (
	"context"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrate applies the Go service's migrations in order within the plume schema.
func (s *Store) Migrate(ctx context.Context) error {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}

	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		// SCARLET and a namespace byte, held until this transaction completes.
		const migrationLock int64 = 0x534341524c455401
		if _, err := tx.Exec(ctx, "SELECT pg_catalog.pg_advisory_xact_lock($1)", migrationLock); err != nil {
			return fmt.Errorf("lock plume migrations: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			CREATE SCHEMA IF NOT EXISTS plume;
			CREATE TABLE IF NOT EXISTS plume.schema_migrations (
				version text PRIMARY KEY,
				applied_at timestamptz NOT NULL DEFAULT now()
			)
		`); err != nil {
			return fmt.Errorf("initialize plume migrations: %w", err)
		}

		for _, entry := range entries {
			version := entry.Name()
			var applied bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM plume.schema_migrations WHERE version = $1)", version).
				Scan(&applied); err != nil {
				return err
			}
			if applied {
				continue
			}

			sql, err := migrationFiles.ReadFile("migrations/" + version)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return fmt.Errorf("apply migration %s: %w", version, err)
			}
			if _, err := tx.Exec(ctx, "INSERT INTO plume.schema_migrations (version) VALUES ($1)", version); err != nil {
				return fmt.Errorf("record migration %s: %w", version, err)
			}
		}
		return nil
	})
}
