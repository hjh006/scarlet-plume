package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) checkRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}"
	mux.HandleFunc("GET "+base+"/requirements", s.workspace(false, s.listRequirements))
	mux.HandleFunc("GET "+base+"/checks", s.workspace(false, s.listChecks))
	mux.HandleFunc("POST "+base+"/checks", s.workspace(true, s.createCheck))
	mux.HandleFunc("GET "+base+"/checks/{check}", s.workspace(false, s.checkDetail))
	mux.HandleFunc("GET "+base+"/checks/{check}/export", s.workspace(false, s.exportCheck))
	mux.HandleFunc("POST "+base+"/checks/{check}/evidence", s.workspace(true, s.addCheckEvidence))
	mux.HandleFunc("POST "+base+"/checks/{check}/feishu", s.workspace(true, s.addFeishuEvidence))
	mux.HandleFunc("POST "+base+"/checks/{check}/submit", s.workspace(true, s.submitCheck))
	mux.HandleFunc("POST "+base+"/checks/{check}/review", s.workspace(false, s.reviewCheck))
}

func checkFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrForbidden):
		fail(w, http.StatusForbidden, "当前账号无权执行此操作。检查创建人、提交人和证据登记人须回避复核。")
	case errors.Is(err, store.ErrEvidenceRequired):
		fail(w, http.StatusConflict, "缺少当前检查所需的证据。整改后须登记新复测。飞书检查需登记整改，并用同一目标的新清单核对授权已移除。")
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, "检查、证据或复核字段无效。请核对来源、检查目标及时间。")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, "检查状态、提交版本或证据已变化，请刷新后核对。")
	default:
		storeFailure(w, err)
	}
}

func checkID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("check"), 10, 64)
	if err != nil || id < 1 {
		fail(w, http.StatusNotFound, "找不到此检查。")
		return 0, false
	}
	return id, true
}

func (s *server) listRequirements(w http.ResponseWriter, r *http.Request, _ store.User, _ store.Workspace) {
	requirements, err := s.store.ListRequirements(r.Context())
	if err != nil {
		checkFailure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"items": requirements})
}

func (s *server) listChecks(w http.ResponseWriter, r *http.Request, _ store.User, workspace store.Workspace) {
	var before int64
	if raw := r.URL.Query().Get("before"); raw != "" {
		var err error
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before < 1 {
			fail(w, http.StatusBadRequest, "分页参数无效。")
			return
		}
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "draft", "in_review", "needs_work", "verified":
	default:
		fail(w, http.StatusBadRequest, "检查状态无效。")
		return
	}
	const limit = 50
	items, err := s.store.ListChecks(r.Context(), workspace.ID, status, before, limit+1)
	if err != nil {
		checkFailure(w, err)
		return
	}
	var nextCursor *int64
	if len(items) > limit {
		items = items[:limit]
		nextCursor = &items[limit-1].ID
	}
	respond(w, http.StatusOK, map[string]any{"items": items, "nextCursor": nextCursor})
}

func (s *server) createCheck(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	var input store.CheckInput
	if !readJSON(w, r, &input) {
		return
	}
	input.RequirementCode = strings.TrimSpace(input.RequirementCode)
	input.OwnerTeam = strings.TrimSpace(input.OwnerTeam)
	input.Note = strings.TrimSpace(input.Note)
	input.FeishuTargetDigest = strings.TrimSpace(input.FeishuTargetDigest)
	check, err := s.store.CreateCheck(r.Context(), workspace.ID, user.ID, input)
	if err != nil {
		checkFailure(w, err)
		return
	}
	respond(w, http.StatusCreated, check)
}

func (s *server) readCheck(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) (store.CheckDetail, bool) {
	id, ok := checkID(w, r)
	if !ok {
		return store.CheckDetail{}, false
	}
	detail, err := s.store.CheckDetail(r.Context(), workspace.ID, user.ID, id)
	if err != nil {
		checkFailure(w, err)
		return store.CheckDetail{}, false
	}
	return detail, true
}

func (s *server) checkDetail(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	if detail, ok := s.readCheck(w, r, user, workspace); ok {
		respond(w, http.StatusOK, detail)
	}
}

func (s *server) exportCheck(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	detail, ok := s.readCheck(w, r, user, workspace)
	if !ok {
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="control-`+strconv.FormatInt(detail.ID, 10)+`.json"`)
	respond(w, http.StatusOK, map[string]any{
		"workspace": workspace.Slug, "exportedAt": time.Now().UTC(),
		"check": detail.Check, "requirement": detail.Requirement,
		"evidence": detail.Evidence, "events": detail.Events,
	})
}

func checkMutationResult(w http.ResponseWriter, err error) {
	if err != nil {
		checkFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) addCheckEvidence(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	var input store.EvidenceInput
	id, ok := checkID(w, r)
	if !ok || !readJSON(w, r, &input) {
		return
	}
	input.SourceSystem = strings.TrimSpace(input.SourceSystem)
	input.SourceRecord = strings.TrimSpace(input.SourceRecord)
	input.Summary = strings.TrimSpace(input.Summary)
	checkMutationResult(w, s.store.AddEvidence(r.Context(), workspace.ID, user.ID, id, input))
}

func (s *server) submitCheck(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	id, ok := checkID(w, r)
	if !ok || !readJSON(w, r, &struct{}{}) {
		return
	}
	checkMutationResult(w, s.store.SubmitCheck(r.Context(), workspace.ID, user.ID, id))
}

func (s *server) reviewCheck(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	var input struct {
		ReviewRevision int64  `json:"reviewRevision"`
		Decision       string `json:"decision"`
		Note           string `json:"note"`
	}
	id, ok := checkID(w, r)
	if !ok || !readJSON(w, r, &input) {
		return
	}
	checkMutationResult(w, s.store.ReviewCheck(r.Context(), workspace.ID, user.ID, id,
		input.ReviewRevision, input.Decision, strings.TrimSpace(input.Note)))
}
