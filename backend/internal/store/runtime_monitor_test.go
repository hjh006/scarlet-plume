package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestRuntimeMonitorAndAudit(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("could not configure PostgreSQL test database")
	}
	t.Cleanup(pool.Close)
	repo := store.New(pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
	must(repo.Migrate(ctx))

	slug := fmt.Sprintf("runtime-monitor-%d", time.Now().UnixNano())
	otherSlug, ownerName := slug+"-other", slug+"-owner"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM plume.workspaces WHERE slug = ANY($1)", []string{slug, otherSlug}); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM plume.users WHERE username = $1", ownerName); err != nil {
			t.Error(err)
		}
	})
	must(repo.Bootstrap(ctx, ownerName, "unused", slug, "Runtime monitor"))
	must(repo.Bootstrap(ctx, ownerName, "unused", otherSlug, "Other runtime monitor"))
	owner, err := repo.FindUser(ctx, ownerName)
	must(err)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	otherWorkspace, err := repo.Membership(ctx, owner.ID, otherSlug)
	must(err)

	const service = "Orders"
	gatewayToken, otlpToken, otherToken := digest(slug+"-gateway"), digest(slug+"-otlp"), digest(slug+"-other")
	gateway, err := repo.CreateRuntimeSource(ctx, workspace.ID, owner.ID, "Ingress", service, "gateway", gatewayToken)
	must(err)
	_, err = repo.CreateRuntimeSource(ctx, workspace.ID, owner.ID, "OpenTelemetry", service, "otlp", otlpToken)
	must(err)
	other, err := repo.CreateRuntimeSource(ctx, otherWorkspace.ID, owner.ID, "Ingress", service, "gateway", otherToken)
	must(err)
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	deliver := func(kind, token, name string, events ...runtimesecurity.Observation) store.RuntimeReceipt {
		t.Helper()
		receipt, err := repo.DeliverRuntimeBatch(ctx, kind, token, digest(name), events)
		must(err)
		if receipt.Accepted != len(events) {
			t.Fatalf("unexpected receipt: %+v", receipt)
		}
		return receipt
	}
	gatewayEvents := []runtimesecurity.Observation{
		{Method: "GET", Route: "/orders/{id}", Status: 200, ObservedAt: now},
		{Method: "GET", Route: "/orders/{customer}", Status: 404, ObservedAt: now.Add(-10 * time.Minute)},
		{Method: "POST", Route: "/reports", Status: 503, ObservedAt: now},
	}
	deliver("gateway", gatewayToken, "original ingress", gatewayEvents...)
	if replay := deliver("gateway", gatewayToken, "original ingress", gatewayEvents[2], gatewayEvents[0], gatewayEvents[1]); !replay.Replayed {
		t.Fatal("expected idempotent replay")
	}
	deliver("otlp", otlpToken, "runtime", runtimesecurity.Observation{
		Method: "GET", Route: "/legacy", Status: 410, ObservedAt: now, DatabaseSystems: []string{"postgresql"},
	})
	deliver("gateway", otherToken, "other workspace", runtimesecurity.Observation{
		Method: "POST", Route: "/workspace-private", Status: 500, ObservedAt: now,
	})

	monitor, err := repo.RuntimeMonitor(ctx, workspace.ID, 1, 0)
	must(err)
	if monitor.Totals != (store.RuntimeMonitorCounts{Requests: 4, Responses4xx: 2, Responses5xx: 1}) ||
		len(monitor.Points) != 12 || monitor.EndAt.Sub(monitor.StartAt) != time.Hour ||
		len(monitor.TopRoutes) != 3 || monitor.TopRoutes[0].Requests != 2 || monitor.TopRoutesPartial {
		t.Fatalf("unexpected workspace monitor: %+v", monitor)
	}
	var counted int64
	for _, point := range monitor.Points {
		counted += point.Requests
	}
	if counted != 4 {
		t.Fatalf("trend included %d requests, want 4", counted)
	}
	filteredMonitor, err := repo.RuntimeMonitor(ctx, workspace.ID, 6, gateway.ID)
	must(err)
	if filteredMonitor.Totals != (store.RuntimeMonitorCounts{Requests: 3, Responses4xx: 1, Responses5xx: 1}) ||
		len(filteredMonitor.Points) != 24 {
		t.Fatalf("unexpected source monitor: %+v", filteredMonitor)
	}
	foreignMonitor, err := repo.RuntimeMonitor(ctx, workspace.ID, 24, other.ID)
	must(err)
	if foreignMonitor.Totals.Requests != 0 || len(foreignMonitor.TopRoutes) != 0 {
		t.Fatalf("foreign source leaked into monitor: %+v", foreignMonitor)
	}

	filter := store.RuntimeAuditFilter{Hours: 24, SourceID: gateway.ID, Method: "GET", Status: 404, Route: "/orders/{orderId}"}
	audit, err := repo.RuntimeAudit(ctx, workspace.ID, filter)
	must(err)
	if len(audit.Items) != 1 || audit.Truncated || audit.Items[0].Route != "/orders/{customer}" ||
		audit.Items[0].SourceID != gateway.ID || audit.Items[0].Status != 404 || audit.Items[0].Service != service {
		t.Fatalf("unexpected filtered audit: %+v", audit)
	}
	audit, err = repo.RuntimeAudit(ctx, workspace.ID, store.RuntimeAuditFilter{Hours: 24})
	must(err)
	data, err := json.Marshal(audit)
	must(err)
	if len(audit.Items) != 4 || strings.Contains(string(data), "workspace-private") ||
		strings.Contains(string(data), "postgresql") || strings.Contains(string(data), gatewayToken) {
		t.Fatal("audit was duplicated or included metadata outside its projection")
	}
	audit, err = repo.RuntimeAudit(ctx, workspace.ID, store.RuntimeAuditFilter{Hours: 24, SourceID: other.ID})
	must(err)
	if audit.Items == nil || len(audit.Items) != 0 {
		t.Fatalf("foreign source leaked into audit: %+v", audit)
	}

	// Seed retained history at the row cap, plus one old row still waiting for ingest cleanup.
	_, err = pool.Exec(ctx, `
		INSERT INTO plume.runtime_request_audit (source_id, observed_at, method, route, route_key, status)
		SELECT $1, CASE WHEN n = 1 THEN $2::timestamptz - interval '8 days'
		                ELSE $2::timestamptz - interval '20 minutes' END,
		       'GET', '/fixture', '/fixture', 200
		FROM generate_series(1, $3) AS n
	`, gateway.ID, now, store.RuntimeAuditPerSourceLimit-3)
	must(err)
	_, err = pool.Exec(ctx, "UPDATE plume.runtime_security_sources SET audit_count = $2 WHERE id = $1",
		gateway.ID, store.RuntimeAuditPerSourceLimit)
	must(err)
	_, err = pool.Exec(ctx, `
		INSERT INTO plume.runtime_monitor_minutes (source_id, minute_at, requests, responses_4xx, responses_5xx)
		VALUES ($1, date_trunc('minute', $2::timestamptz - interval '20 minutes', 'UTC'), $3, 0, 0)
	`, gateway.ID, now, store.RuntimeAuditPerSourceLimit-4)
	must(err)
	latest := runtimesecurity.Observation{Method: "GET", Route: "/fresh", Status: 200, ObservedAt: now}
	deliver("gateway", gatewayToken, "retention cleanup", latest, latest)
	var retained, expired int
	must(pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE observed_at < clock_timestamp() - interval '7 days')
		FROM plume.runtime_request_audit WHERE source_id = $1
	`, gateway.ID).Scan(&retained, &expired))
	if retained != store.RuntimeAuditPerSourceLimit || expired != 0 {
		t.Fatalf("unexpected retention result: retained=%d expired=%d", retained, expired)
	}
	audit, err = repo.RuntimeAudit(ctx, workspace.ID, store.RuntimeAuditFilter{Hours: 168, SourceID: gateway.ID})
	must(err)
	if len(audit.Items) != store.RuntimeAuditLimit || !audit.Truncated {
		t.Fatalf("expected truncated audit at %d rows", store.RuntimeAuditLimit)
	}
	monitor, err = repo.RuntimeMonitor(ctx, workspace.ID, 1, gateway.ID)
	must(err)
	if monitor.Totals.Requests != int64(store.RuntimeAuditPerSourceLimit+1) || !monitor.TopRoutesPartial {
		t.Fatal("trimming request audit changed the independent monitoring counters")
	}
}
