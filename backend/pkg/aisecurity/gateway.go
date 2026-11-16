package aisecurity

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

// GatewayAuthorization is content-free, derived after credential authentication
// and catalog evaluation. It is never accepted from a caller as identity proof.
type GatewayAuthorization struct {
	gatewaygovernance.TaskContext
	ApprovalID             string   `json:"approvalId,omitempty"`
	SourceDigests          []string `json:"sourceDigests,omitempty"`
	ProjectionBytes        int64    `json:"projectionBytes,omitempty"`
	MediaBytes             int64    `json:"mediaBytes,omitempty"`
	MediaDigests           []string `json:"mediaDigests,omitempty"`
	MediaInspectionVersion string   `json:"mediaInspectionVersion,omitempty"`
	Identity
	TokenHash             string `json:"tokenHash"`
	Kind                  string `json:"kind"`
	RequestID             string `json:"requestId"`
	ModelID               string `json:"modelId,omitempty"`
	ToolID                string `json:"toolId,omitempty"`
	Action                string `json:"action,omitempty"`
	Target                string `json:"target,omitempty"`
	RequestDigest         string `json:"requestDigest"`
	CatalogDigest         string `json:"catalogDigest"`
	InputBytes            int64  `json:"inputBytes"`
	MaxOutputTokens       int64  `json:"maxOutputTokens"`
	ParentRequestID       string `json:"parentRequestId,omitempty"`
	RootModelID           string `json:"rootModelId,omitempty"`
	PreviousReservationID string `json:"previousReservationId,omitempty"`
	Attempt               int    `json:"attempt,omitempty"`
}

type GatewayTool struct {
	Name   string `json:"name"`
	ToolID string `json:"toolId"`
	Action string `json:"action"`
}

type GatewayOptions struct {
	Runtime         RuntimeOptions
	Bifrost         *BifrostProfile
	BifrostProfiles []BifrostProfile
	Fallbacks       map[string][]string
	FallbackModels  func(context.Context, string) ([]string, error)
	Tools           []GatewayTool
	BeforeExecute   func(context.Context, GatewayAuthorization) error
	DiscoverTools   func(context.Context, Identity, string, string) ([]string, error)
}

type gatewayHandler struct {
	runtime         *runtimeHandler
	tools           map[string]GatewayTool
	before          func(context.Context, GatewayAuthorization) error
	slots           chan struct{}
	bifrost         *BifrostProfile
	bifrostProfiles bool
	discoverTools   func(context.Context, Identity, string, string) ([]string, error)
	fallbacks       map[string][]string
	fallbackModels  func(context.Context, string) ([]string, error)
}
type gatewayCatalogDigestKey struct{}
type gatewayCallIDKey struct{}
type bifrostReplyKey struct{}

