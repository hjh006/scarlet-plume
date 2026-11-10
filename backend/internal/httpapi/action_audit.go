package httpapi

import (
	"net/http"
	"strconv"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) actionAuditRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/audit/actions"
	mux.HandleFunc("GET "+base, s.workspace(false, s.actionAudit(false)))
	mux.HandleFunc("GET "+base+"/export", s.workspace(false, s.actionAudit(true)))
}

func (s *server) actionAudit(export bool) workspaceAction {
	return func(w http.ResponseWriter, r *http.Request, _ store.User, workspace store.Workspace) {
		hours := 24
		if raw := r.URL.Query().Get("hours"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || (value != 24 && value != 168 && value != 720) {
				fail(w, http.StatusBadRequest, "请选择 24 小时、7 天或 30 天的审计范围。")
				return
			}
			hours = value
		}
		page, err := s.store.ActionAudit(r.Context(), workspace.ID, hours)
		if err != nil {
			storeFailure(w, err)
			return
		}
		if export {
			w.Header().Set("Content-Disposition", `attachment; filename="scarlet-platform-audit.json"`)
		}
		respond(w, http.StatusOK, page)
	}
}
