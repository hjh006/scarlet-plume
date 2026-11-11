package store

import (
	"context"
	"errors"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/jackc/pgx/v5"
)

func controlVerificationAllowed(ctx context.Context, tx pgx.Tx, check Check) (bool, error) {
	var spec ControlSpec
	var result *ControlResult
	var status string
	var workspaceID int64
	var scope OperationsScope
	err := tx.QueryRow(ctx, `SELECT a.spec,a.result,a.status,s.scope,a.workspace_id FROM plume.control_actions a JOIN plume.operations_sources s ON s.id=a.operations_source_id AND s.workspace_id=a.workspace_id WHERE a.check_id=$1 AND a.kind='revoke_select' ORDER BY a.id DESC LIMIT 1`, check.ID).Scan(&spec, &result, &status, &scope, &workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if status != "applied" || result == nil || !result.Success || result.Phase != "apply" {
		return false, nil
	}
	current, err := currentOperationsScope(ctx, tx, workspaceID, scope, false)
	if err != nil || !current {
		return false, err
	}
	var table collector.PostgreSQLTable
	var observed time.Time
	err = tx.QueryRow(ctx, `SELECT i.inspection,r.observed_at FROM plume.postgres_asset_inspections i JOIN plume.automation_jobs j ON j.id=i.job_id JOIN plume.managed_postgres_jobs p ON p.job_id=j.id JOIN plume.automation_runs r ON r.id=i.automation_run_id JOIN LATERAL(SELECT id,status FROM plume.automation_runs WHERE job_id=j.id ORDER BY id DESC LIMIT 1)latest ON true WHERE i.asset_id=$1 AND j.id=$2 AND j.revoked_at IS NULL AND latest.id=r.id AND latest.status='success' AND (i.source_host,i.source_port,i.source_database)=(p.host,p.port,p.database_name) AND (p.host,p.port,p.database_name)=($3,$4,$5)`, check.AssetID, scope.JobID, spec.Host, spec.Port, spec.Database).Scan(&table, &observed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !observed.After(result.ObservedAt) || table.Schema != spec.Schema || table.Name != spec.Table {
		return false, nil
	}
	for _, grant := range table.Grants {
		if grant.Column == "" && grant.Privilege == "SELECT" && (spec.Role == "PUBLIC" && grant.Public || spec.Role != "PUBLIC" && grant.Grantee == spec.Role && !grant.Public) {
			return false, nil
		}
	}
	return true, nil
}
