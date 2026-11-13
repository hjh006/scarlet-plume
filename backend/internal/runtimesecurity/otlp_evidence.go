package runtimesecurity

import (
	"bytes"
	"encoding/json"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"io"
)

func otlpRuntimeEvidence(attrs []*commonpb.KeyValue, o *Observation) bool {
	copy := *o
	for _, field := range []struct {
		key    string
		target *string
	}{{"scarlet.client.ip", &copy.ClientIP}, {"scarlet.transport", &copy.Transport}, {"scarlet.transport.hop", &copy.TransportHop}} {
		if !hasOTLPAttribute(attrs, field.key) {
			continue
		}
		v := otlpAttribute(attrs, field.key, "")
		if _, ok := v.GetValue().(*commonpb.AnyValue_StringValue); !ok {
			return false
		}
		*field.target = v.GetStringValue()
	}
	for _, field := range []struct {
		key    string
		target any
	}{{"scarlet.evidence.context", &copy.Context}, {"scarlet.value.fingerprints", &copy.ValueFingerprints}} {
		if !hasOTLPAttribute(attrs, field.key) {
			continue
		}
		raw := otlpAttribute(attrs, field.key, "").GetStringValue()
		if len(raw) == 0 || len(raw) > 16<<10 {
			return false
		}
		d := json.NewDecoder(bytes.NewBufferString(raw))
		d.DisallowUnknownFields()
		if d.Decode(field.target) != nil || d.Decode(new(any)) != io.EOF {
			return false
		}
	}
	if normalizeRuntimeEvidence(&copy) != nil {
		return false
	}
	o.ClientIP, o.Transport, o.TransportHop, o.Context, o.ValueFingerprints = copy.ClientIP, copy.Transport, copy.TransportHop, copy.Context, copy.ValueFingerprints
	return true
}
