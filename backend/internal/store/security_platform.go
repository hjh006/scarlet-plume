package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const securityPlatformAssetLimit = 250

type SecurityCapability struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Stage    string   `json:"stage"`
	Records  int64    `json:"records"`
	Href     string   `json:"href"`
	Coverage string   `json:"coverage"`
	Warnings []string `json:"warnings"`
}
type SecurityPostureFinding struct {
	Key           string     `json:"key"`
	AssetID       int64      `json:"assetId"`
	AssetName     string     `json:"assetName"`
	Severity      string     `json:"severity"`
	Code          string     `json:"code"`
	Title         string     `json:"title"`
	Detail        string     `json:"detail"`
	SourceRef     string     `json:"sourceRef"`
	ObservedAt    *time.Time `json:"observedAt,omitempty"`
	EvidenceState string     `json:"evidenceState"`
}
type SecurityPlatformSnapshot struct {
	ID            int64     `json:"id"`
	Digest        string    `json:"digest"`
	CreatedAt     time.Time `json:"createdAt"`
	FindingsCount int       `json:"findingsCount"`
}
type SecurityNetworkBindingInput struct {
	ID               int64   `json:"id,omitempty"`
	ExpectedRevision int64   `json:"expectedRevision,omitempty"`
	Site             string  `json:"site"`
	Circuit          string  `json:"circuit"`
	VRF              string  `json:"vrf"`
	ApplicationRef   string  `json:"applicationRef"`
	AssetIDs         []int64 `json:"assetIds"`
	ActivityIDs      []int64 `json:"activityIds"`
	Region           string  `json:"region"`
	Receiver         string  `json:"receiver"`
	Purpose          string  `json:"purpose"`
	EvidenceRef      string  `json:"evidenceRef"`
}
type SecurityNetworkBinding struct {
	SecurityNetworkBindingInput
	Revision     int64     `json:"revision"`
	UpdatedAt    time.Time `json:"updatedAt"`
	EvidenceType string    `json:"evidenceType"`
}
type SecurityNetworkChangeInput struct {
	BindingID              int64  `json:"bindingId"`
	ExpectedRevision       int64  `json:"expectedRevision"`
	ProposedRegion         string `json:"proposedRegion"`
	ProposedApplicationRef string `json:"proposedApplicationRef"`
	Reason                 string `json:"reason"`
}
type SecurityNetworkChange struct {
	SecurityNetworkChangeInput
	ID                  int64                  `json:"id"`
	BindingRevision     int64                  `json:"bindingRevision"`
	Binding             SecurityNetworkBinding `json:"binding"`
	AffectedAssetIDs    []int64                `json:"affectedAssetIds"`
	AffectedActivityIDs []int64                `json:"affectedActivityIds"`
	Warnings            []string               `json:"warnings"`
	Status              string                 `json:"status"`
	CreatedBy           int64                  `json:"createdBy"`
	CreatedAt           time.Time              `json:"createdAt"`
	Reviewer            *int64                 `json:"reviewer,omitempty"`
	ReviewNote          string                 `json:"reviewNote,omitempty"`
	ReviewedAt          *time.Time             `json:"reviewedAt,omitempty"`
}
type SecurityPlatform struct {
	GeneratedAt     time.Time                  `json:"generatedAt"`
	Counts          map[string]int64           `json:"counts"`
	Capabilities    []SecurityCapability       `json:"capabilities"`
	Findings        []SecurityPostureFinding   `json:"findings"`
	NetworkBindings []SecurityNetworkBinding   `json:"networkBindings"`
	NetworkChanges  []SecurityNetworkChange    `json:"networkChanges"`
	Snapshots       []SecurityPlatformSnapshot `json:"snapshots"`
	Truncated       bool                       `json:"truncated"`
	Coverage        []string                   `json:"coverage"`
}

func securityPlatformMember(ctx context.Context, tx pgx.Tx, ws, actor int64, write bool) error {
	var role string
	if err := tx.QueryRow(ctx, `SELECT role FROM plume.memberships WHERE workspace_id=$1 AND user_id=$2`, ws, actor).Scan(&role); err != nil {
		return storeError(err)
	}
	if write && role != "owner" && role != "operator" {
		return ErrForbidden
	}
	return nil
}

