package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) apiLifecycleRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/api-security/lifecycle"
	mux.HandleFunc("GET "+base, s.workspace(false, s.apiLifecycle(false)))
	mux.HandleFunc("GET "+base+"/export", s.workspace(false, s.apiLifecycle(true)))
}

func (s *server) apiLifecycle(export bool) workspaceAction {
	return func(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
		filter := store.APILifecycleFilter{Service: strings.TrimSpace(r.URL.Query().Get("service"))}
		if raw := r.URL.Query().Get("sourceId"); raw != "" {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 {
				fail(w, http.StatusBadRequest, "请选择有效的运行时来源。")
				return
			}
			filter.SourceID = id
		}
		result, err := s.store.APILifecycle(r.Context(), workspace.ID, user.ID, filter)
		switch {
		case errors.Is(err, store.ErrInvalid):
			fail(w, http.StatusBadRequest, "请核对服务别名与运行时来源，二者应属于同一服务。")
		case errors.Is(err, store.ErrAPILifecycleScope):
			fail(w, http.StatusUnprocessableEntity, "生命周期目录超过单次支持的50个服务或2000项接口与来源记录，请按服务或来源筛选。")
		case err != nil:
			storeFailure(w, err)
		default:
			if export {
				w.Header().Set("Content-Disposition", `attachment; filename="scarlet-api-lifecycle.json"`)
			}
			respond(w, http.StatusOK, result)
		}
	}
}
