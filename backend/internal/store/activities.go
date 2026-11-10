package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type ActivityInput struct {
	AssetID         int64  `json:"assetId"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Purpose         string `json:"purpose"`
	Recipient       string `json:"recipient"`
	Region          string `json:"region"`
	RetentionPolicy string `json:"retentionPolicy"`
	TransferPath    string `json:"transferPath"`
	Safeguards      string `json:"safeguards"`
}

type Activity struct {
	ActivityInput
	ID          int64     `json:"id"`
	AssetName   string    `json:"assetName"`
	AssetSystem string    `json:"assetSystem"`
	CreatedAt   time.Time `json:"createdAt"`
}

type ActivityReference struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

const activitySelect = `
	SELECT p.id, p.asset_id, a.name, a.system, p.name, p.kind, p.purpose,
	       p.recipient, p.region, p.retention_policy, p.transfer_path, p.safeguards, p.created_at
	FROM plume.activities p
	JOIN plume.assets a ON a.workspace_id = p.workspace_id AND a.id = p.asset_id
`

func scanActivity(row pgx.Row) (Activity, error) {
	var p Activity
	err := row.Scan(&p.ID, &p.AssetID, &p.AssetName, &p.AssetSystem, &p.Name, &p.Kind, &p.Purpose,
		&p.Recipient, &p.Region, &p.RetentionPolicy, &p.TransferPath, &p.Safeguards, &p.CreatedAt)
	return p, storeError(err)
}

func validActivityKind(kind string) bool {
	switch kind {
	case "internal", "sharing", "cross_border", "ai":
		return true
	default:
		return false
	}
}

func validOptionalActivityText(value string, length int) bool {
	return value == "" || validCheckText(value, length)
}

func (s *Store) ListActivities(ctx context.Context, workspaceID int64, kind string, assetID, before int64, limit int) ([]Activity, error) {
	if kind != "" && !validActivityKind(kind) {
		return nil, ErrInvalid
	}
	rows, err := s.pool.Query(ctx, activitySelect+`
		WHERE p.workspace_id = $1 AND ($2 = '' OR p.kind = $2)
		  AND ($3 = 0::bigint OR p.asset_id = $3) AND ($4 = 0::bigint OR p.id < $4)
		ORDER BY p.id DESC LIMIT $5`, workspaceID, kind, assetID, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	activities := make([]Activity, 0)
	for rows.Next() {
		activity, err := scanActivity(rows)
		if err != nil {
			return nil, err
		}
		activities = append(activities, activity)
	}
	return activities, rows.Err()
}

func (s *Store) Activity(ctx context.Context, workspaceID, id int64) (Activity, error) {
	return scanActivity(s.pool.QueryRow(ctx, activitySelect+" WHERE p.workspace_id = $1 AND p.id = $2", workspaceID, id))
}

func (s *Store) CreateActivity(ctx context.Context, workspaceID, actorID int64, input ActivityInput) (Activity, error) {
	if input.AssetID < 1 || !validActivityKind(input.Kind) ||
		!validCheckText(input.Name, 100) || !validCheckText(input.Purpose, 400) ||
		!validOptionalActivityText(input.Recipient, 100) || !validOptionalActivityText(input.Region, 100) ||
		!validOptionalActivityText(input.RetentionPolicy, 400) || !validOptionalActivityText(input.TransferPath, 400) ||
		!validOptionalActivityText(input.Safeguards, 400) ||
		(input.Kind == "cross_border" && (input.Recipient == "" || input.Region == "")) {
		return Activity{}, ErrInvalid
	}

	var activity Activity
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		var id int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO plume.activities (workspace_id, asset_id, name, kind, purpose, recipient, region,
				retention_policy, transfer_path, safeguards)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
			workspaceID, input.AssetID, input.Name, input.Kind, input.Purpose, input.Recipient, input.Region,
			input.RetentionPolicy, input.TransferPath, input.Safeguards,
		).Scan(&id); err != nil {
			return storeError(err)
		}
		activity, err = scanActivity(tx.QueryRow(ctx, activitySelect+" WHERE p.workspace_id = $1 AND p.id = $2", workspaceID, id))
		return err
	})
	return activity, err
}
