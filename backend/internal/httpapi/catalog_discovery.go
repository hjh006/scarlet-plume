package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

const CatalogDiscoveryClaimPath = "/api/v1/discovery-agent/claim"
const CatalogDiscoveryRunPath = "/api/v1/discovery-agent/runs"

func (s *server) catalogDiscoveryRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}"
	s.valueFingerprintRoutes(mux)
	mux.HandleFunc("GET "+base+"/discovery/sources", s.workspace(false, s.listDiscoverySources))
	mux.HandleFunc("POST "+base+"/discovery/sources", s.workspace(false, s.saveDiscoverySource))
	mux.HandleFunc("PUT "+base+"/discovery/sources/{id}", s.workspace(false, s.saveDiscoverySource))
	mux.HandleFunc("POST "+base+"/discovery/sources/{id}/token", s.workspace(false, s.rotateDiscoveryToken))
	mux.HandleFunc("POST "+base+"/discovery/sources/{id}/run", s.workspace(false, s.queueDiscovery))
	mux.HandleFunc("GET "+base+"/discovery/sources/{id}/runs", s.workspace(false, s.listDiscoveryRuns))
	mux.HandleFunc("GET "+base+"/discovery/rules", s.workspace(false, s.listDiscoveryRules))
	mux.HandleFunc("POST "+base+"/discovery/rules", s.workspace(false, s.saveDiscoveryRule))
	mux.HandleFunc("POST "+base+"/discovery/rules/publish", s.workspace(false, s.publishDiscoveryRule))
	mux.HandleFunc("GET "+base+"/catalog", s.workspace(false, s.listCatalog))
	mux.HandleFunc("GET "+base+"/catalog/summary", s.workspace(false, s.catalogSummary))
	mux.HandleFunc("GET "+base+"/catalog/{id}/history", s.workspace(false, s.catalogHistory))
	mux.HandleFunc("PUT "+base+"/catalog/{id}/metadata", s.workspace(false, s.confirmCatalog))
	mux.HandleFunc("POST "+base+"/catalog/cmdb", s.workspace(false, s.importCMDB))
	mux.HandleFunc("POST "+CatalogDiscoveryClaimPath, s.claimDiscovery)
	mux.HandleFunc("POST "+CatalogDiscoveryRunPath, s.submitDiscovery)
}
func discoveryID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, e := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if e != nil || id <= 0 {
		fail(w, 400, "记录标识无效。")
		return 0, false
	}
	return id, true
}
func (s *server) listDiscoverySources(w http.ResponseWriter, r *http.Request, _ store.User, ws store.Workspace) {
	v, e := s.store.ListDiscoverySources(r.Context(), ws.ID)
	if e != nil {
		automationFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"items": v})
}
func (s *server) saveDiscoverySource(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	var input store.DiscoverySourceInput
	if !readJSON(w, r, &input) {
		return
	}
	var id int64
	var ok bool
	token := ""
	digest := ""
	if r.Method == http.MethodPut {
		id, ok = discoveryID(w, r)
		if !ok {
			return
		}
	} else {
		var e error
		token, e = sessionToken()
		if e != nil {
			storeFailure(w, e)
			return
		}
		digest = sessionHash(token)
	}
	v, e := s.store.SaveDiscoverySource(r.Context(), ws.ID, user.ID, id, input, digest)
	if e != nil {
		automationFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"source": v, "token": token})
}
func (s *server) queueDiscovery(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	id, ok := discoveryID(w, r)
	if !ok {
		return
	}
	var input struct {
		Revision int64  `json:"revision"`
		Mode     string `json:"mode"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if e := s.store.QueueDiscoverySource(r.Context(), ws.ID, user.ID, id, input.Revision, input.Mode); e != nil {
		automationFailure(w, e)
		return
	}
	respond(w, 202, map[string]bool{"queued": true})
}
func (s *server) listDiscoveryRuns(w http.ResponseWriter, r *http.Request, _ store.User, ws store.Workspace) {
	id, ok := discoveryID(w, r)
	if !ok {
		return
	}
	v, e := s.store.ListDiscoveryRuns(r.Context(), ws.ID, id)
	if e != nil {
		automationFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"items": v})
}
func (s *server) listDiscoveryRules(w http.ResponseWriter, r *http.Request, _ store.User, ws store.Workspace) {
	v, e := s.store.ListDiscoveryRules(r.Context(), ws.ID)
	if e != nil {
		automationFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"items": v})
}
func (s *server) saveDiscoveryRule(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	var input collector.DiscoveryRule
	if !readJSON(w, r, &input) {
		return
	}
	if e := collector.ValidateDiscoveryRule(input); e != nil {
		fail(w, 400, e.Error())
		return
	}
	v, e := s.store.SaveDiscoveryRule(r.Context(), ws.ID, user.ID, input)
	if e != nil {
		automationFailure(w, e)
		return
	}
	respond(w, 201, v)
}
func (s *server) publishDiscoveryRule(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	var input struct {
		Key     string `json:"key"`
		Version int    `json:"version"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if e := s.store.PublishDiscoveryRule(r.Context(), ws.ID, user.ID, input.Key, input.Version); e != nil {
		automationFailure(w, e)
		return
	}
	respond(w, 200, map[string]bool{"published": true})
}
func (s *server) listCatalog(w http.ResponseWriter, r *http.Request, _ store.User, ws store.Workspace) {
	v, e := s.store.ListCatalogEntries(r.Context(), ws.ID, r.URL.Query().Get("kind"), r.URL.Query().Get("q"))
	if e != nil {
		automationFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"items": v, "limit": 1000, "truncated": len(v) == 1000})
}
func (s *server) catalogSummary(w http.ResponseWriter, r *http.Request, _ store.User, ws store.Workspace) {
	v, e := s.store.CatalogDiscoverySummary(r.Context(), ws.ID)
	if e != nil {
		automationFailure(w, e)
		return
	}
	respond(w, 200, v)
}
func (s *server) catalogHistory(w http.ResponseWriter, r *http.Request, _ store.User, ws store.Workspace) {
	id, ok := discoveryID(w, r)
	if !ok {
		return
	}
	v, e := s.store.CatalogEntryHistory(r.Context(), ws.ID, id)
	if e != nil {
		automationFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"items": v})
}
func (s *server) confirmCatalog(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	id, ok := discoveryID(w, r)
	if !ok {
		return
	}
	var input struct {
		Revision int64             `json:"revision"`
		Metadata map[string]string `json:"metadata"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if e := s.store.ConfirmCatalogMetadata(r.Context(), ws.ID, user.ID, id, input.Revision, input.Metadata); e != nil {
		automationFailure(w, e)
		return
	}
	respond(w, 200, map[string]bool{"saved": true})
}
func (s *server) importCMDB(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	var input store.CMDBImport
	if !readJSON(w, r, &input) {
		return
	}
	v, e := s.store.ImportCMDB(r.Context(), ws.ID, user.ID, input)
	if e != nil {
		automationFailure(w, e)
		return
	}
	respond(w, 200, v)
}
func discoveryAuth(w http.ResponseWriter, r *http.Request) (string, bool) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == r.Header.Get("Authorization") || len(token) != 43 {
		fail(w, 401, "采集任务令牌无效。")
		return "", false
	}
	return sessionHash(token), true
}
func (s *server) claimDiscovery(w http.ResponseWriter, r *http.Request) {
	digest, ok := discoveryAuth(w, r)
	if !ok {
		return
	}
	var input struct{}
	if !readJSON(w, r, &input) {
		return
	}
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		storeFailure(w, e)
		return
	}
	claim, e := s.store.ClaimDiscovery(r.Context(), digest, hex.EncodeToString(b))
	if e != nil {
		automationFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"claim": claim})
}
func (s *server) submitDiscovery(w http.ResponseWriter, r *http.Request) {
	digest, ok := discoveryAuth(w, r)
	if !ok {
		return
	}
	var input store.DiscoverySubmission
	if !readJSONLimit(w, r, &input, 4*1024*1024) {
		return
	}
	v, e := s.store.SubmitDiscovery(r.Context(), digest, input)
	if e != nil {
		automationFailure(w, e)
		return
	}

	respond(w, 200, v)
}

func (s *server) rotateDiscoveryToken(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	id, ok := discoveryID(w, r)
	if !ok {
		return
	}
	var input struct {
		Revision int64 `json:"revision"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	token, e := sessionToken()
	if e != nil {
		storeFailure(w, e)
		return
	}
	if e = s.store.RotateDiscoveryToken(r.Context(), ws.ID, user.ID, id, input.Revision, sessionHash(token)); e != nil {
		automationFailure(w, e)
		return
	}
	respond(w, 200, map[string]string{"token": token})
}
