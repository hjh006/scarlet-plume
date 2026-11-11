package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSecurityOperationsHTTPBoundariesAndSourceCredentials(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
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
	slug := fmt.Sprintf("operations-http-%d", time.Now().UnixNano())
	names := []string{slug + "-owner", slug + "-reviewer", slug + "-viewer", slug + "-foreign"}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := pool.Exec(cleanup, `DELETE FROM plume.workspaces WHERE slug=ANY($1)`, []string{slug, slug + "-foreign"})
		if err != nil {
			t.Error(err)
		}
		_, err = pool.Exec(cleanup, `DELETE FROM plume.users WHERE username=ANY($1)`, names)
		if err != nil {
			t.Error(err)
		}
	})
	must(repo.Bootstrap(ctx, names[0], "unused", slug, "Operations HTTP"))
	must(repo.ProvisionReviewer(ctx, slug, names[1], "unused"))
	must(repo.ProvisionReviewer(ctx, slug, names[2], "unused"))
	must(repo.Bootstrap(ctx, names[3], "unused", slug+"-foreign", "Foreign operations HTTP"))
	users := []store.User{}
	sessions := []string{}
	for _, name := range names {
		user, err := repo.FindUser(ctx, name)
		must(err)
		users = append(users, user)
		token, err := sessionToken()
		must(err)
		must(repo.CreateSession(ctx, user.ID, sessionHash(token), time.Now().Add(time.Hour)))
		sessions = append(sessions, token)
	}
	ws, err := repo.Membership(ctx, users[0].ID, slug)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE plume.memberships SET role='viewer' WHERE workspace_id=$1 AND user_id=$2`, ws.ID, users[2].ID)
	must(err)
	handler := New(repo, "https://example.invalid", true)
	base := "/api/v1/workspaces/" + slug + "/operations"
	call := func(method, path, cookie, bearer, body, origin string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: cookie})
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if method != "GET" {
			r.Header.Set("Content-Type", "application/json")
			if origin != "" {
				r.Header.Set("Origin", origin)
			}
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s =%d want%d: %.300s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	owner, reviewer, viewer, foreign := sessions[0], sessions[1], sessions[2], sessions[3]
	call("GET", base+"/overview", "", "", "", "", 401)
	call("GET", base+"/sources", foreign, "", "", "", 404)
	call("GET", base+"/controls", viewer, "", "", "", 200)
	body := `{"name":"Authorized SIEM","kind":"security_events","ownerTeam":"Security"}`
	call("POST", base+"/sources", owner, "", body, "https://untrusted.invalid", 403)
	call("POST", base+"/sources", viewer, "", body, "https://example.invalid", 403)
	call("POST", base+"/sources", owner, "", `{"name":"hidden","token":"private"}`, "https://example.invalid", 400)
	var issued struct {
		Source store.OperationsSource `json:"source"`
		Token  string                 `json:"token"`
	}
	must(json.Unmarshal(call("POST", base+"/sources", owner, "", body, "https://example.invalid", 201).Body.Bytes(), &issued))
	if runtimeDigest("Bearer "+issued.Token) == "" || issued.Source.ID == 0 {
		t.Fatal("source token not generated once")
	}
	listed := call("GET", base+"/sources", viewer, "", "", "", 200)
	if bytes.Contains(listed.Body.Bytes(), []byte(issued.Token)) || bytes.Contains(listed.Body.Bytes(), []byte(sessionHash(issued.Token))) {
		t.Fatal("list leaked source credential or digest")
	}
	call("POST", securityEventsPath, "", "", "{", "", 401)
	call("POST", securityEventsPath, "", runtimeTokenPrefix+strings.Repeat("A", 43), "{", "", 401)
	call("POST", securityEventsPath, "", issued.Token, `{"raw":"private-raw-request"}`, "", 400)
	call("POST", securityEventsPath, "", issued.Token, `{"eventId":"`+strings.Repeat("x", 17000)+`"}`, "", 413)
	input := store.SecurityEventInput{Outcome: "observed", EventID: "event-1", ObservedAt: time.Now().UTC(), Category: "config", Rule: "fixture.configuration", Severity: "high", Count: 1, WindowSeconds: 60}
	raw, _ := json.Marshal(input)
	var received struct {
		Event    store.OperationsSecurityEvent `json:"event"`
		Replayed bool                          `json:"replayed"`
	}
	must(json.Unmarshal(call("POST", securityEventsPath, "", issued.Token, string(raw), "", 200).Body.Bytes(), &received))
	if received.Event.CheckID == 0 || received.Event.AssetID == 0 {
		t.Fatal("source intake not linked to check")
	}
	must(json.Unmarshal(call("POST", securityEventsPath, "", issued.Token, string(raw), "", 200).Body.Bytes(), &received))
	if !received.Replayed || received.Event.AssetID == 0 {
		t.Fatal("replay lost source-bound projection")
	}
	input.Count = 2
	raw, _ = json.Marshal(input)
	call("POST", securityEventsPath, "", issued.Token, string(raw), "", 409)
	call("POST", postgresControlClaimPath, "", issued.Token, `{}`, "", 401)
	call("GET", runtimeControlsPath, "", issued.Token, "", "", 401)
	random, err := sessionToken()
	must(err)
	gatewayToken := runtimeTokenPrefix + random
	gateway, err := repo.CreateRuntimeSource(ctx, ws.ID, users[0].ID, "Authorized gateway", "Gateway app", "gateway", sessionHash(gatewayToken))
	must(err)
	_, err = repo.DeliverRuntimeBatch(ctx, "gateway", sessionHash(gatewayToken), sessionHash("ops-http-observation"), []runtimesecurity.Observation{{Method: "GET", Route: "/contacts/{id}", Status: 200, ObservedAt: time.Now().UTC()}})
	must(err)
	call("POST", securityEventsPath, "", gatewayToken, string(raw), "", 401)
	call("GET", runtimeControlsPath, "", gatewayToken, "", "", 200)
	proposal := store.ControlActionInput{Kind: "block_api", RuntimeSourceID: gateway.ID, Method: "GET", Route: "/contacts/{contact_id}", Reason: "Limited approved block", DurationMinutes: 5}
	raw, _ = json.Marshal(proposal)
	var action store.ControlAction
	must(json.Unmarshal(call("POST", base+"/controls", owner, "", string(raw), "https://example.invalid", 201).Body.Bytes(), &action))
	path := fmt.Sprintf("%s/controls/%d", base, action.ID)
	approval := fmt.Sprintf(`{"revision":%d}`, action.Revision)
	call("GET", path, foreign, "", "", "", 404)
	call("POST", path+"/approve", owner, "", approval, "https://example.invalid", 403)
	call("POST", path+"/approve", viewer, "", approval, "https://example.invalid", 403)
	call("POST", path+"/approve", reviewer, "", `{"revision":1,"approvedBy":"forged"}`, "https://example.invalid", 400)
	must(json.Unmarshal(call("POST", path+"/approve", reviewer, "", approval, "https://example.invalid", 200).Body.Bytes(), &action))
	var commands struct {
		Items []store.ControlCommand `json:"items"`
	}
	must(json.Unmarshal(call("GET", runtimeControlsPath, "", gatewayToken, "", "", 200).Body.Bytes(), &commands))
	if len(commands.Items) != 1 || commands.Items[0].ID != action.ID {
		t.Fatal("approved gateway command missing")
	}
	receipt := store.ControlReceipt{ActionID: action.ID, Revision: action.Revision, Result: store.ControlResult{Phase: "apply", Success: true, Code: "route_blocked", ObservedAt: time.Now().UTC()}}
	badReceipt := receipt
	badReceipt.Result.Code = "free-form SQL or raw request is not a result code"
	badRaw, _ := json.Marshal(badReceipt)
	call("POST", runtimeControlReceiptsPath, "", gatewayToken, string(badRaw), "", 400)
	call("POST", runtimeControlReceiptsPath, "", gatewayToken, `{"raw":"private-execution-output"}`, "", 400)
	raw, _ = json.Marshal(receipt)
	call("POST", runtimeControlReceiptsPath, "", gatewayToken, string(raw), "", 200)
	call("POST", runtimeControlReceiptsPath, "", gatewayToken, string(raw), "", 200)
	receipt.Result.ObservedAt = receipt.Result.ObservedAt.Add(time.Microsecond)
	raw, _ = json.Marshal(receipt)
	call("POST", runtimeControlReceiptsPath, "", gatewayToken, string(raw), "", 409)
	call("DELETE", fmt.Sprintf("%s/sources/%d", base, issued.Source.ID), owner, "", "", "https://example.invalid", 204)
	call("POST", securityEventsPath, "", issued.Token, string(raw), "", 401)
	call("GET", base+"/overview", viewer, "", "", "", 200)
	call("GET", base+"/events", viewer, "", "", "", 200)
}

func TestOperationsExternalConcurrencyLimitBeforePayload(t *testing.T) {
	s := &server{runtimeSlots: make(chan struct{}, 1)}
	s.runtimeSlots <- struct{}{}
	called := false
	handler := s.operationsExternal("security_events", func(http.ResponseWriter, *http.Request, string) { called = true })
	r := httptest.NewRequest(http.MethodPost, securityEventsPath, strings.NewReader(strings.Repeat("x", operationsBodyLimit+1)))
	r.Header.Set("Authorization", "Bearer "+runtimeTokenPrefix+strings.Repeat("A", 43))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if called || w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal("receiver did not bound unauthenticated parsing pressure")
	}
}
