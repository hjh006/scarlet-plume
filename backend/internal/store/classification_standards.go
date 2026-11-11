package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationstandards"
)

func (s *Store) ClassificationStandardDefault(ctx context.Context, workspaceID int64) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(classification_standard_id, $2) FROM plume.workspaces WHERE id = $1
	`, workspaceID, classificationstandards.DefaultID).Scan(&id)
	if err != nil {
		return "", storeError(err)
	}
	active, err := classificationstandards.IsSelectable(id)
	if err != nil {
		return "", err
	}
	if !active {
		return classificationstandards.DefaultID, nil
	}
	return id, nil
}

func (s *Store) SetClassificationStandardDefault(ctx context.Context, workspaceID, actorID int64, id string) error {
	active, err := classificationstandards.IsSelectable(id)
	if err != nil {
		return err
	}
	if !active {
		return classificationstandards.ErrSelection
	}
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if role != "owner" {
			return ErrForbidden
		}
		_, err = tx.Exec(ctx, "UPDATE plume.workspaces SET classification_standard_id = $2 WHERE id = $1", workspaceID, id)
		return err
	})
}
