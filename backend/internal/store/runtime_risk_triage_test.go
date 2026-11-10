package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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

func TestRuntimeRiskTriageAndActionAudit(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("could not configure test PostgreSQL database")
	}
	config.ConnConfig.RuntimeParams["DateStyle"] = "SQL, DMY"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("could not configure test PostgreSQL database")
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
	slug := fmt.Sprintf("runtime-risk-%d", time.Now().UnixNano())
	otherSlug := slug + "-other"
	ownerName, operatorName, reviewerName := slug+"-owner", slug+"-operator", slug+"-reviewer"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM plume.workspaces WHERE slug = ANY($1)", []string{slug, otherSlug}); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM plume.users WHERE username = ANY($1)", []string{ownerName, operatorName, reviewerName}); err != nil {
			t.Error(err)
		}
	})
	want(repo.Bootstrap(ctx, ownerName, "unused", slug, "Runtime risk"), nil)
	want(repo.Bootstrap(ctx, ownerName, "unused", otherSlug, "Other runtime risk"), nil)
	want(repo.ProvisionReviewer(ctx, slug, reviewerName, "unused"), nil)
	owner, err := repo.FindUser(ctx, ownerName)
	want(err, nil)
	reviewer, err := repo.FindUser(ctx, reviewerName)
	want(err, nil)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	want(err, nil)
	other, err := repo.Membership(ctx, owner.ID, otherSlug)
	want(err, nil)
	var operatorID int64
	want(pool.QueryRow(ctx, "INSERT INTO plume.users (username, password_hash) VALUES ($1, 'unused') RETURNING id",
		operatorName).Scan(&operatorID), nil)
	_, err = pool.Exec(ctx, "INSERT INTO plume.memberships (workspace_id, user_id, role) VALUES ($1, $2, 'operator')",
		workspace.ID, operatorID)
	want(err, nil)

	digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
	const service = "Risk fixture"
	publish := func(name string, includeObserved bool) {
		t.Helper()
		operations := []apisecurity.Operation{{Method: "GET", Path: "/registered", Auth: "required"}}
		if includeObserved {
			operations = append(operations, apisecurity.Operation{Method: "GET", Path: "/observed", Auth: "required"})
		}
		_, err := repo.ImportAPISecurityScan(ctx, workspace.ID, owner.ID, service, digest(name), apisecurity.Scan{
			Version: "3.1.0", Operations: operations,
		})
		want(err, nil)
	}
	publish("opening", false)
	token := digest(slug + "-gateway")
	_, err = repo.CreateRuntimeSource(ctx, workspace.ID, owner.ID, "Risk observer", service, "gateway", token)
	want(err, nil)
	stamp := func() time.Time {
		t.Helper()
		var now time.Time
		want(pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now), nil)
		return now
	}
	deliver := func(name string) {
		t.Helper()
		_, err := repo.DeliverRuntimeBatch(ctx, "gateway", token, digest(name), []runtimesecurity.Observation{{
			Method: "GET", Route: "/observed", Status: 200, ObservedAt: stamp(),
		}})
		want(err, nil)
	}
	deliver("opening request")
	findings, err := repo.ListRuntimeFindings(ctx, workspace.ID)
	want(err, nil)
	if len(findings) != 1 || findings[0].Risk != nil {
		t.Fatalf("expected one untriaged finding: %+v", findings)
	}
	finding := findings[0]

	const privateReason = "triage-private-reason"
	input := store.RuntimeRiskInput{Severity: "high", OwnerTeam: "Risk team fixture", DueDate: "2026-11-21", Reason: privateReason}
	_, err = repo.UpdateRuntimeRisk(ctx, workspace.ID, reviewer.ID, finding.ID, input)
	want(err, store.ErrForbidden)
	_, err = repo.UpdateRuntimeRisk(ctx, other.ID, owner.ID, finding.ID, input)
	want(err, store.ErrNotFound)
	invalid := input
	invalid.DueDate = "2026-02-30"
	_, err = repo.UpdateRuntimeRisk(ctx, workspace.ID, owner.ID, finding.ID, invalid)
	want(err, store.ErrInvalid)
	first, err := repo.UpdateRuntimeRisk(ctx, workspace.ID, owner.ID, finding.ID, input)
	want(err, nil)
	if first.Revision != 1 || first.UpdatedBy != ownerName || first.UpdatedAt.IsZero() {
		t.Fatalf("unexpected initial revision: %+v", first)
	}
	findings, err = repo.ListRuntimeFindings(ctx, workspace.ID)
	want(err, nil)
	if findings[0].Risk == nil || findings[0].Risk.DueDate != input.DueDate || findings[0].Risk.Revision != 1 {
		t.Fatalf("triage metadata lost its ISO date or revision: %+v", findings[0].Risk)
	}

	type update struct {
		result store.RuntimeRisk
		err    error
	}
	results := make(chan update, 2)
	for _, team := range []string{"API operators", "Runtime operators"} {
		go func() {
			next := store.RuntimeRiskInput{Revision: 1, Severity: "medium", OwnerTeam: team, Reason: privateReason + "-edit"}
			result, err := repo.UpdateRuntimeRisk(ctx, workspace.ID, operatorID, finding.ID, next)
			results <- update{result, err}
		}()
	}
	a, b := <-results, <-results
	if a.err != nil {
		a, b = b, a
	}
	want(a.err, nil)
	want(b.err, store.ErrConflict)
	if a.result.Revision != 2 || a.result.UpdatedBy != operatorName || a.result.Severity != "medium" {
		t.Fatalf("unexpected winning operator revision: %+v", a.result)
	}
	detail, err := repo.CheckDetail(ctx, workspace.ID, owner.ID, finding.CheckID)
	want(err, nil)
	if detail.OwnerTeam != a.result.OwnerTeam {
		t.Fatal("triage did not synchronize check ownership")
	}

	review := func(decision string) {
		t.Helper()
		check, err := repo.CheckDetail(ctx, workspace.ID, reviewer.ID, finding.CheckID)
		want(err, nil)
		if !check.CanReview || (decision == "verified" && !check.CanVerify) {
			t.Fatalf("reviewer cannot perform %s: %+v", decision, check)
		}
		want(repo.ReviewCheck(ctx, workspace.ID, reviewer.ID, finding.CheckID, check.ReviewRevision, decision, "Fixture decision"), nil)
	}
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, finding.CheckID), nil)
	review("changes")
	want(repo.AddEvidence(ctx, workspace.ID, owner.ID, finding.CheckID, store.EvidenceInput{
		Kind: "remediation", SourceSystem: "fixture", SourceRecord: "release", Summary: "Registered observed route", ObservedAt: stamp(),
	}), nil)
	publish("registered", true)
	deliver("post-remediation request")
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, finding.CheckID), nil)
	review("verified")
	input.Revision = 2
	_, err = repo.UpdateRuntimeRisk(ctx, workspace.ID, owner.ID, finding.ID, input)
	want(err, store.ErrConflict)
	findings, err = repo.ListRuntimeFindings(ctx, workspace.ID)
	want(err, nil)
	if findings[0].CheckStatus != "verified" || findings[0].Risk == nil || findings[0].Risk.Revision != 2 ||
		findings[0].Risk.UpdatedBy != operatorName || findings[0].Risk.DueDate != "" {
		t.Fatalf("verified finding changed risk metadata: %+v", findings[0])
	}

	audit, err := repo.ActionAudit(ctx, workspace.ID, 24)
	want(err, nil)
	riskActors := make(map[string]int)
	var independentVerification bool
	for _, entry := range audit.Items {
		if entry.Action == "risk.triaged" {
			if entry.TargetType != "runtime_finding" || entry.TargetID != finding.ID || entry.At.IsZero() {
				t.Fatalf("unexpected risk audit projection: %+v", entry)
			}
			riskActors[entry.Actor]++
		}
		if entry.Action == "check.verified" && entry.TargetID == finding.CheckID && entry.Actor == reviewerName {
			independentVerification = true
		}
	}
	data, err := json.Marshal(audit)
	want(err, nil)
	if len(riskActors) != 2 || riskActors[ownerName] != 1 || riskActors[operatorName] != 1 ||
		!independentVerification || strings.Contains(string(data), privateReason) || strings.Contains(string(data), token) {
		t.Fatal("audit included unauthorized or private metadata, or omitted its actual actor")
	}
	outside, err := repo.ActionAudit(ctx, other.ID, 24)
	want(err, nil)
	if outside.Items == nil || len(outside.Items) != 0 {
		t.Fatalf("action audit crossed workspace boundary: %+v", outside)
	}
}