func securityPostureFindings(asset Asset, classification *ScenarioClassification, pg *scenarioPostgresSource) []SecurityPostureFinding {
	out := []SecurityPostureFinding{}
	add := func(code, severity, title, detail, state string, observed *time.Time) {
		out = append(out, SecurityPostureFinding{Key: fmt.Sprintf("asset:%d/%s", asset.ID, code), AssetID: asset.ID, AssetName: asset.Name, Code: code, Severity: severity, Title: title, Detail: detail, SourceRef: asset.SourceRef, ObservedAt: observed, EvidenceState: state})
	}
	if classification == nil || !classification.Usable {
		add("classification_review", "medium", "数据分类需要确认", "候选识别、历史登记或已变化来源尚未形成当前可采用分类；请在分类工作台复核。", "needs_verification", nil)
	}
	if strings.TrimSpace(asset.OwnerTeam) == "" {
		add("missing_owner", "medium", "缺少数据责任团队", "为后续授权、整改和AI使用确认责任人。", "registered", nil)
	}
	if pg == nil {
		if asset.SourceProvider == "postgresql" {
			add("permission_coverage", "medium", "缺少权限来源", "当前资产没有受控PostgreSQL权限检查来源；不能据此推断权限安全。", "unknown", nil)
		}
		return out
	}
	observed := pg.Inspection.ObservedAt
	if !pg.Current || !pg.Complete {
		add("stale_permission_source", "medium", "权限证据待更新", "来源已暂停、撤销、修订、观察超时或采集不完整。历史权限保留，等待当前有效来源。", "historical", &observed)
		return out
	}
	table := pg.Inspection.Table
	public := table.PublicSchemaUsage && (table.PublicSelect || len(table.PublicSelectColumns) > 0)
	if public {
		severity := "medium"
		if classification != nil && classification.Usable && (asset.Category == "personal" || asset.Category == "sensitive_personal" || asset.BusinessLevel == "confidential") {
			severity = "high"
		}
		add("public_select", severity, "PUBLIC具备读取条件", "当前来源观察到PUBLIC schema使用权与表级或列级SELECT。此结果表示数据库授权暴露，不证明互联网可达或已经发生数据外发。", "observed", &observed)
	}
	for _, g := range table.Grants {
		if g.Privilege == "SELECT" && g.Grantable && g.SchemaUsage {
			add("delegable_select", "medium", "存在可转授的读取权限", "当前来源观察到WITH GRANT OPTION读取授权；请核对实际业务需要。角色继承与网络可达性需另行取得证据。", "observed", &observed)
			break
		}
	}
	return out
}

func (s *Store) SecurityPlatform(ctx context.Context, ws, actor int64) (SecurityPlatform, error) {
	var out SecurityPlatform
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if err := securityPlatformMember(ctx, tx, ws, actor, false); err != nil {
			return err
		}
		var err error
		out, err = readSecurityPlatform(ctx, tx, ws)
		return err
	})
	return out, storeError(err)
}

