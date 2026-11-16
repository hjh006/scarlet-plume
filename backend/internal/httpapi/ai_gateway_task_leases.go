package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
	"net/http"
	"strconv"
	"strings"
)

func (s *server) gatewayTaskLeaseRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/ai-gateway/tasks/{task}"
	mux.HandleFunc("GET "+base+"/credential-lease", s.workspace(false, s.getGatewayTaskLease))
	mux.HandleFunc("GET "+base+"/credential-lease/history", s.workspace(false, s.getGatewayTaskLeaseHistory))
	mux.HandleFunc("GET "+base+"/credential-bindings", s.workspace(false, s.getGatewayTaskLeaseProposals))
	mux.HandleFunc("GET "+base+"/credential-bindings/{proposal}", s.workspace(false, s.getGatewayTaskLeaseProposal))
	mux.HandleFunc("GET "+base+"/credential-bindings/by-operation/{operation}", s.workspace(false, s.getGatewayTaskLeaseProposalByOperation))
	mux.HandleFunc("POST "+base+"/credential-bindings", s.workspace(false, s.proposeGatewayTaskLease))
	mux.HandleFunc("POST "+base+"/credential-bindings/{proposal}/review", s.workspace(false, s.reviewGatewayTaskLease))
	mux.HandleFunc("GET "+base+"/credential-renewal", s.preflightGatewayTaskLease)
	mux.HandleFunc("POST "+base+"/credential-renewal", s.renewGatewayTaskLease)
	mux.HandleFunc("POST "+base+"/credential-renewal/complete-step", s.completeRenewedGatewayTaskStep)
}
func gatewayTaskLeaseRenewalOriginExempt(r *http.Request) bool {
	p := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	return r.Method == http.MethodPost && (len(p) == 8 || len(p) == 9 && p[8] == "complete-step") && p[0] == "api" && p[1] == "v1" && p[2] == "workspaces" && p[3] != "" && p[4] == "ai-gateway" && p[5] == "tasks" && p[6] != "" && p[7] == "credential-renewal" && r.Header.Get("Cookie") == "" && len(r.Header.Values("Authorization")) == 1 && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sgw_")
}
func taskLeaseFailure(w http.ResponseWriter, e error) {
	var le *store.TaskLeaseError
	if errors.As(e, &le) {
		status := http.StatusConflict
		if errors.Is(e, store.ErrForbidden) {
			status = 403
		} else if errors.Is(e, store.ErrInvalid) {
			status = 400
		}
		respond(w, status, le)
		return
	}
	gatewayFailure(w, e)
}
func taskLeaseLimit(r *http.Request) (int, error) {
	if _, ok := r.URL.Query()["limit"]; !ok {
		return 50, nil
	}
	return gatewayTaskLimit(r)
}
func taskLeasePageQuery(w http.ResponseWriter, r *http.Request) (int, bool) {
	n, e := taskLeaseLimit(r)
	if e != nil || len(r.URL.Query()["before"]) > 1 {
		taskLeaseFailure(w, store.ErrInvalid)
		return 0, false
	}
	return n, true
}
func (s *server) getGatewayTaskLease(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	out, e := s.store.GatewayTaskLeaseView(r.Context(), ws.ID, u.ID, r.PathValue("task"))
	if e != nil {
		taskLeaseFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) getGatewayTaskLeaseHistory(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	limit, ok := taskLeasePageQuery(w, r)
	if !ok {
		return
	}
	out, e := s.store.GatewayTaskLeaseHistory(r.Context(), ws.ID, u.ID, r.PathValue("task"), r.URL.Query().Get("before"), limit)
	if e != nil {
		taskLeaseFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) getGatewayTaskLeaseProposals(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	limit, ok := taskLeasePageQuery(w, r)
	if !ok {
		return
	}
	out, e := s.store.GatewayTaskLeaseProposals(r.Context(), ws.ID, u.ID, r.PathValue("task"), r.URL.Query().Get("before"), limit)
	if e != nil {
		taskLeaseFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) getGatewayTaskLeaseProposal(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, e := strconv.ParseInt(r.PathValue("proposal"), 10, 64)
	if e != nil || id < 1 {
		taskLeaseFailure(w, store.ErrInvalid)
		return
	}
	out, e := s.store.GatewayTaskLeaseProposal(r.Context(), ws.ID, u.ID, r.PathValue("task"), id, "")
	if e != nil {
		taskLeaseFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) getGatewayTaskLeaseProposalByOperation(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	out, e := s.store.GatewayTaskLeaseProposal(r.Context(), ws.ID, u.ID, r.PathValue("task"), 0, r.PathValue("operation"))
	if e != nil {
		taskLeaseFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) proposeGatewayTaskLease(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in store.TaskLeaseProposalInput
	if !readJSONLimit(w, r, &in, 16384) {
		return
	}
	out, e := s.store.ProposeGatewayTaskCredentialBinding(r.Context(), ws.ID, u.ID, r.PathValue("task"), in)
	if e != nil {
		taskLeaseFailure(w, e)
		return
	}
	respond(w, 201, out)
}
func (s *server) reviewGatewayTaskLease(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, e := strconv.ParseInt(r.PathValue("proposal"), 10, 64)
	if e != nil || id < 1 {
		taskLeaseFailure(w, store.ErrInvalid)
		return
	}
	var in store.TaskLeaseReviewInput
	if !readJSONLimit(w, r, &in, 4096) {
		return
	}
	out, e := s.store.ReviewGatewayTaskCredentialBinding(r.Context(), ws.ID, u.ID, r.PathValue("task"), id, in)
	if e != nil {
		taskLeaseFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func taskLeaseBearer(w http.ResponseWriter, r *http.Request) (string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("Cookie") != "" || len(r.Header.Values("Authorization")) != 1 {
		fail(w, 401, "请使用独立的新有效工作负载短期凭据。")
		return "", false
	}
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || scheme != "Bearer" || !strings.HasPrefix(token, "sgw_") || len(token) != 68 || strings.ContainsAny(token, " \r\n\t") {
		fail(w, 401, "工作负载短期凭据无效。")
		return "", false
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:]), true
}
func (s *server) preflightGatewayTaskLease(w http.ResponseWriter, r *http.Request) {
	hash, ok := taskLeaseBearer(w, r)
	if !ok {
		return
	}
	out, e := s.store.GatewayTaskLeasePreflight(r.Context(), r.PathValue("slug"), hash, r.PathValue("task"))
	if e != nil {
		taskLeaseFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) renewGatewayTaskLease(w http.ResponseWriter, r *http.Request) {
	hash, ok := taskLeaseBearer(w, r)
	if !ok {
		return
	}
	var in store.TaskLeaseRenewalInput
	if !readJSONLimit(w, r, &in, 16384) {
		return
	}
	out, e := s.store.RenewGatewayTaskCredential(r.Context(), r.PathValue("slug"), hash, r.PathValue("task"), in)
	if e != nil {
		taskLeaseFailure(w, e)
		return
	}
	respond(w, 200, out)
}

func (s *server) completeRenewedGatewayTaskStep(w http.ResponseWriter, r *http.Request) {
	hash, ok := taskLeaseBearer(w, r)
	if !ok {
		return
	}
	var in struct {
		gatewaygovernance.TaskContext
		Kind        string                         `json:"kind"`
		ModelID     string                         `json:"modelId"`
		ToolID      string                         `json:"toolId"`
		Action      string                         `json:"action"`
		Target      string                         `json:"target"`
		DocumentIDs []string                       `json:"documentIds"`
		Categories  []string                       `json:"categories"`
		Sources     []gatewaygovernance.TaskSource `json:"sources"`
		Audit       json.RawMessage                `json:"audit"`
	}
	if !readJSONLimit(w, r, &in, 131072) {
		return
	}
	fact := gatewaygovernance.TaskCompleteInput{TaskCheckInput: gatewaygovernance.TaskCheckInput{TaskContext: in.TaskContext, Kind: in.Kind, ModelID: in.ModelID, ToolID: in.ToolID, Action: in.Action, Target: in.Target, DocumentIDs: in.DocumentIDs, Categories: in.Categories}, Sources: in.Sources, Audit: in.Audit}
	out, e := s.store.CompleteRenewedGatewayTaskStep(r.Context(), r.PathValue("slug"), hash, r.PathValue("task"), fact)
	if e != nil {
		taskLeaseFailure(w, e)
		return
	}
	respond(w, 200, out)
}
