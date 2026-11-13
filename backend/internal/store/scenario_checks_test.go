package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
)

func TestScenarioTechnicalChecksSnapshotFreshnessAndControlledProposal(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	digest := func(v string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(v))) }
	cfg := ManagedPostgresInput{Host: "technical-source", Port: 5432, Database: "fixture", User: "reader", Schemas: []string{"inventory"}, IntervalMinutes: 5, Enabled: true, AutoChecks: true, EncryptedPassword: []byte("fixture-ciphertext")}
	job, e := f.repo.CreateManagedPostgresJob(ctx, f.ws.ID, f.user.ID, "technical-pg", digest(f.ws.Slug), cfg)
	must(e)
	table := collector.PostgreSQLTable{Asset: collector.Asset{SourceRef: "inventory.customers", Name: "customers"}, Schema: "inventory", Owner: "data_owner", MetadataStatus: "complete", Columns: []collector.PostgreSQLColumn{{Name: "email", Type: "text", Sensitivity: "email", CanSelect: true}}, Constraints: []collector.PostgreSQLConstraint{}, Grants: []collector.PostgreSQLGrant{{Grantee: "data_owner", Privilege: "SELECT", SchemaUsage: true}, {Grantee: "PUBLIC", Privilege: "SELECT", Public: true, SchemaUsage: true}}, PublicSchemaUsage: true, PublicSelect: true, ContentAnalysis: &collector.PostgreSQLContentAnalysis{Status: "complete", Method: "bounded_prefix_source_aggregate", RowsScanned: 2, Columns: []collector.PostgreSQLColumnStatistics{{Column: "email", Status: "complete", NonNullRows: 2, Matches: []collector.PostgreSQLPatternCount{{Category: "email", Count: 2}}}}}}
	counter := 0
	collect := func() {
		t.Helper()
		counter++
		must(f.repo.QueueManagedPostgresJob(ctx, f.ws.ID, f.user.ID, job.ID))
		claim, e := f.repo.ClaimManagedPostgresJob(ctx, fmt.Sprintf("technical-%d-%d", f.ws.ID, counter), time.Minute)
		must(e)
		if claim == nil {
			t.Fatal("no source claim")
		}
		_, e = f.repo.FinishManagedPostgresJob(ctx, *claim, AutomationDelivery{Digest: digest(fmt.Sprint(counter)), ObservedAt: time.Now(), Assets: []CollectedAsset{{SourceRef: table.SourceRef, Name: table.Name}}, PostgresTables: []collector.PostgreSQLTable{table}})
		must(e)
	}
	collect()
	assets, e := f.repo.ListAssets(ctx, f.ws.ID, 0, 10)
	must(e)
	if len(assets) != 1 {
		t.Fatal("fixture asset missing")
	}
	asset := assets[0]
	operationsSource, e := f.repo.CreateOperationsSource(ctx, f.ws.ID, f.user.ID, OperationsSourceInput{Name: "PG controlled executor", Kind: "postgres_control", OwnerTeam: "Data", JobID: job.ID}, digest(f.ws.Slug+"-control"))
	must(e)
	request := ScenarioCheckRunInput{Scenario: "personal", Task: "inventory", AssetID: asset.ID}
	run, e := f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, request)
	must(e)
	finding := func(run ScenarioCheckRun, id string) ScenarioCheckFinding {
		t.Helper()
		for _, f := range run.Findings {
			if f.ID == id {
				return f
			}
		}
		t.Fatalf("missing finding %s", id)
		return ScenarioCheckFinding{}
	}
	acl := finding(run, "pg_public_select")
	if acl.Outcome != "non_compliant" || !acl.Remediation.Executable || acl.Remediation.Target == nil || run.Counts.NonCompliant != 1 || len(run.Evidence) < 2 || len(run.ReportRequirements) < 2 || run.ReportRequirements[0].Status != "available" || run.ReportRequirements[1].Status != "partial" {
		t.Fatalf("real ACL observation not evaluated %+v", run)
	}
	if run.Mode != "stored_observations" || run.RetestState != "not_retested" || run.SnapshotDigest == "" {
		t.Fatal("run provenance missing")
	}
	frozen, _ := json.Marshal(run)
	request.PreviousRunID = &run.ID
	same, e := f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, request)
	must(e)
	if same.RetestState != "same_source_observation" {
		t.Fatal("same saved source was called a new source retest")
	}
	// Different scenes keep independently filtered histories and report mappings.
	domestic := request
	domestic.Scenario = "domestic"
	domestic.Task = "important-data"
	domestic.PreviousRunID = nil
	d, e := f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, domestic)
	must(e)
	if d.ReportRequirements[1].Key != "important_data_risk_report" || d.ReportRequirements[1].Applicability != "conditional" {
		t.Fatal("regulatory report applicability was guessed")
	}
	list, e := f.repo.ListScenarioCheckRuns(ctx, f.ws.ID, f.user.ID, ScenarioCheckRunFilter{Scenario: "personal", Task: "inventory", AssetID: asset.ID})
	must(e)
	if len(list) != 2 || list[0].Findings != nil {
		t.Fatal("history filter or metadata list contract broken")
	}
	// A same-prefix request for another role must never be recovered as the
	// frozen PUBLIC target, even when it uses the same asset and executor.
	unrelated, e := f.repo.CreateControlAction(ctx, f.ws.ID, f.user.ID, ControlActionInput{Kind: "revoke_select", OperationsSourceID: operationsSource.ID, AssetID: asset.ID, Role: "data_owner", Reason: fmt.Sprintf("场景检查#%d/pg_public_select：manually chosen different role", run.ID), DurationMinutes: 15})
	must(e)
	// A finding only creates one existing controlled action request, even if
	// two identical client requests arrive concurrently.
	poolConfig := f.pool.Config()
	poolConfig.MaxConns = 2
	poolConfig.MinConns = 0
	smallPool, e := pgxpool.NewWithConfig(ctx, poolConfig)
	must(e)
	t.Cleanup(smallPool.Close)
	proposalRepo := New(smallPool)
	var wg sync.WaitGroup
	wg.Add(2)
	ids := make([]int64, 2)
	errs := make([]error, 2)
	for i := range 2 {
		go func(i int) {
			defer wg.Done()
			a, e := proposalRepo.ProposeScenarioCheckRemediation(ctx, f.ws.ID, f.user.ID, run.ID, "pg_public_select", "Remove unnecessary PUBLIC grant after owner confirmation", 15)
			ids[i] = a.ID
			errs[i] = e
		}(i)
	}
	wg.Wait()
	must(errs[0])
	must(errs[1])
	if ids[0] < 1 || ids[0] != ids[1] || ids[0] == unrelated.ID {
		t.Fatal("duplicate remediation proposal created multiple actions")
	}
	action, e := f.repo.ControlAction(ctx, f.ws.ID, f.user.ID, ids[0])
	must(e)
	if action.Status != "proposed" || action.CheckID == nil || action.Spec.Role != "PUBLIC" {
		t.Fatal("proposal skipped existing approval workflow")
	}
	linked, e := f.repo.GetScenarioCheckRun(ctx, f.ws.ID, f.user.ID, run.ID)
	must(e)
	if len(linked.Actions) != 1 || linked.SnapshotDigest != run.SnapshotDigest {
		t.Fatal("action link rewrote frozen result")
	}
	if ScenarioCheckRunSnapshotDigest(linked) != run.SnapshotDigest {
		t.Fatal("immutable run digest cannot be reproduced after database round trip")
	}
	linked.Actions = nil
	after, _ := json.Marshal(linked)
	if string(after) != string(frozen) {
		t.Fatal("run snapshot changed after proposal")
	}
	var receipts int
	must(f.pool.QueryRow(ctx, `SELECT count(*) FROM plume.control_actions WHERE workspace_id=$1 AND status<>'proposed'`, f.ws.ID).Scan(&receipts))
	if receipts != 0 {
		t.Fatal("scanning/proposal executed a destructive action")
	}
	unlinkedOld, e := f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, ScenarioCheckRunInput{Scenario: "personal", Task: "inventory", AssetID: asset.ID})
	must(e)
	// Full fresh metadata with no PUBLIC direct grant passes only this narrow ACL
	// baseline; complete access controls/encryption/backups remain unknown.
	table.PublicSelect = false
	table.Grants = table.Grants[:1]
	collect()
	retest, e := f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, request)
	must(e)
	if retest.RetestState != "new_source_observation" || finding(retest, "pg_public_select").Outcome != "compliant" || finding(retest, "protection_coverage").Outcome != "needs_verification" {
		t.Fatalf("new source observation was not distinguished %+v", retest)
	}
	compliantACL := finding(retest, "pg_public_select").Remediation
	if !strings.Contains(compliantACL.Advice, "维持") || !strings.Contains(compliantACL.Reason, "无需发起修复") || compliantACL.Executable || compliantACL.Target != nil {
		t.Fatalf("compliant ACL retained missing-evidence repair guidance: %+v", compliantACL)
	}
	// A current target alone is insufficient after its configuration revision has
	// changed without a corresponding collector snapshot.
	_, e = f.pool.Exec(ctx, `UPDATE plume.managed_postgres_jobs SET revision=revision+1 WHERE job_id=$1`, job.ID)
	must(e)
	stale, e := f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, request)
	must(e)
	if finding(stale, "pg_public_select").Outcome != "needs_verification" {
		t.Fatal("config revision drift still passed ACL baseline")
	}
	// Even if the next collector run sees an identical PUBLIC target, a new
	// source configuration revision cannot be acted on through the old finding.
	table.PublicSelect = true
	table.Grants = append(table.Grants, collector.PostgreSQLGrant{Grantee: "PUBLIC", Privilege: "SELECT", Public: true, SchemaUsage: true})
	collect()
	_, e = f.repo.ProposeScenarioCheckRemediation(ctx, f.ws.ID, f.user.ID, unlinkedOld.ID, "pg_public_select", "Old finding must not target new config", 15)
	if !errors.Is(e, ErrConflict) {
		t.Fatalf("old snapshot targeted a new source revision: %v", e)
	}
	_, e = f.pool.Exec(ctx, `UPDATE plume.managed_postgres_jobs SET revision=revision-1 WHERE job_id=$1`, job.ID)
	must(e)
	table.PublicSelect = false
	table.Grants = table.Grants[:1]
	collect()
	_, e = f.pool.Exec(ctx, `UPDATE plume.postgres_asset_inspections SET source_revision=NULL WHERE asset_id=$1`, asset.ID)
	must(e)
	legacy, e := f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, request)
	must(e)
	if finding(legacy, "pg_public_select").Outcome != "needs_verification" {
		t.Fatal("legacy inspection without revision proof passed")
	}
	_, e = f.pool.Exec(ctx, `UPDATE plume.postgres_asset_inspections SET source_revision=1 WHERE asset_id=$1`, asset.ID)
	must(e)
	table.MetadataStatus = "partial"
	collect()
	partial, e := f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, request)
	must(e)
	if finding(partial, "pg_public_select").Outcome != "needs_verification" {
		t.Fatal("partial negative observation passed")
	}
	_, e = f.pool.Exec(ctx, `UPDATE plume.automation_runs SET observed_at=now()-interval '2 days' WHERE job_id=$1`, job.ID)
	must(e)
	expired, e := f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, request)
	must(e)
	if finding(expired, "pg_public_select").Outcome != "needs_verification" {
		t.Fatal("expired observation passed")
	}
	// The original evidence report remains available after source/asset removal.
	_, e = f.pool.Exec(ctx, `DELETE FROM plume.checks WHERE workspace_id=$1 AND asset_id=$2`, f.ws.ID, asset.ID)
	must(e)
	_, e = f.pool.Exec(ctx, `DELETE FROM plume.assets WHERE workspace_id=$1 AND id=$2`, f.ws.ID, asset.ID)
	must(e)
	historical, e := f.repo.GetScenarioCheckRun(ctx, f.ws.ID, f.user.ID, run.ID)
	must(e)
	if historical.AssetName != "customers" || len(historical.Evidence) < 2 {
		t.Fatal("historical evidence was deleted with the live asset")
	}
}

