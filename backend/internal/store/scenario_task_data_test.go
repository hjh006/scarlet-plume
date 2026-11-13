package store

import (
	"context"
	"errors"
	"fmt"
	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"strings"
	"testing"
	"time"
)

func TestScenarioTaskDataClassificationMaterialsAndAssessmentPrefill(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	want := func(got, expected error) {
		t.Helper()
		if !errors.Is(got, expected) {
			t.Fatalf("got %v want %v", got, expected)
		}
	}
	asset, e := f.repo.CreateAsset(ctx, f.ws.ID, "Customer contact", "CRM", "Data")
	want(e, nil)
	legacy, e := f.repo.CreateAsset(ctx, f.ws.ID, "Imported classified asset", "CRM", "Data")
	want(e, nil)
	_, e = f.pool.Exec(ctx, `UPDATE plume.assets SET category='personal',business_level='confidential',classification_basis='Imported human determination' WHERE id=$1`, legacy.ID)
	want(e, nil)
	old, e := f.repo.ScenarioTaskData(ctx, f.ws.ID, f.user.ID, legacy.ID, 0, "personal", "inventory")
	want(e, nil)
	if old.Classification.Trust != "legacy_declared" || old.Classification.Item.Asset.Category != "personal" || old.Classification.Usable || scenarioKnownFact(old.Facts, "asset_category") || !scenarioKnownFact(old.Facts, "classification_legacy_record") {
		t.Fatal("legacy values were discarded or promoted into a verified conclusion")
	}
	empty, e := f.repo.ScenarioTaskData(ctx, f.ws.ID, f.user.ID, 0, 0, "crossborder", "procedures")
	want(e, nil)
	if empty.MaterialCounts.Missing < 1 || empty.MaterialCounts.NotApplicable != 0 {
		t.Fatal("missing scope became complete or not-applicable")
	}
	_, e = f.repo.ScenarioTaskData(ctx, f.ws.ID, f.user.ID, asset.ID, 0, "personal", "invented-task")
	want(e, ErrInvalid)
	_, e = f.repo.ScenarioTaskData(ctx, f.ws.ID, f.user.ID+1000000, asset.ID, 0, "personal", "pia")
	want(e, ErrNotFound)
	_, e = f.pool.Exec(ctx, `UPDATE plume.assets SET category='personal',business_level='internal',classification_origin='automatic' WHERE id=$1`, asset.ID)
	want(e, nil)
	candidate, e := f.repo.ScenarioTaskData(ctx, f.ws.ID, f.user.ID, asset.ID, 0, "personal", "pia")
	want(e, nil)
	if candidate.Classification.Trust != "candidate" || candidate.Classification.Usable || scenarioKnownFact(candidate.Facts, "classification_context") || scenarioKnownFact(candidate.Facts, "asset_category") {
		t.Fatal("automatic candidate promoted to manual/legal fact")
	}
	taxonomy, e := f.repo.SaveClassificationTaxonomy(ctx, f.ws.ID, f.user.ID, 0, ClassificationTaxonomyInput{Name: "Contact data", Enabled: true})
	want(e, nil)
	reviewed, e := f.repo.UpdateAssetClassification(ctx, f.ws.ID, f.user.ID, asset.ID, AssetClassificationInput{Revision: 0, Category: "personal", BusinessLevel: "confidential", StatutoryCategory: "important", Basis: "Human reviewed classification scope", StandardID: "education-jyt-0661-2025", LevelCode: "l4", RuleID: "education-l4-scale", TaxonomyID: &taxonomy.ID})
	want(e, nil)
	activity, e := f.repo.CreateActivity(ctx, f.ws.ID, f.user.ID, ActivityInput{AssetID: asset.ID, Name: "Customer handling", Kind: "internal", Purpose: "Support requests", Recipient: "Support team", Region: "Registered region", RetentionPolicy: "Registered retention", Safeguards: "Registered access control"})
	want(e, nil)
	material := func(data ScenarioData, key string) ScenarioMaterial {
		t.Helper()
		for _, m := range data.Materials {
			if m.Key == key {
				return m
			}
		}
		t.Fatalf("missing material %s", key)
		return ScenarioMaterial{}
	}
	for _, scope := range [][2]string{{"personal", "pia"}, {"domestic", "important-data"}, {"crossborder", "procedures"}} {
		data, e := f.repo.ScenarioTaskData(ctx, f.ws.ID, f.user.ID, 0, activity.ID, scope[0], scope[1])
		want(e, nil)
		if data.AssetID != asset.ID || !data.Classification.ManuallyConfirmed || !data.Classification.Usable || data.Classification.Item.Standard == nil || data.Classification.Item.Taxonomy.Path != taxonomy.Name {
			t.Fatalf("trusted classification provenance missing %+v", data.Classification)
		}
		if material(data, "classification").Status != "auto_filled" || material(data, "purpose").Status != "auto_filled" || material(data, "source_scope").Status != "missing" || data.MaterialCounts.NotApplicable != 0 {
			t.Fatalf("material states inferred unsupported facts %+v", data.MaterialCounts)
		}
		if scope[0] == "domestic" && material(data, "important_identification").Status != "missing" {
			t.Fatal("important label auto-completed statutory identification")
		}
		if scope[0] == "crossborder" && (material(data, "subject_count").Status != "missing" || material(data, "transfer_path").Status != "missing" || material(data, "external_procedure").Status != "missing") {
			t.Fatal("activity region/classification inferred crossborder procedures")
		}
	}
	templates, e := f.repo.ListAssessmentTemplates(ctx, f.ws.ID, f.user.ID)
	want(e, nil)
	var selected AssessmentTemplate
	for _, item := range templates {
		if item.Key == "assessment-pia" {
			selected = item
		}
	}
	if selected.Revision < 2 || selected.RevisionID == nil {
		t.Fatal("missing current versioned template")
	}
	assessment, e := f.repo.CreateAssessment(ctx, f.ws.ID, f.user.ID, AssessmentInput{ActivityID: activity.ID, TemplateRevisionID: *selected.RevisionID, Title: "Scope assessment", DueAt: time.Now().Add(24 * time.Hour)})
	want(e, nil)
	contextAnswer := ""
	for _, answer := range assessment.Answers {
		if answer.Key == "classification_context" {
			contextAnswer = answer.Value
		}
		if answer.Key == "processing_scope" {
			t.Fatal("partial classification filled compound required scope")
		}
	}
	if !strings.Contains(contextAnswer, "education") && !strings.Contains(contextAnswer, "JY/T") {
		t.Fatalf("classification context was not automatically adopted: %s", contextAnswer)
	}
	if !strings.Contains(contextAnswer, taxonomy.Name) || len(assessment.Gaps) == 0 {
		t.Fatal("context lost taxonomy or falsely completed assessment")
	}
	continued, e := f.repo.ScenarioTaskData(ctx, f.ws.ID, f.user.ID, asset.ID, activity.ID, "personal", "pia")
	want(e, nil)
	lawful := material(continued, "lawful_basis")
	if lawful.Status != "needs_confirmation" || lawful.Action.Page != fmt.Sprintf("assessments/%d", assessment.ID) || !strings.Contains(lawful.Guidance, "已有同一活动") {
		t.Fatalf("existing assessment was ignored or duplicate creation requested %+v", lawful)
	}
	// A superficially matching revision is insufficient when the current manual
	// values no longer match their immutable history snapshot.
	_, e = f.pool.Exec(ctx, `UPDATE plume.assets SET classification_basis='unversioned alteration' WHERE id=$1`, asset.ID)
	want(e, nil)
	mismatch, e := f.repo.ScenarioTaskData(ctx, f.ws.ID, f.user.ID, asset.ID, activity.ID, "personal", "pia")
	want(e, nil)
	if mismatch.Classification.ManuallyConfirmed || mismatch.Classification.Usable {
		t.Fatal("revision number alone established confirmation")
	}
	_, e = f.pool.Exec(ctx, `UPDATE plume.assets SET classification_basis=$2 WHERE id=$1`, asset.ID, reviewed.Asset.ClassificationBasis)
	want(e, nil)
	_, e = f.repo.SaveClassificationTaxonomy(ctx, f.ws.ID, f.user.ID, taxonomy.ID, ClassificationTaxonomyInput{Name: "Renamed category", Enabled: true, Revision: taxonomy.Revision})
	want(e, nil)
	stale, e := f.repo.ScenarioTaskData(ctx, f.ws.ID, f.user.ID, asset.ID, activity.ID, "personal", "pia")
	want(e, nil)
	if stale.Classification.Usable || scenarioKnownFact(stale.Facts, "classification_context") || material(stale, "classification").Status != "needs_confirmation" {
		t.Fatal("stale taxonomy remained an automatically adopted fact")
	}
	retained, e := f.repo.Assessment(ctx, f.ws.ID, f.user.ID, assessment.ID)
	want(e, nil)
	found := ""
	for _, a := range retained.Answers {
		if a.Key == "classification_context" {
			found = a.Value
		}
	}
	if found != contextAnswer {
		t.Fatal("adopted answer was rewritten after classification changed")
	}
	// Candidate scope changes do not create any business entities while previewing.
	var records int
	want(f.pool.QueryRow(ctx, `SELECT count(*) FROM plume.assessments WHERE workspace_id=$1`, f.ws.ID).Scan(&records), nil)
	if records != 1 {
		t.Fatal("scene selection created assessments")
	}
}

