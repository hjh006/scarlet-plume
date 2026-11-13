package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/httpapi"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Real PostgreSQL/HTTP and a memory clear driver; never a Windows API test.
func TestClipboardSessionHTTPPartialClearExportRevokeAndReplay(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable clipboard PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
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
	slug := "clipboard-http-" + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "-")
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Synthetic clipboard protocol"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reviewer", "unused"))
	owner, err := repo.FindUser(ctx, slug)
	must(err)
	reviewer, err := repo.FindUser(ctx, slug+"-reviewer")
	must(err)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(cleanup, "DELETE FROM plume.workspaces WHERE id=$1", ws.ID)
		_, _ = pool.Exec(cleanup, "DELETE FROM plume.users WHERE id=ANY($1)", []int64{owner.ID, reviewer.ID})
	})
	random := make([]byte, 32)
	_, err = rand.Read(random)
	must(err)
	token := "spr_" + base64.RawURLEncoding.EncodeToString(random)
	hash := sha256.Sum256([]byte(token))
	source, err := repo.CreateOperationsSource(ctx, ws.ID, owner.ID, store.OperationsSourceInput{Name: "Synthetic session fixture; no Windows runtime", Kind: "security_events", OwnerTeam: "isolated"}, hex.EncodeToString(hash[:]))
	must(err)
	server := httptest.NewServer(httpapi.New(repo, "https://example.invalid", false))
	defer server.Close()
	r, calls, _ := clipboardResponderFixture(t)
	client := r.c
	client.origin = server.URL
	client.http = server.Client()
	client.token = token
	cap := store.EndpointCapability{Channel: "clipboard", Scope: "session_response", Supported: true, Modes: []string{"monitor", "clear_sensitive", "clear_all"}, Executor: clipboardSessionExecutor, Reason: "synthetic protocol fixture, not application compatibility", TargetScope: "interactive_session_clipboard", TargetRefs: []string{r.binding.sessionRef}, ConfigDigest: r.binding.configDigest, Privilege: "interactive_user"}
	hb := store.EndpointHeartbeatInput{DeviceRef: client.device, OS: "windows", AgentVersion: "synthetic-session-v3", ObservedAt: time.Now().UTC(), Capabilities: []store.EndpointCapability{cap}}
	must(client.request(ctx, "POST", "/api/v1/endpoint/heartbeat", hb, nil))
	p, err := repo.CreateEndpointPolicy(ctx, ws.ID, owner.ID, store.EndpointPolicyInput{SourceID: source.ID, DeviceRef: client.device, Channel: "clipboard", Scope: "session_response", Mode: "clear_sensitive", Categories: []string{"email"}, Reason: "synthetic partial Office text response; no real clipboard", DurationMinutes: 30, TargetScope: cap.TargetScope, TargetRefs: cap.TargetRefs, ConfigDigest: cap.ConfigDigest})
	must(err)
	p, err = repo.DecideEndpointPolicy(ctx, ws.ID, reviewer.ID, p.ID, p.Revision, "approve", "")
	must(err)
	adopted, err := fetchClipboardPolicy(ctx, client, r.binding)
	must(err)
	if adopted == nil || adopted.ID != p.ID {
		t.Fatal("approved session policy missing")
	}
	r.cache = &clipboardPolicyCache{}
	must(r.cache.listenerReady(client, r.queue))
	must(r.cache.replace(client, adopted, r.queue))
	must(r.event(ctx, syntheticClipboardChange()))
	if *calls != 1 {
		t.Fatal("synthetic response not attempted once")
	}
	must(r.queue.replay(ctx))
	must(r.queue.replay(ctx))
	if *calls != 1 {
		t.Fatal("replay repeated clear")
	}
	detail, err := repo.EndpointPolicyDetail(ctx, ws.ID, owner.ID, p.ID)
	must(err)
	if len(detail.SessionEvents) != 2 || len(detail.Receipts) != 1 {
		t.Fatalf("phase counts: %d events, %d receipts", len(detail.SessionEvents), len(detail.Receipts))
	}
	var cleared bool
	for _, e := range detail.SessionEvents {
		if e.Status == "cleared_after_change" {
			cleared = true
			if e.Capture != "partial" || !e.ClipboardEmpty || !e.SequenceMatched {
				t.Fatal("partial same-instance response proof lost")
			}
		}
	}
	if !cleared {
		t.Fatal("no conditional-clear source fact")
	}
	frozen, err := repo.CreateEndpointEvidenceExport(ctx, ws.ID, owner.ID, p.ID)
	must(err)
	raw, _ := json.Marshal(frozen)
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "synthetic@example.test") {
		t.Fatal("private content in export")
	}
	_, err = repo.DecideEndpointPolicy(ctx, ws.ID, owner.ID, p.ID, p.Revision, "revoke", "synthetic stop future response")
	must(err)
	next, err := fetchClipboardPolicy(ctx, client, r.binding)
	must(err)
	if next != nil {
		t.Fatal("revoked response policy readopted")
	}
	must(r.cache.replace(client, nil, r.queue))
	must(r.queue.replay(ctx))
	must(r.event(ctx, syntheticClipboardChange()))
	if *calls != 1 {
		t.Fatal("revoke allowed another clear")
	}
	old, err := repo.EndpointEvidenceExport(ctx, ws.ID, owner.ID, frozen.ID)
	must(err)
	if old.SnapshotDigest != frozen.SnapshotDigest || len(old.Snapshot.SessionEvents) != 2 || old.Snapshot.Policy.Status != "approved" {
		t.Fatal("frozen pre-revoke evidence was rewritten")
	}
}
