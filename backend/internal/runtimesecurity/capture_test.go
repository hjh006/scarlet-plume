package runtimesecurity

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestCaptureNormalizationAndSourceJSON(t *testing.T) {
	input := Observation{Method: "GET", Route: "/contacts", Status: 200, ObservedAt: time.Now(), TraceRef: strings.Repeat("1", 32), SpanRef: strings.Repeat("2", 16), SubjectRef: strings.Repeat("3", 64),
		DatabaseTargets: []DatabaseTarget{{System: "postgresql", Source: "Sample source", Database: "scarlet_sample", Schema: "inventory", Table: "classification_contacts", Operation: "select"}},
		DataFields:      []DataField{{Name: "contacts[].contact_value", Location: "response", Category: "email", Count: 2, Observed: 3, Method: "format"}, {Name: "contacts[].contact_value", Location: "response", Category: "email", Count: 2, Observed: 3, Method: "format"}}, CaptureWarnings: []string{"json_dynamic_keys", "json_depth_limit"}}
	before, _ := json.Marshal(input)
	got, err := NormalizeObservation(input)
	if err != nil || len(got.DataFields) != 1 || got.DatabaseTargets[0].Operation != "SELECT" {
		t.Fatalf("invalid normalized projection: %+v %v", got, err)
	}
	after, _ := json.Marshal(input)
	if string(before) != string(after) {
		t.Fatal("normalization mutated the caller's slices")
	}
	for _, mutate := range []func(*Observation){func(o *Observation) { o.SubjectRef = "raw-user@example.test" }, func(o *Observation) { o.TraceRef = "" }, func(o *Observation) { o.DataFields[0].Count = 4 }, func(o *Observation) { o.DatabaseTargets[0].Table = "SELECT * FROM private" }, func(o *Observation) { o.CaptureWarnings = []string{"business value"} }} {
		copy := got
		copy.DataFields = slices.Clone(got.DataFields)
		copy.DatabaseTargets = slices.Clone(got.DatabaseTargets)
		mutate(&copy)
		if _, err := NormalizeObservation(copy); err == nil {
			t.Fatal("invalid capture metadata was accepted")
		}
	}
	raw := []byte(`{"contacts":[{"contact_value":"alice@example.test"},{"contact_value":"bob@example.test"},{"contact_value":"none"}],"alice-private@example.test":"13900000000","token":"do-not-export-this-token","national_id":"11010519491231002X","card":"4111111111111111"}`)
	fields, warnings := InspectJSON(raw, "response")
	if !slices.Contains(warnings, "json_dynamic_keys") {
		t.Fatal("dynamic map keys were not marked as masked")
	}
	found := false
	for _, field := range fields {
		if field.Name == "contacts[].contact_value" && field.Category == "email" && field.Method == "format" {
			found = field.Count == 2 && field.Observed == 3
		}
	}
	encoded, _ := json.Marshal(fields)
	if !found || strings.Contains(string(encoded), "alice-private") || strings.Contains(string(encoded), "alice@example") || strings.Contains(string(encoded), "do-not-export") {
		t.Fatalf("unsafe or incomplete field projection: %s", encoded)
	}
	for value, category := range map[string]string{"11010519491231002X": "national_id", "4111111111111111": "financial_account", "13900000000": "phone", "a@example.test": "email"} {
		if !slices.Contains(FormatCategories(value), category) {
			t.Errorf("format checksum failed for %s", category)
		}
	}
	if slices.Contains(FormatCategories("110105194912310021"), "national_id") || slices.Contains(FormatCategories("8888888888888888"), "financial_account") {
		t.Fatal("invalid checksums became confirmed formats")
	}
	conflicting := got
	conflicting.DataFields = slices.Clone(got.DataFields)
	conflict := conflicting.DataFields[0]
	conflict.Count = 1
	conflicting.DataFields = append(conflicting.DataFields, conflict)
	if _, err := NormalizeObservation(conflicting); err == nil {
		t.Fatal("conflicting statistics for the same field were silently combined")
	}
	if _, warnings := InspectJSON(make([]byte, MaxJSONCaptureBytes+1), "response"); !slices.Contains(warnings, "json_size_limit") {
		t.Fatal("size bound missing")
	}
	if _, warnings := InspectJSON([]byte(strings.Repeat(`{"data":`, 10)+`1`+strings.Repeat("}", 10)), "response"); !slices.Contains(warnings, "json_depth_limit") {
		t.Fatal("depth bound missing")
	}
}

func TestOTLPControlledRelationships(t *testing.T) {
	stamp := time.Now()
	fields := `[{"name":"contacts[].contact_value","location":"response","category":"email","count":2,"observed":2,"method":"format"}]`
	server := otlpTestSpan(stamp, 1, 1, 0, tracepb.Span_SPAN_KIND_SERVER, otlpText("http.request.method", "GET"), otlpText("http.route", "/contacts"), otlpNumber("http.response.status_code", 200), otlpText("scarlet.subject.ref", strings.Repeat("a", 64)), otlpText("scarlet.data.fields", fields))
	dbattrs := []*commonpb.KeyValue{otlpText("db.system.name", "postgresql"), otlpText("db.namespace", "scarlet_sample"), otlpText("scarlet.data.source", "Sample source"), otlpText("scarlet.db.schema", "inventory"), otlpText("db.collection.name", "classification_contacts"), otlpText("db.operation.name", "SELECT"), otlpText("db.query.text", "SELECT private-value-never-export")}
	database := otlpTestSpan(stamp, 1, 2, 1, tracepb.Span_SPAN_KIND_CLIENT, dbattrs...)
	got, err := ParseOTLP(otlpTestPayload(t, []*tracepb.Span{server, database}))
	if err != nil || len(got) != 1 || len(got[0].DatabaseTargets) != 1 || got[0].SubjectRef != strings.Repeat("a", 64) || len(got[0].TraceRef) != 32 || len(got[0].SpanRef) != 16 || len(got[0].DataFields) != 1 {
		t.Fatalf("controlled relationship missing: %+v %v", got, err)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "private-value") {
		t.Fatal("SQL escaped into telemetry")
	}
	database.Attributes = append(database.Attributes, otlpText("db.namespace", "other_database"))
	server.Attributes = append(server.Attributes, otlpText("scarlet.subject.ref", strings.Repeat("b", 64)))
	got, err = ParseOTLP(otlpTestPayload(t, []*tracepb.Span{server, database}))
	if err != nil || len(got) != 1 || len(got[0].DatabaseTargets) != 0 || got[0].SubjectRef != "" || !slices.Contains(got[0].CaptureWarnings, "invalid_database_target") {
		t.Fatal("ambiguous attributes established a relationship")
	}
	if !reflect.DeepEqual(got[0].DatabaseSystems, []string{"postgresql"}) {
		t.Fatal("invalid optional metadata discarded the original database-family observation")
	}
}
