package httpapi

import (
	"net/http"
	"strconv"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

func (s *server) gatewayTaskRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/ai-gateway/tasks"
	mux.HandleFunc("GET "+base, s.workspace(false, s.getGatewayTasks))
	mux.HandleFunc("POST "+base, s.workspace(false, s.createGatewayTask))
	mux.HandleFunc("GET "+base+"/{task}", s.workspace(false, s.getGatewayTask))
	mux.HandleFunc("POST "+base+"/{task}/status", s.workspace(false, s.setGatewayTaskStatus))
	mux.HandleFunc("POST /api/v1/internal/ai-gateway/task/check", s.checkGatewayTask)
	mux.HandleFunc("POST /api/v1/internal/ai-gateway/task/steps/admit", s.admitGatewayTask)
	mux.HandleFunc("POST /api/v1/internal/ai-gateway/task/steps/complete", s.completeGatewayTask)
}

func gatewayTaskLimit(r *http.Request) (int, error) {
	if r.URL.Query().Get("limit") == "" {
		return 100, nil
	}
	n, e := strconv.Atoi(r.URL.Query().Get("limit"))
	if e != nil || n < 1 || n > 200 {
		return 0, store.ErrInvalid
	}
	return n, nil
}
func (s *server) getGatewayTasks(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	limit, e := gatewayTaskLimit(r)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	out, e := s.store.GatewayTasks(r.Context(), ws.ID, u.ID, r.URL.Query().Get("after"), limit)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) createGatewayTask(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in store.GatewayTaskInput
	if !readJSONLimit(w, r, &in, 65536) {
		return
	}
	out, e := s.store.CreateGatewayTask(r.Context(), ws.ID, u.ID, in)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	respond(w, 201, out)
}
func (s *server) getGatewayTask(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	limit, e := gatewayTaskLimit(r)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	out, e := s.store.GatewayTaskDetail(r.Context(), ws.ID, u.ID, r.PathValue("task"), limit)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) setGatewayTaskStatus(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in store.GatewayTaskStatusInput
	if !readJSONLimit(w, r, &in, 4096) {
		return
	}
	out, e := s.store.SetGatewayTaskStatus(r.Context(), ws.ID, u.ID, r.PathValue("task"), in)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) checkGatewayTask(w http.ResponseWriter, r *http.Request) {
	var in gatewaygovernance.TaskCheckInput
	if !readJSONLimit(w, r, &in, 32768) || !gatewayMachine(w, r, in.WorkspaceSlug) {
		return
	}
	out, e := s.store.CheckGatewayTask(r.Context(), in)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) admitGatewayTask(w http.ResponseWriter, r *http.Request) {
	var in gatewaygovernance.TaskCheckInput
	if !readJSONLimit(w, r, &in, 32768) || !gatewayMachine(w, r, in.WorkspaceSlug) {
		return
	}
	out, e := s.store.AdmitGatewayTask(r.Context(), in)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) completeGatewayTask(w http.ResponseWriter, r *http.Request) {
	var in gatewaygovernance.TaskCompleteInput
	if !readJSONLimit(w, r, &in, 131072) || !gatewayMachine(w, r, in.WorkspaceSlug) {
		return
	}
	out, e := s.store.CompleteGatewayTask(r.Context(), in)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	respond(w, 200, out)
}
