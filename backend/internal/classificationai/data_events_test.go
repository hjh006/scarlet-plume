package classificationai

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func eventAssessmentResponse(protocol, text string) map[string]any {
	if protocol == "responses" {
		return map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]string{"type": "output_text", "text": text}}}}}
	}
	return map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": text}}}}
}

func syntheticEventFacts() []EventFact {
	return []EventFact{
		{EventID: 41, Chain: 1, Kind: "endpoint", Channel: "file_copy", Rule: "dlp:protected", Severity: "high", Enforcement: "blocked", Categories: []string{"email"}, SourceCurrent: true, Capture: "complete"},
		{EventID: 42, Chain: 1, Kind: "database", Channel: "database", Rule: "pgaudit:read", Severity: "low", Enforcement: "observed", Categories: []string{}, SourceCurrent: false, Capture: "partial"},
	}
}

func TestAssessEventsUsesExactProjectionAndSelectedTLSProtocol(t *testing.T) {
	facts := syntheticEventFacts()
	answer := EventAssessment{Summary: "Synthetic events require source review.", Severity: "medium", EvidenceIDs: []int64{41}, Uncertainties: []string{"Source-reported blocking is not independently verified."}, Recommendations: []string{"Review the authorized source records."}}
	for _, protocol := range []string{"chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int64
			_, manager, profile := testTLS(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.TLS == nil || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+fixtureAPIKey {
					t.Error("request did not use verified TLS with the selected profile")
				}
				raw, err := io.ReadAll(r.Body)
				var input map[string]json.RawMessage
				if err != nil || json.Unmarshal(raw, &input) != nil {
					t.Error("invalid request envelope")
					w.WriteHeader(400)
					return
				}
				if strings.Contains(string(raw), fixtureAPIKey) || input["tools"] != nil || input["tool_choice"] != nil || string(input["store"]) != "false" || string(input["stream"]) != "false" {
					t.Error("request body exposed a key or enabled storage, streaming or tools")
				}
				var model, projected, prompt string
				_ = json.Unmarshal(input["model"], &model)
				if model != "user/model-manually-selected" {
					t.Error("manually selected model was replaced")
				}
				if protocol == "responses" {
					if r.URL.Path != "/v1/responses" || string(input["max_output_tokens"]) != "1024" {
						t.Error("Responses endpoint or response budget changed")
					}
					_ = json.Unmarshal(input["input"], &projected)
					_ = json.Unmarshal(input["instructions"], &prompt)
				} else {
					var messages []struct{ Role, Content string }
					if json.Unmarshal(input["messages"], &messages) != nil || len(messages) != 2 || messages[0].Role != "system" || messages[1].Role != "user" {
						t.Error("event data was not separated from instructions")
						w.WriteHeader(400)
						return
					}
					if r.URL.Path != "/v1/chat/completions" || string(input["max_tokens"]) != "1024" {
						t.Error("Chat endpoint or response budget changed")
					}
					projected, prompt = messages[1].Content, messages[0].Content
				}
				want, _ := json.Marshal(facts)
				if projected != string(want) || !strings.Contains(prompt, "untrusted data") || !strings.Contains(prompt, "not a proven real person") || !strings.Contains(prompt, "No tools or actions") {
					t.Error("event projection or reference-only instructions changed")
				}
				var fields []map[string]json.RawMessage
				if json.Unmarshal([]byte(projected), &fields) != nil || len(fields) != len(facts) {
					t.Error("invalid projected event array")
				}
				allowed := map[string]bool{"eventId": true, "chain": true, "kind": true, "channel": true, "rule": true, "severity": true, "enforcement": true, "categories": true, "sourceCurrentAtSnapshot": true, "capture": true}
				for _, field := range fields {
					if len(field) != len(allowed) {
						t.Error("event projection contains additional source fields")
					}
					for name := range field {
						if !allowed[name] {
							t.Errorf("non-whitelisted event field %q sent to model", name)
						}
					}
				}
				for _, forbidden := range []string{"subjectRef", "deviceRef", "fileDigest", "databaseRef", "objectRef", "statementRef", "destinationDomain", "sql", "prompt", "path", "body", "records"} {
					if strings.Contains(projected, `"`+forbidden+`"`) {
						t.Error("private source context was sent to model")
					}
				}
				encoded, _ := json.Marshal(answer)
				_ = json.NewEncoder(w).Encode(eventAssessmentResponse(protocol, string(encoded)))
			})
			profile.Protocol = protocol
			got, err := manager.AssessEvents(t.Context(), profile, facts)
			if err != nil || !reflect.DeepEqual(got, answer) || calls.Load() != 1 {
				t.Fatalf("valid %s event reference rejected: %v", protocol, err)
			}
		})
	}
}

func TestAssessEventsRejectsUnsafeModelResponses(t *testing.T) {
	valid := `{"summary":"Synthetic reference only.","severity":"unknown","evidenceIds":[41],"uncertainties":[],"recommendations":[]}`
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, tc := range []struct {
			name string
			text string
			tool bool
		}{
			{"unknown_event", strings.Replace(valid, "[41]", "[999]", 1), false},
			{"duplicate_event", strings.Replace(valid, "[41]", "[41,41]", 1), false},
			{"unknown_field", strings.TrimSuffix(valid, "}") + `,"execute":"block_all"}`, false},
			{"long_summary", strings.Replace(valid, "Synthetic reference only.", strings.Repeat("字", 601), 1), false},
			{"long_advice", strings.Replace(valid, `"recommendations":[]`, `"recommendations":["`+strings.Repeat("字", 201)+`"]`, 1), false},
			{"oversize_response", strings.Replace(valid, "Synthetic reference only.", strings.Repeat("x", 8193), 1), false},
			{"secret", strings.Replace(valid, "Synthetic reference only.", fixtureAPIKey, 1), false},
			{"trailing", valid + " {}", false},
			{"tool_output", valid, true},
		} {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				_, manager, profile := testTLS(t, func(w http.ResponseWriter, r *http.Request) {
					response := eventAssessmentResponse(protocol, tc.text)
					if tc.tool {
						if protocol == "responses" {
							response["output"] = []any{map[string]any{"type": "function_call", "name": "block_all", "arguments": "{}", "call_id": "synthetic"}}
						} else {
							response["choices"] = []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"role": "tool", "content": tc.text}}}
						}
					}
					_ = json.NewEncoder(w).Encode(response)
				})
				profile.Protocol = protocol
				_, err := manager.AssessEvents(t.Context(), profile, syntheticEventFacts())
				if !errors.Is(err, ErrResponse) || strings.Contains(err.Error(), fixtureAPIKey) || strings.Contains(err.Error(), tc.text) {
					t.Fatal("unsafe model output accepted or exposed in error")
				}
			})
		}
	}
}
