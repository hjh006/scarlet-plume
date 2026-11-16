package httpapi

import (
	"net/http"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) gatewayCredentialPageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/workspaces/{slug}/ai-gateway/credentials", s.workspace(false, s.getGatewayCredentialPage))
}

func (s *server) getGatewayCredentialPage(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	q := r.URL.Query()
	limit, err := gatewayTaskLimit(r)
	if err != nil || len(q["before"]) > 1 || len(q["state"]) > 1 || len(q["applicationId"]) > 1 {
		gatewayFailure(w, store.ErrInvalid)
		return
	}
	out, err := s.store.GatewayCredentials(r.Context(), ws.ID, u.ID, q.Get("before"), q.Get("state"), q.Get("applicationId"), limit)
	if err != nil {
		gatewayFailure(w, err)
		return
	}
	respond(w, http.StatusOK, out)
}