// NewGateway adds a bounded text-only OpenAI facade and explicitly registered
// MCP tools to the existing enforcement runtime. It is not a transparent proxy.
func NewGateway(o GatewayOptions) (http.Handler, error) {
	originalCatalog := o.Runtime.Catalog
	if originalCatalog == nil {
		return nil, errors.New("catalog required")
	}
	o.Runtime.Catalog = func(ctx context.Context) (Config, error) {
		c, err := originalCatalog(ctx)
		if err == nil && len(o.BifrostProfiles) > 0 {
			err = ValidateBifrostCatalogProfiles(o.BifrostProfiles, c, o.Runtime.Targets)
		} else if err == nil && o.Bifrost != nil {
			err = ValidateBifrostCatalog(*o.Bifrost, c, o.Runtime.Targets)
		}
		if expected, _ := ctx.Value(gatewayCatalogDigestKey{}).(string); err == nil && expected != "" && ConfigDigest(c) != expected {
			return Config{}, ErrConfig
		}
		return c, err
	}
	runtime, err := NewRuntime(o.Runtime)
	if err != nil {
		return nil, err
	}
	concurrency := o.Runtime.Concurrency
	if concurrency == 0 {
		concurrency = 8
	}
	g := &gatewayHandler{runtime: runtime.(*runtimeHandler), tools: map[string]GatewayTool{}, before: o.BeforeExecute, slots: make(chan struct{}, concurrency), bifrost: o.Bifrost, discoverTools: o.DiscoverTools, fallbacks: o.Fallbacks, fallbackModels: o.FallbackModels, bifrostProfiles: len(o.BifrostProfiles) > 0}
	c, err := o.Runtime.Catalog(context.Background())
	if err != nil {
		return nil, err
	}
	for _, binding := range o.Tools {
		if !validID(binding.Name) || !validID(binding.ToolID) || !validID(binding.Action) {
			return nil, errors.New("invalid gateway tool binding")
		}
		if _, exists := g.tools[binding.Name]; exists {
			return nil, errors.New("duplicate gateway tool")
		}
		found := false
		for _, tool := range c.Tools {
			if tool.ID == binding.ToolID {
				for _, action := range tool.Actions {
					if action == binding.Action {
						found = true
					}
				}
			}
		}
		if !found {
			return nil, errors.New("gateway tool not registered in catalog")
		}
		g.tools[binding.Name] = binding
	}
	return g, nil
}

func gatewayError(w http.ResponseWriter, status int, code string) {
	respond(w, status, map[string]any{"error": map[string]any{"message": code, "type": "scarlet_security_error", "code": code}})
}

func (g *gatewayHandler) ServeHTTP(w http.ResponseWriter, in *http.Request) {
	if in.URL.Path == "/mcp" && in.Method != "POST" {
		w.Header().Set("Allow", "POST")
		gatewayError(w, http.StatusMethodNotAllowed, "mcp_post_required")
		return
	}
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		gatewayError(w, 429, "gateway_busy")
		return
	}
	if in.Method != "POST" || in.URL.RawQuery != "" {
		gatewayError(w, 404, "not_found")
		return
	}
	if len(in.Header.Values("Content-Type")) != 1 || in.Header.Get("Content-Type") != "application/json" || len(in.Header.Values("Content-Encoding")) > 1 || in.Header.Get("Content-Encoding") != "" {
		gatewayError(w, 415, "json_uncompressed_required")
		return
	}
	auth := in.Header.Get("Authorization")
	if len(in.Header.Values("Authorization")) != 1 || !strings.HasPrefix(auth, "Bearer ") || len(auth) < 39 || len(auth) > 520 {
		gatewayError(w, 401, "authentication_required")
		return
	}
	credentialHash := tokenHash(strings.TrimPrefix(auth, "Bearer "))
	identity, authErr := g.runtime.authenticate(in.Context(), credentialHash)
	if authErr != nil {
		gatewayError(w, 401, "authentication_required")
		return
	}
	in = in.WithContext(context.WithValue(in.Context(), gatewayCredentialHashKey{}, credentialHash))
	taskID, taskErr := selectedTask(in, "")
	if taskErr != nil {
		gatewayError(w, 400, "task_selection_invalid")
		return
	}
	in = in.WithContext(context.WithValue(in.Context(), gatewayTaskSelectionKey{}, taskID))
	var callID [16]byte
	if _, err := rand.Read(callID[:]); err != nil {
		gatewayError(w, 503, "call_reference_unavailable")
		return
	}
	in = in.WithContext(context.WithValue(in.Context(), gatewayCallIDKey{}, hex.EncodeToString(callID[:])))
	in.Body = http.MaxBytesReader(w, in.Body, 16<<20)
	switch in.URL.Path {
	case "/v1/execute":
		var request RuntimeRequest
		if strictJSON(in.Body, &request) != nil {
			gatewayError(w, 400, "request_json_invalid")
			return
		}
		g.execute(w, in, identity, request, false)
	case "/v1/chat/completions":
		g.chat(w, in, identity)
	case "/mcp":
		g.mcpFacade(w, in, identity)
	case "/v1/mcp/execute":
		g.mcp(w, in, identity)
	default:
		gatewayError(w, 404, "not_found")
	}
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (g *gatewayHandler) executeAttempt(w http.ResponseWriter, in *http.Request, identity Identity, wire RuntimeRequest, c Config) {
	d := Decision{CatalogDigest: ConfigDigest(c)}
	body, _ := json.Marshal(wire)
	ctx := context.WithValue(context.WithValue(in.Context(), gatewayCatalogDigestKey{}, d.CatalogDigest), gatewayIdentityKey{}, identity)
	ctx = context.WithValue(ctx, gatewayBeforeKey{}, g.beforeRuntime)
	if g.bifrost != nil || g.bifrostProfiles {
		ctx = context.WithValue(ctx, bifrostReplyKey{}, true)
	}
	forward := in.Clone(ctx)
	forward.URL.Path, forward.URL.RawQuery = "/v1/execute", ""
	forward.Body = io.NopCloser(bytes.NewReader(body))
	forward.ContentLength = int64(len(body))
	g.runtime.ServeHTTP(w, forward)
}

