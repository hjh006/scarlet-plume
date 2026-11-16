package aisecurity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const aliceToken = "synthetic-client-token-alice-0000001"
const bobToken = "synthetic-client-token-bob-000000001"

func credential(token, principal string) Credential {
	s := sha256.Sum256([]byte(token))
	return Credential{TokenHash: hex.EncodeToString(s[:]), Identity: Identity{ApplicationID: "app", PrincipalID: principal}}
}
func wire(r Request) RuntimeRequest {
	return RuntimeRequest{TaskID: r.TaskID, RequestID: r.RequestID, Kind: r.Kind, DocumentIDs: r.DocumentIDs, ModelID: r.ModelID, ToolID: r.ToolID, Action: r.Action, Target: r.Target, Arguments: r.Arguments, Input: r.Input, ApprovalID: r.ApprovalID, IssuedAt: r.IssuedAt, ExpiresAt: r.ExpiresAt}
}
func callRuntime(t *testing.T, h http.Handler, token string, r any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(r)
	request := httptest.NewRequest("POST", "/v1/execute", bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	h.ServeHTTP(out, request)
	return out
}
func runtimeFixture(t *testing.T, catalog func(context.Context) (Config, error), target string, audit func(context.Context, AuditRecord) error, docs func(context.Context) (map[string]LocalDocument, error)) (http.Handler, *ReplayJournal) {
	t.Helper()
	j, e := OpenReplayJournal(filepath.Join(t.TempDir(), "replay"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { j.Close() })
	if audit == nil {
		audit = func(context.Context, AuditRecord) error { return nil }
	}
	h, e := NewRuntime(RuntimeOptions{Catalog: catalog, Credentials: []Credential{credential(aliceToken, "alice"), credential(bobToken, "bob")}, Targets: map[string]Target{"model": {URL: target}, "tools": {URL: target}}, Documents: docs, Audit: audit, Journal: j, AllowLoopback: true})
	if e != nil {
		t.Fatal(e)
	}
	return h, j
}

func TestRuntimeAuditSeparatesAttemptsWhileReplayRemainsBlocked(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"result":"ok"}`)
	}))
	defer up.Close()
	cfg := fixture()
	records := []AuditRecord{}
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return cfg, nil }, up.URL, func(_ context.Context, r AuditRecord) error { records = append(records, r); return nil }, nil)
	r := testRequest("model")
	r.ModelID = "enterprise"
	r.Input = `{"prompt":"hello"}`
	if out := callRuntime(t, h, aliceToken, wire(r)); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	if out := callRuntime(t, h, aliceToken, wire(r)); out.Code != 409 {
		t.Fatal("replay admitted", out.Code)
	}
	if hits.Load() != 1 || len(records) != 4 {
		t.Fatalf("unexpected attempts: %d %+v", hits.Load(), records)
	}
	if len(records[0].AttemptID) != 32 || records[0].AttemptID != records[1].AttemptID || records[0].AttemptID != records[2].AttemptID || records[0].AttemptID == records[3].AttemptID {
		t.Fatal("execution phases or replay attempts share the wrong reference")
	}
	for _, record := range records {
		if record.RuntimeProtocolVersion != RuntimeProtocolVersion || record.InspectionVersion != InspectionVersion {
			t.Fatal("version metadata missing")
		}
	}
}
func TestRuntimeDeniedRequestsNeverReachUpstream(t *testing.T) {
	var hit atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()
	cfg := fixture()
	provider := func(context.Context) (Config, error) { return cfg, nil }
	h, _ := runtimeFixture(t, provider, up.URL, nil, nil)
	r := testRequest("model")
	r.Input = `{"prompt":"hello"}`
	r.ModelID = "absent"
	if out := callRuntime(t, h, aliceToken, wire(r)); out.Code != 403 {
		t.Fatal(out.Code, out.Body.String())
	}
	r.ModelID = "enterprise"
	r.Input = `{"prompt":"private@example.com"}`
	if out := callRuntime(t, h, aliceToken, wire(r)); out.Code != 403 {
		t.Fatal(out.Code, out.Body.String())
	}
	if hit.Load() != 0 {
		t.Fatal("denied request reached upstream")
	}
	bad := map[string]any{"kind": "model", "principalId": "bob", "modelId": "enterprise"}
	if out := callRuntime(t, h, aliceToken, bad); out.Code != 400 {
		t.Fatal("caller-selected principal accepted", out.Code)
	}
	bad = map[string]any{"kind": "model", "upstream": "https://attacker.invalid"}
	if out := callRuntime(t, h, aliceToken, bad); out.Code != 400 {
		t.Fatal("caller-selected upstream accepted")
	}
}
func TestRuntimeModelForwardOutputAndAudits(t *testing.T) {
	var hit atomic.Int32
	var body string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		if r.Header.Get("Authorization") != "" {
			t.Error("client credential forwarded")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"reply":"safe summary"}`)
	}))
	defer up.Close()
	cfg := fixture()
	events := []AuditRecord{}
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return cfg, nil }, up.URL, func(_ context.Context, a AuditRecord) error { events = append(events, a); return nil }, nil)
	r := testRequest("model")
	r.ModelID = "enterprise"
	r.Input = `{"prompt":"summarize synthetic business material"}`
	out := callRuntime(t, h, aliceToken, wire(r))
	if out.Code != 200 || hit.Load() != 1 || !strings.Contains(body, "synthetic business material") || !strings.Contains(body, `"model":"enterprise"`) {
		t.Fatal(out.Code, out.Body.String(), hit.Load())
	}
	if len(events) != 3 || events[2].Execution.Outcome != "succeeded" || !events[2].Execution.Attempted {
		t.Fatal(events)
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "safe summary") || strings.Contains(string(raw), "synthetic business material") || strings.Contains(string(raw), aliceToken) {
		t.Fatal("audit leaked source content")
	}
	if out = callRuntime(t, h, aliceToken, wire(r)); out.Code != 409 || hit.Load() != 1 {
		t.Fatal("model nonce replayed", out.Code)
	}
	// Real upstream output is inspected, not a caller-provided output assertion.
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"reply":"person@example.com"}`) }))
	defer blocked.Close()
	hh, _ := runtimeFixture(t, func(context.Context) (Config, error) { return cfg, nil }, blocked.URL, nil, nil)
	r.RequestID = "second-request-123456"
	out = callRuntime(t, hh, aliceToken, wire(r))
	if out.Code != 403 || strings.Contains(out.Body.String(), "person@example.com") {
		t.Fatal(out.Code, out.Body.String())
	}
}
func TestRuntimeRAGACLBeforeReadRevocationAndActualContent(t *testing.T) {
	cfg := fixture()
	var mu sync.Mutex
	provider := func(context.Context) (Config, error) { mu.Lock(); defer mu.Unlock(); return clone(cfg), nil }
	var reads atomic.Int32
	docstore := func(context.Context) (map[string]LocalDocument, error) {
		reads.Add(1)
		return map[string]LocalDocument{"doc": {ID: "doc", Version: "v1", Content: "Synthetic acquisition notes"}}, nil
	}
	h, _ := runtimeFixture(t, provider, "http://127.0.0.1:1", nil, docstore)
	r := testRequest("rag")
	r.DocumentIDs = []string{"doc"}
	if out := callRuntime(t, h, bobToken, wire(r)); out.Code != 403 || reads.Load() != 0 {
		t.Fatal("ACL did not precede retrieval", out.Code)
	}
	if out := callRuntime(t, h, aliceToken, wire(r)); out.Code != 200 || !strings.Contains(out.Body.String(), "Synthetic acquisition notes") {
		t.Fatal(out.Code, out.Body.String())
	}
	readsBeforeRevocation := reads.Load()
	mu.Lock()
	cfg.Documents[0].ACLVersion = "acl2"
	cfg.Documents[0].AllowedPrincipalIDs = []string{}
	mu.Unlock()
	r.RequestID = "revoked-request-12345"
	if out := callRuntime(t, h, aliceToken, wire(r)); out.Code != 403 || reads.Load() != readsBeforeRevocation {
		t.Fatal("revoked ACL retrieved content", out.Code, reads.Load())
	}
}
func TestRuntimeToolReplaySurvivesRestartAndApprovalBindsArgs(t *testing.T) {
	var hit atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Add(1); io.WriteString(w, `{"closed":true}`) }))
	defer up.Close()
	cfg := fixture()
	r := testRequest("tool")
	r.ToolID = "ticket"
	r.Action = "close"
	r.Target = "ticket-17"
	r.Arguments = map[string]string{"resolution": "fixed"}
	approve(&cfg, &r)
	path := filepath.Join(t.TempDir(), "journal")
	j, e := OpenReplayJournal(path)
	if e != nil {
		t.Fatal(e)
	}
	provider := func(context.Context) (Config, error) { return cfg, nil }
	makeHandler := func(j *ReplayJournal) http.Handler {
		h, e := NewRuntime(RuntimeOptions{Catalog: provider, Credentials: []Credential{credential(aliceToken, "alice")}, Targets: map[string]Target{"tools": {URL: up.URL}}, Audit: func(context.Context, AuditRecord) error { return nil }, Journal: j, AllowLoopback: true})
		if e != nil {
			t.Fatal(e)
		}
		return h
	}
	h := makeHandler(j)
	changed := r
	changed.Arguments = map[string]string{"resolution": "duplicate"}
	if out := callRuntime(t, h, aliceToken, wire(changed)); out.Code != 403 || hit.Load() != 0 {
		t.Fatal("modified approved args executed")
	}
	if out := callRuntime(t, h, aliceToken, wire(r)); out.Code != 200 || hit.Load() != 1 {
		t.Fatal(out.Code, out.Body.String())
	}
	j.Close()
	j, e = OpenReplayJournal(path)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	if out := callRuntime(t, makeHandler(j), aliceToken, wire(r)); out.Code != 409 || hit.Load() != 1 {
		t.Fatal("restart replay caused duplicate side effect", out.Code)
	}
}
func TestAuditFailureBeforeAndAfterSideEffect(t *testing.T) {
	var hit atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Add(1); io.WriteString(w, `{"ok":true}`) }))
	defer up.Close()
	cfg := fixture()
	r := testRequest("model")
	r.ModelID = "enterprise"
	r.Input = `{"prompt":"safe"}`
	for _, stage := range []string{"prepared", "completed"} {
		t.Run(stage, func(t *testing.T) {
			before := hit.Load()
			h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return cfg, nil }, up.URL, func(_ context.Context, a AuditRecord) error {
				if a.Stage == stage {
					return errors.New("disk unavailable")
				}
				return nil
			}, nil)
			out := callRuntime(t, h, aliceToken, wire(r))
			want := int32(0)
			if stage == "completed" {
				want = 1
			}
			if out.Code != 503 || hit.Load()-before != want {
				t.Fatal(out.Code, hit.Load()-before, want)
			}
			if out = callRuntime(t, h, aliceToken, wire(r)); out.Code != 409 || hit.Load()-before != want {
				t.Fatal("audit failure retried a consumed operation", out.Code)
			}
		})
	}
}
func TestRuntimeRedirectEncodingOversizeAndJournalFailure(t *testing.T) {
	var redirectHits atomic.Int32
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirectHits.Add(1) }))
	defer dest.Close()
	cfg := fixture()
	r := testRequest("model")
	r.ModelID = "enterprise"
	r.Input = `{"prompt":"safe"}`
	for _, kind := range []string{"redirect", "oversize", "encoding"} {
		t.Run(kind, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch kind {
				case "redirect":
					http.Redirect(w, req, dest.URL, http.StatusTemporaryRedirect)
				case "oversize":
					io.WriteString(w, strings.Repeat("x", cfg.Policy.MaxOutputBytes+1))
				case "encoding":
					w.Header().Set("Content-Encoding", "gzip")
					io.WriteString(w, `{"ok":true}`)
				}
			}))
			defer up.Close()
			h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return cfg, nil }, up.URL, nil, nil)
			if out := callRuntime(t, h, aliceToken, wire(r)); out.Code < 400 {
				t.Fatal("unsafe upstream response released", kind, out.Code)
			}
		})
	}
	if redirectHits.Load() != 0 {
		t.Fatal("redirect followed")
	}
	if validTarget("http://example.invalid", true) || validTarget("http://127.0.0.1:80", false) || validTarget("https://user:password@example.invalid", false) {
		t.Fatal("invalid static target allowed")
	}
	j, e := OpenReplayJournal(filepath.Join(t.TempDir(), "j"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = OpenReplayJournal(strings.TrimSuffix(j.lockPath, ".lock")); e == nil {
		t.Fatal("concurrent process journal accepted")
	}
	j.Close()
}
func TestPrivateDurableAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	appendEvent, closeFile, e := AppendAudit(path)
	if e != nil {
		t.Fatal(e)
	}
	a := AuditRecord{EventID: strings.Repeat("a", 32), RequestID: "request-1234567890", ObservedAt: time.Now(), Stage: "decision", Decision: Decision{Outcome: "deny", Code: "document_acl_denied"}, Execution: Execution{Outcome: "not_attempted"}}
	if e = appendEvent(a); e != nil {
		t.Fatal(e)
	}
	closeFile()
	raw, e := os.ReadFile(path)
	if e != nil || !json.Valid(bytes.TrimSpace(raw)) {
		t.Fatal(fmt.Sprint(e))
	}
}

func TestRuntimeEnforcesActualModelSelectorAndNarrowTextProtocol(t *testing.T) {
	var hits atomic.Int32
	actual := ""
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		actual, _ = body["model"].(string)
		io.WriteString(w, `{"reply":"ok"}`)
	}))
	defer up.Close()
	cfg := fixture()
	cfg.Models[0].UpstreamModel = "approved-deployment-v1"
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return cfg, nil }, up.URL, nil, nil)
	r := testRequest("model")
	r.ModelID = "enterprise"
	for i, payload := range []string{`{"model":"unapproved","prompt":"safe"}`, `{"model":"approved-deployment-v1","prompt":"safe","deployment":"unapproved"}`, `{"prompt":"safe","provider":{"name":"other"}}`, `{"prompt":"safe","tools":[]}`, `{"prompt":"safe","stream":true}`, `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://other.invalid"}}]}]}`} {
		r.RequestID = fmt.Sprintf("model-protocol-test-%d", i)
		r.Input = payload
		if out := callRuntime(t, h, aliceToken, wire(r)); (out.Code != 422 && out.Code != 403) || hits.Load() != 0 {
			t.Fatal("unsupported model routing reached upstream", out.Code, out.Body.String())
		}
	}
	r.RequestID = "model-selector-good-123"
	r.Input = `{"messages":[{"role":"user","content":"safe"}]}`
	if out := callRuntime(t, h, aliceToken, wire(r)); out.Code != 200 || hits.Load() != 1 || actual != "approved-deployment-v1" {
		t.Fatal(out.Code, out.Body.String(), actual)
	}
}

func TestRAGIntegrityPinRejectsContentChangeWithSameVersion(t *testing.T) {
	cfg := fixture()
	sum := sha256.Sum256([]byte("approved content"))
	cfg.Documents[0].IntegrityDigest = hex.EncodeToString(sum[:])
	content := "approved content"
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return cfg, nil }, "http://127.0.0.1:1", nil, func(context.Context) (map[string]LocalDocument, error) {
		return map[string]LocalDocument{"doc": {ID: "doc", Version: "v1", Content: content}}, nil
	})
	r := testRequest("rag")
	r.DocumentIDs = []string{"doc"}
	if out := callRuntime(t, h, aliceToken, wire(r)); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	content = "replaced without version change"
	r.RequestID = "content-tamper-request-123"
	if out := callRuntime(t, h, aliceToken, wire(r)); out.Code != 409 || strings.Contains(out.Body.String(), content) {
		t.Fatal(out.Code, out.Body.String())
	}
}
