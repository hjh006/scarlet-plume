package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

const securityEventsPath = "/api/v1/operations/security-events"
const runtimeControlsPath = "/api/v1/runtime/controls"
const runtimeControlReceiptsPath = "/api/v1/runtime/controls/receipts"
const postgresControlClaimPath = "/api/v1/operations/control/claim"
const postgresControlReceiptsPath = "/api/v1/operations/control/receipts"
const operationsBodyLimit = 16 << 10

func (s *server) securityOperationsRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/operations"
	mux.HandleFunc("GET "+base+"/overview", s.workspace(false, s.operationsOverview))
	mux.HandleFunc("GET "+base+"/sources", s.workspace(false, s.operationsSources))
	mux.HandleFunc("POST "+base+"/sources", s.workspace(true, s.createOperationsSource))
	mux.HandleFunc("DELETE "+base+"/sources/{source}", s.workspace(true, s.revokeOperationsSource))
	mux.HandleFunc("GET "+base+"/events", s.workspace(false, s.operationsEvents))
	mux.HandleFunc("GET "+base+"/controls", s.workspace(false, s.controlActions))
	mux.HandleFunc("POST "+base+"/controls", s.workspace(true, s.createControlAction))
	mux.HandleFunc("GET "+base+"/controls/{control}", s.workspace(false, s.controlAction))
	// A reviewer can approve a proposal but cannot edit its target.
	mux.HandleFunc("POST "+base+"/controls/{control}/approve", s.workspace(false, s.approveControlAction))
	mux.HandleFunc("POST "+base+"/controls/{control}/rollback", s.workspace(true, s.rollbackControlAction))
	mux.HandleFunc("POST "+securityEventsPath, s.operationsExternal("security_events", s.receiveSecurityEvent))
	mux.HandleFunc("GET "+runtimeControlsPath, s.operationsExternal("gateway", s.runtimeControlCommands))
	mux.HandleFunc("POST "+runtimeControlReceiptsPath, s.operationsExternal("gateway", s.runtimeControlReceipt))
	mux.HandleFunc("POST "+postgresControlClaimPath, s.operationsExternal("postgres_control", s.claimPostgresControl))
	mux.HandleFunc("POST "+postgresControlReceiptsPath, s.operationsExternal("postgres_control", s.postgresControlReceipt))
}

func operationsFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrAutomationUnauthorized):
		w.Header().Set("WWW-Authenticate", "Bearer")
		fail(w, 401, "集成来源令牌无效、用途不符或已撤销。")
	case errors.Is(err, store.ErrForbidden):
		fail(w, 403, "当前身份、来源或独立审批权限不允许执行此操作。")
	case errors.Is(err, store.ErrInvalid):
		fail(w, 400, "请核对来源、限定目标、版本、时间和回执枚举。")
	case errors.Is(err, store.ErrConflict):
		fail(w, 409, "记录版本、执行阶段或已绑定的授权范围发生变化，请刷新后核对。")
	case errors.Is(err, store.ErrEvidenceRequired):
		fail(w, 422, "缺少当前范围的有效观测、整改记录或新鲜来源复测。")
	case errors.Is(err, store.ErrRuntimeLimit):
		fail(w, 422, "当前处置或来源范围已达到支持上限，请缩小范围。")
	default:
		storeFailure(w, err)
	}
}

func operationPathID(w http.ResponseWriter, r *http.Request, key string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(key), 10, 64)
	if err != nil || id < 1 {
		fail(w, 404, "找不到此安全运营记录。")
		return 0, false
	}
	return id, true
}