func readSecurityPlatform(ctx context.Context, tx pgx.Tx, ws int64) (SecurityPlatform, error) {
	out := SecurityPlatform{GeneratedAt: time.Now().UTC(), Counts: map[string]int64{}, Capabilities: []SecurityCapability{}, Findings: []SecurityPostureFinding{}, NetworkBindings: []SecurityNetworkBinding{}, NetworkChanges: []SecurityNetworkChange{}, Snapshots: []SecurityPlatformSnapshot{}, Coverage: []string{"DSPM使用当前资产及受控PostgreSQL权限来源；不推导完整IAM角色继承或互联网可达性。", "网络关联为授权人员登记，未伪装为设备实际观测；网络复核不修改线路或防火墙。", "AI策略评估与客户侧真实执行分别记录；仅有配置不能认定控制已生效。"}}
	counts := []struct{ key, query string }{
		{"assets", `SELECT count(*) FROM plume.assets WHERE workspace_id=$1`},
		{"classifiedAssets", `SELECT count(*) FROM plume.assets WHERE workspace_id=$1 AND category<>'unknown' AND classification_revision>0`},
		{"activities", `SELECT count(*) FROM plume.activities WHERE workspace_id=$1`},
		{"activeSources", `SELECT (SELECT count(*) FROM plume.automation_jobs WHERE workspace_id=$1 AND revoked_at IS NULL)+(SELECT count(*) FROM plume.runtime_security_sources WHERE workspace_id=$1 AND revoked_at IS NULL)+(SELECT count(*) FROM plume.discovery_sources WHERE workspace_id=$1 AND enabled)+(SELECT count(*) FROM plume.operations_sources WHERE workspace_id=$1 AND revoked_at IS NULL)+(SELECT count(*) FROM plume.application_sources WHERE workspace_id=$1 AND revoked_at IS NULL)`},
		{"openFindings", `SELECT count(*) FROM plume.runtime_findings f JOIN plume.checks c ON c.id=f.check_id AND c.workspace_id=f.workspace_id WHERE f.workspace_id=$1 AND c.status<>'verified'`},
		{"networkBindings", `SELECT count(*) FROM plume.security_network_bindings WHERE workspace_id=$1`},
		{"flowObservations", `SELECT count(*) FROM plume.data_flow_observations WHERE workspace_id=$1`},
		{"runtimeRoutes", `SELECT count(*) FROM plume.runtime_security_routes r JOIN plume.runtime_security_sources s ON s.id=r.source_id WHERE s.workspace_id=$1 AND s.revoked_at IS NULL`},
		{"reports", `SELECT count(*) FROM plume.operations_investigation_reports WHERE workspace_id=$1`},
		{"aiApplications", `SELECT COALESCE((SELECT CASE WHEN jsonb_typeof(configuration->'applications')='array' THEN jsonb_array_length(configuration->'applications') ELSE 0 END FROM plume.ai_security_configurations WHERE workspace_id=$1),0)`},
		{"aiDocuments", `SELECT COALESCE((SELECT CASE WHEN jsonb_typeof(configuration->'documents')='array' THEN jsonb_array_length(configuration->'documents') ELSE 0 END FROM plume.ai_security_configurations WHERE workspace_id=$1),0)`},
		{"aiModels", `SELECT COALESCE((SELECT CASE WHEN jsonb_typeof(configuration->'models')='array' THEN jsonb_array_length(configuration->'models') ELSE 0 END FROM plume.ai_security_configurations WHERE workspace_id=$1),0)`},
		{"aiTools", `SELECT COALESCE((SELECT CASE WHEN jsonb_typeof(configuration->'tools')='array' THEN jsonb_array_length(configuration->'tools') ELSE 0 END FROM plume.ai_security_configurations WHERE workspace_id=$1),0)`},
		{"aiPublishedRevision", `SELECT COALESCE((SELECT published_revision FROM plume.ai_security_configurations WHERE workspace_id=$1),0)`},
		{"aiRuntimeEvents", `SELECT count(*) FROM plume.ai_security_runtime_events e JOIN plume.ai_security_runtime_sources s ON s.id=e.source_id WHERE s.workspace_id=$1`},
		{"aiCurrentRuntimeEvents", `SELECT count(*) FROM plume.ai_security_runtime_events e JOIN plume.ai_security_runtime_sources s ON s.id=e.source_id JOIN plume.ai_security_configurations c ON c.workspace_id=s.workspace_id WHERE s.workspace_id=$1 AND s.revoked_at IS NULL AND s.catalog_digest=c.published_digest AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=s.workspace_id AND m.user_id=s.created_by AND m.role='owner')`},
	}
	for _, q := range counts {
		var n int64
		if err := tx.QueryRow(ctx, q.query, ws).Scan(&n); err != nil {
			return out, err
		}
		out.Counts[q.key] = n
	}
	rows, err := tx.Query(ctx, `SELECT `+assetColumns+` FROM plume.assets WHERE workspace_id=$1 ORDER BY id LIMIT $2`, ws, securityPlatformAssetLimit+1)
	if err != nil {
		return out, err
	}
	assets := []Asset{}
	for rows.Next() {
		a, e := scanAsset(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		assets = append(assets, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	out.Truncated = len(assets) > securityPlatformAssetLimit
	if out.Truncated {
		assets = assets[:securityPlatformAssetLimit]
		out.Coverage = append(out.Coverage, "本次DSPM采用按资产ID排序的前250项；其余资产未被本次检查覆盖。")
	}
	for _, a := range assets {
		c, e := scenarioClassification(ctx, tx, ws, a.ID)
		if e != nil {
			return out, e
		}
		p, e := readScenarioPostgresSource(ctx, tx, ws, a.ID)
		if e != nil {
			return out, e
		}
		out.Findings = append(out.Findings, securityPostureFindings(a, c, p)...)
	}
	slices.SortStableFunc(out.Findings, func(a, b SecurityPostureFinding) int {
		rank := func(s string) int {
			if s == "high" {
				return 0
			}
			if s == "medium" {
				return 1
			}
			return 2
		}
		return rank(a.Severity) - rank(b.Severity)
	})
	if err = readSecurityNetwork(ctx, tx, ws, &out); err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, `SELECT id,digest,created_at,jsonb_array_length(snapshot->'findings') FROM plume.security_platform_snapshots WHERE workspace_id=$1 ORDER BY id DESC LIMIT 30`, ws)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var p SecurityPlatformSnapshot
		if err = rows.Scan(&p.ID, &p.Digest, &p.CreatedAt, &p.FindingsCount); err != nil {
			rows.Close()
			return out, err
		}
		out.Snapshots = append(out.Snapshots, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	capabilities := []SecurityCapability{
		{ID: "discovery", Name: "数据发现与分类", Records: out.Counts["assets"], Href: "classification", Coverage: "原生数据库、已授权文件与目录来源；分类候选、人工确认、发布和质量验证分别留证。"},
		{ID: "dspm", Name: "数据安全态势管理", Records: int64(len(out.Findings)), Href: "security-platform", Coverage: "当前资产分类与有界PostgreSQL权限暴露；可冻结快照并进入现有整改复核。"},
		{ID: "flows", Name: "数据流向与使用治理", Records: out.Counts["flowObservations"], Href: "data-flows", Coverage: "声明活动与网关、OTLP、明确目标来源的观察分别展示，支持快照与差异比较。"},
		{ID: "ai_inventory", Name: "AI资产与供应链治理", Records: out.Counts["aiApplications"] + out.Counts["aiModels"] + out.Counts["aiTools"], Href: "ai-security", Coverage: "AI应用、模型、知识文档、主体、工具和策略的配置修订与发布评测；版本、来源和摘要登记不等于完成软件漏洞或供应链实测。"},
		{ID: "ai_data", Name: "AI数据安全", Records: out.Counts["aiDocuments"], Href: "ai-security?tab=rag", Coverage: "检索前ACL与数据分类、模型外发目标和地域策略；仅覆盖接入的执行路径。"},
		{ID: "ai_runtime", Name: "AI运行时防护", Records: out.Counts["aiCurrentRuntimeEvents"], Href: "ai-security?tab=runtime", Coverage: "输入输出检查、限定注入规则与模型请求执行；语义规则不能保证识别全部攻击。"},
		{ID: "agent", Name: "Agent与MCP安全", Records: out.Counts["aiTools"], Href: "ai-security?tab=agent", Coverage: "可信身份、工具动作与参数约束、高影响批准、有效期与重放检查；执行点须接入客户侧代理。"},
		{ID: "operations", Name: "持续评测与安全运营", Records: out.Counts["reports"] + out.Counts["aiRuntimeEvents"], Href: "ai-security?tab=evaluations", Coverage: "实际策略正反例、版本门禁、决策元数据、客户侧认证来源报告回执与现有安全运营报告。"},
	}
	for i := range capabilities {
		capabilities[i].Warnings = []string{}
		capabilities[i].Stage = "needs_source"
		if capabilities[i].Records > 0 {
			capabilities[i].Stage = "implemented"
		}
		if strings.HasPrefix(capabilities[i].ID, "ai_") || capabilities[i].ID == "agent" {
			capabilities[i].Stage = "needs_configuration"
			if out.Counts["aiApplications"] > 0 {
				capabilities[i].Stage = "needs_source"
			}
			if out.Counts["aiCurrentRuntimeEvents"] > 0 {
				capabilities[i].Stage = "implemented"
			}
			capabilities[i].Warnings = append(capabilities[i].Warnings, "配置条数与同版本来源回执分别列示；收到回执不等于平台已独立复测，也不能由某个动作外推全部AI能力。")
		}
		out.Capabilities = append(out.Capabilities, capabilities[i])
	}
	return out, nil
}

func (s *Store) CaptureSecurityPlatform(ctx context.Context, ws, actor int64) (SecurityPlatformSnapshot, error) {
	var result SecurityPlatformSnapshot
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if err := securityPlatformMember(ctx, tx, ws, actor, true); err != nil {
			return err
		}
		out, err := readSecurityPlatform(ctx, tx, ws)
		if err != nil {
			return err
		}
		out.Snapshots = []SecurityPlatformSnapshot{}
		body, err := json.Marshal(out)
		if err != nil {
			return err
		}
		result.Digest = assessmentDigest(scenarioCanonicalJSON(body))
		result.FindingsCount = len(out.Findings)
		return tx.QueryRow(ctx, `INSERT INTO plume.security_platform_snapshots(workspace_id,actor_id,digest,snapshot) VALUES($1,$2,$3,$4) RETURNING id,created_at`, ws, actor, result.Digest, body).Scan(&result.ID, &result.CreatedAt)
	})
	return result, storeError(err)
}

func (s *Store) SecurityPlatformSnapshot(ctx context.Context, ws, actor, id int64) (json.RawMessage, error) {
	var raw json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT snapshot FROM plume.security_platform_snapshots WHERE workspace_id=$1 AND id=$3 AND EXISTS(SELECT 1 FROM plume.memberships WHERE workspace_id=$1 AND user_id=$2)`, ws, actor, id).Scan(&raw)
	return raw, storeError(err)
}

func readSecurityNetwork(ctx context.Context, tx pgx.Tx, ws int64, out *SecurityPlatform) error {
	rows, err := tx.Query(ctx, `SELECT id,revision,configuration,updated_at FROM plume.security_network_bindings WHERE workspace_id=$1 ORDER BY id LIMIT 251`, ws)
	if err != nil {
		return err
	}
	for rows.Next() {
		var b SecurityNetworkBinding
		var config json.RawMessage
		if err = rows.Scan(&b.ID, &b.Revision, &config, &b.UpdatedAt); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(config, &b.SecurityNetworkBindingInput); err != nil {
			rows.Close()
			return err
		}
		b.EvidenceType = "declared"
		out.NetworkBindings = append(out.NetworkBindings, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(out.NetworkBindings) > 250 {
		out.NetworkBindings = out.NetworkBindings[:250]
		out.Truncated = true
		out.Coverage = append(out.Coverage, "网络关联仅展示前250项。")
	}
	rows, err = tx.Query(ctx, `SELECT id,binding_id,binding_revision,proposal,status,created_by,created_at,reviewed_by,review_note,reviewed_at FROM plume.security_network_changes WHERE workspace_id=$1 ORDER BY id DESC LIMIT 100`, ws)
	if err != nil {
		return err
	}
	for rows.Next() {
		c, err := scanSecurityNetworkChange(rows)
		if err != nil {
			rows.Close()
			return err
		}
		out.NetworkChanges = append(out.NetworkChanges, c)
	}
	err = rows.Err()
	rows.Close()
	return err
}

func securityText(s string, max int, required bool) bool {
	return (!required || strings.TrimSpace(s) != "") && utf8.RuneCountInString(s) <= max && strings.IndexFunc(s, unicode.IsControl) < 0
}

func securityProse(s string, max int, required bool) bool {
	return (!required || strings.TrimSpace(s) != "") && utf8.RuneCountInString(s) <= max && strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
	}) < 0
}
func validSecurityNetworkBinding(b SecurityNetworkBindingInput) bool {
	if b.ID < 0 || b.ExpectedRevision < 0 || !securityText(b.Site, 160, true) || !securityText(b.Circuit, 160, true) || !securityText(b.VRF, 160, false) || !securityText(b.ApplicationRef, 160, false) || !securityText(b.Region, 160, false) || !securityText(b.Receiver, 240, false) || !securityProse(b.Purpose, 2000, true) || !securityText(b.EvidenceRef, 500, true) || len(b.AssetIDs) > 100 || len(b.ActivityIDs) > 100 {
		return false
	}
	for _, ids := range [][]int64{b.AssetIDs, b.ActivityIDs} {
		seen := map[int64]bool{}
		for _, id := range ids {
			if id < 1 || seen[id] {
				return false
			}
			seen[id] = true
		}
	}
	return (b.ID == 0 && b.ExpectedRevision == 0) || (b.ID > 0 && b.ExpectedRevision > 0)
}

func (s *Store) SaveSecurityNetworkBinding(ctx context.Context, ws, actor int64, in SecurityNetworkBindingInput) (SecurityNetworkBinding, error) {
	var out SecurityNetworkBinding
	if !validSecurityNetworkBinding(in) {
		return out, ErrInvalid
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := securityPlatformMember(ctx, tx, ws, actor, true); err != nil {
			return err
		}
		for _, pair := range []struct {
			table string
			ids   []int64
		}{{"assets", in.AssetIDs}, {"activities", in.ActivityIDs}} {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM plume.`+pair.table+` WHERE workspace_id=$1 AND id=ANY($2::bigint[])`, ws, pair.ids).Scan(&n); err != nil {
				return err
			}
			if n != len(pair.ids) {
				return ErrNotFound
			}
		}
		out = SecurityNetworkBinding{SecurityNetworkBindingInput: in, EvidenceType: "declared"}
		config := in
		config.ID = 0
		config.ExpectedRevision = 0
		raw, err := json.Marshal(config)
		if err != nil {
			return err
		}
		if in.ID == 0 {
			return tx.QueryRow(ctx, `INSERT INTO plume.security_network_bindings(workspace_id,configuration,actor_id) VALUES($1,$2,$3) RETURNING id,revision,updated_at`, ws, raw, actor).Scan(&out.ID, &out.Revision, &out.UpdatedAt)
		}
		err = tx.QueryRow(ctx, `UPDATE plume.security_network_bindings SET configuration=$4,actor_id=$5,revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND id=$2 AND revision=$3 RETURNING revision,updated_at`, ws, in.ID, in.ExpectedRevision, raw, actor).Scan(&out.Revision, &out.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return err
	})
	out.ExpectedRevision = 0
	return out, storeError(err)
}

