package store

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

func extendedProofFixture(t *testing.T, rule string) (RuntimeDetectionPolicy, *RuntimeRetestScope, []runtimesecurity.Observation, runtimeRouteKey, time.Time, time.Time, time.Time) {
	t.Helper()
	var p RuntimeDetectionPolicy
	for _, candidate := range defaultRuntimeDetectionPolicies() {
		if candidate.Rule == rule {
			p = candidate
			break
		}
	}
	if p.Rule == "" {
		t.Fatal("unknown fixture rule", rule)
	}
	p.ID = 1
	p.Revision = 1
	p.ScopeKey = "proof-fixture"
	p.Enabled = true
	p.Threshold = 3
	p.MinRequests = 1
	p.WindowMinutes = 1
	p.AllowlistConfigured = true
	p.Direction = "request"
	p.AllowedCategories = []string{"phone"}
	p.AllowedClientCountries = []string{"CN"}
	p.AllowedCountries = []string{"CN"}
	p.AllowedDomains = []string{"approved.example"}
	p.AllowedRecipients = []string{"approved"}
	p.MaxVolume = 100
	p.VolumeUnit = "records"
	end := time.Now().Add(-time.Second).UTC()
	remediated := end.Add(-3 * time.Minute)
	positive := end.Add(-4 * time.Minute)
	o := runtimesecurity.Observation{Method: "POST", Route: "/exports/{id}", Status: 200, ObservedAt: end, SubjectRef: strings.Repeat("a", 64), ClientIP: "192.0.2.10", Transport: "https", TransportHop: "proxy_upstream", InspectedLocations: []string{"request", "response"}, DataFields: []runtimesecurity.DataField{{Name: "phone", Category: "phone", Location: "request", Method: "format", Count: 1, Observed: 1}}}
	volume := int64(10)
	o.Context = &runtimesecurity.RuntimeContext{ClientCountry: "CN", DestinationCountry: "CN", DestinationDomain: "approved.example", Recipient: "approved", Volume: &volume, VolumeUnit: "records"}
	positiveEvents := []runtimesecurity.Observation{}
	for i := 0; i < 3; i++ {
		v := cloneExtendedProofObservation(o)
		v.ObservedAt = positive
		switch rule {
		case "over_collection":
			v.DataFields = []runtimesecurity.DataField{{Name: "email", Category: "email", Location: "request", Method: "format", Count: 1, Observed: 1}}
		case "credential_cleartext_transport":
			v.Transport = "http"
			v.DataFields = []runtimesecurity.DataField{{Name: "password", Category: "credential", Location: "request", Method: "metadata", Count: 1, Observed: 1}}
		case "subject_multi_ip":
			v.ClientIP = fmt.Sprintf("192.0.2.%d", 10+i)
		case "subject_multi_region":
			v.Context.ClientCountry = []string{"US", "GB", "AU"}[i]
		case "client_region_mismatch":
			v.Context.ClientCountry = "US"
		case "recipient_scope":
			v.Context.Recipient = "unapproved"
		case "destination_scope":
			v.Context.DestinationCountry = "US"
			v.Context.DestinationDomain = "unapproved.example"
		case "volume_limit":
			amount := int64(250)
			v.Context.Volume = &amount
		}
		positiveEvents = append(positiveEvents, v)
	}
	if rule == "credential_cleartext_transport" {
		o.DataFields = []runtimesecurity.DataField{{Name: "password", Category: "credential", Location: "request", Method: "metadata", Count: 1, Observed: 1}}
	}
	key := runtimeRouteKey{method: o.Method, route: runtimeRoute(o.Route)}
	scope := captureRuntimeRetestScope(p, positiveEvents, key, "", positive.Add(-time.Second), positive.Add(time.Second))
	if !scope.Complete {
		t.Fatalf("fixture did not capture actual positive scope: %+v", scope)
	}
	anchor := cloneExtendedProofObservation(o)
	anchor.ObservedAt = end.Add(-90 * time.Second)
	return p, scope, []runtimesecurity.Observation{anchor, o}, key, end, remediated, positive
}
func cloneExtendedProofObservation(o runtimesecurity.Observation) runtimesecurity.Observation {
	o.CaptureWarnings = slices.Clone(o.CaptureWarnings)
	o.InspectedLocations = slices.Clone(o.InspectedLocations)
	o.DataFields = slices.Clone(o.DataFields)
	if o.Context != nil {
		c := *o.Context
		o.Context = &c
		if c.Volume != nil {
			volume := *c.Volume
			c.Volume = &volume
		}
	}
	return o
}
func assertExtendedProofUnavailable(t *testing.T, p RuntimeDetectionPolicy, scope *RuntimeRetestScope, events []runtimesecurity.Observation, key runtimeRouteKey, end, remediated, positive time.Time) {
	t.Helper()
	proof, reason := runtimeExtendedNegativeProof(p, scope, events, key, end, remediated, positive)
	if proof != nil || reason == "" {
		t.Fatalf("unavailable evidence yielded proof=%+v reason=%q", proof, reason)
	}
}
func TestRuntimeExtendedNegativeProofNineRulesAndMissingDimensions(t *testing.T) {
	for _, rule := range extendedRuntimeRules {
		t.Run(rule, func(t *testing.T) {
			p, scope, events, key, end, remediated, positive := extendedProofFixture(t, rule)
			proof, reason := runtimeExtendedNegativeProof(p, scope, events, key, end, remediated, positive)
			if proof == nil || reason != "" || proof.Requests != 1 || proof.Observed == 0 || proof.PolicyID != p.ID || proof.PolicyRevision != p.Revision || proof.ScopeKey != p.ScopeKey || proof.ScopeDigest != assessmentDigest(scope) || !proof.WindowStart.Equal(end.Add(-time.Minute)) {
				t.Fatalf("complete real dimensions rejected: %+v %q", proof, reason)
			}
			missing := cloneExtendedProofObservation(events[1])
			switch rule {
			case "over_collection":
				missing.InspectedLocations = nil
			case "credential_cleartext_transport":
				missing.Transport = ""
			case "subject_multi_ip":
				missing.ClientIP = ""
			case "subject_multi_region":
				missing.Context.ClientCountry = ""
			case "subject_repeated_access":
				missing.SubjectRef = ""
			case "client_region_mismatch":
				missing.Context = nil
			case "recipient_scope":
				missing.Context.Recipient = ""
			case "destination_scope":
				missing.Context.DestinationDomain = ""
			case "volume_limit":
				missing.Context.Volume = nil
			}
			assertExtendedProofUnavailable(t, p, scope, []runtimesecurity.Observation{events[0], missing}, key, end, remediated, positive)
			incomplete := cloneExtendedProofObservation(events[1])
			incomplete.CaptureWarnings = []string{"json_size_limit"}
			assertExtendedProofUnavailable(t, p, scope, []runtimesecurity.Observation{events[0], incomplete}, key, end, remediated, positive)
			noRequests := []runtimesecurity.Observation{events[0]}
			assertExtendedProofUnavailable(t, p, scope, noRequests, key, end, remediated, positive)
		})
	}
}
func TestRuntimeExtendedNegativeProofWindowVersionAndRetentionBounds(t *testing.T) {
	p, scope, events, key, end, remediated, positive := extendedProofFixture(t, "over_collection")
	for _, tc := range []struct {
		name   string
		change func(*RuntimeDetectionPolicy, **RuntimeRetestScope, *[]runtimesecurity.Observation, *time.Time, *time.Time, *time.Time)
	}{
		{"no-original-scope", func(_ *RuntimeDetectionPolicy, s **RuntimeRetestScope, _ *[]runtimesecurity.Observation, _, _, _ *time.Time) {
			*s = nil
		}},
		{"incomplete-original-scope", func(_ *RuntimeDetectionPolicy, s **RuntimeRetestScope, _ *[]runtimesecurity.Observation, _, _, _ *time.Time) {
			(*s).Complete = false
		}},
		{"new-policy-id", func(p *RuntimeDetectionPolicy, _ **RuntimeRetestScope, _ *[]runtimesecurity.Observation, _, _, _ *time.Time) {
			p.ID++
		}},
		{"new-policy-revision", func(p *RuntimeDetectionPolicy, _ **RuntimeRetestScope, _ *[]runtimesecurity.Observation, _, _, _ *time.Time) {
			p.Revision++
		}},
		{"no-window-anchor", func(_ *RuntimeDetectionPolicy, _ **RuntimeRetestScope, e *[]runtimesecurity.Observation, _, _, _ *time.Time) {
			*e = (*e)[1:]
		}},
		{"anchor-before-remediation", func(_ *RuntimeDetectionPolicy, _ **RuntimeRetestScope, e *[]runtimesecurity.Observation, _ *time.Time, r *time.Time, _ *time.Time) {
			(*e)[0].ObservedAt = r.Add(-time.Second)
		}},
		{"window-overlaps-remediation", func(_ *RuntimeDetectionPolicy, _ **RuntimeRetestScope, _ *[]runtimesecurity.Observation, e, r, _ *time.Time) {
			*r = e.Add(-30 * time.Second)
		}},
		{"window-overlaps-positive", func(_ *RuntimeDetectionPolicy, _ **RuntimeRetestScope, _ *[]runtimesecurity.Observation, e, _, positive *time.Time) {
			*positive = e.Add(-30 * time.Second)
		}},
		{"window-overlaps-policy-activation", func(p *RuntimeDetectionPolicy, _ **RuntimeRetestScope, _ *[]runtimesecurity.Observation, e, _, _ *time.Time) {
			at := e.Add(-30 * time.Second)
			p.CreatedAt = &at
		}},
		{"no-remediation", func(_ *RuntimeDetectionPolicy, _ **RuntimeRetestScope, _ *[]runtimesecurity.Observation, _, r, _ *time.Time) {
			*r = time.Time{}
		}},
		{"future-window", func(_ *RuntimeDetectionPolicy, _ **RuntimeRetestScope, _ *[]runtimesecurity.Observation, e, _, _ *time.Time) {
			*e = time.Now().Add(time.Minute)
		}},
		{"sample-budget", func(_ *RuntimeDetectionPolicy, _ **RuntimeRetestScope, e *[]runtimesecurity.Observation, _, _, _ *time.Time) {
			samples := make([]runtimesecurity.Observation, 20000)
			copy(samples, *e)
			*e = samples
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := p
			stored := *scope
			candidateScope := &stored
			samples := []runtimesecurity.Observation{cloneExtendedProofObservation(events[0]), cloneExtendedProofObservation(events[1])}
			at, rem, pos := end, remediated, positive
			tc.change(&candidate, &candidateScope, &samples, &at, &rem, &pos)
			assertExtendedProofUnavailable(t, candidate, candidateScope, samples, key, at, rem, pos)
		})
	}
	for _, scopeMutation := range []func(*RuntimeRetestScope){func(s *RuntimeRetestScope) { s.Version = 0 }, func(s *RuntimeRetestScope) { s.PolicyID++ }, func(s *RuntimeRetestScope) { s.PolicyRevision++ }} {
		stored := *scope
		scopeMutation(&stored)
		assertExtendedProofUnavailable(t, p, &stored, events, key, end, remediated, positive)
	}
}
func TestRuntimeExtendedNegativeProofOriginalContextCannotShrink(t *testing.T) {
	for _, rule := range []string{"over_collection", "credential_cleartext_transport", "client_region_mismatch", "recipient_scope", "destination_scope", "volume_limit"} {
		t.Run(rule, func(t *testing.T) {
			p, scope, events, key, end, remediated, positive := extendedProofFixture(t, rule)
			for _, dimension := range []string{"subject", "ip"} {
				t.Run(dimension, func(t *testing.T) {
					replacement := cloneExtendedProofObservation(events[1])
					if dimension == "subject" {
						replacement.SubjectRef = strings.Repeat("b", 64)
					} else {
						replacement.ClientIP = "192.0.2.99"
					}
					assertExtendedProofUnavailable(t, p, scope, []runtimesecurity.Observation{events[0], replacement}, key, end, remediated, positive)
				})
			}
		})
	}
	p, scope, events, key, end, remediated, positive := extendedProofFixture(t, "credential_cleartext_transport")
	for _, dimension := range []string{"hop", "location", "credentials", "capture", "plaintext"} {
		t.Run(dimension, func(t *testing.T) {
			replacement := cloneExtendedProofObservation(events[1])
			switch dimension {
			case "hop":
				replacement.TransportHop = "source_connection"
			case "location":
				replacement.DataFields[0].Location = "response"
			case "credentials":
				replacement.DataFields = nil
			case "capture":
				replacement.InspectedLocations = []string{"response"}
			case "plaintext":
				replacement.Transport = "http"
			}
			assertExtendedProofUnavailable(t, p, scope, []runtimesecurity.Observation{events[0], replacement}, key, end, remediated, positive)
		})
	}
}
func TestRuntimeExtendedNegativeProofAllTriggeringSubjectsAndPositiveInvalidation(t *testing.T) {
	for _, rule := range []string{"subject_multi_ip", "subject_multi_region", "subject_repeated_access"} {
		t.Run(rule, func(t *testing.T) {
			p, scope, events, key, end, remediated, positive := extendedProofFixture(t, rule)
			otherScope := *scope
			otherScope.Subjects = []string{strings.Repeat("b", 64)}
			both := mergeRuntimeRetestScope(scope, &otherScope)
			if len(both.Subjects) != 2 || len(scope.Subjects) != 1 {
				t.Fatal("subject merge mutated old scope or dropped trigger")
			}
			assertExtendedProofUnavailable(t, p, both, events, key, end, remediated, positive)
			b := cloneExtendedProofObservation(events[1])
			b.SubjectRef = otherScope.Subjects[0]
			complete := append(slices.Clone(events), b)
			proof, reason := runtimeExtendedNegativeProof(p, both, complete, key, end, remediated, positive)
			if proof == nil || reason != "" || len(proof.Subjects) != 2 {
				t.Fatalf("explicit fresh observations for both subjects rejected: %+v %s", proof, reason)
			}
			renewed := append(slices.Clone(complete), cloneExtendedProofObservation(b), cloneExtendedProofObservation(b))
			if rule == "subject_multi_ip" {
				renewed[len(renewed)-2].ClientIP = "192.0.2.20"
				renewed[len(renewed)-1].ClientIP = "192.0.2.21"
			}
			if rule == "subject_multi_region" {
				renewed[len(renewed)-2].Context.ClientCountry = "US"
				renewed[len(renewed)-1].Context.ClientCountry = "GB"
			}
			assertExtendedProofUnavailable(t, p, both, renewed, key, end, remediated, positive)
		})
	}
}
func TestRuntimeCaptureAndMergeRetestScopeRemainBoundedAndFailClosed(t *testing.T) {
	p, scope, events, key, end, _, positive := extendedProofFixture(t, "credential_cleartext_transport")
	hit := cloneExtendedProofObservation(events[1])
	hit.Transport = "http"
	hit.ObservedAt = positive
	hit.TransportHop = ""
	captured := captureRuntimeRetestScope(p, []runtimesecurity.Observation{hit}, key, "", positive.Add(-time.Second), end)
	if captured.Complete {
		t.Fatal("missing original hop was silently usable")
	}
	changed := *scope
	changed.PolicyRevision++
	if mergeRuntimeRetestScope(scope, &changed).Complete || mergeRuntimeRetestScope(scope, nil).Complete {
		t.Fatal("incomplete/version mismatch scope merged as complete")
	}
	p.Rule = "over_collection"
	hits := []runtimesecurity.Observation{}
	for i := 0; i < 65; i++ {
		o := cloneExtendedProofObservation(events[1])
		o.DataFields = []runtimesecurity.DataField{{Name: "email", Category: "email", Location: "request", Method: "format", Count: 1, Observed: 1}}
		o.ClientIP = fmt.Sprintf("192.0.2.%d", i+1)
		o.ObservedAt = positive
		hits = append(hits, o)
	}
	captured = captureRuntimeRetestScope(p, hits, key, "", positive.Add(-time.Second), end)
	if captured.Complete || len(captured.Contexts) != 64 {
		t.Fatal("context budget loss was hidden", captured)
	}
	p.Rule = "subject_repeated_access"
	for i := range hits {
		hits[i].SubjectRef = fmt.Sprintf("%064x", i+1)
	}
	captured = captureRuntimeRetestScope(p, hits, key, "", positive.Add(-time.Second), end)
	if captured.Complete || len(captured.Subjects) != 64 {
		t.Fatal("subject budget loss was hidden", captured)
	}
}

