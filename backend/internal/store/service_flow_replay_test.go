package store

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

func TestServiceFlowReplayIdentity(t *testing.T) {
	input := runtimesecurity.Observation{Method: "GET", Route: "/chain", Status: 200, ObservedAt: time.Now(), TraceRef: strings.Repeat("1", 32), SpanRef: strings.Repeat("2", 16), ParentSpanRef: strings.Repeat("3", 16), ServiceCalls: []runtimesecurity.ServiceCall{{TargetService: "B", SpanRef: strings.Repeat("4", 16)}, {TargetService: "C", SpanRef: strings.Repeat("5", 16)}}}
	_, want, _, err := prepareRuntimeBatch("otlp", []runtimesecurity.Observation{input})
	if err != nil {
		t.Fatal(err)
	}
	copy := input
	copy.ServiceCalls = slices.Clone(input.ServiceCalls)
	slices.Reverse(copy.ServiceCalls)
	copy.ServiceCalls = append(copy.ServiceCalls, copy.ServiceCalls[0])
	_, got, _, err := prepareRuntimeBatch("otlp", []runtimesecurity.Observation{copy})
	if err != nil || got != want {
		t.Fatal("equivalent call projection changed digest")
	}
	copy.ParentSpanRef = strings.Repeat("6", 16)
	_, different, _, err := prepareRuntimeBatch("otlp", []runtimesecurity.Observation{copy})
	if err != nil || different == want {
		t.Fatal("changed parent treated as same receipt")
	}
}
