package apivalidator

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestBOLAResponse200IsNotObjectProof(t *testing.T) {
	t.Setenv("PROBE_A_USER", "a")
	t.Setenv("PROBE_A_PASSWORD", "provided-a-secret")
	t.Setenv("PROBE_B_USER", "b")
	t.Setenv("PROBE_B_PASSWORD", "provided-b-secret")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		user, _, _ := r.BasicAuth()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/a") && user == "a" {
			_, _ = io.WriteString(w, `{"id":"a","owner":"a"}`)
		} else if strings.HasSuffix(r.URL.Path, "/b") {
			_, _ = io.WriteString(w, `{"id":"b","owner":"b"}`)
		} else {
			_, _ = io.WriteString(w, `{"message":"generic success page"}`)
		}
	}))
	defer server.Close()
	client, err := PrivateClient("")
	if err != nil {
		t.Fatal(err)
	}
	b := Binding{Ref: "local", TargetOrigin: server.URL, Route: "/objects/{id}", Rules: []string{"bola"}, ObjectField: "id", OwnerField: "owner", AccountA: Account{"PROBE_A_USER", "PROBE_A_PASSWORD", "/objects/a", "a", "a"}, AccountB: Account{"PROBE_B_USER", "PROBE_B_PASSWORD", "/objects/b", "b", "b"}}
	m := Manifest{SourceID: 1, Bindings: []Binding{b}}
	claim := store.APIValidationClaim{Mode: "validation", Plan: store.APIValidationPlan{ID: 1, APIValidationPlanInput: store.APIValidationPlanInput{SourceID: 1, Revision: 1, Method: "GET", Route: b.Route, TargetOrigin: b.TargetOrigin, ManifestRef: b.Ref, Rules: b.Rules}}}
	result := Execute(t.Context(), client, m, claim)
	if result.Results[0].Outcome != "inconclusive" || result.Results[0].SameObject || requests.Load() != 3 {
		t.Fatal("200 without exact same owned object became a vulnerability or clear")
	}
	t.Setenv("PROBE_B_USER", "a")
	before := requests.Load()
	result = Execute(t.Context(), client, m, claim)
	if requests.Load() != before || result.Results[0].Outcome != "inconclusive" {
		t.Fatal("one account masqueraded as two")
	}
	for _, bad := range []string{"https://else.invalid/objects/a", "/objects/a?next=private", "/objects/a/other", "/objects/%2fother", "/objects/../a"} {
		if MatchesRoute(b.Route, bad) {
			t.Fatal("unapproved source path matched")
		}
	}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://198.51.100.1/", nil)
	if _, err := client.Do(req); err == nil || !strings.Contains(err.Error(), "target_not_private") {
		t.Fatal("public destination was dialed")
	}
}
