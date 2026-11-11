package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) activityRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/activities"
	mux.HandleFunc("GET "+base, s.workspace(false, s.listActivities))
	mux.HandleFunc("POST "+base, s.workspace(true, s.createActivity))
	mux.HandleFunc("GET "+base+"/{activity}", s.workspace(false, s.getActivity))
	mux.HandleFunc("PUT "+base+"/{activity}", s.workspace(false, s.updateActivity))
	mux.HandleFunc("GET "+base+"/{activity}/history", s.workspace(false, s.activityHistory(false)))
	mux.HandleFunc("GET "+base+"/{activity}/history/export", s.workspace(false, s.activityHistory(true)))
}

func optionalQueryID(w http.ResponseWriter, r *http.Request, key string) (int64, bool) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		fail(w, http.StatusBadRequest, "筛选或分页参数无效。")
		return 0, false
	}
	return id, true
}

func activityFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, "请检查活动类型、字段长度及必填项。数据出境活动须填写接收方和目的地域。")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, "活动已被其他人更新。请重新读取当前版本，再核对并保存。")
	case errors.Is(err, store.ErrForbidden):
		fail(w, http.StatusForbidden, "当前账号无权登记处理活动。")
	default:
		storeFailure(w, err)
	}
}

func (s *server) listActivities(w http.ResponseWriter, r *http.Request, _ store.User, workspace store.Workspace) {
	before, ok := optionalQueryID(w, r, "before")
	if !ok {
		return
	}
	assetID, ok := optionalQueryID(w, r, "assetId")
	if !ok {
		return
	}
	const limit = 50
	items, err := s.store.ListActivities(r.Context(), workspace.ID, r.URL.Query().Get("kind"), assetID, before, limit+1)
	if err != nil {
		activityFailure(w, err)
		return
	}
	var nextCursor *int64
	if len(items) > limit {
		items = items[:limit]
		nextCursor = &items[limit-1].ID
	}
	respond(w, http.StatusOK, map[string]any{"items": items, "nextCursor": nextCursor})
}

func (s *server) getActivity(w http.ResponseWriter, r *http.Request, _ store.User, workspace store.Workspace) {
	id, err := strconv.ParseInt(r.PathValue("activity"), 10, 64)
	if err != nil || id < 1 {
		fail(w, http.StatusNotFound, "找不到此处理活动。")
		return
	}
	activity, err := s.store.Activity(r.Context(), workspace.ID, id)
	if err != nil {
		activityFailure(w, err)
		return
	}
	respond(w, http.StatusOK, activity)
}

func (s *server) createActivity(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	var input store.ActivityInput
	if !readJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Purpose = strings.TrimSpace(input.Purpose)
	input.Recipient = strings.TrimSpace(input.Recipient)
	input.Region = strings.TrimSpace(input.Region)
	input.RetentionPolicy = strings.TrimSpace(input.RetentionPolicy)
	input.TransferPath = strings.TrimSpace(input.TransferPath)
	input.Safeguards = strings.TrimSpace(input.Safeguards)
	activity, err := s.store.CreateActivity(r.Context(), workspace.ID, user.ID, input)
	if err != nil {
		activityFailure(w, err)
		return
	}
	respond(w, http.StatusCreated, activity)
}

func activityPathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("activity"), 10, 64)
	if err != nil || id < 1 {
		fail(w, http.StatusNotFound, "找不到此处理活动。")
		return 0, false
	}
	return id, true
}
func (s *server) updateActivity(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	id, ok := activityPathID(w, r)
	if !ok {
		return
	}
	var input store.ActivityUpdateInput
	if !readJSON(w, r, &input) {
		return
	}
	item, err := s.store.UpdateActivity(r.Context(), workspace.ID, user.ID, id, input)
	if err != nil {
		activityFailure(w, err)
		return
	}
	respond(w, http.StatusOK, item)
}
func (s *server) activityHistory(export bool) func(http.ResponseWriter, *http.Request, store.User, store.Workspace) {
	return func(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
		id, ok := activityPathID(w, r)
		if !ok {
			return
		}
		before, ok := optionalQueryID(w, r, "before")
		if !ok {
			return
		}
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 100 {
				fail(w, http.StatusBadRequest, "历史分页数量应为1至100。")
				return
			}
			limit = parsed
		}
		if export {
			if before != 0 {
				fail(w, http.StatusBadRequest, "导出使用完整活动范围，请勿填写分页游标。")
				return
			}
			limit = 1000
		}
		items, err := s.store.ActivityHistory(r.Context(), workspace.ID, user.ID, id, before, limit+1)
		if err != nil {
			activityFailure(w, err)
			return
		}
		if export && len(items) > limit {
			fail(w, http.StatusUnprocessableEntity, "历史记录超过1000条，请通过分页历史接口完整提取。")
			return
		}
		var nextCursor *int64
		if len(items) > limit {
			items = items[:limit]
			nextCursor = &items[len(items)-1].ID
		}
		if export {
			w.Header().Set("Content-Disposition", `attachment; filename="scarlet-activity-history.json"`)
		}
		respond(w, http.StatusOK, map[string]any{"activityId": id, "items": items, "nextCursor": nextCursor, "limit": limit})
	}
}
