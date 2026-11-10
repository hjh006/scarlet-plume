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
	format := r.URL.Query().Get("format")
	switch format {
	case "", "html", "json":
	default:
		fail(w, http.StatusBadRequest, "请选择 HTML 或 JSON 报告格式。")
		return
	}
	report, ok := s.loadReport(w, r, user, workspace)
	if !ok {
		return
	}
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
	"draft": "待取证", "in_review": "待复核", "needs_work": "待整改", "verified": "已验证",
	"observation": "初次取证", "remediation": "整改", "retest": "复测",
	"created": "创建检查", "evidence": "登记证据", "submitted": "提交复核", "changes": "要求整改",
	"sharing": "对外共享或委托", "cross_border": "数据出境", "ai": "AI 使用", "internal": "内部处理",
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
{{with .Scope.Activity}}
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
<h3>证据记录</h3>
{{if .Evidence}}<ul class="records">{{range .Evidence}}
<li><strong>{{label .Kind}} · {{.SourceSystem}}</strong><br>
引用 <span class="wrap">{{.SourceRecord}}</span> · 观察 {{date .ObservedAt}} · {{.RecordedBy}} 于 {{date .CreatedAt}} 登记
<div class="wrap">{{.Summary}}</div>
{{with .Feishu}}<div class="wrap">飞书目标摘要 {{.TargetDigest}} · 快照摘要 {{.SnapshotDigest}} · {{if .MemberPresent}}存在直接授权 {{.Permission}}{{else}}未观察到指定直接授权{{end}}</div>{{end}}</li>
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
</html>`))
