package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAssessmentsVersionedIndependentLifecycle(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := New(pool)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	want := func(e, target error) {
		t.Helper()
		if !errors.Is(e, target) {
			t.Fatalf("got %v want %v", e, target)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("assessment-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Assessment lifecycle"))
	owner, err := repo.FindUser(ctx, slug)
	must(err)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	usernames := []string{slug, slug + "-operator", slug + "-reviewer", slug + "-second-owner", slug + "-foreign"}
	member := func(name, role string) int64 {
		t.Helper()
		var id int64
		must(pool.QueryRow(ctx, `INSERT INTO plume.users(username,password_hash)VALUES($1,'unused')RETURNING id`, name).Scan(&id))
		_, err := pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role)VALUES($1,$2,$3)`, ws.ID, id, role)
		must(err)
		return id
	}
	operator := member(usernames[1], "operator")
	reviewer := member(usernames[2], "reviewer")
	secondOwner := member(usernames[3], "owner")
	must(repo.Bootstrap(ctx, usernames[4], "unused", slug+"-other", "Other"))
	otherUser, err := repo.FindUser(ctx, usernames[4])
	must(err)
	other, err := repo.Membership(ctx, otherUser.ID, slug+"-other")
	must(err)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=ANY($1)`, []int64{ws.ID, other.ID})
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE username=ANY($1)`, usernames)
	})
	templates, err := repo.ListAssessmentTemplates(ctx, ws.ID, owner.ID)
	must(err)
	if len(templates) != 4 {
		t.Fatalf("got %d builtins", len(templates))
	}
	var base AssessmentTemplate
	for _, v := range templates {
		if v.Kind == "pia" {
			base = v
		}
	}
	if len(base.Questions) != 7 {
		t.Fatal("missing builtin PIA clauses")
	}
	_, err = repo.SaveAssessmentTemplate(ctx, ws.ID, operator, 0, AssessmentTemplateInput{})
	want(err, ErrForbidden)
	draftInput := AssessmentTemplateInput{Key: "local-pia", Title: "Local PIA", Kind: "pia", BaseRevisionID: base.RevisionID}
	draft, err := repo.SaveAssessmentTemplate(ctx, ws.ID, owner.ID, 0, draftInput)
	must(err)
	version1, err := repo.PublishAssessmentTemplate(ctx, ws.ID, owner.ID, draft.ID, draft.Revision)
	must(err)
	asset, err := repo.CreateAsset(ctx, ws.ID, "Assessment dataset", "sample", "Data")
	must(err)
	activity, err := repo.CreateActivity(ctx, ws.ID, owner.ID, ActivityInput{AssetID: asset.ID, Name: "Synthetic processing", Kind: "internal", Purpose: "Process approved sample"})
	must(err)
	in := AssessmentInput{ActivityID: activity.ID, TemplateRevisionID: version1.ID, Title: "PIA fixture", DueAt: time.Now().Add(24 * time.Hour), RefreshMinutes: 5}
	assessment, err := repo.CreateAssessment(ctx, ws.ID, operator, in)
	must(err)
	if assessment.Applicability != "unknown" || len(assessment.Gaps) == 0 {
		t.Fatal("missing business facts must remain unknown")
	}
	for _, fact := range assessment.Facts {
		if fact.Key == "deduplicated_subject_count" && fact.State != "unknown" {
			t.Fatal("sample count became subject count")
		}
	}
	_, err = repo.Assessment(ctx, ws.ID, otherUser.ID, assessment.ID)
	want(err, ErrNotFound)
	_, err = repo.Assessment(ctx, other.ID, otherUser.ID, assessment.ID)
	want(err, ErrNotFound)
	// A second published revision must not mutate an already adopted template.
	draftInput.BaseRevisionID = version1.RevisionID
	draftInput.Questions = append([]AssessmentQuestion(nil), version1.Questions...)
	draftInput.Questions[1].Title = "Updated purpose question"
	draft2, err := repo.SaveAssessmentTemplate(ctx, ws.ID, owner.ID, 0, draftInput)
	must(err)
	version2, err := repo.PublishAssessmentTemplate(ctx, ws.ID, owner.ID, draft2.ID, draft2.Revision)
	must(err)
	if version2.Revision != 2 || version2.ID == version1.ID {
		t.Fatal("template version did not advance")
	}
	adopted, err := repo.Assessment(ctx, ws.ID, operator, assessment.ID)
	must(err)
	autoFilled := false
	for _, answer := range adopted.Answers {
		if answer.Key == "purpose" && answer.Value == activity.Purpose && answer.Note != "" {
			autoFilled = true
		}
	}
	if !autoFilled {
		t.Fatal("source-bound purpose was not automatically adopted")
	}
	if adopted.Template.ID != version1.ID || adopted.Template.Questions[1].Title == draftInput.Questions[1].Title {
		t.Fatal("immutable adopted template changed")
	}
	// Existing check evidence is reused, including an old selected record outside the latest 200.
	check, err := repo.CreateCheck(ctx, ws.ID, owner.ID, CheckInput{AssetID: asset.ID, ActivityID: &activity.ID, RequirementCode: "P04", OwnerTeam: "Governance", Note: "Assess concrete synthetic processing facts"})
	must(err)
	var evidenceID int64
	must(pool.QueryRow(ctx, `INSERT INTO plume.check_evidence(check_id,kind,source_system,source_record,summary,observed_at,created_by)VALUES($1,'observation','fixture','approved:1','Reviewed assessment facts',clock_timestamp(),$2)RETURNING id`, check.ID, owner.ID).Scan(&evidenceID))
	_, err = pool.Exec(ctx, `INSERT INTO plume.check_evidence(check_id,kind,source_system,source_record,summary,observed_at,created_by) SELECT $1,'observation','fixture','new:'||n::text,'Later observation',clock_timestamp(),$2 FROM generate_series(1,205)n`, check.ID, owner.ID)
	must(err)
	number := 12.0
	answers := []AssessmentAnswer{}
	for _, q := range version1.Questions {
		a := AssessmentAnswer{Key: q.Key}
		switch q.Kind {
		case "single":
			a.Value = "yes"
		case "text":
			a.Value = "Verified synthetic business fact"
		case "number":
			a.Number = &number
		case "evidence":
			a.EvidenceIDs = []int64{evidenceID}
		}
		answers = append(answers, a)
	}
	saved, err := repo.SaveAssessmentAnswers(ctx, ws.ID, operator, assessment.ID, AssessmentAnswersInput{Revision: adopted.Revision, Answers: answers, CheckIDs: []int64{check.ID}})
	must(err)
	if len(saved.Risks) != 1 {
		t.Fatal("unfinished linked remediation did not block approval")
	}
	found := false
	for _, e := range saved.Evidence {
		if e.ID == evidenceID {
			found = true
		}
	}
	if !found || !saved.EvidenceTruncated {
		t.Fatal("selected older evidence missing from bounded report")
	}
	_, err = repo.SaveAssessmentAnswers(ctx, ws.ID, operator, assessment.ID, AssessmentAnswersInput{Revision: adopted.Revision, Answers: answers})
	want(err, ErrConflict)
	// Any answer editor is permanently excluded from independent approval.
	saved, err = repo.SaveAssessmentAnswers(ctx, ws.ID, secondOwner, assessment.ID, AssessmentAnswersInput{Revision: saved.Revision, Answers: answers})
	must(err)
	submitted, err := repo.ReviewAssessment(ctx, ws.ID, operator, assessment.ID, AssessmentReviewInput{Revision: saved.Revision, Action: "submit"})
	must(err)
	_, err = repo.ReviewAssessment(ctx, ws.ID, owner.ID, assessment.ID, AssessmentReviewInput{Revision: submitted.Revision, Action: "approve", Note: "Evidence contributor self approval"})
	want(err, ErrForbidden)
	_, err = repo.ReviewAssessment(ctx, ws.ID, secondOwner, assessment.ID, AssessmentReviewInput{Revision: submitted.Revision, Action: "approve", Note: "Editor self approval"})
	want(err, ErrForbidden)
	_, err = repo.ReviewAssessment(ctx, ws.ID, operator, assessment.ID, AssessmentReviewInput{Revision: submitted.Revision, Action: "approve", Note: "Creator self approval"})
	want(err, ErrForbidden)
	approved, err := repo.ReviewAssessment(ctx, ws.ID, reviewer, assessment.ID, AssessmentReviewInput{Revision: submitted.Revision, Action: "approve", Note: "Independently verified synthetic evidence"})
	must(err)
	if approved.Status != "approved" {
		t.Fatal("not approved")
	}
	before, err := repo.AssessmentReport(ctx, ws.ID, reviewer, assessment.ID)
	must(err)
	noChange, err := repo.UpdateActivity(ctx, ws.ID, operator, activity.ID, ActivityUpdateInput{ActivityInput: activity.ActivityInput, Revision: activity.Revision})
	must(err)
	if noChange.Revision != activity.Revision {
		t.Fatal("same input created a revision")
	}
	unchanged, err := repo.Assessment(ctx, ws.ID, reviewer, assessment.ID)
	must(err)
	if unchanged.Stale {
		t.Fatal("no-op invalidated assessment")
	}
	changedInput := activity.ActivityInput
	changedInput.Purpose = "Changed business purpose"
	_, err = repo.UpdateActivity(ctx, ws.ID, reviewer, activity.ID, ActivityUpdateInput{ActivityInput: changedInput, Revision: activity.Revision})
	want(err, ErrForbidden)
	_, err = repo.UpdateActivity(ctx, ws.ID, otherUser.ID, activity.ID, ActivityUpdateInput{ActivityInput: changedInput, Revision: activity.Revision})
	want(err, ErrNotFound)
	assetRebind := changedInput
	assetRebind.AssetID = asset.ID + 1
	_, err = repo.UpdateActivity(ctx, ws.ID, operator, activity.ID, ActivityUpdateInput{ActivityInput: assetRebind, Revision: activity.Revision})
	want(err, ErrInvalid)
	changed, err := repo.UpdateActivity(ctx, ws.ID, operator, activity.ID, ActivityUpdateInput{ActivityInput: changedInput, Revision: activity.Revision})
	must(err)
	if changed.Revision != activity.Revision+1 || changed.CreatedAt != activity.CreatedAt {
		t.Fatal("activity revision or original timestamp lost")
	}
	_, err = repo.UpdateActivity(ctx, ws.ID, operator, activity.ID, ActivityUpdateInput{ActivityInput: changedInput, Revision: activity.Revision})
	want(err, ErrConflict)
	immediate, err := repo.Assessment(ctx, ws.ID, reviewer, assessment.ID)
	must(err)
	if !immediate.Stale || !immediate.ReassessmentPending {
		t.Fatal("activity update did not immediately invalidate assessment")
	}
	history, err := repo.ActivityHistory(ctx, ws.ID, reviewer, activity.ID, 0, 50)
	must(err)
	if len(history) != 1 || history[0].Before.Purpose != activity.Purpose || history[0].After.Purpose != changedInput.Purpose || history[0].Actor != usernames[1] {
		t.Fatal("activity before/after actor history incomplete")
	}
	_, err = repo.ActivityHistory(ctx, other.ID, otherUser.ID, activity.ID, 0, 50)
	want(err, ErrNotFound)
	_, err = pool.Exec(ctx, `UPDATE plume.assessments SET next_refresh_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, assessment.ID)
	must(err)
	refreshed, err := repo.RefreshDueAssessments(ctx, 50)
	must(err)
	if refreshed < 1 {
		t.Fatal("periodic refresh missed assessment")
	}
	after, err := repo.AssessmentReport(ctx, ws.ID, reviewer, assessment.ID)
	must(err)
	if !after.Stale || !after.ReassessmentPending || after.Activity.Purpose != before.Activity.Purpose || after.Template.ID != version1.ID {
		t.Fatal("staleness or immutable report lost")
	}
	var task string
	must(pool.QueryRow(ctx, `SELECT state FROM plume.assessment_reassessment_tasks WHERE assessment_id=$1`, assessment.ID).Scan(&task))
	if task != "pending" {
		t.Fatal("missing reassessment task")
	}
	must(repo.DisableAssessmentTemplate(ctx, ws.ID, owner.ID, version2.ID))
	in.TemplateRevisionID = version2.ID
	_, err = repo.CreateAssessment(ctx, ws.ID, operator, in)
	want(err, ErrInvalid)
	_, err = repo.Assessment(ctx, ws.ID, reviewer, assessment.ID)
	must(err)
}
