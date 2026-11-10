package runtimesecurity

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestParseOTLP(t *testing.T) {
	stamp := time.Date(2026, 11, 10, 8, 0, 0, 123, time.UTC)
	server := func(trace, id, parent byte, method, route string, status int64) *tracepb.Span {
		return otlpTestSpan(stamp, trace, id, parent, tracepb.Span_SPAN_KIND_SERVER,
			otlpText("http.request.method", method), otlpText("http.route", route), otlpNumber("http.response.status_code", status))
	}
	database := func(trace, id, parent byte, system string) *tracepb.Span {
		return otlpTestSpan(stamp, trace, id, parent, tracepb.Span_SPAN_KIND_CLIENT, otlpText("db.system.name", system),
			otlpText("db.namespace", "secret_db_name"), otlpText("db.query.text", "SELECT 'private_query_value'"))
	}
	entry := server(1, 1, 0, "GET", "/v1/customers/{customerId}", 200)
	entry.Attributes = append(entry.Attributes,
		otlpText("http.method", "DELETE"), otlpNumber("http.status_code", 500),
		otlpText("url.full", "https://example.invalid/v1/customers/private_url_value?q=secret"),
		otlpText("http.request.header.authorization", "private_authorization_value"),
		otlpText("http.request.body.content", "private_body_value"),
	)
	entry.Name = "private_span_name"
	internal := otlpTestSpan(stamp, 1, 2, 1, tracepb.Span_SPAN_KIND_INTERNAL)
	nested := otlpTestSpan(stamp, 1, 4, 2, tracepb.Span_SPAN_KIND_SERVER,
		otlpText("http.method", "POST"), otlpText("http.route", "/v1/events"), otlpNumber("http.status_code", 503))
	unrouted := otlpTestSpan(stamp, 1, 7, 1, tracepb.Span_SPAN_KIND_SERVER,
		otlpText("http.request.method", "GET"), otlpText("url.path", "/v1/private_path"), otlpNumber("http.response.status_code", 200))
	// The input order and grouping differ from the span ancestry.
	payload := otlpTestPayload(t,
		[]*tracepb.Span{database(1, 3, 2, "postgresql"), database(1, 5, 4, "mysql"), database(1, 6, 1, "postgresql"), database(1, 8, 7, "sqlite")},
		[]*tracepb.Span{
			entry, internal, nested, unrouted,
			database(2, 9, 1, "mysql"),   // Same parent ID in another trace.
			database(1, 10, 99, "mysql"), // Missing parent.
			database(1, 11, 1, "private_database_system"),
			server(1, 12, 0, "GET", "/v1/users?token=private_query", 200),
			server(1, 13, 0, "CUSTOM", "/v1/users", 200),
			server(1, 14, 0, "GET", "/v1/users", 600),
			server(1, 15, 0, "GET", "/v1/users/13900000000", 200),
			server(1, 16, 0, "get", "/v1/users", 200),
		},
	)
	got, err := ParseOTLP(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d observations, want 2: %+v", len(got), got)
	}
	wantFirst, err := NormalizeObservation(Observation{Method: "GET", Route: "/v1/customers/{customerId}", Status: 200, ObservedAt: stamp, DatabaseSystems: []string{"postgresql"}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Method != wantFirst.Method || got[0].Route != wantFirst.Route || got[0].Status != wantFirst.Status ||
		!got[0].ObservedAt.Equal(stamp) || !slices.Equal(got[0].DatabaseSystems, []string{"postgresql"}) {
		t.Fatalf("unexpected primary observation: %+v", got[0])
	}
	if got[1].Method != "POST" || got[1].Route != "/v1/events" || got[1].Status != 503 ||
		!slices.Equal(got[1].DatabaseSystems, []string{"mysql"}) {
		t.Fatalf("unexpected nested observation: %+v", got[1])
	}
	projected, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(projected), "private_") || strings.Contains(string(projected), "secret_db_name") {
		t.Fatalf("non-allowlisted telemetry escaped into normalized observations: %s", projected)
	}
}

func TestParseOTLPRejectsAmbiguousIDsAndAttributes(t *testing.T) {
	now := time.Now()
	server := func(id byte, extra ...*commonpb.KeyValue) *tracepb.Span {
		attrs := []*commonpb.KeyValue{otlpText("http.request.method", "GET"), otlpText("http.route", "/v1/users"), otlpNumber("http.response.status_code", 200)}
		return otlpTestSpan(now, 1, id, 0, tracepb.Span_SPAN_KIND_SERVER, append(attrs, extra...)...)
	}
	payload := otlpTestPayload(t, []*tracepb.Span{
		server(1), server(1),
		server(2, otlpText("http.route", "/v1/admin")),
		server(3, otlpNumber("http.response.status_code", 502), otlpNumber("http.status_code", 200)),
		server(4),
		otlpTestSpan(now, 1, 5, 4, tracepb.Span_SPAN_KIND_CLIENT, otlpText("db.system", "postgresql")),
	})
	got, err := ParseOTLP(payload)
	if err != nil || len(got) != 1 || !slices.Equal(got[0].DatabaseSystems, []string{"postgresql"}) {
		t.Fatalf("got %+v with %v, want one legacy database link", got, err)
	}
}

func TestParseOTLPLimits(t *testing.T) {
	if got, err := ParseOTLP(nil); err != nil || len(got) != 0 {
		t.Fatalf("empty OTLP export should succeed: %+v, %v", got, err)
	}
	if _, err := ParseOTLP([]byte{0x0a, 0x02, 0xff}); !errors.Is(err, ErrInvalidOTLP) {
		t.Fatalf("expected malformed protobuf error, got %v", err)
	}
	if _, err := ParseOTLP(make([]byte, MaxOTLPBytes+1)); !errors.Is(err, ErrOTLPTooLarge) {
		t.Fatalf("expected payload size error, got %v", err)
	}
	if _, err := ParseOTLP(otlpTestPayload(t, make([]*tracepb.Span, MaxOTLPSpans+1))); !errors.Is(err, ErrOTLPTooLarge) {
		t.Fatalf("expected span count error, got %v", err)
	}

	now := time.Now()
	spans := make([]*tracepb.Span, MaxObservations+1)
	for i := range spans {
		spans[i] = otlpTestSpan(now, 1, byte(i+1), 0, tracepb.Span_SPAN_KIND_SERVER,
			otlpText("http.request.method", "GET"), otlpText("http.route", "/v1/users"), otlpNumber("http.response.status_code", 200))
	}
	if _, err := ParseOTLP(otlpTestPayload(t, spans)); !errors.Is(err, ErrOTLPTooLarge) {
		t.Fatalf("expected observation count error, got %v", err)
	}
}

func otlpTestSpan(stamp time.Time, trace, id, parent byte, kind tracepb.Span_SpanKind, attrs ...*commonpb.KeyValue) *tracepb.Span {
	traceID, spanID := [16]byte{trace}, [8]byte{id}
	span := &tracepb.Span{TraceId: traceID[:], SpanId: spanID[:], Kind: kind, EndTimeUnixNano: uint64(stamp.UnixNano()), Attributes: attrs}
	if parent != 0 {
		parentID := [8]byte{parent}
		span.ParentSpanId = parentID[:]
	}
	return span
}

func otlpText(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

func otlpNumber(key string, value int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: value}}}
}

func otlpTestPayload(t *testing.T, groups ...[]*tracepb.Span) []byte {
	t.Helper()
	request := &tracepb.TracesData{}
	for _, group := range groups {
		request.ResourceSpans = append(request.ResourceSpans, &tracepb.ResourceSpans{ScopeSpans: []*tracepb.ScopeSpans{{Spans: group}}})
	}
	payload, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
