package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) reportRoutes(mux *http.ServeMux) {
	const path = "/api/v1/workspaces/{slug}/reports/compliance"
	mux.HandleFunc("GET "+path, s.workspace(false, s.reportPreview))
	mux.HandleFunc("GET "+path+"/export", s.workspace(false, s.reportExport))
	const archives = "/api/v1/workspaces/{slug}/reports/archives"
	mux.HandleFunc("GET "+archives, s.workspace(false, s.reportArchives))
	mux.HandleFunc("GET "+archives+"/{archive}", s.workspace(false, s.reportArchivePreview))
	mux.HandleFunc("GET "+archives+"/{archive}/export", s.workspace(false, s.reportArchiveExport))
}

func (s *server) loadReport(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) (store.ComplianceReport, bool) {
	activityID, ok := optionalQueryID(w, r, "activityId")
	if !ok {
		return store.ComplianceReport{}, false
	}
	report, err := s.store.ComplianceReport(r.Context(), workspace.ID, user.ID, activityID)
	switch {
	case errors.Is(err, store.ErrReportScopeTooLarge):
		fail(w, http.StatusUnprocessableEntity, fmt.Sprintf("报告超过 %d 项检查或 %d 条证据和操作记录。请缩小范围，可尝试按处理活动导出。", store.ReportCheckLimit, store.ReportTimelineLimit))
	case err != nil:
		storeFailure(w, err)
	default:
		return report, true
	}
	return store.ComplianceReport{}, false
}

func (s *server) reportPreview(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	if report, ok := s.loadReport(w, r, user, workspace); ok {
		respond(w, http.StatusOK, report)
	}
}

func (s *server) reportExport(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	format, ok := reportExportFormat(w, r)
	if !ok {
		return
	}
	if report, ok := s.loadReport(w, r, user, workspace); ok {
		writeReportExport(w, report, format)
	}
}

func reportExportFormat(w http.ResponseWriter, r *http.Request) (string, bool) {
	format := r.URL.Query().Get("format")
	switch format {
	case "", "html", "json":
		return format, true
	default:
		fail(w, http.StatusBadRequest, "请选择 HTML 或 JSON 报告格式。")
		return "", false
	}
}

