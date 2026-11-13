package httpapi

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationai"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDataSecurityHTTPFrozenCasesConsentAndSourceBoundaries(t *testing.T) {
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
	t.Cleanup(pool.Close)
	repo := store.New(pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("data-events-http-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Data events"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reviewer", "unused"))
	must(repo.Bootstrap(ctx, slug+"-foreign", "unused", slug+"-foreign", "Foreign"))
	owner, err := repo.FindUser(ctx, slug)
	must(err)
	reviewer, err := repo.FindUser(ctx, slug+"-reviewer")
	must(err)
	foreign, err := repo.FindUser(ctx, slug+"-foreign")
	must(err)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE slug=ANY($1)`, []string{slug, slug + "-foreign"})
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE id=ANY($1)`, []int64{owner.ID, reviewer.ID, foreign.ID})
	})
	session := func(id int64) string {
		v, e := sessionToken()
		must(e)
		must(repo.CreateSession(ctx, id, sessionHash(v), time.Now().Add(time.Hour)))
		return v
	}
	ownerCookie, reviewerCookie, foreignCookie := session(owner.ID), session(reviewer.ID), session(foreign.ID)
	var modelCalls atomic.Int64
	model := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Messages) != 2 {
			t.Error("invalid model request")
			w.WriteHeader(400)
			return
		}
		for _, secret := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), "destinationDomain", "fileDigest", "deviceRef", "subjectRef"} {
			if strings.Contains(body.Messages[1].Content, secret) {
				t.Error("identity/object details sent to model")
			}
		}
		var facts []classificationai.EventFact
		if json.Unmarshal([]byte(body.Messages[1].Content), &facts) != nil || len(facts) != 2 {
			t.Error("invalid facts projection")
			w.WriteHeader(400)
			return
		}
		result := classificationai.EventAssessment{Summary: "合成协议：核对同来源事件，实际意图未知。", Severity: "medium", EvidenceIDs: []int64{facts[0].EventID}, Uncertainties: []string{"合成协议结果不代表生产模型识别效果"}, Recommendations: []string{"由授权人员核对原事件"}}
		content, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": string(content)}}}})
	}))
	defer model.Close()
	roots := x509.NewCertPool()
	roots.AddCert(model.Certificate())
	manager, err := classificationai.NewWithRootCAs(strings.Repeat("ab", 32), strings.TrimPrefix(model.URL, "https://"), roots)
	must(err)
	profile, err := repo.SaveClassificationAIProfile(ctx, ws.ID, owner.ID, 0, store.ClassificationAIProfileInput{Name: "Synthetic protocol fixture", BaseURL: model.URL + "/v1", Protocol: "chat_completions", Model: "fixture", Enabled: true, AllowMetadata: true})
	must(err)
	handler := NewWithClassificationAI(repo, "https://example.invalid", true, nil, manager)
	call := func(method, path, cookie, bearer string, body any, status int) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: cookie})
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if method != "GET" {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "https://example.invalid")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s status%d want%d body=%.300s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	sourceRandom, err := sessionToken()
	must(err)
	sourceToken := runtimeTokenPrefix + sourceRandom
	source, err := repo.CreateOperationsSource(ctx, ws.ID, owner.ID, store.OperationsSourceInput{Name: "Scoped DLP source", Kind: "security_events", OwnerTeam: "Security"}, sessionHash(sourceToken))
	must(err)
	now := time.Now().UTC()
	base := "/api/v1/workspaces/" + slug + "/data-security"
	c := datasafety.Context{Kind: "endpoint", Channel: "file_copy", SubjectRef: strings.Repeat("a", 64), DeviceRef: strings.Repeat("b", 64), FileDigest: strings.Repeat("c", 64), Categories: []string{"email"}, Operation: "copy", Enforcement: "blocked", Capture: "complete"}
	event := store.SecurityEventInput{DataContext: &c, Outcome: "observed", EventID: "fixture-copy", ObservedAt: now, Category: "data_leak", Rule: "dlp:protected", Severity: "high", Count: 1, WindowSeconds: 1}
	call("POST", securityEventsPath, "", sourceToken, event, 200)
	replay := call("POST", securityEventsPath, "", sourceToken, event, 200)
	if !strings.Contains(replay.Body.String(), `"replayed":true`) {
		t.Fatal("replay duplicated event")
	}
	ai := c
	ai.Kind, ai.Channel, ai.Operation, ai.DestinationDomain = "ai", "ai_prompt", "upload", "unapproved.example"
	event.DataContext = &ai
	event.EventID = "fixture-ai"
	event.Rule = "dlp:unapproved-ai"
	call("POST", securityEventsPath, "", sourceToken, event, 200)
	other := c
	other.SubjectRef = strings.Repeat("d", 64)
	event.DataContext = &other
	event.EventID = "fixture-other"
	call("POST", securityEventsPath, "", sourceToken, event, 200)
	bad := other
	bad.SubjectRef = "private@example.test"
	event.DataContext = &bad
	event.EventID = "fixture-bad"
	call("POST", securityEventsPath, "", sourceToken, event, 400)
	event.DataContext = nil
	event.EventID = "legacy-no-context"
	call("POST", securityEventsPath, "", sourceToken, event, 200)
	scope := store.DataSecurityScope{SourceID: source.ID, SubjectRef: c.SubjectRef, From: now.Add(-time.Minute), To: now.Add(time.Minute)}
	q := fmt.Sprintf("?sourceId=%d&subjectRef=%s&from=%s&to=%s", source.ID, c.SubjectRef, scope.From.Format(time.RFC3339Nano), scope.To.Format(time.RFC3339Nano))
	call("GET", base+"/events"+q, "", "", nil, 401)
	call("GET", base+"/events"+q, foreignCookie, "", nil, 404)
	var window store.DataSecurityWindow
	must(json.Unmarshal(call("GET", base+"/events"+q, reviewerCookie, "", nil, 200).Body.Bytes(), &window))
	if window.Total != 2 || len(window.Items) != 2 || !window.Items[0].SourceCurrent {
		t.Fatal("query scope was not applied before limit")
	}
	payload := map[string]any{"title": "fixture <script>unsafe</script>", "scope": scope}
	call("POST", base+"/cases", reviewerCookie, "", payload, 403)
	if _, e := repo.CreateDataSecurityCase(ctx, ws.ID, reviewer.ID, "forbidden", scope); e != store.ErrForbidden {
		t.Fatalf("Store reviewer bypass: %v", e)
	}
	var frozen store.DataSecurityCase
	must(json.Unmarshal(call("POST", base+"/cases", ownerCookie, "", payload, 201).Body.Bytes(), &frozen))
	if len(frozen.Snapshot.Chains) != 1 || len(frozen.Snapshot.Chains[0].Channels) != 2 {
		t.Fatal("same-source principal chain missing")
	}
	path := fmt.Sprintf("%s/cases/%d", base, frozen.ID)
	if e := repo.AddDataSecurityCaseFeedback(ctx, ws.ID, reviewer.ID, frozen.ID, "false_positive", "forbidden direct write"); e != store.ErrForbidden {
		t.Fatalf("Store reviewer feedback bypass: %v", e)
	}
	call("GET", path, foreignCookie, "", nil, 404)
	call("GET", path, reviewerCookie, "", nil, 200)
	call("POST", path+"/ai-reference", ownerCookie, "", map[string]any{"profileId": profile.ID, "allowMetadata": false, "caseDigest": frozen.SnapshotDigest}, 400)
	if modelCalls.Load() != 0 {
		t.Fatal("model called without consent")
	}
	call("POST", path+"/ai-reference", reviewerCookie, "", map[string]any{"profileId": profile.ID, "allowMetadata": true, "caseDigest": frozen.SnapshotDigest}, 403)
	call("POST", path+"/ai-reference", ownerCookie, "", map[string]any{"profileId": profile.ID, "allowMetadata": true, "caseDigest": strings.Repeat("0", 64)}, 409)
	call("POST", path+"/ai-reference", ownerCookie, "", map[string]any{"profileId": profile.ID, "allowMetadata": true, "caseDigest": frozen.SnapshotDigest}, 403)
	if modelCalls.Load() != 0 {
		t.Fatal("local-only mode contacted external model")
	}
	_, err = repo.SaveClassificationEngineSettings(ctx, ws.ID, owner.ID, store.ClassificationEngineSettingsInput{Mode: "hybrid", DefaultProfileID: &profile.ID, Revision: 0})
	must(err)
	call("POST", path+"/ai-reference", ownerCookie, "", map[string]any{"profileId": profile.ID, "allowMetadata": true, "caseDigest": frozen.SnapshotDigest}, 201)
	if modelCalls.Load() != 1 {
		t.Fatal("explicit model call missing")
	}
	call("POST", path+"/feedback", ownerCookie, "", map[string]string{"decision": "false_positive", "note": "Synthetic human feedback only"}, 204)
	var got store.DataSecurityCase
	must(json.Unmarshal(call("GET", path, ownerCookie, "", nil, 200).Body.Bytes(), &got))
	if got.SnapshotDigest != frozen.SnapshotDigest || len(got.Notes) != 2 {
		t.Fatal("notes overwrote frozen evidence")
	}
	html := call("GET", path+"/export?format=html", ownerCookie, "", nil, 200).Body.String()
	if strings.Contains(html, "<script>") || !strings.Contains(html, "&lt;script&gt;") {
		t.Fatal("export failed to escape case title")
	}
	call("GET", path+"/export?format=json", ownerCookie, "", nil, 200)
	must(repo.RevokeOperationsSource(ctx, ws.ID, owner.ID, source.ID))
	must(json.Unmarshal(call("GET", base+"/events"+q, ownerCookie, "", nil, 200).Body.Bytes(), &window))
	if window.Items[0].SourceCurrent {
		t.Fatal("revoked source remained current")
	}
	must(json.Unmarshal(call("GET", path, ownerCookie, "", nil, 200).Body.Bytes(), &got))
	if !got.Snapshot.Window.Items[0].SourceCurrent || got.SnapshotDigest != frozen.SnapshotDigest {
		t.Fatal("historical case silently changed")
	}
	_, err = pool.Exec(ctx, `INSERT INTO plume.data_security_case_notes(workspace_id,case_id,actor_id,kind,snapshot) SELECT $1,$2,$3,'feedback','{}'::jsonb FROM generate_series(1,198)`, ws.ID, frozen.ID, owner.ID)
	must(err)
	call("POST", path+"/feedback", ownerCookie, "", map[string]string{"decision": "needs_investigation", "note": "cannot silently disappear behind the read limit"}, 422)
	call("POST", path+"/ai-reference", ownerCookie, "", map[string]any{"profileId": profile.ID, "allowMetadata": true, "caseDigest": frozen.SnapshotDigest}, 422)
	if modelCalls.Load() != 1 {
		t.Fatal("full case called model before rejecting note budget")
	}
}
