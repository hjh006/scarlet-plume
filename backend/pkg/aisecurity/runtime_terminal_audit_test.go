package aisecurity

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeTerminalAuditKeepsFactsWhenCallerCancelsDuringReceipt(t *testing.T) {
	for _, outcome := range []string{"succeeded", "unknown", "blocked_output", "not_attempted"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type proofKey struct{}
			ctx = context.WithValue(ctx, proofKey{}, "original-execution")
			h := &runtimeHandler{options: RuntimeOptions{Now: time.Now, Audit: func(receipt context.Context, record AuditRecord) error {
				if record.Execution.Outcome != outcome || receipt.Value(proofKey{}) != "original-execution" {
					t.Fatal("terminal receipt lost its original facts or trusted context")
				}
				deadline, bounded := receipt.Deadline()
				if !bounded || time.Until(deadline) > 5*time.Second {
					t.Fatal("terminal reconciliation is not bounded")
				}
				cancel() // Cancellation races with an already-started receipt write.
				return receipt.Err()
			}}}
			err := h.audit(ctx, testRequest("model"), Decision{Outcome: "allow"}, "completed", Execution{Attempted: outcome != "not_attempted", Outcome: outcome})
			if err != nil || !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("caller cancellation discarded terminal facts", err)
			}
		})
	}
}

func TestRuntimePreparedAuditStillUsesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h := &runtimeHandler{options: RuntimeOptions{Now: time.Now, Audit: func(receipt context.Context, _ AuditRecord) error {
		cancel()
		return receipt.Err()
	}}}
	if err := h.audit(ctx, testRequest("model"), Decision{Outcome: "allow"}, "prepared", Execution{Outcome: "not_attempted"}); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-dispatch work continued after caller cancellation", err)
	}
}

func TestStrictHoldDisconnectAfterCompletionKeepsOneReceipt(t *testing.T) {
	for _, source := range []string{"provider", "cache"} {
		t.Run(source, func(t *testing.T) {
			const body = `{"model":"enterprise","messages":[{"role":"user","content":"request"}],"stream":true}`
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, sseCompletion("enterprise", []string{"checked reply"}, "data: [DONE]\n\n"))
			}))
			defer up.Close()
			cfg := fixture()
			cfg.Models[0].ResponseFormat = "openai_chat"
			var records []AuditRecord
			var disconnect context.CancelFunc
			runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return cfg, nil }, up.URL, func(_ context.Context, a AuditRecord) error {
				if a.Stage == "completed" && disconnect != nil {
					records = append(records, a)
					disconnect()
				}
				return nil
			}, nil)
			options := runtime.(*runtimeHandler).options
			if source == "cache" {
				options.Cache = &testModelCache{data: map[ModelCacheScope][]byte{}}
			}
			gateway, err := NewGateway(GatewayOptions{Runtime: options})
			if err != nil {
				t.Fatal(err)
			}
			if source == "cache" {
				warm := gatewayRequest(gateway, "/v1/chat/completions", aliceToken, body, "warm-disconnect-cache-001")
				if warm.Code != 200 || !strings.Contains(warm.Body.String(), "[DONE]") {
					t.Fatalf("cache warm-up failed: %d", warm.Code)
				}
			}
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			disconnect = stop
			in := httptest.NewRequestWithContext(ctx, "POST", "/v1/chat/completions", strings.NewReader(body))
			in.Header.Set("Content-Type", "application/json")
			in.Header.Set("Authorization", "Bearer "+aliceToken)
			out := httptest.NewRecorder()
			gateway.ServeHTTP(out, in)
			if out.Code != 403 || ctx.Err() != context.Canceled || strings.Contains(out.Body.String(), "checked reply") || strings.Contains(out.Body.String(), "[DONE]") {
				t.Fatalf("interrupted release emitted success: %d %s", out.Code, out.Body.String())
			}
			if len(records) != 1 || calls.Load() != 1 {
				t.Fatalf("want one terminal receipt and one provider call, got %d receipts and %d calls", len(records), calls.Load())
			}
			want := Execution{Attempted: true, Outcome: "succeeded", HTTPStatus: 200}
			if source == "cache" {
				want = Execution{CacheHit: true, Outcome: "not_attempted"}
			}
			if got := records[0].Execution; got != want {
				t.Fatalf("interruption changed the recorded execution: got %+v, want %+v", got, want)
			}
		})
	}
}