func (g *gatewayHandler) chat(w http.ResponseWriter, in *http.Request, identity Identity) {
	nonces := in.Header.Values("Idempotency-Key")
	if len(nonces) > 1 || len(nonces) == 1 && (len(nonces[0]) < 16 || !validID(nonces[0])) {
		gatewayError(w, 400, "request_nonce_invalid")
		return
	}
	var body modelPayload
	if strictJSON(in.Body, &body) != nil || body.Prompt != nil || body.Input != nil || len(body.Messages) == 0 || len(body.Messages) > 128 {
		gatewayError(w, 400, "supported_chat_json_required")
		return
	}
	logicalModel := body.Model
	c, err := g.runtime.options.Catalog(in.Context())
	if err != nil {
		gatewayError(w, 503, "catalog_unavailable")
		return
	}
	found := false
	for _, model := range c.Models {
		if model.ID == logicalModel && model.ResponseFormat == "openai_chat" {
			found = true
		}
	}
	if !found {
		gatewayError(w, 403, "configured_text_model_required")
		return
	}
	// The model selector comes from the catalog inside Runtime; the public alias
	// is not forwarded as a provider/deployment override.
	body.Model = ""
	input, _ := json.Marshal(body)
	now := g.runtime.options.Now().UTC()
	id := in.Header.Get("Idempotency-Key")
	if len(nonces) == 0 {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			gatewayError(w, 503, "request_reference_unavailable")
			return
		}
		id = hex.EncodeToString(nonce[:])
	}
	age := c.Policy.MaxRequestAgeSeconds
	if age > 60 {
		age = 60
	}
	issued, expires := now, now.Add(time.Duration(age)*time.Second)
	for header, value := range map[string]*time.Time{"X-Scarlet-Issued-At": &issued, "X-Scarlet-Expires-At": &expires} {
		if values := in.Header.Values(header); len(values) != 0 {
			if len(values) != 1 {
				gatewayError(w, 400, "request_time_invalid")
				return
			}
			parsed, err := time.Parse(time.RFC3339Nano, values[0])
			if err != nil || parsed.IsZero() {
				gatewayError(w, 400, "request_time_invalid")
				return
			}
			*value = parsed
		}
	}
	w.Header().Set("X-Scarlet-Request-ID", id)
	g.execute(w, in, identity, RuntimeRequest{RequestID: id, Kind: "model", ModelID: logicalModel, Input: string(input), IssuedAt: issued, ExpiresAt: expires}, true)
}

// gatewayCheckedOutput holds the original successful execution context. It is
// never serialized or reconstructed from caller-controlled response metadata.
type gatewayCheckedOutput struct {
	values   context.Context
	request  Request
	decision Decision
}

