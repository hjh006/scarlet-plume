package httpapi

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

// A run is immutable. Linked actions are captured separately at export time,
// because a later receipt must never rewrite the old inspection conclusion.
type scenarioCheckReport struct {
	Version       string                          `json:"reportVersion"`
	ExportedAt    time.Time                       `json:"exportedAt"`
	Workspace     store.Workspace                 `json:"workspace"`
	Run           store.ScenarioCheckRun          `json:"run"`
	LinkedActions []store.ScenarioCheckActionLink `json:"linkedActions"`
	Boundary      string                          `json:"boundary"`
}

const scenarioReportBoundary = "符合与不符合仅针对本次采用的技术规则及可观察范围。未采集、覆盖不足和需要业务判断的条件继续核实；修复执行回执不等于新来源复测或独立通过。正式报告适用性、签章、报送和保管按实际要求完成。"

func scenarioReport(run store.ScenarioCheckRun, ws store.Workspace) scenarioCheckReport {
	actions := run.Actions
	if actions == nil {
		actions = []store.ScenarioCheckActionLink{}
	}
	run.Actions = nil
	return scenarioCheckReport{Version: "scarlet-technical-check-report-v1", ExportedAt: time.Now().UTC(), Workspace: ws, Run: run, LinkedActions: actions, Boundary: scenarioReportBoundary}
}

func scenarioCheckLabel(value string) string {
	labels := map[string]string{
		"compliant": "符合", "non_compliant": "不符合", "needs_verification": "待核实",
		"available": "已具备", "partial": "部分具备", "missing": "待补充",
		"not_retested": "首次检查", "same_source_observation": "尚未取得新的来源观察", "new_source_observation": "采用了新的来源观察",
		"applicable": "适用", "conditional": "按条件适用", "unknown": "待核实适用性",
		"governance": "数据治理与防护", "domestic": "国内监管检查", "personal": "个人信息保护", "crossborder": "数据跨境检查", "ai": "AI数据安全", "sharing": "共享与委托", "incident": "风险监测与处置",
	}
	if label := labels[value]; label != "" {
		return label
	}
	if label := reportLabels[value]; label != "" {
		return label
	}
	return value
}

func scenarioCheckScopeText(run store.ScenarioCheckRun) string {
	if run.Scope == nil {
		return ""
	}
	scope := run.Scope
	return fmt.Sprintf("source=%d service=%s %s %s subject=%s ip=%s from=%s to=%s", scope.SourceID, scope.Service, scope.Method, scope.Route, scope.SubjectRef, scope.ClientIP, scope.From.Format(time.RFC3339), scope.To.Format(time.RFC3339))
}

