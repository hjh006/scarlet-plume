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

	"github.com/hjh006/scarlet-plume/backend/internal/classificationstandards"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestScenarioClassificationHTTPPrefillAndScope(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repo := store.New(pool)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("scenario-classification-%d", time.Now().UnixNano())
	otherSlug := slug + "-other"
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Scenario classification HTTP"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reviewer", "unused"))
	must(repo.Bootstrap(ctx, otherSlug, "unused", otherSlug, "Other workspace"))
	owner, err := repo.FindUser(ctx, slug)
	must(err)
	reviewer, err := repo.FindUser(ctx, slug+"-reviewer")
	must(err)
	otherOwner, err := repo.FindUser(ctx, otherSlug)
	must(err)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	otherWS, err := repo.Membership(ctx, otherOwner.ID, otherSlug)
	must(err)
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=ANY($1)`, []int64{ws.ID, otherWS.ID})
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE id=ANY($1)`, []int64{owner.ID, reviewer.ID, otherOwner.ID})
	})
	session := func(id int64) string {
		t.Helper()
		token, e := sessionToken()
		must(e)
		must(repo.CreateSession(ctx, id, sessionHash(token), time.Now().Add(time.Hour)))
		return token
	}
	ownerCookie, reviewCookie := session(owner.ID), session(reviewer.ID)
	asset, err := repo.CreateAsset(ctx, ws.ID, "Synthetic customer metadata", "Synthetic CRM", "Governance")
	must(err)
	unrelated, err := repo.CreateAsset(ctx, ws.ID, "Unrelated synthetic asset", "Other system", "Other team")
	must(err)
	foreign, err := repo.CreateAsset(ctx, otherWS.ID, "Other data", "Other", "Other")
	must(err)
	activity, err := repo.CreateActivity(ctx, ws.ID, owner.ID, store.ActivityInput{AssetID: asset.ID, Name: "Synthetic cross-border support", Kind: "cross_border", Purpose: "Synthetic support", Recipient: "Synthetic recipient", Region: "Synthetic region", RetentionPolicy: "Synthetic retention", TransferPath: "Synthetic transfer", Safeguards: "Synthetic controls"})
	must(err)
	unrelatedActivity, err := repo.CreateActivity(ctx, ws.ID, owner.ID, store.ActivityInput{AssetID: unrelated.ID, Name: "Unrelated synthetic processing", Kind: "internal", Purpose: "Unrelated scope"})
	must(err)
	handler := New(repo, "https://console.invalid", true)
	base := "/api/v1/workspaces/" + slug
	call := func(method, path string, payload any, cookie string, status int) []byte {
		t.Helper()
		raw, e := json.Marshal(payload)
		must(e)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://console.invalid")
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: cookie})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s got %d, want %d: %.200s", method, path, w.Code, status, w.Body.String())
		}
		return w.Body.Bytes()
	}
	type dataResult struct {
		AssetID        int64  `json:"assetId"`
		ActivityID     int64  `json:"activityId"`
		Scenario       string `json:"scenario"`
		Task           string `json:"task"`
		Classification *struct {
			Item              store.ClassificationWorkbenchItem `json:"item"`
			Usable            bool                              `json:"usable"`
			ManuallyConfirmed bool                              `json:"manuallyConfirmed"`
		} `json:"classification"`
		Facts     []store.AssessmentFact `json:"facts"`
		Materials []struct {
			Key    string `json:"key"`
			Status string `json:"status"`
			Value  string `json:"value"`
		} `json:"materials"`
	}
	read := func(query string) dataResult {
		t.Helper()
		var result dataResult
		must(json.Unmarshal(call("GET", base+"/scenario-data?"+query, nil, reviewCookie, 200), &result))
		return result
	}
	fact := func(result dataResult, key string) store.AssessmentFact {
		for _, f := range result.Facts {
			if f.Key == key {
				return f
			}
		}
		return store.AssessmentFact{}
	}
	call("GET", base+"/scenario-data?scenario=personal&task=pia", nil, "", 401)
	for _, query := range []string{
		"scenario=missing", "scenario=personal&task=scope", "task=pia",
		"scenario=personal&scenario=crossborder", "scenario=personal&task=pia&task=inventory",
		fmt.Sprintf("assetId=%d&assetId=%d", asset.ID, foreign.ID),
		fmt.Sprintf("activityId=%d&activityId=%d", activity.ID, activity.ID),
		fmt.Sprintf("assetId=%d&activityId=%d", foreign.ID, activity.ID),
	} {
		call("GET", base+"/scenario-data?"+query, nil, ownerCookie, 400)
	}
	call("GET", base+fmt.Sprintf("/scenario-data?scenario=personal&task=pia&assetId=%d", foreign.ID), nil, ownerCookie, 404)
	call("GET", "/api/v1/workspaces/"+otherSlug+"/scenario-data?scenario=personal&task=pia", nil, ownerCookie, 404)
	selected := fmt.Sprintf("assetId=%d&scenario=personal&task=pia", asset.ID)
	unconfirmed := read(selected)
	if unconfirmed.Classification == nil || unconfirmed.Classification.Usable || len(unconfirmed.Materials) == 0 {
		t.Fatal("unconfirmed classification was adopted or missing materials were hidden")
	}
	var node store.ClassificationTaxonomyNode
	must(json.Unmarshal(call("POST", base+"/classification/taxonomy", map[string]any{"name": "Synthetic customer data", "description": "Synthetic taxonomy", "enabled": true, "parentId": nil}, ownerCookie, 201), &node))
	call("POST", base+"/classification/assets/batch-review", map[string]any{"items": []map[string]any{{"assetId": asset.ID, "revision": 0}}, "category": "personal", "businessLevel": "confidential", "statutoryCategory": "general", "standardId": classificationstandards.DefaultID, "levelCode": "general", "ruleId": "ordinary-grading", "basis": "Synthetic classification reviewed against its actual scope", "taxonomyId": node.ID}, ownerCookie, 200)
	for _, scope := range []struct{ scene, task string }{{"personal", "pia"}, {"domestic", "inspection"}, {"crossborder", "assessment"}} {
		result := read(fmt.Sprintf("scenario=%s&task=%s&activityId=%d", scope.scene, scope.task, activity.ID))
		if result.AssetID != asset.ID || result.ActivityID != activity.ID || result.Scenario != scope.scene || result.Task != scope.task || result.Classification == nil || !result.Classification.Usable || !result.Classification.ManuallyConfirmed || result.Classification.Item.Revision != 1 {
			t.Fatal("scenario lost selected scope or current confirmed classification")
		}
		if f := fact(result, "asset_category"); f.State != "known" || f.Value != "personal" || f.Record == "" {
			t.Fatal("classification fact lost adopted value or provenance")
		}
		if f := fact(result, "classification_context"); f.State != "known" || f.Value == "" {
			t.Fatal("classification context was not available for real assessment prefill")
		}
		for _, key := range []string{"deduplicated_subject_count", "cii_status", "cross_border_exception", "lawful_basis", "notice_consent"} {
			if f := fact(result, key); f.State != "unknown" {
				t.Fatalf("classification inferred business/legal condition %s", key)
			}
		}
		automatic, missing := 0, 0
		for _, m := range result.Materials {
			switch m.Status {
			case "auto_filled":
				automatic++
			case "missing", "needs_confirmation":
				missing++
			case "not_applicable":
				t.Fatal("classification alone made a task not applicable")
			default:
				t.Fatalf("unknown material state %q", m.Status)
			}
		}
		if automatic == 0 || missing == 0 {
			t.Fatal("scene materials did not distinguish reusable data from gaps")
		}
	}
	// The HTTP creation path adopts the new optional context without answering
	// the separate compound scope/risk questions on the user's behalf.
	var templates struct {
		Items []store.AssessmentTemplate `json:"items"`
	}
	must(json.Unmarshal(call("GET", base+"/assessments/templates", nil, ownerCookie, 200), &templates))
	var chosen store.AssessmentTemplate
	for _, t := range templates.Items {
		if t.Kind == "pia" && t.State == "published" && t.Revision > chosen.Revision {
			chosen = t
		}
	}
	if chosen.RevisionID == nil {
		t.Fatal("no published PIA template")
	}
	var assessment store.AssessmentDetail
	must(json.Unmarshal(call("POST", base+"/assessments", store.AssessmentInput{ActivityID: activity.ID, TemplateRevisionID: *chosen.RevisionID, Title: "Synthetic scoped PIA", DueAt: time.Now().Add(24 * time.Hour), RefreshMinutes: 1440}, ownerCookie, 201), &assessment))
	prefilled, scopeGap := false, false
	for _, a := range assessment.Answers {
		if a.Key == "classification_context" && a.Value != "" && a.Note != "" {
			prefilled = true
		}
	}
	for _, g := range assessment.Gaps {
		if g.Key == "processing_scope" {
			scopeGap = true
		}
	}
	if !prefilled || !scopeGap {
		t.Fatal("assessment did not reuse confirmed classification or hid the remaining scope question")
	}
	call("POST", base+"/assessments", store.AssessmentInput{ActivityID: unrelatedActivity.ID, TemplateRevisionID: *chosen.RevisionID, Title: "Other scope", DueAt: time.Now().Add(24 * time.Hour), RefreshMinutes: 1440}, ownerCookie, 201)
	for _, target := range []struct{ assetID, activityID int64 }{{asset.ID, activity.ID}, {unrelated.ID, unrelatedActivity.ID}} {
		call("POST", base+"/checks", store.CheckInput{AssetID: target.assetID, ActivityID: &target.activityID, RequirementCode: "G01", OwnerTeam: "Synthetic team", Note: "Synthetic scoped check"}, ownerCookie, 201)
	}
	for _, route := range []string{"checks", "assessments"} {
		for _, query := range []string{fmt.Sprintf("assetId=%d", asset.ID), fmt.Sprintf("activityId=%d", activity.ID), fmt.Sprintf("assetId=%d&activityId=%d", asset.ID, activity.ID)} {
			var list struct {
				Items []struct {
					ActivityID *int64 `json:"activityId"`
					Activity   *struct {
						ID int64 `json:"id"`
					} `json:"activity"`
					ID int64 `json:"id"`
				} `json:"items"`
			}
			must(json.Unmarshal(call("GET", base+"/"+route+"?"+query, nil, reviewCookie, 200), &list))
			if len(list.Items) != 1 {
				t.Fatalf("%s lost its actual server-side scope", route)
			}
			if route == "assessments" && (list.Items[0].ActivityID == nil || *list.Items[0].ActivityID != activity.ID) || route == "checks" && (list.Items[0].Activity == nil || list.Items[0].Activity.ID != activity.ID) {
				t.Fatalf("%s returned another processing activity", route)
			}
		}
		call("GET", base+"/"+route+fmt.Sprintf("?assetId=%d", foreign.ID), nil, ownerCookie, 404)
		call("GET", base+"/"+route+"?activityId=9223372036854775807", nil, ownerCookie, 404)
		call("GET", base+"/"+route+fmt.Sprintf("?assetId=%d&activityId=%d", unrelated.ID, activity.ID), nil, ownerCookie, 400)
		call("GET", base+"/"+route+"?assetId=0", nil, ownerCookie, 400)
	}
	// Changing the adopted taxonomy invalidates automatic reuse. Reading an
	// old assessment continues to expose its adopted snapshot separately.
	call("PUT", base+"/classification/taxonomy/"+fmt.Sprint(node.ID), map[string]any{"name": "Renamed synthetic taxonomy", "description": "Changed scope", "enabled": true, "parentId": nil, "revision": node.Revision}, ownerCookie, 200)
	changed := read(selected)
	if changed.Classification == nil || changed.Classification.Usable || fact(changed, "classification_context").State == "known" {
		t.Fatal("stale taxonomy remained automatically usable")
	}
	var old store.AssessmentDetail
	must(json.Unmarshal(call("GET", base+"/assessments/"+fmt.Sprint(assessment.ID), nil, reviewCookie, 200), &old))
	for _, f := range old.Facts {
		if f.Key == "classification_context" && strings.Contains(f.Value, "Renamed synthetic taxonomy") {
			t.Fatal("live taxonomy rewrote adopted assessment facts")
		}
	}
}
