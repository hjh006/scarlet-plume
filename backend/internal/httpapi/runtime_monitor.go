package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) runtimeMonitorRoutes(mux *http.ServeMux) {
	const workspace = "/api/v1/workspaces/{slug}"
	mux.HandleFunc("GET "+workspace+"/api-security/runtime/monitor", s.workspace(false, s.runtimeMonitor))
	mux.HandleFunc("GET "+workspace+"/audit/runtime", s.workspace(false, s.runtimeAudit(false)))
	mux.HandleFunc("GET "+workspace+"/audit/runtime/export", s.workspace(false, s.runtimeAudit(true)))
}

func runtimeWindowQuery(r *http.Request, defaultHours int) (int, int64, error) {
	query := r.URL.Query()
	hours := defaultHours
	if raw := query.Get("hours"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return 0, 0, store.ErrInvalid
		}
		hours = value
	}
	var sourceID int64
	if raw := query.Get("sourceId"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			return 0, 0, store.ErrInvalid
		}
		sourceID = value
	}
	return hours, sourceID, nil
}

func (s *server) runtimeMonitor(w http.ResponseWriter, r *http.Request, _ store.User, workspace store.Workspace) {
	hours, sourceID, err := runtimeWindowQuery(r, 1)
	if err != nil {
		fail(w, http.StatusBadRequest, "请核对监测时间范围和来源。")
		return
	}
	monitor, err := s.store.RuntimeMonitor(r.Context(), workspace.ID, hours, sourceID)
	if errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, "监测范围支持最近 1、6 或 24 小时。")
		return
	}
	if err != nil {
		storeFailure(w, err)
		return
	}
	respond(w, http.StatusOK, monitor)
}

func (s *server) runtimeAudit(export bool) func(http.ResponseWriter, *http.Request, store.User, store.Workspace) {
	return func(w http.ResponseWriter, r *http.Request, _ store.User, workspace store.Workspace) {
		hours, sourceID, err := runtimeWindowQuery(r, 24)
		if err != nil {
			fail(w, http.StatusBadRequest, "请核对审计时间范围和来源。")
			return
		}
		query := r.URL.Query()
		filter := store.RuntimeAuditFilter{
			Hours: hours, SourceID: sourceID,
			Method: strings.ToUpper(strings.TrimSpace(query.Get("method"))), Route: strings.TrimSpace(query.Get("route")),
		}
		if raw := query.Get("status"); raw != "" {
			filter.Status, err = strconv.Atoi(raw)
			if err != nil || filter.Status < 100 || filter.Status > 599 {
				fail(w, http.StatusBadRequest, "请填写有效的 HTTP 状态码。")
				return
			}
		}
		page, err := s.store.RuntimeAudit(r.Context(), workspace.ID, filter)
		if errors.Is(err, store.ErrInvalid) {
			fail(w, http.StatusBadRequest, "请核对审计时间范围、HTTP 方法和路由筛选条件。")
			return
		}
		if err != nil {
			storeFailure(w, err)
			return
		}
		if export {
			w.Header().Set("Content-Disposition", `attachment; filename="scarlet-runtime-audit.json"`)
		}
		respond(w, http.StatusOK, page)
	}
}