func scenarioCheckCSV(run store.ScenarioCheckRun) ([]byte, error) {
	var out bytes.Buffer
	out.WriteString("\ufeff")
	w := csv.NewWriter(&out)
	headers := []string{"检查运行", "资产", "检查项", "结论", "要求代码", "依据", "条款", "依据版本", "依据链接", "规则版本", "期望状态", "实际观察", "证据引用", "观察覆盖", "待核实条件", "整改建议", "平台可修复", "修复类型", "修复限制", "快照摘要", "运行时来源范围"}
	if err := w.Write(headers); err != nil {
		return nil, err
	}
	for _, finding := range run.Findings {
		fix := "否"
		if finding.Remediation.Executable {
			fix = "是（受控修复申请）"
		}
		row := []string{fmt.Sprint(run.ID), run.AssetName, finding.Title, scenarioCheckLabel(finding.Outcome), finding.RequirementCode, finding.Basis.Title, finding.Basis.Clause, finding.Basis.Version, finding.Basis.URL, finding.RuleVersion, finding.Expected, finding.Observed, strings.Join(finding.EvidenceRefs, "；"), finding.Coverage, strings.Join(finding.MissingConditions, "；"), finding.Remediation.Advice, fix, scenarioCheckLabel(finding.Remediation.Kind), finding.Remediation.Reason, run.SnapshotDigest, scenarioCheckScopeText(run)}
		for i := range row {
			row[i] = classificationCSVCell(row[i])
		}
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return out.Bytes(), w.Error()
}

func scenarioCheckHTML(report scenarioCheckReport) ([]byte, error) {
	var out bytes.Buffer
	if err := scenarioCheckReportHTML.Execute(&out, report); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

type scenarioEvidenceFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

func scenarioCheckZIP(report scenarioCheckReport) ([]byte, error) {
	html, err := scenarioCheckHTML(report)
	if err != nil {
		return nil, err
	}
	csvData, err := scenarioCheckCSV(report.Run)
	if err != nil {
		return nil, err
	}
	runJSON, err := json.MarshalIndent(report.Run, "", "  ")
	if err != nil {
		return nil, err
	}
	actionsJSON, err := json.MarshalIndent(map[string]any{"capturedAt": report.ExportedAt, "scope": "export_time_linked_actions", "items": report.LinkedActions}, "", "  ")
	if err != nil {
		return nil, err
	}
	files := []struct {
		name string
		data []byte
	}{{"report.html", html}, {"run-snapshot.json", runJSON}, {"findings.csv", csvData}, {"linked-actions.json", actionsJSON}}
	evidenceIndex := []map[string]string{}
	for i, evidence := range report.Run.Evidence {
		name := fmt.Sprintf("evidence/%03d.json", i+1)
		raw, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			return nil, err
		}
		files = append(files, struct {
			name string
			data []byte
		}{name, raw})
		evidenceIndex = append(evidenceIndex, map[string]string{"id": evidence.ID, "file": name, "sourceDigest": evidence.Digest})
	}
	manifestFiles := make([]scenarioEvidenceFile, 0, len(files))
	for _, file := range files {
		sum := sha256.Sum256(file.data)
		manifestFiles = append(manifestFiles, scenarioEvidenceFile{Name: file.name, SHA256: hex.EncodeToString(sum[:]), Bytes: len(file.data)})
	}
	manifest, err := json.MarshalIndent(map[string]any{"version": report.Version, "runId": report.Run.ID, "runSnapshotDigest": report.Run.SnapshotDigest, "exportedAt": report.ExportedAt, "boundary": report.Boundary, "files": manifestFiles, "evidence": evidenceIndex, "integrityNote": "文件摘要用于完整性比对；不代表电子签章、外部审核或监管受理。"}, "", "  ")
	if err != nil {
		return nil, err
	}
	files = append(files, struct {
		name string
		data []byte
	}{"manifest.json", manifest})
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	for _, file := range files {
		header := &zip.FileHeader{Name: file.name, Method: zip.Deflate}
		header.SetModTime(report.ExportedAt)
		w, err := z.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err = w.Write(file.data); err != nil {
			return nil, err
		}
	}
	if err = z.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeScenarioCheckExport(w http.ResponseWriter, run store.ScenarioCheckRun, ws store.Workspace, format string) {
	report := scenarioReport(run, ws)
	var data []byte
	var err error
	contentType := "text/html; charset=utf-8"
	switch format {
	case "json":
		data, err = json.MarshalIndent(report, "", "  ")
		contentType = "application/json; charset=utf-8"
	case "csv":
		data, err = scenarioCheckCSV(report.Run)
		contentType = "text/csv; charset=utf-8"
	case "zip":
		data, err = scenarioCheckZIP(report)
		contentType = "application/zip"
	default:
		format = "html"
		data, err = scenarioCheckHTML(report)
	}
	if err != nil {
		fail(w, 500, "暂时无法生成本次技术检查报告，请重试。")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="scarlet-technical-check-%d.%s"`, run.ID, format))
	w.Header().Set("Cache-Control", "private, no-store")
	if format == "html" {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; sandbox")
	}
	_, _ = w.Write(data)
}

var scenarioCheckReportHTML = template.Must(template.New("technical-check").Funcs(template.FuncMap{
	"label": scenarioCheckLabel,
	"join":  func(values []string) string { return strings.Join(values, "；") },
	"date":  func(value time.Time) string { return value.In(reportClock).Format("2006-01-02 15:04:05") },
	"optionalDate": func(value *time.Time) string {
		if value == nil {
			return "未记录"
		}
		return value.In(reportClock).Format("2006-01-02 15:04:05")
	},
	"pretty": func(value any) string {
		raw, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return "无法显示结构化记录"
		}
		return string(raw)
	},
}).Parse(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"><title>赤翎 · 技术检查与证据报告</title><style>
*{box-sizing:border-box}body{margin:0;color:#15233d;background:#f5f8fc;font:14px/1.75 system-ui,"PingFang SC","Microsoft YaHei",sans-serif}main{max-width:1100px;padding:36px 28px;margin:auto}h1{font-size:28px;margin:8px 0}h2{font-size:20px;margin:0 0 16px}h3{font-size:16px;margin:0 0 12px}section,article{background:white;border:1px solid #dbe3ee;border-radius:10px;padding:22px;margin:20px 0}article{margin:16px 0}dl{display:grid;grid-template-columns:140px minmax(0,1fr);gap:8px 16px}dt{color:#5a6a83}dd{margin:0;overflow-wrap:anywhere;white-space:pre-wrap}pre{white-space:pre-wrap;overflow-wrap:anywhere;font-size:12px;max-height:none}a{color:#076ca7;overflow-wrap:anywhere}p,li{overflow-wrap:anywhere}.muted{color:#5a6a83}.summary{display:flex;gap:28px;flex-wrap:wrap}.summary strong{font-size:22px}.result{font-weight:700;color:#076ca7}table{width:100%;border-collapse:collapse;font-size:12px}th,td{text-align:left;vertical-align:top;padding:10px;border-bottom:1px solid #dbe3ee;overflow-wrap:anywhere}th{background:#eef4fb}.digest{font:12px/1.8 ui-monospace,monospace;overflow-wrap:anywhere}@media print{@page{size:A4;margin:16mm 12mm}body{background:white}main{max-width:none;padding:0}section,article{border-radius:0}h1,h2,h3{break-after:avoid}thead{display:table-header-group}tr{break-inside:avoid}}
</style></head><body><main><header><p>赤翎 · Scarlet Plume</p><h1>技术检查与证据报告</h1><p>{{.Workspace.Name}} · {{label .Run.Scenario}} · 检查 #{{.Run.ID}}</p><p class="muted">检查时间：{{date .Run.CreatedAt}}；导出时间：{{date .ExportedAt}}（北京时间）</p></header>
<section><h2>检查范围与采用结果</h2><dl><dt>资产</dt><dd>{{if .Run.AssetID}}{{.Run.AssetName}} · #{{.Run.AssetID}} · 分类修订 {{.Run.AssetRevision}}{{else}}按下列运行时范围检查，未限定数据库资产{{end}}</dd>{{with .Run.Scope}}<dt>运行时来源</dt><dd>来源 #{{.SourceID}} · 服务 {{.Service}} · {{.Method}} {{.Route}}</dd><dt>对象筛选</dt><dd>来源内主体 {{.SubjectRef}} · 来源报告 IP {{.ClientIP}}</dd>{{with .ValueFingerprint}}<dt>数据指纹筛选</dt><dd><pre>{{pretty .}}</pre></dd>{{end}}<dt>采用窗口</dt><dd>{{date .From}} 至 {{date .To}}（北京时间）</dd>{{end}}<dt>处理活动</dt><dd>{{if .Run.ActivityID}}#{{.Run.ActivityID}} · 修订 {{.Run.ActivityRevision}}{{else}}未限定处理活动{{end}}</dd><dt>检查规则版本</dt><dd>{{.Run.EngineVersion}}</dd><dt>数据方式</dt><dd>分析本次采用的已采集事实；不表示实时或全量扫描源系统。</dd><dt>检查结果</dt><dd class="result">{{label .Run.Outcome}}</dd><dt>复测观察</dt><dd>{{label .Run.RetestState}}{{with .Run.PreviousRunID}} · 前次检查 #{{.}}{{end}}</dd></dl><p class="summary"><span>符合项 <strong>{{.Run.Counts.Compliant}}</strong></span><span>不符合项 <strong>{{.Run.Counts.NonCompliant}}</strong></span><span>待核实项 <strong>{{.Run.Counts.NeedsVerification}}</strong></span></p><p>{{.Boundary}}</p>{{range .Run.Warnings}}<p class="muted">{{.}}</p>{{end}}<p class="digest">检查快照摘要：{{.Run.SnapshotDigest}}</p></section>
<section><h2>检查发现与整改建议</h2>{{range .Run.Findings}}<article><h3>{{.Title}} · {{label .Outcome}}</h3><dl><dt>要求与依据</dt><dd>{{.RequirementCode}} · {{.Basis.Title}} · {{.Basis.Clause}} · {{.Basis.Version}}{{if .Basis.URL}}<br><a href="{{.Basis.URL}}">查看依据</a>{{end}}</dd><dt>规则版本</dt><dd>{{.RuleVersion}}</dd><dt>期望状态</dt><dd>{{.Expected}}</dd><dt>实际观察</dt><dd>{{.Observed}}</dd><dt>证据引用</dt><dd>{{join .EvidenceRefs}}</dd><dt>覆盖范围</dt><dd>{{.Coverage}}</dd><dt>待核实条件</dt><dd>{{join .MissingConditions}}</dd><dt>整改建议</dt><dd>{{.Remediation.Advice}}</dd><dt>平台修复能力</dt><dd>{{if eq .Outcome "compliant"}}本项技术检查符合，无需发起修复。{{else}}{{if .Remediation.Executable}}支持发起受控修复申请 · {{label .Remediation.Kind}}{{else}}需按建议处理或补充执行器{{end}}；{{.Remediation.Reason}}{{end}}</dd>{{with .CheckID}}<dt>关联检查</dt><dd>#{{.}}</dd>{{end}}</dl></article>{{else}}<p>本次未记录可展示的技术检查项。</p>{{end}}</section>
<section><h2>监管与检查报告要求对照</h2><p>下列要求按实际适用条件核对，技术检查覆盖与完整报告的编制、签章和外部提交分别记录。</p>{{range .Run.ReportRequirements}}<article><h3>{{.Title}} · {{label .Status}}</h3><dl><dt>适用性</dt><dd>{{label .Applicability}}</dd><dt>依据</dt><dd>{{.Basis.Title}} · {{.Basis.Clause}} · {{.Basis.Version}}{{if .Basis.URL}}<br><a href="{{.Basis.URL}}">查看依据</a>{{end}}</dd><dt>所需内容与交付</dt><dd>{{join .RequiredDeliverables}}</dd><dt>本次覆盖</dt><dd>{{.Coverage}}</dd><dt>对应发现</dt><dd>{{join .FindingIDs}}</dd><dt>证据引用</dt><dd>{{join .EvidenceRefs}}</dd><dt>未完成条件</dt><dd>{{join .MissingConditions}}</dd><dt>外部与人工步骤</dt><dd>{{join .ExternalSteps}}</dd><dt>保管要求</dt><dd>{{.RetentionNote}}</dd></dl></article>{{else}}<p>本次未自动生成正式监管报告适用结论，请按实际检查要求补充。</p>{{end}}</section>
<section><h2>采用证据清单</h2><p>仅导出受限采集快照、来源引用和摘要；原始业务记录与完整日志按源系统授权管理。</p>{{range .Run.Evidence}}<article><h3>{{.ID}} · {{.Source}}</h3><dl><dt>来源记录</dt><dd>{{.Record}}</dd><dt>采集时点</dt><dd>{{optionalDate .ObservedAt}}</dd><dt>范围</dt><dd>{{.Scope}}</dd><dt>证据摘要</dt><dd class="digest">{{.Digest}}</dd></dl><details><summary>查看采用快照</summary><pre>{{pretty .Snapshot}}</pre></details></article>{{else}}<p>尚未取得可采用证据。</p>{{end}}</section>
<section><h2>导出时关联的修复进展</h2><p class="muted">以下为 {{date .ExportedAt}} 的处置记录快照，独立于原检查判定。新的执行回执须经过新来源复测及相应复核。</p>{{if .LinkedActions}}<table><thead><tr><th>发现 / 修复申请</th><th>阶段 / 修订</th><th>来源回执</th></tr></thead><tbody>{{range .LinkedActions}}<tr><td>{{.FindingID}} · #{{.ActionID}}</td><td>{{label .Status}} · v{{.Revision}}</td><td>{{with .Result}}{{pretty .}}{{else}}尚无来源执行回执{{end}}</td></tr>{{end}}</tbody></table>{{else}}<p>本次检查尚无关联修复申请。</p>{{end}}</section><footer><p class="muted">文件摘要用于完整性比对；本报告不包含电子签章或外部监管受理凭证。</p></footer></main></body></html>`))
