package aisecurity

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const gatewayMCPControlMeta = "org.scarletplume/control"

type gatewayMCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type gatewayMCPParams struct {
	Meta      map[string]json.RawMessage `json:"_meta"`
	Name      string                     `json:"name,omitempty"`
	Arguments map[string]string          `json:"arguments,omitempty"`
	Cursor    string                     `json:"cursor,omitempty"`
}
type gatewayMCPControl struct {
	RequestID  string    `json:"requestId,omitempty"`
	IssuedAt   time.Time `json:"issuedAt,omitempty"`
	ExpiresAt  time.Time `json:"expiresAt,omitempty"`
	ApprovalID string    `json:"approvalId,omitempty"`
}

func gatewayMCPID(raw json.RawMessage) bool {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return bounded(s, 256)
	}
	var n json.Number
	if json.Unmarshal(raw, &n) != nil {
		return false
	}
	_, e := strconv.ParseInt(n.String(), 10, 64)
	return e == nil && string(raw) != "null"
}
func gatewayMCPError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string, data any) {
	value := map[string]any{"code": code, "message": message}
	if data != nil {
		value["data"] = data
	}
	out := map[string]any{"jsonrpc": "2.0", "error": value}
	if gatewayMCPID(id) {
		out["id"] = id
	}
	respond(w, status, out)
}
func gatewayMCPResult(w http.ResponseWriter, id json.RawMessage, result map[string]any) {
	result["resultType"] = "complete"
	if result["_meta"] == nil {
		result["_meta"] = map[string]any{"io.modelcontextprotocol/serverInfo": map[string]string{"name": "scarlet-ai-gateway", "version": RuntimeProtocolVersion}}
	}
	respond(w, 200, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}
func gatewayMCPHeader(raw string) (string, error) {
	if strings.HasPrefix(raw, "=?base64?") && strings.HasSuffix(raw, "?=") {
		v, e := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(raw, "=?base64?"), "?="))
		if e != nil || !utf8.Valid(v) || len(v) > 4096 {
			return "", errors.New("invalid mirrored header")
		}
		return string(v), nil
	}
	if len(raw) > 4096 || strings.TrimSpace(raw) != raw {
		return "", errors.New("invalid mirrored header")
	}
	for _, v := range raw {
		if v < 32 || v > 126 {
			return "", errors.New("invalid mirrored header")
		}
	}
	return raw, nil
}

