package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func TestAIRuntimeRotationOneTimeCredentialsAndReceiptPaginationHTTP(t *testing.T) {
	f := newIdentityHTTPFixture(t)
	c := aisecurity.DefaultConfig()
	c.Applications = []aisecurity.Application{{ID: "app", Enabled: true}}
	digest := aisecurity.ConfigDigest(c)
	body, _ := json.Marshal(c)
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO plume.ai_security_configurations(workspace_id,revision,configuration,updated_by,published_revision,published_configuration,published_digest,published_by,published_at) VALUES($1,1,$2,$3,1,$2,$4,$3,now())`, f.ws.ID, body, f.owner.ID, digest); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/workspaces/" + f.ws.Slug + "/ai-security"
	created := identityHTTPRequest(t, f.handler, "POST", base+"/runtime-sources", gatewayHTTPJSON(t, store.AISecurityRuntimeSourceInput{Name: "Synthetic receipt source", ApplicationID: "app", CatalogDigest: digest}), f.cookie, "", 201)
	var source struct {
		Source store.AISecurityRuntimeSource `json:"source"`
		Token  string                        `json:"token"`
	}
	if json.Unmarshal(created.Body.Bytes(), &source) != nil || source.Source.TokenVersion != 1 || !strings.HasPrefix(source.Token, aiSecurityRuntimePrefix) || created.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("initial one-time credentials incomplete")
	}
	path := base + "/runtime-sources/" + strconv.FormatInt(source.Source.ID, 10) + "/rotate-token"
	identityHTTPRequest(t, f.handler, "POST", path, `{"expectedTokenVersion":1}`, nil, "", 403)
	identityHTTPRequest(t, f.handler, "POST", path, `{"expectedTokenVersion":1}`, f.viewerCookie, "", 403)
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"expectedTokenVersion":1}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(f.cookie)
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("rotation accepted without Origin", response.Code)
	}
	rotated := identityHTTPRequest(t, f.handler, "POST", path, `{"expectedTokenVersion":1}`, f.cookie, "", 200)
	var fresh struct {
		Source store.AISecurityRuntimeSource `json:"source"`
		Token  string                        `json:"token"`
	}
	if json.Unmarshal(rotated.Body.Bytes(), &fresh) != nil || fresh.Source.TokenVersion != 2 || fresh.Token == "" || fresh.Token == source.Token || rotated.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("rotation one-time credentials incomplete")
	}
	identityHTTPRequest(t, f.handler, "POST", path, `{"expectedTokenVersion":1}`, f.cookie, "", 409)
	r := aisecurity.AuditRecord{EventID: strings.Repeat("a", 32), RequestID: "synthetic-http-receipt", Stage: "completed", ObservedAt: time.Now().UTC(), RuntimeProtocolVersion: aisecurity.RuntimeProtocolVersion, InspectionVersion: aisecurity.InspectionVersion, Decision: aisecurity.Decision{Outcome: "allow", Code: "allowed", CatalogVersion: c.Version, CatalogDigest: digest, RequestDigest: strings.Repeat("c", 64), Kind: "model", ApplicationID: "app", PrincipalID: "principal", Rules: []string{}, Categories: []string{}, Coverage: []string{}, AllowedDocumentIDs: []string{}}, Execution: aisecurity.Execution{Attempted: true, Outcome: "succeeded", HTTPStatus: 200}}
	identityHTTPRequest(t, f.handler, "POST", aiSecurityRuntimePath, gatewayHTTPJSON(t, r), nil, source.Token, 401)
	identityHTTPRequest(t, f.handler, "POST", aiSecurityRuntimePath, gatewayHTTPJSON(t, r), nil, fresh.Token, 200)
	for i := 1; i <= 2; i++ {
		r.EventID = fmt.Sprintf("%032x", i)
		identityHTTPRequest(t, f.handler, "POST", aiSecurityRuntimePath, gatewayHTTPJSON(t, r), nil, fresh.Token, 200)
	}
	view := identityHTTPRequest(t, f.handler, "GET", base+"/runtime-evidence?limit=1", "", f.viewerCookie, "", 200)
	if strings.Contains(view.Body.String(), source.Token) || strings.Contains(view.Body.String(), fresh.Token) || strings.Contains(view.Body.String(), "token_hash") {
		t.Fatal("read API exposed source credentials")
	}
	var page store.AISecurityRuntimeEvidence
	if json.Unmarshal(view.Body.Bytes(), &page) != nil || !page.HasMore || page.NextCursor == "" || len(page.Events) != 1 {
		t.Fatal("runtime page metadata missing")
	}
	identityHTTPRequest(t, f.handler, "GET", base+"/runtime-evidence?limit=1&before="+page.NextCursor, "", f.viewerCookie, "", 200)
	identityHTTPRequest(t, f.handler, "GET", base+"/runtime-evidence?kind=rag&before="+page.NextCursor, "", f.cookie, "", 400)
	for _, query := range []string{"limit=", "limit=0", "limit=201", "limit=invalid", "limit=1&limit=2", "sourceId=0", "sourceId=oops", "kind=unknown", "outcome=success", "before=invalid"} {
		identityHTTPRequest(t, f.handler, "GET", base+"/runtime-evidence?"+query, "", f.cookie, "", 400)
	}
	identityHTTPRequest(t, f.handler, "POST", strings.TrimSuffix(path, "/rotate-token")+"/revoke", `{}`, f.cookie, "", 200)
	identityHTTPRequest(t, f.handler, "POST", path, `{"expectedTokenVersion":2}`, f.cookie, "", 409)
	identityHTTPRequest(t, f.handler, "POST", aiSecurityRuntimePath, gatewayHTTPJSON(t, r), nil, fresh.Token, 401)
}

func TestGatewayTaskPageLimitRejectsExplicitEmptyAndDuplicateValues(t *testing.T) {
	for _, query := range []string{"limit=", "limit=0", "limit=201", "limit=2&limit=3", "limit=not-a-number"} {
		r := httptest.NewRequest("GET", "/?"+query, nil)
		if _, err := gatewayTaskLimit(r); err == nil {
			t.Fatal("invalid limit accepted", query)
		}
	}
	if n, err := gatewayTaskLimit(httptest.NewRequest("GET", "/", nil)); err != nil || n != 100 {
		t.Fatal("legacy default changed")
	}
}
