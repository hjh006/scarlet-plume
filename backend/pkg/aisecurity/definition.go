package aisecurity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"
)

// ToolDefinitionDigest pins the complete bounded provider Tool object, including
// descriptions/annotations/extensions, without interpreting them as authority.
// Map keys are sorted by encoding/json; JSON number spelling is preserved. This
// deliberately is not an RFC 8785 implementation. Duplicate keys are rejected.
func ToolDefinitionDigest(raw json.RawMessage) (string, error) {
	if len(raw) > 65536 || !utf8.Valid(raw) {
		return "", errors.New("tool definition budget or encoding")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	nodes := 0
	value, err := definitionValue(d, 0, &nodes, true)
	if err != nil {
		return "", err
	}
	if _, err = d.Token(); err != io.EOF {
		return "", errors.New("trailing definition JSON")
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return "", errors.New("tool definition must be an object")
	}
	name, ok := obj["name"].(string)
	if !ok || !validID(name) {
		return "", errors.New("invalid tool definition name")
	}
	if _, ok := obj["inputSchema"].(map[string]any); !ok {
		return "", errors.New("tool input schema must be an object")
	}
	return hash(value), nil
}

// The same token walker rejects ambiguous JSON at the runtime wire boundary.
// The smaller definition profile additionally rejects unsupported header routing.
func definitionValue(d *json.Decoder, depth int, nodes *int, definition bool) (any, error) {
	*nodes++
	maxDepth, maxNodes := 64, 131072
	if definition {
		maxDepth, maxNodes = 32, 8192
	}
	if depth > maxDepth || *nodes > maxNodes {
		return nil, errors.New("JSON complexity budget")
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		obj := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok || definition && name == "x-mcp-header" {
				return nil, errors.New("unsupported tool schema header routing")
			}
			if _, exists := obj[name]; exists {
				return nil, errors.New("duplicate definition key")
			}
			value, err := definitionValue(d, depth+1, nodes, definition)
			if err != nil {
				return nil, err
			}
			obj[name] = value
		}
		if _, err := d.Token(); err != nil {
			return nil, err
		}
		return obj, nil
	case json.Delim('['):
		values := []any{}
		for d.More() {
			value, err := definitionValue(d, depth+1, nodes, definition)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		if _, err := d.Token(); err != nil {
			return nil, err
		}
		return values, nil
	}
	if _, isDelimiter := token.(json.Delim); isDelimiter {
		return nil, errors.New("invalid definition JSON")
	}
	return token, nil
}

func (h *runtimeHandler) checkToolDefinition(ctx context.Context, r Request, t Tool, target Target, limit int) error {
	id := r.RequestID + "-definition"
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/list", "params": map[string]any{"_meta": mcpMeta()}})
	req, err := http.NewRequestWithContext(ctx, "POST", target.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", MCPVersion)
	req.Header.Set("Mcp-Method", "tools/list")
	if target.Authorization != "" {
		req.Header.Set("Authorization", target.Authorization)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("tool definition response unsuccessful")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil || len(raw) > limit {
		return errors.New("tool definition response incomplete")
	}
	message, err := mcpResponseMessage(raw, resp.Header, id)
	if err != nil {
		return err
	}
	var envelope mcpEnvelope
	_ = json.Unmarshal(message, &envelope)
	var result struct {
		ResultType string            `json:"resultType"`
		Tools      []json.RawMessage `json:"tools"`
		NextCursor string            `json:"nextCursor,omitempty"`
		TTLMS      int64             `json:"ttlMs,omitempty"`
		CacheScope string            `json:"cacheScope,omitempty"`
		Meta       json.RawMessage   `json:"_meta,omitempty"`
	}
	if strictJSON(bytes.NewReader(envelope.Result), &result) != nil || result.ResultType != "complete" || result.NextCursor != "" || len(result.Tools) > 256 {
		return errors.New("tool definition list incomplete or unsupported")
	}
	found := false
	for _, raw := range result.Tools {
		var named struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &named) != nil {
			return errors.New("invalid tool definition")
		}
		if named.Name == r.Action {
			actual, err := ToolDefinitionDigest(raw)
			if err != nil || found || actual != t.DefinitionDigests[r.Action] {
				return errors.New("tool definition changed or ambiguous")
			}
			found = true
		}
	}
	if !found {
		return errors.New("pinned tool absent")
	}
	return nil
}
