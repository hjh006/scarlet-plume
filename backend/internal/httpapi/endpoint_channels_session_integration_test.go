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

func TestEndpointSessionHTTPPartialResponseAndStrictEvidence(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := t.Context()
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
	slug := fmt.Sprintf("session-http-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Synthetic interactive clipboard"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reviewer", "unused"))
	owner, err := repo.FindUser(ctx, slug)
	must(err)
	reviewer, err := repo.FindUser(ctx, slug+"-reviewer")
	must(err)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=$1`, ws.ID)
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE id=ANY($1)`, []int64{owner.ID, reviewer.ID})
	})
	session := func(id int64) string {
		value, err := sessionToken()
		must(err)
		must(repo.CreateSession(ctx, id, sessionHash(value), time.Now().Add(time.Hour)))
		return value
	}
	ownerCookie, reviewCookie := session(owner.ID), session(reviewer.ID)
	tokenValue, err := sessionToken()
	must(err)
	token := runtimeTokenPrefix + tokenValue
	source, err := repo.CreateOperationsSource(ctx, ws.ID, owner.ID, store.OperationsSourceInput{Name: "Synthetic user source", Kind: "security_events", OwnerTeam: "test"}, sessionHash(token))
	must(err)
	handler := New(repo, "https://console.invalid", true)
	call := func(method, path, cookie, bearer, origin string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		must(err)
		request := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		request.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			request.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: cookie})
		}
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, request)
		if out.Code != want {
			t.Fatalf("%s %s got%d want%d %.160s", method, path, out.Code, want, out.Body.String())
		}
		return out
	}
	now := func() time.Time {
		var at time.Time
		must(pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at))
		return at.UTC()
	}
	device, login, config := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	h := store.EndpointHeartbeatInput{DeviceRef: device, OS: "windows", AgentVersion: "session-http-fixture", ObservedAt: now(), Capabilities: []store.EndpointCapability{{Channel: "clipboard", Scope: "session_response", Supported: true, Modes: []string{"monitor", "clear_sensitive", "clear_all"}, Executor: "windows_clipboard_session_guard", Reason: "synthetic protocol fixture, not Windows validation", TargetScope: "interactive_session_clipboard", TargetRefs: []string{login}, ConfigDigest: config, Privilege: "interactive_user"}}}
	call("POST", endpointHeartbeatPath, "", token, "", h, 200)
	base := "/api/v1/workspaces/" + slug + "/endpoint"
	input := store.EndpointPolicyInput{SourceID: source.ID, DeviceRef: device, Channel: "clipboard", Scope: "session_response", Mode: "clear_sensitive", Categories: []string{"phone"}, Reason: "Synthetic after-change response <script>unsafe</script>", DurationMinutes: 60, TargetScope: "interactive_session_clipboard", TargetRefs: []string{login}, ConfigDigest: config}
	call("POST", base+"/policies", ownerCookie, "", "https://other.invalid", input, 403)
	var p store.EndpointPolicy
	must(json.Unmarshal(call("POST", base+"/policies", ownerCookie, "", "https://console.invalid", input, 201).Body.Bytes(), &p))
	path := fmt.Sprintf("%s/policies/%d", base, p.ID)
	call("POST", path+"/approve", ownerCookie, "", "https://console.invalid", map[string]int64{"revision": p.Revision}, 403)
	must(json.Unmarshal(call("POST", path+"/approve", reviewCookie, "", "https://console.invalid", map[string]int64{"revision": p.Revision}, 200).Body.Bytes(), &p))
	loaded := store.EndpointReceiptInput{ReceiptRef: strings.Repeat("d", 64), DeviceRef: device, PolicyID: p.ID, PolicyRevision: p.Revision, PolicyDigest: p.Digest, Channel: "clipboard", Scope: p.Scope, Executor: p.Executor, Status: "loaded", Code: "session_guard_loaded", ObservedAt: now(), TargetScope: p.TargetScope, TargetRefs: p.TargetRefs, ConfigDigest: p.ConfigDigest}
	call("POST", endpointReceiptsPath, "", token, "", loaded, 201)
	at := now()
	intent := store.EndpointSessionEventInput{EventRef: strings.Repeat("e", 64), OperationRef: strings.Repeat("f", 64), DeviceRef: device, SessionRef: login, PolicyID: p.ID, PolicyRevision: p.Revision, PolicyDigest: p.Digest, Scope: p.Scope, Executor: p.Executor, ConfigDigest: config, Status: "unknown", Code: "session_clear_intent", ObservedAt: at, ResponseAt: at, Capture: "partial", Categories: []string{"phone"}, SequenceRef: strings.Repeat("1", 64), ContentRef: strings.Repeat("2", 64)}
	call("POST", endpointSessionEventsPath, "", "", "", intent, 401)
	call("POST", endpointSessionEventsPath, "", token, "", map[string]any{"rawPID": 1, "text": "private clipboard body"}, 400)
	call("POST", endpointSessionEventsPath, "", token, "", intent, 201)
	call("POST", endpointSessionEventsPath, "", token, "", intent, 200)
	result := intent
	result.EventRef = strings.Repeat("3", 64)
	result.Status = "cleared_after_change"
	result.Code = "session_cleared_after_change"
	result.ResponseAt = now()
	result.CurrentSequenceRef = result.SequenceRef
	result.ClearAttempted = true
	result.SequenceMatched = true
	result.ClipboardEmpty = true
	call("POST", endpointSessionEventsPath, "", token, "", result, 201)
	var detail store.EndpointPolicyDetail
	must(json.Unmarshal(call("GET", path, reviewCookie, "", "", nil, 200).Body.Bytes(), &detail))
	if detail.SessionEventsTotal != 2 || len(detail.SessionEvents) != 2 || detail.DeploymentState != "listening" || !detail.SessionEvents[0].CurrentResponse || detail.SessionEvents[0].Capture != "partial" {
		t.Fatal("missing partial positive response evidence", detail)
	}
	var frozen store.EndpointEvidenceExport
	must(json.Unmarshal(call("POST", path+"/exports", ownerCookie, "", "https://console.invalid", map[string]any{}, 201).Body.Bytes(), &frozen))
	if frozen.Snapshot.Version != 3 || frozen.Snapshot.SessionEventsTotal != 2 || len(frozen.Snapshot.SessionCoverageMissing) == 0 {
		t.Fatal("export omitted response scope/gaps")
	}
	html := call("GET", fmt.Sprintf("%s/exports/%d/export?format=html", base, frozen.ID), ownerCookie, "", "", nil, 200).Body.String()
	if strings.Contains(html, "<script>unsafe</script>") || strings.Contains(html, "private clipboard body") || strings.Contains(html, token) || !strings.Contains(html, "cleared_after_change") {
		t.Fatal("export leaked raw data or omitted source fact")
	}
	call("POST", path+"/revoke", ownerCookie, "", "https://console.invalid", map[string]any{"revision": p.Revision, "reason": "Stop session response"}, 200)
	loaded.ReceiptRef = strings.Repeat("4", 64)
	loaded.Status = "stopped"
	loaded.Code = "session_guard_stopped"
	loaded.ObservedAt = now()
	call("POST", endpointReceiptsPath, "", token, "", loaded, 201)
	must(json.Unmarshal(call("GET", path, reviewCookie, "", "", nil, 200).Body.Bytes(), &detail))
	if detail.Current || detail.DeploymentState != "stopped" {
		t.Fatal("stop acknowledged as restored content or remained active")
	}
}
