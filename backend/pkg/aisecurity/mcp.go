package aisecurity

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
)

// MCPVersion is the supported stateless protocol revision. No legacy handshake,
// session, automatic discovery, MRTR, reconnection or arbitrary proxy is provided.
const MCPVersion = "2026-07-28"

func mcpRequestBody(r Request, t Tool) []byte {
	args := map[string]string{}
	for key, value := range r.Arguments {
		args[key] = value
	}
	args[t.TargetArgument] = r.Target
	request := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      string `json:"id"`
		Method  string `json:"method"`
		Params  struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
			Meta      map[string]any    `json:"_meta"`
		} `json:"params"`
	}{JSONRPC: "2.0", ID: r.RequestID, Method: "tools/call"}
	request.Params.Name = r.Action
	request.Params.Arguments = args
	request.Params.Meta = mcpMeta()
	raw, _ := json.Marshal(request)
	return raw
}

type mcpEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func mcpResponseMessage(raw []byte, headers http.Header, requestID string) ([]byte, error) {
	invalid := errors.New("unsupported or malformed MCP response")
	if headers.Get("Mcp-Session-Id") != "" || headers.Get("Content-Encoding") != "" {
		return nil, invalid
	}
	if version := headers.Get("MCP-Protocol-Version"); version != "" && version != MCPVersion {
		return nil, invalid
	}
	typ, _, err := mime.ParseMediaType(headers.Get("Content-Type"))
	if err != nil {
		return nil, invalid
	}
	messages := [][]byte{}
	switch typ {
	case "application/json":
		messages = append(messages, raw)
	case "text/event-stream":
		scan := bufio.NewScanner(bytes.NewReader(raw))
		scan.Buffer(make([]byte, 4096), 1<<20)
		var data []string
		eventType := ""
		events := 0
		dispatch := func() bool {
			if len(data) == 0 {
				eventType = ""
				return true
			}
			if eventType != "" && eventType != "message" {
				return false
			}
			events++
			if events > 128 {
				return false
			}
			messages = append(messages, []byte(strings.Join(data, "\n")))
			data = nil
			eventType = ""
			return true
		}
		for scan.Scan() {
			line := scan.Text()
			if line == "" {
				if !dispatch() {
					return nil, invalid
				}
				continue
			}
			if strings.HasPrefix(line, ":") {
				continue
			}
			field, value, found := strings.Cut(line, ":")
			if found {
				value = strings.TrimPrefix(value, " ")
			}
			switch field {
			case "data":
				data = append(data, value)
			case "event":
				eventType = value
			case "id", "retry": /* no resumption or automatic reconnect */
			default:
				return nil, invalid
			}
		}
		if scan.Err() != nil || !dispatch() {
			return nil, invalid
		}
	default:
		return nil, invalid
	}
	var result []byte
	for _, message := range messages {
		var envelope mcpEnvelope
		if strictJSON(bytes.NewReader(message), &envelope) != nil || envelope.JSONRPC != "2.0" {
			return nil, invalid
		}
		if len(envelope.ID) == 0 && envelope.Method != "" {
			if envelope.Method != "notifications/progress" && envelope.Method != "notifications/message" || len(envelope.Result) > 0 || len(envelope.Error) > 0 {
				return nil, invalid
			}
			continue
		}
		var responseID string
		if json.Unmarshal(envelope.ID, &responseID) != nil || responseID != requestID || envelope.Method != "" || len(envelope.Params) > 0 || len(envelope.Error) > 0 || len(envelope.Result) == 0 || result != nil {
			return nil, invalid
		}
		result = message
	}
	if result == nil {
		return nil, invalid
	}
	return result, nil
}

func mcpMeta() map[string]any {
	return map[string]any{"io.modelcontextprotocol/protocolVersion": MCPVersion, "io.modelcontextprotocol/clientInfo": map[string]string{"name": "scarlet-ai-security-agent", "version": RuntimeProtocolVersion}, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}
}

func mcpResultBody(raw []byte, headers http.Header, requestID string) ([]byte, error) {
	message, err := mcpResponseMessage(raw, headers, requestID)
	if err != nil {
		return nil, err
	}
	var envelope mcpEnvelope
	_ = json.Unmarshal(message, &envelope)
	type textContent struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	var payload struct {
		ResultType        string          `json:"resultType"`
		Content           []textContent   `json:"content,omitempty"`
		StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
		IsError           bool            `json:"isError,omitempty"`
		Meta              json.RawMessage `json:"_meta,omitempty"`
	}
	if strictJSON(bytes.NewReader(envelope.Result), &payload) != nil || payload.ResultType != "complete" || payload.IsError {
		return nil, errors.New("unsupported MCP tool result")
	}
	for _, part := range payload.Content {
		if part.Type != "text" {
			return nil, errors.New("unsupported MCP content")
		}
	}
	return message, nil
}
