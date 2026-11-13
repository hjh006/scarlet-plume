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

// Full native command/receipt/rollback protocol against real HTTP/PostgreSQL.
// The driver is an explicit memory fixture: this never calls Windows APIs.
func TestNativeBrokerHTTPPreparedReadbackAndRevocation(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable native_http PostgreSQL")
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
	slug := "native-agent-" + time.Now().Format("150405.000000000")
	slug = strings.ReplaceAll(slug, ".", "-")
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Synthetic native service protocol"))
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
	tokenHash := sha256.Sum256([]byte(token))
	source, err := repo.CreateOperationsSource(ctx, ws.ID, owner.ID, store.OperationsSourceInput{Name: "Synthetic native protocol; Windows not exercised", Kind: "security_events", OwnerTeam: "isolated"}, hex.EncodeToString(tokenHash[:]))
	must(err)
	server := httptest.NewServer(httpapi.New(repo, "https://example.invalid", false))
	defer server.Close()
	e, driver, example, _, _ := nativeFixture(t)
	client := e.client
	client.origin = server.URL
	client.http = server.Client()
	client.token = token
	cap := store.EndpointCapability{Channel: "print", Scope: "native_control", Supported: true, Modes: []string{"block_all"}, Executor: example.Policy.Executor, Reason: "synthetic protocol fixture, not Windows runtime acceptance", TargetScope: example.Policy.TargetScope, TargetRefs: example.Policy.TargetRefs, ConfigDigest: example.Policy.ConfigDigest, Privilege: "system"}
	hb := store.EndpointHeartbeatInput{DeviceRef: client.device, OS: "windows", AgentVersion: "synthetic-native-fixture-v2", ObservedAt: time.Now().UTC(), Capabilities: []store.EndpointCapability{cap}}
	must(client.request(ctx, "POST", "/api/v1/endpoint/heartbeat", hb, nil))
	p, err := repo.CreateEndpointPolicy(ctx, ws.ID, owner.ID, store.EndpointPolicyInput{SourceID: source.ID, DeviceRef: client.device, Channel: "print", Scope: "native_control", Mode: "block_all", Categories: []string{}, Reason: "synthetic native lease acceptance, no physical printing", DurationMinutes: 30, TargetScope: cap.TargetScope, TargetRefs: cap.TargetRefs, ConfigDigest: cap.ConfigDigest})
	must(err)
	p, err = repo.DecideEndpointPolicy(ctx, ws.ID, reviewer.ID, p.ID, p.Revision, "approve", "")
	must(err)
	b, err := client.nativeBundle(ctx)
	must(err)
	if len(b.NativeCommands) != 1 || b.NativeCommands[0].Action != "apply" {
		t.Fatal("approved native command not returned")
	}
	var durable nativeJournal
	e.save = func(j nativeJournal) error { raw, _ := json.Marshal(j); return json.Unmarshal(raw, &durable) }
	e.report = func(ctx context.Context, r store.EndpointReceiptInput) error {
		return client.request(ctx, "POST", "/api/v1/endpoint/receipts", r, nil)
	}
	must(e.Handle(ctx, b.NativeCommands[0]))
	if driver.applied != 1 {
		t.Fatal("native fixture mutation count incorrect")
	}
	detail, err := repo.EndpointPolicyDetail(ctx, ws.ID, owner.ID, p.ID)
	must(err)
	if detail.NativeLease == nil || detail.NativeLease.State == "functional_verified" || len(detail.Receipts) != 3 {
		t.Fatal("preparation/configuration/readback did not remain separate")
	}
	exported, err := repo.CreateEndpointEvidenceExport(ctx, ws.ID, owner.ID, p.ID)
	must(err)
	encoded, _ := json.Marshal(exported)
	if strings.Contains(string(encoded), token) || strings.Contains(string(encoded), "PrinterName") {
		t.Fatal("private native/source material in export")
	}
	// Restart from the sealed applied journal: refresh from authenticated
	// commands before the expiry worker, then never reapply the native action.
	restarted, _, _, _, _ := nativeFixture(t)
	restarted.client = client
	restarted.driver = driver
	restarted.save = e.save
	restarted.report = e.report
	must(restarted.load(durable))
	restarted.refreshDeadlines(b.NativeCommands)
	must(restarted.Handle(ctx, b.NativeCommands[0]))
	if driver.applied != 1 {
		t.Fatal("restart repeated native mutation")
	}
	_, err = repo.DecideEndpointPolicy(ctx, ws.ID, owner.ID, p.ID, p.Revision, "revoke", "synthetic revoke after readback")
	must(err)
	b, err = client.nativeBundle(ctx)
	must(err)
	if len(b.NativeCommands) != 1 || b.NativeCommands[0].Action != "rollback" {
		t.Fatal("revoked policy did not return stable rollback command")
	}
	must(restarted.Handle(ctx, b.NativeCommands[0]))
	if driver.restored != 1 || driver.current[cap.TargetRefs[0]].State.Paused {
		t.Fatal("native source did not restore recorded original")
	}
	detail, err = repo.EndpointPolicyDetail(ctx, ws.ID, owner.ID, p.ID)
	must(err)
	if detail.NativeLease.State != "rolled_back" {
		t.Fatalf("rollback receipt not retained: %s", detail.NativeLease.State)
	}
	old, err := repo.EndpointEvidenceExport(ctx, ws.ID, owner.ID, exported.ID)
	must(err)
	if old.SnapshotDigest != exported.SnapshotDigest || old.Snapshot.Policy.NativeLease.State == "rolled_back" {
		t.Fatal("frozen readback export rewritten after rollback")
	}
}
