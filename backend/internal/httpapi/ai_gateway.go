package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"net/http"
	"os"
	"strconv"
	"strings"
)

func (s *server) gatewayRoutes(mux *http.ServeMux) {
	s.gatewayTaskRoutes(mux)
	const base = "/api/v1/workspaces/{slug}/ai-gateway"
	for _, route := range []struct {
		pattern string
		fn      workspaceAction
	}{
		{"GET " + base, s.getGateway}, {"PUT " + base + "/config", s.saveGateway}, {"GET " + base + "/config/export", s.exportGateway},
		{"POST " + base + "/credentials", s.createGatewayCredential}, {"DELETE " + base + "/credentials/{id}", s.revokeGatewayCredential},
		{"GET " + base + "/audit", s.getGatewayAudit}, {"GET " + base + "/audit/export", s.getGatewayAudit}, {"GET " + base + "/reservations", s.getGatewayReservations}, {"POST " + base + "/cache/invalidate", s.invalidateGatewayCache},
	} {
		mux.HandleFunc(route.pattern, s.workspace(false, route.fn))
	}
	mux.HandleFunc("POST /api/v1/internal/ai-gateway/cache/get", s.getGatewayCache)
	mux.HandleFunc("POST /api/v1/internal/ai-gateway/cache/put", s.putGatewayCache)
	mux.HandleFunc("POST /api/v1/internal/ai-gateway/resolve", s.resolveGateway)
	mux.HandleFunc("POST /api/v1/internal/ai-gateway/authorize", s.authorizeGateway)
	mux.HandleFunc("POST /api/v1/internal/ai-gateway/settle", s.settleGateway)
	mux.HandleFunc("POST /api/v1/internal/ai-gateway/heartbeat", s.heartbeatGateway)
	mux.HandleFunc("POST /api/v1/internal/ai-gateway/events", s.recordGatewayDenied)
	mux.HandleFunc("GET /api/v1/internal/ai-gateway/audit/export", s.exportGatewayMachineAudit)
}
func gatewayFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrGatewayCircuit):
		fail(w, 503, "此路由熔断中，请使用已获授权的备用路由。")
	case errors.Is(err, store.ErrInvalid):
		fail(w, 400, "网关字段或运行回执无效。")
	case errors.Is(err, store.ErrForbidden):
		fail(w, 403, "应用、身份、任务或模型/工具范围无效、已撤权或目录未获授权。")
	case errors.Is(err, store.ErrGatewayBudget):
		fail(w, 402, "应用或任务保守占额已达到硬额度；未决调用仍保留占额。")
	case errors.Is(err, store.ErrGatewayTaskSteps):
		fail(w, 429, "任务或祖先步骤额度已用尽；请核对当前任务范围。")
	case errors.Is(err, store.ErrGatewayRate):
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "应用请求或令牌速率已达到上限。")
	default:
		storeFailure(w, err)
	}
}
func gatewayControlToken(slug string) string {
	// Each node credential is bound to one workspace; no all-tenant default exists.
	if raw := os.Getenv("SCARLET_GATEWAY_CONTROL_TOKENS"); raw != "" {
		var tokens map[string]string
		if len(raw) > 1<<20 || json.Unmarshal([]byte(raw), &tokens) != nil || len(tokens) > 1000 {
			return ""
		}
		token := tokens[slug]
		for workspace, other := range tokens {
			if workspace != slug && other == token && token != "" {
				return ""
			}
		}
		if len(token) < 32 || len(token) > 512 {
			return ""
		}
		return token
	}
	if os.Getenv("SCARLET_GATEWAY_WORKSPACE") != slug {
		return ""
	}
	token := os.Getenv("SCARLET_GATEWAY_CONTROL_TOKEN")
	if len(token) < 32 || len(token) > 512 {
		return ""
	}
	return token
}
func gatewayMachine(w http.ResponseWriter, r *http.Request, slug string) bool {
	configured := gatewayControlToken(slug)
	if configured == "" {
		fail(w, 503, "此工作区未配置网关节点认证。")
		return false
	}
	received := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare([]byte(configured), []byte(received)) != 1 {
		fail(w, 401, "网关节点认证失败。")
		return false
	}
	return true
}
func (s *server) getGateway(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	out, e := s.store.GatewayOverview(r.Context(), ws.ID, u.ID)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	out.Runtime.Configured = gatewayControlToken(ws.Slug) != ""
	respond(w, 200, out)
}
func (s *server) saveGateway(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in struct {
		ExpectedRevision int64                      `json:"expectedRevision"`
		Enabled          bool                       `json:"enabled"`
		CatalogDigest    string                     `json:"catalogDigest"`
		Applications     []store.GatewayApplication `json:"applications"`
		Providers        []store.GatewayProvider    `json:"providers"`
	}
	if !readJSONLimit(w, r, &in, 1<<20) {
		return
	}
	out, e := s.store.SaveGatewayConfig(r.Context(), ws.ID, u.ID, in.ExpectedRevision, store.GatewayConfig{CatalogDigest: in.CatalogDigest, Enabled: in.Enabled, Applications: in.Applications, Providers: in.Providers})
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) exportGateway(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	out, e := s.store.GatewayOverview(r.Context(), ws.ID, u.ID)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="scarlet-gateway-config.json"`)
	if id := r.URL.Query().Get("providerId"); id != "" {
		root := ""
		routes := map[string]store.GatewayProvider{}
		for _, p := range out.Config.Providers {
			routes[p.ModelID] = p
			if p.ID == id {
				root = p.ModelID
			}
		}
		if root == "" {
			gatewayFailure(w, store.ErrInvalid)
			return
		}
		included := map[string]bool{}
		var include func(string)
		include = func(model string) {
			if included[model] {
				return
			}
			included[model] = true
			for _, fallback := range routes[model].FallbackModelIDs {
				include(fallback)
			}
		}
		include(root)
		providers := []store.GatewayProvider{}
		for _, p := range out.Config.Providers {
			if included[p.ModelID] {
				providers = append(providers, p)
			}
		}
		out.Config.Providers = providers
		apps := []store.GatewayApplication{}
		for _, app := range out.Config.Applications {
			hasRoot := false
			models := []string{}
			for _, model := range app.ModelIDs {
				if model == root {
					hasRoot = true
				}
				if included[model] {
					models = append(models, model)
				}
			}
			if hasRoot {
				app.ModelIDs = models
				apps = append(apps, app)
			}
		}
		out.Config.Applications = apps
	}

	respond(w, 200, out.Config)
}
func (s *server) createGatewayCredential(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in struct {
		ApplicationID string `json:"applicationId"`
		Name          string `json:"name"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	credential, token, e := s.store.CreateGatewayCredential(r.Context(), ws.ID, u.ID, in.ApplicationID, in.Name)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, 201, map[string]any{"credential": credential, "token": token})
}
func (s *server) revokeGatewayCredential(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, e := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if e != nil {
		gatewayFailure(w, store.ErrInvalid)
		return
	}
	if e = s.store.RevokeGatewayCredential(r.Context(), ws.ID, u.ID, id); e != nil {
		gatewayFailure(w, e)
		return
	}
	w.WriteHeader(204)
}
func gatewayPagination(r *http.Request) (after int64, limit int, err error) {
	limit = 100
	if v := r.URL.Query().Get("after"); v != "" {
		after, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			return
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
	}
	return
}
func (s *server) getGatewayAudit(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	after, limit, e := gatewayPagination(r)
	if e != nil {
		gatewayFailure(w, store.ErrInvalid)
		return
	}
	out, e := s.store.GatewayAuditRoute(r.Context(), ws.ID, u.ID, after, limit, r.URL.Query().Get("providerId"))
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/export") {
		w.Header().Set("Content-Disposition", `attachment; filename="scarlet-gateway-audit.json"`)
	}
	respond(w, 200, out)
}
func (s *server) getGatewayReservations(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var e error
		limit, e = strconv.Atoi(raw)
		if e != nil {
			gatewayFailure(w, store.ErrInvalid)
			return
		}
	}
	after := r.URL.Query().Get("after")
	items, e := s.store.GatewayReservations(r.Context(), ws.ID, u.ID, after, limit)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	next := after
	if len(items) > 0 {
		next, _ = items[len(items)-1]["id"].(string)
	}
	respond(w, 200, map[string]any{"items": items, "nextCursor": next})
}
func (s *server) resolveGateway(w http.ResponseWriter, r *http.Request) {
	var in store.GatewayResolveInput
	if !readJSONLimit(w, r, &in, 8192) || !gatewayMachine(w, r, in.WorkspaceSlug) {
		return
	}
	out, e := s.store.ResolveGateway(r.Context(), in)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) authorizeGateway(w http.ResponseWriter, r *http.Request) {
	var in store.GatewayAuthorizeInput
	if !readJSONLimit(w, r, &in, 16384) || !gatewayMachine(w, r, in.WorkspaceSlug) {
		return
	}
	out, e := s.store.AuthorizeGateway(r.Context(), in)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) settleGateway(w http.ResponseWriter, r *http.Request) {
	var in store.GatewaySettlement
	if !readJSONLimit(w, r, &in, 131072) || !gatewayMachine(w, r, in.WorkspaceSlug) {
		return
	}
	if e := s.store.SettleGateway(r.Context(), in); e != nil {
		gatewayFailure(w, e)
		return
	}
	respond(w, 200, map[string]bool{"accepted": true})
}
func (s *server) heartbeatGateway(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WorkspaceSlug string   `json:"workspaceSlug"`
		ID            string   `json:"id"`
		Status        string   `json:"status"`
		Failures      int      `json:"failures"`
		Capabilities  []string `json:"capabilities"`
		Version       string   `json:"version"`
	}
	if !readJSONLimit(w, r, &in, 8192) || !gatewayMachine(w, r, in.WorkspaceSlug) {
		return
	}
	if e := s.store.GatewayHeartbeat(r.Context(), in.WorkspaceSlug, store.GatewayNode{ID: in.ID, Status: in.Status, Failures: in.Failures, Capabilities: in.Capabilities, Version: in.Version}); e != nil {
		gatewayFailure(w, e)
		return
	}
	respond(w, 200, map[string]bool{"accepted": true})
}

