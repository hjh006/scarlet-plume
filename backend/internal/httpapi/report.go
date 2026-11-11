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
<h3>证据记录</h3>
{{if .Evidence}}<ul class="records">{{range .Evidence}}
<li><strong>{{label .Kind}} · {{.SourceSystem}}</strong><br>
引用 <span class="wrap">{{.SourceRecord}}</span> · 观察 {{date .ObservedAt}} · {{.RecordedBy}} 于 {{date .CreatedAt}} 登记
<div class="wrap">{{.Summary}}</div>
{{if .AutomationJobID}}<div class="wrap">自动采集 · 任务 #{{.AutomationJobID}} · 运行 {{.AutomationRunID}}</div>{{end}}
{{with .Feishu}}<div class="wrap">飞书目标摘要 {{.TargetDigest}} · 快照摘要 {{.SnapshotDigest}} · {{if .MemberPresent}}存在直接授权 {{.Permission}}{{else}}未观察到指定直接授权{{end}}</div>{{end}}
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
