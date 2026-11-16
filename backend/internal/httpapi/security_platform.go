package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) securityPlatformRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/security-platform"
	mux.HandleFunc("GET "+base, s.workspace(false, s.securityPlatformOverview))
	mux.HandleFunc("GET "+base+"/posture", s.workspace(false, s.securityPosturePage))
	mux.HandleFunc("GET "+base+"/export", s.workspace(false, s.securityPlatformOverview))
	mux.HandleFunc("POST "+base+"/snapshots", s.workspace(true, s.captureSecurityPlatform))
	mux.HandleFunc("GET "+base+"/snapshots/{snapshot}", s.workspace(false, s.securityPlatformSnapshot))
	mux.HandleFunc("POST "+base+"/network-bindings", s.workspace(true, s.saveSecurityNetworkBinding))
	mux.HandleFunc("POST "+base+"/network-changes", s.workspace(true, s.createSecurityNetworkChange))
	mux.HandleFunc("POST "+base+"/network-changes/{change}/review", s.workspace(false, s.reviewSecurityNetworkChange))
}

func securityPlatformFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrAIRuntimeSourceLimit):
		fail(w, http.StatusUnprocessableEntity, "活跃AI运行时来源已达100个。请先撤销停用来源，再创建新的独立来源。")
	case errors.Is(err, store.ErrAIRuntimeReceiptLimit):
		fail(w, http.StatusUnprocessableEntity, "此AI运行时来源已达20000条回执。请保留本地审计，建立新的来源并从未确认记录继续投递；刷新配置不能解除此容量限制。")
	case errors.Is(err, store.ErrInvalid):
		fail(w, 400, "请核对关联对象、版本及必填字段。")
	case errors.Is(err, store.ErrForbidden):
		fail(w, 403, "当前角色无权操作，复核须由独立Owner或Reviewer完成。")
	case errors.Is(err, store.ErrConflict):
		fail(w, 409, "采用版本已变化或记录已复核，请刷新后重新核对。")
	default:
		storeFailure(w, err)
	}
}
func (s *server) securityPlatformOverview(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	out, err := s.store.SecurityPlatform(r.Context(), ws.ID, u.ID)
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/export") {
		w.Header().Set("Content-Disposition", `attachment; filename="scarlet-security-platform.json"`)
	}
	respond(w, 200, out)
}
func (s *server) captureSecurityPlatform(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var input struct{}
	if !readJSONLimit(w, r, &input, 1024) {
		return
	}
	out, err := s.store.CaptureSecurityPlatform(r.Context(), ws.ID, u.ID)
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	respond(w, 201, out)
}
func (s *server) securityPlatformSnapshot(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, err := strconv.ParseInt(r.PathValue("snapshot"), 10, 64)
	if err != nil || id < 1 {
		fail(w, 400, "请选择有效快照。")
		return
	}
	out, err := s.store.SecurityPlatformSnapshot(r.Context(), ws.ID, u.ID, id)
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="scarlet-security-platform-snapshot.json"`)
	respond(w, 200, out)
}
func (s *server) saveSecurityNetworkBinding(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var input store.SecurityNetworkBindingInput
	if !readJSONLimit(w, r, &input, 32*1024) {
		return
	}
	out, err := s.store.SaveSecurityNetworkBinding(r.Context(), ws.ID, u.ID, input)
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	respond(w, 200, out)
}
func (s *server) createSecurityNetworkChange(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var input store.SecurityNetworkChangeInput
	if !readJSONLimit(w, r, &input, 8192) {
		return
	}
	out, err := s.store.CreateSecurityNetworkChange(r.Context(), ws.ID, u.ID, input)
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	respond(w, 201, out)
}
func (s *server) reviewSecurityNetworkChange(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var input struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if !readJSONLimit(w, r, &input, 8192) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("change"), 10, 64)
	if err != nil || id < 1 {
		fail(w, 400, "请选择有效变更。")
		return
	}
	out, err := s.store.ReviewSecurityNetworkChange(r.Context(), ws.ID, u.ID, id, input.Decision, input.Note)
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	respond(w, 200, out)
}
