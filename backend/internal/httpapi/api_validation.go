package httpapi

import (
	"errors"
	"html/template"
	"net/http"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

const apiValidationClaimPath = "/api/v1/api-validation/claim"
const apiValidationResultPath = "/api/v1/api-validation/result"

func (s *server) apiValidationRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/api-validation"
	mux.HandleFunc("GET "+base+"/plans", s.workspace(false, s.apiValidationPlans))
	mux.HandleFunc("POST "+base+"/plans", s.workspace(false, s.saveAPIValidationPlan(false)))
	mux.HandleFunc("PUT "+base+"/plans/{plan}", s.workspace(false, s.saveAPIValidationPlan(true)))
	for _, action := range []string{"approve", "request", "disable"} {
		mux.HandleFunc("POST "+base+"/plans/{plan}/"+action, s.workspace(false, s.actAPIValidationPlan(action)))
	}
	mux.HandleFunc("GET "+base+"/plans/{plan}/runs", s.workspace(false, s.apiValidationRuns))
	mux.HandleFunc("GET "+base+"/plans/{plan}/export", s.workspace(false, s.apiValidationExport))
	mux.HandleFunc("POST "+base+"/plans/{plan}/runs/{run}/retry", s.workspace(false, s.retryAPIValidation))
	mux.HandleFunc("POST "+apiValidationClaimPath, s.operationsExternal("security_events", s.claimAPIValidation))
	mux.HandleFunc("POST "+apiValidationResultPath, s.operationsExternal("security_events", s.resultAPIValidation))
}
func apiValidationFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrAPIValidationProjection) {
		fail(w, 503, "验证结果已保存，安全事件汇总尚未完成；可安全重试同一结果或在界面重试汇总。")
		return
	}
	operationsFailure(w, err)
}
func (s *server) apiValidationPlans(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	items, err := s.store.ListAPIValidationPlans(r.Context(), ws.ID, user.ID)
	if err != nil {
		apiValidationFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items, "limit": 100})
}
func (s *server) saveAPIValidationPlan(update bool) func(http.ResponseWriter, *http.Request, store.User, store.Workspace) {
	return func(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
		var id int64
		if update {
			var ok bool
			id, ok = operationPathID(w, r, "plan")
			if !ok {
				return
			}
		}
		var input store.APIValidationPlanInput
		if !readJSON(w, r, &input) {
			return
		}
		item, err := s.store.SaveAPIValidationPlan(r.Context(), ws.ID, user.ID, id, input)
		if err != nil {
			apiValidationFailure(w, err)
			return
		}
		status := 201
		if update {
			status = 200
		}
		respond(w, status, item)
	}
}
func (s *server) actAPIValidationPlan(action string) func(http.ResponseWriter, *http.Request, store.User, store.Workspace) {
	return func(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
		id, ok := operationPathID(w, r, "plan")
		if !ok {
			return
		}
		var input struct {
			Revision int64  `json:"revision"`
			Mode     string `json:"mode,omitempty"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		item, err := s.store.ActAPIValidationPlan(r.Context(), ws.ID, user.ID, id, input.Revision, action, input.Mode)
		if err != nil {
			apiValidationFailure(w, err)
			return
		}
		respond(w, 200, item)
	}
}
func (s *server) apiValidationRuns(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "plan")
	if !ok {
		return
	}
	items, err := s.store.ListAPIValidationRuns(r.Context(), ws.ID, user.ID, id, 100)
	if err != nil {
		apiValidationFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items, "limit": 100, "retainedPerPlan": 200})
}
func (s *server) claimAPIValidation(w http.ResponseWriter, r *http.Request, digest string) {
	var input struct {
		SourceID int64 `json:"sourceId"`
	}
	if !readJSONLimit(w, r, &input, operationsBodyLimit) {
		return
	}
	claim, err := s.store.ClaimAPIValidation(r.Context(), digest, input.SourceID)
	if err != nil {
		apiValidationFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"claim": claim})
}
func (s *server) resultAPIValidation(w http.ResponseWriter, r *http.Request, digest string) {
	var input store.APIValidationResult
	if !readJSONLimit(w, r, &input, operationsBodyLimit) {
		return
	}
	run, replayed, err := s.store.SubmitAPIValidationResult(r.Context(), digest, input)
	if err != nil {
		apiValidationFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"run": run, "replayed": replayed})
}
func (s *server) retryAPIValidation(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "plan")
	if !ok {
		return
	}
	run, err := s.store.RetryAPIValidationProjection(r.Context(), ws.ID, user.ID, id, r.PathValue("run"))
	if err != nil {
		apiValidationFailure(w, err)
		return
	}
	respond(w, 200, run)
}

var apiValidationHTML = template.Must(template.New("validation").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>受限API验证报告</title><style>body{max-width:1100px;margin:40px auto;padding:20px;font:16px system-ui;color:#15233d}table{border-collapse:collapse;width:100%}td,th{border:1px solid #cdd8e5;padding:10px;text-align:left}pre{white-space:pre-wrap;overflow-wrap:anywhere}</style><h1>受限API验证报告</h1><p>{{.Note}}</p><p>生成 {{.GeneratedAt}}</p>{{range .Runs}}<h2>{{.Plan.Name}} · 计划v{{.Revision}}</h2><p>{{.Plan.Method}} {{.Plan.Route}} · {{.Plan.TargetOrigin}} · 来源#{{.Plan.SourceID}} · 本地引用 {{.Plan.ManifestRef}}</p><p>采用规则 {{.Plan.Rules}} v1（以所选规则为准）。状态 {{.State}} · 汇总 {{.ProjectionState}} · {{.ErrorCode}}</p><p>已批准检测基线：认证要求 {{.Plan.ProbePolicy.AuthRequired}}；{{range .Plan.ProbePolicy.SensitiveFields}}字段 {{.Path}} / {{.Category}}；{{end}}{{range .Plan.ProbePolicy.ResponseHeaders}}响应头 {{.Name}}: {{.Value}}；{{end}}</p><p>运行 {{.RunRef}} · {{.CreatedAt}} · 检查 {{.CheckIDs}}</p>{{if .Result}}<p>源端观察 {{.Result.ObservedAt}} · 模式 {{.Result.Mode}}</p><table><tr><th>规则</th><th>判断</th><th>源端布尔证明与缺口</th></tr>{{range .Result.Results}}<tr><td>{{.Rule}}</td><td>{{.Outcome}}</td><td>所有者确认 {{.OwnerConfirmed}} / B账号确认 {{.OtherAccountConfirmed}} / 同一对象 {{.SameObject}} / 已提供凭据接受 {{.CredentialAccepted}} / 弱规则命中 {{.WeakRuleMatched}} / 比较完成 {{.ComparisonComplete}}<p>状态码 A {{.OwnerStatus}} B自有 {{.OtherOwnedStatus}} B交叉 {{.OtherStatus}} · 请求{{.Requests}}</p><p>匿名状态 {{.AnonymousStatus}} / 同对象 {{.AnonymousObjectConfirmed}} / 基线匹配 {{.PolicyConfirmed}}；字段检查 {{.SensitiveChecked}} / A命中 {{.OwnerSensitiveCount}} / 匿名命中 {{.AnonymousSensitiveCount}}；响应头检查 {{.HeaderChecked}} / 不符 {{.HeaderMismatches}}；基线摘要 {{.PolicyDigest}}</p><p>{{.Gaps}}</p></td></tr>{{end}}</table>{{end}}{{end}}</html>`))

func (s *server) apiValidationExport(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "plan")
	if !ok {
		return
	}
	format := r.URL.Query().Get("format")
	if format != "" && format != "json" && format != "html" {
		apiValidationFailure(w, store.ErrInvalid)
		return
	}
	runs, err := s.store.ListAPIValidationRuns(r.Context(), ws.ID, user.ID, id, 1001)
	if err != nil {
		apiValidationFailure(w, err)
		return
	}
	if len(runs) > 1000 {
		fail(w, 422, "报告超过1000次运行，请缩小范围。")
		return
	}
	report := struct {
		GeneratedAt time.Time                `json:"generatedAt"`
		Runs        []store.APIValidationRun `json:"runs"`
		Note        string                   `json:"note"`
	}{time.Now().UTC(), runs, "仅覆盖已批准计划和本地白名单中的测试账号、合成对象。HTTP 200不等于对象越权；无法判定与连接诊断不会生成风险消除结论。报告固定每次采用的计划和规则版本，运行历史最多保留200条。"}
	if format == "html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="scarlet-api-validation.html"`)
		_ = apiValidationHTML.Execute(w, report)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="scarlet-api-validation.json"`)
	respond(w, 200, report)
}
