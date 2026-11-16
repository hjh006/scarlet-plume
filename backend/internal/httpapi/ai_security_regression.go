package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"strconv"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
)

func (s *server) aiSecurityRegressionRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/ai-security"
	mux.HandleFunc("GET "+base+"/regression-packs", s.workspace(false, s.listAIRegressionPacks))
	mux.HandleFunc("GET "+base+"/regression-packs/baseline", s.workspace(false, func(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
		respond(w, 200, store.BaselineAISecurityRegressionPack())
	}))
	mux.HandleFunc("GET "+base+"/regression-packs/{pack}", s.workspace(false, s.getAIRegressionPack))
	mux.HandleFunc("POST "+base+"/regression-packs/validate", s.workspace(true, s.validateAIRegressionPack))
	mux.HandleFunc("POST "+base+"/regression-packs", s.workspace(true, s.importAIRegressionPack))
	mux.HandleFunc("GET "+base+"/evaluations/{evaluation}/compare", s.workspace(false, s.compareAIRegression))
}
func (s *server) listAIRegressionPacks(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	items, e := s.store.AISecurityRegressionPacks(r.Context(), ws.ID, u.ID)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"items": items, "requiredDimensions": store.AISecurityRegressionDimensions(), "latestGate": store.AISecurityRegressionGate, "limit": 100, "engineVersion": aisecurity.RuntimeProtocolVersion, "inspectionVersion": aisecurity.InspectionVersion})
}
func (s *server) getAIRegressionPack(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, e := strconv.ParseInt(r.PathValue("pack"), 10, 64)
	if e != nil || id < 1 {
		operationsFailure(w, store.ErrInvalid)
		return
	}
	p, e := s.store.AISecurityRegressionPack(r.Context(), ws.ID, u.ID, id)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 200, p)
}
func (s *server) validateAIRegressionPack(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in struct {
		Pack store.AISecurityRegressionPack `json:"pack"`
	}
	if !readAIRegressionJSON(w, r, &in) {
		return
	}
	out, e := store.ValidateAISecurityRegressionPack(in.Pack, false)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) importAIRegressionPack(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in struct {
		ExpectedLatestVersion string                         `json:"expectedLatestVersion"`
		Pack                  store.AISecurityRegressionPack `json:"pack"`
	}
	if !readAIRegressionJSON(w, r, &in) {
		return
	}
	out, e := s.store.ImportAISecurityRegressionPack(r.Context(), ws.ID, u.ID, in.ExpectedLatestVersion, in.Pack)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 201, out)
}

func readAIRegressionJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 262144))
	if e != nil {
		fail(w, 413, "测试包超过256KiB或未完整读取。")
		return false
	}
	facts, _, e := datasafety.InspectJSON(bytes.NewReader(raw))
	if e != nil || facts.Capture != "complete" {
		fail(w, 400, "测试包JSON含重复字段、非法编码或超出解析预算。")
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return readJSONLimit(w, r, target, 262144)
}
func (s *server) compareAIRegression(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	to, e := strconv.ParseInt(r.PathValue("evaluation"), 10, 64)
	if e != nil || to < 1 {
		operationsFailure(w, store.ErrInvalid)
		return
	}
	from, e := strconv.ParseInt(r.URL.Query().Get("against"), 10, 64)
	if e != nil || from < 1 {
		operationsFailure(w, store.ErrInvalid)
		return
	}
	out, e := s.store.CompareAISecurityRegression(r.Context(), ws.ID, u.ID, from, to)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 200, out)
}
