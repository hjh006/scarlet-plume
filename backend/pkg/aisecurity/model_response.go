package aisecurity

import (
	"bytes"
	"encoding/json"
	"errors"
)

// normalizeModelOutput is opt-in to retain existing JSON clients. A normalized
// chat result contains only checked text, never raw provider extension fields.
func normalizeModelOutput(raw []byte, model Model) ([]byte, error) {
	if model.ResponseFormat != "openai_chat" {
		return raw, nil
	}
	var response struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Index   int `json:"index"`
			Message struct {
				Role    string  `json:"role"`
				Content *string `json:"content"`
				Refusal *string `json:"refusal,omitempty"`
			} `json:"message"`
			FinishReason string          `json:"finish_reason"`
			Logprobs     json.RawMessage `json:"logprobs,omitempty"`
		} `json:"choices"`
		Usage             *modelUsage `json:"usage,omitempty"`
		SystemFingerprint *string     `json:"system_fingerprint,omitempty"`
		ServiceTier       string      `json:"service_tier,omitempty"`
	}
	if strictJSON(bytes.NewReader(raw), &response) != nil || response.Object != "chat.completion" || len(response.Choices) != 1 || !response.Usage.valid() {
		return nil, errors.New("unsupported model response schema")
	}
	expected := model.UpstreamModel
	if expected == "" {
		expected = model.ID
	}
	choice := response.Choices[0]
	if response.Model != expected || choice.Index != 0 || choice.Message.Role != "assistant" || choice.Message.Content == nil || choice.Message.Refusal != nil && *choice.Message.Refusal != "" || choice.FinishReason != "stop" && choice.FinishReason != "length" {
		return nil, errors.New("model response selector or text completion invalid")
	}
	return json.Marshal(struct {
		Model        string `json:"model"`
		Output       string `json:"output"`
		FinishReason string `json:"finishReason"`
	}{response.Model, *choice.Message.Content, choice.FinishReason})
}