// This facade implements the stateless 2026-07-28 HTTP profile. Legacy
// initialize/sessions, MRTR, subscriptions, multimodal and automatic execution
// are intentionally not advertised. Browser Origins are denied by default;
// authenticated server-to-server clients do not send an Origin header.
func (g *gatewayHandler) mcpFacade(w http.ResponseWriter, in *http.Request, identity Identity) {
	if in.Header.Get("Origin") != "" {
		gatewayMCPError(w, 403, nil, -32000, "Browser origin is not enabled", nil)
		return
	}
	var rpc gatewayMCPRequest
	if strictJSON(in.Body, &rpc) != nil || rpc.JSONRPC != "2.0" || !gatewayMCPID(rpc.ID) {
		gatewayMCPError(w, 400, nil, -32600, "Invalid JSON-RPC request", nil)
		return
	}
	var p gatewayMCPParams
	if strictJSON(bytes.NewReader(rpc.Params), &p) != nil {
		gatewayMCPError(w, 400, rpc.ID, -32602, "Invalid MCP parameters", nil)
		return
	}
	accept := map[string]bool{}
	for _, part := range strings.Split(in.Header.Get("Accept"), ",") {
		typ, params, e := mime.ParseMediaType(strings.TrimSpace(part))
		if e == nil && params["q"] != "0" {
			accept[typ] = true
		}
	}
	if !accept["application/json"] || !accept["text/event-stream"] {
		gatewayMCPError(w, 406, rpc.ID, -32600, "Accept must include JSON and event-stream", nil)
		return
	}
	var version string
	var capabilities map[string]json.RawMessage
	if json.Unmarshal(p.Meta["io.modelcontextprotocol/protocolVersion"], &version) != nil || json.Unmarshal(p.Meta["io.modelcontextprotocol/clientCapabilities"], &capabilities) != nil || capabilities == nil {
		gatewayMCPError(w, 400, rpc.ID, -32602, "Required MCP metadata missing", nil)
		return
	}
	if version != MCPVersion {
		gatewayMCPError(w, 400, rpc.ID, -32022, "Unsupported protocol version", map[string]any{"supported": []string{MCPVersion}, "requested": version})
		return
	}
	if len(in.Header.Values("MCP-Protocol-Version")) != 1 || in.Header.Get("MCP-Protocol-Version") != version || len(in.Header.Values("Mcp-Method")) != 1 || in.Header.Get("Mcp-Method") != rpc.Method {
		gatewayMCPError(w, 400, rpc.ID, -32020, "MCP header mismatch", nil)
		return
	}
	if raw, ok := p.Meta["io.modelcontextprotocol/clientInfo"]; ok {
		var info map[string]json.RawMessage
		var name, version string
		if json.Unmarshal(raw, &info) != nil || json.Unmarshal(info["name"], &name) != nil || json.Unmarshal(info["version"], &version) != nil || name == "" || !bounded(name, 160) || !bounded(version, 80) {
			gatewayMCPError(w, 400, rpc.ID, -32602, "Invalid client metadata", nil)
			return
		}
	}
	c, e := g.runtime.options.Catalog(in.Context())
	if e != nil || ValidateConfig(c) != nil {
		gatewayMCPError(w, 503, rpc.ID, -32603, "Security catalog unavailable", nil)
		return
	}
	tools, ok := g.mcpVisibleTools(c, identity)
	if !ok {
		gatewayMCPError(w, 403, rpc.ID, -32000, "Identity not authorized", nil)
		return
	}
	if g.discoverTools != nil {
		allowed, err := g.discoverTools(in.Context(), identity, tokenHash(strings.TrimPrefix(in.Header.Get("Authorization"), "Bearer ")), ConfigDigest(c))
		if err != nil {
			gatewayMCPError(w, 503, rpc.ID, -32603, "Tool authorization unavailable", nil)
			return
		}
		tools = slices.DeleteFunc(tools, func(v gatewayMCPVisibleTool) bool { return !slices.Contains(allowed, v.tool.ID) })
	}
	taskID, taskErr := selectedTask(in, "")
	if taskErr != nil {
		gatewayMCPError(w, 400, rpc.ID, -32602, "Invalid task selector", nil)
		return
	}
	requireTask := false
	for _, app := range c.Applications {
		if app.ID == identity.ApplicationID {
			requireTask = app.RequireTask
		}
	}
	if requireTask && taskID == "" {
		gatewayMCPError(w, 403, rpc.ID, -32000, "Task required", nil)
		return
	}
	if taskID != "" {
		r := Request{TaskID: taskID, RequestID: "discovery-" + hash(taskID), Kind: "discovery", ApplicationID: identity.ApplicationID, PrincipalID: identity.PrincipalID}
		_, grant, err := g.runtime.task(in.Context(), r, Decision{CatalogDigest: ConfigDigest(c)}, "check", nil)
		if err != nil {
			gatewayMCPError(w, 403, rpc.ID, -32000, "Task discovery denied", nil)
			return
		}
		tools = slices.DeleteFunc(tools, func(v gatewayMCPVisibleTool) bool { return !slices.Contains(grant.ToolIDs, v.tool.ID) })
		for i := range tools {
			foundScope := false
			for _, scope := range grant.ToolScopes {
				if scope.ToolID == tools[i].tool.ID {
					foundScope = true
					tools[i].tool.Targets = slices.DeleteFunc(append([]string{}, tools[i].tool.Targets...), func(v string) bool { return !slices.Contains(scope.Targets, v) })
					if !slices.Contains(scope.Actions, tools[i].binding.Action) {
						tools[i].tool.Targets = nil
					}
				}
			}
			if !foundScope {
				tools[i].tool.Targets = nil
			}
		}
		tools = slices.DeleteFunc(tools, func(v gatewayMCPVisibleTool) bool { return len(v.tool.Targets) == 0 })
	}
	switch rpc.Method {
	case "server/discover":
		if p.Name != "" || len(p.Arguments) != 0 || p.Cursor != "" {
			gatewayMCPError(w, 400, rpc.ID, -32602, "Unexpected discovery parameters", nil)
			return
		}
		gatewayMCPResult(w, rpc.ID, map[string]any{"supportedVersions": []string{MCPVersion}, "capabilities": map[string]any{"tools": map[string]any{}}, "instructions": "Registered string-argument tools only. High-impact actions require a separate digest-bound approval.", "ttlMs": 0, "cacheScope": "private"})
	case "tools/list":
		if p.Cursor != "" || p.Name != "" || len(p.Arguments) != 0 {
			gatewayMCPError(w, 400, rpc.ID, -32602, "Unsupported list parameters", nil)
			return
		}
		definitions := []map[string]any{}
		for _, binding := range tools {
			tool := binding.tool
			properties := map[string]any{tool.TargetArgument: map[string]any{"type": "string", "enum": tool.Targets}}
			for name, values := range tool.AllowedArguments {
				properties[name] = map[string]any{"type": "string", "enum": values}
			}
			required := append([]string{tool.TargetArgument}, tool.RequiredArguments...)
			sort.Strings(required)
			definitions = append(definitions, map[string]any{"name": binding.binding.Name, "description": tool.Name, "inputSchema": map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}, "annotations": map[string]any{"destructiveHint": tool.HighImpact}})
		}
		gatewayMCPResult(w, rpc.ID, map[string]any{"tools": definitions, "ttlMs": 0, "cacheScope": "private"})
	case "tools/call":
		name, err := gatewayMCPHeader(in.Header.Get("Mcp-Name"))
		if err != nil || len(in.Header.Values("Mcp-Name")) != 1 || name != p.Name {
			gatewayMCPError(w, 400, rpc.ID, -32020, "MCP tool header mismatch", nil)
			return
		}
		if p.Cursor != "" {
			gatewayMCPError(w, 400, rpc.ID, -32602, "Unexpected tool cursor", nil)
			return
		}
		var selected *gatewayMCPVisibleTool
		for i := range tools {
			if tools[i].binding.Name == p.Name {
				selected = &tools[i]
				break
			}
		}
		if selected == nil {
			gatewayMCPError(w, 403, rpc.ID, -32000, "Tool not authorized", nil)
			return
		}
		for h, values := range in.Header {
			if !strings.HasPrefix(strings.ToLower(h), "mcp-param-") {
				continue
			}
			key := h[len("Mcp-Param-"):]
			matches := 0
			expected := ""
			for k, v := range p.Arguments {
				if strings.EqualFold(k, key) {
					matches++
					expected = v
				}
			}
			if matches != 1 || len(values) != 1 {
				gatewayMCPError(w, 400, rpc.ID, -32020, "MCP parameter header mismatch", nil)
				return
			}
			actual, e := gatewayMCPHeader(values[0])
			if e != nil || actual != expected {
				gatewayMCPError(w, 400, rpc.ID, -32020, "MCP parameter header mismatch", nil)
				return
			}
		}
		target, exists := p.Arguments[selected.tool.TargetArgument]
		if !exists {
			gatewayMCPError(w, 400, rpc.ID, -32602, "Explicit tool target required", nil)
			return
		}
		var control gatewayMCPControl
		if raw, exists := p.Meta[gatewayMCPControlMeta]; exists && strictJSON(bytes.NewReader(raw), &control) != nil {
			gatewayMCPError(w, 400, rpc.ID, -32602, "Invalid execution metadata", nil)
			return
		}
		if header := in.Header.Get("Idempotency-Key"); header != "" {
			if control.RequestID != "" && control.RequestID != header {
				gatewayMCPError(w, 400, rpc.ID, -32602, "Execution nonce mismatch", nil)
				return
			}
			control.RequestID = header
		}
		if control.RequestID == "" {
			var nonce [16]byte
			if _, e := rand.Read(nonce[:]); e != nil {
				gatewayMCPError(w, 503, rpc.ID, -32603, "Execution nonce unavailable", nil)
				return
			}
			control.RequestID = hex.EncodeToString(nonce[:])
		}
		if !validID(control.RequestID) || len(control.RequestID) < 16 {
			gatewayMCPError(w, 400, rpc.ID, -32602, "Invalid execution nonce", nil)
			return
		}
		now := g.runtime.options.Now().UTC()
		if control.IssuedAt.IsZero() {
			control.IssuedAt = now
		}
		if control.ExpiresAt.IsZero() {
			control.ExpiresAt = control.IssuedAt.Add(time.Duration(min(c.Policy.MaxRequestAgeSeconds, 60)) * time.Second)
		}
		delete(p.Arguments, selected.tool.TargetArgument)
		buffer := &gatewayBuffer{header: http.Header{}}
		w.Header().Set("X-Scarlet-Request-ID", control.RequestID)
		g.execute(buffer, in, identity, RuntimeRequest{RequestID: control.RequestID, Kind: "tool", ToolID: selected.binding.ToolID, Action: selected.binding.Action, Target: target, Arguments: p.Arguments, ApprovalID: control.ApprovalID, IssuedAt: control.IssuedAt, ExpiresAt: control.ExpiresAt}, false)
		if buffer.status != 200 {
			var detail struct {
				Code          string `json:"code"`
				RequestDigest string `json:"requestDigest"`
				CatalogDigest string `json:"catalogDigest"`
			}
			_ = json.Unmarshal(buffer.body.Bytes(), &detail)
			data := map[string]any{"requestId": control.RequestID, "code": detail.Code, "requestDigest": detail.RequestDigest, "catalogDigest": detail.CatalogDigest}
			gatewayMCPError(w, buffer.status, rpc.ID, -32000, "Tool execution rejected", data)
			return
		}
		var envelope mcpEnvelope
		if strictJSON(bytes.NewReader(buffer.body.Bytes()), &envelope) != nil || len(envelope.Result) == 0 {
			gatewayMCPError(w, 502, rpc.ID, -32603, "Tool result invalid", nil)
			return
		}
		var result map[string]any
		resultDecoder := json.NewDecoder(bytes.NewReader(envelope.Result))
		resultDecoder.UseNumber()
		if resultDecoder.Decode(&result) != nil || result == nil {
			gatewayMCPError(w, 502, rpc.ID, -32603, "Tool result invalid", nil)
			return
		}
		// Upstream metadata is untrusted and need not be exposed to clients.
		result["_meta"] = map[string]any{"io.modelcontextprotocol/serverInfo": map[string]string{"name": "scarlet-ai-gateway", "version": RuntimeProtocolVersion}, gatewayMCPControlMeta: map[string]string{"requestId": control.RequestID}}
		gatewayMCPResult(w, rpc.ID, result)
	default:
		gatewayMCPError(w, 404, rpc.ID, -32601, "Method not supported by this stateless profile", nil)
	}
}