func writeReportExport(w http.ResponseWriter, report store.ComplianceReport, format string) {
	if format == "json" {
		w.Header().Set("Content-Disposition", `attachment; filename="scarlet-compliance-report.json"`)
		respond(w, http.StatusOK, report)
		return
	}

	var document bytes.Buffer
	if err := complianceHTML.Execute(&document, report); err != nil {
		slog.Error("could not render compliance report")
		fail(w, http.StatusInternalServerError, "无法生成报告。")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="scarlet-compliance-report.html"`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; sandbox")
	_, _ = w.Write(document.Bytes())
}

var reportLabels = map[string]string{
	"block_api": "临时阻断API", "revoke_select": "撤销表级SELECT", "proposed": "待独立审批", "approved": "已审批，待来源执行", "applied": "来源回执执行成功", "failed": "来源回执执行失败", "rollback_requested": "已请求回滚，待来源回执", "rolled_back": "来源回执已回滚", "cleared": "来源报告消除", "observed": "来源报告发现",
	"sensitive_response": "敏感数据响应", "credential_response": "疑似凭据响应", "missing_auth": "需认证接口匿名成功", "subject_bulk_access": "主体访问频次超阈值", "error_spike": "接口错误比例超阈值", "off_hours": "非工作时段访问", "sql_injection_signal": "疑似SQL注入模式", "xss_signal": "疑似XSS模式", "path_traversal_signal": "疑似路径穿越模式",
	"fresh_source_after_manual_confirmation_required": "人工确认后需重新领取任务采集正向来源事实",
	"manual_classification_confirmation_required":     "需要人工确认后新的正向来源统计", "current_positive_content_scope_required": "当前正向内容统计范围尚未满足", "adopted_standard_unavailable": "所选标准自动规则不可用", "customer_collector_aggregate": "客户侧有界统计",
	"connected_source_scope_only": "仅覆盖已接入来源", "pattern_or_policy_signal_not_proven_vulnerability": "策略或模式命中不等于已证明漏洞", "recipient_authorization_not_verified": "接收者授权未核实", "external_destination_not_observed": "未观察外部目的地", "format_not_real_identity": "格式不等于真实身份", "requests_not_rows_or_people": "请求数不是数据行数或人数", "subject_scoped_to_source": "主体仅在同来源内比较", "absolute_window_threshold_not_historical_baseline": "绝对窗口阈值，不是历史基线", "fixed_offset_not_business_calendar": "固定时区，不含业务日历", "source_sample_limit_20000": "已达到来源样本上限", "auth_state_unknown": "部分认证状态未知", "source_declared_auth_state": "认证状态由授权来源声明", "credential_field_name_signal_requires_validation": "凭据字段名信号仍需核验",
	"scope_is_connected_sources":           "仅覆盖已接入且可见的来源",
	"runtime_window_7_days":                "运行时关联只覆盖最近7天",
	"database_family_is_not_table_lineage": "数据库类型关联不代表已定位具体库表",
	"unmatched_database_target":            "部分数据库目标未匹配到唯一的当前目录资产",
	"draft":                                "待取证", "in_review": "待复核", "needs_work": "待整改", "verified": "已验证",
	"observation": "初次取证", "remediation": "整改", "retest": "复测",
	"created": "创建检查", "evidence": "登记证据", "submitted": "提交复核", "changes": "要求整改",
	"postgres_binding": "关联 PostgreSQL 自动证据",
	"email":            "邮箱", "phone": "电话号码", "national_id": "身份标识", "financial_account": "金融账号",
	"credential": "凭据", "personal_name": "个人姓名", "address": "地址", "ip_address": "IP 地址",
	"sharing": "对外共享或委托", "cross_border": "数据出境", "ai": "AI 使用", "internal": "内部处理",
	"review_required": "待人工核实", "no_signal": "未发现规则信号",
	"sensitive_candidate": "发现疑似敏感字段", "no_sensitive_candidate": "未识别疑似敏感字段",
	"public_select": "发现 PUBLIC 直接 SELECT 授权", "named_select": "发现非所有者具名 SELECT 授权",
	"no_non_owner_select": "未观察到非所有者直接 SELECT 授权",
	"detected":            "已识别属性信号", "insufficient_data": "采证条件不足",
	"matched": "与当前分类相容", "conflict": "与当前分类存在差异", "unconfirmed": "待确认分类", "insufficient": "继续采证",
	"classification_matched": "自动识别属性与分类相容", "classification_conflict": "自动识别与当前分类存在差异",
	"classification_unconfirmed": "已有分类待确认", "classification_insufficient": "自动定级条件待补充",
	"column_count": "字段数", "constraint_count": "约束数", "estimated_row_count": "估计行数", "row_security": "行级安全",
	"sampled_row_count": "本次观察行数", "metadata_candidate": "元数据识别", "format_match": "格式校验命中", "content_scope": "内容统计范围",
	"catalog": "数据库目录", "column_name": "字段名", "column_comment": "字段注释", "source_aggregate": "源内聚合",
}

var classificationReportLabels = map[string]string{
	"origin_manual": "人工确认", "origin_automatic": "自动识别",
	"category_unknown": "待识别", "category_business": "业务数据", "category_personal": "个人信息", "category_sensitive_personal": "敏感个人信息",
	"business_unknown": "待分级", "business_internal": "企业内部", "business_confidential": "机密",
	"statutory_unknown": "待评定", "statutory_general": "一般数据", "statutory_important": "重要数据", "statutory_core": "核心数据",
}

var reportClock = time.FixedZone("CST", 8*60*60)

var complianceHTML = template.Must(template.New("compliance").Funcs(template.FuncMap{
	"label": func(key string) string {
		if label, ok := reportLabels[key]; ok {
			return label
		}
		return key
	},
	"eventLabel": func(key string) string {
		if key == "verified" {
			return "复核通过"
		}
		return reportLabels[key]
	},
	"classLabel": func(kind, value string) string {
		if label, ok := classificationReportLabels[kind+"_"+value]; ok {
			return label
		}
		return value
	},
	"optionalDate": func(t *time.Time) string {
		if t == nil {
			return "—"
		}
		return t.In(reportClock).Format("2006-01-02 15:04:05")
	},
	"date": func(t time.Time) string { return t.In(reportClock).Format("2006-01-02 15:04:05") },
}).Parse(`<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'">
<title>赤翎 · 合规检查报告</title>
<style>
:root{color-scheme:light;color:#17254a;background:#f1f5fb;font-family:system-ui,"PingFang SC","Microsoft YaHei",sans-serif}
*{box-sizing:border-box}body{margin:0}main{max-width:1050px;margin:auto;padding:2rem 1.25rem}
header{border-top:5px solid #3357aa;padding:.75rem 0 1.2rem}h1{margin:.25em 0;font-size:2.2rem}h2{margin-top:0}
.brand{font-weight:700;color:#a33731}.muted{color:#415478}.panel{background:#fff;border:1px solid #cdd9ed;border-radius:10px;padding:1.15rem;margin-bottom:1rem}
.summary{display:flex;gap:1rem;flex-wrap:wrap}.summary dt,.facts dt{font-weight:700}.summary dd,.facts dd{margin:0}.summary dd{font-size:1.35rem}
.facts{display:grid;grid-template-columns:minmax(5rem,max-content) 1fr;gap:.45rem 1rem}
.wrap{overflow-wrap:anywhere;white-space:pre-wrap}ul.records{padding-left:1.25rem}li{margin:.6rem 0}time{white-space:nowrap}
@media print{:root{background:#fff}main{max-width:none;padding:0}.panel{border-color:#aab5c7}h1,h2,h3{break-after:avoid}li{break-inside:avoid}}
</style>
</head>
<body>
<main>
<header>
<p class="brand">赤翎 · Scarlet Plume</p>
<h1>合规检查报告</h1>
<p>{{.Workspace.Name}} · {{.Workspace.Slug}}</p>
<p class="muted">生成时间 <time datetime="{{.GeneratedAt.Format "2006-01-02T15:04:05Z07:00"}}">{{date .GeneratedAt}}</time>（北京时间）</p>
</header>
<section class="panel">
<h2>报告范围</h2>
{{with .Scope.Source}}
<dl class="facts">
<dt>采集来源</dt><dd class="wrap">{{.Source}} · 任务 #{{.JobID}}</dd>
<dt>采集运行</dt><dd class="wrap">{{.RunID}}</dd>
<dt>观测时间</dt><dd>{{date .ObservedAt}}（北京时间）</dd>
</dl>
{{else with .Scope.Activity}}
<p>处理活动 {{.Name}} · {{label .Kind}} · 编号 #{{.ID}}</p>
<dl class="facts">
<dt>关联资产</dt><dd>{{.AssetName}} · {{.AssetSystem}}</dd>
<dt>处理目的</dt><dd class="wrap">{{.Purpose}}</dd>
{{if .Recipient}}<dt>接收方</dt><dd class="wrap">{{.Recipient}}</dd>{{end}}
{{if .Region}}<dt>处理地域</dt><dd class="wrap">{{.Region}}</dd>{{end}}
{{if .RetentionPolicy}}<dt>留存与训练条款</dt><dd class="wrap">{{.RetentionPolicy}}</dd>{{end}}
{{if .TransferPath}}<dt>出境路径</dt><dd class="wrap">{{.TransferPath}}</dd>{{end}}
{{if .Safeguards}}<dt>保护措施</dt><dd class="wrap">{{.Safeguards}}</dd>{{end}}
</dl>
{{else}}<p>工作区内已创建的全部检查</p>{{end}}
<p class="muted">{{.Scope.Basis}}</p>
</section>
{{with .Identification}}
<section class="panel">
<h2>本次自动识别</h2>
<dl class="summary">
<div><dt>表</dt><dd>{{.Tables}}</dd></div><div><dt>字段</dt><dd>{{.Columns}}</dd></div>
<div><dt>疑似敏感字段</dt><dd>{{.SensitiveCandidates}}</dd></div><div><dt>涉及表</dt><dd>{{.TablesWithCandidates}}</dd></div>
<div><dt>PUBLIC 表级 SELECT</dt><dd>{{.PublicTableSelect}} 张表</dd></div>
<div><dt>PUBLIC 字段级 SELECT</dt><dd>{{.PublicColumnSelect}} 个字段</dd></div>
</dl>
<p class="muted">采集范围包括字段、约束、直接授权及可读取范围内的有界格式统计。各项证据列明实际观察范围和仍需补充的条件。</p>
</section>
{{end}}
{{with .AssessmentReferences}}<section class="panel"><h2>关联活动的专项评估引用</h2><p class="muted wrap">{{.Basis}}</p>{{if .Truncated}}<p>最多列200项引用，请查专项评估完整范围。</p>{{end}}<ul>{{range .Items}}<li class="wrap">评估#{{.ID}} · {{.Title}} · {{.Kind}} · 状态{{.Status}} · 活动#{{.ActivityID}} · 模板#{{.TemplateRevisionID}} · 修订v{{.Revision}} · {{date .UpdatedAt}}{{if or .Stale .ReassessmentPending}} · 已标记需要重评{{end}}</li>{{end}}</ul></section>{{end}}
{{with .Operations}}<section class="panel"><h2>关联安全运营与处置回执</h2><p class="muted wrap">{{.Scope}}</p><p>归档时点 {{date .CapturedAt}}</p>{{range .Controls}}<article><h3>处置#{{.ID}} · {{label .Kind}} · {{label .Status}}</h3><p>检查{{with .CheckID}}#{{.}}{{end}} · 修订v{{.Revision}} · 申请 {{.Actor}} · 审批 {{.ApprovedBy}} · 到期 {{date .ExpiresAt}}</p><p class="wrap">{{.Spec.Service}} {{.Spec.Method}} {{.Spec.Route}} {{.Spec.Database}} / {{.Spec.Schema}} / {{.Spec.Table}} / {{.Spec.Role}}</p><p class="wrap">{{.Reason}}</p>{{with .Result}}<p>执行回执 {{.Phase}} · {{if .Success}}来源报告成功{{else}}来源报告失败{{end}} · {{label .Code}} · {{date .ObservedAt}}</p>{{end}}{{if .Events}}<ul>{{range .Events}}<li class="wrap">{{label .Action}} · {{.Actor}} · {{date .CreatedAt}} · {{.Note}}</li>{{end}}</ul>{{end}}</article>{{end}}{{if .SecurityEvents}}<h3>每项检查的最新来源安全信号</h3><ul>{{range .SecurityEvents}}<li class="wrap">检查#{{.CheckID}} · 来源 {{.Source}} #{{.SourceID}} · {{.Category}} / {{.Rule}} · {{.Severity}} · {{label .Outcome}} · 计数{{.Count}} / {{.WindowSeconds}}秒 · {{date .ObservedAt}}</li>{{end}}</ul>{{end}}</section>{{end}}
{{with .CatalogDiscovery}}
<section class="panel"><h2>多源目录与分类证据</h2><p class="muted wrap">{{.Basis}}</p><p>目录表/集合 {{.Tables}} · 字段/路径 {{.Fields}} · 最新规则证据 {{.RuleHits}}{{if .Truncated}} · 达到报告范围上限{{end}}</p>
<ul>{{range .Sources}}<li>{{.Source}} · {{.Provider}} / {{.System}} · 配置v{{.Revision}}{{with .RunID}} · 运行#{{.}}{{end}} · {{if .Current}}当前有效{{else}}当前未观察、失败、暂停或过期{{end}} · {{optionalDate .ObservedAt}}</li>{{end}}</ul>
{{range .Assessments}}<div class="panel"><p>检查#{{.CheckID}} · 证据#{{.EvidenceID}} · {{label .Kind}} · {{if .Current}}当前来源事实仍相同{{else}}历史证据，当前事实需重新核对{{end}}</p>{{template "catalog-assessment" .Assessment}}</div>{{end}}</section>
{{end}}
{{with .DataFlows}}
<section class="panel">
<h2>工作区数据流快照摘要</h2>
<p>快照 #{{.SnapshotID}} · {{date .CapturedAt}}（北京时间）</p>
<dl class="summary">
<div><dt>应用</dt><dd>{{.Summary.Applications}}</dd></div><div><dt>接口</dt><dd>{{.Summary.APIs}}</dd></div>
<div><dt>库表</dt><dd>{{.Summary.Tables}}</dd></div><div><dt>去标识主体</dt><dd>{{.Summary.Subjects}}</dd></div>
<div><dt>节点</dt><dd>{{.Summary.Nodes}}</dd></div><div><dt>关系</dt><dd>{{.Summary.Edges}}</dd></div>
</dl>
<p class="muted">{{.Basis}}</p>
{{if .Warnings}}<p>快照覆盖说明：{{range .Warnings}}<span class="wrap">{{label .}} </span>{{end}}</p>{{end}}
</section>
{{end}}
<section class="panel">
<h2>执行概况</h2>
<dl class="summary">
<div><dt>检查</dt><dd>{{.Counts.Total}}</dd></div><div><dt>待取证</dt><dd>{{.Counts.Draft}}</dd></div>
<div><dt>待复核</dt><dd>{{.Counts.InReview}}</dd></div><div><dt>待整改</dt><dd>{{.Counts.NeedsWork}}</dd></div>
<div><dt>已验证</dt><dd>{{.Counts.Verified}}</dd></div><div><dt>关联模板</dt><dd>{{.Counts.Templated}}</dd></div>
</dl>
<p>证据 {{.Counts.Evidence}} 条，含整改 {{.Counts.Remediations}} 条、复测 {{.Counts.Retests}} 条。复核决定 {{.Counts.Reviews}} 次。</p>
</section>
{{range .Checks}}
<article class="panel">
<h2>#{{.Check.ID}} · {{.Check.Requirement.Title}}</h2>
<dl class="facts">
<dt>状态</dt><dd>{{label .Check.Status}}</dd>
{{with .LatestReview}}<dt>最近复核</dt><dd class="wrap">{{eventLabel .Action}} · {{.Actor}} · {{date .CreatedAt}}{{if .Note}}<br>{{.Note}}{{end}}</dd>{{end}}
<dt>资产</dt><dd>{{.Check.AssetName}} · {{.Check.AssetSystem}}</dd>
{{with .Check.Activity}}<dt>关联活动</dt><dd>#{{.ID}} · {{.Name}} · {{label .Kind}}</dd>{{end}}
{{with .Check.DjangoID}}<dt>Django 原编号</dt><dd>#{{.}}</dd>{{end}}
<dt>责任团队</dt><dd>{{.Check.OwnerTeam}}</dd>
<dt>监管依据</dt><dd class="wrap">{{.Check.Requirement.Code}} · {{.Check.Requirement.Citation}}
{{.Check.Requirement.SourceURL}}</dd>
<dt>实际检查范围</dt><dd class="wrap">{{.Check.Note}}</dd>
<dt>创建时间</dt><dd>{{date .Check.CreatedAt}}</dd>
<dt>最近更新</dt><dd>{{date .Check.UpdatedAt}}</dd>
</dl>
{{with .Check.Template}}
<h3>模板 {{.Title}} · v{{.Version}} · {{.Item.Title}}</h3>
<dl class="facts">
<dt>模板与检查项</dt><dd>{{.Key}} / {{.Item.Key}}</dd>
<dt>适用条件</dt><dd class="wrap">{{.Item.Applicability}}</dd>
<dt>取证要求</dt><dd class="wrap">{{.Item.EvidenceRequirements}}</dd>
<dt>验收标准</dt><dd class="wrap">{{.Item.AcceptanceCriteria}}</dd>
</dl>
{{else}}<p class="muted">未关联模板</p>{{end}}
{{with .Classification}}
<h3>分类分级记录</h3>
<dl class="facts">
<dt>来源和版本</dt><dd>{{classLabel "origin" .Origin}} · v{{.Revision}}</dd>
<dt>数据属性</dt><dd>{{classLabel "category" .Asset.Category}}</dd>
<dt>内部级别</dt><dd>{{classLabel "business" .Asset.BusinessLevel}}</dd>
<dt>法定类别</dt><dd>{{classLabel "statutory" .Asset.StatutoryCategory}}</dd>
<dt>判定依据</dt><dd class="wrap">{{.Asset.ClassificationBasis}}</dd>
{{with .Standard}}<dt>采用标准</dt><dd class="wrap">{{.Code}} · {{.Title}}{{if .LevelLabel}} · {{.LevelLabel}}{{end}}{{if .RuleClause}} · {{.RuleClause}}{{end}}</dd>{{end}}
</dl>
{{with .Automatic}}{{template "automatic-classification" .}}{{end}}
{{end}}
{{with .AutomaticEvaluation}}
<h3>本次自动判定</h3>
<p><strong>{{label .Assessment}}</strong>{{range .AssessmentReasons}} · {{label .}}{{end}}</p>
<p class="wrap muted">{{.Scope}}</p>
{{end}}
{{with .RuntimeFinding}}<h3>运行时发现 · {{label .Rule}}</h3><p>{{.Service}} · {{.Method}} {{.Route}} · 来源 {{.Source}} #{{.SourceID}}</p>{{with .Detection}}{{template "runtime-detection" .}}{{end}}{{end}}
<h3>证据记录</h3>
{{if .Evidence}}<ul class="records">{{range .Evidence}}
<li><strong>{{label .Kind}} · {{.SourceSystem}}</strong><br>
引用 <span class="wrap">{{.SourceRecord}}</span> · 观察 {{date .ObservedAt}} · {{.RecordedBy}} 于 {{date .CreatedAt}} 登记
<div class="wrap">{{.Summary}}</div>
{{if .AutomationJobID}}<div class="wrap">自动采集 · 任务 #{{.AutomationJobID}} · 运行 {{.AutomationRunID}}</div>{{end}}
{{with .Feishu}}<div class="wrap">飞书目标摘要 {{.TargetDigest}} · 快照摘要 {{.SnapshotDigest}} · {{if .MemberPresent}}存在直接授权 {{.Permission}}{{else}}未观察到指定直接授权{{end}}</div>{{end}}
{{with .Catalog}}{{template "catalog-assessment" .}}{{end}}
{{with .Postgres}}
<div class="wrap">PostgreSQL 目录观测 · {{.Database}} · {{.SourceRef}}</div>
{{if .Assessment}}<div>自动判定 {{label .Assessment}}{{range .AssessmentReasons}} · {{label .}}{{end}}</div>{{end}}
<div class="wrap">{{.Scope}}</div>
<div>字段 {{.ColumnCount}} 个 · 疑似敏感 {{.SensitiveCandidateCount}} 个 · 直接 ACL {{.GrantCount}} 项</div>
<div>PUBLIC 表级 SELECT {{if .PublicSelect}}已观察到{{else}}未观察到{{end}} · PUBLIC 列级 SELECT {{.PublicSelectColumnCount}} 列 · PUBLIC schema USAGE {{if .PublicSchemaUsage}}已观察到{{else}}未观察到{{end}}</div>
{{if .SensitiveFields}}<div>候选字段 {{range $index, $field := .SensitiveFields}}{{if $index}}、{{end}}<span class="wrap">{{$field.Name}}（{{label $field.Sensitivity}}）</span>{{end}}{{if .SensitiveFieldsOmitted}}，另有 {{.SensitiveFieldsOmitted}} 项{{end}}</div>{{end}}
{{if .SelectGrants}}<div>非所有者 SELECT {{range $index, $grant := .SelectGrants}}{{if $index}}、{{end}}<span class="wrap">{{$grant.Grantee}}{{if $grant.Column}} / {{$grant.Column}}{{else}} / 整表{{end}}（schema USAGE {{if $grant.SchemaUsage}}已观察到{{else}}未观察到{{end}}）</span>{{end}}{{if .SelectGrantsOmitted}}，另有 {{.SelectGrantsOmitted}} 项{{end}}</div>{{end}}
{{with .Classification}}{{template "automatic-classification" .}}{{end}}
{{end}}</li>
{{end}}</ul>{{else}}<p class="muted">暂无证据记录</p>{{end}}
<h3>操作和独立复核</h3>
{{if .Events}}<ul class="records">{{range .Events}}
<li><strong>{{eventLabel .Action}}</strong> · {{.Actor}} · {{date .CreatedAt}}{{if .Note}}<div class="wrap">{{.Note}}</div>{{end}}</li>
{{end}}</ul>{{else}}<p class="muted">暂无操作记录</p>{{end}}
</article>
{{else}}
<p class="panel muted">当前范围暂无检查。</p>
{{end}}
</main>
</body>
</html>
{{define "catalog-assessment"}}
<h4>不可变目录评估 #{{.ID}}</h4><p class="wrap">{{.System}} / {{.Database}} / {{.Schema}} / {{.Table}} · 来源 {{.Source}} #{{.SourceID}} 配置v{{.SourceRevision}} · 运行#{{.RunID}} · 分类v{{.ClassificationRevision}} · {{date .ObservedAt}}</p><p>{{label .Comparison}}。当前复核还需满足来源、规则、人工分类版本及整改周期的时效条件。</p>
{{with .Evaluation}}<p>{{.Standard.Code}} · {{.Standard.Title}} · {{.Standard.URL}}</p><ul>{{range .Rules}}<li class="wrap">{{.Rule.ID}} · {{.Rule.Clause}} · {{.Rule.Title}} · {{label .Status}}<div>{{.Rule.Criterion}}</div><div>取证 {{.Rule.Evidence}}</div>{{range .MissingConditions}}<div>待采证 {{.}}</div>{{end}}</li>{{end}}</ul>{{end}}
<ul>{{range .MissingConditions}}<li class="wrap">{{label .}}</li>{{end}}</ul><p>采用的客户及内置识别规则：</p><ul>{{range .AppliedRules}}<li>{{.Key}} v{{.Version}} · {{.Name}} · {{.Target}}</li>{{end}}</ul><ul>{{range .Fields}}{{$field:=.Name}}{{range .Hits}}<li class="wrap">{{$field}} · {{.RuleKey}} v{{.RuleVersion}} · {{label .Category}} · {{.Method}} · 命中{{.MatchCount}} / 观察{{.ScannedCount}}</li>{{end}}{{end}}</ul><p class="wrap">元数据 {{.Coverage.Metadata}} · 内容 {{.Coverage.Content}} · 权限 {{.Coverage.Permissions}} · 血缘 {{.Coverage.Lineage}}</p><ul>{{range .Coverage.Warnings}}<li class="wrap">{{label .}}</li>{{end}}</ul>
{{end}}
{{define "runtime-detection"}}
<h4>策略与观测依据</h4><dl class="facts"><dt>来源</dt><dd>{{.Source}} #{{.SourceID}}</dd><dt>命中时策略</dt><dd>{{label .Policy.Rule}} · {{if .Policy.Revision}}v{{.Policy.Revision}} #{{.Policy.ID}}{{else}}系统默认{{end}} · 窗口{{.Policy.WindowMinutes}}分钟 · 命中阈值{{.Policy.Threshold}} · 最少请求{{.Policy.MinRequests}}</dd><dt>实际窗口</dt><dd>{{date .WindowStart}} 至 {{date .WindowEnd}}</dd><dt>实际统计分母</dt><dd>请求{{.Requests}} · 信号命中{{.Matched}} / 可观察分母{{.Observed}}；字段计数可能包含同响应多个字段，不是唯一数据行数或人数。</dd>{{if .SubjectRef}}<dt>去标识主体</dt><dd class="wrap">{{.SubjectRef}}（仅同来源内比较）</dd>{{end}}{{if eq .Policy.Rule "error_spike"}}<dt>错误比例阈值</dt><dd>{{.Policy.ErrorPercent}}%</dd>{{end}}{{if eq .Policy.Rule "off_hours"}}<dt>时段</dt><dd>{{.Policy.StartHour}}时至{{.Policy.EndHour}}时 · UTC偏移{{.Policy.TimezoneOffsetMinutes}}分钟</dd>{{end}}</dl><p>模式和策略信号不能独立证明漏洞、泄露或越权。</p><ul>{{range .Warnings}}<li class="wrap">{{label .}}</li>{{end}}</ul>
{{end}}
{{define "automatic-classification"}}
<h4>自动分类与采证快照</h4>
<dl class="facts">
<dt>采集运行</dt><dd class="wrap">任务 #{{.JobID}} · {{.RunID}} · {{date .ObservedAt}}</dd>
<dt>比对结果</dt><dd class="wrap">{{label .Comparison}} · 分类版本 {{.ClassificationRevision}}{{if .Stale}} · 当前条件已变化，需重新采集{{end}}{{range .ComparisonReasons}}<br>{{.}}{{end}}</dd>
<dt>采用标准</dt><dd class="wrap">{{.Evaluation.Standard.Code}} · {{.Evaluation.Standard.Title}}<br>{{.Evaluation.Standard.URL}}</dd>
<dt>自动属性判定</dt><dd>{{label .Evaluation.Status}} · {{classLabel "category" .Evaluation.Category}} · {{classLabel "business" .Evaluation.BusinessLevel}}</dd>
</dl>
<h4>自动获取的条件</h4>
<ul class="records">{{range .Evaluation.Facts}}<li class="wrap">{{label .Kind}}{{if .Column}} · {{.Column}}{{end}}{{if .Category}} · {{label .Category}}{{end}}{{if .Count}} · {{.Count}}{{end}} · {{label .Source}}{{if .Note}}<br>{{.Note}}{{end}}</li>{{else}}<li>当前条件待采集</li>{{end}}</ul>
<h4>规则与取证要求</h4>
<ul class="records">{{range .Evaluation.Rules}}<li class="wrap"><strong>{{.Rule.Clause}} · {{.Rule.Title}} · {{label .Status}}</strong><br>{{.Rule.Criterion}}<br>取证要求 {{.Rule.Evidence}}
{{range .Facts}}<div>{{label .Kind}}{{if .Column}} · {{.Column}}{{end}}{{if .Category}} · {{label .Category}}{{end}}{{if .Count}} · {{.Count}}{{end}} · {{label .Source}}</div>{{end}}
{{range .MissingConditions}}<div>待采证 {{.}}</div>{{end}}</li>{{else}}<li>所选版本的正式条款规则待补充</li>{{end}}</ul>
<h4>定级采证缺口</h4><ul>{{range .Evaluation.MissingConditions}}<li class="wrap">{{.}}</li>{{end}}</ul>
{{end}}`))
