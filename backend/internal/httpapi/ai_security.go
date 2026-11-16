package httpapi

import (
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"net/http"
	"strconv"
)

func (s *server) aiSecurityRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/ai-security"
	mux.HandleFunc("GET "+base, s.workspace(false, s.getAISecurity))
	mux.HandleFunc("PUT "+base+"/configuration", s.workspace(true, s.putAISecurity))
	mux.HandleFunc("POST "+base+"/evaluate", s.workspace(true, s.evaluateAISecurity))
	mux.HandleFunc("POST "+base+"/evaluations", s.workspace(true, s.runAISecurityEvaluations))
	mux.HandleFunc("POST "+base+"/publish", s.workspace(false, s.publishAISecurity))
	mux.HandleFunc("POST "+base+"/approvals", s.workspace(true, s.requestAISecurityApproval))
	mux.HandleFunc("POST "+base+"/approvals/{approval}/review", s.workspace(false, s.reviewAISecurityApproval))
	mux.HandleFunc("GET "+base+"/export", s.workspace(false, s.exportAISecurity))
	mux.HandleFunc("GET "+base+"/runtime-config", s.workspace(false, s.runtimeAISecurityConfig))
	s.aiSecurityRegressionRoutes(mux)
}
func (s *server) getAISecurity(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	v, e := s.store.AISecurityBundle(r.Context(), ws.ID, u.ID)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 200, v)
}
func (s *server) putAISecurity(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in struct {
		ExpectedRevision int64             `json:"expectedRevision"`
		Configuration    aisecurity.Config `json:"configuration"`
	}
	if !readJSONLimit(w, r, &in, 1<<20) {
		return
	}
	v, e := s.store.SaveAISecurityConfiguration(r.Context(), ws.ID, u.ID, in.ExpectedRevision, in.Configuration)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 200, v)
}
func (s *server) evaluateAISecurity(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in aisecurity.Request
	if !readJSONLimit(w, r, &in, 2<<20) {
		return
	}
	v, e := s.store.EvaluateAISecurity(r.Context(), ws.ID, u.ID, in)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 201, v)
}
func (s *server) runAISecurityEvaluations(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in struct {
		PackID int64 `json:"packId"`
	}
	if !readJSONLimit(w, r, &in, 1024) {
		return
	}
	v, e := s.store.RunAISecurityRegression(r.Context(), ws.ID, u.ID, in.PackID)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 201, v)
}
func (s *server) publishAISecurity(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in struct {
		ExpectedRevision int64  `json:"expectedRevision"`
		EvaluationID     int64  `json:"evaluationId"`
		Note             string `json:"note"`
	}
	if !readJSONLimit(w, r, &in, 4096) {
		return
	}
	v, e := s.store.PublishAISecurity(r.Context(), ws.ID, u.ID, in.ExpectedRevision, in.EvaluationID, in.Note)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 200, v)
}
func (s *server) requestAISecurityApproval(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in struct {
		ExpectedRevision int64              `json:"expectedRevision"`
		Request          aisecurity.Request `json:"request"`
	}
	if !readJSONLimit(w, r, &in, 64<<10) {
		return
	}
	v, e := s.store.RequestAISecurityApproval(r.Context(), ws.ID, u.ID, in.ExpectedRevision, in.Request)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 201, v)
}
func (s *server) reviewAISecurityApproval(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, e := strconv.ParseInt(r.PathValue("approval"), 10, 64)
	if e != nil || id < 1 {
		fail(w, 404, "找不到审批记录。")
		return
	}
	var in struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if !readJSONLimit(w, r, &in, 4096) {
		return
	}
	v, e := s.store.ReviewAISecurityApproval(r.Context(), ws.ID, u.ID, id, in.Decision, in.Note)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 200, v)
}
func (s *server) exportAISecurity(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	v, e := s.store.AISecurityBundle(r.Context(), ws.ID, u.ID)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="ai-security-evaluation-evidence.json"`)
	respond(w, 200, map[string]any{"kind": "ai-security-control-plane-evidence", "mode": "evaluation", "workspace": ws.Slug, "bundle": v})
}
func (s *server) runtimeAISecurityConfig(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	v, revision, e := s.store.AISecurityRuntimeConfig(r.Context(), ws.ID, u.ID)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="ai-security-published-config.json"`)
	w.Header().Set("X-Configuration-Revision", strconv.FormatInt(revision, 10))
	respond(w, 200, v)
}
