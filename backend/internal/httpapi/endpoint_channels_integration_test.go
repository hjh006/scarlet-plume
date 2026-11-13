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

	"github.com/hjh006/scarlet-plume/backend/internal/endpointguard"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This is a real PostgreSQL + HTTP contract test with synthetic endpoint facts.
// Native Win32 hardware/clipboard/printer runtime validation remains separate.
func TestEndpointHTTPThreeChannelsEvidenceAndBoundaries(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Second)
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
	slug := fmt.Sprintf("endpoint-http-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Synthetic endpoint protocol"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reviewer", "unused"))
	owner, e := repo.FindUser(ctx, slug)
	must(e)
	reviewer, e := repo.FindUser(ctx, slug+"-reviewer")
	must(e)
	ws, e := repo.Membership(ctx, owner.ID, slug)
	must(e)
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(cleanup, "DELETE FROM plume.workspaces WHERE id=$1", ws.ID)
		_, _ = pool.Exec(cleanup, "DELETE FROM plume.users WHERE id=ANY($1)", []int64{owner.ID, reviewer.ID})
	})
	session := func(actor int64) string {
		s, e := sessionToken()
		must(e)
		must(repo.CreateSession(ctx, actor, sessionHash(s), time.Now().Add(time.Hour)))
		return s
	}
	ownerCookie, reviewCookie := session(owner.ID), session(reviewer.ID)
	tokenValue, e := sessionToken()
	must(e)
	token := runtimeTokenPrefix + tokenValue
	source, e := repo.CreateOperationsSource(ctx, ws.ID, owner.ID, store.OperationsSourceInput{Name: "Synthetic Windows endpoint contract", Kind: "security_events", OwnerTeam: "test"}, sessionHash(token))
	must(e)
	handler := New(repo, "https://example.invalid", true)
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
			if cookie != "" {
				r.Header.Set("Origin", "https://example.invalid")
			}
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s got%d want%d %.300s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	decode := func(w *httptest.ResponseRecorder, out any) { t.Helper(); must(json.Unmarshal(w.Body.Bytes(), out)) }
	device := strings.Repeat("a", 64)
	caps := []store.EndpointCapability{}
	for _, channel := range []string{"usb", "print", "clipboard"} {
		executor := map[string]string{"usb": "usb_copy_guard", "print": "windows_print_gdi", "clipboard": "windows_clipboard_write"}[channel]
		caps = append(caps, store.EndpointCapability{Channel: channel, Scope: "controlled_operations", Supported: true, Modes: []string{"monitor", "block_sensitive", "block_all"}, Executor: executor, Reason: "synthetic protocol fixture; not physical Windows acceptance"})
	}
	hb := store.EndpointHeartbeatInput{DeviceRef: device, OS: "windows", AgentVersion: "synthetic-endpoint-v1", ObservedAt: time.Now().UTC(), Capabilities: caps}
	call("POST", endpointHeartbeatPath, "", token, hb, 200)
	other := hb
	other.DeviceRef = strings.Repeat("b", 64)
	call("POST", endpointHeartbeatPath, "", token, other, 409)
	base := "/api/v1/workspaces/" + slug + "/endpoint"
	var frozenExports []int64
	for index, channel := range []string{"usb", "print", "clipboard"} {
		in := store.EndpointPolicyInput{SourceID: source.ID, DeviceRef: device, Channel: channel, Scope: "controlled_operations", Mode: "block_sensitive", Categories: []string{"email"}, Reason: "Synthetic channel acceptance <script>unsafe</script>", DurationMinutes: 60}
		var p store.EndpointPolicy
		decode(call("POST", base+"/policies", ownerCookie, "", in, 201), &p)
		path := fmt.Sprintf("%s/policies/%d", base, p.ID)
		call("POST", path+"/approve", ownerCookie, "", map[string]any{"revision": p.Revision}, 403)
		decode(call("POST", path+"/approve", reviewCookie, "", map[string]any{"revision": p.Revision}, 200), &p)
		var bundle store.EndpointPolicyBundle
		decode(call("GET", endpointPolicyPath+"?deviceRef="+device, "", token, nil, 200), &bundle)
		if len(bundle.Policies) != index+1 {
			t.Fatal("approved policy missing")
		}
		receipt := store.EndpointReceiptInput{ReceiptRef: fmt.Sprintf("%064x", index+1), DeviceRef: device, PolicyID: p.ID, PolicyRevision: p.Revision, PolicyDigest: p.Digest, Channel: channel, Scope: p.Scope, Executor: p.Executor, Status: "applied", Code: "policy_loaded", ObservedAt: time.Now().UTC()}
		call("POST", endpointReceiptsPath, "", token, receipt, 201)
		call("POST", endpointReceiptsPath, "", token, receipt, 200)
		wrong := receipt
		wrong.ReceiptRef = strings.Repeat("f", 64)
		wrong.PolicyDigest = strings.Repeat("f", 64)
		call("POST", endpointReceiptsPath, "", token, wrong, 409)
		operationRef := fmt.Sprintf("%064x", index+101)
		nativeCalls := 0
		audit := func(fact datasafety.Context) error {
			event := store.SecurityEventInput{DataContext: &fact, EventID: fmt.Sprintf("endpoint-http-%d-%s", p.ID, fact.Endpoint.Stage), ObservedAt: time.Now().UTC(), Outcome: "observed", Category: "data_leak", Rule: "endpoint:blocked", Severity: "high", Count: 1, WindowSeconds: 1}
			call("POST", securityEventsPath, "", token, event, 200)
			return nil
		}
		fact, err := endpointguard.Run(endpointguard.Policy{ID: p.ID, Revision: p.Revision, DeviceRef: device, Channel: channel, Scope: p.Scope, Mode: p.Mode, Digest: p.Digest, Categories: p.Categories, ExpiresAt: p.ExpiresAt}, endpointguard.Identity{DeviceRef: device, OperationRef: operationRef}, "json", []byte(`{"value":"private\u0040example.test"}`), audit, func([]byte) error { nativeCalls++; return nil })
		if err != endpointguard.ErrBlocked || nativeCalls != 0 || fact.Endpoint.Stage != "preflight" {
			t.Fatal("sensitive action reached native boundary")
		}
		// A source cannot turn a known protected hit into an allowed receipt.
		forged := fact
		evidence := *fact.Endpoint
		forged.Endpoint = &evidence
		forged.Enforcement = "allowed"
		forged.Endpoint.Stage = "completed"
		call("POST", securityEventsPath, "", token, store.SecurityEventInput{DataContext: &forged, EventID: fmt.Sprintf("forged-%d", p.ID), ObservedAt: time.Now().UTC(), Outcome: "activity", Category: "data_leak", Rule: "endpoint:activity", Severity: "low", Count: 1, WindowSeconds: 1}, 400)
		var exported store.EndpointEvidenceExport
		decode(call("POST", path+"/exports", ownerCookie, "", map[string]any{}, 201), &exported)
		if len(exported.Snapshot.Events) != 1 || len(exported.Snapshot.Receipts) != 1 || exported.SnapshotDigest == "" {
			t.Fatal("frozen proof missing source events or receipts")
		}
		frozenExports = append(frozenExports, exported.ID)
		html := call("GET", fmt.Sprintf("%s/exports/%d/export?format=html", base, exported.ID), ownerCookie, "", nil, 200).Body.String()
		if strings.Contains(html, "<script>unsafe</script>") || strings.Contains(html, "private@example.test") || strings.Contains(html, token) {
			t.Fatal("unsafe export content")
		}
	}
	// Frozen exports stay available after source revocation, but no new source
	// heartbeat/policy fetch can restore this revoked token.
	must(repo.RevokeOperationsSource(ctx, ws.ID, owner.ID, source.ID))
	call("GET", endpointPolicyPath+"?deviceRef="+device, "", token, nil, 401)
	for _, id := range frozenExports {
		call("GET", fmt.Sprintf("%s/exports/%d/export?format=json", base, id), ownerCookie, "", nil, 200)
	}
}
