package httpapi

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) operationsReportRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}"
	mux.HandleFunc("GET "+base+"/runtime-evidence", s.workspace(false, s.runtimeEvidence))
	mux.HandleFunc("GET "+base+"/operations-reports", s.workspace(false, s.listOperationsReports))
	mux.HandleFunc("POST "+base+"/operations-reports", s.workspace(false, s.createOperationsReport))
	mux.HandleFunc("GET "+base+"/operations-reports/{report}", s.workspace(false, s.getOperationsReport))
	mux.HandleFunc("GET "+base+"/operations-reports/{report}/export", s.workspace(false, s.exportOperationsReport))
	mux.HandleFunc("GET "+base+"/operations-report-schedules", s.workspace(false, s.listOperationsReportSchedules))
	mux.HandleFunc("POST "+base+"/operations-report-schedules", s.workspace(false, s.saveOperationsReportSchedule))
	mux.HandleFunc("PUT "+base+"/operations-report-schedules/{schedule}", s.workspace(false, s.saveOperationsReportSchedule))
	mux.HandleFunc("POST "+base+"/operations-report-schedules/{schedule}/run", s.workspace(false, s.runOperationsReportSchedule))
}
func runtimeEvidenceQuery(w http.ResponseWriter, r *http.Request) (*store.RuntimeEvidenceScope, bool) {
	q := r.URL.Query()
	scope := store.RuntimeEvidenceScope{}
	present := false
	for _, key := range []string{"sourceId", "service", "method", "route", "subjectRef", "clientIp", "from", "to"} {
		if len(q[key]) > 1 {
			fail(w, 400, "范围参数不可重复。")
			return nil, false
		}
		present = present || q.Get(key) != ""
	}
	if !present {
		return nil, true
	}
	var ok bool
	scope.SourceID, ok = optionalQueryID(w, r, "sourceId")
	if !ok {
		return nil, false
	}
	scope.Service = q.Get("service")
	scope.Method = q.Get("method")
	scope.Route = q.Get("route")
	scope.SubjectRef = q.Get("subjectRef")
	scope.ClientIP = q.Get("clientIp")
	for key, target := range map[string]*time.Time{"from": &scope.From, "to": &scope.To} {
		if q.Get(key) != "" {
			at, e := time.Parse(time.RFC3339Nano, q.Get(key))
			if e != nil {
				fail(w, 400, "时间范围请使用含时区的RFC3339格式。")
				return nil, false
			}
			*target = at
		}
	}
	return &scope, true
}
func (s *server) runtimeEvidence(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	scope, ok := runtimeEvidenceQuery(w, r)
	if !ok {
		return
	}
	if scope == nil {
		scope = &store.RuntimeEvidenceScope{}
	}
	item, e := s.store.RuntimeEvidence(r.Context(), ws.ID, u.ID, *scope)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 200, item)
}
func (s *server) listOperationsReports(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	scope, ok := runtimeEvidenceQuery(w, r)
	if !ok {
		return
	}
	if scope == nil {
		scope = &store.RuntimeEvidenceScope{}
	}
	items, e := s.store.ListOperationsReports(r.Context(), ws.ID, u.ID, *scope)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"items": items, "limit": 50})
}
func (s *server) createOperationsReport(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in store.OperationsReportInput
	if !readJSONLimit(w, r, &in, 8<<10) {
		return
	}
	item, e := s.store.CreateOperationsReport(r.Context(), ws.ID, u.ID, in)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 201, item)
}
func operationsPathID(w http.ResponseWriter, r *http.Request, key string) (int64, bool) {
	id, e := strconv.ParseInt(r.PathValue(key), 10, 64)
	if e != nil || id < 1 {
		fail(w, 404, "找不到该报告或计划。")
		return 0, false
	}
	return id, true
}
func (s *server) getOperationsReport(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := operationsPathID(w, r, "report")
	if !ok {
		return
	}
	item, e := s.store.GetOperationsReport(r.Context(), ws.ID, u.ID, id)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 200, item)
}
func (s *server) listOperationsReportSchedules(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	items, e := s.store.ListOperationsReportSchedules(r.Context(), ws.ID, u.ID)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"items": items, "limit": 50})
}
func (s *server) saveOperationsReportSchedule(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in store.OperationsReportScheduleInput
	if !readJSONLimit(w, r, &in, 8<<10) {
		return
	}
	var id int64
	if r.Method == http.MethodPut {
		var ok bool
		id, ok = operationsPathID(w, r, "schedule")
		if !ok {
			return
		}
	}
	item, e := s.store.SaveOperationsReportSchedule(r.Context(), ws.ID, u.ID, id, in)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	status := 200
	if id == 0 {
		status = 201
	}
	respond(w, status, item)
}
func (s *server) runOperationsReportSchedule(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := operationsPathID(w, r, "schedule")
	if !ok {
		return
	}
	item, e := s.store.RunOperationsReportSchedule(r.Context(), ws.ID, u.ID, id)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 201, item)
}
func (s *server) exportOperationsReport(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := operationsPathID(w, r, "report")
	if !ok {
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "html"
	}
	if format != "html" && format != "json" && format != "csv" {
		fail(w, 400, "请选择HTML、JSON或CSV格式。")
		return
	}
	item, e := s.store.GetOperationsReport(r.Context(), ws.ID, u.ID, id)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	var out bytes.Buffer
	contentType := "application/json; charset=utf-8"
	switch format {
	case "json":
		e = json.NewEncoder(&out).Encode(item)
	case "csv":
		contentType = "text/csv; charset=utf-8"
		out.WriteString("\ufeff")
		writer := csv.NewWriter(&out)
		e = writer.Write([]string{"记录类型", "记录ID", "来源/规则", "时间/结论", "范围/证据", "快照摘要"})
		write := func(row []string) {
			for i := range row {
				row[i] = classificationCSVCell(row[i])
			}
			if e == nil {
				e = writer.Write(row)
			}
		}
		scope, _ := json.Marshal(item.Scope)
		write([]string{"报告", fmt.Sprint(item.ID), item.Title, item.CreatedAt.Format(time.RFC3339), string(scope), item.SnapshotDigest})
		metadata, _ := json.Marshal(map[string]any{"scope": item.Scope, "windowTotal": item.Window.Total, "windowLimit": item.Window.Limit, "truncated": item.Window.Truncated, "retentionDays": item.Window.RetentionDays, "retentionLimitPerSource": item.Window.RetentionLimitPerSource, "warnings": item.Window.Warnings, "coverage": item.Coverage, "missingConditions": item.MissingConditions, "period": item.Period, "timezone": item.Timezone, "scheduleId": item.ScheduleID})
		write([]string{"采用范围与缺口", fmt.Sprint(item.ID), "观察分母/保留边界", item.CreatedAt.Format(time.RFC3339), string(metadata), item.SnapshotDigest})
		for _, risk := range item.Findings {
			raw, _ := json.Marshal(risk)
			write([]string{"风险周期", fmt.Sprint(risk.ID), risk.Rule, risk.CheckStatus, string(raw), item.SnapshotDigest})
		}
		for _, entry := range item.Window.Items {
			raw, _ := json.Marshal(entry.Observation)
			write([]string{"访问观察", fmt.Sprint(entry.ID), entry.Source, entry.Observation.ObservedAt.Format(time.RFC3339), string(raw), item.SnapshotDigest})
		}
		for _, f := range item.Analysis {
			raw, _ := json.Marshal(f)
			write([]string{"技术检查", f.ID, f.RuleVersion, scenarioCheckLabel(f.Outcome), string(raw), item.SnapshotDigest})
		}
		for _, evidence := range item.Evidence {
			write([]string{"证据", evidence.ID, evidence.Source, evidence.Record, string(evidence.Snapshot), item.SnapshotDigest})
		}
		for _, action := range item.Actions {
			raw, _ := json.Marshal(action)
			write([]string{"处置引用", fmt.Sprint(action.ID), action.Kind, action.Status, string(raw), item.SnapshotDigest})
		}
		writer.Flush()
		if e == nil {
			e = writer.Error()
		}
	case "html":
		contentType = "text/html; charset=utf-8"
		raw, _ := json.MarshalIndent(item, "", "  ")
		t := template.Must(template.New("report").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>{{.Title}}</title><style>body{font:15px system-ui;max-width:1100px;margin:40px auto;padding:0 24px;color:#172b4d}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#f3f6fa;padding:20px}h1{color:#175bc1}</style><h1>{{.Title}}</h1><p>本报告仅分析已观察范围，缺失记录不补零；去标识主体、来源报告IP与客户地域标签不自动证明自然人或法律结论。执行回执与新来源复查分别留痕。</p><p>快照摘要：{{.Digest}}</p><pre>{{.JSON}}</pre></html>`))
		e = t.Execute(&out, map[string]string{"Title": item.Title, "Digest": item.SnapshotDigest, "JSON": string(raw)})
	}
	if e != nil {
		fail(w, 500, "报告导出失败。")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="operations-report-%d.%s"`, id, format))
	w.WriteHeader(200)
	_, _ = w.Write(out.Bytes())
}
