package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

const scenarioCheckEngineVersion = "existing-detectors-adapter-v2"

type ScenarioCheckRunFilter struct {
	Scope      *RuntimeEvidenceScope
	Scenario   string
	Task       string
	AssetID    int64
	ActivityID int64
}
type scenarioPostgresSource struct {
	Inspection      PostgresInspection `json:"inspection"`
	SourceRevision  *int64             `json:"sourceRevision"`
	CurrentRevision int64              `json:"currentRevision"`
	Current         bool               `json:"current"`
	Complete        bool               `json:"complete"`
	Scope           OperationsScope    `json:"scope"`
}

func readScenarioPostgresSource(ctx context.Context, tx pgx.Tx, ws, assetID int64) (*scenarioPostgresSource, error) {
	var p scenarioPostgresSource
	e := tx.QueryRow(ctx, `SELECT i.asset_id,i.job_id,r.run_id,r.observed_at,i.inspection,i.classification,i.source_revision,mp.revision,j.source,mp.host,mp.port,mp.database_name,mp.schemas,
 COALESCE(j.revoked_at IS NULL AND mp.enabled AND r.status='success' AND r.id=(SELECT max(id) FROM plume.automation_runs WHERE job_id=j.id)
 AND i.source_revision=mp.revision AND (i.source_host,i.source_port,i.source_database)=(mp.host,mp.port,mp.database_name)
 AND a.source_provider='postgresql' AND a.source=j.source AND a.source_ref=i.inspection->>'sourceRef'
 AND r.observed_at>now()-make_interval(mins=>greatest(mp.interval_minutes*3,60))
 AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=j.workspace_id AND m.user_id=j.actor_id AND m.role IN ('owner','operator')),false)
 FROM plume.postgres_asset_inspections i JOIN plume.assets a ON a.id=i.asset_id AND a.workspace_id=$1
 JOIN plume.automation_jobs j ON j.id=i.job_id AND j.workspace_id=a.workspace_id
 JOIN plume.managed_postgres_jobs mp ON mp.job_id=j.id JOIN plume.automation_runs r ON r.id=i.automation_run_id AND r.job_id=j.id WHERE i.asset_id=$2`, ws, assetID).Scan(&p.Inspection.AssetID, &p.Inspection.JobID, &p.Inspection.RunID, &p.Inspection.ObservedAt, &p.Inspection.Table, &p.Inspection.Classification, &p.SourceRevision, &p.CurrentRevision, &p.Scope.Source, &p.Scope.Host, &p.Scope.Port, &p.Scope.Database, &p.Scope.Schemas, &p.Current)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	p.Scope.JobID = p.Inspection.JobID
	p.Complete = p.Current && p.Inspection.Table.MetadataStatus == "complete" && len(p.Inspection.Table.Warnings) == 0
	return &p, nil
}
func scenarioCheckBasis(ctx context.Context, tx pgx.Tx, code string) (ScenarioCheckBasis, error) {
	var b ScenarioCheckBasis
	e := tx.QueryRow(ctx, `SELECT title,citation,source_url FROM plume.requirements WHERE code=$1`, code).Scan(&b.Title, &b.Clause, &b.URL)
	b.Version = "registered-requirement-v1"
	return b, e
}
func scenarioEvidence(run *ScenarioCheckRun, id, source, record, scope string, observed *time.Time, snapshot any) string {
	raw, _ := json.Marshal(snapshot)
	raw = scenarioCanonicalJSON(raw)
	run.Evidence = append(run.Evidence, ScenarioCheckEvidence{ID: id, Source: source, Record: record, Scope: scope, ObservedAt: observed, Snapshot: raw, Digest: assessmentDigest(json.RawMessage(raw))})
	return id
}
func scenarioFinding(id, title, code, expected, coverage string, basis ScenarioCheckBasis) ScenarioCheckFinding {
	return ScenarioCheckFinding{ID: id, Title: title, Outcome: "needs_verification", RequirementCode: code, Basis: basis, RuleVersion: scenarioCheckEngineVersion, Expected: expected, Observed: "当前没有足以自动判定的同范围来源事实。", Coverage: coverage, EvidenceRefs: []string{}, MissingConditions: []string{}, Remediation: ScenarioCheckRemediation{Advice: "补充有效来源观察，再重新运行同范围检查。", Reason: "当前证据不足，不能生成自动处置申请。"}}
}
func scenarioCurrentControlTarget(ctx context.Context, tx pgx.Tx, ws, assetID int64, p *scenarioPostgresSource) (*ControlActionInput, error) {
	if p == nil || !p.Current {
		return nil, nil
	}
	table := p.Inspection.Table
	allowed := false
	for _, g := range table.Grants {
		if g.Public && g.Privilege == "SELECT" && g.Column == "" && !g.Grantable {
			allowed = true
		}
	}
	if !allowed {
		return nil, nil
	}
	rows, e := tx.Query(ctx, `SELECT id,scope FROM plume.operations_sources WHERE workspace_id=$1 AND kind='postgres_control' AND revoked_at IS NULL AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=operations_sources.workspace_id AND m.user_id=actor_id AND m.role='owner') ORDER BY id`, ws)
	if e != nil {
		return nil, e
	}
	var matches []int64
	for rows.Next() {
		var id int64
		var s OperationsScope
		if e = rows.Scan(&id, &s); e != nil {
			rows.Close()
			return nil, e
		}
		if s.JobID == p.Scope.JobID && s.Source == p.Scope.Source && s.Host == p.Scope.Host && s.Port == p.Scope.Port && s.Database == p.Scope.Database && slices.Contains(s.Schemas, table.Schema) {
			matches = append(matches, id)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if len(matches) != 1 {
		return nil, nil
	}
	return &ControlActionInput{Kind: "revoke_select", OperationsSourceID: matches[0], AssetID: assetID, Role: "PUBLIC"}, nil
}
func evaluateScenarioFindings(ctx context.Context, tx pgx.Tx, ws int64, run *ScenarioCheckRun, c *ScenarioClassification, p *scenarioPostgresSource) error {
	g02, e := scenarioCheckBasis(ctx, tx, "G02")
	if e != nil {
		return e
	}
	g03, e := scenarioCheckBasis(ctx, tx, "G03")
	if e != nil {
		return e
	}
	scope := fmt.Sprintf("所选资产#%d；仅已保存的来源观察，未在本次请求中访问客户数据库。", run.AssetID)
	classify := scenarioFinding("classification_consistency", "来源识别与当前分类一致性", "G02", "受支持的源内识别规则与当前人工分类相容；仅判断已采集范围。", scope, g02)
	acl := scenarioFinding("pg_public_select", "PUBLIC直接SELECT授权技术基线", "G03", "所选技术基线：不保留PUBLIC的直接表/列SELECT授权。该基线为访问控制核查建议，不是法规对任意数据的一概禁止。", scope, g03)
	protection := scenarioFinding("protection_coverage", "完整访问控制、加密与备份验证范围", "G03", "角色继承、有效访问、加密和备份等保护措施具有相应真实技术验证。", scope, g03)
	protection.MissingConditions = []string{"尚缺角色继承与完整有效访问控制验证、数据库静态加密及备份恢复验证。"}
	protection.Remediation.Advice = "接入相应配置、访问测试或恢复验证证据；勿将未观察到PUBLIC授权当作全部保护措施符合。"
	if c != nil {
		ref := scenarioEvidence(run, "classification", "classification_snapshot", fmt.Sprintf("asset:%d/classification:%d", run.AssetID, c.Item.Revision), scope, nil, map[string]any{"record": c.Item.Asset, "revision": c.Item.Revision, "standard": c.Item.Standard, "taxonomy": c.Item.Taxonomy, "manuallyConfirmed": c.ManuallyConfirmed, "usable": c.Usable, "publication": c.Publication})
		classify.EvidenceRefs = append(classify.EvidenceRefs, ref)
		automatic := c.Item.Automatic
		if automatic != nil {
			classify.RuleVersion = automatic.Evaluation.Version
			classify.Observed = fmt.Sprintf("源规则%s，识别比较%s，人工确认=%t，识别快照过期=%t。", automatic.Evaluation.Version, automatic.Comparison, c.ManuallyConfirmed, automatic.Stale)
			classify.EvidenceRefs = append(classify.EvidenceRefs, scenarioEvidence(run, "classification_engine", "classification_engine", automatic.RunID, scope, &automatic.ObservedAt, automatic))
			if !automatic.Stale && p != nil && p.Current && automatic.Comparison == "conflict" {
				classify.Outcome = "non_compliant"
				classify.Remediation.Advice = "核对本次正向格式信号与人工分类差异，补充真实业务事实并重新确认分类；不自动降低或更改法定类别。"
			}
			if !automatic.Stale && p != nil && p.Complete && p.Inspection.Table.ContentAnalysis != nil && p.Inspection.Table.ContentAnalysis.Status == "complete" && len(p.Inspection.Table.ContentAnalysis.Warnings) == 0 && c.Usable && automatic.Comparison == "matched" && automatic.Evaluation.AutoApplicable {
				classify.Outcome = "compliant"
			}
			classify.MissingConditions = append(classify.MissingConditions, automatic.Evaluation.MissingConditions...)
		}
	}
	if p != nil {
		observedRevision := "unknown"
		if p.SourceRevision != nil {
			observedRevision = fmt.Sprint(*p.SourceRevision)
		}
		record := fmt.Sprintf("postgres-job:%d/run:%s/source-revision:%s/current-source-revision:%d", p.Scope.JobID, p.Inspection.RunID, observedRevision, p.CurrentRevision)
		ref := scenarioEvidence(run, "postgres_inspection", "postgresql", record, scope, &p.Inspection.ObservedAt, p)
		acl.EvidenceRefs = append(acl.EvidenceRefs, ref)
		protection.EvidenceRefs = append(protection.EvidenceRefs, ref)
		if p.Current && p.Inspection.Table.Transport != nil {
			transport := p.Inspection.Table.Transport
			if transport.TLS {
				protection.Observed = fmt.Sprintf("本次采集器→配置PostgreSQL端点连接实际使用%s，密码套件%s。仅覆盖该连接跳段，不代表端到端或数据库静态加密有效。", transport.Protocol, transport.Cipher)
			} else {
				protection.Observed = "本次采集器→配置PostgreSQL端点连接未使用TLS，该连接跳段为明文；不据此推断数据库静态加密状态。"
			}
		} else {
			protection.MissingConditions = append(protection.MissingConditions, "未取得有效当前采集连接的TLS观察；旧记录缺少字段、来源失效或修订不符时保持未知。")
		}
		observation, summary, _ := evaluatePostgresInspection(p.Inspection.Table, p.Scope.Database, "G03")
		acl.RuleVersion = "existing-postgres-inspection-direct-acl-v1"
		acl.Observed = summary
		acl.Coverage = observation.Scope
		if !p.Current {
			acl.MissingConditions = append(acl.MissingConditions, "来源停用/失败/超时效/目标或配置修订不匹配，或旧快照未记录采集时配置修订。")
		}
		if p.Current && (observation.PublicSelect || observation.PublicSelectColumnCount > 0) {
			acl.Outcome = "non_compliant"
			acl.Remediation.Advice = "核对业务必要性与批准范围；不需要的PUBLIC直接表级SELECT可发起平台受控撤权申请，字段级授权或其他授权由源系统管理员按实际范围处理。"
		}
		if p.Complete && !observation.PublicSelect && observation.PublicSelectColumnCount == 0 {
			acl.Outcome = "compliant"
		}
		if p.Current && !p.Complete {
			acl.MissingConditions = append(acl.MissingConditions, "元数据或授权观察不完整，缺少命中不能证明此基线符合。")
		}
		if acl.Outcome == "non_compliant" {
			target, e := scenarioCurrentControlTarget(ctx, tx, ws, run.AssetID, p)
			if e != nil {
				return e
			}
			if target != nil {
				acl.Remediation.Executable = true
				acl.Remediation.Kind = target.Kind
				acl.Remediation.Target = target
				acl.Remediation.Reason = "唯一匹配当前资产/采集目标的执行器及可撤销PUBLIC表级授权；只创建申请，需独立审批、TTL和回滚。"
			} else {
				acl.Remediation.Reason = "未找到唯一匹配的当前PostgreSQL执行器，或观察仅涉及列授权/授予选项；不能自动生成该处置目标。"
			}
		}
	}
	// The linked check is a workflow reference, not the automatic result itself.
	for _, target := range []*ScenarioCheckFinding{&classify, &acl} {
		var id int64
		e = tx.QueryRow(ctx, `SELECT id FROM plume.checks WHERE workspace_id=$1 AND asset_id=$2 AND requirement_code=$3 ORDER BY id DESC LIMIT 1`, ws, run.AssetID, target.RequirementCode).Scan(&id)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if e == nil {
			target.CheckID = &id
		}
	}
	if e := applyScenarioCatalogClassification(ctx, tx, ws, run, c, &classify); e != nil {
		return e
	}
	if classify.Outcome == "compliant" {
		classify.Remediation = ScenarioCheckRemediation{
			Advice: "维持当前已确认分类与采用规则；数据、标准或来源配置变化后重新采集并核对。",
			Reason: "本次已观察范围内分类相容性检查符合，无需发起修复。",
		}
	}
	if acl.Outcome == "compliant" {
		acl.Remediation = ScenarioCheckRemediation{
			Advice: "维持当前直接授权配置；权限、对象或来源配置变化后重新采集并复查该技术基线。",
			Reason: "本次直接授权观察范围内未发现PUBLIC SELECT，符合本项技术基线，无需发起修复。",
		}
	}
	run.Findings = []ScenarioCheckFinding{classify, acl, protection}
	return nil
}

func (s *Store) CreateScenarioCheckRun(ctx context.Context, ws, actor int64, input ScenarioCheckRunInput) (ScenarioCheckRun, error) {
	if input.AssetID < 0 || input.ActivityID < 0 || input.Scenario == "" || !validScenarioTask(input.Scenario, input.Task) || input.PreviousRunID != nil && *input.PreviousRunID < 1 {
		return ScenarioCheckRun{}, ErrInvalid
	}
	run := ScenarioCheckRun{Scenario: input.Scenario, Task: input.Task, AssetID: input.AssetID, ActivityID: input.ActivityID, Status: "completed", Mode: "stored_observations", EngineVersion: scenarioCheckEngineVersion, CreatedBy: actor, Findings: []ScenarioCheckFinding{}, Evidence: []ScenarioCheckEvidence{}, ReportRequirements: []ScenarioCheckReportRequirement{}, Warnings: []string{"结果只针对列示技术检查项和保存的来源范围；不表示整部法规符合，不代表已经提交正式监管报告。", "本次重新评估已保存来源观察；如需源端复测，应先取得新的采集批次。"}, Scope: input.Scope, PreviousRunID: input.PreviousRunID, RetestState: "not_retested"}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, ws, actor, true)
		if e != nil {
			return e
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		if input.ActivityID > 0 {
			activity, e := scanActivity(tx.QueryRow(ctx, activitySelect+` WHERE p.workspace_id=$1 AND p.id=$2`, ws, input.ActivityID))
			if e != nil {
				return e
			}
			if input.AssetID > 0 && input.AssetID != activity.AssetID {
				return ErrInvalid
			}
			run.AssetID = activity.AssetID
			run.ActivityRevision = activity.Revision
		}
		if run.AssetID < 1 && input.Scope == nil {
			return ErrInvalid
		}
		if run.AssetID > 0 {
			c, e := scenarioClassification(ctx, tx, ws, run.AssetID)
			if e != nil {
				return e
			}
			run.AssetName = c.Item.Asset.Name
			run.AssetRevision = c.Item.Revision
			p, e := readScenarioPostgresSource(ctx, tx, ws, run.AssetID)
			if e != nil {
				return e
			}
			if e = evaluateScenarioFindings(ctx, tx, ws, &run, c, p); e != nil {
				return e
			}
		} else {
			run.AssetName = "运行时调查范围"
		}
		if e = applyScenarioRuntimeEvidence(ctx, tx, ws, &run); e != nil {
			return e
		}

		run.ReportRequirements, e = scenarioCheckReportRequirements(ctx, tx, ws, &run)
		if e != nil {
			return e
		}
		if e = tx.QueryRow(ctx, `SELECT username,clock_timestamp() FROM plume.users WHERE id=$1`, actor).Scan(&run.Actor, &run.CreatedAt); e != nil {
			return e
		}
		run.Outcome = "compliant"
		for _, f := range run.Findings {
			switch f.Outcome {
			case "compliant":
				run.Counts.Compliant++
			case "non_compliant":
				run.Counts.NonCompliant++
			case "needs_verification":
				run.Counts.NeedsVerification++
			}
		}
		if run.Counts.NeedsVerification > 0 {
			run.Outcome = "needs_verification"
		}
		if run.Counts.NonCompliant > 0 {
			run.Outcome = "non_compliant"
		}
		if input.PreviousRunID != nil {
			previous, e := readScenarioCheckRun(ctx, tx, ws, *input.PreviousRunID)
			if e != nil {
				return e
			}
			if previous.AssetID != run.AssetID || previous.ActivityID != run.ActivityID || previous.Scenario != run.Scenario || previous.Task != run.Task || !sameScenarioRuntimeScope(previous.Scope, run.Scope) {
				return ErrInvalid
			}
			run.RetestState = "same_source_observation"
			if scenarioRunHasNewObservation(previous, run) {
				run.RetestState = "new_source_observation"
			}
		}
		if e = tx.QueryRow(ctx, `INSERT INTO plume.scenario_check_runs(workspace_id,asset_id,activity_id,scenario,task,created_by,snapshot,snapshot_digest) VALUES($1,$2,NULLIF($3,0),$4,$5,$6,'{}','') RETURNING id`, ws, run.AssetID, run.ActivityID, run.Scenario, run.Task, actor).Scan(&run.ID); e != nil {
			return e
		}
		run.SnapshotDigest = ScenarioCheckRunSnapshotDigest(run)
		_, e = tx.Exec(ctx, `UPDATE plume.scenario_check_runs SET snapshot=$3,snapshot_digest=$4 WHERE workspace_id=$1 AND id=$2`, ws, run.ID, run, run.SnapshotDigest)
		return e
	})
	return run, storeError(e)
}
func scenarioRunHasNewObservation(previous, current ScenarioCheckRun) bool {
	for _, now := range current.Evidence {
		if now.Source != "postgresql" && now.Source != "discovery" && now.Source != "api_validation" && now.Source != "runtime" {
			continue
		}
		if now.ObservedAt == nil {
			continue
		}
		for _, old := range previous.Evidence {
			if old.ID == now.ID && old.Source == now.Source && old.ObservedAt != nil && now.ObservedAt.After(*old.ObservedAt) && now.Record != old.Record {
				return true
			}
		}
	}
	return false
}

