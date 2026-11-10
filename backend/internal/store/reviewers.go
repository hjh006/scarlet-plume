package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ProvisionReviewer adds a dedicated reviewer without changing existing credentials or roles.
func (s *Store) ProvisionReviewer(ctx context.Context, slug, username, passwordHash string) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var workspaceID int64
		if err := tx.QueryRow(ctx, "SELECT id FROM plume.workspaces WHERE slug = $1", slug).Scan(&workspaceID); err != nil {
			return storeError(err)
		}
		var userID int64
		err := tx.QueryRow(ctx, `
			INSERT INTO plume.users (username, password_hash)
			VALUES ($1, $2) ON CONFLICT (username) DO NOTHING RETURNING id
		`, username, passwordHash).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			var assigned bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM plume.memberships m JOIN plume.users u ON u.id = m.user_id
					WHERE m.workspace_id = $1 AND u.username = $2 AND m.role = 'reviewer'
				)
			`, workspaceID, username).Scan(&assigned); err != nil {
				return err
			}
			if !assigned {
				return ErrConflict
			}
			return nil
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO plume.memberships (workspace_id, user_id, role)
			VALUES ($1, $2, 'reviewer')
		`, workspaceID, userID)
		return storeError(err)
	})
}
