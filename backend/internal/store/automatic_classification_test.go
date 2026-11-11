package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationstandards"
	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestAutomaticClassificationSourcePersistence(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("configure test PostgreSQL")
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
	slug := fmt.Sprintf("automatic-classification-%d", time.Now().UnixNano())
	otherSlug := slug + "-other"
	ownerName, reviewerName, otherName := slug+"-owner", slug+"-reviewer", slug+"-other-owner"
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := pool.Exec(cleanup, "DELETE FROM plume.workspaces WHERE slug=ANY($1)", []string{slug, otherSlug})
		if err != nil {
			t.Error(err)
		}
		_, err = pool.Exec(cleanup, "DELETE FROM plume.users WHERE username=ANY($1)", []string{ownerName, reviewerName, otherName})
		if err != nil {
			t.Error(err)
		}
	})
	want(repo.Bootstrap(ctx, ownerName, "unused", slug, "Automatic classification"), nil)
	want(repo.Bootstrap(ctx, otherName, "unused", otherSlug, "Other workspace"), nil)
	want(repo.ProvisionReviewer(ctx, slug, reviewerName, "unused"), nil)
	owner, err := repo.FindUser(ctx, ownerName)
	want(err, nil)
	reviewer, err := repo.FindUser(ctx, reviewerName)
	want(err, nil)
	other, err := repo.FindUser(ctx, otherName)
	want(err, nil)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	want(err, nil)
	otherWorkspace, err := repo.Membership(ctx, other.ID, otherSlug)
	want(err, nil)
	digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
	config := store.ManagedPostgresInput{Host: "sample-db", Port: 5432, Database: "classification_source", User: "reader", Schemas: []string{"inventory"}, IntervalMinutes: 5,
		AutoChecks: true, AutoReports: true, EncryptedPassword: []byte("test-ciphertext")}
	job, err := repo.CreateManagedPostgresJob(ctx, workspace.ID, owner.ID, "Classification source", digest(slug), config)
	want(err, nil)
	preexisting := func(name string) store.Asset {
		t.Helper()
		asset, err := repo.CreateAsset(ctx, workspace.ID, name, "fixture", "Data")
		want(err, nil)
		_, err = pool.Exec(ctx, "UPDATE plume.assets SET source_provider='postgresql',source=$2,source_ref=$3 WHERE id=$1", asset.ID, job.Source, "inventory."+name)
		want(err, nil)
		return asset
	}
	manualAsset := preexisting("manual")
	manualInput := store.AssetClassificationInput{Category: "business", BusinessLevel: "internal", StatutoryCategory: "general", Basis: "Reviewed business scope and impacts",
		StandardID: classificationstandards.DefaultID, LevelCode: "general", RuleID: "ordinary-grading"}
	manualBefore, err := repo.UpdateAssetClassification(ctx, workspace.ID, owner.ID, manualAsset.ID, manualInput)
	want(err, nil)
	legacyAsset := preexisting("legacy")
	_, err = pool.Exec(ctx, "UPDATE plume.assets SET category='business',business_level='internal',statutory_category='general',classification_basis='Imported legacy review' WHERE id=$1", legacyAsset.ID)
	want(err, nil)
	table := func(name, category string) collector.PostgreSQLTable {
		return collector.PostgreSQLTable{Asset: collector.Asset{SourceRef: "inventory." + name, Name: name}, Schema: "inventory", Owner: "source_owner", MetadataStatus: "complete",
			Columns: []collector.PostgreSQLColumn{{Name: "value", Type: "text", BaseType: "text", Ordinal: 1, CanSelect: true}}, Constraints: []collector.PostgreSQLConstraint{}, Warnings: []string{}, Grants: []collector.PostgreSQLGrant{}, PublicSelectColumns: []string{},
			ContentAnalysis: &collector.PostgreSQLContentAnalysis{Status: "complete", Method: "bounded_prefix_aggregate", RowLimit: 200, RowsScanned: 2, Warnings: []string{},
				Columns: []collector.PostgreSQLColumnStatistics{{Column: "value", Status: "complete", NonNullRows: 2, Matches: []collector.PostgreSQLPatternCount{{Category: category, Count: 1}}}}}}
	}
	customers := table("customers", "national_id")
	contacts := table("contacts", "email")
	denied := table("denied", "national_id")
	denied.Columns[0].CanSelect = false
	denied.ContentAnalysis = &collector.PostgreSQLContentAnalysis{Status: "unknown", Method: "bounded_prefix_aggregate", RowLimit: 200, Columns: []collector.PostgreSQLColumnStatistics{}, Warnings: []string{"columns_without_select"}}
	partial := table("partial", "national_id")
	partial.ContentAnalysis.Status = "partial"
	partial.ContentAnalysis.Warnings = []string{"columns_without_select"}
	rls := table("rls", "phone")
	rls.RowSecurity = &collector.PostgreSQLRowSecurity{Enabled: true, Active: true}
	rls.ContentAnalysis.Status = "partial"
	rls.ContentAnalysis.Warnings = []string{"row_security_filtered_scope"}
	tables := []collector.PostgreSQLTable{customers, contacts, table("manual", "national_id"), denied, partial, table("legacy", "national_id"), rls}
	clock := func() time.Time {
		t.Helper()
		var now time.Time
		want(pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now), nil)
		return now
	}
	collectAt := func(name string, scanned []collector.PostgreSQLTable, observedAt time.Time) store.AutomationRun {
		t.Helper()
		want(repo.QueueManagedPostgresJob(ctx, workspace.ID, owner.ID, job.ID), nil)
		claim, err := repo.ClaimManagedPostgresJob(ctx, slug+"-"+name, time.Minute)
		want(err, nil)
		if claim == nil || claim.JobID != job.ID {
			t.Fatal("fixture did not claim its own managed source")
		}
		assets := make([]store.CollectedAsset, 0, len(scanned))
		for _, table := range scanned {
			assets = append(assets, store.CollectedAsset{SourceRef: table.SourceRef, Name: table.Name})
		}
		if observedAt.IsZero() {
			observedAt = clock()
		}
		run, err := repo.FinishManagedPostgresJob(ctx, *claim, store.AutomationDelivery{Digest: digest(name), ObservedAt: observedAt, Assets: assets, PostgresTables: scanned})
		want(err, nil)
		if run.Status != "success" {
			t.Fatalf("collection did not succeed: %+v", run)
		}
		return run
	}
	collect := func(name string, scanned []collector.PostgreSQLTable) store.AutomationRun {
		t.Helper()
		return collectAt(name, scanned, time.Time{})
	}
	initial := collect("initial", tables)
	assets, err := repo.ListAssets(ctx, workspace.ID, 0, 100)
	want(err, nil)
	byName := map[string]int64{}
	for _, asset := range assets {
		byName[asset.Name] = asset.ID
	}
	detail := func(name string) store.AssetClassificationDetail {
		t.Helper()
		d, err := repo.GetAssetClassification(ctx, workspace.ID, reviewer.ID, byName[name])
		want(err, nil)
		return d
	}
	first := detail("customers")
	if first.Origin != "automatic" || first.Revision != 1 || first.Asset.Category != "sensitive_personal" || first.Asset.BusinessLevel != "confidential" || first.Asset.StatutoryCategory != "unknown" || len(first.History) != 1 || first.History[0].Origin != "automatic" || first.Standard == nil || first.Standard.ID != classificationstandards.DefaultID || first.Standard.RuleEvidence == "" || first.Standard.RuleID != "industry-business-classification" || first.Standard.LevelCode != "" {
		t.Fatalf("observed formats did not persist the automatic property and immutable rule snapshot: %+v", first)
	}
	if first.Automatic == nil || first.Automatic.Stale || first.Automatic.Comparison != "matched" || !first.Automatic.Evaluation.AutoApplicable || !slices.ContainsFunc(first.Automatic.Evaluation.Facts, func(f classificationstandards.Fact) bool {
		return f.Kind == "format_match" && f.Count != nil && *f.Count == 1
	}) {
		t.Fatal("automatic result omitted scoped format counts or comparison")
	}
	if d := detail("denied"); d.Revision != 0 || len(d.History) != 0 || d.Asset.Category != "unknown" || d.Automatic == nil || d.Automatic.Comparison != "insufficient" || d.Automatic.Evaluation.AutoApplicable {
		t.Fatalf("unavailable statistics auto-applied a classification: %+v", d)
	}
	for _, expected := range []struct{ name, category, level string }{{"partial", "sensitive_personal", "confidential"}, {"rls", "personal", "internal"}} {
		d := detail(expected.name)
		if d.Origin != "automatic" || d.Revision != 1 || len(d.History) != 1 || d.Asset.Category != expected.category || d.Asset.BusinessLevel != expected.level || d.Asset.StatutoryCategory != "unknown" || d.Automatic == nil || !d.Automatic.Evaluation.AutoApplicable || d.Automatic.Evaluation.LevelCode != "" || d.Automatic.Evaluation.StatutoryCategory != "" || len(d.Automatic.Evaluation.MissingConditions) == 0 {
			t.Fatalf("limited scope discarded observed positive attributes or invented a statutory grade for %s: %+v", expected.name, d)
		}
	}
	manual := detail("manual")
	if manual.Origin != "manual" || manual.Revision != manualBefore.Revision || manual.Asset.Category != "business" || len(manual.History) != 1 || manual.Automatic == nil || manual.Automatic.Comparison != "conflict" {
		t.Fatalf("automatic observation overwrote a human determination: %+v", manual)
	}
	audit, err := repo.ActionAudit(ctx, workspace.ID, 24)
	want(err, nil)
	if !slices.ContainsFunc(audit.Items, func(entry store.ActionAuditEntry) bool {
		return entry.TargetType == "asset" && entry.TargetID == first.Asset.ID && entry.Action == "classification.detected" && entry.Automated
	}) || !slices.ContainsFunc(audit.Items, func(entry store.ActionAuditEntry) bool {
		return entry.TargetType == "asset" && entry.TargetID == manual.Asset.ID && entry.Action == "classification.reviewed" && !entry.Automated
	}) {
		t.Fatal("audit did not distinguish source-driven classification from human confirmation")
	}
	overview, err := repo.OverviewCounts(ctx, workspace.ID)
	want(err, nil)
	if overview.PendingClassification < 3 || overview.ActiveSources < 1 {
		t.Fatalf("overview omitted unconfirmed automatic assets or active source: %+v", overview)
	}
	sources, err := repo.OverviewSources(ctx, workspace.ID)
	want(err, nil)
	if len(sources) == 0 || len(sources) > 6 || !slices.ContainsFunc(sources, func(source store.OverviewSource) bool {
		return source.Kind == "automation" && source.Provider == "postgresql" && source.LastRunAt != nil
	}) {
		t.Fatal("overview source preview omitted actual collection history")
	}
	legacy := detail("legacy")
	if legacy.Revision != 0 || legacy.Asset.Category != "business" || len(legacy.History) != 0 || legacy.Automatic == nil || legacy.Automatic.Comparison != "unconfirmed" {
		t.Fatal("legacy classification was silently overwritten")
	}
	_, err = repo.GetAssetClassification(ctx, workspace.ID, other.ID, first.Asset.ID)
	want(err, store.ErrNotFound)
	_, err = repo.GetAssetClassification(ctx, otherWorkspace.ID, other.ID, first.Asset.ID)
	want(err, store.ErrNotFound)
	_, err = repo.GetAssetPostgresInspection(ctx, workspace.ID, other.ID, first.Asset.ID)
	want(err, store.ErrNotFound)
	checks, err := repo.ListChecks(ctx, workspace.ID, "", 0, 100)
	want(err, nil)
	checkID := func(assetID int64) int64 {
		t.Helper()
		for _, check := range checks {
			if check.AssetID == assetID && check.Requirement.Code == "G02" {
				return check.ID
			}
		}
		t.Fatal("automatic G02 check missing")
		return 0
	}
	g02 := checkID(first.Asset.ID)
	checkDetail := func() store.CheckDetail {
		t.Helper()
		d, err := repo.CheckDetail(ctx, workspace.ID, reviewer.ID, g02)
		want(err, nil)
		return d
	}
	checkFirst := checkDetail()
	if len(checkFirst.Evidence) != 1 || checkFirst.Evidence[0].Postgres == nil || checkFirst.Evidence[0].Postgres.Classification == nil || checkFirst.Evidence[0].Postgres.Classification.Evaluation.Standard.ID != classificationstandards.DefaultID {
		t.Fatal("G02 evidence lost the classification evaluation")
	}
	if observed := checkFirst.Evidence[0].Postgres; observed.SensitiveCandidateCount != 1 || len(observed.SensitiveFields) != 1 || observed.SensitiveFields[0].Name != "value" || observed.SensitiveFields[0].Sensitivity != "national_id" {
		t.Fatal("source format match on a generic field was omitted from candidate evidence")
	}
	archiveBefore, err := repo.ReportArchive(ctx, workspace.ID, reviewer.ID, initial.ID)
	want(err, nil)
	findReport := func(report store.ComplianceReport, assetID int64) store.ReportCheck {
		t.Helper()
		for _, entry := range report.Checks {
			if entry.Check.AssetID == assetID && entry.Check.Requirement.Code == "G02" {
				return entry
			}
		}
		t.Fatal("classification missing from report")
		return store.ReportCheck{}
	}
	entry := findReport(archiveBefore, first.Asset.ID)
	if entry.Classification == nil || entry.Classification.Automatic == nil || entry.AutomaticEvaluation == nil || entry.AutomaticEvaluation.Classification == nil || entry.Classification.Standard == nil || entry.Classification.Standard.RuleEvidence == "" {
		t.Fatal("report archive omitted automatic classification or evidence requirements")
	}
	if entry.AutomaticEvaluation.SensitiveCandidateCount != 1 || archiveBefore.Identification == nil || archiveBefore.Identification.SensitiveCandidates != 6 || archiveBefore.Identification.TablesWithCandidates != 6 {
		t.Fatalf("report omitted content-detected generic fields from candidate totals: %+v", archiveBefore.Identification)
	}
	collect("unchanged", tables)
	unchanged := detail("customers")
	if unchanged.Revision != first.Revision || !reflect.DeepEqual(unchanged.History, first.History) || len(checkDetail().Evidence) != 1 {
		t.Fatal("identical polling duplicated classification history or evidence")
	}
	rlsBefore := detail("rls")
	rlsMatches := rls.ContentAnalysis.Columns[0].Matches
	rls.ContentAnalysis.Columns[0].Matches = nil
	collect("rls-limited-negative", tables)
	if after := detail("rls"); after.Revision != rlsBefore.Revision || after.Asset.Category != "personal" || after.Asset.BusinessLevel != "internal" || !reflect.DeepEqual(after.History, rlsBefore.History) || after.Automatic == nil || after.Automatic.Evaluation.AutoApplicable || after.Automatic.Evaluation.Status != "insufficient_data" {
		t.Fatal("limited negative observation automatically downgraded previously observed attributes")
	}
	rls.ContentAnalysis.Columns[0].Matches = rlsMatches
	collect("rls-positive-again", tables)
	manualInput.Revision = first.Revision
	human, err := repo.UpdateAssetClassification(ctx, workspace.ID, owner.ID, first.Asset.ID, manualInput)
	want(err, nil)
	if human.Origin != "manual" || human.Automatic == nil || !human.Automatic.Stale {
		t.Fatal("manual confirmation did not invalidate previous automatic comparison")
	}
	collect("human-conflict", tables)
	afterHuman := detail("customers")
	if afterHuman.Revision != human.Revision || afterHuman.Asset.Category != "business" || !reflect.DeepEqual(afterHuman.History, human.History) || afterHuman.Automatic == nil || afterHuman.Automatic.Stale || afterHuman.Automatic.Comparison != "conflict" || len(checkDetail().Evidence) != 2 {
		t.Fatal("new source observation overwrote manual review or failed to expose the conflict")
	}
	want(repo.SetClassificationStandardDefault(ctx, workspace.ID, owner.ID, "gbt-45574-2025"), nil)
	tables = append(tables, table("fresh", "financial_account"))
	collect("changed-standard", tables)
	assets, err = repo.ListAssets(ctx, workspace.ID, 0, 100)
	want(err, nil)
	for _, asset := range assets {
		byName[asset.Name] = asset.ID
	}
	fresh := detail("fresh")
	if fresh.Origin != "automatic" || fresh.Standard == nil || fresh.Standard.ID != "gbt-45574-2025" || fresh.Standard.RuleID != "sensitive-pi-identification" || fresh.Standard.LevelCode != "" || len(fresh.History) != 1 {
		t.Fatalf("new asset did not capture the changed standard without a fabricated grade: %+v", fresh)
	}
	if pinned := detail("contacts"); pinned.Standard == nil || pinned.Standard.ID != classificationstandards.DefaultID || pinned.Revision != 1 {
		t.Fatal("workspace default retroactively changed an adopted standard")
	}
	archiveAfter, err := repo.ReportArchive(ctx, workspace.ID, reviewer.ID, initial.ID)
	want(err, nil)
	beforeJSON, _ := json.Marshal(archiveBefore)
	afterJSON, _ := json.Marshal(archiveAfter)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatal("later classification or standard changes rewrote an archived report")
	}
	_, err = repo.ReportArchive(ctx, workspace.ID, other.ID, initial.ID)
	want(err, store.ErrNotFound)
	_, err = repo.ComplianceReport(ctx, workspace.ID, other.ID, 0)
	want(err, store.ErrNotFound)

	// A human can request review of conflicting evidence, but an independent
	// reviewer must not certify it while source facts contradict classification.
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, g02), nil)
	inReview := checkDetail()
	if inReview.CanVerify {
		t.Fatal("conflicting automatic attributes allowed independent verification")
	}
	want(repo.ReviewCheck(ctx, workspace.ID, reviewer.ID, g02, inReview.ReviewRevision, "verified", "Review source facts"), store.ErrEvidenceRequired)
	want(repo.ReviewCheck(ctx, workspace.ID, reviewer.ID, g02, inReview.ReviewRevision, "changes", "Reconcile classification and collect a new source retest"), nil)
	fixedInput := manualInput
	fixedInput.Revision = afterHuman.Revision
	fixedInput.Category, fixedInput.BusinessLevel = "sensitive_personal", "confidential"
	fixedInput.Basis = "Reviewed observed sensitive formats and adopted confidential handling"
	beforeCorrection := clock()
	fixed, err := repo.UpdateAssetClassification(ctx, workspace.ID, owner.ID, first.Asset.ID, fixedInput)
	want(err, nil)
	if fixed.Automatic == nil || !fixed.Automatic.Stale {
		t.Fatal("manual correction reused an older comparison as current evidence")
	}
	want(repo.AddEvidence(ctx, workspace.ID, owner.ID, g02, store.EvidenceInput{Kind: "remediation", SourceSystem: "Classification review", SourceRecord: "review-fix", Summary: "Corrected category and internal handling", ObservedAt: clock()}), nil)
	want(repo.AddEvidence(ctx, workspace.ID, owner.ID, g02, store.EvidenceInput{Kind: "retest", SourceSystem: "Manual note", SourceRecord: "manual-retest", Summary: "Manual retest cannot replace the bound collector", ObservedAt: clock()}), nil)
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, g02), store.ErrEvidenceRequired)

	// The binding API already requires a new job's source snapshot. Exercise the
	// workflow's independent guard with a persisted replacement binding, keeping
	// the exact source tuple and old typed evidence but no run from the new job.
	replacementConfig := config
	replacementConfig.AutoChecks, replacementConfig.AutoReports = false, false
	replacementJob, err := repo.CreateManagedPostgresJob(ctx, workspace.ID, owner.ID, "Classification replacement", digest(slug+"-replacement"), replacementConfig)
	want(err, nil)
	want(repo.BindCheckPostgres(ctx, workspace.ID, owner.ID, g02, replacementJob.ID), store.ErrNotFound)
	result, err := pool.Exec(ctx, `UPDATE plume.postgres_check_bindings SET job_id=$2
		WHERE check_id=$1 AND source_host=$3 AND source_port=$4 AND source_database=$5`,
		g02, replacementJob.ID, config.Host, config.Port, config.Database)
	want(err, nil)
	if result.RowsAffected() != 1 {
		t.Fatal("replacement fixture changed the source target instead of only the job")
	}
	rebound, err := repo.CheckDetail(ctx, workspace.ID, owner.ID, g02)
	want(err, nil)
	if rebound.CanSubmit {
		t.Fatal("replacement job without a source run accepted a manual retest")
	}
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, g02), store.ErrEvidenceRequired)
	_, err = pool.Exec(ctx, "UPDATE plume.postgres_check_bindings SET job_id=$2 WHERE check_id=$1", g02, job.ID)
	want(err, nil)

	// A draft can still be presented to a reviewer; the same job replacement
	// must not hide its old typed evidence and fall back to generic verification.
	replacementCheck, err := repo.CreateCheck(ctx, workspace.ID, owner.ID, store.CheckInput{AssetID: byName["contacts"], RequirementCode: "G02", OwnerTeam: "Data", Note: "Replacement binding guard"})
	want(err, nil)
	want(repo.AddEvidence(ctx, workspace.ID, owner.ID, replacementCheck.ID, store.EvidenceInput{Kind: "observation", SourceSystem: "Fixture source", SourceRecord: "old-job-snapshot", Summary: "Preserved original source classification", ObservedAt: clock()}), nil)
	_, err = pool.Exec(ctx, `INSERT INTO plume.postgres_check_evidence (evidence_id,automation_run_id,cycle_id,fact_digest,observation)
		SELECT (SELECT max(id) FROM plume.check_evidence WHERE check_id=$1),p.automation_run_id,p.cycle_id,p.fact_digest,p.observation
		FROM plume.postgres_check_evidence p JOIN plume.check_evidence e ON e.id=p.evidence_id
		WHERE e.check_id=$2 ORDER BY e.id DESC LIMIT 1`, replacementCheck.ID, checkID(byName["contacts"]))
	want(err, nil)
	_, err = pool.Exec(ctx, `INSERT INTO plume.postgres_check_bindings (check_id,job_id,source_host,source_port,source_database)
		SELECT $1,$2,source_host,source_port,source_database FROM plume.postgres_check_bindings WHERE check_id=$3`,
		replacementCheck.ID, replacementJob.ID, checkID(byName["contacts"]))
	want(err, nil)
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, replacementCheck.ID), nil)
	replacementReview, err := repo.CheckDetail(ctx, workspace.ID, reviewer.ID, replacementCheck.ID)
	want(err, nil)
	if replacementReview.CanVerify {
		t.Fatal("job replacement hid old typed classification and enabled generic verification")
	}
	want(repo.ReviewCheck(ctx, workspace.ID, reviewer.ID, replacementCheck.ID, replacementReview.ReviewRevision, "verified", "Must await the replacement collector"), store.ErrEvidenceRequired)
	replacementRuns, err := repo.ListAutomationRuns(ctx, workspace.ID, owner.ID, replacementJob.ID)
	want(err, nil)
	if len(replacementRuns) != 0 {
		t.Fatal("replacement fixture unexpectedly collected a source snapshot")
	}

	// A source read can begin before confirmation and finish after it. Matching
	// the new revision's attributes must not turn that older observation fresh.
	newOverlapCheck := func(note string) int64 {
		t.Helper()
		check, err := repo.CreateCheck(ctx, workspace.ID, owner.ID, store.CheckInput{AssetID: first.Asset.ID, RequirementCode: "G02", OwnerTeam: "Data", Note: note})
		want(err, nil)
		want(repo.BindCheckPostgres(ctx, workspace.ID, owner.ID, check.ID, job.ID), nil)
		return check.ID
	}
	overlapDraft := newOverlapCheck("Observe stale-to-fresh evidence deduplication")
	overlapReview := newOverlapCheck("Independently review a long-running source observation")
	late := collectAt("began-before-confirmation", tables, beforeCorrection)
	if late.ObservedAt.After(fixed.History[0].CreatedAt) || !late.CreatedAt.After(fixed.History[0].CreatedAt) {
		t.Fatal("fixture did not reproduce a source read spanning manual confirmation")
	}
	lateClass := detail("customers")
	if lateClass.Automatic == nil || !lateClass.Automatic.Stale || lateClass.Automatic.Comparison != "matched" || lateClass.Automatic.ClassificationRevision != fixed.Revision || lateClass.Revision != fixed.Revision || lateClass.Automatic.RunID != late.RunID {
		t.Fatal("a late-finishing read predating manual confirmation became current despite matching attributes")
	}
	lateRetest, err := repo.CheckDetail(ctx, workspace.ID, owner.ID, g02)
	want(err, nil)
	if lateRetest.CanSubmit {
		t.Fatal("a source read predating remediation enabled resubmission")
	}
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, g02), store.ErrEvidenceRequired)
	staleDraft, err := repo.CheckDetail(ctx, workspace.ID, owner.ID, overlapDraft)
	want(err, nil)
	if len(staleDraft.Evidence) != 1 || staleDraft.Evidence[0].Postgres == nil || staleDraft.Evidence[0].Postgres.Classification == nil || !staleDraft.Evidence[0].Postgres.Classification.Stale {
		t.Fatal("typed evidence failed to preserve the stale source observation")
	}
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, overlapReview), nil)
	lateReview, err := repo.CheckDetail(ctx, workspace.ID, reviewer.ID, overlapReview)
	want(err, nil)
	if lateReview.CanVerify {
		t.Fatal("matching but stale source evidence enabled independent verification")
	}
	want(repo.ReviewCheck(ctx, workspace.ID, reviewer.ID, overlapReview, lateReview.ReviewRevision, "verified", "The read began before confirmation"), store.ErrEvidenceRequired)
	want(repo.ReviewCheck(ctx, workspace.ID, reviewer.ID, overlapReview, lateReview.ReviewRevision, "changes", "Collect after confirmation and remediation"), nil)
	want(repo.AddEvidence(ctx, workspace.ID, owner.ID, overlapReview, store.EvidenceInput{Kind: "remediation", SourceSystem: "Classification review", SourceRecord: "timing-remediation", Summary: "Require a new source read after confirmation", ObservedAt: clock()}), nil)

	freshRun := collect("source-retest", tables)
	if !freshRun.ObservedAt.After(late.CreatedAt) {
		t.Fatal("replacement source observation did not genuinely begin after the previous completion")
	}
	freshClass := detail("customers")
	if freshClass.Automatic == nil || freshClass.Automatic.Stale || freshClass.Automatic.Comparison != "matched" || freshClass.Revision != fixed.Revision || !reflect.DeepEqual(freshClass.Automatic.Evaluation, lateClass.Automatic.Evaluation) {
		t.Fatal("a genuinely later read could not restore freshness with unchanged source facts")
	}
	freshDraft, err := repo.CheckDetail(ctx, workspace.ID, owner.ID, overlapDraft)
	want(err, nil)
	if len(freshDraft.Evidence) != 2 || freshDraft.Evidence[0].Postgres == nil || freshDraft.Evidence[0].Postgres.Classification == nil || freshDraft.Evidence[0].Postgres.Classification.Stale || freshDraft.Evidence[0].AutomationRunID != freshRun.RunID || !freshDraft.Evidence[1].Postgres.Classification.Stale {
		t.Fatal("same-fact stale-to-fresh transition was deduplicated or rewrote its original evidence")
	}
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, overlapReview), nil)
	freshReview, err := repo.CheckDetail(ctx, workspace.ID, reviewer.ID, overlapReview)
	want(err, nil)
	if !freshReview.CanVerify {
		t.Fatal("new source retest after confirmation could not be independently reviewed")
	}
	want(repo.ReviewCheck(ctx, workspace.ID, reviewer.ID, overlapReview, freshReview.ReviewRevision, "verified", "Reviewed the genuinely new source observation"), nil)
	retested, err := repo.CheckDetail(ctx, workspace.ID, owner.ID, g02)
	want(err, nil)
	if retested.Status != "needs_work" || !retested.CanSubmit || len(retested.Evidence) == 0 || retested.Evidence[0].Kind != "retest" || retested.Evidence[0].Postgres == nil || retested.Evidence[0].Postgres.Classification == nil || retested.Evidence[0].Postgres.Classification.Comparison != "matched" {
		t.Fatal("source-bound automatic retest did not complete the remediation evidence")
	}
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, g02), nil)
	retestReview := checkDetail()
	if !retestReview.CanVerify {
		t.Fatal("matching current source retest could not be independently verified")
	}
	want(repo.ReviewCheck(ctx, workspace.ID, reviewer.ID, g02, retestReview.ReviewRevision, "verified", "Reviewed source retest and classification scope"), nil)
	verified := checkDetail()
	if verified.Status != "verified" {
		t.Fatal("independent review did not complete the check")
	}
	verifiedEvidence := verified.Evidence
	config.Database = "classification_source_retargeted"
	_, err = repo.UpdateManagedPostgresJob(ctx, workspace.ID, owner.ID, job.ID, 1, config)
	want(err, nil)
	if current := detail("contacts"); current.Automatic == nil || !current.Automatic.Stale {
		t.Fatal("retargeted source retained a fresh classification result")
	}
	contactsCheck := checkID(byName["contacts"])
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, contactsCheck), nil)
	staleReview, err := repo.CheckDetail(ctx, workspace.ID, reviewer.ID, contactsCheck)
	want(err, nil)
	if staleReview.CanVerify {
		t.Fatal("source retarget allowed stale evidence to be verified")
	}
	want(repo.ReviewCheck(ctx, workspace.ID, reviewer.ID, contactsCheck, staleReview.ReviewRevision, "verified", "Check old source evidence"), store.ErrEvidenceRequired)
	collect("new-source", tables)
	if !reflect.DeepEqual(checkDetail().Evidence, verifiedEvidence) {
		t.Fatal("a new source mutated the sealed check's evidence")
	}
	withoutContacts := slices.DeleteFunc(slices.Clone(tables), func(table collector.PostgreSQLTable) bool { return table.Name == "contacts" })
	collect("table-absent", withoutContacts)
	if missing := detail("contacts"); missing.Automatic == nil || !missing.Automatic.Stale {
		t.Fatal("table omitted from the latest source snapshot retained a fresh automatic result")
	}
	want(repo.QueueManagedPostgresJob(ctx, workspace.ID, owner.ID, job.ID), nil)
	failedClaim, err := repo.ClaimManagedPostgresJob(ctx, slug+"-failed", time.Minute)
	want(err, nil)
	if failedClaim == nil || failedClaim.JobID != job.ID {
		t.Fatal("missing failure fixture claim")
	}
	failedRun, err := repo.FinishManagedPostgresJob(ctx, *failedClaim, store.AutomationDelivery{Digest: digest("failed"), ObservedAt: clock(), FailureCode: "collection_timeout"})
	want(err, nil)
	if failedRun.Status != "error" {
		t.Fatal("failed source observation was accepted as successful")
	}
	if failed := detail("customers"); failed.Automatic == nil || !failed.Automatic.Stale || failed.Revision != fixed.Revision {
		t.Fatal("failed collection retained a fresh classification or changed its history")
	}
	collect("healthy-again", tables)
	if healthy := detail("customers"); healthy.Automatic == nil || healthy.Automatic.Stale || healthy.Revision != fixed.Revision {
		t.Fatal("new successful source snapshot did not restore currentness without rewriting human review")
	}
	archiveFinal, err := repo.ReportArchive(ctx, workspace.ID, reviewer.ID, initial.ID)
	want(err, nil)
	finalJSON, _ := json.Marshal(archiveFinal)
	if string(finalJSON) != string(beforeJSON) {
		t.Fatal("remediation or source changes rewrote the archived historical report")
	}
}
