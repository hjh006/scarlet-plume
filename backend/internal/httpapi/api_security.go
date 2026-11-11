package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/apisecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) apiSecurityRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/api-security/scans"
	mux.HandleFunc("GET "+base, s.workspace(false, s.listAPISecurity))
	mux.HandleFunc("POST "+base, s.workspace(true, s.scanAPISecurity))
	mux.HandleFunc("GET "+base+"/{scan}", s.workspace(false, s.readAPISecurity))
	mux.HandleFunc("GET "+base+"/{scan}/export", s.workspace(false, s.exportAPISecurity))
	s.apiLifecycleRoutes(mux)
}

func (s *server) listAPISecurity(w http.ResponseWriter, r *http.Request, _ store.User, workspace store.Workspace) {
	items, err := s.store.ListAPISecurityScans(r.Context(), workspace.ID)
	if err != nil {
		storeFailure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"items": items})
}

func (s *server) scanAPISecurity(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	var input struct {
		Source   string          `json:"source"`
		Document json.RawMessage `json:"document"`
	}
	if !readJSONLimit(w, r, &input, 512<<10) {
		return
	}
	input.Source = strings.TrimSpace(input.Source)
	if !validText(input.Source, 80) {
		fail(w, http.StatusBadRequest, "请填写不超过 80 字的来源别名。")
		return
	}
	scan, err := apisecurity.Analyze(input.Document)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	projection, err := json.Marshal(scan)
	if err != nil {
		storeFailure(w, err)
		return
	}
	digest := sha256.Sum256(projection)
	result, err := s.store.ImportAPISecurityScan(r.Context(), workspace.ID, user.ID, input.Source, hex.EncodeToString(digest[:]), scan)
	switch {
	case errors.Is(err, store.ErrForbidden):
		fail(w, http.StatusForbidden, "当前角色不能导入 API 规范。")
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, "API 规范或来源别名无效。")
	case err != nil:
		storeFailure(w, err)
	case result.Replayed:
		respond(w, http.StatusOK, result)
	default:
		respond(w, http.StatusCreated, result)
	}
}

func (s *server) loadAPISecurity(w http.ResponseWriter, r *http.Request, workspace store.Workspace) (store.APISecurityScan, bool) {
	id, err := strconv.ParseInt(r.PathValue("scan"), 10, 64)
	if err != nil || id < 1 {
		fail(w, http.StatusBadRequest, "API 扫描编号无效。")
		return store.APISecurityScan{}, false
	}
	scan, err := s.store.APISecurityScan(r.Context(), workspace.ID, id)
	if err != nil {
		storeFailure(w, err)
		return store.APISecurityScan{}, false
	}
	return scan, true
}

func (s *server) readAPISecurity(w http.ResponseWriter, r *http.Request, _ store.User, workspace store.Workspace) {
	if scan, ok := s.loadAPISecurity(w, r, workspace); ok {
		respond(w, http.StatusOK, scan)
	}
}

func (s *server) exportAPISecurity(w http.ResponseWriter, r *http.Request, _ store.User, workspace store.Workspace) {
	if scan, ok := s.loadAPISecurity(w, r, workspace); ok {
		w.Header().Set("Content-Disposition", `attachment; filename="scarlet-api-security-`+strconv.FormatInt(scan.ID, 10)+`.json"`)
		respond(w, http.StatusOK, scan)
	}
}
