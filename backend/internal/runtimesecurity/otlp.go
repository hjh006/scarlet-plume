package runtimesecurity

import (
	"errors"
	"math"
	"slices"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

const (
	MaxOTLPBytes = 1 << 20
	MaxOTLPSpans = 2048
	maxOTLPDepth = 64
)

var (
	ErrInvalidOTLP  = errors.New("invalid OTLP trace request")
	ErrOTLPTooLarge = errors.New("OTLP trace request exceeds the size limit")
)

type otlpSpanID struct {
	trace [16]byte
	span  [8]byte
}

type otlpSpan struct {
	id     otlpSpanID
	parent otlpSpanID
	span   *tracepb.Span
}

func readOTLPSpans(payload []byte) ([]otlpSpan, error) {
	if len(payload) > MaxOTLPBytes {
		return nil, ErrOTLPTooLarge
	}
	// TracesData uses the same field-1 ResourceSpans wire format as
	// ExportTraceServiceRequest without importing the generated gRPC services.
	var request tracepb.TracesData
	if err := (proto.UnmarshalOptions{DiscardUnknown: true, RecursionLimit: 32}).Unmarshal(payload, &request); err != nil {
		return nil, ErrInvalidOTLP
	}
	spans := make([]otlpSpan, 0)
	var received int
	for _, resource := range request.GetResourceSpans() {
		for _, scope := range resource.GetScopeSpans() {
			spanBatch := scope.GetSpans()
			received += len(spanBatch)
			if received > MaxOTLPSpans {
				return nil, ErrOTLPTooLarge
			}
			for _, span := range spanBatch {
				traceID, spanID := span.GetTraceId(), span.GetSpanId()
				if len(traceID) != 16 || len(spanID) != 8 {
					continue
				}
				id := otlpSpanID{trace: [16]byte(traceID), span: [8]byte(spanID)}
				if id.trace == [16]byte{} || id.span == [8]byte{} {
					continue
				}
				parent := otlpSpanID{trace: id.trace}
				if parentID := span.GetParentSpanId(); len(parentID) == 8 {
					parent.span = [8]byte(parentID)
				}
				spans = append(spans, otlpSpan{id: id, parent: parent, span: span})
			}
		}
	}
	return spans, nil
}

// otlpAttribute accepts one unambiguous value for the preferred attribute.
// A legacy name is considered only if the preferred attribute is absent.
func otlpAttribute(attrs []*commonpb.KeyValue, preferred, legacy string) *commonpb.AnyValue {
	var current, old *commonpb.AnyValue
	currentCount, oldCount := 0, 0
	for _, attr := range attrs {
		switch key := attr.GetKey(); {
		case key == preferred:
			current, currentCount = attr.GetValue(), currentCount+1
		case legacy != "" && key == legacy:
			old, oldCount = attr.GetValue(), oldCount+1
		}
	}
	if currentCount == 1 {
		return current
	}
	if currentCount == 0 && oldCount == 1 {
		return old
	}
	return nil
}

// ParseOTLP normalizes an OTLP/HTTP binary trace request into a limited set of
// server observations. Database client spans are linked only through known
// parents in this request. Trace identifiers and span payloads are discarded.
func ParseOTLP(payload []byte) ([]Observation, error) {
	spans, err := readOTLPSpans(payload)
	if err != nil {
		return nil, err
	}
	graph := make(map[otlpSpanID]*otlpSpan, len(spans))
	for i := range spans {
		node := &spans[i]
		if _, duplicate := graph[node.id]; duplicate {
			// An ambiguous identifier cannot establish ancestry or count twice.
			graph[node.id] = nil
		} else {
			graph[node.id] = node
		}
	}
	observations := make([]Observation, 0)
	servers := make(map[otlpSpanID]int)
	for i := range spans {
		node := &spans[i]
		if graph[node.id] != node || node.span.GetKind() != tracepb.Span_SPAN_KIND_SERVER {
			continue
		}
		end := node.span.GetEndTimeUnixNano()
		if end == 0 || end > math.MaxInt64 || end < node.span.GetStartTimeUnixNano() {
			continue
		}
		attrs := node.span.GetAttributes()
		status := otlpAttribute(attrs, "http.response.status_code", "http.status_code").GetIntValue()
		if status < 100 || status > 599 {
			continue
		}
		method := otlpAttribute(attrs, "http.request.method", "http.method").GetStringValue()
		observation, err := NormalizeObservation(Observation{
			Method:     method,
			Route:      otlpAttribute(attrs, "http.route", "").GetStringValue(),
			Status:     int(status),
			ObservedAt: time.Unix(0, int64(end)).UTC(),
		})
		if err != nil || observation.Method != method {
			continue
		}
		if len(observations) == MaxObservations {
			return nil, ErrOTLPTooLarge
		}
		servers[node.id] = len(observations)
		observations = append(observations, observation)
	}

	for i := range spans {
		node := &spans[i]
		if graph[node.id] != node || node.span.GetKind() != tracepb.Span_SPAN_KIND_CLIENT {
			continue
		}
		system := otlpAttribute(node.span.GetAttributes(), "db.system.name", "db.system").GetStringValue()
		if system == "" {
			continue
		}
		index, ok := nearestOTLPServer(graph, servers, node.parent)
		if !ok {
			continue
		}
		observation := &observations[index]
		if !slices.Contains(observation.DatabaseSystems, system) {
			observation.DatabaseSystems = append(observation.DatabaseSystems, system)
		}
	}
	for i, observation := range observations {
		normalized, err := NormalizeObservation(observation)
		if err != nil {
			// A database attribute cannot invalidate a valid server observation.
			observation.DatabaseSystems = nil
			observations[i] = observation
			continue
		}
		observations[i] = normalized
	}
	return observations, nil
}

func nearestOTLPServer(graph map[otlpSpanID]*otlpSpan, servers map[otlpSpanID]int, parent otlpSpanID) (int, bool) {
	for depth := 0; depth < maxOTLPDepth; depth++ {
		node := graph[parent]
		if node == nil {
			break
		}
		if node.span.GetKind() == tracepb.Span_SPAN_KIND_SERVER {
			// Stop even when the nearest server lacks an eligible HTTP route.
			index, ok := servers[node.id]
			return index, ok
		}
		parent = node.parent
	}
	return 0, false
}
