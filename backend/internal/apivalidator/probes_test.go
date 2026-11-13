package apivalidator

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestApprovedAnonymousSensitiveAndHeaderProbes(t *testing.T) {
	t.Setenv("EXT_A_USER", "fixture-a")
	t.Setenv("EXT_A_PASSWORD", "provided-fixture-password")
	var mode atomic.Value
	mode.Store("vulnerable")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		current := mode.Load().(string)
		user, password, authenticated := r.BasicAuth()
		if authenticated && (user != "fixture-a" || password != "provided-fixture-password") {
			t.Error("unexpected supplied credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		if current != "vulnerable" {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-store")
		}
		if !authenticated && current == "fixed" {
			w.WriteHeader(403)
			return
		}
		if !authenticated && current == "generic" {
			_, _ = io.WriteString(w, `{"message":"generic 200 is not object proof"}`)
			return
		}
		email := "synthetic@example.invalid"
		if current == "no_control" && authenticated || current == "redacted" && !authenticated {
			email = ""
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "fixture-object", "owner": "fixture-owner", "email": email})
	}))
	defer server.Close()
	client, e := PrivateClient("")
	if e != nil {
		t.Fatal(e)
	}
	policy := store.APIValidationProbePolicy{AuthRequired: true, SensitiveFields: []store.APIValidationSensitiveField{{Path: "email", Category: "email"}}, ResponseHeaders: []store.APIValidationResponseHeader{{Name: "Cache-Control", Value: "no-store"}, {Name: "X-Content-Type-Options", Value: "nosniff"}}}
	rules := []string{"auth_required", "sensitive_exposure", "response_headers"}
	binding := Binding{Ref: "extended-v1", TargetOrigin: server.URL, Route: "/objects/{id}", Rules: rules, ObjectField: "id", OwnerField: "owner", AccountA: Account{UsernameEnv: "EXT_A_USER", PasswordEnv: "EXT_A_PASSWORD", ObjectPath: "/objects/fixture-object", ExpectedObject: "fixture-object", ExpectedOwner: "fixture-owner"}, ProbePolicy: policy}
	claim := store.APIValidationClaim{Mode: "validation", Plan: store.APIValidationPlan{ID: 1, APIValidationPlanInput: store.APIValidationPlanInput{Revision: 1, SourceID: 1, Method: "GET", Route: binding.Route, TargetOrigin: server.URL, ManifestRef: binding.Ref, Rules: rules, ProbePolicy: policy}}}
	tests := []struct {
		mode string
		want []string
	}{{"vulnerable", []string{"vulnerable", "vulnerable", "vulnerable"}}, {"fixed", []string{"not_observed", "not_observed", "not_observed"}}, {"generic", []string{"inconclusive", "inconclusive", "not_observed"}}, {"no_control", []string{"vulnerable", "inconclusive", "not_observed"}}, {"redacted", []string{"vulnerable", "not_observed", "not_observed"}}}
	for _, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			mode.Store(test.mode)
			result := Execute(t.Context(), client, Manifest{SourceID: 1, Bindings: []Binding{binding}}, claim)
			for i, r := range result.Results {
				if r.Outcome != test.want[i] {
					t.Fatalf("%s got%s want%s gaps%v", r.Rule, r.Outcome, test.want[i], r.Gaps)
				}
			}
			raw, _ := json.Marshal(result)
			for _, secret := range []string{"synthetic@example.invalid", "provided-fixture-password", "fixture-owner", "fixture-object"} {
				if strings.Contains(string(raw), secret) {
					t.Fatal("raw source value or credentials in receipt")
				}
			}
		})
	}
	claim.Mode = "connection"
	before := requests.Load()
	result := Execute(t.Context(), client, Manifest{SourceID: 1, Bindings: []Binding{binding}}, claim)
	for _, r := range result.Results {
		if r.Outcome != "inconclusive" || r.ComparisonComplete {
			t.Fatal("connection diagnosis became security verdict")
		}
	}
	if requests.Load()-before != 3 {
		t.Fatal("connection diagnosis sent anonymous probes")
	}
	// Changing a local policy after approval cannot silently change the baseline.
	changed := binding
	changed.ProbePolicy.ResponseHeaders = append([]store.APIValidationResponseHeader{}, binding.ProbePolicy.ResponseHeaders...)
	changed.ProbePolicy.ResponseHeaders[0].Value = "private"
	before = requests.Load()
	result = Execute(t.Context(), client, Manifest{SourceID: 1, Bindings: []Binding{changed}}, claim)
	if requests.Load() != before || result.Results[0].Gaps[0] != "manifest_mismatch" {
		t.Fatal("unapproved policy was probed")
	}
}