func (s *server) exportGatewayMachineAudit(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("workspaceSlug")
	if !gatewayMachine(w, r, slug) {
		return
	}
	after, limit, e := gatewayPagination(r)
	if e != nil {
		gatewayFailure(w, store.ErrInvalid)
		return
	}
	page, e := s.store.GatewayAuditMachine(r.Context(), slug, after, limit)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	respond(w, 200, page)
}

func (s *server) recordGatewayDenied(w http.ResponseWriter, r *http.Request) {
	var in store.GatewayDeniedEvent
	if !readJSONLimit(w, r, &in, 65536) || !gatewayMachine(w, r, in.WorkspaceSlug) {
		return
	}
	out, e := s.store.RecordGatewayDenied(r.Context(), in)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	respond(w, 200, out)
}

func (s *server) getGatewayCache(w http.ResponseWriter, r *http.Request) {
	var in store.GatewayCacheScope
	if !readJSONLimit(w, r, &in, 16384) || !gatewayMachine(w, r, in.WorkspaceSlug) {
		return
	}
	out, e := s.store.GetGatewayCache(r.Context(), in)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, 200, out)
}
func (s *server) putGatewayCache(w http.ResponseWriter, r *http.Request) {
	var in store.GatewayCachePut
	if !readJSONLimit(w, r, &in, 2<<20) || !gatewayMachine(w, r, in.WorkspaceSlug) {
		return
	}
	if e := s.store.PutGatewayCache(r.Context(), in); e != nil {
		gatewayFailure(w, e)
		return
	}
	respond(w, 200, map[string]bool{"accepted": true})
}
func (s *server) invalidateGatewayCache(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in struct {
		ApplicationID string `json:"applicationId"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	n, e := s.store.InvalidateGatewayCache(r.Context(), ws.ID, u.ID, in.ApplicationID)
	if e != nil {
		gatewayFailure(w, e)
		return
	}
	respond(w, 200, map[string]int64{"invalidated": n})
}
