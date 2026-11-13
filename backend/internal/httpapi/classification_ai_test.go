package httpapi

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationai"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestClassificationAIHTTPOptionalModelChoiceAndMetadataConsent(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
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
	slug := fmt.Sprintf("classification-ai-http-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "AI HTTP fixture"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reviewer", "unused"))
	must(repo.Bootstrap(ctx, slug+"-foreign", "unused", slug+"-foreign", "Other"))
	owner, err := repo.FindUser(ctx, slug)
	must(err)
	reviewer, err := repo.FindUser(ctx, slug+"-reviewer")
	must(err)
	foreign, err := repo.FindUser(ctx, slug+"-foreign")
	must(err)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	other, err := repo.Membership(ctx, foreign.ID, slug+"-foreign")
	must(err)
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=ANY($1)`, []int64{ws.ID, other.ID})
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE id=ANY($1)`, []int64{owner.ID, reviewer.ID, foreign.ID})
	})
	cookie := func(id int64) string {
		t.Helper()
		token, e := sessionToken()
		must(e)
		must(repo.CreateSession(ctx, id, sessionHash(token), time.Now().Add(time.Hour)))
		return token
	}
	own, read, otherCookie := cookie(owner.ID), cookie(reviewer.ID), cookie(foreign.ID)
	asset, err := repo.CreateAsset(ctx, ws.ID, "Synthetic customer_email dataset", "Fixture CRM", "Governance")
	must(err)
	var requests atomic.Int64
	var chat atomic.Int64
	var responses atomic.Int64
	const fixtureKey = "fixture-model-token-only"
	candidate := `{"category":"personal","businessLevel":"confidential","rationale":"Synthetic field metadata suggests contact information; independent confirmation remains required."}`
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+fixtureKey {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "fixture-chat"}, {"id": "fixture-responses"}}})
			return
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 32<<10))
		if strings.Contains(string(raw), "must-not-leave") || strings.Contains(string(raw), "comments") {
			t.Error("outbound metadata crossed allowlist")
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			chat.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": candidate}}}})
		case "/v1/responses":
			responses.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": candidate}}}}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	upstreamURL, err := url.Parse(upstream.URL)
	must(err)
	manager, err := classificationai.NewWithRootCAs(strings.Repeat("ab", 32), upstreamURL.Host, roots)
	must(err)
	handler := NewWithClassificationAI(repo, "https://console.invalid", true, nil, manager)
	base := "/api/v1/workspaces/" + slug + "/classification"
	call := func(method, path string, body any, session string, status int) *httptest.ResponseRecorder {
		t.Helper()
		raw, e := json.Marshal(body)
		must(e)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://console.invalid")
		if session != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: session})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s got%d want%d", method, path, w.Code, status)
		}
		return w
	}
	call("GET", base+"/engine", nil, "", 401)
	var engine struct {
		Settings        store.ClassificationEngineSettings `json:"settings"`
		EncryptionReady bool                               `json:"encryptionReady"`
	}
	must(json.Unmarshal(call("GET", base+"/engine", nil, read, 200).Body.Bytes(), &engine))
	if engine.Settings.Mode != "local" || !engine.EncryptionReady || requests.Load() != 0 {
		t.Fatal("default engine performed external inference or reported incorrect capability")
	}
	payload := map[string]any{"name": "Private fixture Chat", "baseUrl": upstream.URL + "/v1/", "protocol": "chat_completions", "model": "fixture-chat", "enabled": true, "allowMetadata": false, "apiKey": fixtureKey}
	call("POST", base+"/ai/profiles", payload, read, 403)
	var profile store.ClassificationAIProfile
	resp := call("POST", base+"/ai/profiles", payload, own, 201)
	must(json.Unmarshal(resp.Body.Bytes(), &profile))
	if strings.Contains(resp.Body.String(), fixtureKey) || strings.Contains(resp.Body.String(), "encryptedAPIKey") || !profile.HasAPIKey {
		t.Fatal("model credential not write-only")
	}
	listed := call("GET", base+"/ai/profiles", nil, read, 200)
	if strings.Contains(listed.Body.String(), fixtureKey) {
		t.Fatal("profile listing exposed a credential")
	}
	internal, err := repo.GetClassificationAIProfileSecret(ctx, ws.ID, owner.ID, profile.ID)
	must(err)
	if len(internal.EncryptedAPIKey) == 0 || strings.Contains(string(internal.EncryptedAPIKey), fixtureKey) {
		t.Fatal("model credential was not encrypted")
	}
	suggest := map[string]any{"assetId": asset.ID, "revision": 0, "profileId": profile.ID}
	call("POST", base+"/ai/suggestions", suggest, own, 409)
	call("POST", base+"/ai/suggestions", suggest, read, 403)
	if requests.Load() != 0 {
		t.Fatal("local mode or read-only identity invoked provider")
	}
	settingsPayload := map[string]any{"mode": "hybrid", "defaultProfileId": profile.ID, "revision": 0}
	call("PUT", base+"/engine", settingsPayload, own, 400)
	profilePath := base + "/ai/profiles/" + fmt.Sprint(profile.ID)
	payload["allowMetadata"] = true
	payload["revision"] = profile.Revision
	delete(payload, "apiKey")
	must(json.Unmarshal(call("PUT", profilePath, payload, own, 200).Body.Bytes(), &profile))
	must(json.Unmarshal(call("PUT", base+"/engine", settingsPayload, own, 200).Body.Bytes(), &engine.Settings))
	call("POST", profilePath+"/test", nil, own, 200)
	if requests.Load() != 1 {
		t.Fatal("model-list test did not use registered connection")
	}
	preview := call("GET", base+"/ai/input?assetId="+fmt.Sprint(asset.ID)+"&revision=0", nil, own, 200)
	if strings.Contains(preview.Body.String(), "description") || strings.Contains(preview.Body.String(), "comments") || !strings.Contains(preview.Body.String(), "assetName") {
		t.Fatal("preview is not the exact restricted model input")
	}
	var aiSuggestion store.ClassificationAISuggestion
	must(json.Unmarshal(call("POST", base+"/ai/suggestions", suggest, own, 201).Body.Bytes(), &aiSuggestion))
	if aiSuggestion.ProfileID != profile.ID || aiSuggestion.AssetRevision != 0 || chat.Load() != 1 {
		t.Fatal("AI candidate lost profile or classification revision provenance")
	}
	unchanged, err := repo.GetAssetClassification(ctx, ws.ID, owner.ID, asset.ID)
	must(err)
	if unchanged.Revision != 0 || unchanged.Asset.Category != "unknown" {
		t.Fatal("AI candidate silently changed classification")
	}
	// A second configured model is freely selectable without changing the
	// workspace's default profile. Its different wire protocol is exercised.
	secondPayload := map[string]any{"name": "Private fixture Responses", "baseUrl": upstream.URL + "/v1", "protocol": "responses", "model": "fixture-responses", "enabled": true, "allowMetadata": true, "apiKey": fixtureKey}
	var second store.ClassificationAIProfile
	must(json.Unmarshal(call("POST", base+"/ai/profiles", secondPayload, own, 201).Body.Bytes(), &second))
	suggest["profileId"] = second.ID
	must(json.Unmarshal(call("POST", base+"/ai/suggestions", suggest, own, 201).Body.Bytes(), &aiSuggestion))
	if aiSuggestion.ProfileID != second.ID || responses.Load() != 1 {
		t.Fatal("user-selected alternate model or Responses protocol did not execute")
	}
	call("GET", base+"/ai/suggestions?assetId="+fmt.Sprint(asset.ID), nil, read, 200)
	call("GET", base+"/ai/profiles", nil, otherCookie, 404)
	// Changing a destination cannot silently carry its saved key to a new URL.
	payload["baseUrl"] = upstream.URL + "/different"
	payload["revision"] = profile.Revision
	call("PUT", profilePath, payload, own, 400)
	payload["baseUrl"] = upstream.URL + "/v1"
	payload["clearApiKey"] = true
	must(json.Unmarshal(call("PUT", profilePath, payload, own, 200).Body.Bytes(), &profile))
	if profile.HasAPIKey {
		t.Fatal("explicit credential clearing ignored")
	}
	count := requests.Load()
	call("PUT", base+"/engine", map[string]any{"mode": "local", "defaultProfileId": nil, "revision": engine.Settings.Revision}, own, 200)
	call("POST", base+"/ai/suggestions", suggest, own, 409)
	if requests.Load() != count {
		t.Fatal("local engine mode did not stop external model calls")
	}
}
