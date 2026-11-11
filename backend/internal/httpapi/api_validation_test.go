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
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAPIValidationHTTPAuthScopesAndMetadataOnlyResults(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := store.New(pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("validation-http-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Probe HTTP"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reviewer", "unused"))
	owner, err := repo.FindUser(ctx, slug)
	must(err)
	reviewer, err := repo.FindUser(ctx, slug+"-reviewer")
	must(err)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=$1`, ws.ID)
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE username=ANY($1)`, []string{slug, slug + "-reviewer"})
	})
	session := func(id int64) string {
		token, err := sessionToken()
		must(err)
		must(repo.CreateSession(ctx, id, sessionHash(token), time.Now().Add(time.Hour)))
		return token
	}
	own, review := session(owner.ID), session(reviewer.ID)
	token := runtimeTokenPrefix + strings.Repeat("A", 43)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	source, err := repo.CreateOperationsSource(ctx, ws.ID, owner.ID, store.OperationsSourceInput{Name: "Approved API probe", Kind: "security_events", OwnerTeam: "Governance"}, digest)
	must(err)
	handler := New(repo, "https://console.invalid", true)
	base := "/api/v1/workspaces/" + slug + "/api-validation"
	call := func(method, path string, body any, cookie, bearer, origin string, status int) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(body)
		must(err)
		request := httptest.NewRequest(method, path, strings.NewReader(string(data)))
		request.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			request.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: cookie})
		}
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("%s %s got%d want%d %.300s", method, path, response.Code, status, response.Body.String())
		}
		return response
	}
	input := store.APIValidationPlanInput{SourceID: source.ID, Name: "<script>fixture</script>", Service: "Fixture API", Method: "GET", Route: "/objects/{id}", TargetOrigin: "http://127.0.0.1:8896", ManifestRef: "fixture-v1", Rules: []string{"bola"}, IntervalMinutes: 5}
	call("GET", base+"/plans", nil, "", "", "", 401)
	call("POST", base+"/plans", input, own, "", "https://other.invalid", 403)
	call("POST", base+"/plans", input, review, "", "https://console.invalid", 403)
	var plan store.APIValidationPlan
	must(json.Unmarshal(call("POST", base+"/plans", input, own, "", "https://console.invalid", 201).Body.Bytes(), &plan))
	path := fmt.Sprintf("%s/plans/%d", base, plan.ID)
	call("POST", path+"/approve", map[string]int64{"revision": plan.Revision}, own, "", "https://console.invalid", 403)
	call("POST", apiValidationClaimPath, map[string]int64{"sourceId": source.ID}, "", "wrong-token", "", 401)
	var noClaim struct {
		Claim *store.APIValidationClaim `json:"claim"`
	}
	must(json.Unmarshal(call("POST", apiValidationClaimPath, map[string]int64{"sourceId": source.ID}, "", token, "", 200).Body.Bytes(), &noClaim))
	if noClaim.Claim != nil {
		t.Fatal("HTTP leased draft")
	}
	call("POST", path+"/approve", map[string]int64{"revision": plan.Revision}, review, "", "https://console.invalid", 200)
	var issued struct {
		Claim *store.APIValidationClaim `json:"claim"`
	}
	must(json.Unmarshal(call("POST", apiValidationClaimPath, map[string]int64{"sourceId": source.ID}, "", token, "", 200).Body.Bytes(), &issued))
	if issued.Claim == nil {
		t.Fatal("no approved lease")
	}
	result := store.APIValidationResult{PlanID: plan.ID, Revision: plan.Revision, RunRef: issued.Claim.RunRef, Mode: "validation", ObservedAt: time.Now(), Results: []store.APIValidationRuleResult{{Rule: "bola", Outcome: "inconclusive", OwnerStatus: 200, OtherStatus: 200, Requests: 3, Gaps: []string{"comparison_missing"}}}}
	raw, _ := json.Marshal(result)
	var unsafe map[string]any
	must(json.Unmarshal(raw, &unsafe))
	unsafe["password"] = "never-upload-provided-secrets"
	call("POST", apiValidationResultPath, unsafe, "", token, "", 400)
	call("POST", apiValidationResultPath, result, "", token, "", 200)
	call("GET", path+"/runs", nil, "", token, "", 401)
	runs := call("GET", path+"/runs", nil, review, "", "", 200)
	if !strings.Contains(runs.Body.String(), "comparison_missing") || strings.Contains(runs.Body.String(), "never-upload-provided-secrets") {
		t.Fatal("bounded result gap or metadata isolation lost")
	}
	report := call("GET", path+"/export?format=html", nil, own, "", "", 200)
	if strings.Contains(report.Body.String(), "<script>fixture</script>") || !strings.Contains(report.Body.String(), "&lt;script&gt;") {
		t.Fatal("report title not escaped")
	}
	report = call("GET", path+"/export?format=json", nil, review, "", "", 200)
	if !strings.Contains(report.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("JSON report not a download")
	}
	call("GET", "/api/v1/workspaces/no-such-tenant/api-validation/plans", nil, own, "", "", 404)
	_, err = pool.Exec(ctx, `UPDATE plume.operations_sources SET revoked_at=clock_timestamp()WHERE id=$1`, source.ID)
	must(err)
	call("POST", apiValidationClaimPath, map[string]int64{"sourceId": source.ID}, "", token, "", 401)
}