// ScenarioCheckRunSnapshotDigest verifies the immutable result. Action links
// are a separate mutable workflow and are excluded from this original digest.
func ScenarioCheckRunSnapshotDigest(run ScenarioCheckRun) string {
	run.SnapshotDigest = ""
	run.Actions = nil
	run.Evidence = append([]ScenarioCheckEvidence(nil), run.Evidence...)
	for i := range run.Evidence {
		run.Evidence[i].Snapshot = scenarioCanonicalJSON(run.Evidence[i].Snapshot)
	}
	return assessmentDigest(run)
}
func scenarioCanonicalJSON(raw json.RawMessage) json.RawMessage {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return raw
	}
	canonical, e := json.Marshal(value)
	if e != nil {
		return raw
	}
	return canonical
}
func readScenarioCheckRun(ctx context.Context, tx pgx.Tx, ws, id int64) (ScenarioCheckRun, error) {
	var r ScenarioCheckRun
	e := tx.QueryRow(ctx, `SELECT snapshot FROM plume.scenario_check_runs WHERE workspace_id=$1 AND id=$2`, ws, id).Scan(&r)
	for i := range r.Evidence {
		r.Evidence[i].Snapshot = scenarioCanonicalJSON(r.Evidence[i].Snapshot)
	}
	return r, e
}

