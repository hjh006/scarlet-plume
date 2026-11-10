package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type RuntimeRiskInput struct {
	Revision  int64  `json:"revision"`
	Severity  string `json:"severity"`
	OwnerTeam string `json:"ownerTeam"`
	DueDate   string `json:"dueDate,omitempty"`
	Reason    string `json:"reason"`
}

type RuntimeRisk struct {
	RuntimeRiskInput
	UpdatedBy string    `json:"updatedBy"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (s *Store) UpdateRuntimeRisk(ctx context.Context, workspaceID, actorID, findingID int64, input RuntimeRiskInput) (RuntimeRisk, error) {
	switch input.Severity {
	case "low", "medium", "high", "critical":
	default:
		return RuntimeRisk{}, ErrInvalid
	}
	if findingID <= 0 || input.Revision < 0 || !validCheckText(input.OwnerTeam, 80) || !validCheckText(input.Reason, 400) {
		return RuntimeRisk{}, ErrInvalid
	}
	var dueDate *time.Time
	if input.DueDate != "" {
		date, err := time.Parse(time.DateOnly, input.DueDate)
		if err != nil || date.Year() < 2000 || date.Year() > 9999 {
			return RuntimeRisk{}, ErrInvalid
		}
		dueDate = &date
	}
	var result RuntimeRisk
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		var checkID int64
		var status string
		err = tx.QueryRow(ctx, `
			SELECT c.id, c.status FROM plume.runtime_findings f
			JOIN plume.checks c ON c.workspace_id = f.workspace_id AND c.id = f.check_id
			WHERE f.workspace_id = $1 AND f.id = $2 FOR NO KEY UPDATE OF c
		`, workspaceID, findingID).Scan(&checkID, &status)
		if err != nil {
			return storeError(err)
		}
		if status == "verified" {
			return ErrConflict
		}
		var revision int64
		err = tx.QueryRow(ctx, `SELECT revision FROM plume.runtime_risk_triage
			WHERE finding_id = $1 ORDER BY revision DESC LIMIT 1`, findingID).Scan(&revision)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if revision != input.Revision {
			return ErrConflict
		}
		result.RuntimeRiskInput = input
		result.Revision++
		err = tx.QueryRow(ctx, `
			INSERT INTO plume.runtime_risk_triage
				(workspace_id, finding_id, revision, actor_id, severity, owner_team, due_date, reason)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING created_at, (SELECT username FROM plume.users WHERE id = $4)
		`, workspaceID, findingID, result.Revision, actorID, input.Severity, input.OwnerTeam, dueDate, input.Reason).
			Scan(&result.UpdatedAt, &result.UpdatedBy)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE plume.checks SET owner_team = $2, updated_at = clock_timestamp() WHERE id = $1", checkID, input.OwnerTeam)
		return err
	})
	return result, storeError(err)
}