func captureGatewayCheckedOutput(w http.ResponseWriter, ctx context.Context, r Request, d Decision) {
	if b, ok := w.(*gatewayBuffer); ok {
		b.checkedOutput = &gatewayCheckedOutput{values: context.WithoutCancel(ctx), request: r, decision: d}
	}
}

type gatewayBuffer struct {
	checkedOutput *gatewayCheckedOutput
	header        http.Header
	status        int
	body          bytes.Buffer
}

func (b *gatewayBuffer) Header() http.Header { return b.header }
func (b *gatewayBuffer) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}
func (b *gatewayBuffer) Write(raw []byte) (int, error) {
	if b.status == 0 {
		b.status = 200
	}
	// A policy allows up to 16 MiB of checked output. Leave a bounded 64 KiB
	// for normalized JSON escaping and envelope fields, matching strictJSON.
	if b.body.Len()+len(raw) > (16<<20)+65536 {
		return 0, errors.New("gateway output budget")
	}
	return b.body.Write(raw)
}

func (g *gatewayHandler) mcp(w http.ResponseWriter, in *http.Request, identity Identity) {
	var rpc struct {
		JSONRPC string `json:"jsonrpc"`
		ID      string `json:"id"`
		Method  string `json:"method"`
		Params  struct {
			Name      string                     `json:"name,omitempty"`
			Arguments map[string]string          `json:"arguments,omitempty"`
			Meta      map[string]json.RawMessage `json:"_meta"`
		} `json:"params"`
	}
	if strictJSON(in.Body, &rpc) != nil || rpc.JSONRPC != "2.0" || !validID(rpc.ID) || len(rpc.ID) < 16 {
		gatewayError(w, 400, "supported_mcp_request_required")
		return
	}
	var version string
	if json.Unmarshal(rpc.Params.Meta["io.modelcontextprotocol/protocolVersion"], &version) != nil || version != MCPVersion || in.Header.Get("MCP-Protocol-Version") != MCPVersion {
		gatewayError(w, 400, "mcp_protocol_version_required")
		return
	}
	var control struct {
		IssuedAt   time.Time `json:"issuedAt"`
		ExpiresAt  time.Time `json:"expiresAt"`
		ApprovalID string    `json:"approvalId,omitempty"`
	}
	for name, value := range rpc.Params.Meta {
		switch name {
		case "scarlet":
			if strictJSON(bytes.NewReader(value), &control) != nil {
				gatewayError(w, 400, "mcp_control_metadata_invalid")
				return
			}
		case "io.modelcontextprotocol/protocolVersion", "io.modelcontextprotocol/clientInfo", "io.modelcontextprotocol/clientCapabilities":
		default:
			gatewayError(w, 400, "mcp_metadata_unsupported")
			return
		}
	}
	if rpc.Method != "tools/call" {
		gatewayError(w, 400, "only_registered_tools_call_supported")
		return
	}
	binding, ok := g.tools[rpc.Params.Name]
	if !ok {
		gatewayError(w, 403, "gateway_tool_unknown")
		return
	}
	c, err := g.runtime.options.Catalog(in.Context())
	if err != nil {
		gatewayError(w, 503, "catalog_unavailable")
		return
	}
	var tool Tool
	for _, candidate := range c.Tools {
		if candidate.ID == binding.ToolID {
			tool = candidate
		}
	}
	if tool.Protocol != "mcp_http" {
		gatewayError(w, 403, "mcp_tool_profile_required")
		return
	}
	target, ok := rpc.Params.Arguments[tool.TargetArgument]
	if !ok {
		gatewayError(w, 400, "explicit_tool_target_required")
		return
	}
	delete(rpc.Params.Arguments, tool.TargetArgument)
	g.execute(w, in, identity, RuntimeRequest{RequestID: rpc.ID, Kind: "tool", ToolID: binding.ToolID, Action: binding.Action, Target: target, Arguments: rpc.Params.Arguments, ApprovalID: control.ApprovalID, IssuedAt: control.IssuedAt, ExpiresAt: control.ExpiresAt}, false)
}