type gatewayMCPVisibleTool struct {
	binding GatewayTool
	tool    Tool
}

func (g *gatewayHandler) mcpVisibleTools(c Config, identity Identity) ([]gatewayMCPVisibleTool, bool) {
	var app Application
	var principal Principal
	for _, v := range c.Applications {
		if v.ID == identity.ApplicationID {
			app = v
		}
	}
	for _, v := range c.Principals {
		if v.ID == identity.PrincipalID {
			principal = v
		}
	}
	now := g.runtime.options.Now()
	if !app.Enabled || !principal.Enabled || !slices.Contains(app.PrincipalIDs, principal.ID) || !slices.Contains(principal.ApplicationIDs, app.ID) || principal.AuthorizationExpiresAt != nil && !now.Before(*principal.AuthorizationExpiresAt) || c.Policy.RequireAuthorizationLeases && principal.AuthorizationExpiresAt == nil {
		return nil, false
	}
	out := []gatewayMCPVisibleTool{}
	for _, binding := range g.tools {
		for _, tool := range c.Tools {
			if tool.ID == binding.ToolID && tool.Enabled && tool.Protocol == "mcp_http" && slices.Contains(app.ToolIDs, tool.ID) && slices.Contains(tool.AllowedApplicationIDs, app.ID) && slices.Contains(tool.AllowedPrincipalIDs, principal.ID) && slices.Contains(tool.Actions, binding.Action) {
				out = append(out, gatewayMCPVisibleTool{binding, tool})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].binding.Name < out[j].binding.Name })
	return out, true
}
