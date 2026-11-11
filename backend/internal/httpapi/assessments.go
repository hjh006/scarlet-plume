package httpapi

import (
	"errors"
	"html/template"
	"net/http"
	"strconv"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) assessmentRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/assessments"
	mux.HandleFunc("GET "+base, s.workspace(false, s.listAssessments))
	mux.HandleFunc("POST "+base, s.workspace(false, s.createAssessment))
	mux.HandleFunc("GET "+base+"/templates", s.workspace(false, s.assessmentTemplates))
	mux.HandleFunc("POST "+base+"/templates", s.workspace(false, s.saveAssessmentTemplate(false)))
	mux.HandleFunc("PUT "+base+"/templates/{template}/draft", s.workspace(false, s.saveAssessmentTemplate(true)))
	mux.HandleFunc("POST "+base+"/templates/{template}/publish", s.workspace(false, s.publishAssessmentTemplate))
	mux.HandleFunc("POST "+base+"/templates/{template}/disable", s.workspace(false, s.disableAssessmentTemplate))
	mux.HandleFunc("GET "+base+"/{assessment}", s.workspace(false, s.getAssessment))
	mux.HandleFunc("PUT "+base+"/{assessment}/answers", s.workspace(false, s.answerAssessment))
	mux.HandleFunc("POST "+base+"/{assessment}/review", s.workspace(false, s.reviewAssessment))
	mux.HandleFunc("POST "+base+"/{assessment}/refresh", s.workspace(false, s.refreshAssessment))
	mux.HandleFunc("GET "+base+"/{assessment}/export", s.workspace(false, s.exportAssessment))
}
func assessmentID(w http.ResponseWriter, r *http.Request, key string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(key), 10, 64)
	if err != nil || id < 1 {
		fail(w, http.StatusNotFound, "找不到此评估或模板。")
		return 0, false
	}
	return id, true
}
func assessmentFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, "请核对评估范围、题型、答复和证据引用。")
	case errors.Is(err, store.ErrForbidden):
		fail(w, http.StatusForbidden, "当前角色无权操作，创建人和答题人需要由独立人员复核。")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, "版本、活动或事实已变化，或仍有取证与整改缺口。请刷新后核对。")
	default:
		storeFailure(w, err)
	}
}
func (s *server) assessmentTemplates(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	items, err := s.store.ListAssessmentTemplates(r.Context(), ws.ID, u.ID)
	if err != nil {
		assessmentFailure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"items": items})
}
func (s *server) saveAssessmentTemplate(update bool) func(http.ResponseWriter, *http.Request, store.User, store.Workspace) {
	return func(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
		var id int64
		if update {
			var ok bool
			id, ok = assessmentID(w, r, "template")
			if !ok {
				return
			}
		}
		var in store.AssessmentTemplateInput
		if !readJSON(w, r, &in) {
			return
		}
		item, err := s.store.SaveAssessmentTemplate(r.Context(), ws.ID, u.ID, id, in)
		if err != nil {
			assessmentFailure(w, err)
			return
		}
		status := http.StatusCreated
		if update {
			status = http.StatusOK
		}
		respond(w, status, item)
	}
}
func (s *server) publishAssessmentTemplate(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := assessmentID(w, r, "template")
	if !ok {
		return
	}
	var input struct {
		Revision int64 `json:"revision"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	item, err := s.store.PublishAssessmentTemplate(r.Context(), ws.ID, u.ID, id, input.Revision)
	if err != nil {
		assessmentFailure(w, err)
		return
	}
	respond(w, http.StatusCreated, item)
}
func (s *server) disableAssessmentTemplate(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := assessmentID(w, r, "template")
	if !ok {
		return
	}
	if err := s.store.DisableAssessmentTemplate(r.Context(), ws.ID, u.ID, id); err != nil {
		assessmentFailure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]string{"status": "disabled"})
}
func (s *server) listAssessments(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	items, err := s.store.ListAssessments(r.Context(), ws.ID, u.ID, r.URL.Query().Get("kind"), r.URL.Query().Get("status"))
	if err != nil {
		assessmentFailure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"items": items, "limit": 100})
}
func (s *server) createAssessment(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in store.AssessmentInput
	if !readJSON(w, r, &in) {
		return
	}
	item, err := s.store.CreateAssessment(r.Context(), ws.ID, u.ID, in)
	if err != nil {
		assessmentFailure(w, err)
		return
	}
	respond(w, http.StatusCreated, item)
}
func (s *server) getAssessment(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := assessmentID(w, r, "assessment")
	if !ok {
		return
	}
	item, err := s.store.Assessment(r.Context(), ws.ID, u.ID, id)
	if err != nil {
		assessmentFailure(w, err)
		return
	}
	respond(w, http.StatusOK, item)
}
func (s *server) answerAssessment(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := assessmentID(w, r, "assessment")
	if !ok {
		return
	}
	var in store.AssessmentAnswersInput
	if !readJSON(w, r, &in) {
		return
	}
	item, err := s.store.SaveAssessmentAnswers(r.Context(), ws.ID, u.ID, id, in)
	if err != nil {
		assessmentFailure(w, err)
		return
	}
	respond(w, http.StatusOK, item)
}
func (s *server) reviewAssessment(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := assessmentID(w, r, "assessment")
	if !ok {
		return
	}
	var in store.AssessmentReviewInput
	if !readJSON(w, r, &in) {
		return
	}
	item, err := s.store.ReviewAssessment(r.Context(), ws.ID, u.ID, id, in)
	if err != nil {
		assessmentFailure(w, err)
		return
	}
	respond(w, http.StatusOK, item)
}
func (s *server) refreshAssessment(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := assessmentID(w, r, "assessment")
	if !ok {
		return
	}
	item, err := s.store.RefreshAssessment(r.Context(), ws.ID, u.ID, id)
	if err != nil {
		assessmentFailure(w, err)
		return
	}
	respond(w, http.StatusOK, item)
}

var assessmentHTML = template.Must(template.New("assessment").Parse(`<!doctype html>
<html lang="zh-CN">
<meta charset="utf-8">
<title>{{.Title}}</title>
<style>body{font:16px system-ui;max-width:1100px;margin:40px auto;color:#16334b}table{border-collapse:collapse;width:100%}td,th{border:1px solid #ccd8df;padding:10px;text-align:left;vertical-align:top}small{color:#526675}li{margin:8px 0}a{color:#006a96}</style>
<h1>{{.Title}}</h1>
<p>评估 {{.ID}} · {{.Kind}} · {{.Status}} · 版本 {{.Revision}}</p>{{if .Stale}}<p>
<strong>采用的活动或来源事实已变化，需要重新评估。</strong>
</p>{{end}}<p>{{.Basis}}</p>
<h2>采用范围与模板</h2>
<p>{{.Activity.Name}} / {{.Activity.Purpose}}</p>
<p>{{.Template.Title}} · {{.Template.Key}} · v{{.Template.Revision}} · 发布版本 {{.TemplateRevisionID}}</p>
<p>创建人 {{.Creator}}，复核人 {{.Reviewer}}。{{.ReviewNote}}</p>
<h2>条款与验收</h2>
<table>
<tr>
<th>题目及适用条件</th>
<th>依据</th>
<th>取证与验收</th>
</tr>{{range .Template.Questions}}<tr>
<td>{{.Title}}<p>{{.Applicability}}</p>
<small>{{.Obligation}}</small>
</td>
<td>{{.Basis}}<p>
<a href="{{.BasisURL}}">官方依据</a>
</p>
</td>
<td>{{.Evidence}}<p>{{.Acceptance}}</p>
</td>
</tr>{{end}}</table>
<h2>来源事实</h2>
<table>{{range .Facts}}<tr>
<td>{{.Key}}</td>
<td>{{.State}} {{.Value}}</td>
<td>{{.Source}} {{.Record}}</td>
</tr>{{end}}</table>
<h2>答复</h2>
<table>{{range .Answers}}<tr>
<td>{{.Key}}</td>
<td>{{.Value}} {{.Values}} {{if .Number}}{{.Number}}{{end}}</td>
<td>证据 {{.EvidenceIDs}}<p>{{.Note}}</p>
</td>
</tr>{{end}}</table>
<h2>缺口和风险</h2>
<ul>{{range .Gaps}}<li>{{.Title}} {{.Reason}}</li>{{end}}{{range .Risks}}<li>{{.Title}} {{.Reason}}</li>{{end}}</ul>
<h2>采用证据</h2>
<table>{{range .Evidence}}<tr>
<td>{{.ID}}</td>
<td>{{.Source}} {{.Record}}</td>
<td>{{.Summary}}</td>
</tr>{{end}}</table>
<h2>整改与独立复核</h2>
<ul>{{range .Checks}}<li>检查 {{.ID}} · {{.RequirementCode}} · {{.Status}} · {{.OwnerTeam}}</li>{{end}}</ul>
</html>`))

func (s *server) exportAssessment(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := assessmentID(w, r, "assessment")
	if !ok {
		return
	}
	format := r.URL.Query().Get("format")
	if format != "" && format != "json" && format != "html" {
		assessmentFailure(w, store.ErrInvalid)
		return
	}
	item, err := s.store.AssessmentReport(r.Context(), ws.ID, u.ID, id)
	if err != nil {
		assessmentFailure(w, err)
		return
	}
	if format == "html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="scarlet-assessment.html"`)
		if err := assessmentHTML.Execute(w, item); err != nil {
			return
		}
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="scarlet-assessment.json"`)
	respond(w, http.StatusOK, item)
}
