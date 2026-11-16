package aisecurity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const sourceTestToken = "synthetic-document-source-credential-00001"

func sourceTestReceipt(content string) map[string]any {
	h := sha256.Sum256([]byte(content))
	return map[string]any{"profile": DocumentSourceProtocolVersion, "applicationId": "app", "principalId": "alice", "documents": []map[string]any{{"id": "doc", "version": "v1", "aclVersion": "acl1", "content": content, "contentDigest": hex.EncodeToString(h[:]), "authorized": true, "authorizationExpiresAt": time.Now().Add(time.Minute)}}}
}

func TestHTTPDocumentSourceRequiresBoundedCurrentSourceAuthorization(t *testing.T) {
	mode := "safe"
	escaped := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/escaped" {
			escaped++
			return
		}
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+sourceTestToken {
			t.Error("unbound source request")
		}
		var query struct {
			ApplicationID string   `json:"applicationId"`
			PrincipalID   string   `json:"principalId"`
			DocumentIDs   []string `json:"documentIds"`
		}
		if json.NewDecoder(r.Body).Decode(&query) != nil || query.ApplicationID != "app" || query.PrincipalID != "alice" || len(query.DocumentIDs) != 1 || query.DocumentIDs[0] != "doc" {
			t.Error("source saw caller selected or missing identity/scope")
		}
		out := sourceTestReceipt("Synthetic source\nwith ordinary line breaks")
		doc := out["documents"].([]map[string]any)[0]
		switch mode {
		case "wrong_workspace":
			out["workspaceSlug"] = "other-tenant"
		case "wrong_profile":
			out["profile"] = "unreviewed-source-v2"
		case "wrong_identity":
			out["principalId"] = "bob"
		case "wrong_document":
			doc["id"] = "other"
		case "changed_content":
			doc["content"] = "changed"
		case "not_authorized":
			doc["authorized"] = false
		case "stale":
			doc["authorizationExpiresAt"] = time.Now().Add(-time.Second)
		case "unbounded_lease":
			doc["authorizationExpiresAt"] = time.Now().Add(time.Hour)
		case "missing_acl":
			delete(doc, "aclVersion")
		case "missing_document":
			out["documents"] = []any{}
		case "duplicate":
			out["documents"] = []any{doc, doc}
		case "redirect":
			w.Header().Set("Location", "/escaped")
			w.WriteHeader(302)
			return
		case "compressed":
			w.Header().Set("Content-Encoding", "gzip")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer server.Close()
	resolve, e := NewHTTPDocumentSource(server.URL+"/resolve", sourceTestToken, true)
	if e != nil {
		t.Fatal(e)
	}
	identity := Identity{ApplicationID: "app", PrincipalID: "alice"}
	if docs, e := resolve(t.Context(), identity, []string{"doc"}); e != nil || !strings.Contains(docs["doc"].Content, "\n") || docs["doc"].ACLVersion != "acl1" {
		t.Fatal("valid source grant rejected", docs, e)
	}
	for _, value := range []string{"wrong_workspace", "wrong_profile", "wrong_identity", "wrong_document", "changed_content", "not_authorized", "stale", "unbounded_lease", "missing_acl", "missing_document", "duplicate", "redirect", "compressed"} {
		mode = value
		if _, e := resolve(t.Context(), identity, []string{"doc"}); e == nil {
			t.Fatal("invalid source grant accepted", value)
		}
	}
	if escaped != 0 {
		t.Fatal("source redirect followed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e := resolve(ctx, identity, []string{"doc"}); e == nil {
		t.Fatal("cancelled source read continued")
	}
	if _, e := NewHTTPDocumentSource(server.URL, sourceTestToken, false); e == nil {
		t.Fatal("production plaintext source accepted")
	}
	if _, e := resolve(t.Context(), identity, []string{"doc", "doc"}); e == nil {
		t.Fatal("duplicate source query accepted")
	}
}

func TestHTTPSourceWorkspaceCannotBeSelectedByTheCaller(t *testing.T) {
	var mismatch atomic.Bool
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var query map[string]any
		_ = json.NewDecoder(r.Body).Decode(&query)
		if query["workspaceSlug"] != "tenant-a" || query["profile"] != DocumentSourceProtocolVersion {
			t.Error("source missing fixed workspace/profile binding")
		}
		out := sourceTestReceipt("synthetic workspace content")
		out["workspaceSlug"] = "tenant-a"
		if mismatch.Load() {
			out["workspaceSlug"] = "tenant-b"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer source.Close()
	resolve, e := NewHTTPDocumentSourceForWorkspace(source.URL, sourceTestToken, "tenant-a", true)
	if e != nil {
		t.Fatal(e)
	}
	identity := Identity{ApplicationID: "app", PrincipalID: "alice"}
	if _, e := resolve(t.Context(), identity, []string{"doc"}); e != nil {
		t.Fatal(e)
	}
	mismatch.Store(true)
	if _, e := resolve(t.Context(), identity, []string{"doc"}); e == nil {
		t.Fatal("source response crossed workspace")
	}
	if _, e := NewHTTPDocumentSource("https://source.example.invalid/resolve", sourceTestToken, false); e == nil {
		t.Fatal("production source allowed no workspace binding")
	}
}

func TestHTTPSourceActualInjectionAndRevocationSuppressProviderOutput(t *testing.T) {
	const content = "Synthetic enterprise knowledge\nwith a second line"
	var revoked atomic.Bool
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := sourceTestReceipt(content)
		if revoked.Load() {
			out["documents"].([]map[string]any)[0]["authorized"] = false
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer source.Close()
	resolve, e := NewHTTPDocumentSource(source.URL, sourceTestToken, true)
	if e != nil {
		t.Fatal(e)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("provider request invalid")
		}
		found := false
		for _, message := range body.Messages {
			if strings.Contains(message.Content, "Synthetic enterprise knowledge") && strings.Contains(message.Content, "contentDigest") {
				found = true
			}
		}
		if !found {
			t.Error("actual authorized source was not injected")
		}
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"must remain private"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	c := fixture()
	c.Models[0].ResponseFormat = "openai_chat"
	c.Policy.RequirePinnedSourceContent = true
	h := sha256.Sum256([]byte(content))
	c.Documents[0].IntegrityDigest = hex.EncodeToString(h[:])
	runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	o := runtime.(*runtimeHandler).options
	o.ResolveDocuments = resolve
	handler, e := NewRuntime(o)
	if e != nil {
		t.Fatal(e)
	}
	r := testRequest("model")
	r.ModelID = "enterprise"
	r.DocumentIDs = []string{"doc"}
	r.Input = `{"messages":[{"role":"user","content":"summarize permitted source"}]}`
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- callRuntime(t, handler, aliceToken, wire(r)) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("model did not receive actual source")
	}
	revoked.Store(true)
	close(release)
	response := <-result
	if response.Code != 403 || strings.Contains(response.Body.String(), "must remain private") || hits.Load() != 1 {
		t.Fatal("source revocation did not suppress actual result", response.Code, response.Body.String())
	}
}
