package httpapi

import (
	"context"
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

func TestAssessmentHTTPWorkspaceAndWorkflowBoundaries(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
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
	slug := fmt.Sprintf("assessment-http-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Assessments HTTP"))
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
		t.Helper()
		token, e := sessionToken()
		must(e)
		must(repo.CreateSession(ctx, id, sessionHash(token), time.Now().Add(time.Hour)))
		return token
	}
	ownerSession, reviewSession := session(owner.ID), session(reviewer.ID)
	handler := New(repo, "https://console.invalid", true)
	base := "/api/v1/workspaces/" + slug + "/assessments"
	call := func(method, path string, body any, cookie, origin string, status int) *httptest.ResponseRecorder {
		t.Helper()
		data, e := json.Marshal(body)
		must(e)
		r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
		r.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: cookie})
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s got%d want%d %.400s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	call("GET", base, nil, "", "", 401)
	call("POST", base, map[string]any{}, ownerSession, "https://untrusted.invalid", 403)
	call("POST", base, map[string]any{}, reviewSession, "https://console.invalid", 400)
	call("GET", "/api/v1/workspaces/unknown-workspace/assessments", nil, ownerSession, "", 404)
	var templates struct {
		Items []store.AssessmentTemplate `json:"items"`
	}
	must(json.Unmarshal(call("GET", base+"/templates", nil, reviewSession, "", 200).Body.Bytes(), &templates))
	if len(templates.Items) != 4 {
		t.Fatal("builtin templates unavailable over HTTP")
	}
	var pia store.AssessmentTemplate
	for _, v := range templates.Items {
		if v.Kind == "pia" {
			pia = v
		}
	}
	asset, err := repo.CreateAsset(ctx, ws.ID, "Dataset", "sample", "Data")
	must(err)
	activity, err := repo.CreateActivity(ctx, ws.ID, owner.ID, store.ActivityInput{AssetID: asset.ID, Name: "Approved synthetic activity", Kind: "internal", Purpose: "Test effects"})
	must(err)
	input := store.AssessmentInput{ActivityID: activity.ID, TemplateRevisionID: pia.ID, Title: "<script>alert(1)</script>", DueAt: time.Now().Add(time.Hour), RefreshMinutes: 5}
	call("POST", base, input, reviewSession, "https://console.invalid", 403)
	var created store.AssessmentDetail
	must(json.Unmarshal(call("POST", base, input, ownerSession, "https://console.invalid", 201).Body.Bytes(), &created))
	detailPath := fmt.Sprintf("%s/%d", base, created.ID)
	call("GET", base+"/0", nil, ownerSession, "", 404)
	call("GET", base+"?kind=not-a-kind", nil, ownerSession, "", 400)
	call("PUT", detailPath+"/answers", store.AssessmentAnswersInput{Revision: created.Revision, Answers: []store.AssessmentAnswer{{Key: "purpose", Value: "bad", Number: new(float64)}}}, ownerSession, "https://console.invalid", 400)
	html := call("GET", detailPath+"/export?format=html", nil, ownerSession, "", 200)
	if strings.Contains(html.Body.String(), "<script>alert(1)</script>") || !strings.Contains(html.Body.String(), "&lt;script&gt;") {
		t.Fatal("assessment export did not escape untrusted title")
	}
	call("GET", detailPath+"/export?format=csv", nil, ownerSession, "", 400)
	var report store.AssessmentDetail
	must(json.Unmarshal(call("GET", detailPath+"/export?format=json", nil, reviewSession, "", 200).Body.Bytes(), &report))
	if report.Template.ID != pia.ID || len(report.Gaps) == 0 || report.CanEdit || report.CanApprove {
		t.Fatal("export missing adopted clauses/gaps or exposes edit state")
	}
	var submitted store.AssessmentDetail
	must(json.Unmarshal(call("POST", detailPath+"/review", store.AssessmentReviewInput{Revision: created.Revision, Action: "submit"}, ownerSession, "https://console.invalid", 200).Body.Bytes(), &submitted))
	call("POST", detailPath+"/review", store.AssessmentReviewInput{Revision: submitted.Revision, Action: "approve", Note: "Self approval"}, ownerSession, "https://console.invalid", 403)
	call("POST", detailPath+"/review", store.AssessmentReviewInput{Revision: submitted.Revision, Action: "approve", Note: "Missing facts must prevent approval"}, reviewSession, "https://console.invalid", 409)
	call("POST", detailPath+"/review", store.AssessmentReviewInput{Revision: submitted.Revision, Action: "return", Note: "Please gather verified business facts"}, reviewSession, "https://console.invalid", 200)
	call("POST", detailPath+"/refresh", map[string]any{}, reviewSession, "https://console.invalid", 403)
	call("POST", detailPath+"/refresh", map[string]any{}, ownerSession, "https://console.invalid", 200)
	activityPath := fmt.Sprintf("/api/v1/workspaces/%s/activities/%d", slug, activity.ID)
	update := store.ActivityUpdateInput{ActivityInput: activity.ActivityInput, Revision: activity.Revision}
	call("PUT", activityPath, update, reviewSession, "https://console.invalid", 403)
	call("PUT", activityPath, update, ownerSession, "https://untrusted.invalid", 403)
	call("PUT", activityPath, update, ownerSession, "https://console.invalid", 200)
	var noHistory struct {
		Items []store.ActivityChange `json:"items"`
	}
	must(json.Unmarshal(call("GET", activityPath+"/history", nil, ownerSession, "", 200).Body.Bytes(), &noHistory))
	if len(noHistory.Items) != 0 {
		t.Fatal("no-op HTTP save wrote history")
	}
	update.Purpose = "Changed through authorized HTTP"
	var updated store.Activity
	must(json.Unmarshal(call("PUT", activityPath, update, ownerSession, "https://console.invalid", 200).Body.Bytes(), &updated))
	if updated.Revision != activity.Revision+1 {
		t.Fatal("missing optimistic activity revision")
	}
	call("PUT", activityPath, update, ownerSession, "https://console.invalid", 409)
	history := call("GET", activityPath+"/history/export", nil, reviewSession, "", 200)
	if !strings.Contains(history.Body.String(), "Changed through authorized HTTP") || !strings.Contains(history.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("change export incomplete")
	}
	call("GET", activityPath+"/history?limit=101", nil, ownerSession, "", 400)
	var stale store.AssessmentDetail
	must(json.Unmarshal(call("GET", detailPath, nil, ownerSession, "", 200).Body.Bytes(), &stale))
	if !stale.Stale || !stale.ReassessmentPending {
		t.Fatal("HTTP change did not create reassessment task")
	}

}
