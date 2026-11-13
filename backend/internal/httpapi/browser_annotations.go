package httpapi

import (
	"errors"
	"net/http"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) browserAnnotationRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/browser-annotations"
	mux.HandleFunc("GET "+base, s.workspace(false, s.browserAnnotations))
	mux.HandleFunc("POST "+base, s.workspace(false, s.createBrowserAnnotation))
	mux.HandleFunc("POST "+base+"/match", s.workspace(false, s.matchBrowserAnnotation))
	mux.HandleFunc("GET "+base+"/{id}", s.workspace(false, s.browserAnnotation))
}
func (s *server) matchBrowserAnnotation(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var input store.BrowserAnnotationInput
	if !readJSONLimit(w, r, &input, 128<<10) {
		return
	}
	items, e := s.store.MatchBrowserAnnotation(r.Context(), ws.ID, u.ID, input)
	if e != nil {
		browserAnnotationFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"candidates": items, "coverage": "imported_openapi_identified_fields_only", "status": "candidate"})
}
func browserAnnotationFailure(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, store.ErrInvalid):
		fail(w, 400, "请检查页面标识、字段标签和关联资产；一次支持1至100个字段，不接收页面正文或输入值。")
	case errors.Is(e, store.ErrForbidden):
		fail(w, 403, "仅工作区负责人或操作员可保存人工字段标注。")
	default:
		storeFailure(w, e)
	}
}
func (s *server) browserAnnotations(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	asset, ok := optionalQueryID(w, r, "assetId")
	if !ok {
		return
	}
	before, ok := optionalQueryID(w, r, "before")
	if !ok {
		return
	}
	items, e := s.store.ListBrowserAnnotations(r.Context(), ws.ID, u.ID, asset, before)
	if e != nil {
		browserAnnotationFailure(w, e)
		return
	}
	var next *int64
	if len(items) == 50 {
		next = &items[len(items)-1].ID
	}
	respond(w, 200, map[string]any{"items": items, "limit": 50, "nextCursor": next})
}
func (s *server) createBrowserAnnotation(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var input store.BrowserAnnotationInput
	if !readJSONLimit(w, r, &input, 128<<10) {
		return
	}
	item, e := s.store.CreateBrowserAnnotation(r.Context(), ws.ID, u.ID, input)
	if e != nil {
		browserAnnotationFailure(w, e)
		return
	}
	respond(w, 201, item)
}
func (s *server) browserAnnotation(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := discoveryID(w, r)
	if !ok {
		return
	}
	item, e := s.store.GetBrowserAnnotation(r.Context(), ws.ID, u.ID, id)
	if e != nil {
		browserAnnotationFailure(w, e)
		return
	}
	respond(w, 200, item)
}
