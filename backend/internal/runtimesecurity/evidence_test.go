package runtimesecurity

import (
	"github.com/hjh006/scarlet-plume/backend/internal/valuefingerprint"
	"strings"
	"testing"
	"time"
)

func TestRuntimeEvidenceContractBoundsAndNoImplicitGeolocation(t *testing.T) {
	token, e := valuefingerprint.Create(strings.Repeat("11", 32), "key", "scope", "fixture@example.test")
	if e != nil {
		t.Fatal(e)
	}
	base := Observation{Method: "GET", Route: "/contacts", Status: 200, ObservedAt: time.Now(), ClientIP: "::ffff:192.0.2.1", Transport: "http", TransportHop: "proxy_upstream", DataFields: []DataField{{Name: "email", Location: "request", Category: "email", Method: "format", Count: 1, Observed: 1}}, ValueFingerprints: []ValueFingerprint{{KeyID: token.KeyID, Scope: token.Scope, Version: token.Version, Digest: token.Digest, Locator: "email", Category: "email"}}}
	normalized, e := NormalizeObservation(base)
	if e != nil || normalized.ClientIP != "192.0.2.1" || normalized.Context != nil {
		t.Fatalf("IP normalization/unknown location: %+v %v", normalized, e)
	}
	for _, change := range []func(*Observation){func(o *Observation) { o.ClientIP = "untrusted header" }, func(o *Observation) { o.Transport = "cleartext" }, func(o *Observation) { o.Context = &RuntimeContext{ClientCountry: "China"} }, func(o *Observation) {
		o.ValueFingerprints = append([]ValueFingerprint(nil), base.ValueFingerprints...)
		o.ValueFingerprints[0].Locator = "not-observed-field"
	}, func(o *Observation) {
		o.ValueFingerprints = append([]ValueFingerprint(nil), base.ValueFingerprints...)
		o.ValueFingerprints[0].Digest = "raw-value"
	}} {
		o := base
		change(&o)
		if _, e = NormalizeObservation(o); e == nil {
			t.Fatal("invalid evidence accepted")
		}
	}
	legacy := Observation{Method: "GET", Route: "/old", Status: 200, ObservedAt: time.Now()}
	o, e := NormalizeObservation(legacy)
	if e != nil || o.Transport != "" || o.ClientIP != "" || o.Context != nil {
		t.Fatal("legacy evidence invented dimensions")
	}
}
