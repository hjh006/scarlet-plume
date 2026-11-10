package store_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/apisecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestRuntimeFindingsAutomaticEvidence(t *testing.T) {
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

	slug := fmt.Sprintf("runtime-findings-%d", time.Now().UnixNano())
	ownerName := slug + "-owner"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM plume.workspaces WHERE slug = $1", slug); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM plume.users WHERE username = $1", ownerName); err != nil {
			t.Error(err)
		}
	})
	must(repo.Bootstrap(ctx, ownerName, "unused", slug, "Runtime findings"))
	owner, err := repo.FindUser(ctx, ownerName)
	must(err)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	must(err)

	const service = "Customer API"
	baseline, err := repo.ImportAPISecurityScan(ctx, workspace.ID, owner.ID, service, digest("baseline"), apisecurity.Scan{
		Version: "3.1.0",
		Operations: []apisecurity.Operation{
			{Method: "GET", Path: "/old/{id}", Auth: "required", Deprecated: true},
			{Method: "GET", Path: "/old-blocked", Auth: "required", Deprecated: true},
			{Method: "GET", Path: "/registered", Auth: "required"},
		},
	})
	must(err)
	gatewayToken, otlpToken := digest(slug+"-gateway"), digest(slug+"-otlp")
	gateway, err := repo.CreateRuntimeSource(ctx, workspace.ID, owner.ID, "Ingress", service, "gateway", gatewayToken)
	must(err)
	otlp, err := repo.CreateRuntimeSource(ctx, workspace.ID, owner.ID, "OpenTelemetry", service, "otlp", otlpToken)
	must(err)
	otherToken := digest(slug + "-other")
	_, err = repo.CreateRuntimeSource(ctx, workspace.ID, owner.ID, "No baseline", "Other API", "gateway", otherToken)
	must(err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	deliver := func(kind, token, name string, events ...runtimesecurity.Observation) {
		t.Helper()
		receipt, err := repo.DeliverRuntimeBatch(ctx, kind, token, digest(name), events)
		must(err)
		if receipt.Accepted != len(events) {
			t.Fatalf("unexpected receipt: %+v", receipt)
		}
	}
	deliver("gateway", otherToken, "missing baseline", runtimesecurity.Observation{
		Method: "GET", Route: "/shadow", Status: 200, ObservedAt: now,
	})

	findings, err := repo.ListRuntimeFindings(ctx, workspace.ID)
	must(err)
	if len(findings) != 0 {
		t.Fatal("a missing OpenAPI baseline generated a finding")
	}

	ingressEvents := []runtimesecurity.Observation{
		{Method: "GET", Route: "/old/{customerId}", Status: 200, ObservedAt: now},
		{Method: "GET", Route: "/shadow", Status: 204, ObservedAt: now},
		{Method: "GET", Route: "/redirect", Status: 302, ObservedAt: now},
		{Method: "GET", Route: "/old-blocked", Status: 410, ObservedAt: now},
		{Method: "GET", Route: "/registered", Status: 200, ObservedAt: now},
	}
	otlpEvents := []runtimesecurity.Observation{
		{Method: "GET", Route: "/old/{orderId}", Status: 201, ObservedAt: now},
		{Method: "GET", Route: "/shadow", Status: 200, ObservedAt: now},
	}
	deliveries := make(chan error, 2)
	go func() {
		_, err := repo.DeliverRuntimeBatch(ctx, "gateway", gatewayToken, digest("gateway evidence"), ingressEvents)
		deliveries <- err
	}()
	go func() {
		_, err := repo.DeliverRuntimeBatch(ctx, "otlp", otlpToken, digest("otlp evidence"), otlpEvents)
		deliveries <- err
	}()
	must(<-deliveries)
	must(<-deliveries)

	findings, err = repo.ListRuntimeFindings(ctx, workspace.ID)
	must(err)
	if len(findings) != 2 {
		t.Fatalf("expected two distinct runtime findings, got %+v", findings)
	}
	byRule := make(map[string]store.RuntimeFinding)
	for _, finding := range findings {
		byRule[finding.Rule] = finding
		sources := map[int64]string{gateway.ID: gateway.Name, otlp.ID: otlp.Name}
		if finding.Source == "" || sources[finding.SourceID] != finding.Source || finding.BaselineScanID != baseline.ID ||
			finding.CheckStatus != "draft" || !finding.FirstSeen.Equal(now) || !finding.LastSeen.Equal(now) ||
			finding.PreviousCheckID != nil {
			t.Fatalf("unexpected finding provenance or status: %+v", finding)
		}
		detail, err := repo.CheckDetail(ctx, workspace.ID, owner.ID, finding.CheckID)
		must(err)
		if detail.Requirement.Code != "G03" || len(detail.Evidence) != 1 || detail.Evidence[0].Kind != "observation" {
			t.Fatalf("expected one G03 draft with initial evidence, got %+v", detail)
		}
		asset, err := repo.Asset(ctx, workspace.ID, detail.AssetID)
		must(err)
		if asset.SourceProvider != "runtime-api" || asset.Source != service {
			t.Fatalf("unexpected runtime asset: %+v", asset)
		}
	}
	if byRule["unregistered_success"].Route != "/shadow" ||
		runtimesecurity.RouteKey(byRule["deprecated_success"].Route) != "/old/{}" {
		t.Fatalf("unexpected rule assignment: %+v", findings)
	}

	newer := now.Add(time.Second)
	deliver("gateway", gatewayToken, "new positive", runtimesecurity.Observation{
		Method: "GET", Route: "/old/{replacement}", Status: 202, ObservedAt: newer,
	})
	findings, err = repo.ListRuntimeFindings(ctx, workspace.ID)
	must(err)
	if len(findings) != 2 {
		t.Fatal("an existing finding created a duplicate")
	}
	for _, finding := range findings {
		if finding.Rule == "deprecated_success" && (!finding.LastSeen.Equal(newer) ||
			finding.CheckID != byRule[finding.Rule].CheckID || finding.SourceID != byRule[finding.Rule].SourceID) {
			t.Fatalf("new positive did not preserve original finding provenance: %+v", finding)
		}
	}
	outside, err := repo.ListRuntimeFindings(ctx, 0)
	must(err)
	if outside == nil || len(outside) != 0 {
		t.Fatalf("unexpected findings outside workspace: %+v", outside)
	}

	// Seed an independently closed cycle to exercise recurrence boundaries
	// separately from the retest workflow that establishes the markers.
	closed := byRule["deprecated_success"]
	retestAt, verifiedAt := now.Add(2*time.Second), now.Add(3*time.Second)
	var retestID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO plume.check_evidence (check_id, kind, source_system, source_record, summary, observed_at, created_by)
		VALUES ($1, 'retest', 'fixture', 'fixture', 'fixture', $2, $3) RETURNING id
	`, closed.CheckID, retestAt, owner.ID).Scan(&retestID)
	must(err)
	_, err = pool.Exec(ctx, "UPDATE plume.runtime_findings SET retest_at = $2, retest_evidence_id = $3 WHERE id = $1",
		closed.ID, retestAt, retestID)
	must(err)
	_, err = pool.Exec(ctx, "UPDATE plume.checks SET status = 'verified' WHERE id = $1", closed.CheckID)
	must(err)
	_, err = pool.Exec(ctx, `
		INSERT INTO plume.check_events (check_id, action, actor_id, note, created_at)
		VALUES ($1, 'verified', $2, 'fixture decision', $3)
	`, closed.CheckID, owner.ID, verifiedAt)
	must(err)
	deliver("otlp", otlpToken, "historical traffic after retest", runtimesecurity.Observation{
		Method: "GET", Route: "/old/{alias}", Status: 200, ObservedAt: verifiedAt.Add(-time.Millisecond),
	})
	findings, err = repo.ListRuntimeFindings(ctx, workspace.ID)
	must(err)
	if len(findings) != 2 {
		t.Fatal("a delayed observation from before verification reopened the finding")
	}
	deliver("otlp", otlpToken, "fresh recurrence", runtimesecurity.Observation{
		Method: "GET", Route: "/old/{alias}", Status: 200, ObservedAt: verifiedAt.Add(time.Second),
	})
	findings, err = repo.ListRuntimeFindings(ctx, workspace.ID)
	must(err)
	if len(findings) != 3 || findings[0].CheckID == closed.CheckID || findings[0].CheckStatus != "draft" ||
		findings[0].PreviousCheckID == nil || *findings[0].PreviousCheckID != closed.CheckID {
		t.Fatalf("recurrence did not open a new linked draft: %+v", findings)
	}
	previous, err := repo.CheckDetail(ctx, workspace.ID, owner.ID, closed.CheckID)
	must(err)
	if previous.Status != "verified" || len(previous.Evidence) != 2 {
		t.Fatalf("recurrence altered the independently closed check: %+v", previous)
	}

	lock, err := pool.Acquire(ctx)
	must(err)
	defer lock.Release()
	_, err = lock.Exec(ctx, "SELECT pg_advisory_lock(hashtextextended('plume:runtime-findings', $1))", workspace.ID)
	must(err)
	deliver("gateway", gatewayToken, "locked evaluation", runtimesecurity.Observation{
		Method: "GET", Route: "/aggregation-survives", Status: 200, ObservedAt: now,
	})
	_, err = lock.Exec(ctx, "SELECT pg_advisory_unlock(hashtextextended('plume:runtime-findings', $1))", workspace.ID)
	must(err)
	var requests int
	must(pool.QueryRow(ctx, "SELECT requests FROM plume.runtime_security_routes WHERE source_id = $1 AND route = '/aggregation-survives'",
		gateway.ID).Scan(&requests))
	sources, err := repo.ListRuntimeSources(ctx, workspace.ID)
	must(err)
	var warningRecorded bool
	for _, source := range sources {
		if source.ID == gateway.ID && source.FindingsWarning == "evaluation_failed" && source.FindingsWarningAt != nil {
			warningRecorded = true
		}
	}
	if requests != 1 || !warningRecorded {
		t.Fatal("failed evaluation discarded telemetry or failed to record a warning")
	}

	oversized := make([]runtimesecurity.Observation, 60)
	for i := range oversized {
		oversized[i] = runtimesecurity.Observation{
			Method: "GET", Route: fmt.Sprintf("/inventory/%c%c", 'a'+i/26, 'a'+i%26),
			Status: 200, ObservedAt: now,
		}
	}
	deliver("gateway", gatewayToken, "large finding scope", oversized...)
	must(pool.QueryRow(ctx, `
		SELECT count(*) FROM plume.runtime_security_routes WHERE source_id = $1 AND route LIKE '/inventory/%'
	`, gateway.ID).Scan(&requests))
	findings, err = repo.ListRuntimeFindings(ctx, workspace.ID)
	must(err)
	if requests != len(oversized) || len(findings) <= 3 || len(findings) >= 3+len(oversized) {
		t.Fatal("finding scope cap did not preserve all aggregates while limiting workflow writes")
	}
	sources, err = repo.ListRuntimeSources(ctx, workspace.ID)
	must(err)
	warningRecorded = false
	for _, source := range sources {
		if source.ID == gateway.ID && source.FindingsWarning == "scope_limited" && source.FindingsWarningAt != nil {
			warningRecorded = true
		}
	}
	if !warningRecorded {
		t.Fatal("scope cap was not recorded as a source warning")
	}
}
