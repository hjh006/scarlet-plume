package httpapi

import (
	"net/http"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) gatewayTaskCredentialStatusRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/workspaces/{slug}/ai-gateway/tasks/{task}/credential-status", s.workspace(false, s.getGatewayTaskCredentialStatus))
}
func (s *server) getGatewayTaskCredentialStatus(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	out, err := s.store.GatewayTaskCredentialStatus(r.Context(), ws.ID, u.ID, r.PathValue("task"))
	if err != nil {
		gatewayFailure(w, err)
		return
	}
	respond(w, http.StatusOK, out)
}
