package store_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReportRuntimeDetectionUsesAdoptedPolicySnapshot(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx := t.Context()
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
	slug := fmt.Sprintf("report-policy-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Policy snapshot report"))
	owner, e := repo.FindUser(ctx, slug)
	must(e)
	ws, e := repo.Membership(ctx, owner.ID, slug)
	must(e)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=$1`, ws.ID)
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE id=$1`, owner.ID)
	})
	token := fmt.Sprintf("%x", sha256.Sum256([]byte(slug)))
	source, e := repo.CreateRuntimeSource(ctx, ws.ID, owner.ID, "Sensitive response source", "Policy report API", "gateway", token)
	must(e)
	_, e = repo.DeliverRuntimeBatch(ctx, "gateway", token, fmt.Sprintf("%x", sha256.Sum256([]byte("policy-report-batch"))), []runtimesecurity.Observation{{Method: "GET", Route: "/records", Status: 200, ObservedAt: time.Now().UTC(), DataFields: []runtimesecurity.DataField{{Name: "email", Location: "response", Category: "email", Method: "format", Count: 3, Observed: 5}}, InspectedLocations: []string{"response"}}})
	must(e)
	report, e := repo.ComplianceReport(ctx, ws.ID, owner.ID, 0)
	must(e)
	var finding *store.RuntimeFinding
	for _, entry := range report.Checks {
		if entry.RuntimeFinding != nil && entry.RuntimeFinding.Rule == "sensitive_response" {
			finding = entry.RuntimeFinding
		}
	}
	if finding == nil || finding.Detection == nil || finding.Detection.SourceID != source.ID || finding.Detection.Matched != 3 || finding.Detection.Observed != 5 {
		t.Fatalf("missing adopted runtime denominator %+v", finding)
	}
	policies, e := repo.RuntimeDetectionPolicies(ctx, ws.ID, owner.ID)
	must(e)
	for _, policy := range policies {
		if policy.Rule == "sensitive_response" {
			policy.Threshold = 100
			_, e = repo.SaveRuntimeDetectionPolicy(ctx, ws.ID, owner.ID, policy)
			must(e)
		}
	}
	later, e := repo.ComplianceReport(ctx, ws.ID, owner.ID, 0)
	must(e)
	for _, entry := range later.Checks {
		if entry.RuntimeFinding != nil && entry.RuntimeFinding.ID == finding.ID {
			if entry.RuntimeFinding.Detection.Policy.Threshold != 1 || entry.RuntimeFinding.Detection.Policy.Revision != 0 {
				t.Fatal("report replaced adopted detection policy with current edited policy")
			}
			t.Log("same snapshot report retained original policy, actual window and denominator after policy edit")
			return
		}
	}
	t.Fatal("runtime finding disappeared from report")
}
