package legacy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/httpapi"
	"github.com/hjh006/scarlet-plume/backend/internal/legacy"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestDjangoHistory(t *testing.T) {
	dsn := os.Getenv("PLUME_LEGACY_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_LEGACY_TEST_DSN for the disposable Django fixture")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("could not configure the historical fixture database")
	}
	defer pool.Close()

	const slug, owner, reviewer = "Django_History", "legacy-import-owner", "legacy-import-reviewer"
	const inactive, unusable = "legacy-import-inactive", "legacy-import-unusable"
	var fixtureCount, workspaceCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE slug = $1 AND name = 'Django import fixture'), count(*)
		FROM public.governance_workspace
	`, slug).Scan(&fixtureCount, &workspaceCount); err != nil || fixtureCount != 1 || workspaceCount != 1 {
		t.Fatal("PLUME_LEGACY_TEST_DSN must point to a disposable database seeded with the Django import fixture")
	}

	repo := store.New(pool)
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	preflight, err := legacy.Import(ctx, pool, false)
	if err != nil {
		t.Fatal(err)
	}
	if preflight.Mode != "check" || preflight.Counts.Workspaces != 1 || preflight.Counts.Users != 2 ||
		preflight.Counts.Memberships != 2 || preflight.Counts.Assets != 4 || preflight.Counts.Activities != 2 ||
		preflight.Counts.Checks != 4 || preflight.Counts.Evidence != 3 || preflight.Counts.Events != 12 ||
		preflight.Counts.Requirements != 14 || preflight.Counts.NewRequirements != 0 {
		t.Fatalf("unexpected preflight: %+v", preflight)
	}
	var copied bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM plume.workspaces WHERE django_id IS NOT NULL)
		OR EXISTS(SELECT FROM plume.django_import)`).Scan(&copied); err != nil || copied {
		t.Fatal("read-only preflight changed the Go schema")
	}

	if _, err := pool.Exec(ctx, `INSERT INTO plume.workspaces (slug, name) VALUES ($1, 'Conflicting Go workspace')`, slug); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Import(ctx, pool, true); err == nil || !strings.Contains(err.Error(), "1 workspace slugs") {
		t.Fatalf("workspace conflict was not rejected: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM plume.workspaces WHERE slug = $1 AND name = 'Conflicting Go workspace'`, slug); err != nil {
		t.Fatal(err)
	}

	// Exercise rollback after users, assets and checks have already been inserted.
	var evidenceID int64
	var summary string
	if err := pool.QueryRow(ctx, "SELECT id, summary FROM public.governance_evidence ORDER BY id LIMIT 1").
		Scan(&evidenceID, &summary); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE public.governance_evidence SET summary = '' WHERE id = $1", evidenceID); err != nil {
		t.Fatal(err)
	}
	_, failedApply := legacy.Import(ctx, pool, true)
	if _, err := pool.Exec(ctx, "UPDATE public.governance_evidence SET summary = $1 WHERE id = $2", summary, evidenceID); err != nil {
		t.Fatal(err)
	}
	if failedApply == nil || !strings.Contains(failedApply.Error(), "copy Django evidence") {
		t.Fatalf("late evidence constraint did not fail the import: %v", failedApply)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM plume.users WHERE django_id IS NOT NULL)
		OR EXISTS(SELECT FROM plume.workspaces WHERE django_id IS NOT NULL)
		OR EXISTS(SELECT FROM plume.checks WHERE django_id IS NOT NULL)
		OR EXISTS(SELECT FROM plume.django_import)`).Scan(&copied); err != nil || copied {
		t.Fatal("partial imported records remained after a late constraint failure")
	}

	if _, err := pool.Exec(ctx, `
		WITH users AS (
			INSERT INTO public.auth_user (
				username, password, is_active, is_superuser, is_staff, first_name, last_name, email, date_joined
			) VALUES ($1, 'test-placeholder', false, false, false, '', '', '', clock_timestamp()),
			         ($2, '!test-unusable-marker', true, false, false, '', '', '', clock_timestamp())
			RETURNING id
		)
		INSERT INTO public.governance_membership (workspace_id, user_id, role)
		SELECT w.id, u.id, 'viewer' FROM public.governance_workspace w CROSS JOIN users u WHERE w.slug = $3
	`, inactive, unusable, slug); err != nil {
		t.Fatal(err)
	}
	preflight.Counts.Users += 2
	preflight.Counts.Memberships += 2

	// A backfilled event has the highest source ID but predates the submitted
	// event. The destination uses insertion ID for latest review revision.
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.governance_checkevent (control_check_id, action, actor_id, note, created_at)
		SELECT c.id, 'submitted', c.created_by_id, 'Legacy backfill', c.created_at - interval '1 second'
		FROM public.governance_check c WHERE c.status = 'in_review'
	`); err != nil {
		t.Fatal(err)
	}
	preflight.Counts.Events++
	applied, err := legacy.Import(ctx, pool, true)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Mode != "applied" || applied.CompletedAt == nil || applied.Counts != preflight.Counts {
		t.Fatalf("unexpected apply report: %+v", applied)
	}
	repeated, err := legacy.Import(ctx, pool, true)
	if err != nil || repeated.Mode != "already-applied" || repeated.Counts != applied.Counts ||
		repeated.CompletedAt == nil || !repeated.CompletedAt.Equal(*applied.CompletedAt) {
		t.Fatalf("unexpected repeated import: %+v %v", repeated, err)
	}

	for _, item := range []struct {
		kind, query string
	}{
		{"assets", `SELECT count(*) FROM public.governance_asset d
			LEFT JOIN plume.assets p ON p.django_id = d.id
			WHERE (p.name,p.system,p.category,p.business_level,p.statutory_category,p.classification_basis,p.owner_team,p.description,p.created_at)
			IS DISTINCT FROM (d.name,d.system,d.category,d.business_level,d.statutory_category,d.classification_basis,d.owner_team,d.description,d.created_at)`},
		{"activities", `SELECT count(*) FROM public.governance_activity d
			LEFT JOIN plume.activities p ON p.django_id = d.id LEFT JOIN plume.assets a ON a.id = p.asset_id
			WHERE (a.django_id,p.name,p.kind,p.purpose,p.recipient,p.region,p.retention_policy,p.created_at)
			IS DISTINCT FROM (d.asset_id,d.name,d.kind,d.purpose,d.recipient,d.region,d.retention_policy,d.created_at)`},
		{"checks", `SELECT count(*) FROM public.governance_check d
			JOIN public.governance_requirement r ON r.id = d.requirement_id
			LEFT JOIN plume.checks p ON p.django_id = d.id LEFT JOIN plume.assets a ON a.id = p.asset_id
			LEFT JOIN plume.users c ON c.id = p.created_by LEFT JOIN plume.users s ON s.id = p.submitted_by
			WHERE (a.django_id,p.requirement_code,p.owner_team,p.note,p.status,c.django_id,s.django_id,p.created_at,p.updated_at)
			IS DISTINCT FROM (d.asset_id,r.code,d.owner_team,d.note,d.status,d.created_by_id,d.submitted_by_id,d.created_at,d.updated_at)`},
		{"evidence", `SELECT count(*) FROM public.governance_evidence d
			LEFT JOIN plume.check_evidence p ON p.django_id = d.id LEFT JOIN plume.checks c ON c.id = p.check_id
			LEFT JOIN plume.users u ON u.id = p.created_by
			WHERE (c.django_id,p.kind,p.source_system,p.source_record,p.summary,p.observed_at,u.django_id,p.created_at)
			IS DISTINCT FROM (d.control_check_id,d.kind,d.source_system,d.source_record,d.summary,d.observed_at,d.created_by_id,d.created_at)`},
		{"events", `SELECT count(*) FROM public.governance_checkevent d
			LEFT JOIN plume.check_events p ON p.django_id = d.id LEFT JOIN plume.checks c ON c.id = p.check_id
			LEFT JOIN plume.users u ON u.id = p.actor_id
			WHERE (c.django_id,p.action,u.django_id,p.note,p.created_at)
			IS DISTINCT FROM (d.control_check_id,d.action,d.actor_id,d.note,d.created_at)`},
	} {
		var mismatches int
		if err := pool.QueryRow(ctx, item.query).Scan(&mismatches); err != nil || mismatches != 0 {
			t.Fatalf("imported %s differ: %d mismatches, %v", item.kind, mismatches, err)
		}
	}

	reviewerUser, err := repo.FindUser(ctx, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := repo.Membership(ctx, reviewerUser.ID, slug)
	if err != nil || workspace.Role != "reviewer" {
		t.Fatalf("reviewer membership changed: %+v %v", workspace, err)
	}
	checks, err := repo.ListChecks(ctx, workspace.ID, "in_review", 0, 50)
	if err != nil || len(checks) != 1 {
		t.Fatalf("imported review queue incorrect: %+v %v", checks, err)
	}
	detail, err := repo.CheckDetail(ctx, workspace.ID, reviewerUser.ID, checks[0].ID)
	if err != nil || !detail.CanReview || detail.ReviewRevision == 0 {
		t.Fatalf("imported check cannot be independently reviewed: %v", err)
	}
	for _, event := range detail.Events {
		if event.ID == detail.ReviewRevision && event.Note == "Legacy backfill" {
			t.Fatal("backfilled event displaced the original review revision")
		}
	}

	const password, origin = "Django-fixture-new-password!", "https://scarlet.example"
	handler := httpapi.New(repo, origin, true)
	signIn := func(username string, want int) {
		t.Helper()
		body, err := json.Marshal(map[string]string{"username": username, "password": password})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, origin+"/api/v1/session", strings.NewReader(string(body)))
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("unexpected response during imported login: got %d, want %d", response.Code, want)
		}
	}
	signIn(owner, http.StatusUnauthorized)
	hash, err := httpapi.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.ActivateUser(ctx, pool, owner, hash); err != nil {
		t.Fatal(err)
	}
	if err := legacy.ActivateUser(ctx, pool, owner, hash); err == nil {
		t.Fatal("import activation could reset an already active user")
	}
	signIn(owner, http.StatusNoContent)
	signIn(reviewer, http.StatusUnauthorized)
	for _, username := range []string{inactive, unusable} {
		if err := legacy.ActivateUser(ctx, pool, username, hash); err == nil {
			t.Fatal("an ineligible imported user was activated")
		}
		signIn(username, http.StatusUnauthorized)
	}
}
