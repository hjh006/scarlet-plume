package httpapi

import (
	"net/http"
	"strconv"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) scenarioCheckRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/scenario-check-runs"
	mux.HandleFunc("GET "+base, s.workspace(false, s.listScenarioChecks))
	mux.HandleFunc("POST "+base, s.workspace(false, s.createScenarioCheck))
	mux.HandleFunc("GET "+base+"/{run}", s.workspace(false, s.getScenarioCheck))
	mux.HandleFunc("GET "+base+"/{run}/export", s.workspace(false, s.exportScenarioCheck))
	mux.HandleFunc("POST "+base+"/{run}/findings/{finding}/remediation", s.workspace(false, s.proposeScenarioCheckRemediation))
}

func scenarioCheckRunID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("run"), 10, 64)
	if err != nil || id < 1 {
		fail(w, 404, "找不到这次技术检查。")
		return 0, false
	}
	return id, true
}

func (s *server) listScenarioChecks(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	for _, key := range []string{"assetId", "activityId", "scenario", "task"} {
		if len(r.URL.Query()[key]) > 1 {
			fail(w, 400, "请只选择一个检查场景和对象范围。")
			return
		}
	}
	assetID, ok := optionalQueryID(w, r, "assetId")
	if !ok {
		return
	}
	activityID, ok := optionalQueryID(w, r, "activityId")
	if !ok {
		return
	}
	filter := store.ScenarioCheckRunFilter{AssetID: assetID, ActivityID: activityID, Scenario: r.URL.Query().Get("scenario"), Task: r.URL.Query().Get("task")}
	scope, ok := runtimeEvidenceQuery(w, r)
	if !ok {
		return
	}
	filter.Scope = scope
	items, err := s.store.ListScenarioCheckRuns(r.Context(), ws.ID, u.ID, filter)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items, "limit": 50})
}

func (s *server) createScenarioCheck(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var input store.ScenarioCheckRunInput
	if !readJSONLimit(w, r, &input, 4<<10) {
		return
	}
	select {
	case s.runtimeSlots <- struct{}{}:
		defer func() { <-s.runtimeSlots }()
	default:
		fail(w, 429, "技术检查任务繁忙，请稍后重试。")
		return
	}
	item, err := s.store.CreateScenarioCheckRun(r.Context(), ws.ID, u.ID, input)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 201, item)
}

func (s *server) getScenarioCheck(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := scenarioCheckRunID(w, r)
	if !ok {
		return
	}
	item, err := s.store.GetScenarioCheckRun(r.Context(), ws.ID, u.ID, id)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, item)
}

func (s *server) exportScenarioCheck(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := scenarioCheckRunID(w, r)
	if !ok {
		return
	}
	format := r.URL.Query().Get("format")
	switch format {
	case "", "html", "json", "csv", "zip":
	default:
		fail(w, 400, "请选择HTML、JSON、CSV或ZIP证据包格式。")
		return
	}
	item, err := s.store.GetScenarioCheckRun(r.Context(), ws.ID, u.ID, id)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	writeScenarioCheckExport(w, item, ws, format)
}

func (s *server) proposeScenarioCheckRemediation(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := scenarioCheckRunID(w, r)
	if !ok {
		return
	}
	var input struct {
		Reason          string `json:"reason"`
		DurationMinutes int    `json:"durationMinutes"`
	}
	if !readJSONLimit(w, r, &input, 4<<10) {
		return
	}
	action, err := s.store.ProposeScenarioCheckRemediation(r.Context(), ws.ID, u.ID, id, r.PathValue("finding"), input.Reason, input.DurationMinutes)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 201, action)
}
