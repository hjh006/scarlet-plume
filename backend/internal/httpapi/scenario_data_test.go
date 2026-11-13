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

func TestScenarioDataHTTPApplicationSourceBoundaries(t *testing.T) {
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("scenario-data-http-%d", time.Now().UnixNano())
	foreignSlug := slug + "-foreign"
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Scenario data HTTP fixture"))
	must(repo.Bootstrap(ctx, foreignSlug, "unused", foreignSlug, "Other workspace"))
	owner, err := repo.FindUser(ctx, slug)
	must(err)
	foreignOwner, err := repo.FindUser(ctx, foreignSlug)
	must(err)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	foreign, err := repo.Membership(ctx, foreignOwner.ID, foreignSlug)
	must(err)
	usernames := []string{slug, foreignSlug}
	member := func(role string) int64 {
		t.Helper()
		name := slug + "-" + role
		usernames = append(usernames, name)
		var id int64
		must(pool.QueryRow(ctx, `INSERT INTO plume.users(username,password_hash)VALUES($1,'unused')RETURNING id`, name).Scan(&id))
		_, err := pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role)VALUES($1,$2,$3)`, ws.ID, id, role)
		must(err)
		return id
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM plume.workspaces WHERE id=ANY($1)`, []int64{ws.ID, foreign.ID})
		_, _ = pool.Exec(cleanup, `DELETE FROM plume.users WHERE username=ANY($1)`, usernames)
	})
	session := func(id int64) string {
		t.Helper()
		token, err := sessionToken()
		must(err)
		must(repo.CreateSession(ctx, id, sessionHash(token), time.Now().Add(time.Hour)))
		return token
	}
	ownerSession := session(owner.ID)
	viewerSession, operatorSession, reviewerSession := session(member("viewer")), session(member("operator")), session(member("reviewer"))
	asset, err := repo.CreateAsset(ctx, ws.ID, "Selected AI application", "support", "Governance")
	must(err)
	other, err := repo.CreateAsset(ctx, ws.ID, "Unbound application", "other", "Other")
	must(err)
	foreignAsset, err := repo.CreateAsset(ctx, foreign.ID, "Foreign application", "foreign", "Other")
	must(err)
	activity, err := repo.CreateActivity(ctx, ws.ID, owner.ID, store.ActivityInput{AssetID: asset.ID, Name: "Synthetic AI processing", Kind: "ai", Purpose: "approved synthetic support"})
	must(err)
	handler := New(repo, "https://console.invalid", true)
	base := "/api/v1/workspaces/" + slug
	sources := base + "/application-sources"
	dataPath := base + "/scenario-data"
	callRaw := func(method, path, body, cookie, bearer, origin, contentType string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: cookie})
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			// Responses can contain one-time tokens. Do not print bodies on failure.
			t.Fatalf("%s %s got status %d, want %d", method, path, w.Code, status)
		}
		return w
	}
	call := func(method, path string, body any, cookie, bearer, origin string, status int) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		must(err)
		return callRaw(method, path, string(raw), cookie, bearer, origin, "application/json", status)
	}
	input := store.ApplicationSourceInput{Name: "Approved application API", AssetIDs: []int64{asset.ID}, MaxAgeMinutes: 60}
	call("GET", sources, nil, "", "", "", 401)
	call("POST", sources, input, "", "", "https://console.invalid", 401)
	call("POST", sources, input, ownerSession, "", "", 403)
	call("POST", sources, input, ownerSession, "", "https://untrusted.invalid", 403)
	for _, cookie := range []string{viewerSession, operatorSession, reviewerSession} {
		call("POST", sources, input, cookie, "", "https://console.invalid", 403)
	}
	foreignInput := input
	foreignInput.AssetIDs = []int64{foreignAsset.ID}
	call("POST", sources, foreignInput, ownerSession, "", "https://console.invalid", 404)
	call("POST", sources, map[string]any{"name": "bad", "assetIds": []int64{asset.ID}, "unknown": "rejected"}, ownerSession, "", "https://console.invalid", 400)
	var created struct {
		Source store.ApplicationSource `json:"source"`
		Token  string                  `json:"token"`
	}
	must(json.Unmarshal(call("POST", sources, input, ownerSession, "", "https://console.invalid", 201).Body.Bytes(), &created))
	if len(created.Token) != 43 || created.Source.ID <= 0 || created.Source.Status != "never_run" {
		t.Fatal("source registration did not return a one-time token and unobserved source")
	}
	for _, cookie := range []string{ownerSession, viewerSession} {
		listed := call("GET", sources, nil, cookie, "", "", 200)
		if strings.Contains(listed.Body.String(), created.Token) || strings.Contains(listed.Body.String(), "token") {
			t.Fatal("source listing exposed a token or credential field")
		}
	}
	call("GET", "/api/v1/workspaces/"+foreignSlug+"/application-sources", nil, ownerSession, "", "", 404)
	observation := store.ApplicationSubmission{BatchID: "first", ObservedAt: time.Now().UTC(), Status: "success", Items: []store.ApplicationObservation{{AssetID: asset.ID, Facts: map[string]string{"application_name": "Support AI", "application_vendor": "Approved vendor", "application_region": "configured region", "application_training_use": "disabled"}}}}
	call("POST", applicationObservationsPath, observation, "", "", "", 401)
	call("POST", applicationObservationsPath, observation, "", strings.Repeat("x", 43), "", 403)
	call("POST", applicationObservationsPath+"/", observation, "", created.Token, "", 403)
	call("PUT", applicationObservationsPath, observation, "", created.Token, "", 403)
	call("DELETE", applicationObservationsPath, nil, "", created.Token, "", 403)
	call("GET", applicationObservationsPath, nil, "", created.Token, "", 404)
	callRaw("POST", applicationObservationsPath, "{}", "", created.Token, "", "text/plain", 415)
	call("POST", applicationObservationsPath, map[string]any{"batchId": "bad", "observedAt": time.Now(), "status": "success", "items": []any{}, "unknown": true}, "", created.Token, "", 400)
	callRaw("POST", applicationObservationsPath, strings.Repeat("x", maxRequestBytes+1), "", created.Token, "", "application/json", 413)
	for _, id := range []int64{other.ID, foreignAsset.ID} {
		outOfScope := observation
		outOfScope.BatchID = fmt.Sprint(id)
		outOfScope.Items = []store.ApplicationObservation{{AssetID: id, Facts: map[string]string{"application_vendor": "outside binding"}}}
		call("POST", applicationObservationsPath, outOfScope, "", created.Token, "", 403)
	}
	invalidFact := observation
	invalidFact.BatchID = "unknown-fact"
	invalidFact.Items = []store.ApplicationObservation{{AssetID: asset.ID, Facts: map[string]string{"lawful_basis": "unverified"}}}
	call("POST", applicationObservationsPath, invalidFact, "", created.Token, "", 400)
	// This exact POST accepts its scoped bearer without a browser Origin or session.
	call("POST", applicationObservationsPath, observation, "", created.Token, "", 200)
	var replay store.ApplicationReceipt
	must(json.Unmarshal(call("POST", applicationObservationsPath, observation, "", created.Token, "", 200).Body.Bytes(), &replay))
	if !replay.Replayed {
		t.Fatal("identical application batch was not idempotent")
	}
	conflict := observation
	conflict.Items = []store.ApplicationObservation{{AssetID: asset.ID, Facts: map[string]string{"application_vendor": "different"}}}
	call("POST", applicationObservationsPath, conflict, "", created.Token, "", 409)
	call("GET", dataPath, nil, "", "", "", 401)
	call("GET", "/api/v1/workspaces/"+foreignSlug+"/scenario-data", nil, ownerSession, "", "", 404)
	call("GET", dataPath+fmt.Sprintf("?assetId=%d", foreignAsset.ID), nil, ownerSession, "", "", 404)
	for _, query := range []string{"assetId=0", "assetId=-1", "assetId=bad", "activityId=0", "activityId=1.5", "activityId=9223372036854775808"} {
		call("GET", dataPath+"?"+query, nil, ownerSession, "", "", 400)
	}
	call("GET", dataPath+fmt.Sprintf("?assetId=%d&activityId=%d", other.ID, activity.ID), nil, ownerSession, "", "", 400)
	if _, err := repo.ScenarioData(ctx, ws.ID, owner.ID, 0, activity.ID); err != nil {
		t.Fatalf("scenario data store query failed: %v", err)
	}
	var selected store.ScenarioData
	must(json.Unmarshal(call("GET", dataPath+fmt.Sprintf("?activityId=%d", activity.ID), nil, viewerSession, "", "", 200).Body.Bytes(), &selected))
	if selected.AssetID != asset.ID || selected.ActivityID != activity.ID || len(selected.Gaps) == 0 {
		t.Fatal("selected scenario scope or explicit gaps missing")
	}
	found := false
	for _, fact := range selected.Facts {
		if fact.Key == "application_vendor" {
			found = fact.State == "known" && fact.Value == "Approved vendor" && fact.Source == "application_api" && fact.Record != "" && fact.ObservedAt != nil
		}
		if fact.Key == "lawful_basis" && fact.State != "unknown" {
			t.Fatal("observed application configuration inferred legal facts")
		}
	}
	if !found {
		t.Fatal("actual application observation not present in selected scenario data")
	}
	var unbound store.ScenarioData
	must(json.Unmarshal(call("GET", dataPath+fmt.Sprintf("?assetId=%d", other.ID), nil, viewerSession, "", "", 200).Body.Bytes(), &unbound))
	for _, fact := range unbound.Facts {
		if strings.HasPrefix(fact.Key, "application_") && fact.State == "known" {
			t.Fatal("application observation leaked into unbound asset")
		}
	}
	revokePath := fmt.Sprintf("%s/%d", sources, created.Source.ID)
	call("DELETE", revokePath, nil, viewerSession, "", "https://console.invalid", 403)
	call("DELETE", revokePath, nil, ownerSession, "", "", 403)
	call("DELETE", revokePath, nil, ownerSession, "", "https://console.invalid", 204)
	observation.BatchID = "after-revoke"
	call("POST", applicationObservationsPath, observation, "", created.Token, "", 403)
	var revoked store.ScenarioData
	must(json.Unmarshal(call("GET", dataPath+fmt.Sprintf("?assetId=%d", asset.ID), nil, viewerSession, "", "", 200).Body.Bytes(), &revoked))
	for _, fact := range revoked.Facts {
		if strings.HasPrefix(fact.Key, "application_") && fact.State == "known" {
			t.Fatal("revoked source configuration remained current")
		}
	}
}