func networkChangeProposal(b SecurityNetworkBinding, in SecurityNetworkChangeInput) SecurityNetworkChange {
	out := SecurityNetworkChange{SecurityNetworkChangeInput: in, BindingRevision: b.Revision, Binding: b, AffectedAssetIDs: b.AssetIDs, AffectedActivityIDs: b.ActivityIDs, Warnings: []string{"本次为登记范围的变更影响复核，不表示网络设备已变更，也不证明实际数据处理位置。"}, Status: "pending_review"}
	if b.Region != in.ProposedRegion {
		out.Warnings = append(out.Warnings, "登记地域发生变化，请复核数据接收方、AI供应商、模型目标及允许的数据类别。")
	}
	if b.ApplicationRef != in.ProposedApplicationRef {
		out.Warnings = append(out.Warnings, "应用关联发生变化，请重新核对知识来源ACL、主体权限及工具授权。")
	}
	if len(b.AssetIDs) == 0 {
		out.Warnings = append(out.Warnings, "尚未关联数据资产，无法推导数据影响范围。")
	}
	if len(b.ActivityIDs) == 0 {
		out.Warnings = append(out.Warnings, "尚未关联处理活动，用途和接收关系需补充确认。")
	}
	return out
}

func (s *Store) CreateSecurityNetworkChange(ctx context.Context, ws, actor int64, in SecurityNetworkChangeInput) (SecurityNetworkChange, error) {
	var out SecurityNetworkChange
	if in.BindingID < 1 || in.ExpectedRevision < 1 || !securityText(in.ProposedRegion, 160, true) || !securityText(in.ProposedApplicationRef, 160, true) || !securityProse(in.Reason, 2000, true) {
		return out, ErrInvalid
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := securityPlatformMember(ctx, tx, ws, actor, true); err != nil {
			return err
		}
		var b SecurityNetworkBinding
		var raw json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT id,revision,configuration,updated_at FROM plume.security_network_bindings WHERE workspace_id=$1 AND id=$2 FOR SHARE`, ws, in.BindingID).Scan(&b.ID, &b.Revision, &raw, &b.UpdatedAt); err != nil {
			return storeError(err)
		}
		if b.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		if err := json.Unmarshal(raw, &b.SecurityNetworkBindingInput); err != nil {
			return err
		}
		b.EvidenceType = "declared"
		out = networkChangeProposal(b, in)
		out.CreatedBy = actor
		raw, err := json.Marshal(out)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO plume.security_network_changes(workspace_id,binding_id,binding_revision,proposal,created_by) VALUES($1,$2,$3,$4,$5) RETURNING id,created_at`, ws, b.ID, b.Revision, raw, actor).Scan(&out.ID, &out.CreatedAt)
	})
	return out, storeError(err)
}

