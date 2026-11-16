package httpapi

import (
	"net/http"
	"strconv"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) securityPosturePage(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	q := r.URL.Query()
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			securityPlatformFailure(w, store.ErrInvalid)
			return
		}
	}
	out, err := s.store.SecurityPosturePage(r.Context(), ws.ID, u.ID, store.SecurityPostureFilter{Query: q.Get("q"), Severity: q.Get("severity"), Code: q.Get("code")}, q.Get("cursor"), limit)
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	respond(w, http.StatusOK, out)
}
