package aisecurity

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

type modelUsage struct {
	PromptTokens     *int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens *int64 `json:"completion_tokens,omitempty"`
	TotalTokens      *int64 `json:"total_tokens,omitempty"`
}

func (u *modelUsage) valid() bool {
	if u == nil {
		return true
	}
	for _, n := range []*int64{u.PromptTokens, u.CompletionTokens, u.TotalTokens} {
		if n != nil && (*n < 0 || *n > 1_000_000_000) {
			return false
		}
	}
	return u.PromptTokens == nil || u.CompletionTokens == nil || u.TotalTokens == nil || *u.PromptTokens+*u.CompletionTokens == *u.TotalTokens
}

// assembleModelStream holds every byte until a complete, strictly typed SSE
// completion has been checked. Delta boundaries have no DLP significance.
func assembleModelStream(reader io.Reader, model Model, limit int, bifrost bool, fresh func() bool) ([]byte, error) {
	bad := errors.New("model stream incomplete or unsupported")
	expected := model.UpstreamModel
	if expected == "" {
		expected = model.ID
	}
	scanner := bufio.NewScanner(io.LimitReader(reader, int64(limit)*8+65537))
	scanner.Buffer(make([]byte, 4096), limit+65536)
	var text strings.Builder
	total, frames := 0, 0
	done, finished, hadChoice := false, false, false
	id := ""
	var usage *modelUsage
	var data []string
	finish := ""
	consume := func() error {
		if len(data) == 0 {
			return nil
		}
		raw := []byte(strings.Join(data, "\n"))
		data = nil
		frames++
		if frames > 4096 || !fresh() || done {
			return bad
		}
		if bytes.Equal(raw, []byte("[DONE]")) {
			if !finished || !hadChoice {
				return bad
			}
			done = true
			return nil
		}
		if bifrost {
			var err error
			raw, err = normalizeBifrostEnvelope(raw)
			if err != nil {
				return bad
			}
		}
		var chunk struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			Model   string `json:"model"`
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Role    string  `json:"role,omitempty"`
					Content *string `json:"content,omitempty"`
					Refusal *string `json:"refusal,omitempty"`
				} `json:"delta"`
				FinishReason *string         `json:"finish_reason"`
				Logprobs     json.RawMessage `json:"logprobs,omitempty"`
			} `json:"choices"`
			Usage             *modelUsage `json:"usage,omitempty"`
			SystemFingerprint *string     `json:"system_fingerprint,omitempty"`
			ServiceTier       string      `json:"service_tier,omitempty"`
		}
		if strictJSON(bytes.NewReader(raw), &chunk) != nil || chunk.Object != "chat.completion.chunk" || chunk.Model != expected || len(chunk.Choices) > 1 || !chunk.Usage.valid() {
			return bad
		}
		if id != "" && chunk.ID != "" && id != chunk.ID {
			return bad
		}
		if chunk.ID != "" {
			id = chunk.ID
		}
		if chunk.Usage != nil {
			if usage != nil {
				return bad
			}
			usage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			if !finished || chunk.Usage == nil {
				return bad
			}
			return nil
		}
		c := chunk.Choices[0]
		if finished || c.Index != 0 || c.Delta.Role != "" && c.Delta.Role != "assistant" || c.Delta.Refusal != nil && *c.Delta.Refusal != "" || len(c.Logprobs) > 0 && string(c.Logprobs) != "null" {
			return bad
		}
		hadChoice = true
		if c.Delta.Content != nil {
			text.WriteString(*c.Delta.Content)
			if text.Len() > limit {
				return bad
			}
		}
		if c.FinishReason != nil {
			if *c.FinishReason != "stop" && *c.FinishReason != "length" {
				return bad
			}
			finish = *c.FinishReason
			finished = true
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		if total > limit*8+65536 {
			return nil, bad
		}
		if line == "" {
			if err := consume(); err != nil {
				return nil, err
			}
			continue
		}
		if done {
			return nil, bad
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			return nil, bad
		}
		value := strings.TrimPrefix(line, "data:")
		value = strings.TrimPrefix(value, " ")
		data = append(data, value)
	}
	if scanner.Err() != nil || len(data) != 0 || !done || usage == nil || usage.TotalTokens == nil || !fresh() {
		return nil, bad
	}
	return json.Marshal(map[string]any{"object": "chat.completion", "model": expected, "choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": text.String()}, "finish_reason": finish}}, "usage": usage})
}