func TestScenarioTaskDataCurrentDiscoveryAndFailedSource(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	source, run := f.discovery(t, "mysql")
	_ = source
	run("collect", "success", []collector.DiscoveryTable{assessmentSourceTable("customers", "email")})
	entries, e := f.repo.ListCatalogEntries(ctx, f.ws.ID, "table", "customers")
	if e != nil || len(entries) != 1 {
		t.Fatal(e)
	}
	assetID := entries[0].AssetID
	current, e := f.repo.GetAssetClassification(ctx, f.ws.ID, f.user.ID, assetID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.repo.UpdateAssetClassification(ctx, f.ws.ID, f.user.ID, assetID, AssetClassificationInput{Revision: current.Revision, Category: "personal", BusinessLevel: "confidential", StatutoryCategory: "unknown", Basis: "Confirmed positive content scope"})
	if e != nil {
		t.Fatal(e)
	}
	// A new collection binds the recognition facts to the manual confirmation.
	run("collect", "success", []collector.DiscoveryTable{assessmentSourceTable("customers", "email")})
	good, e := f.repo.ScenarioTaskData(ctx, f.ws.ID, f.user.ID, assetID, 0, "personal", "inventory")
	if e != nil || !good.Classification.Usable || !scenarioKnownFact(good.Facts, "source_coverage") {
		t.Fatalf("current source unavailable: %v %+v", e, good.Classification)
	}
	run("collect", "error", nil)
	failed, e := f.repo.ScenarioTaskData(ctx, f.ws.ID, f.user.ID, assetID, 0, "personal", "inventory")
	if e != nil {
		t.Fatal(e)
	}
	if failed.Classification.Usable || scenarioKnownFact(failed.Facts, "asset_category") || scenarioKnownFact(failed.Facts, "source_coverage") {
		t.Fatal("failed source reused stale trusted facts")
	}
}

func TestScenarioCurrentManualClassificationIndependentOfOldPublication(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	asset, e := f.repo.CreateAsset(ctx, f.ws.ID, "Reconfirmed asset", "CRM", "Data")
	must(e)
	input := AssetClassificationInput{Revision: 0, Category: "personal", BusinessLevel: "confidential", StatutoryCategory: "important", Basis: "Initial human scope", StandardID: "education-jyt-0661-2025", LevelCode: "l4", RuleID: "education-l4-scale"}
	first, e := f.repo.UpdateAssetClassification(ctx, f.ws.ID, f.user.ID, asset.ID, input)
	must(e)
	reviewerName := f.user.Username + "-publication-reviewer"
	must(f.repo.ProvisionReviewer(ctx, f.ws.Slug, reviewerName, "unused"))
	reviewer, e := f.repo.FindUser(ctx, reviewerName)
	must(e)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = f.pool.Exec(cleanup, `DELETE FROM plume.workspaces WHERE id=$1`, f.ws.ID)
		_, _ = f.pool.Exec(cleanup, `DELETE FROM plume.users WHERE id=$1`, reviewer.ID)
	})
	publication, e := f.repo.CreateClassificationPublication(ctx, f.ws.ID, f.user.ID, ClassificationPublicationInput{Title: "Prior reviewed publication", Items: []ClassificationAssetRevision{{AssetID: asset.ID, Revision: first.Revision}}})
	must(e)
	_, e = f.repo.ReviewClassificationPublication(ctx, f.ws.ID, reviewer.ID, publication.ID, ClassificationPublicationReviewInput{Action: "publish"})
	must(e)
	input.Revision = first.Revision
	input.Basis = "New independently recorded human classification scope"
	_, e = f.repo.UpdateAssetClassification(ctx, f.ws.ID, f.user.ID, asset.ID, input)
	must(e)
	data, e := f.repo.ScenarioTaskData(ctx, f.ws.ID, f.user.ID, asset.ID, 0, "personal", "inventory")
	must(e)
	if !data.Classification.ManuallyConfirmed || !data.Classification.Usable || data.Classification.Publication == nil || data.Classification.Publication.Current || !scenarioKnownFact(data.Facts, "classification_context") {
		t.Fatalf("old publication prevented current manual classification reuse %+v", data.Classification)
	}
	publicationStatus := ""
	classificationStatus := ""
	for _, m := range data.Materials {
		if m.Key == "publication" {
			publicationStatus = m.Status
		}
		if m.Key == "classification" {
			classificationStatus = m.Status
		}
	}
	if publicationStatus != "needs_confirmation" || classificationStatus != "auto_filled" {
		t.Fatalf("publication and manual classification were conflated: %s %s", publicationStatus, classificationStatus)
	}
}
