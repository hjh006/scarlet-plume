package store_test

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestClassificationContextAssessmentTemplateRevisions(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
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
	slug := fmt.Sprintf("assessment-context-templates-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Assessment context templates"))
	user, err := repo.FindUser(ctx, slug)
	must(err)
	ws, err := repo.Membership(ctx, user.ID, slug)
	must(err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM plume.workspaces WHERE id=$1`, ws.ID)
		_, _ = pool.Exec(cleanup, `DELETE FROM plume.users WHERE id=$1`, user.ID)
	})
	templates, err := repo.ListAssessmentTemplates(ctx, ws.ID, user.ID)
	must(err)
	byKey := map[string]store.AssessmentTemplate{}
	for _, template := range templates {
		if template.BuiltIn {
			byKey[template.Key] = template
		}
	}
	var oldPIA, newPIA int64
	for _, key := range []string{"assessment-pia", "assessment-cross-border", "assessment-business-change"} {
		latest, ok := byKey[key]
		if !ok || latest.Revision != 2 || latest.State != "published" {
			t.Fatalf("missing additive v2 for %s: %+v", key, latest)
		}
		var oldID int64
		var oldQuestions []store.AssessmentQuestion
		must(pool.QueryRow(ctx, `SELECT t.id,r.questions FROM plume.check_templates t JOIN plume.assessment_template_revisions r ON r.revision_id=t.id WHERE t.workspace_id IS NULL AND t.template_key=$1 AND t.version=1`, key).Scan(&oldID, &oldQuestions))
		if len(latest.Questions) != len(oldQuestions)+1 || !reflect.DeepEqual(latest.Questions[:len(oldQuestions)], oldQuestions) {
			t.Fatalf("old questions changed for %s", key)
		}
		for _, question := range oldQuestions {
			if question.Key == "classification_context" {
				t.Fatal("old adopted template gained new question")
			}
		}
		question := latest.Questions[len(oldQuestions)]
		if question.Key != "classification_context" || question.FactKey != "classification_context" || question.Kind != "text" || question.Required || question.Obligation != "recommended" || question.RequirementCode != "G01" || question.BasisURL != oldQuestions[0].BasisURL {
			t.Fatalf("context can stand in for mandatory or new legal requirement: %+v", question)
		}
		var oldCount, newCount int
		must(pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM plume.check_template_items WHERE template_revision_id=$1),(SELECT count(*) FROM plume.check_template_items WHERE template_revision_id=$2)`, oldID, latest.ID).Scan(&oldCount, &newCount))
		if oldCount != len(oldQuestions) || newCount != oldCount+1 {
			t.Fatal("template item revision boundary lost")
		}
		if key == "assessment-pia" {
			oldPIA, newPIA = oldID, latest.ID
		}
	}
	if ai := byKey["assessment-ai"]; ai.Revision != 2 || ai.Questions[len(ai.Questions)-1].Key != "application_configuration" {
		t.Fatal("existing AI template revision changed")
	}
	asset, err := repo.CreateAsset(ctx, ws.ID, "Synthetic scoped asset", "inventory", "Governance")
	must(err)
	activity, err := repo.CreateActivity(ctx, ws.ID, user.ID, store.ActivityInput{AssetID: asset.ID, Name: "Synthetic processing", Kind: "internal", Purpose: "Synthetic assessment template verification"})
	must(err)
	create := func(templateID int64) store.AssessmentDetail {
		t.Helper()
		detail, err := repo.CreateAssessment(ctx, ws.ID, user.ID, store.AssessmentInput{ActivityID: activity.ID, TemplateRevisionID: templateID, Title: "Synthetic PIA", DueAt: time.Now().Add(24 * time.Hour)})
		must(err)
		return detail
	}
	oldAssessment := create(oldPIA)
	if oldAssessment.Template.ID != oldPIA || oldAssessment.Template.Revision != 1 {
		t.Fatal("old adopted revision not retained")
	}
	newAssessment := create(newPIA)
	foundProcessingGap := false
	for _, gap := range newAssessment.Gaps {
		if gap.Key == "classification_context" {
			t.Fatal("optional context became a mandatory gap")
		}
		if gap.Key == "processing_scope" {
			foundProcessingGap = true
		}
	}
	if !foundProcessingGap {
		t.Fatal("missing business scope hidden by optional context")
	}
	for _, question := range oldAssessment.Template.Questions {
		if question.Key == "classification_context" {
			t.Fatal("new question leaked into old assessment")
		}
	}
}
