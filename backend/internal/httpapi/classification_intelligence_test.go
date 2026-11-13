package httpapi

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"github.com/hjh006/scarlet-plume/backend/internal/classificationai"
	"github.com/hjh006/scarlet-plume/backend/internal/classificationml"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClassificationKnowledgeAndLocalTrainingHTTP(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
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
	slug := fmt.Sprintf("intelligence-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Intelligence fixture"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reviewer", "unused"))
	must(repo.Bootstrap(ctx, slug+"-foreign", "unused", slug+"-foreign", "Foreign"))
	owner, e := repo.FindUser(ctx, slug)
	must(e)
	reader, e := repo.FindUser(ctx, slug+"-reviewer")
	must(e)
	foreign, e := repo.FindUser(ctx, slug+"-foreign")
	must(e)
	ws, e := repo.Membership(ctx, owner.ID, slug)
	must(e)
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, e := pool.Exec(c, "DELETE FROM plume.workspaces WHERE slug=ANY($1)", []string{slug, slug + "-foreign"})
		if e != nil {
			t.Error(e)
		}
		_, e = pool.Exec(c, "DELETE FROM plume.users WHERE id=ANY($1)", []int64{owner.ID, reader.ID, foreign.ID})
		if e != nil {
			t.Error(e)
		}
	})
	cookie := func(id int64) string {
		token, e := sessionToken()
		must(e)
		must(repo.CreateSession(ctx, id, sessionHash(token), time.Now().Add(time.Hour)))
		return token
	}
	own, read, other := cookie(owner.ID), cookie(reader.ID), cookie(foreign.ID)
	asset, e := repo.CreateAsset(ctx, ws.ID, "customer_email personal address", "Synthetic CRM", "Security")
	must(e)
	var revision int64
	must(pool.QueryRow(ctx, "SELECT classification_revision FROM plume.assets WHERE id=$1", asset.ID).Scan(&revision))
	var upstreamCalls atomic.Int64
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if !strings.Contains(string(raw), "references") || !strings.Contains(string(raw), "Synthetic contact policy") {
			t.Error("actual augmented request lost its selected reference")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"category\":\"personal\",\"businessLevel\":\"internal\",\"rationale\":\"Contact metadata and the selected reference support a human review.\"}"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(upstream.Close)
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	parsed, e := url.Parse(upstream.URL)
	must(e)
	manager, e := classificationai.NewWithRootCAs(strings.Repeat("ab", 32), parsed.Host, roots)
	must(e)
	handler := NewWithClassificationAI(repo, "https://console.invalid", true, nil, manager)
	base := "/api/v1/workspaces/" + slug + "/classification"
	call := func(method, path string, body any, token string, status int) *httptest.ResponseRecorder {
		t.Helper()
		raw, e := json.Marshal(body)
		must(e)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://console.invalid")
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d want%d %.250s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	call("GET", base+"/knowledge", nil, "", 401)
	docInput := store.ClassificationKnowledgeInput{Name: "Synthetic contact policy", SourceURL: "https://standards.example.test/contact", Content: "customer email personal address metadata requires human confirmation. This synthetic policy is not a statutory category.", Enabled: true, AllowAI: false}
	call("POST", base+"/knowledge", docInput, read, 403)
	var doc store.ClassificationKnowledge
	must(json.Unmarshal(call("POST", base+"/knowledge", docInput, own, 201).Body.Bytes(), &doc))
	scope := map[string]any{"assetId": asset.ID, "revision": revision}
	var preview struct {
		References []store.ClassificationKnowledgeReference `json:"references"`
	}
	must(json.Unmarshal(call("POST", base+"/knowledge/search", scope, own, 200).Body.Bytes(), &preview))
	if len(preview.References) != 1 || !strings.Contains(doc.Content, preview.References[0].Excerpt) {
		t.Fatal("no exact versioned retrieval")
	}
	profile, e := repo.SaveClassificationAIProfile(ctx, ws.ID, owner.ID, 0, store.ClassificationAIProfileInput{Name: "Fixture", BaseURL: upstream.URL + "/v1", Protocol: "chat_completions", Model: "fixture", Enabled: true, AllowMetadata: true})
	must(e)
	_, e = repo.SaveClassificationEngineSettings(ctx, ws.ID, owner.ID, store.ClassificationEngineSettings{Mode: "hybrid", DefaultProfileID: &profile.ID})
	must(e)
	request := func(d store.ClassificationKnowledge, allow bool) map[string]any {
		return map[string]any{"assetId": asset.ID, "revision": revision, "profileId": profile.ID, "allowKnowledge": allow, "references": []store.ClassificationKnowledgeSelection{{ID: d.ID, Revision: d.Revision, Digest: d.Digest}}}
	}
	call("POST", base+"/knowledge/suggest", request(doc, false), own, 403)
	call("POST", base+"/knowledge/suggest", request(doc, true), own, 403)
	docInput.Revision = doc.Revision
	docInput.AllowAI = true
	var allowed store.ClassificationKnowledge
	must(json.Unmarshal(call("PUT", base+"/knowledge/"+fmt.Sprint(doc.ID), docInput, own, 200).Body.Bytes(), &allowed))
	call("POST", base+"/knowledge/suggest", request(doc, true), own, 409)
	if upstreamCalls.Load() != 0 {
		t.Fatal("unapproved or obsolete reference reached the model")
	}
	var suggestion store.ClassificationAISuggestion
	must(json.Unmarshal(call("POST", base+"/knowledge/suggest", request(allowed, true), own, 201).Body.Bytes(), &suggestion))
	if upstreamCalls.Load() != 1 || len(suggestion.KnowledgeContext) != 1 || suggestion.KnowledgeContext[0].Revision != allowed.Revision {
		t.Fatal("augmented suggestion not frozen")
	}
	docInput.Revision = allowed.Revision
	docInput.Content = "replacement text"
	docInput.Enabled = false
	call("PUT", base+"/knowledge/"+fmt.Sprint(doc.ID), docInput, own, 200)
	var history struct {
		Items []store.ClassificationAISuggestion
	}
	must(json.Unmarshal(call("GET", base+"/ai/suggestions?assetId="+fmt.Sprint(asset.ID), nil, own, 200).Body.Bytes(), &history))
	if len(history.Items) != 1 || history.Items[0].KnowledgeContext[0].Excerpt != suggestion.KnowledgeContext[0].Excerpt {
		t.Fatal("knowledge edit rewrote old suggestion")
	}
	examples := []classificationml.Example{
		{Text: "invoice order amount product catalog", Category: "business", Split: "train"}, {Text: "product stock inventory quantity", Category: "business", Split: "train"}, {Text: "invoice billing order revenue", Category: "business", Split: "train"},
		{Text: "customer email contact personal", Category: "personal", Split: "train"}, {Text: "email phone person name", Category: "personal", Split: "train"}, {Text: "customer phone address personal", Category: "personal", Split: "train"},
		{Text: "customer email personal address", Category: "personal", Split: "evaluation"}, {Text: "product invoice stock order", Category: "business", Split: "evaluation"},
	}
	training := store.ClassificationTrainingInput{Name: "Synthetic metadata model", MinScore: 0.65, Examples: examples}
	call("POST", base+"/training/models", training, read, 403)
	var model store.ClassificationTrainingModel
	must(json.Unmarshal(call("POST", base+"/training/models", training, own, 201).Body.Bytes(), &model))
	if model.TrainCount != 6 || model.Evaluation.Count != 2 || model.Evaluation.Accuracy != 1 || len(model.ModelDigest) != 64 {
		t.Fatal("training/holdout result missing")
	}
	call("GET", base+"/training/models/"+fmt.Sprint(model.ID), nil, other, 404)
	var prediction store.ClassificationTrainingPrediction
	must(json.Unmarshal(call("POST", base+"/training/models/"+fmt.Sprint(model.ID)+"/predict", scope, own, 201).Body.Bytes(), &prediction))
	if prediction.Category != "personal" || prediction.AssetRevision != revision || len(prediction.MatchedTokens) == 0 {
		t.Fatal("real asset model prediction missing")
	}
	var unchanged int64
	must(pool.QueryRow(ctx, "SELECT classification_revision FROM plume.assets WHERE id=$1", asset.ID).Scan(&unchanged))
	if unchanged != revision {
		t.Fatal("model overwrote classification")
	}
	training.Examples[6].Text = training.Examples[0].Text
	call("POST", base+"/training/models", training, own, 400)
}
