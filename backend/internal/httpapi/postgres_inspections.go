package httpapi

import (
	"net/http"
	"strconv"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) postgresInspectionRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}"
	mux.HandleFunc("GET "+base+"/assets/{asset}/postgresql", s.workspace(false, s.getPostgresInspection))
	mux.HandleFunc("POST "+base+"/checks/{check}/postgresql", s.workspace(true, s.bindCheckPostgres))
}

func (s *server) getPostgresInspection(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	id, err := strconv.ParseInt(r.PathValue("asset"), 10, 64)
	if err != nil || id < 1 {
		fail(w, http.StatusNotFound, "找不到此资产。")
		return
	}
	inspection, err := s.store.GetAssetPostgresInspection(r.Context(), workspace.ID, user.ID, id)
	if err != nil {
		checkFailure(w, err)
		return
	}
	respond(w, http.StatusOK, inspection)
}

func (s *server) bindCheckPostgres(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	id, ok := checkID(w, r)
	if !ok {
		return
	}
	var input struct {
		JobID int64 `json:"jobId"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if input.JobID < 1 {
		fail(w, http.StatusBadRequest, "请选择此资产对应的 PostgreSQL 采集任务。")
		return
	}
	checkMutationResult(w, s.store.BindCheckPostgres(r.Context(), workspace.ID, user.ID, id, input.JobID))
}
