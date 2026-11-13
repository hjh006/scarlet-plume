package store

import (
	"fmt"
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"strings"
	"testing"
	"time"
)

func TestExtendedRuntimeBaselineSignalsRequireActualDimensions(t *testing.T) {
	now := time.Now().UTC()
	subject := strings.Repeat("a", 64)
	volume := int64(250)
	original := runtimesecurity.Observation{Method: "POST", Route: "/exports", Status: 200, ObservedAt: now, SubjectRef: subject, ClientIP: "192.0.2.1", Context: &runtimesecurity.RuntimeContext{ClientCountry: "US", DestinationCountry: "US", DestinationDomain: "external.example", Recipient: "unregistered-recipient", Volume: &volume, VolumeUnit: "records"}}
	cases := []struct {
		rule      string
		configure func(*RuntimeDetectionPolicy)
		positive  int64
	}{
		{"client_region_mismatch", func(p *RuntimeDetectionPolicy) { p.AllowedClientCountries = []string{"CN"} }, 1},
		{"recipient_scope", func(p *RuntimeDetectionPolicy) { p.AllowedRecipients = []string{"approved-recipient"} }, 1},
		{"destination_scope", func(p *RuntimeDetectionPolicy) {
			p.AllowedCountries = []string{"CN"}
			p.AllowedDomains = []string{"approved.example"}
		}, 1},
		{"volume_limit", func(p *RuntimeDetectionPolicy) { p.MaxVolume = 200; p.VolumeUnit = "records" }, 250},
		{"subject_repeated_access", func(p *RuntimeDetectionPolicy) { p.WindowMinutes = 1440 }, 1},
	}
	for _, tc := range cases {
		t.Run(tc.rule, func(t *testing.T) {
			var p RuntimeDetectionPolicy
			for _, v := range defaultRuntimeDetectionPolicies() {
				if v.Rule == tc.rule {
					p = v
				}
			}
			p.Enabled = true
			p.Threshold = 1
			tc.configure(&p)
			if e := validateRuntimeDetectionPolicy(&p); e != nil {
				t.Fatal(e)
			}
			d := evaluateRuntimeDetection(p, []runtimesecurity.Observation{original}, runtimeRouteKey{"POST", runtimeRoute(original.Route)}, now, subject, false)
			if d.Matched != tc.positive || !runtimeDetectionPositive(d) {
				t.Fatal(d)
			}
			unknown := original
			unknown.Context = nil
			unknown.SubjectRef = ""
			d = evaluateRuntimeDetection(p, []runtimesecurity.Observation{unknown}, runtimeRouteKey{"POST", runtimeRoute(original.Route)}, now, subject, false)
			if runtimeDetectionPositive(d) {
				t.Fatal("missing labels/volume/subject invented positive")
			}
		})
	}
	for _, rule := range []string{"subject_multi_ip", "subject_multi_region"} {
		t.Run(rule, func(t *testing.T) {
			var p RuntimeDetectionPolicy
			for _, v := range defaultRuntimeDetectionPolicies() {
				if v.Rule == rule {
					p = v
				}
			}
			p.Enabled = true
			p.Threshold = 2
			other := original
			other.ClientIP = "192.0.2.2"
			ctx := *original.Context
			ctx.ClientCountry = "CN"
			other.Context = &ctx
			events := []runtimesecurity.Observation{original, original, other}
			d := evaluateRuntimeDetection(p, events, runtimeRouteKey{"POST", runtimeRoute(original.Route)}, now, subject, false)
			if d.Matched != 2 || d.Requests != 3 || !runtimeDetectionPositive(d) {
				t.Fatal("distinct count lost", d)
			}
			other.SubjectRef = strings.Repeat("b", 64)
			d = evaluateRuntimeDetection(p, []runtimesecurity.Observation{original, other}, runtimeRouteKey{"POST", runtimeRoute(original.Route)}, now, subject, false)
			if d.Matched != 1 || runtimeDetectionPositive(d) {
				t.Fatal("other account leaked into subject", d)
			}
		})
	}
	p := defaultRuntimeDetectionPolicies()[0]
	p.Rule = "destination_scope"
	p.AllowedCountries = []string{"cn"}
	if validateRuntimeDetectionPolicy(&p) == nil {
		t.Fatal("invalid country accepted")
	}
	p.AllowedCountries = []string{"CN"}
	p.AllowedDomains = []string{"https://example.test"}
	if validateRuntimeDetectionPolicy(&p) == nil {
		t.Fatal("URL is not a declared domain")
	}
	for _, ip := range []string{"192.0.2.1", "2001:db8::1"} {
		p.ClientIP = ip
		p.AllowedDomains = []string{"example.test"}
		if e := validateRuntimeDetectionPolicy(&p); e != nil {
			t.Fatal(fmt.Sprint(ip, e))
		}
	}
}
