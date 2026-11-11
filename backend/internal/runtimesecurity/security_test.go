package runtimesecurity

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestSecurityProjectionValidationAndPrivacy(t *testing.T) {
	for _, c := range []struct{ kind, input string }{{"sql_injection", `q=private_value+UNION+SELECT+password+FROM+users`}, {"xss", `q=%3Cscript%3Eprivate_value%3C/script%3E`}, {"path_traversal", `q=..%2F..%2Fprivate_value`}} {
		t.Run(c.kind, func(t *testing.T) {
			signals := InspectSecurity([]byte(c.input))
			if !slices.ContainsFunc(signals, func(s SecuritySignal) bool { return s.Kind == c.kind && s.Count == 1 && s.Observed == 1 }) {
				t.Fatalf("missing pattern: %+v", signals)
			}
			raw, _ := json.Marshal(signals)
			if strings.Contains(string(raw), "private_value") {
				t.Fatal("raw request leaked")
			}
		})
	}
	if got := InspectSecurity(make([]byte, MaxJSONCaptureBytes+1)); got != nil {
		t.Fatal("oversized request was inspected")
	}
	o := Observation{Method: "GET", Route: "/safe", Status: 200, ObservedAt: time.Now(), AuthState: "anonymous", InspectedLocations: []string{"response", "request"}, SecuritySignals: []SecuritySignal{{Kind: "xss", Location: "request", Count: 1, Observed: 2}, {Kind: "sql_injection", Location: "request", Observed: 1}}}
	n, err := NormalizeObservation(o)
	if err != nil {
		t.Fatal(err)
	}
	if o.SecuritySignals[0].Kind != "xss" || o.InspectedLocations[0] != "response" {
		t.Fatal("normalization mutated caller")
	}
	n.SecuritySignals[0].Count = 99
	if o.SecuritySignals[1].Count != 0 {
		t.Fatal("normalization aliased caller")
	}
	for _, mutate := range []func(*Observation){func(o *Observation) { o.AuthState = "present" }, func(o *Observation) {
		o.SecuritySignals = []SecuritySignal{{Kind: "raw SQL", Location: "request", Count: 1, Observed: 1}}
	}, func(o *Observation) { o.SecuritySignals[0].Count = 3 }, func(o *Observation) { o.InspectedLocations = []string{"headers"} }} {
		candidate := o
		candidate.SecuritySignals = slices.Clone(o.SecuritySignals)
		mutate(&candidate)
		if _, err := NormalizeObservation(candidate); err == nil {
			t.Fatal("unsafe projection accepted")
		}
	}
	fields, _ := InspectJSON([]byte(`{"password":"","secret":"fixture-not-a-real-secret"}`), "response")
	if slices.ContainsFunc(fields, func(f DataField) bool { return f.Name == "password" && f.Count > 0 }) {
		t.Fatal("empty credential was counted as a nonempty signal")
	}
}

func TestOTLPSecurityProjectionUnambiguousAndOptional(t *testing.T) {
	stamp := time.Now()
	base := func(id byte, attrs ...*commonpb.KeyValue) *tracepb.Span {
		return otlpTestSpan(stamp, 1, id, 0, tracepb.Span_SPAN_KIND_SERVER, append([]*commonpb.KeyValue{otlpText("http.request.method", "GET"), otlpText("http.route", "/safe"), otlpNumber("http.response.status_code", 200)}, attrs...)...)
	}
	boolAttr := &commonpb.KeyValue{Key: "scarlet.auth.required", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: true}}}
	valid := base(1, otlpText("scarlet.auth.state", "anonymous"), boolAttr, otlpText("scarlet.security.signals", `[{"kind":"xss","location":"request","count":1,"observed":1}]`), otlpText("scarlet.inspected.locations", `["request"]`))
	ambiguous := base(2, otlpText("scarlet.auth.state", "anonymous"), otlpText("scarlet.auth.state", "authenticated"))
	wrongBool := base(3, otlpText("scarlet.auth.required", "true"))
	rawExtra := base(4, otlpText("scarlet.security.signals", `[{"kind":"xss","location":"request","count":1,"observed":1,"raw":"private-request-value"}]`))
	got, err := ParseOTLP(otlpTestPayload(t, []*tracepb.Span{valid, ambiguous, wrongBool, rawExtra}))
	if err != nil || len(got) != 4 {
		t.Fatalf("optional invalid projection dropped core HTTP telemetry: %v %+v", err, got)
	}
	if got[0].AuthState != "anonymous" || got[0].AuthRequired == nil || !*got[0].AuthRequired || len(got[0].SecuritySignals) != 1 {
		t.Fatalf("valid security projection lost: %+v", got[0])
	}
	for _, o := range got[1:] {
		if o.AuthState != "" || o.AuthRequired != nil || len(o.SecuritySignals) > 0 || !slices.Contains(o.CaptureWarnings, "invalid_security_context") {
			t.Fatalf("ambiguous security claim established: %+v", o)
		}
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "private-request-value") {
		t.Fatal("unexpected raw signal escaped")
	}
}
