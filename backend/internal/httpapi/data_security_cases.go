package httpapi

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) dataSecurityRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/data-security"
	mux.HandleFunc("GET "+base+"/events", s.workspace(false, s.dataSecurityEvents))
	mux.HandleFunc("GET "+base+"/cases", s.workspace(false, s.dataSecurityCases))
	mux.HandleFunc("POST "+base+"/cases", s.workspace(true, s.createDataSecurityCase))
	mux.HandleFunc("GET "+base+"/cases/{case}", s.workspace(false, s.dataSecurityCase))
	mux.HandleFunc("GET "+base+"/cases/{case}/export", s.workspace(false, s.exportDataSecurityCase))
	mux.HandleFunc("POST "+base+"/cases/{case}/feedback", s.workspace(true, s.dataSecurityFeedback))
	mux.HandleFunc("POST "+base+"/cases/{case}/ai-reference", s.workspace(true, s.dataSecurityAIReference))
}
func (s *server) dataSecurityEvents(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	q := r.URL.Query()
	scope := store.DataSecurityScope{SubjectRef: q.Get("subjectRef"), DeviceRef: q.Get("deviceRef"), Channel: q.Get("channel")}
	var err error
	if q.Get("sourceId") != "" {
		scope.SourceID, err = strconv.ParseInt(q.Get("sourceId"), 10, 64)
		if err != nil {
			operationsFailure(w, store.ErrInvalid)
			return
		}
	}
	scope.From, err = time.Parse(time.RFC3339Nano, q.Get("from"))
	if err != nil {
		operationsFailure(w, store.ErrInvalid)
		return
	}
	scope.To, err = time.Parse(time.RFC3339Nano, q.Get("to"))
	if err != nil {
		operationsFailure(w, store.ErrInvalid)
		return
	}
	out, err := s.store.DataSecurityEvents(r.Context(), ws.ID, u.ID, scope)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, out)
}
func (s *server) dataSecurityCases(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	out, err := s.store.DataSecurityCases(r.Context(), ws.ID, u.ID)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": out})
}
func (s *server) createDataSecurityCase(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in struct {
		Title string                  `json:"title"`
		Scope store.DataSecurityScope `json:"scope"`
	}
	if !readJSONLimit(w, r, &in, 8<<10) {
		return
	}
	out, err := s.store.CreateDataSecurityCase(r.Context(), ws.ID, u.ID, in.Title, in.Scope)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 201, out)
}
func (s *server) dataSecurityCase(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "case")
	if !ok {
		return
	}
	out, err := s.store.DataSecurityCase(r.Context(), ws.ID, u.ID, id)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, out)
}
func (s *server) dataSecurityFeedback(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "case")
	if !ok {
		return
	}
	var in struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if !readJSONLimit(w, r, &in, 8<<10) {
		return
	}
	if err := s.store.AddDataSecurityCaseFeedback(r.Context(), ws.ID, u.ID, id, in.Decision, in.Note); err != nil {
		operationsFailure(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *server) dataSecurityAIReference(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "case")
	if !ok {
		return
	}
	var in struct {
		ProfileID     int64  `json:"profileId"`
		AllowMetadata bool   `json:"allowMetadata"`
		CaseDigest    string `json:"caseDigest"`
	}
	if !readJSONLimit(w, r, &in, 4<<10) {
		return
	}
	if !in.AllowMetadata || in.ProfileID < 1 {
		fail(w, 400, "请逐次确认发送预览中的脱敏事件投影，并选择模型。不会发送文件、SQL、提示词、路径或身份引用。")
		return
	}
	c, err := s.store.DataSecurityCase(r.Context(), ws.ID, u.ID, id)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	if c.SnapshotDigest != in.CaseDigest {
		operationsFailure(w, store.ErrConflict)
		return
	}
	if len(c.Notes) >= 200 {
		fail(w, 422, "这份调查的参考与反馈记录已满，请保留导出并建立后续调查后再调用模型。")
		return
	}
	facts := store.DataSecurityCaseFacts(c)
	if len(facts) > 100 {
		fail(w, 422, "模型参考最多采用100条事件，请缩小范围后生成新的调查快照。")
		return
	}
	settings, err := s.store.GetClassificationEngineSettings(r.Context(), ws.ID, u.ID)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	if settings.Mode != "hybrid" {
		fail(w, 403, "当前引擎仅使用本地能力。请由负责人在模型配置中明确启用混合模式后再调用。")
		return
	}
	profile, runtime, ok := s.classificationRuntimeProfile(w, r, u, ws, in.ProfileID)
	if !ok {
		return
	}
	if !profile.Enabled || !profile.AllowMetadata {
		operationsFailure(w, store.ErrForbidden)
		return
	}
	release, ok := s.classificationAISlot(w)
	if !ok {
		return
	}
	defer release()
	result, err := s.classificationAI.AssessEvents(r.Context(), runtime, facts)
	if err != nil {
		classificationAIFailure(w, err)
		return
	}
	if err = s.store.SaveDataSecurityAIReference(r.Context(), ws.ID, u.ID, id, c.SnapshotDigest, profile, result); err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 201, result)
}
func (s *server) exportDataSecurityCase(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "case")
	if !ok {
		return
	}
	c, err := s.store.DataSecurityCase(r.Context(), ws.ID, u.ID, id)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	switch r.URL.Query().Get("format") {
	case "", "json":
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="data-security-case-%d.json"`, id))
		respond(w, 200, c)
	case "html":
		raw, _ := json.MarshalIndent(c, "", "  ")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="data-security-case-%d.html"`, id))
		t := template.Must(template.New("case").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>{{.Title}}</title><style>body{font:15px/1.7 system-ui;max-width:1100px;margin:40px auto;padding:0 24px;color:#17324a}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#f3f7fb;padding:24px}h1{color:#205a86}</style><h1>{{.Title}}</h1><p>冻结证据摘要：{{.Digest}}</p><p>来源投影、规则关联与人工反馈分别保留；AI仅作参考，不自动认定人员、泄漏或监管符合，也不代替独立复核。</p><pre>{{.JSON}}</pre></html>`))
		_ = t.Execute(w, map[string]string{"Title": c.Title, "Digest": c.SnapshotDigest, "JSON": string(raw)})
	default:
		operationsFailure(w, store.ErrInvalid)
	}
}