func TestScenarioTechnicalChecksRolesAndMissingSources(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	asset, e := f.repo.CreateAsset(ctx, f.ws.ID, "Uncollected asset", "CRM", "Data")
	must(e)
	run, e := f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, ScenarioCheckRunInput{Scenario: "crossborder", Task: "procedures", AssetID: asset.ID})
	must(e)
	if run.Counts.Compliant != 0 || run.Counts.NonCompliant != 0 || run.Counts.NeedsVerification != 3 || run.ReportRequirements[1].Status != "missing" {
		t.Fatal("missing source became a completed compliance finding")
	}
	comprehensive, e := f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, ScenarioCheckRunInput{Scenario: "crossborder", AssetID: asset.ID})
	must(e)
	if comprehensive.Task != "" {
		t.Fatal("comprehensive check invented a task scope")
	}
	_, e = f.repo.ListScenarioCheckRuns(ctx, f.ws.ID, f.user.ID, ScenarioCheckRunFilter{Scenario: "crossborder", AssetID: asset.ID + 1000000})
	if !errors.Is(e, ErrNotFound) {
		t.Fatal("history accepted foreign/missing asset scope")
	}
	_, e = f.repo.GetScenarioCheckRun(ctx, f.ws.ID, f.user.ID+100000, run.ID)
	if !errors.Is(e, ErrNotFound) {
		t.Fatal("non-member read snapshot")
	}
	_, e = f.repo.ProposeScenarioCheckRemediation(ctx, f.ws.ID, f.user.ID, run.ID, "pg_public_select", "No target", 15)
	if !errors.Is(e, ErrEvidenceRequired) {
		t.Fatalf("unsupported target accepted %v", e)
	}
	reviewerName := f.user.Username + "-technical-reviewer"
	must(f.repo.ProvisionReviewer(ctx, f.ws.Slug, reviewerName, "unused"))
	reviewer, e := f.repo.FindUser(ctx, reviewerName)
	must(e)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = f.pool.Exec(cleanup, `DELETE FROM plume.workspaces WHERE id=$1`, f.ws.ID)
		_, _ = f.pool.Exec(cleanup, `DELETE FROM plume.users WHERE id=$1`, reviewer.ID)
	})
	_, e = f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, reviewer.ID, ScenarioCheckRunInput{Scenario: "personal", Task: "pia", AssetID: asset.ID})
	if !errors.Is(e, ErrForbidden) {
		t.Fatal("read-only reviewer created run")
	}
	_, e = f.repo.GetScenarioCheckRun(ctx, f.ws.ID, reviewer.ID, run.ID)
	must(e)
	_, e = f.repo.ProposeScenarioCheckRemediation(ctx, f.ws.ID, reviewer.ID, run.ID, "pg_public_select", "Cannot propose", 15)
	if !errors.Is(e, ErrForbidden) {
		t.Fatal("reviewer created controlled action")
	}
}
