package httpapi

import (
	"errors"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"net/http"
)

func (s *server) runtimeDetectionRoutes(mux *http.ServeMux) {
	const path = "/api/v1/workspaces/{slug}/api-security/runtime/policies"
	mux.HandleFunc("GET "+path, s.workspace(false, s.runtimeDetectionPolicies))
	mux.HandleFunc("GET "+path+"/history", s.workspace(false, s.runtimeDetectionPolicyHistory))
	mux.HandleFunc("PUT "+path, s.workspace(true, s.saveRuntimeDetectionPolicy))
}

func (s *server) runtimeDetectionPolicyHistory(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	items, err := s.store.RuntimeDetectionPolicyHistory(r.Context(), ws.ID, user.ID, r.URL.Query().Get("rule"))
	if err != nil {
		runtimeDetectionFailure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"items": items, "limit": 30})
}

func (s *server) runtimeDetectionPolicies(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	items, err := s.store.RuntimeDetectionPolicies(r.Context(), ws.ID, user.ID)
	if err != nil {
		storeFailure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"items": items, "retentionHours": 24, "sampleLimitPerSource": 20000, "windowLimitMinutes": 60})
}

func (s *server) saveRuntimeDetectionPolicy(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	var input store.RuntimeDetectionPolicy
	if !readJSONLimit(w, r, &input, 8192) {
		return
	}
	item, err := s.store.SaveRuntimeDetectionPolicy(r.Context(), ws.ID, user.ID, input)
	if err != nil {
		runtimeDetectionFailure(w, err)
		return
	}
	respond(w, http.StatusOK, item)
}

func runtimeDetectionFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInvalid):
		fail(w, 400, "请核对检测规则、统计窗口、类别和阈值。")
	case errors.Is(err, store.ErrForbidden):
		fail(w, 403, "只有工作区负责人可以修改检测策略。")
	case errors.Is(err, store.ErrConflict):
		fail(w, 409, "策略版本已更新，请刷新后重新修改。")
	default:
		storeFailure(w, err)
	}
}
