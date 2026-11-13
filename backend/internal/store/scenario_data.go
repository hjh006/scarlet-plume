package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type ScenarioDataSource struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Kind           string     `json:"kind"`
	Status         string     `json:"status"`
	LastObservedAt *time.Time `json:"lastObservedAt,omitempty"`
}
type ScenarioData struct {
	Scenario       string                  `json:"scenario,omitempty"`
	Task           string                  `json:"task,omitempty"`
	Classification *ScenarioClassification `json:"classification,omitempty"`
	Materials      []ScenarioMaterial      `json:"materials"`
	MaterialCounts ScenarioMaterialCounts  `json:"materialCounts"`
	AssetID        int64                   `json:"assetId,omitempty"`
	ActivityID     int64                   `json:"activityId,omitempty"`
	Facts          []AssessmentFact        `json:"facts"`
	Sources        []ScenarioDataSource    `json:"sources"`
	Gaps           []string                `json:"gaps"`
}

func (s *Store) ScenarioData(ctx context.Context, ws, actor, assetID, activityID int64) (ScenarioData, error) {
	return s.ScenarioTaskData(ctx, ws, actor, assetID, activityID, "", "")
}

func (s *Store) ScenarioTaskData(ctx context.Context, ws, actor, assetID, activityID int64, scenario, task string) (ScenarioData, error) {
	result := ScenarioData{Facts: []AssessmentFact{}, Sources: []ScenarioDataSource{}, Gaps: []string{}}
	result.Scenario = scenario
	result.Task = task
	result.Materials = []ScenarioMaterial{}
	if assetID < 0 || activityID < 0 || !validScenarioTask(scenario, task) {
		return result, ErrInvalid
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, ws, actor, false); err != nil {
			return err
		}
		if activityID > 0 {
			activity, err := scanActivity(tx.QueryRow(ctx, activitySelect+` WHERE p.workspace_id=$1 AND p.id=$2`, ws, activityID))
			if err != nil {
				return err
			}
			if assetID > 0 && assetID != activity.AssetID {
				return ErrInvalid
			}
			assetID = activity.AssetID
			result.ActivityID = activity.ID
			result.Facts, err = assessmentFacts(ctx, tx, ws, activity)
			if err != nil {
				return err
			}
		} else if assetID > 0 {
			facts, classification, err := classificationAssessmentFacts(ctx, tx, ws, assetID)
			if err != nil {
				return err
			}
			result.Facts = facts
			result.Classification = classification
			facts, err = collectedAssessmentFacts(ctx, tx, ws, assetID)
			if err != nil {
				return err
			}
			result.Facts = append(result.Facts, facts...)
		}
		if assetID > 0 && result.Classification == nil {
			var err error
			result.Classification, err = scenarioClassification(ctx, tx, ws, assetID)
			if err != nil {
				return err
			}
		}
		result.AssetID = assetID
		rows, err := tx.Query(ctx, `SELECT 'discovery:'||s.id,s.source,s.provider,
 CASE WHEN NOT s.enabled THEN 'paused'
 WHEN NOT EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=s.workspace_id AND m.user_id=s.created_by AND m.role='owner') THEN 'revoked'
 WHEN r.id IS NULL THEN 'never_run' WHEN r.status<>'success' THEN 'failed'
 WHEN r.source_revision<>s.revision OR r.mode<>'collect' OR r.observed_at<now()-make_interval(mins=>greatest(s.interval_minutes*3,60)) THEN 'stale' ELSE 'current' END,r.observed_at
 FROM plume.discovery_sources s LEFT JOIN plume.discovery_runs r ON r.id=s.latest_run_id AND r.workspace_id=s.workspace_id WHERE s.workspace_id=$1
 UNION ALL
 SELECT 'automation:'||j.id,COALESCE(j.source,'关联检查'),j.provider,
 CASE WHEN j.revoked_at IS NOT NULL THEN 'revoked' WHEN r.id IS NULL THEN 'never_run' WHEN r.status<>'success' THEN 'failed' ELSE 'current' END,r.observed_at
 FROM plume.automation_jobs j LEFT JOIN LATERAL(SELECT id,status,observed_at FROM plume.automation_runs WHERE job_id=j.id ORDER BY id DESC LIMIT 1) r ON true WHERE j.workspace_id=$1
 UNION ALL
 SELECT 'runtime:'||s.id,s.name,s.kind,CASE WHEN s.revoked_at IS NOT NULL THEN 'revoked' WHEN s.last_seen IS NULL THEN 'never_run' WHEN s.last_seen<now()-interval '1 day' THEN 'stale' ELSE 'observing' END,s.last_seen FROM plume.runtime_security_sources s WHERE s.workspace_id=$1
 UNION ALL
 SELECT 'operations:'||s.id,s.name,s.kind,CASE WHEN s.revoked_at IS NOT NULL THEN 'revoked' WHEN s.last_seen IS NULL THEN 'never_run' WHEN s.last_seen<now()-interval '1 day' THEN 'stale' ELSE 'observing' END,s.last_seen FROM plume.operations_sources s WHERE s.workspace_id=$1 ORDER BY 1 LIMIT 200`, ws)
		if err != nil {
			return err
		}
		result.Sources, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ScenarioDataSource, error) {
			var source ScenarioDataSource
			err := row.Scan(&source.ID, &source.Name, &source.Kind, &source.Status, &source.LastObservedAt)
			return source, err
		})
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, applicationSourceSelect+` ORDER BY s.id DESC LIMIT 200`, ws)
		if err != nil {
			return err
		}
		apps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ApplicationSource, error) { return scanApplicationSource(row) })
		if err != nil {
			return err
		}
		for _, source := range apps {
			result.Sources = append(result.Sources, ScenarioDataSource{ID: fmt.Sprintf("application:%d", source.ID), Name: source.Name, Kind: "application_api", Status: source.Status, LastObservedAt: source.LastObservedAt})
		}
		if assetID == 0 {
			result.Gaps = append(result.Gaps, "请选择已有处理活动或资产，自动获取该范围的来源事实。")
		}
		if len(result.Sources) == 0 {
			result.Gaps = append(result.Gaps, "尚未接入数据来源，可配置数据库采集、应用/API采集或运行时来源。")
		}
		if len(result.Sources) >= 200 {
			result.Gaps = append(result.Gaps, "来源列表有展示上限，请到专业工具核对完整配置范围。")
		}
		if assetID > 0 {
			result.Gaps = append(result.Gaps, "事实只适用于所选资产；来源状态不代表该资产已完整覆盖。业务用途、适用条件和合同等仍需核实。")
		}
		if err := scenarioRecordFacts(ctx, tx, ws, &result); err != nil {
			return err
		}
		result.Materials, result.MaterialCounts = scenarioMaterials(result)
		return nil
	})
	return result, storeError(err)
}
