package httpapi

import (
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"net/http"
)

func (s *server) valueFingerprintRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/workspaces/{slug}/investigations/value-search", s.workspace(false, s.searchValueFingerprints))
	s.cryptoTransformRoutes(mux)
}
func (s *server) searchValueFingerprints(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	if r.Header.Get("Origin") != s.origin {
		fail(w, 403, "请求来源无效。")
		return
	}
	var in store.ValueSearchInput
	if !readJSONLimit(w, r, &in, 4096) {
		return
	}
	result, e := s.store.SearchValueFingerprints(r.Context(), ws.ID, user.ID, in)
	if e != nil {
		automationFailure(w, e)
		return
	}
	respond(w, 200, result)
}
