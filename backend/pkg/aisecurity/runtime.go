package aisecurity

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

type Identity struct {
	ApplicationID string `json:"applicationId"`
	PrincipalID   string `json:"principalId"`
}

// Credential holds a SHA256 token hash, not a caller-provided principal claim.
type Credential struct {
	TokenHash string `json:"tokenHash"`
	Identity
}
type Target struct {
	URL           string `json:"url"`
	Authorization string `json:"-"`
}
type LocalDocument struct {
	ID                     string     `json:"id"`
	Version                string     `json:"version"`
	ACLVersion             string     `json:"aclVersion,omitempty"`
	Content                string     `json:"content"`
	AuthorizationExpiresAt *time.Time `json:"authorizationExpiresAt,omitempty"`
}
type RuntimeOptions struct {
	Authenticate     func(context.Context, string) (Identity, error)
	Catalog          func(context.Context) (Config, error)
	Credentials      []Credential
	Targets          map[string]Target
	Documents        func(context.Context) (map[string]LocalDocument, error)
	ResolveDocuments func(context.Context, Identity, []string) (map[string]LocalDocument, error)
	TaskControl      func(context.Context, TaskOperation) (gatewaygovernance.TaskGrant, error)
	Audit            func(context.Context, AuditRecord) error
	Journal          *ReplayJournal
	AllowLoopback    bool
	Concurrency      int
	Now              func() time.Time
	MediaProcessor   MediaProcessor
	Cache            ModelCache
}
type runtimeHandler struct {
	options    RuntimeOptions
	client     *http.Client
	slots      chan struct{}
	identities map[string]Identity
}

// RuntimeRequest intentionally omits identity, ACLs, categories, output and upstream addresses.
// Such unknown fields are rejected rather than overwritten or trusted.
type RuntimeRequest struct {
	TaskID      string            `json:"taskId,omitempty"`
	RequestID   string            `json:"requestId"`
	Kind        string            `json:"kind"`
	DocumentIDs []string          `json:"documentIds,omitempty"`
	ModelID     string            `json:"modelId,omitempty"`
	ToolID      string            `json:"toolId,omitempty"`
	Action      string            `json:"action,omitempty"`
	Target      string            `json:"target,omitempty"`
	Arguments   map[string]string `json:"arguments,omitempty"`
	Input       string            `json:"input,omitempty"`
	ApprovalID  string            `json:"approvalId,omitempty"`
	IssuedAt    time.Time         `json:"issuedAt"`
	ExpiresAt   time.Time         `json:"expiresAt"`
}

