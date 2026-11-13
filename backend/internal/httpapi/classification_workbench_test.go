package httpapi

import (
	"context"
	"encoding/csv"
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

func TestClassificationWorkbenchHTTPReviewPublicationAndIsolation(t *testing.T) {
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
	slug := fmt.Sprintf("classification-http-%d", time.Now().UnixNano())
	otherSlug := slug + "-foreign"
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Classification workbench HTTP"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reviewer", "unused"))
	must(repo.Bootstrap(ctx, otherSlug, "unused", otherSlug, "Other"))
	owner, err := repo.FindUser(ctx, slug)
	must(err)
	reviewer, err := repo.FindUser(ctx, slug+"-reviewer")
	must(err)
	foreign, err := repo.FindUser(ctx, otherSlug)
	must(err)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	other, err := repo.Membership(ctx, foreign.ID, otherSlug)
	must(err)
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=ANY($1)`, []int64{ws.ID, other.ID})
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE id=ANY($1)`, []int64{owner.ID, reviewer.ID, foreign.ID})
	})
	session := func(id int64) string {
		t.Helper()
		token, e := sessionToken()
		must(e)
		must(repo.CreateSession(ctx, id, sessionHash(token), time.Now().Add(time.Hour)))
		return token
	}
	ownCookie, reviewCookie, foreignCookie := session(owner.ID), session(reviewer.ID), session(foreign.ID)
	asset, err := repo.CreateAsset(ctx, ws.ID, "=SUM(1,2)", "Fixture system", "Governance")
	must(err)
	second, err := repo.CreateAsset(ctx, ws.ID, "Second dataset", "Fixture system", "Governance")
	must(err)
	foreignAsset, err := repo.CreateAsset(ctx, other.ID, "Foreign dataset", "Other", "Other")
	must(err)
	handler := New(repo, "https://console.invalid", true)
	base := "/api/v1/workspaces/" + slug + "/classification"
	rawCall := func(method, path, body, cookie, origin string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
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
			t.Fatalf("%s %s status %d want %d: %.400s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	call := func(method, path string, body any, cookie string, status int) *httptest.ResponseRecorder {
		t.Helper()
		raw, e := json.Marshal(body)
		must(e)
		return rawCall(method, path, string(raw), cookie, "https://console.invalid", status)
	}
	call("GET", base+"/workbench", nil, "", 401)
	call("GET", "/api/v1/workspaces/"+otherSlug+"/classification/workbench", nil, ownCookie, 404)
	call("GET", base+"/workbench?page=0", nil, ownCookie, 400)
	call("GET", base+"/workbench?pageSize=1000", nil, ownCookie, 400)
	call("GET", base+"/workbench?taxonomyId=-1", nil, ownCookie, 400)
	var initial store.ClassificationWorkbenchPage
	must(json.Unmarshal(call("GET", base+"/workbench", nil, reviewCookie, 200).Body.Bytes(), &initial))
	if initial.Total != 2 || initial.Counts.Total != 2 {
		t.Fatal("workbench scope/count mismatch")
	}
	nodeInput := map[string]any{"name": "Customer data", "description": "Synthetic business classification", "parentId": nil, "enabled": true}
	rawCall("POST", base+"/taxonomy", `{"name":"untrusted"}`, ownCookie, "", 403)
	call("POST", base+"/taxonomy", nodeInput, reviewCookie, 403)
	var node store.ClassificationTaxonomyNode
	must(json.Unmarshal(call("POST", base+"/taxonomy", nodeInput, ownCookie, 201).Body.Bytes(), &node))
	batch := map[string]any{"items": []map[string]any{{"assetId": asset.ID, "revision": 0}, {"assetId": second.ID, "revision": 999}}, "category": "business", "businessLevel": "internal", "statutoryCategory": "general", "basis": "Synthetic scope independently evaluated", "standardId": classificationstandards.DefaultID, "levelCode": "general", "ruleId": "ordinary-grading", "taxonomyId": node.ID}
	call("POST", base+"/assets/batch-review", batch, reviewCookie, 403)
	call("POST", base+"/assets/batch-review", batch, ownCookie, 409)
	untouched, err := repo.GetAssetClassification(ctx, ws.ID, owner.ID, asset.ID)
	must(err)
	if untouched.Revision != 0 {
		t.Fatal("failed batch committed partial classification")
	}
	batch["items"] = []map[string]any{{"assetId": asset.ID, "revision": 0}, {"assetId": foreignAsset.ID, "revision": 0}}
	call("POST", base+"/assets/batch-review", batch, ownCookie, 404)
	batch["items"] = []map[string]any{{"assetId": asset.ID, "revision": 0}, {"assetId": second.ID, "revision": 0}}
	var reviewed struct {
		Items []store.AssetClassificationDetail `json:"items"`
	}
	must(json.Unmarshal(call("POST", base+"/assets/batch-review", batch, ownCookie, 200).Body.Bytes(), &reviewed))
	if len(reviewed.Items) != 2 || reviewed.Items[0].Revision != 1 || reviewed.Items[0].Taxonomy == nil {
		t.Fatal("batch did not preserve classification/taxonomy evidence")
	}
	call("POST", base+"/assets/batch-review", batch, ownCookie, 409)
	var filtered store.ClassificationWorkbenchPage
	must(json.Unmarshal(call("GET", base+"/workbench?q=Second&category=business&pageSize=1&taxonomyId="+fmt.Sprint(node.ID), nil, reviewCookie, 200).Body.Bytes(), &filtered))
	if filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0].Asset.ID != second.ID || filtered.Counts.Total != 2 {
		t.Fatal("filtered rows and workspace facets conflated")
	}
	publicationInput := map[string]any{"title": "Reviewed result fixture", "note": "Synthetic publication only", "items": []map[string]any{{"assetId": asset.ID, "revision": 1}}}
	call("POST", base+"/publications", publicationInput, reviewCookie, 403)
	var publication store.ClassificationPublication
	must(json.Unmarshal(call("POST", base+"/publications", publicationInput, ownCookie, 201).Body.Bytes(), &publication))
	pubPath := base + "/publications/" + fmt.Sprint(publication.ID)
	if publication.Status != "submitted" || len(publication.Items) != 1 || len(publication.SnapshotDigest) != 64 {
		t.Fatal("publication was not an immutable submitted snapshot")
	}
	call("GET", pubPath+"/export.csv", nil, ownCookie, 409)
	call("GET", pubPath+"/export.json", nil, reviewCookie, 409)
	call("POST", pubPath+"/review", map[string]string{"action": "publish", "note": "Self approval"}, ownCookie, 403)
	var published store.ClassificationPublication
	must(json.Unmarshal(call("POST", pubPath+"/review", map[string]string{"action": "publish", "note": "Independently reviewed synthetic classification"}, reviewCookie, 200).Body.Bytes(), &published))
	if published.Status != "published" || published.SnapshotDigest != publication.SnapshotDigest {
		t.Fatal("independent publication changed frozen contents")
	}
	exported := call("GET", pubPath+"/export.csv", nil, reviewCookie, 200)
	if !strings.HasPrefix(exported.Header().Get("Content-Type"), "text/csv") || !strings.Contains(exported.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("CSV export headers incorrect")
	}
	csvRows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(exported.Body.String(), "\ufeff"))).ReadAll()
	must(err)
	if len(csvRows) != 2 || csvRows[1][1] != "'=SUM(1,2)" {
		t.Fatal("CSV metadata was interpreted as a formula or frozen row missing")
	}
	var frozen store.ClassificationPublication
	must(json.Unmarshal(call("GET", pubPath+"/export.json", nil, reviewCookie, 200).Body.Bytes(), &frozen))
	if frozen.Items[0].Revision != 1 {
		t.Fatal("JSON export omitted adopted revision")
	}
	call("GET", pubPath, nil, foreignCookie, 404)
	call("GET", "/api/v1/workspaces/"+otherSlug+"/classification/publications/"+fmt.Sprint(publication.ID), nil, foreignCookie, 404)
	// Rename the taxonomy after publishing: the current workbench flags drift,
	// while the exported reference remains the adopted category name.
	nodeInput["name"] = "Renamed customer data"
	nodeInput["revision"] = node.Revision
	call("PUT", base+"/taxonomy/"+fmt.Sprint(node.ID), nodeInput, ownCookie, 200)
	var changed store.ClassificationWorkbenchPage
	must(json.Unmarshal(call("GET", base+"/workbench?status=changed", nil, reviewCookie, 200).Body.Bytes(), &changed))
	if changed.Total != 1 || !changed.Items[0].TaxonomyStale {
		t.Fatal("published taxonomy drift not flagged")
	}
	must(json.Unmarshal(call("GET", pubPath+"/export.json", nil, reviewCookie, 200).Body.Bytes(), &frozen))
	if frozen.Items[0].Taxonomy.Path != "Customer data" || frozen.SnapshotDigest != publication.SnapshotDigest {
		t.Fatal("taxonomy edit rewrote an existing publication")
	}
	rawCall("POST", base+"/publications", `{"title":"x","items":[],"actorId":1}`, ownCookie, "https://console.invalid", 400)
	// Calibration is a bounded, editor-only calculation; sample text is never
	// reflected into the result, including on errors.
	rulePath := "/api/v1/workspaces/" + slug + "/discovery/rules/test"
	calibration := `{"rule":{"key":"fixture.email","version":1,"name":"Email fixture","kind":"regexp","target":"content","category":"email","expression":"^[^ @]+@[^ @]+\\.[^ @]+$","dictionary":[],"status":"draft"},"samples":[{"value":"synthetic-private-value@example.invalid","expected":true},{"value":"no-match","expected":false}]}`
	rawCall("POST", rulePath, calibration, reviewCookie, "https://console.invalid", 403)
	response := rawCall("POST", rulePath, calibration, ownCookie, "https://console.invalid", 200)
	if strings.Contains(response.Body.String(), "synthetic-private-value") {
		t.Fatal("calibration reflected sample contents")
	}
	var result struct{ Matched, Scanned, TruePositive, TrueNegative int }
	must(json.Unmarshal(response.Body.Bytes(), &result))
	if result.Matched != 1 || result.Scanned != 2 || result.TruePositive != 1 || result.TrueNegative != 1 {
		t.Fatal("calibration not using actual matching semantics")
	}
	rawCall("POST", rulePath, strings.Repeat(" ", 65<<10), ownCookie, "https://console.invalid", 413)
}

func TestClassificationCSVCellsRemainLiteral(t *testing.T) {
	for _, value := range []string{"=1+1", " +SUM(A1)", "\t@fixture", "-2+1", "\r=HYPERLINK(\"https://invalid\")"} {
		if !strings.HasPrefix(classificationCSVCell(value), "'") {
			t.Error("spreadsheet formula not made literal")
		}
	}
	for _, value := range []string{"Customer data", "客户 / 联系方式", "a,b\nquoted \"text\"", ""} {
		if classificationCSVCell(value) != value {
			t.Error("ordinary CSV metadata changed")
		}
	}
}