func (s *Store) GetScenarioCheckRun(ctx context.Context, ws, actor, id int64) (ScenarioCheckRun, error) {
	var r ScenarioCheckRun
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var e error
		r, e = readScenarioCheckRun(ctx, tx, ws, id)
		if e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT l.finding_id,a.id,a.status,a.revision,a.created_at,a.result FROM plume.scenario_check_remediations l JOIN plume.control_actions a ON a.id=l.action_id AND a.workspace_id=l.workspace_id WHERE l.workspace_id=$1 AND l.run_id=$2 ORDER BY a.id`, ws, id)
		if e != nil {
			return e
		}
		r.Actions, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ScenarioCheckActionLink, error) {
			var l ScenarioCheckActionLink
			e := row.Scan(&l.FindingID, &l.ActionID, &l.Status, &l.Revision, &l.CreatedAt, &l.Result)
			return l, e
		})
		return e
	})
	return r, storeError(e)
}
func (s *Store) ListScenarioCheckRuns(ctx context.Context, ws, actor int64, f ScenarioCheckRunFilter) ([]ScenarioCheckRun, error) {
	if f.AssetID < 0 || f.ActivityID < 0 || !validScenarioTask(f.Scenario, f.Task) {
		return nil, ErrInvalid
	}
	items := []ScenarioCheckRun{}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		if f.AssetID > 0 {
			var id int64
			if e := tx.QueryRow(ctx, `SELECT id FROM plume.assets WHERE workspace_id=$1 AND id=$2`, ws, f.AssetID).Scan(&id); e != nil {
				return e
			}
		}
		if f.ActivityID > 0 {
			var assetID int64
			if e := tx.QueryRow(ctx, `SELECT asset_id FROM plume.activities WHERE workspace_id=$1 AND id=$2`, ws, f.ActivityID).Scan(&assetID); e != nil {
				return e
			}
			if f.AssetID > 0 && f.AssetID != assetID {
				return ErrInvalid
			}
		}
		scoped := RuntimeEvidenceScope{}
		var from, to any
		if f.Scope != nil {
			scoped = *f.Scope
			normalized, e := normalizeRuntimeEvidenceScope(scoped)
			if e != nil {
				return e
			}
			scoped.ClientIP = normalized.ClientIP
			if !scoped.From.IsZero() {
				from = scoped.From
			}
			if !scoped.To.IsZero() {
				to = scoped.To
			}
			if scoped.SourceID > 0 {
				var id int64
				if e = tx.QueryRow(ctx, `SELECT id FROM plume.runtime_security_sources WHERE workspace_id=$1 AND id=$2`, ws, scoped.SourceID).Scan(&id); e != nil {
					return e
				}
			}
		}
		rows, e := tx.Query(ctx, `SELECT snapshot FROM plume.scenario_check_runs WHERE workspace_id=$1 AND ($2::bigint=0 OR asset_id=$2) AND ($3::bigint=0 OR activity_id=$3) AND ($4='' OR scenario=$4) AND ($5='' OR task=$5)
          AND ($6::bigint=0 OR (snapshot#>>'{scope,sourceId}')::bigint=$6) AND ($7='' OR snapshot#>>'{scope,service}'=$7) AND ($8='' OR snapshot#>>'{scope,method}'=$8) AND ($9='' OR snapshot#>>'{scope,route}'=$9) AND ($10='' OR snapshot#>>'{scope,subjectRef}'=$10) AND ($11='' OR snapshot#>>'{scope,clientIp}'=$11)
          AND ($12::timestamptz IS NULL OR COALESCE((snapshot#>>'{scope,to}')::timestamptz,created_at)>=$12) AND ($13::timestamptz IS NULL OR COALESCE((snapshot#>>'{scope,from}')::timestamptz,created_at)<=$13)
          ORDER BY id DESC LIMIT 50`, ws, f.AssetID, f.ActivityID, f.Scenario, f.Task, scoped.SourceID, scoped.Service, scoped.Method, scoped.Route, scoped.SubjectRef, scoped.ClientIP, from, to)
		if e != nil {
			return e
		}
		items, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ScenarioCheckRun, error) {
			var r ScenarioCheckRun
			e := row.Scan(&r)
			r.Findings = nil
			r.Evidence = nil
			r.ReportRequirements = nil
			return r, e
		})
		return e
	})
	return items, storeError(e)
}

func sameScenarioRuntimeScope(a, b *RuntimeEvidenceScope) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.SourceID == b.SourceID && a.Service == b.Service && a.Method == b.Method && runtimeRoute(a.Route) == runtimeRoute(b.Route) && a.SubjectRef == b.SubjectRef && a.ClientIP == b.ClientIP && assessmentDigest(a.ValueFingerprint) == assessmentDigest(b.ValueFingerprint)
}
