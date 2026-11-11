package runtimesecurity

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestServiceCallNormalizationOwnsSlicesAndBounds(t *testing.T) {
	input := Observation{Method: "GET", Route: "/chain", Status: 200, ObservedAt: time.Now(), TraceRef: strings.Repeat("1", 32), SpanRef: strings.Repeat("2", 16), ParentSpanRef: strings.Repeat("3", 16), ServiceCalls: []ServiceCall{{TargetService: "Orders", SpanRef: strings.Repeat("b", 16)}, {TargetService: "Billing", SpanRef: strings.Repeat("a", 16)}, {TargetService: "Billing", SpanRef: strings.Repeat("a", 16)}}}
	before, _ := json.Marshal(input)
	got, err := NormalizeObservation(input)
	if err != nil || len(got.ServiceCalls) != 2 || got.ServiceCalls[0].TargetService != "Billing" {
		t.Fatalf("invalid deterministic projection: %+v %v", got, err)
	}
	after, _ := json.Marshal(input)
	if string(before) != string(after) {
		t.Fatal("caller slices mutated")
	}
	for name, mutate := range map[string]func(*Observation){"no_trace": func(o *Observation) { o.TraceRef = "" }, "no_span": func(o *Observation) { o.SpanRef = "" }, "zero_parent": func(o *Observation) { o.ParentSpanRef = strings.Repeat("0", 16) }, "self_parent": func(o *Observation) { o.ParentSpanRef = o.SpanRef }, "raw_parent": func(o *Observation) { o.ParentSpanRef = "raw-parent" }, "zero_client": func(o *Observation) { o.ServiceCalls[0].SpanRef = strings.Repeat("0", 16) }, "self_client": func(o *Observation) { o.ServiceCalls[0].SpanRef = o.SpanRef }, "conflicting_target": func(o *Observation) {
		o.ServiceCalls = append(o.ServiceCalls, ServiceCall{TargetService: "Other", SpanRef: o.ServiceCalls[0].SpanRef})
	}, "too_many": func(o *Observation) { o.ServiceCalls = make([]ServiceCall, 33) }, "unsafe_alias": func(o *Observation) { o.ServiceCalls[0].TargetService = "bad\x00alias" }} {
		t.Run(name, func(t *testing.T) {
			copy := input
			copy.ServiceCalls = slices.Clone(input.ServiceCalls)
			mutate(&copy)
			if _, err := NormalizeObservation(copy); err == nil {
				t.Fatal("invalid span/service metadata accepted")
			}
		})
	}
}
func TestOTLPHTTPClientAndRemoteParentProjection(t *testing.T) {
	now := time.Now()
	server := otlpTestSpan(now, 1, 1, 9, tracepb.Span_SPAN_KIND_SERVER, otlpText("http.request.method", "GET"), otlpText("http.route", "/chain"), otlpNumber("http.response.status_code", 200))
	client := otlpTestSpan(now, 1, 2, 1, tracepb.Span_SPAN_KIND_CLIENT, otlpText("http.request.method", "GET"), otlpNumber("http.response.status_code", 200), otlpText("peer.service", "Orders"), otlpText("url.full", "https://private.example/secret?q=value"), otlpText("http.request.body.content", "raw-body"))
	raw := otlpTestPayload(t, []*tracepb.Span{client, server})
	got, err := ParseOTLP(raw)
	if err != nil || len(got) != 1 || len(got[0].ServiceCalls) != 1 || got[0].ServiceCalls[0].TargetService != "Orders" || got[0].ServiceCalls[0].SpanRef != "0200000000000000" || got[0].ParentSpanRef != "0900000000000000" {
		t.Fatalf("native parent/client evidence lost: %+v %v", got, err)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "private.example") || strings.Contains(string(encoded), "raw-body") {
		t.Fatal("raw URL/body escaped")
	}
	client.ParentSpanId = []byte{8, 0, 0, 0, 0, 0, 0, 0}
	got, err = ParseOTLP(otlpTestPayload(t, []*tracepb.Span{client, server}))
	if err != nil || len(got[0].ServiceCalls) != 0 {
		t.Fatal("unrelated client span linked")
	}
	client.ParentSpanId = server.SpanId
	client.Attributes = append(client.Attributes, otlpText("peer.service", "Conflicting"))
	got, err = ParseOTLP(otlpTestPayload(t, []*tracepb.Span{client, server}))
	if err != nil || len(got[0].ServiceCalls) != 0 || !slices.Contains(got[0].CaptureWarnings, "invalid_service_call") {
		t.Fatal("ambiguous target established call")
	}
}
