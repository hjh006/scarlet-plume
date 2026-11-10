package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/apisecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestRuntimeRetestAndIndependentReview(t *testing.T) {
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
	want(repo.Migrate(ctx), nil)

	slug := fmt.Sprintf("runtime-retest-%d", time.Now().UnixNano())
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
	want(repo.Bootstrap(ctx, ownerName, "unused", slug, "Runtime retest"), nil)
	want(repo.ProvisionReviewer(ctx, slug, reviewerName, "unused"), nil)
	owner, err := repo.FindUser(ctx, ownerName)
	want(err, nil)
	reviewer, err := repo.FindUser(ctx, reviewerName)
	want(err, nil)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	want(err, nil)
	stamp := func() time.Time {
		t.Helper()
		var value time.Time
		want(pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&value), nil)
		return value
	}
	digest := func(label string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(label))) }
	const service = "Customer API"
	publish := func(revision string, registerShadow bool) store.APISecurityScan {
		t.Helper()
		operations := []apisecurity.Operation{
			{Method: "GET", Path: "/old/{id}", Auth: "required", Deprecated: true},
		}
		if registerShadow {
			operations = append(operations, apisecurity.Operation{Method: "GET", Path: "/shadow/{objectId}", Auth: "required"})
		}
		scan, err := repo.ImportAPISecurityScan(ctx, workspace.ID, owner.ID, service, digest(revision),
			apisecurity.Scan{Version: "3.1.0", Operations: operations})
		want(err, nil)
		return scan
	}
	firstScan := publish("opening", false)
	gateway, other := digest(slug+"-gateway"), digest(slug+"-other")
	gatewaySource, err := repo.CreateRuntimeSource(ctx, workspace.ID, owner.ID, "Opening observer", service, "gateway", gateway)
	want(err, nil)
	_, err = repo.CreateRuntimeSource(ctx, workspace.ID, owner.ID, "Other observer", service, "gateway", other)
	want(err, nil)
	deliver := func(token, label string, observedAt time.Time, routes []string, statuses ...int) {
		t.Helper()
		observations := make([]runtimesecurity.Observation, len(statuses))
		for i, status := range statuses {
			observations[i] = runtimesecurity.Observation{Method: "GET", Route: routes[i], Status: status, ObservedAt: observedAt}
		}
		_, err := repo.DeliverRuntimeBatch(ctx, "gateway", token, digest(label), observations)
		want(err, nil)
	}
	old := []string{"/old/{customer}", "/old/{replacement}"}
	shadow := []string{"/shadow/{id}"}
	deliver(gateway, "opening traffic", stamp(), []string{old[0], shadow[0]}, 200, 200)
	findings, err := repo.ListRuntimeFindings(ctx, workspace.ID)
	want(err, nil)
	if len(findings) != 2 {
		t.Fatalf("expected two runtime findings: %+v", findings)
	}
	checkIDs := make(map[string]int64)
	for _, finding := range findings {
		checkIDs[finding.Rule] = finding.CheckID
	}

	detail := func(checkID, actorID int64) store.CheckDetail {
		t.Helper()
		result, err := repo.CheckDetail(ctx, workspace.ID, actorID, checkID)
		want(err, nil)
		if result.RuntimeFinding == nil || result.RuntimeFinding.SourceID != gatewaySource.ID ||
			result.RuntimeFinding.BaselineScanID != firstScan.ID {
			t.Fatalf("missing frozen source binding: %+v", result.RuntimeFinding)
		}
		return result
	}
	review := func(checkID int64, decision string, expected error) {
		t.Helper()
		current := detail(checkID, reviewer.ID)
		if !current.CanReview || current.ReviewRevision == 0 {
			t.Fatal("independent reviewer cannot review")
		}
		want(repo.ReviewCheck(ctx, workspace.ID, reviewer.ID, checkID, current.ReviewRevision, decision, "Fixture decision"), expected)
	}
	remediate := func(checkID int64) {
		t.Helper()
		want(repo.AddEvidence(ctx, workspace.ID, owner.ID, checkID, store.EvidenceInput{
			Kind: "remediation", SourceSystem: "fixture", SourceRecord: "release-1",
			Summary: "Corrected route policy", ObservedAt: stamp(),
		}), nil)
	}
	expectRetests := func(checkID int64, count int) store.CheckDetail {
		t.Helper()
		result := detail(checkID, owner.ID)
		found := 0
		for _, evidence := range result.Evidence {
			if evidence.Kind == "retest" {
				found++
			}
		}
		if found != count || (count == 0) != (result.RuntimeFinding.RetestAt == nil) {
			t.Fatalf("got %d retests, want %d; finding %+v", found, count, result.RuntimeFinding)
		}
		return result
	}
	for _, checkID := range checkIDs {
		want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, checkID), nil)
		review(checkID, "verified", store.ErrEvidenceRequired)
		review(checkID, "changes", nil)
		want(repo.AddEvidence(ctx, workspace.ID, owner.ID, checkID, store.EvidenceInput{
			Kind: "retest", SourceSystem: "fixture", SourceRecord: "manual", Summary: "Manual substitute", ObservedAt: stamp(),
		}), store.ErrEvidenceRequired)
		remediate(checkID)
		want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, checkID), store.ErrEvidenceRequired)
	}

	deprecatedID := checkIDs["deprecated_success"]
	deliver(gateway, "404 is not a retest", stamp(), old, 404)
	deliver(other, "different source", stamp(), old, 410)
	deliver(gateway, "mixed status", stamp(), old, 410, 204)
	expectRetests(deprecatedID, 0)
	deliver(gateway, "retired", stamp(), old, 410)
	deliver(gateway, "repeat retired", stamp(), old, 410)
	accepted := expectRetests(deprecatedID, 1)
	if !accepted.CanSubmit || !strings.Contains(accepted.Evidence[0].Summary, "HTTP 410") ||
		!strings.Contains(accepted.Evidence[0].SourceRecord, digest("retired")) {
		t.Fatalf("missing automatic retest provenance: %+v", accepted)
	}
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, deprecatedID), nil)
	if !detail(deprecatedID, reviewer.ID).CanVerify {
		t.Fatal("fresh same-source 410 did not qualify for review")
	}
	deliver(other, "later positive", stamp(), old, 200)
	if detail(deprecatedID, reviewer.ID).CanVerify {
		t.Fatal("new positive observation did not invalidate the prior retest")
	}
	review(deprecatedID, "verified", store.ErrEvidenceRequired)
	review(deprecatedID, "changes", nil)
	remediate(deprecatedID)
	deliver(gateway, "new cycle retired", stamp(), old, 410)
	expectRetests(deprecatedID, 2)
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, deprecatedID), nil)
	review(deprecatedID, "verified", nil)

	unregisteredID := checkIDs["unregistered_success"]
	preRegistration := stamp()
	deliver(gateway, "still unregistered", preRegistration, shadow, 200)
	secondScan := publish("approved contract", true)
	deliver(gateway, "late pre-registration observation", preRegistration, shadow, 200)
	deliver(other, "registered wrong source", stamp(), shadow, 200)
	expectRetests(unregisteredID, 0)
	deliver(gateway, "registered live", stamp(), shadow, 200)
	registered := expectRetests(unregisteredID, 1)
	if !registered.CanSubmit || !strings.Contains(registered.Evidence[0].SourceRecord, fmt.Sprintf("OpenAPI 扫描 #%d", secondScan.ID)) {
		t.Fatalf("missing approved contract provenance: %+v", registered)
	}
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, unregisteredID), nil)
	if !detail(unregisteredID, reviewer.ID).CanVerify {
		t.Fatal("registered route was not eligible for independent review")
	}
	publish("contract rollback", false)
	if detail(unregisteredID, reviewer.ID).CanVerify {
		t.Fatal("baseline rollback did not invalidate prior retest")
	}
	review(unregisteredID, "verified", store.ErrEvidenceRequired)
	if frozen := expectRetests(unregisteredID, 1); frozen.Evidence[0].ID != registered.Evidence[0].ID {
		t.Fatal("invalidation changed immutable evidence")
	}
}