func scanSecurityNetworkChange(row pgx.Row) (SecurityNetworkChange, error) {
	var c SecurityNetworkChange
	var raw json.RawMessage
	var id, binding, revision, actor int64
	var status, note string
	var created time.Time
	var reviewer *int64
	var reviewed *time.Time
	err := row.Scan(&id, &binding, &revision, &raw, &status, &actor, &created, &reviewer, &note, &reviewed)
	if err != nil {
		return c, storeError(err)
	}
	if err = json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	c.ID = id
	c.BindingID = binding
	c.BindingRevision = revision
	c.Status = status
	c.CreatedBy = actor
	c.CreatedAt = created
	c.Reviewer = reviewer
	c.ReviewNote = note
	c.ReviewedAt = reviewed
	return c, nil
}

func (s *Store) ReviewSecurityNetworkChange(ctx context.Context, ws, actor, id int64, decision, note string) (SecurityNetworkChange, error) {
	var out SecurityNetworkChange
	if id < 1 || (decision != "accepted" && decision != "rejected") || !securityProse(note, 2000, true) {
		return out, ErrInvalid
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(ctx, `SELECT role FROM plume.memberships WHERE workspace_id=$1 AND user_id=$2`, ws, actor).Scan(&role); err != nil {
			return storeError(err)
		}
		if role != "owner" && role != "reviewer" {
			return ErrForbidden
		}
		var err error
		out, err = scanSecurityNetworkChange(tx.QueryRow(ctx, `SELECT id,binding_id,binding_revision,proposal,status,created_by,created_at,reviewed_by,review_note,reviewed_at FROM plume.security_network_changes WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, id))
		if err != nil {
			return err
		}
		if out.CreatedBy == actor {
			return ErrForbidden
		}
		if out.Status != "pending_review" {
			return ErrConflict
		}
		var current int64
		if err = tx.QueryRow(ctx, `SELECT revision FROM plume.security_network_bindings WHERE workspace_id=$1 AND id=$2 FOR SHARE`, ws, out.BindingID).Scan(&current); err != nil {
			return storeError(err)
		}
		if current != out.BindingRevision {
			return ErrConflict
		}
		out.Status = decision
		out.Reviewer = &actor
		out.ReviewNote = note
		var reviewed time.Time
		err = tx.QueryRow(ctx, `UPDATE plume.security_network_changes SET status=$3,reviewed_by=$4,review_note=$5,reviewed_at=now() WHERE workspace_id=$1 AND id=$2 RETURNING reviewed_at`, ws, id, decision, actor, note).Scan(&reviewed)
		out.ReviewedAt = &reviewed
		return err
	})
	return out, storeError(err)
}