func TestRuntimeExtendedNegativeProofResidualSignalsAndVolumeTotals(t *testing.T) {
	for _, rule := range []string{"over_collection", "credential_cleartext_transport", "client_region_mismatch", "recipient_scope", "destination_scope"} {
		t.Run(rule, func(t *testing.T) {
			p, scope, events, key, end, remediated, positive := extendedProofFixture(t, rule)
			residual := cloneExtendedProofObservation(events[1])
			switch rule {
			case "over_collection":
				residual.DataFields = []runtimesecurity.DataField{{Name: "email", Location: "request", Category: "email", Method: "format", Count: 1, Observed: 1}}
			case "credential_cleartext_transport":
				residual.Transport = "http"
			case "client_region_mismatch":
				residual.Context.ClientCountry = "US"
			case "recipient_scope":
				residual.Context.Recipient = "unapproved"
			case "destination_scope":
				residual.Context.DestinationCountry = "US"
			}
			// A single residual signal is below the alert threshold of three, but the
			// explicit baseline deviation has not disappeared and cannot clear it.
			assertExtendedProofUnavailable(t, p, scope, []runtimesecurity.Observation{events[0], residual}, key, end, remediated, positive)
		})
	}
	p, scope, events, key, end, remediated, positive := extendedProofFixture(t, "volume_limit")
	first := cloneExtendedProofObservation(events[1])
	second := cloneExtendedProofObservation(events[1])
	a, b := int64(60), int64(40)
	first.Context.Volume = &a
	second.Context.Volume = &b
	proof, reason := runtimeExtendedNegativeProof(p, scope, []runtimesecurity.Observation{events[0], first, second}, key, end, remediated, positive)
	if proof == nil || reason != "" || proof.Requests != 2 || proof.Observed != 2 {
		t.Fatalf("explicit total at configured limit rejected: %+v %s", proof, reason)
	}
	b = 41
	assertExtendedProofUnavailable(t, p, scope, []runtimesecurity.Observation{events[0], first, second}, key, end, remediated, positive)
	second.Context.VolumeUnit = "bytes"
	assertExtendedProofUnavailable(t, p, scope, []runtimesecurity.Observation{events[0], first, second}, key, end, remediated, positive)
}