func NewRuntime(o RuntimeOptions) (http.Handler, error) {
	if o.Catalog == nil || o.Audit == nil || o.Journal == nil || len(o.Credentials) == 0 && o.Authenticate == nil {
		return nil, errors.New("catalog, audit, durable journal and credentials required")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Concurrency == 0 {
		o.Concurrency = 8
	}
	if o.Concurrency < 1 || o.Concurrency > 32 {
		return nil, errors.New("invalid concurrency")
	}
	initial, err := o.Catalog(context.Background())
	if err != nil || ValidateConfig(initial) != nil {
		return nil, ErrConfig
	}
	identities := map[string]Identity{}
	for _, v := range o.Credentials {
		if !digest.MatchString(v.TokenHash) || !validID(v.ApplicationID) || !validID(v.PrincipalID) {
			return nil, errors.New("invalid credential binding")
		}
		if _, ok := identities[v.TokenHash]; ok {
			return nil, errors.New("duplicate credential")
		}
		identities[v.TokenHash] = v.Identity
	}
	targets := map[string]Target{}
	for ref, v := range o.Targets {
		if !validID(ref) || !validTarget(v.URL, o.AllowLoopback) || strings.ContainsAny(v.Authorization, "\r\n") {
			return nil, errors.New("invalid static target")
		}
		targets[ref] = v
	}
	o.Targets = targets
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, MaxIdleConns: 16, MaxConnsPerHost: o.Concurrency, ResponseHeaderTimeout: 5 * time.Second, DisableCompression: true, MaxResponseHeaderBytes: 65536, TLSHandshakeTimeout: 5 * time.Second}
	return &runtimeHandler{options: o, identities: identities, slots: make(chan struct{}, o.Concurrency), client: &http.Client{Timeout: 60 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func validTarget(raw string, local bool) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return local && u.Scheme == "http" && ip != nil && ip.IsLoopback()
}
func strictJSON(r io.Reader, dst any) error {
	const wireLimit = 16 << 20
	raw, err := io.ReadAll(io.LimitReader(r, wireLimit+1))
	if err != nil || len(raw) > wireLimit || !utf8.Valid(raw) {
		return errors.New("JSON size or encoding invalid")
	}
	tokens := json.NewDecoder(bytes.NewReader(raw))
	tokens.UseNumber()
	nodes := 0
	if _, err := definitionValue(tokens, 0, &nodes, false); err != nil {
		return err
	}
	if _, err := tokens.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(dst); e != nil {
		return e
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type runtimeAttemptKey struct{}

func (h *runtimeHandler) audit(ctx context.Context, r Request, d Decision, stage string, x Execution) error {
	// An already observed terminal fact must survive cancellation arriving while
	// its receipt is in flight. This context only records/settles the original
	// execution; the caller context still controls dispatch and output release.
	if stage == "completed" || ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
	}
	var id [16]byte
	if _, e := rand.Read(id[:]); e != nil {
		return e
	}
	requestID := r.RequestID
	if !validID(requestID) || len(requestID) < 16 {
		requestID = "invalid-" + hash(r.RequestID)
	}
	mcpVersion := ""
	for _, scope := range d.Coverage {
		if scope == "mcp_http_2026_07_28" {
			mcpVersion = MCPVersion
		}
	}
	attempt, _ := ctx.Value(runtimeAttemptKey{}).(string)
	evidence, _ := ctx.Value(cacheEvidenceKey{}).(*CacheEvidence)
	record := AuditRecord{CacheEvidence: evidence, AttemptID: attempt, RuntimeProtocolVersion: RuntimeProtocolVersion, InspectionVersion: InspectionVersion, MCPProtocolVersion: mcpVersion, EventID: hex.EncodeToString(id[:]), RequestID: requestID, Stage: stage, ObservedAt: h.options.Now().UTC(), Decision: d, Execution: x}
	if err := h.options.Audit(ctx, record); err != nil {
		return err
	}
	if r.TaskID != "" && stage == "completed" && d.Outcome == "allow" && (x.Outcome == "succeeded" || x.CacheHit) {
		if _, _, err := h.task(ctx, r, d, "complete", &record); err != nil {
			return err
		}
	}
	return nil
}
func (h *runtimeHandler) ServeHTTP(w http.ResponseWriter, in *http.Request) {
	if in.Method != "POST" || in.URL.Path != "/v1/execute" {
		respond(w, 404, map[string]string{"code": "not_found"})
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		respond(w, 429, map[string]string{"code": "runtime_busy"})
		return
	}
	auth := in.Header.Get("Authorization")
	if len(in.Header.Values("Authorization")) != 1 || !strings.HasPrefix(auth, "Bearer ") || len(auth) < 39 || len(auth) > 520 {
		respond(w, 401, map[string]string{"code": "authentication_required"})
		return
	}
	sum := sha256.Sum256([]byte(strings.TrimPrefix(auth, "Bearer ")))
	identity, authErr := h.authenticate(in.Context(), hex.EncodeToString(sum[:]))
	if authErr != nil {
		respond(w, 401, map[string]string{"code": "authentication_required"})
		return
	}
	in = in.WithContext(context.WithValue(in.Context(), gatewayCredentialHashKey{}, hex.EncodeToString(sum[:])))
	if len(in.Header.Values("Content-Type")) != 1 || in.Header.Get("Content-Type") != "application/json" || len(in.Header.Values("Content-Encoding")) > 1 || in.Header.Get("Content-Encoding") != "" {
		respond(w, 415, map[string]string{"code": "json_uncompressed_required"})
		return
	}
	var wire RuntimeRequest
	if strictJSON(http.MaxBytesReader(w, in.Body, 16<<20), &wire) != nil {
		respond(w, 400, map[string]string{"code": "request_json_invalid"})
		return
	}
	var attempt [16]byte
	if _, e := rand.Read(attempt[:]); e != nil {
		respond(w, 503, map[string]string{"code": "attempt_reference_unavailable"})
		return
	}
	in = in.WithContext(context.WithValue(in.Context(), runtimeAttemptKey{}, hex.EncodeToString(attempt[:])))
	r := Request{TaskID: wire.TaskID, RequestID: wire.RequestID, Kind: wire.Kind, ApplicationID: identity.ApplicationID, PrincipalID: identity.PrincipalID, DocumentIDs: wire.DocumentIDs, ModelID: wire.ModelID, ToolID: wire.ToolID, Action: wire.Action, Target: wire.Target, Arguments: wire.Arguments, Input: wire.Input, ApprovalID: wire.ApprovalID, IssuedAt: wire.IssuedAt, ExpiresAt: wire.ExpiresAt}
	cfg, e := h.options.Catalog(in.Context())
	if e != nil {
		respond(w, 503, map[string]string{"code": "catalog_unavailable"})
		return
	}
	var taskErr error
	r, _, taskErr = h.task(in.Context(), r, Decision{CatalogDigest: ConfigDigest(cfg)}, "check", nil)
	if taskErr == nil {
		var stopTaskWatch func()
		in, stopTaskWatch = h.watchTask(in, r, Decision{CatalogDigest: ConfigDigest(cfg)})
		defer stopTaskWatch()
	}
	var mediaErr error
	canInspect := taskErr == nil
	for _, app := range cfg.Applications {
		if app.ID == r.ApplicationID && app.RequireTask && r.TaskID == "" {
			canInspect = false
		}
	}
	if r.Kind == "model" && canInspect {
		for _, model := range cfg.Models {
			if model.ID == r.ModelID {
				prepared, err := PrepareMediaRequest(in.Context(), r, model, h.options.MediaProcessor)
				if err != nil {
					mediaErr = err
				} else {
					r = prepared
				}
			}
		}
	}
	d := Evaluate(cfg, r, h.options.Now())
	decisionStatus := 403
	if taskErr != nil {
		d.Outcome, d.Code = "deny", "task_scope_unavailable_or_denied"
		d.Rules = append(d.Rules, d.Code)
	}
	if mediaErr != nil && d.Outcome == "allow" {
		d.Outcome = "deny"
		d.Code = "media_inspection_required_or_invalid"
		d.Rules = append(d.Rules, d.Code)
	}
	if d.Outcome == "allow" {
		prepared, err := h.prepareSources(in.Context(), r, cfg)
		if err != nil {
			d.Outcome, d.Code = "deny", err.Error()
			decisionStatus = sourceFailureStatus(d.Code)
			d.Rules = append(d.Rules, d.Code)
		} else {
			r = prepared
			d = Evaluate(cfg, r, h.options.Now())
		}
	}
	if h.audit(in.Context(), r, d, "decision", Execution{Outcome: "not_attempted"}) != nil {
		respond(w, 503, map[string]string{"code": "audit_unavailable"})
		return
	}
	if d.Outcome != "allow" {
		respond(w, decisionStatus, d)
		return
	}
	if e = h.options.Journal.Reserve(r.ApplicationID, r.PrincipalID, r.RequestID, d.RequestDigest); e != nil {
		code, status := "journal_unavailable", 503
		if errors.Is(e, ErrReplay) {
			code, status = "request_replayed", 409
		}
		respond(w, status, map[string]string{"code": code})
		return
	}
	if h.audit(in.Context(), r, d, "prepared", Execution{Outcome: "not_attempted"}) != nil {
		respond(w, 503, map[string]string{"code": "audit_unavailable_nonce_consumed"})
		return
	}
	if !h.fresh(in.Context(), r, d) {
		respond(w, 403, map[string]string{"code": "catalog_or_authorization_changed"})
		return
	}
	if r.TaskID != "" {
		updated, _, err := h.task(in.Context(), r, d, "admit", nil)
		if err != nil {
			h.finishError(w, in, r, d, "task_step_admission_denied", 403, Execution{Outcome: "not_attempted"})
			return
		}
		r = updated
		if !h.fresh(in.Context(), r, d) {
			h.finishError(w, in, r, d, "task_scope_changed", 403, Execution{Outcome: "not_attempted"})
			return
		}
	}
	if r.Kind != "model" && h.authorize(in.Context(), r, d) != nil {
		h.finishError(w, in, r, d, "gateway_governance_denied", 403, Execution{Outcome: "not_attempted"})
		return
	}
	if r.Kind == "rag" {
		h.rag(w, in, r, cfg, d)
		return
	}
	h.forward(w, in, r, cfg, d)
}
func (h *runtimeHandler) fresh(ctx context.Context, r Request, d Decision) bool {
	if h.options.Authenticate != nil {
		hash, _ := ctx.Value(gatewayCredentialHashKey{}).(string)
		identity, err := h.authenticate(ctx, hash)
		if hash == "" || err != nil || identity.ApplicationID != r.ApplicationID || identity.PrincipalID != r.PrincipalID {
			return false
		}
	}
	cfg, e := h.options.Catalog(ctx)
	if e != nil || ConfigDigest(cfg) != d.CatalogDigest {
		return false
	}
	updated, _, err := h.task(ctx, r, d, "check", nil)
	return err == nil && h.sourcesCurrent(ctx, updated, cfg) && Evaluate(cfg, updated, h.options.Now()).Outcome == "allow"
}
func (h *runtimeHandler) rag(w http.ResponseWriter, in *http.Request, r Request, cfg Config, d Decision) {
	if h.options.Documents == nil && h.options.ResolveDocuments == nil {
		h.finishError(w, in, r, d, "document_source_unavailable", 503, Execution{Outcome: "not_attempted"})
		return
	}
	local := r.sourceDocuments
	var e error
	if local == nil {
		local, e = h.documents(in.Context(), r, d.AllowedDocumentIDs)
	}
	if e != nil {
		h.finishError(w, in, r, d, "document_source_unavailable", 503, Execution{Outcome: "not_attempted"})
		return
	}
	type outputDoc struct {
		ID         string `json:"id"`
		Version    string `json:"version"`
		ACLVersion string `json:"aclVersion"`
		Content    string `json:"content"`
	}
	docs := []outputDoc{}
	totalBytes := 0
	out := d
	for _, id := range d.AllowedDocumentIDs {
		var catalog Document
		for _, v := range cfg.Documents {
			if v.ID == id {
				catalog = v
			}
		}
		v, ok := local[id]
		if !ok || v.ID != id || v.Version != catalog.Version {
			h.finishError(w, in, r, d, "document_version_or_source_missing", 409, Execution{Outcome: "not_attempted"})
			return
		}
		if totalBytes+len(v.Content) > cfg.Policy.MaxOutputBytes {
			h.finishError(w, in, r, d, "document_output_budget", 422, Execution{Outcome: "not_attempted"})
			return
		}
		if catalog.IntegrityDigest != "" {
			sum := sha256.Sum256([]byte(v.Content))
			if hex.EncodeToString(sum[:]) != catalog.IntegrityDigest {
				h.finishError(w, in, r, d, "document_integrity_mismatch", 409, Execution{Outcome: "not_attempted"})
				return
			}
			out.Coverage = append(out.Coverage, "local_document_sha256_verified")
		} else {
			out.Coverage = append(out.Coverage, "local_document_version_only_unpinned_content")
		}
		totalBytes += len(v.Content)
		checked := r
		checked.Output = v.Content
		evaluated := Evaluate(cfg, checked, h.options.Now())
		evaluated.RequestDigest = d.RequestDigest
		if evaluated.Outcome != "allow" {
			h.finishError(w, in, r, evaluated, "document_content_denied", 403, Execution{Attempted: true, Outcome: "blocked_output"})
			return
		}
		out.Categories = unique(append(out.Categories, evaluated.Categories...))
		docs = append(docs, outputDoc{id, v.Version, catalog.ACLVersion, v.Content})
	}
	if !h.fresh(in.Context(), r, d) {
		h.finishError(w, in, r, d, "catalog_or_authorization_changed", 403, Execution{Attempted: true, Outcome: "blocked_output"})
		return
	}
	if h.audit(in.Context(), r, out, "completed", Execution{Attempted: true, Outcome: "succeeded"}) != nil {
		respond(w, 503, map[string]string{"code": "audit_unavailable_output_suppressed"})
		return
	}
	if !h.fresh(in.Context(), r, d) {
		respond(w, 403, map[string]string{"code": "task_or_source_changed_before_release"})
		return
	}
	captureGatewayCheckedOutput(w, in.Context(), r, out)
	respond(w, 200, struct {
		Decision  Decision    `json:"decision"`
		Documents []outputDoc `json:"documents"`
	}{out, docs})
}
func (h *runtimeHandler) finishError(w http.ResponseWriter, in *http.Request, r Request, d Decision, code string, status int, x Execution) {
	d.Outcome = "deny"
	d.Code = code
	d.Rules = append(d.Rules, code)
	if h.audit(in.Context(), r, d, "completed", x) != nil {
		respond(w, 503, map[string]string{"code": "audit_unavailable_execution_unknown"})
		return
	}
	respond(w, status, struct {
		Decision  Decision  `json:"decision"`
		Execution Execution `json:"execution"`
	}{d, x})
}
func (h *runtimeHandler) forward(w http.ResponseWriter, in *http.Request, r Request, cfg Config, d Decision) {
	ref := ""
	if r.Kind == "model" {
		for _, m := range cfg.Models {
			if m.ID == r.ModelID {
				ref = m.EndpointRef
			}
		}
	} else {
		for _, t := range cfg.Tools {
			if t.ID == r.ToolID {
				ref = t.EndpointRef
			}
		}
	}
	target, ok := h.options.Targets[ref]
	if !ok {
		h.finishError(w, in, r, d, "endpoint_binding_missing", 503, Execution{Outcome: "not_attempted"})
		return
	}
	var selectedTool Tool
	if r.Kind == "tool" {
		for _, t := range cfg.Tools {
			if t.ID == r.ToolID {
				selectedTool = t
			}
		}
	}
	if selectedTool.Protocol == "mcp_http" && len(selectedTool.DefinitionDigests) != 0 {
		if h.checkToolDefinition(in.Context(), r, selectedTool, target, cfg.Policy.MaxOutputBytes) != nil {
			h.finishError(w, in, r, d, "mcp_tool_definition_unverified_or_changed", 409, Execution{Outcome: "not_attempted"})
			return
		}
		d.Coverage = append(d.Coverage, "mcp_tool_definition_sha256_verified")
	}
	// Metadata checks can block on the provider. Recheck leases and revocations
	// after them, immediately before the actual model/tool operation.
	if !h.fresh(in.Context(), r, d) {
		h.finishError(w, in, r, d, "catalog_or_authorization_changed", 403, Execution{Outcome: "not_attempted"})
		return
	}
	var body []byte
	if r.Kind == "tool" && selectedTool.Protocol == "mcp_http" {
		body = mcpRequestBody(r, selectedTool)
	} else if r.Kind == "model" {
		var model Model
		for _, m := range cfg.Models {
			if m.ID == r.ModelID {
				model = m
			}
		}
		var payloadErr error
		body, payloadErr = modelRequestBody(sourceInput(r), model)
		if stream, _ := ModelRequestStream(r.Input); stream && model.ResponseFormat != "openai_chat" {
			payloadErr = errors.New("streaming requires typed chat response")
		}
		if payloadErr != nil {
			h.finishError(w, in, r, d, "model_payload_target_or_protocol_invalid", 422, Execution{Outcome: "not_attempted"})
			return
		}
	} else {
		body, _ = json.Marshal(struct {
			Action    string            `json:"action"`
			Target    string            `json:"target"`
			Arguments map[string]string `json:"arguments"`
		}{r.Action, r.Target, r.Arguments})
	}
	if r.Kind == "model" {
		if len(body) > cfg.Policy.MaxInputBytes {
			h.finishError(w, in, r, d, "model_wire_input_budget", 422, Execution{Outcome: "not_attempted"})
			return
		}
		r.modelWireBytes = int64(len(body))
	}
	if r.TaskID != "" {
		in = in.WithContext(context.WithValue(in.Context(), cacheTaskContextKey{}, taskBinding(in.Context(), r)))
	}
	scope := modelCacheScope(r, d)
	cacheable := r.Kind == "model" && h.options.Cache != nil
	if cacheable {
		var selected Model
		for _, model := range cfg.Models {
			if model.ID == r.ModelID {
				selected = model
			}
		}
		cacheable = selected.ResponseFormat == "openai_chat" && selected.Media == nil && sourceCacheAllowed(r, cfg)
	}
	if cacheable {
		if cached, hit, err := h.options.Cache.Get(in.Context(), scope); err != nil {
			slog.Warn("gateway cache unavailable", "cause", err, "applicationId", scope.ApplicationID, "principalId", scope.PrincipalID, "modelId", scope.ModelID, "catalogDigest", scope.CatalogDigest)
			h.finishError(w, in, r, d, "cache_unavailable", 503, Execution{Outcome: "not_attempted"})
			return
		} else if hit {
			checked := r
			checked.Output = string(cached)
			output := Evaluate(cfg, checked, h.options.Now())
			output.RequestDigest = d.RequestDigest
			var normalized struct {
				Model        string `json:"model"`
				Output       string `json:"output"`
				FinishReason string `json:"finishReason"`
			}
			var selected Model
			for _, m := range cfg.Models {
				if m.ID == r.ModelID {
					selected = m
				}
			}
			expected := selected.UpstreamModel
			if expected == "" {
				expected = selected.ID
			}
			if strictJSON(bytes.NewReader(cached), &normalized) != nil || normalized.Model != expected || normalized.FinishReason != "stop" && normalized.FinishReason != "length" || output.Outcome != "allow" || !h.fresh(in.Context(), r, d) {
				h.finishError(w, in, r, output, "cache_output_policy_or_scope_denied", 403, Execution{Outcome: "not_attempted"})
				return
			}
			ctx := context.WithValue(in.Context(), cacheEvidenceKey{}, cacheEvidence(scope, cached, true))
			output.Coverage = append(output.Coverage, "encrypted_exact_cache_rechecked")
			if h.audit(ctx, r, output, "completed", Execution{Outcome: "not_attempted", CacheHit: true}) != nil {
				respond(w, 503, map[string]string{"code": "audit_unavailable"})
				return
			}
			if !h.fresh(in.Context(), r, d) || in.Context().Err() != nil {
				respond(w, 403, map[string]string{"code": "task_or_source_changed_before_cache_release"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Scarlet-Cache", "hit")
			captureGatewayCheckedOutput(w, in.Context(), r, output)
			w.WriteHeader(200)
			_, _ = w.Write(cached)
			return
		}
	}
	if r.Kind == "model" && h.authorize(in.Context(), r, d) != nil {
		h.finishError(w, in, r, d, "gateway_governance_denied", 403, Execution{Outcome: "not_attempted"})
		return
	}
	if !h.fresh(in.Context(), r, d) {
		h.finishError(w, in, r, d, "catalog_or_authorization_changed", 403, Execution{Outcome: "not_attempted"})
		return
	}
	req, e := http.NewRequestWithContext(in.Context(), "POST", target.URL, bytes.NewReader(body))
	if e != nil {
		h.finishError(w, in, r, d, "static_target_invalid", 503, Execution{Outcome: "not_attempted"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if selectedTool.Protocol == "mcp_http" {
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", MCPVersion)
		req.Header.Set("Mcp-Method", "tools/call")
		req.Header.Set("Mcp-Name", r.Action)
	}
	req.Header.Set("Idempotency-Key", d.RequestDigest)
	if target.Authorization != "" {
		req.Header.Set("Authorization", target.Authorization)
	}
	resp, e := h.client.Do(req)
	if e != nil {
		h.finishError(w, in, r, d, "upstream_result_unknown", 502, Execution{Attempted: true, Outcome: "unknown"})
		return
	}
	defer resp.Body.Close()
	x := Execution{Attempted: true, Outcome: "failed", HTTPStatus: resp.StatusCode}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		h.finishError(w, in, r, d, "upstream_non_success_or_redirect", 502, x)
		return
	}
	var raw []byte
	stream, streamErr := ModelRequestStream(r.Input)
	if r.Kind == "model" && streamErr == nil && stream {
		var selected Model
		for _, model := range cfg.Models {
			if model.ID == r.ModelID {
				selected = model
			}
		}
		fixed, _ := in.Context().Value(bifrostReplyKey{}).(bool)
		if resp.Header.Get("Content-Type") != "text/event-stream" && !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream;") {
			e = errors.New("SSE required")
		} else {
			raw, e = assembleModelStream(resp.Body, selected, cfg.Policy.MaxOutputBytes, fixed, func() bool { return h.fresh(in.Context(), r, d) && in.Context().Err() == nil })
		}
		if e == nil {
			d.Coverage = append(d.Coverage, "streaming_strict_hold")
		}
	} else {
		raw, e = io.ReadAll(io.LimitReader(resp.Body, int64(cfg.Policy.MaxOutputBytes)+1))
	}
	if e != nil || len(raw) > cfg.Policy.MaxOutputBytes {
		h.finishError(w, in, r, d, "upstream_output_incomplete", 502, Execution{Attempted: true, Outcome: "unknown", HTTPStatus: resp.StatusCode})
		return
	}
	if selectedTool.Protocol == "mcp_http" {
		var parseErr error
		raw, parseErr = mcpResultBody(raw, resp.Header, r.RequestID)
		if parseErr != nil {
			h.finishError(w, in, r, d, "mcp_result_invalid_or_incomplete", 502, Execution{Attempted: true, Outcome: "unknown", HTTPStatus: resp.StatusCode})
			return
		}
		d.Coverage = append(d.Coverage, "mcp_http_2026_07_28_text_json_result", "no_mcp_discovery_mrtr_legacy_sessions_or_multimodal")
	}
	if resp.Header.Get("Content-Encoding") != "" || !json.Valid(raw) {
		h.finishError(w, in, r, d, "upstream_output_unsupported", 422, Execution{Attempted: true, Outcome: "blocked_output", HTTPStatus: resp.StatusCode})
		return
	}
	outputD := d
	if r.Kind == "model" {
		var selected Model
		for _, m := range cfg.Models {
			if m.ID == r.ModelID {
				selected = m
			}
		}
		var normalizedErr error
		if fixed, _ := in.Context().Value(bifrostReplyKey{}).(bool); fixed {
			raw, normalizedErr = normalizeBifrostEnvelope(raw)
			if normalizedErr != nil {
				h.finishError(w, in, r, d, "bifrost_response_envelope_invalid", 422, Execution{Attempted: true, Outcome: "blocked_output", HTTPStatus: resp.StatusCode})
				return
			}
		}
		raw, normalizedErr = normalizeModelOutputWithMedia(in.Context(), raw, selected, h.options.MediaProcessor)
		if normalizedErr != nil {
			h.finishError(w, in, r, d, "model_response_target_or_schema_invalid", 422, Execution{Attempted: true, Outcome: "blocked_output", HTTPStatus: resp.StatusCode})
			return
		}
		checked := r
		checked.Output = string(raw)
		// The normalized bytes are not yet inspected. This is the sole output
		// recognition pass; Evaluate must use its bound projection and categories.
		checked, normalizedErr = ApplyModelOutputInspection(in.Context(), checked, raw, selected, h.options.MediaProcessor)
		if normalizedErr != nil {
			h.finishError(w, in, r, d, "media_output_inspection_required_or_invalid", 422, Execution{Attempted: true, Outcome: "blocked_output", HTTPStatus: resp.StatusCode})
			return
		}
		outputD = Evaluate(cfg, checked, h.options.Now())
		outputD.RequestDigest = d.RequestDigest
		if selected.ResponseFormat == "openai_chat" {
			outputD.Coverage = append(outputD.Coverage, "openai_chat_reported_model_and_text_schema_checked")
		} else {
			outputD.Coverage = append(outputD.Coverage, "generic_json_response_without_provider_schema_verification")
		}
	} else {
		found, code := inspect(string(raw), cfg.Policy.MaxOutputBytes, cfg.Policy.BlockedOutputPatterns, cfg.Policy.Inspection)
		outputD.Categories = unique(append(outputD.Categories, found...))
		if code != "" || !allowedCategories(found, cfg.Policy.AllowedOutputCategories, nil, false) {
			outputD.Outcome = "deny"
			outputD.Code = "tool_output_denied"
		}
	}
	if outputD.Outcome != "allow" {
		h.finishError(w, in, r, outputD, "output_policy_denied", 403, Execution{Attempted: true, Outcome: "blocked_output", HTTPStatus: resp.StatusCode})
		return
	}
	if !h.fresh(in.Context(), r, d) {
		h.finishError(w, in, r, d, "catalog_changed_output_suppressed", 403, Execution{Attempted: true, Outcome: "blocked_output", HTTPStatus: resp.StatusCode})
		return
	}
	x.Outcome = "succeeded"
	if cacheable {
		in = in.WithContext(context.WithValue(in.Context(), cacheEvidenceKey{}, cacheEvidence(scope, raw, false)))
	}
	if h.audit(in.Context(), r, outputD, "completed", x) != nil {
		respond(w, 503, map[string]string{"code": "audit_unavailable_execution_unknown"})
		return
	}
	if cacheable {
		if err := h.options.Cache.Put(in.Context(), scope, raw); err != nil { /* A cache write never changes a checked provider result. */
		}
	}
	if !h.fresh(in.Context(), r, d) || in.Context().Err() != nil {
		// Release failure does not replace the already recorded execution result.
		respond(w, 403, map[string]string{"code": "catalog_changed_output_suppressed"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	captureGatewayCheckedOutput(w, in.Context(), r, outputD)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(raw)
}

func (h *runtimeHandler) authorize(ctx context.Context, r Request, d Decision) error {
	if before, ok := ctx.Value(gatewayBeforeKey{}).(func(context.Context, Request, Decision) error); ok {
		return before(ctx, r, d)
	}
	return nil
}
