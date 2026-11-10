package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) runtimeRiskRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/v1/workspaces/{slug}/api-security/runtime/findings/{finding}/triage", s.workspace(true, s.updateRuntimeRisk))
}

func (s *server) updateRuntimeRisk(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	id, err := strconv.ParseInt(r.PathValue("finding"), 10, 64)
	if err != nil || id < 1 {
		fail(w, http.StatusNotFound, "找不到此运行时发现。")
		return
	}
	var input store.RuntimeRiskInput
	if !readJSON(w, r, &input) {
		return
	}
	input.OwnerTeam = strings.TrimSpace(input.OwnerTeam)
	input.Reason = strings.TrimSpace(input.Reason)
	result, err := s.store.UpdateRuntimeRisk(r.Context(), workspace.ID, user.ID, id, input)
	if err != nil {
		checkFailure(w, err)
		return
	}
	respond(w, http.StatusOK, result)
}
