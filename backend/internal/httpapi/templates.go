package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) templateRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/templates"
	mux.HandleFunc("GET "+base, s.workspace(false, s.listTemplates))
	mux.HandleFunc("GET "+base+"/{revision}", s.workspace(false, s.getTemplate))
	mux.HandleFunc("POST "+base, s.workspace(false, s.publishTemplate))
}

func templateFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrForbidden):
		fail(w, http.StatusForbidden, "只有工作区负责人可以发布检查模板。")
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, "请核对模板标识、场景、监管要求，以及各检查项的适用条件、取证要求和验收标准。")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, "模板标识已使用，或发布基于旧版本。请刷新后再试。")
	default:
		storeFailure(w, err)
	}
}

func (s *server) listTemplates(w http.ResponseWriter, r *http.Request, _ store.User, workspace store.Workspace) {
	items, err := s.store.ListTemplates(r.Context(), workspace.ID, r.URL.Query().Get("kind"))
	if err != nil {
		templateFailure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"items": items})
}

func (s *server) getTemplate(w http.ResponseWriter, r *http.Request, _ store.User, workspace store.Workspace) {
	id, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
	if err != nil || id < 1 {
		fail(w, http.StatusNotFound, "找不到此模板版本。")
		return
	}
	template, err := s.store.Template(r.Context(), workspace.ID, id)
	if err != nil {
		templateFailure(w, err)
		return
	}
	respond(w, http.StatusOK, template)
}

func (s *server) publishTemplate(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	if workspace.Role != "owner" {
		templateFailure(w, store.ErrForbidden)
		return
	}
	var input store.TemplateInput
	if !readJSON(w, r, &input) {
		return
	}
	input.Key = strings.TrimSpace(input.Key)
	input.Title = strings.TrimSpace(input.Title)
	for i := range input.Items {
		item := &input.Items[i]
		item.Key = strings.TrimSpace(item.Key)
		item.Title = strings.TrimSpace(item.Title)
		item.RequirementCode = strings.TrimSpace(item.RequirementCode)
		item.Applicability = strings.TrimSpace(item.Applicability)
		item.EvidenceRequirements = strings.TrimSpace(item.EvidenceRequirements)
		item.AcceptanceCriteria = strings.TrimSpace(item.AcceptanceCriteria)
	}
	template, err := s.store.PublishTemplate(r.Context(), workspace.ID, user.ID, input)
	if err != nil {
		templateFailure(w, err)
		return
	}
	respond(w, http.StatusCreated, template)
}
