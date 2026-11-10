package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) classificationRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/classification/assets"
	mux.HandleFunc("GET "+base, s.workspace(false, s.listAssetClassifications))
	mux.HandleFunc("GET "+base+"/{asset}", s.workspace(false, s.getAssetClassification))
	mux.HandleFunc("PUT "+base+"/{asset}", s.workspace(true, s.updateAssetClassification))
}

func classificationFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, "请核对分类、分级、判定理由和版本。")
	case errors.Is(err, store.ErrForbidden):
		fail(w, http.StatusForbidden, "当前账号无权确认资产分类分级。")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, "此资产的分类分级已更新。请刷新后重新核对。")
	default:
		storeFailure(w, err)
	}
}

func (s *server) listAssetClassifications(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	after, ok := optionalQueryID(w, r, "after")
	if !ok {
		return
	}
	filter := store.AssetClassificationFilter{
		After: after, Status: r.URL.Query().Get("status"), StatutoryCategory: r.URL.Query().Get("statutoryCategory"),
	}
	page, err := s.store.ListAssetClassifications(r.Context(), ws.ID, user.ID, filter)
	if err != nil {
		classificationFailure(w, err)
		return
	}
	respond(w, http.StatusOK, page)
}

func classificationAssetID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("asset"), 10, 64)
	if err != nil || id < 1 {
		fail(w, http.StatusNotFound, "找不到此资产。")
		return 0, false
	}
	return id, true
}

func (s *server) getAssetClassification(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	id, ok := classificationAssetID(w, r)
	if !ok {
		return
	}
	detail, err := s.store.GetAssetClassification(r.Context(), ws.ID, user.ID, id)
	if err != nil {
		classificationFailure(w, err)
		return
	}
	respond(w, http.StatusOK, detail)
}

func (s *server) updateAssetClassification(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	id, ok := classificationAssetID(w, r)
	if !ok {
		return
	}
	var payload struct {
		Revision          *int64 `json:"revision"`
		Category          string `json:"category"`
		BusinessLevel     string `json:"businessLevel"`
		StatutoryCategory string `json:"statutoryCategory"`
		Basis             string `json:"basis"`
	}
	if !readJSONLimit(w, r, &payload, 8<<10) {
		return
	}
	if payload.Revision == nil {
		classificationFailure(w, store.ErrInvalid)
		return
	}
	detail, err := s.store.UpdateAssetClassification(r.Context(), ws.ID, user.ID, id, store.AssetClassificationInput{
		Revision: *payload.Revision, Category: payload.Category, BusinessLevel: payload.BusinessLevel,
		StatutoryCategory: payload.StatutoryCategory, Basis: payload.Basis,
	})
	if err != nil {
		classificationFailure(w, err)
		return
	}
	respond(w, http.StatusOK, detail)
}
