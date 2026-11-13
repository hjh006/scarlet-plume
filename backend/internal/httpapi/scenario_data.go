package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

const applicationObservationsPath = "/api/v1/application-agent/observations"

func (s *server) scenarioDataRoutes(mux *http.ServeMux) {
	s.scenarioCheckRoutes(mux)
	const base = "/api/v1/workspaces/{slug}"
	mux.HandleFunc("GET "+base+"/scenario-data", s.workspace(false, s.scenarioData))
	mux.HandleFunc("GET "+base+"/application-sources", s.workspace(false, s.applicationSources))
	mux.HandleFunc("POST "+base+"/application-sources", s.workspace(false, s.createApplicationSource))
	mux.HandleFunc("DELETE "+base+"/application-sources/{id}", s.workspace(false, s.revokeApplicationSource))
	mux.HandleFunc("POST "+applicationObservationsPath, s.applicationObservations)
}
func (s *server) scenarioData(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	ids := map[string]int64{}
	for _, key := range []string{"assetId", "activityId"} {
		if len(r.URL.Query()[key]) > 1 {
			fail(w, 400, "请只选择一个资产或处理活动范围。")
			return
		}
		value := r.URL.Query().Get(key)
		if value == "" {
			continue
		}
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id < 1 {
			fail(w, 400, "请选择有效的资产或处理活动。")
			return
		}
		ids[key] = id
	}
	for _, key := range []string{"scenario", "task"} {
		if len(r.URL.Query()[key]) > 1 {
			fail(w, 400, "请只选择一个办理场景和任务。")
			return
		}
	}
	result, err := s.store.ScenarioTaskData(r.Context(), ws.ID, u.ID, ids["assetId"], ids["activityId"], r.URL.Query().Get("scenario"), r.URL.Query().Get("task"))
	if err != nil {
		assessmentFailure(w, err)
		return
	}
	respond(w, 200, result)
}
func (s *server) applicationSources(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	items, err := s.store.ListApplicationSources(r.Context(), ws.ID, u.ID)
	if err != nil {
		assessmentFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items, "limit": 200})
}
func (s *server) createApplicationSource(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var input store.ApplicationSourceInput
	if !readJSON(w, r, &input) {
		return
	}
	token, err := sessionToken()
	if err != nil {
		fail(w, 503, "暂时无法生成来源令牌。")
		return
	}
	source, err := s.store.CreateApplicationSource(r.Context(), ws.ID, u.ID, input, sessionHash(token))
	if err != nil {
		assessmentFailure(w, err)
		return
	}
	respond(w, 201, map[string]any{"source": source, "token": token})
}
func (s *server) revokeApplicationSource(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		fail(w, 404, "找不到此应用来源。")
		return
	}
	if err = s.store.RevokeApplicationSource(r.Context(), ws.ID, u.ID, id); err != nil {
		assessmentFailure(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *server) applicationObservations(w http.ResponseWriter, r *http.Request) {
	digest, ok := discoveryAuth(w, r)
	if !ok {
		return
	}
	select {
	case s.runtimeSlots <- struct{}{}:
		defer func() { <-s.runtimeSlots }()
	default:
		fail(w, 429, "应用采集繁忙，请稍后重试。")
		return
	}
	var input store.ApplicationSubmission
	if !readJSON(w, r, &input) {
		return
	}
	receipt, err := s.store.ReceiveApplicationObservation(r.Context(), digest, input)
	if errors.Is(err, store.ErrForbidden) {
		fail(w, 403, "来源令牌或绑定范围无效。")
		return
	}
	if err != nil {
		assessmentFailure(w, err)
		return
	}
	respond(w, 200, receipt)
}
