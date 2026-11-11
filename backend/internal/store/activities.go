package store

import (
	"context"
	"strings"
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
	Revision    int64     `json:"revision"`
	UpdatedAt   time.Time `json:"updatedAt"`
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
	       p.recipient, p.region, p.retention_policy, p.transfer_path, p.safeguards, p.created_at,p.revision,p.updated_at
	FROM plume.activities p
	JOIN plume.assets a ON a.workspace_id = p.workspace_id AND a.id = p.asset_id
`

func scanActivity(row pgx.Row) (Activity, error) {
	var p Activity
	err := row.Scan(&p.ID, &p.AssetID, &p.AssetName, &p.AssetSystem, &p.Name, &p.Kind, &p.Purpose,
		&p.Recipient, &p.Region, &p.RetentionPolicy, &p.TransferPath, &p.Safeguards, &p.CreatedAt, &p.Revision, &p.UpdatedAt)
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

func validActivityInput(input ActivityInput) bool {
	return input.AssetID > 0 && validActivityKind(input.Kind) && validCheckText(input.Name, 100) && validCheckText(input.Purpose, 400) && validOptionalActivityText(input.Recipient, 100) && validOptionalActivityText(input.Region, 100) && validOptionalActivityText(input.RetentionPolicy, 400) && validOptionalActivityText(input.TransferPath, 400) && validOptionalActivityText(input.Safeguards, 400) && (input.Kind != "cross_border" || input.Recipient != "" && input.Region != "")
}
func normalizeActivityInput(input ActivityInput) ActivityInput {
	input.Name = strings.TrimSpace(input.Name)
	input.Purpose = strings.TrimSpace(input.Purpose)
	input.Recipient = strings.TrimSpace(input.Recipient)
	input.Region = strings.TrimSpace(input.Region)
	input.RetentionPolicy = strings.TrimSpace(input.RetentionPolicy)
	input.TransferPath = strings.TrimSpace(input.TransferPath)
	input.Safeguards = strings.TrimSpace(input.Safeguards)
	return input
}
func (s *Store) CreateActivity(ctx context.Context, workspaceID, actorID int64, input ActivityInput) (Activity, error) {
	input = normalizeActivityInput(input)
	if !validActivityInput(input) {
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

type ActivityUpdateInput struct {
	ActivityInput
	Revision int64 `json:"revision"`
}
type ActivityChange struct {
	ID         int64     `json:"id"`
	ActivityID int64     `json:"activityId"`
	Revision   int64     `json:"revision"`
	Actor      string    `json:"actor"`
	CreatedAt  time.Time `json:"createdAt"`
	Before     Activity  `json:"before"`
	After      Activity  `json:"after"`
}

// Serialize assessment creation with the activity update so the related-row set
// cannot grow between locking assessments and locking their activity.
func lockAssessmentActivityCreation(ctx context.Context, tx pgx.Tx, workspaceID, activityID int64) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('plume:assessment-activity:'||$1::bigint::text||':'||$2::bigint::text,0))`, workspaceID, activityID)
	return err
}
func (s *Store) UpdateActivity(ctx context.Context, workspaceID, actorID, id int64, input ActivityUpdateInput) (Activity, error) {
	input.ActivityInput = normalizeActivityInput(input.ActivityInput)
	if id < 1 || input.Revision < 1 || !validActivityInput(input.ActivityInput) {
		return Activity{}, ErrInvalid
	}
	var result Activity
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		if err := lockAssessmentActivityCreation(ctx, tx, workspaceID, id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id FROM plume.assessments WHERE workspace_id=$1 AND activity_id=$2 ORDER BY id FOR UPDATE`, workspaceID, id)
		if err != nil {
			return err
		}
		_, err = pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		before, err := scanActivity(tx.QueryRow(ctx, activitySelect+` WHERE p.workspace_id=$1 AND p.id=$2 FOR UPDATE OF p`, workspaceID, id))
		if err != nil {
			return err
		}
		if before.Revision != input.Revision {
			return ErrConflict
		}
		if before.AssetID != input.AssetID {
			return ErrInvalid
		}
		if before.ActivityInput == input.ActivityInput {
			result = before
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE plume.activities SET name=$3,kind=$4,purpose=$5,recipient=$6,region=$7,retention_policy=$8,transfer_path=$9,safeguards=$10,revision=revision+1,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, workspaceID, id, input.Name, input.Kind, input.Purpose, input.Recipient, input.Region, input.RetentionPolicy, input.TransferPath, input.Safeguards)
		if err != nil {
			return err
		}
		result, err = scanActivity(tx.QueryRow(ctx, activitySelect+` WHERE p.workspace_id=$1 AND p.id=$2`, workspaceID, id))
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO plume.activity_changes(workspace_id,activity_id,revision,actor_id,before_snapshot,after_snapshot)VALUES($1,$2,$3,$4,$5,$6)`, workspaceID, id, result.Revision, actorID, before, result); err != nil {
			return err
		}
		return markAssessmentsStale(ctx, tx, workspaceID, id, actorID)
	})
	return result, storeError(err)
}
func (s *Store) ActivityHistory(ctx context.Context, workspaceID, actorID, activityID, before int64, limit int) ([]ActivityChange, error) {
	if activityID < 1 || before < 0 || limit < 1 || limit > 1001 {
		return nil, ErrInvalid
	}
	result := []ActivityChange{}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		if _, err := scanActivity(tx.QueryRow(ctx, activitySelect+` WHERE p.workspace_id=$1 AND p.id=$2`, workspaceID, activityID)); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT h.id,h.activity_id,h.revision,u.username,h.created_at,h.before_snapshot,h.after_snapshot FROM plume.activity_changes h JOIN plume.users u ON u.id=h.actor_id WHERE h.workspace_id=$1 AND h.activity_id=$2 AND ($3::bigint=0 OR h.id<$3) ORDER BY h.id DESC LIMIT $4`, workspaceID, activityID, before, limit)
		if err != nil {
			return err
		}
		result, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ActivityChange, error) {
			var item ActivityChange
			err := row.Scan(&item.ID, &item.ActivityID, &item.Revision, &item.Actor, &item.CreatedAt, &item.Before, &item.After)
			return item, err
		})
		return err
	})
	return result, storeError(err)
}
