package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/apivalidator"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAPIValidationApprovedExtendedProbesHTTP(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := t.Context()
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	repo := store.New(pool)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("extended-probes-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Extended probes"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reviewer", "unused"))
	owner, e := repo.FindUser(ctx, slug)
	must(e)
	reviewer, e := repo.FindUser(ctx, slug+"-reviewer")
	must(e)
	ws, e := repo.Membership(ctx, owner.ID, slug)
	must(e)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=$1`, ws.ID)
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE username=ANY($1)`, []string{slug, slug + "-reviewer"})
	})
	token := runtimeTokenPrefix + strings.Repeat("A", 43)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	source, e := repo.CreateOperationsSource(ctx, ws.ID, owner.ID, store.OperationsSourceInput{Name: "Extended probe source", Kind: "security_events", OwnerTeam: "Synthetic"}, digest)
	must(e)
	var fixed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, authenticated := r.BasicAuth()
		if fixed.Load() {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			if !authenticated {
				w.WriteHeader(403)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"owned-fixture","owner":"fixture-a","email":"controlled@example.invalid"}`))
	}))
	defer target.Close()
	t.Setenv("HTTP_PROBE_A_USER", "fixture-a")
	t.Setenv("HTTP_PROBE_A_PASSWORD", "provided-fixture-password")
	policy := store.APIValidationProbePolicy{AuthRequired: true, SensitiveFields: []store.APIValidationSensitiveField{{Path: "email", Category: "email"}}, ResponseHeaders: []store.APIValidationResponseHeader{{Name: "X-Content-Type-Options", Value: "nosniff"}}}
	input := store.APIValidationPlanInput{SourceID: source.ID, Name: "Three bounded GET checks", Service: "Extended synthetic API", Method: "GET", Route: "/objects/{id}", TargetOrigin: target.URL, ManifestRef: "extended-fixture-v1", Rules: []string{"auth_required", "sensitive_exposure", "response_headers"}, ProbePolicy: policy, IntervalMinutes: 5}
	session := func(id int64) string {
		v, e := sessionToken()
		must(e)
		must(repo.CreateSession(ctx, id, sessionHash(v), time.Now().Add(time.Hour)))
		return v
	}
	own, review := session(owner.ID), session(reviewer.ID)
	handler := New(repo, "https://console.invalid", true)
	base := "/api/v1/workspaces/" + slug + "/api-validation"
	call := func(method, path string, body any, cookie, bearer string, status int) *httptest.ResponseRecorder {
		t.Helper()
		raw, e := json.Marshal(body)
		must(e)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: cookie})
			r.Header.Set("Origin", "https://console.invalid")
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s got%d want%d %.400s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	var plan store.APIValidationPlan
	must(json.Unmarshal(call("POST", base+"/plans", input, own, "", 201).Body.Bytes(), &plan))
	path := fmt.Sprintf("%s/plans/%d", base, plan.ID)
	call("POST", path+"/approve", map[string]int64{"revision": plan.Revision}, own, "", 403)
	call("POST", path+"/approve", map[string]int64{"revision": plan.Revision}, review, "", 200)
	getClaim := func(mode string) *store.APIValidationClaim {
		call("POST", path+"/request", map[string]any{"revision": plan.Revision, "mode": mode}, own, "", 200)
		var v struct {
			Claim *store.APIValidationClaim `json:"claim"`
		}
		must(json.Unmarshal(call("POST", apiValidationClaimPath, map[string]int64{"sourceId": source.ID}, "", token, 200).Body.Bytes(), &v))
		if v.Claim == nil {
			t.Fatal("approved task not leased")
		}
		return v.Claim
	}
	b := apivalidator.Binding{Ref: input.ManifestRef, TargetOrigin: target.URL, Route: input.Route, Rules: input.Rules, ProbePolicy: policy, ObjectField: "id", OwnerField: "owner", AccountA: apivalidator.Account{UsernameEnv: "HTTP_PROBE_A_USER", PasswordEnv: "HTTP_PROBE_A_PASSWORD", ObjectPath: "/objects/owned-fixture", ExpectedObject: "owned-fixture", ExpectedOwner: "fixture-a"}}
	manifest := apivalidator.Manifest{SourceID: source.ID, Bindings: []apivalidator.Binding{b}}
	client, e := apivalidator.PrivateClient("")
	must(e)
	positive := apivalidator.Execute(ctx, client, manifest, *getClaim("validation"))
	for _, r := range positive.Results {
		if r.Outcome != "vulnerable" {
			t.Fatalf("missing positive proof %s %v", r.Rule, r.Gaps)
		}
	}
	// Server reconstructs the verdict and checks the approved policy cardinality.
	forged := positive
	forged.Results = append([]store.APIValidationRuleResult{}, positive.Results...)
	for i := range forged.Results {
		if forged.Results[i].Rule == "response_headers" {
			forged.Results[i].HeaderChecked = 0
		}
	}
	call("POST", apiValidationResultPath, forged, "", token, 400)
	var accepted struct {
		Run      store.APIValidationRun `json:"run"`
		Replayed bool                   `json:"replayed"`
	}
	must(json.Unmarshal(call("POST", apiValidationResultPath, positive, "", token, 200).Body.Bytes(), &accepted))
	if len(accepted.Run.CheckIDs) != 3 {
		t.Fatal("new rule findings not projected to actual checks")
	}
	originalChecks := accepted.Run.CheckIDs
	for _, id := range originalChecks {
		var at time.Time
		must(pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at))
		must(repo.AddEvidence(ctx, ws.ID, owner.ID, id, store.EvidenceInput{Kind: "remediation", SourceSystem: "Synthetic target", SourceRecord: "fixed-local-fixture", Summary: "Updated explicit auth, redaction and response baseline", ObservedAt: at}))
	}
	fixed.Store(true)
	connection := apivalidator.Execute(ctx, client, manifest, *getClaim("connection"))
	must(json.Unmarshal(call("POST", apiValidationResultPath, connection, "", token, 200).Body.Bytes(), &accepted))
	if len(accepted.Run.EventIDs) != 0 {
		t.Fatal("connection test cleared a finding")
	}
	negative := apivalidator.Execute(ctx, client, manifest, *getClaim("validation"))
	for _, r := range negative.Results {
		if r.Outcome != "not_observed" {
			t.Fatalf("fixed control not proven %s %v", r.Rule, r.Gaps)
		}
	}
	must(json.Unmarshal(call("POST", apiValidationResultPath, negative, "", token, 200).Body.Bytes(), &accepted))
	if len(accepted.Run.CheckIDs) != 3 {
		t.Fatal("fresh same-policy result did not retest original checks")
	}
	for _, id := range originalChecks {
		must(repo.SubmitCheck(ctx, ws.ID, owner.ID, id))
		detail, e := repo.CheckDetail(ctx, ws.ID, reviewer.ID, id)
		must(e)
		if !detail.CanVerify {
			t.Fatal("new-rule native retest proof not available to independent reviewer")
		}
		must(repo.ReviewCheck(ctx, ws.ID, reviewer.ID, id, detail.ReviewRevision, "verified", "Reviewed the bounded new source result"))
	}
	exported := call("GET", path+"/export?format=html", nil, review, "", 200).Body.String()
	if !strings.Contains(exported, "sensitive_exposure") || strings.Contains(exported, "controlled@example.invalid") || strings.Contains(exported, "provided-fixture-password") {
		t.Fatal("report missing bounded evidence or leaked source values")
	}
	old := getClaim("validation")
	input.Revision = plan.Revision
	input.ProbePolicy.ResponseHeaders = []store.APIValidationResponseHeader{{Name: "Cache-Control", Value: "no-store"}}
	var changed store.APIValidationPlan
	must(json.Unmarshal(call("PUT", path, input, own, "", 200).Body.Bytes(), &changed))
	if changed.State != "draft" || changed.Revision != plan.Revision+1 {
		t.Fatal("policy edit retained previous approval")
	}
	stale := apivalidator.Execute(ctx, client, manifest, *old)
	call("POST", apiValidationResultPath, stale, "", token, 409)
}