func (s *server) operationsOverview(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	result, err := s.store.OperationsOverview(r.Context(), ws.ID, user.ID)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, result)
}
func (s *server) operationsSources(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	items, err := s.store.ListOperationsSources(r.Context(), ws.ID, user.ID)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items})
}
func (s *server) createOperationsSource(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	if ws.Role != "owner" {
		operationsFailure(w, store.ErrForbidden)
		return
	}
	var input store.OperationsSourceInput
	if !readJSONLimit(w, r, &input, operationsBodyLimit) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.OwnerTeam = strings.TrimSpace(input.OwnerTeam)
	random, err := sessionToken()
	if err != nil {
		storeFailure(w, err)
		return
	}
	token := runtimeTokenPrefix + random
	source, err := s.store.CreateOperationsSource(r.Context(), ws.ID, user.ID, input, sessionHash(token))
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 201, map[string]any{"source": source, "token": token})
}
func (s *server) revokeOperationsSource(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "source")
	if !ok {
		return
	}
	if err := s.store.RevokeOperationsSource(r.Context(), ws.ID, user.ID, id); err != nil {
		operationsFailure(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *server) operationsEvents(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	items, err := s.store.ListOperationsSecurityEvents(r.Context(), ws.ID, user.ID)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items})
}
func (s *server) controlActions(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	items, err := s.store.ListControlActions(r.Context(), ws.ID, user.ID)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items})
}
func (s *server) createControlAction(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	var input store.ControlActionInput
	if !readJSONLimit(w, r, &input, operationsBodyLimit) {
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	action, err := s.store.CreateControlAction(r.Context(), ws.ID, user.ID, input)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 201, action)
}
func (s *server) controlAction(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "control")
	if !ok {
		return
	}
	item, err := s.store.ControlAction(r.Context(), ws.ID, user.ID, id)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, item)
}
func (s *server) approveControlAction(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "control")
	if !ok {
		return
	}
	var input struct {
		Revision int64 `json:"revision"`
	}
	if !readJSONLimit(w, r, &input, operationsBodyLimit) {
		return
	}
	if input.Revision < 1 {
		operationsFailure(w, store.ErrInvalid)
		return
	}
	item, err := s.store.ApproveControlAction(r.Context(), ws.ID, user.ID, id, input.Revision)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, item)
}
func (s *server) rollbackControlAction(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "control")
	if !ok {
		return
	}
	var input struct {
		Revision int64  `json:"revision"`
		Reason   string `json:"reason"`
	}
	if !readJSONLimit(w, r, &input, operationsBodyLimit) {
		return
	}
	if input.Revision < 1 {
		operationsFailure(w, store.ErrInvalid)
		return
	}
	item, err := s.store.RollbackControlAction(r.Context(), ws.ID, user.ID, id, input.Revision, strings.TrimSpace(input.Reason))
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, item)
}

func (s *server) operationsExternal(kind string, action func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		digest := runtimeDigest(r.Header.Get("Authorization"))
		if digest == "" {
			operationsFailure(w, store.ErrAutomationUnauthorized)
			return
		}
		select {
		case s.runtimeSlots <- struct{}{}:
			defer func() { <-s.runtimeSlots }()
		default:
			w.Header().Set("Retry-After", "2")
			fail(w, 429, "接收繁忙，请稍后重试。")
			return
		}
		var err error
		if kind == "gateway" {
			err = s.store.AuthorizeRuntimeSource(r.Context(), kind, digest)
		} else {
			err = s.store.AuthorizeOperationsSource(r.Context(), kind, digest)
		}
		if err != nil {
			operationsFailure(w, err)
			return
		}
		action(w, r, digest)
	}
}
func (s *server) receiveSecurityEvent(w http.ResponseWriter, r *http.Request, digest string) {
	var input store.SecurityEventInput
	if !readJSONLimit(w, r, &input, operationsBodyLimit) {
		return
	}
	event, replayed, err := s.store.ReceiveSecurityEvent(r.Context(), digest, input)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"event": event, "replayed": replayed})
}
func (s *server) runtimeControlCommands(w http.ResponseWriter, r *http.Request, digest string) {
	items, err := s.store.RuntimeControlCommands(r.Context(), digest)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items})
}
func (s *server) runtimeControlReceipt(w http.ResponseWriter, r *http.Request, digest string) {
	s.acceptControlReceipt(w, r, "gateway", digest)
}
func (s *server) postgresControlReceipt(w http.ResponseWriter, r *http.Request, digest string) {
	s.acceptControlReceipt(w, r, "postgres_control", digest)
}
func (s *server) acceptControlReceipt(w http.ResponseWriter, r *http.Request, kind, digest string) {
	var input store.ControlReceipt
	if !readJSONLimit(w, r, &input, operationsBodyLimit) {
		return
	}
	if err := s.store.RecordControlReceipt(r.Context(), kind, digest, input); err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, map[string]bool{"accepted": true})
}
func (s *server) claimPostgresControl(w http.ResponseWriter, r *http.Request, digest string) {
	var input struct{}
	if !readJSONLimit(w, r, &input, operationsBodyLimit) {
		return
	}
	random, err := sessionToken()
	if err != nil {
		storeFailure(w, err)
		return
	}
	// The store accepts an opaque operations key whose first character is
	// alphanumeric; raw URL-base64 randomness may start with '-' or '_'.
	command, err := s.store.ClaimPostgresControl(r.Context(), digest, "lease_"+random)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"command": command})
}
