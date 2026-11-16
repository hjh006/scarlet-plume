package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) gatewayWorkloadRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/ai-gateway"
	mux.HandleFunc("GET "+base+"/workloads", s.workspace(false, s.getGatewayWorkloads))
	mux.HandleFunc("POST "+base+"/workloads", s.workspace(true, s.createGatewayWorkload))
	mux.HandleFunc("PUT "+base+"/workloads/{binding}", s.workspace(true, s.updateGatewayWorkload))
	mux.HandleFunc("POST "+base+"/workloads/{binding}/revoke", s.workspace(true, s.revokeGatewayWorkload))
	mux.HandleFunc("POST "+base+"/workload-token", s.exchangeGatewayWorkloadToken)
}

// This is the only browser-Origin exemption required by workload exchange. No
// cookie sessions, other methods or neighboring management paths are included.
func gatewayWorkloadTokenOriginExempt(r *http.Request) bool {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	return r.Method == http.MethodPost && len(parts) == 6 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "workspaces" && parts[3] != "" && parts[4] == "ai-gateway" && parts[5] == "workload-token" && r.Header.Get("Cookie") == "" && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func (s *server) getGatewayWorkloads(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	limit, err := gatewayTaskLimit(r)
	if err != nil || len(r.URL.Query()["before"]) > 1 {
		gatewayFailure(w, store.ErrInvalid)
		return
	}
	out, err := s.store.GatewayWorkloads(r.Context(), ws.ID, u.ID, r.URL.Query().Get("before"), limit)
	if err != nil {
		gatewayFailure(w, err)
		return
	}
	respond(w, 200, out)
}

func (s *server) createGatewayWorkload(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in store.GatewayWorkloadInput
	if !readJSONLimit(w, r, &in, 24576) {
		return
	}
	out, err := s.store.SaveGatewayWorkload(r.Context(), ws.ID, u.ID, 0, 0, in, true)
	if err != nil {
		gatewayFailure(w, err)
		return
	}
	respond(w, 201, out)
}

func (s *server) updateGatewayWorkload(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in struct {
		store.GatewayWorkloadInput
		ExpectedRevision int64 `json:"expectedRevision"`
		Enabled          *bool `json:"enabled"`
	}
	if !readJSONLimit(w, r, &in, 24576) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("binding"), 10, 64)
	if err != nil || id < 1 || in.Enabled == nil {
		gatewayFailure(w, store.ErrInvalid)
		return
	}
	out, err := s.store.SaveGatewayWorkload(r.Context(), ws.ID, u.ID, id, in.ExpectedRevision, in.GatewayWorkloadInput, *in.Enabled)
	if err != nil {
		gatewayFailure(w, err)
		return
	}
	respond(w, 200, out)
}

func (s *server) revokeGatewayWorkload(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in struct {
		ExpectedRevision int64 `json:"expectedRevision"`
	}
	if !readJSONLimit(w, r, &in, 1024) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("binding"), 10, 64)
	if err != nil || id < 1 {
		gatewayFailure(w, store.ErrInvalid)
		return
	}
	out, err := s.store.RevokeGatewayWorkload(r.Context(), ws.ID, u.ID, id, in.ExpectedRevision)
	if err != nil {
		gatewayFailure(w, err)
		return
	}
	respond(w, 200, out)
}

func (s *server) exchangeGatewayWorkloadToken(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Cookie") != "" || len(r.Header.Values("Authorization")) != 1 {
		fail(w, 401, "请使用独立签名的工作负载JWT断言。")
		return
	}
	scheme, assertion, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || scheme != "Bearer" || len(assertion) == 0 || len(assertion) > 12288 || strings.ContainsAny(assertion, " \r\n\t") {
		w.Header().Set("WWW-Authenticate", "Bearer")
		fail(w, 401, "工作负载JWT断言无效。")
		return
	}
	select {
	case s.runtimeSlots <- struct{}{}:
		defer func() { <-s.runtimeSlots }()
	default:
		fail(w, 429, "工作负载认证繁忙，请稍后重试。")
		return
	}
	var in struct {
		BindingID int64 `json:"bindingId"`
	}
	if !readJSONLimit(w, r, &in, 1024) {
		return
	}
	out, err := s.store.ExchangeGatewayWorkloadToken(r.Context(), r.PathValue("slug"), in.BindingID, assertion)
	if err != nil {
		if errors.Is(err, store.ErrWorkloadAssertion) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			fail(w, 401, "签名、声明或工作负载绑定无效、到期或已撤销。")
			return
		}
		gatewayFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, 200, out)
}
