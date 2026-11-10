package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/apisecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestRuntimeSecurityStore(t *testing.T) {
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
	want := func(err, expected error) {
		t.Helper()
		if !errors.Is(err, expected) {
			t.Fatalf("got %v, want %v", err, expected)
		}
	}
	digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
	want(repo.Migrate(ctx), nil)

	slug := fmt.Sprintf("runtime-security-%d", time.Now().UnixNano())
	ownerName, reviewerName := slug+"-owner", slug+"-reviewer"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM plume.workspaces WHERE slug = $1", slug); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM plume.users WHERE username = ANY($1)", []string{ownerName, reviewerName}); err != nil {
			t.Error(err)
		}
	})
	want(repo.Bootstrap(ctx, ownerName, "unused", slug, "Runtime security"), nil)
	want(repo.ProvisionReviewer(ctx, slug, reviewerName, "unused"), nil)
	owner, err := repo.FindUser(ctx, ownerName)
	want(err, nil)
	reviewer, err := repo.FindUser(ctx, reviewerName)
	want(err, nil)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	want(err, nil)

	gatewayToken, otlpToken := digest(slug+"-gateway"), digest(slug+"-otlp")
	_, err = repo.CreateRuntimeSource(ctx, workspace.ID, reviewer.ID, "Ingress", "Customer API", "gateway", gatewayToken)
	want(err, store.ErrForbidden)
	gateway, err := repo.CreateRuntimeSource(ctx, workspace.ID, owner.ID, "Ingress", "Customer API", "gateway", gatewayToken)
	want(err, nil)
	otlp, err := repo.CreateRuntimeSource(ctx, workspace.ID, owner.ID, "OpenTelemetry collector", "Billing API", "otlp", otlpToken)
	want(err, nil)
	want(repo.AuthorizeRuntimeSource(ctx, "gateway", gatewayToken), nil)
	want(repo.AuthorizeRuntimeSource(ctx, "otlp", otlpToken), nil)
	want(repo.AuthorizeRuntimeSource(ctx, "otlp", gatewayToken), store.ErrAutomationUnauthorized)
	want(repo.AuthorizeRuntimeSource(ctx, "gateway", otlpToken), store.ErrAutomationUnauthorized)

	now := time.Now().UTC().Truncate(time.Microsecond)
	observations := []runtimesecurity.Observation{
		{Method: "GET", Route: "/customers/{customer_id}", Status: 200, ObservedAt: now},
		{Method: "GET", Route: "/customers/{id}", Status: 404, ObservedAt: now.Add(-time.Minute)},
		{Method: "POST", Route: "/customers", Status: 201, ObservedAt: now},
		{Method: "GET", Route: "/legacy", Status: 502, ObservedAt: now},
	}
	batch := digest("gateway batch")
	type result struct {
		receipt store.RuntimeReceipt
		err     error
	}
	deliveries := make(chan result, 2)
	for _, input := range [][]runtimesecurity.Observation{observations, slices.Clone(observations)} {
		go func() {
			receipt, err := repo.DeliverRuntimeBatch(ctx, "gateway", gatewayToken, batch, input)
			deliveries <- result{receipt, err}
		}()
	}
	first, second := <-deliveries, <-deliveries
	want(first.err, nil)
	want(second.err, nil)
	if first.receipt.Accepted != len(observations) || second.receipt.Accepted != len(observations) ||
		first.receipt.Replayed == second.receipt.Replayed {
		t.Fatalf("unexpected concurrent receipt results: %+v / %+v", first.receipt, second.receipt)
	}
	reordered := slices.Clone(observations)
	slices.Reverse(reordered)
	replayed, err := repo.DeliverRuntimeBatch(ctx, "gateway", gatewayToken, batch, reordered)
	want(err, nil)
	if !replayed.Replayed {
		t.Fatal("reordered normalized batch was not a replay")
	}
	_, err = repo.DeliverRuntimeBatch(ctx, "gateway", gatewayToken, batch, observations[:1])
	want(err, store.ErrConflict)
	for _, stale := range []time.Time{now.Add(-8 * 24 * time.Hour), now.Add(10 * time.Minute)} {
		_, err = repo.DeliverRuntimeBatch(ctx, "gateway", gatewayToken, digest("invalid time"), []runtimesecurity.Observation{
			{Method: "GET", Route: "/legacy", Status: 200, ObservedAt: stale},
		})
		want(err, store.ErrInvalid)
	}

	ignored, err := repo.DeliverRuntimeBatch(ctx, "otlp", otlpToken, digest("ignored traces"), nil)
	want(err, nil)
	if ignored.Accepted != 0 || ignored.Replayed {
		t.Fatalf("unexpected receipt for filtered traces: %+v", ignored)
	}
	receipt, err := repo.DeliverRuntimeBatch(ctx, "otlp", otlpToken, digest("otlp batch"), []runtimesecurity.Observation{
		{Method: "GET", Route: "/payments", Status: 503, ObservedAt: now, DatabaseSystems: []string{"postgresql"}},
	})
	want(err, nil)
	if receipt.Accepted != 1 || receipt.Replayed {
		t.Fatalf("unexpected OTLP receipt: %+v", receipt)
	}
	sources, err := repo.ListRuntimeSources(ctx, workspace.ID)
	want(err, nil)
	raw, err := json.Marshal(sources)
	want(err, nil)
	if len(sources) != 2 || sources[0].LastSeen == nil || sources[1].LastSeen == nil ||
		strings.Contains(string(raw), gatewayToken) || strings.Contains(string(raw), otlpToken) {
		t.Fatalf("unexpected public sources: %s", raw)
	}

	scan := apisecurity.Scan{Version: "3.1.0", Operations: []apisecurity.Operation{
		{Method: "GET", Path: "/customers/{id}", Auth: "required", Deprecated: true},
		{Method: "POST", Path: "/customers", Auth: "required"},
	}}
	_, err = repo.ImportAPISecurityScan(ctx, workspace.ID, owner.ID, "Customer API", digest("deprecated"), scan)
	want(err, nil)
	routes, err := repo.RuntimePosture(ctx, workspace.ID)
	want(err, nil)
	if len(routes) != 4 {
		t.Fatalf("expected four aggregated routes, got %+v", routes)
	}
	byPosture := make(map[string]store.RuntimeRoute)
	for _, route := range routes {
		byPosture[route.Posture] = route
	}
	deprecated := byPosture["deprecated_active"]
	undocumented := byPosture["observed_only"]
	unconfigured := byPosture["no_baseline"]
	if deprecated.SourceID != gateway.ID || deprecated.Requests != 2 || deprecated.Responses4xx != 1 ||
		deprecated.Responses5xx != 0 || !deprecated.LastSeen.Equal(now) ||
		byPosture["documented"].Route != "/customers" || undocumented.Route != "/legacy" || undocumented.Responses5xx != 1 ||
		unconfigured.SourceID != otlp.ID || unconfigured.Kind != "otlp" || unconfigured.Responses5xx != 1 ||
		!slices.Equal(unconfigured.DatabaseSystems, []string{"postgresql"}) {
		t.Fatalf("unexpected runtime posture: %+v", routes)
	}
	empty, err := repo.RuntimePosture(ctx, 0)
	want(err, nil)
	if empty == nil || len(empty) != 0 {
		t.Fatalf("expected empty posture outside the workspace: %#v", empty)
	}

	scan.Operations[0].Deprecated = false
	_, err = repo.ImportAPISecurityScan(ctx, workspace.ID, owner.ID, "Customer API", digest("documented"), scan)
	want(err, nil)
	updated, err := repo.RuntimePosture(ctx, workspace.ID)
	want(err, nil)
	if len(updated) != len(routes) || slices.ContainsFunc(updated, func(route store.RuntimeRoute) bool { return route.Posture == "deprecated_active" }) {
		t.Fatalf("posture did not reconcile to the latest contract: %+v", updated)
	}

	want(repo.RevokeRuntimeSource(ctx, workspace.ID, reviewer.ID, gateway.ID), store.ErrForbidden)
	want(repo.RevokeRuntimeSource(ctx, workspace.ID, owner.ID, gateway.ID), nil)
	want(repo.AuthorizeRuntimeSource(ctx, "gateway", gatewayToken), store.ErrAutomationUnauthorized)
	_, err = repo.DeliverRuntimeBatch(ctx, "gateway", gatewayToken, batch, observations)
	want(err, store.ErrAutomationUnauthorized)
	historical, err := repo.RuntimePosture(ctx, workspace.ID)
	want(err, nil)
	if len(historical) != len(routes) {
		t.Fatal("revocation removed historical runtime posture")
	}

	quotaToken := digest(slug + "-quota")
	quotaSource, err := repo.CreateRuntimeSource(ctx, workspace.ID, owner.ID, "Quota fixture", "Other API", "gateway", quotaToken)
	want(err, nil)
	for batch := range 4 {
		entries := make([]runtimesecurity.Observation, 250)
		for i := range entries {
			index := batch*250 + i
			entries[i] = runtimesecurity.Observation{
				Method: "GET", Status: 200, ObservedAt: now,
				Route: fmt.Sprintf("/resources/%c%c%c", 'a'+index/676, 'a'+index/26%26, 'a'+index%26),
			}
		}
		_, err = repo.DeliverRuntimeBatch(ctx, "gateway", quotaToken, digest(fmt.Sprintf("quota batch %d", batch)), entries)
		want(err, nil)
	}
	_, err = repo.DeliverRuntimeBatch(ctx, "gateway", quotaToken, digest("route quota overflow"), []runtimesecurity.Observation{
		{Method: "GET", Route: "/resources/aaa", Status: 200, ObservedAt: now},
		{Method: "GET", Route: "/new-resource", Status: 200, ObservedAt: now},
	})
	want(err, store.ErrRuntimeLimit)
	var routeCount, requestCount int
	want(pool.QueryRow(ctx, "SELECT count(*), sum(requests) FROM plume.runtime_security_routes WHERE source_id = $1",
		quotaSource.ID).Scan(&routeCount, &requestCount), nil)
	if routeCount != 1000 || requestCount != 1000 {
		t.Fatalf("quota failure partially persisted a batch: routes=%d requests=%d", routeCount, requestCount)
	}
	latest, err := repo.RuntimePosture(ctx, workspace.ID)
	want(err, nil)
	if len(latest) != 1000 {
		t.Fatalf("posture exceeded the workspace display bound: %d", len(latest))
	}
}
