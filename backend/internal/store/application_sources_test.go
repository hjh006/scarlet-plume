package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestApplicationSourceScopeFreshnessAndAssessmentUse(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
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
	slug := fmt.Sprintf("application-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Application sources"))
	must(repo.Bootstrap(ctx, slug+"-other", "unused", slug+"-other", "Other workspace"))
	owner, err := repo.FindUser(ctx, slug)
	must(err)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	foreign, err := repo.FindUser(ctx, slug+"-other")
	must(err)
	other, err := repo.Membership(ctx, foreign.ID, slug+"-other")
	must(err)
	var viewer int64
	must(pool.QueryRow(ctx, `INSERT INTO plume.users(username,password_hash)VALUES($1,'unused')RETURNING id`, slug+"-viewer").Scan(&viewer))
	_, err = pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role)VALUES($1,$2,'viewer')`, ws.ID, viewer)
	must(err)
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=ANY($1)`, []int64{ws.ID, other.ID})
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE id=ANY($1)`, []int64{owner.ID, foreign.ID, viewer})
	})
	asset, err := repo.CreateAsset(ctx, ws.ID, "AI application", "applications", "Governance")
	must(err)
	unbound, err := repo.CreateAsset(ctx, ws.ID, "Unbound application", "applications", "Governance")
	must(err)
	foreignAsset, err := repo.CreateAsset(ctx, other.ID, "Foreign application", "applications", "Governance")
	must(err)
	digest := assessmentDigest(slug)
	input := ApplicationSourceInput{Name: "Approved read-only API", AssetIDs: []int64{asset.ID}, MaxAgeMinutes: 60}
	_, err = repo.CreateApplicationSource(ctx, ws.ID, viewer, input, digest)
	want(err, ErrForbidden)
	_, err = repo.CreateApplicationSource(ctx, ws.ID, foreign.ID, input, digest)
	want(err, ErrNotFound)
	wrong := input
	wrong.AssetIDs = []int64{foreignAsset.ID}
	_, err = repo.CreateApplicationSource(ctx, ws.ID, owner.ID, wrong, digest)
	want(err, ErrNotFound)
	source, err := repo.CreateApplicationSource(ctx, ws.ID, owner.ID, input, digest)
	must(err)
	if source.Status != "never_run" {
		t.Fatal("unobserved source reported current")
	}
	list, err := repo.ListApplicationSources(ctx, ws.ID, viewer)
	must(err)
	if len(list) != 1 || list[0].ID != source.ID {
		t.Fatal("source inventory unavailable")
	}
	data := func(id int64) ScenarioData {
		t.Helper()
		d, e := repo.ScenarioData(ctx, ws.ID, viewer, id, 0)
		must(e)
		return d
	}
	fact := func(d ScenarioData, key string) AssessmentFact {
		t.Helper()
		for _, f := range d.Facts {
			if f.Key == key {
				return f
			}
		}
		return AssessmentFact{Key: key, State: "unknown"}
	}
	if fact(data(asset.ID), "application_model").State != "unknown" {
		t.Fatal("never-run source became evidence")
	}
	now := time.Now().UTC().Add(-time.Minute)
	submission := ApplicationSubmission{BatchID: "first", ObservedAt: now, Status: "success", Items: []ApplicationObservation{{AssetID: asset.ID, Facts: map[string]string{"application_name": "Internal assistant", "application_vendor": "Fixture vendor", "application_model": "model-v1", "application_region": "configured-region-a", "application_retention": "30 days", "application_training_use": "disabled"}}}}
	receipt, err := repo.ReceiveApplicationObservation(ctx, digest, submission)
	must(err)
	replay, err := repo.ReceiveApplicationObservation(ctx, digest, submission)
	must(err)
	if !replay.Replayed || replay.ID != receipt.ID {
		t.Fatal("identical retry not idempotent")
	}
	changed := submission
	changed.Status = "error"
	changed.Items = nil
	_, err = repo.ReceiveApplicationObservation(ctx, digest, changed)
	want(err, ErrConflict)
	d := data(asset.ID)
	f := fact(d, "application_model")
	if f.State != "known" || f.Value != "model-v1" || f.Source != "application_api" || f.ObservedAt == nil {
		t.Fatalf("scoped model missing: %+v", f)
	}
	if fact(data(unbound.ID), "application_model").State == "known" {
		t.Fatal("facts leaked to unbound asset")
	}
	_, err = repo.ScenarioData(ctx, other.ID, foreign.ID, asset.ID, 0)
	want(err, ErrNotFound)
	unauthorized := submission
	unauthorized.BatchID = "unbound"
	unauthorized.Items = []ApplicationObservation{{AssetID: unbound.ID, Facts: map[string]string{"application_model": "bad"}}}
	_, err = repo.ReceiveApplicationObservation(ctx, digest, unauthorized)
	want(err, ErrForbidden)
	unauthorized.Items[0].AssetID = foreignAsset.ID
	_, err = repo.ReceiveApplicationObservation(ctx, digest, unauthorized)
	want(err, ErrForbidden)
	invalid := submission
	invalid.BatchID = "invalid"
	invalid.Items = []ApplicationObservation{{AssetID: asset.ID, Facts: map[string]string{"public_ai_service": "yes"}}}
	_, err = repo.ReceiveApplicationObservation(ctx, digest, invalid)
	want(err, ErrInvalid)
	invalid.Items[0].Facts = map[string]string{"application_model": "sk-obvious-secret-value"}
	_, err = repo.ReceiveApplicationObservation(ctx, digest, invalid)
	want(err, ErrInvalid)
	// The new template adopts only the optional configuration summary. Legal and
	// contractual answers must not be completed by API claims.
	activity, err := repo.CreateActivity(ctx, ws.ID, owner.ID, ActivityInput{AssetID: asset.ID, Name: "Internal assistant use", Kind: "ai", Purpose: "Approved synthetic task"})
	must(err)
	templates, err := repo.ListAssessmentTemplates(ctx, ws.ID, owner.ID)
	must(err)
	var ai AssessmentTemplate
	for _, v := range templates {
		if v.Kind == "ai" {
			ai = v
		}
	}
	if ai.Revision != 2 || len(ai.Questions) != 8 {
		t.Fatal("additive AI template revision missing")
	}
	assessment, err := repo.CreateAssessment(ctx, ws.ID, owner.ID, AssessmentInput{ActivityID: activity.ID, TemplateRevisionID: ai.ID, Title: "Application fixture", DueAt: time.Now().Add(time.Hour), RefreshMinutes: 5})
	must(err)
	auto := false
	for _, a := range assessment.Answers {
		if a.Key == "application_configuration" {
			auto = strings.Contains(a.Value, "model-v1") && strings.Contains(a.Value, "尚需") && a.Note != ""
		}
		if a.Key == "public_service" || a.Key == "provider_terms" || a.Key == "pia_triggers" {
			t.Fatal("configuration prefilled a legal or contractual answer")
		}
	}
	if !auto {
		t.Fatal("application summary not automatically adopted")
	}
	for _, key := range []string{"public_ai_service", "lawful_basis", "deduplicated_subject_count"} {
		if fact(ScenarioData{Facts: assessment.Facts}, key).State != "unknown" {
			t.Fatalf("%s inferred from configuration", key)
		}
	}
	// Conflicting sources cannot silently select the first value for prefilling.
	secondInput := input
	secondInput.Name = "Second API"
	secondDigest := assessmentDigest(slug + "second")
	second, err := repo.CreateApplicationSource(ctx, ws.ID, owner.ID, secondInput, secondDigest)
	must(err)
	conflicting := submission
	conflicting.BatchID = "conflict"
	conflicting.Items = []ApplicationObservation{{AssetID: asset.ID, Facts: map[string]string{"application_model": "model-v2"}}}
	_, err = repo.ReceiveApplicationObservation(ctx, secondDigest, conflicting)
	must(err)
	d = data(asset.ID)
	conflictFact := fact(d, "application_model")
	if conflictFact.State != "unknown" || conflictFact.Source != "application_api_conflict" || !strings.Contains(conflictFact.Value, "model-v2") {
		t.Fatal("conflict silently selected a source")
	}
	if fact(d, "application_configuration").State != "unknown" {
		t.Fatal("conflicting summary became a prefill")
	}
	same := conflicting
	same.BatchID = "matching"
	same.ObservedAt = now.Add(time.Second)
	same.Items[0].Facts = map[string]string{"application_model": "model-v1"}
	_, err = repo.ReceiveApplicationObservation(ctx, secondDigest, same)
	must(err)
	if fact(data(asset.ID), "application_model").State != "known" {
		t.Fatal("matching independent configuration did not resolve conflict")
	}
	must(repo.RevokeApplicationSource(ctx, ws.ID, owner.ID, second.ID))
	_, err = repo.ReceiveApplicationObservation(ctx, secondDigest, same)
	want(err, ErrForbidden)
	// Failure and latest successful omission invalidate older observed settings.
	failed := ApplicationSubmission{BatchID: "failed", ObservedAt: now.Add(2 * time.Second), Status: "error", Items: []ApplicationObservation{}}
	_, err = repo.ReceiveApplicationObservation(ctx, digest, failed)
	must(err)
	if fact(data(asset.ID), "application_model").State != "unknown" {
		t.Fatal("failure retained old current fact")
	}
	omitted := ApplicationSubmission{BatchID: "omitted", ObservedAt: now.Add(3 * time.Second), Status: "success", Items: []ApplicationObservation{}}
	_, err = repo.ReceiveApplicationObservation(ctx, digest, omitted)
	must(err)
	if fact(data(asset.ID), "application_model").State != "unknown" {
		t.Fatal("latest omitted asset borrowed old fact")
	}
	old := submission
	old.BatchID = "old"
	old.ObservedAt = now.Add(-time.Second)
	_, err = repo.ReceiveApplicationObservation(ctx, digest, old)
	want(err, ErrConflict)
	fresh := submission
	fresh.BatchID = "restored"
	fresh.ObservedAt = now.Add(4 * time.Second)
	_, err = repo.ReceiveApplicationObservation(ctx, digest, fresh)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE plume.application_batches SET observed_at=now()-interval '2 hours' WHERE source_id=$1`, source.ID)
	must(err)
	if fact(data(asset.ID), "application_model").State != "unknown" {
		t.Fatal("expired config remained current")
	}
	// Replaying a historical success must not renew observed time or latest status.
	_, err = repo.ReceiveApplicationObservation(ctx, digest, fresh)
	must(err)
	if fact(data(asset.ID), "application_model").State != "unknown" {
		t.Fatal("replay renewed stale configuration")
	}
	fresh.BatchID = "owner-check"
	fresh.ObservedAt = time.Now().UTC()
	_, err = repo.ReceiveApplicationObservation(ctx, digest, fresh)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE plume.memberships SET role='viewer' WHERE workspace_id=$1 AND user_id=$2`, ws.ID, owner.ID)
	must(err)
	if fact(data(asset.ID), "application_model").State != "unknown" {
		t.Fatal("former owner's source remained trusted")
	}
	fresh.BatchID = "former-owner"
	_, err = repo.ReceiveApplicationObservation(ctx, digest, fresh)
	want(err, ErrForbidden)
	_, err = pool.Exec(ctx, `UPDATE plume.memberships SET role='owner' WHERE workspace_id=$1 AND user_id=$2`, ws.ID, owner.ID)
	must(err)
	must(repo.RevokeApplicationSource(ctx, ws.ID, owner.ID, source.ID))
	if fact(data(asset.ID), "application_model").State != "unknown" {
		t.Fatal("revoked source remained current")
	}
	historical, err := repo.Assessment(ctx, ws.ID, owner.ID, assessment.ID)
	must(err)
	found := false
	for _, a := range historical.Answers {
		if a.Key == "application_configuration" && strings.Contains(a.Value, "model-v1") {
			found = true
		}
	}
	if !found {
		t.Fatal("source change overwrote adopted assessment answers")
	}
}

func TestApplicationConfigurationSummaryIsBoundedAndNeverResolvesConflict(t *testing.T) {
	facts := []AssessmentFact{}
	for _, key := range applicationFactKeys {
		facts = append(facts, AssessmentFact{Key: key, State: "known", Value: strings.Repeat("配", 1000), Source: "application_api"})
	}
	summary := applicationConfigurationSummary(facts, "fixture")
	if summary.State != "known" || len([]rune(summary.Value)) > 2000 || !strings.Contains(summary.Value, "节选") {
		t.Fatal("summary cannot be safely saved as an assessment answer")
	}
	facts[3].State = "unknown"
	facts[3].Source = "application_api_conflict"
	if applicationConfigurationSummary(facts, "fixture").State != "unknown" {
		t.Fatal("summary resolved an unknown conflict")
	}
}

func TestApplicationMetadataOmitsCredentialMaterial(t *testing.T) {
	for _, value := range []string{"configured retention 30 days", "Basic search", "asia-pacific-region", "https://approved.invalid/configuration"} {
		if !validApplicationFact(value) {
			t.Errorf("ordinary configuration rejected: %q", value)
		}
	}
	for _, value := range []string{"Bearer fixture-token-value", "api_key=synthetic-secret", "Basic dXNlcjpwYXNz", "AKIA1234567890123456", "https://user:pass@fixture.invalid", "https://fixture.invalid/?token=synthetic"} {
		if validApplicationFact(value) {
			t.Error("credential-shaped metadata accepted")
		}
	}
}
