package aisecurity

import (
	"context"
	"errors"
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
