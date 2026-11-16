package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

const aiSecurityRuntimePath = "/api/v1/ai-security/runtime-events"
const aiSecurityRuntimePrefix = "spai_"

func (s *server) aiSecurityRuntimeRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/ai-security"
	mux.HandleFunc("GET "+base+"/runtime-evidence", s.workspace(false, s.aiSecurityRuntimeEvidence))
	mux.HandleFunc("GET "+base+"/runtime-sources", s.workspace(false, s.aiSecurityRuntimeEvidence))
	mux.HandleFunc("POST "+base+"/runtime-sources", s.workspace(true, s.createAISecurityRuntimeSource))
	mux.HandleFunc("POST "+base+"/runtime-sources/{source}/rotate-token", s.workspace(true, s.rotateAISecurityRuntimeSource))
	mux.HandleFunc("POST "+base+"/runtime-sources/{source}/revoke", s.workspace(true, s.revokeAISecurityRuntimeSource))
	mux.HandleFunc("POST "+aiSecurityRuntimePath, s.receiveAISecurityRuntimeEvent)
}
func (s *server) aiSecurityRuntimeEvidence(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	limit, err := gatewayTaskLimit(r)
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	q := store.AISecurityRuntimeEvidenceQuery{Before: r.URL.Query().Get("before"), Limit: limit, Kind: r.URL.Query().Get("kind"), Outcome: r.URL.Query().Get("outcome")}
	for _, key := range []string{"before", "limit", "sourceId", "kind", "outcome"} {
		if len(r.URL.Query()[key]) > 1 {
			securityPlatformFailure(w, store.ErrInvalid)
			return
		}
	}
	if values, present := r.URL.Query()["sourceId"]; present {
		q.SourceID, err = strconv.ParseInt(values[0], 10, 64)
		if err != nil || q.SourceID < 1 {
			securityPlatformFailure(w, store.ErrInvalid)
			return
		}
	}
	out, err := s.store.AISecurityRuntimeEvidencePage(r.Context(), ws.ID, u.ID, q)
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	respond(w, 200, out)
}
func (s *server) createAISecurityRuntimeSource(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	if ws.Role != "owner" {
		securityPlatformFailure(w, store.ErrForbidden)
		return
	}
	var in store.AISecurityRuntimeSourceInput
	if !readJSONLimit(w, r, &in, 8192) {
		return
	}
	random, err := sessionToken()
	if err != nil {
		storeFailure(w, err)
		return
	}
	token := aiSecurityRuntimePrefix + random
	source, err := s.store.CreateAISecurityRuntimeSource(r.Context(), ws.ID, u.ID, in, sessionHash(token))
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, 201, map[string]any{"source": source, "token": token})
}
func (s *server) revokeAISecurityRuntimeSource(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	if ws.Role != "owner" {
		securityPlatformFailure(w, store.ErrForbidden)
		return
	}
	var in struct{}
	if !readJSONLimit(w, r, &in, 1024) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("source"), 10, 64)
	if err != nil || id < 1 {
		fail(w, 400, "请选择有效来源。")
		return
	}
	if err = s.store.RevokeAISecurityRuntimeSource(r.Context(), ws.ID, u.ID, id); err != nil {
		securityPlatformFailure(w, err)
		return
	}
	respond(w, 200, map[string]bool{"revoked": true})
}
func (s *server) rotateAISecurityRuntimeSource(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	if ws.Role != "owner" {
		securityPlatformFailure(w, store.ErrForbidden)
		return
	}
	var in struct {
		ExpectedTokenVersion int64 `json:"expectedTokenVersion"`
	}
	if !readJSONLimit(w, r, &in, 1024) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("source"), 10, 64)
	if err != nil || id < 1 || in.ExpectedTokenVersion < 1 {
		securityPlatformFailure(w, store.ErrInvalid)
		return
	}
	random, err := sessionToken()
	if err != nil {
		storeFailure(w, err)
		return
	}
	token := aiSecurityRuntimePrefix + random
	source, err := s.store.RotateAISecurityRuntimeSource(r.Context(), ws.ID, u.ID, id, in.ExpectedTokenVersion, sessionHash(token))
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, 200, map[string]any{"source": source, "token": token})
}

func (s *server) receiveAISecurityRuntimeEvent(w http.ResponseWriter, r *http.Request) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || !strings.HasPrefix(token, aiSecurityRuntimePrefix) || len(token) != len(aiSecurityRuntimePrefix)+43 {
		fail(w, 401, "请使用有效的独立AI运行时来源令牌。")
		return
	}
	select {
	case s.runtimeSlots <- struct{}{}:
		defer func() { <-s.runtimeSlots }()
	default:
		fail(w, 429, "运行回执接收繁忙，请保留本地审计后重试。")
		return
	}
	var in aisecurity.AuditRecord
	if !readJSONLimit(w, r, &in, 64*1024) {
		return
	}
	out, err := s.store.ReceiveAISecurityRuntimeEvent(r.Context(), sessionHash(token), in)
	if err != nil {
		if errors.Is(err, store.ErrAutomationUnauthorized) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			fail(w, 401, "AI运行时来源令牌无效、已撤销或创建者权限已失效。")
			return
		}
		securityPlatformFailure(w, err)
		return
	}
	respond(w, 200, out)
}
