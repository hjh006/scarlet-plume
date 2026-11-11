package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationstandards"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestClassificationStandardsPersistence(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
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

	slug := fmt.Sprintf("standard-%d", time.Now().UnixNano())
	otherSlug := slug + "-other"
	usernames := []string{slug + "-owner", slug + "-other-owner", slug + "-operator", slug + "-reviewer"}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanup, "DELETE FROM plume.workspaces WHERE slug = ANY($1)", []string{slug, otherSlug}); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(cleanup, "DELETE FROM plume.users WHERE username = ANY($1)", usernames); err != nil {
			t.Error(err)
		}
	})
	want(repo.Bootstrap(ctx, usernames[0], "unused", slug, "Classification standards"), nil)
	want(repo.Bootstrap(ctx, usernames[1], "unused", otherSlug, "Other workspace"), nil)
	owner, err := repo.FindUser(ctx, usernames[0])
	want(err, nil)
	otherOwner, err := repo.FindUser(ctx, usernames[1])
	want(err, nil)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	want(err, nil)
	otherWorkspace, err := repo.Membership(ctx, otherOwner.ID, otherSlug)
	want(err, nil)
	addMember := func(username, role string) int64 {
		t.Helper()
		var id int64
		want(pool.QueryRow(ctx, "INSERT INTO plume.users (username, password_hash) VALUES ($1, 'unused') RETURNING id", username).Scan(&id), nil)
		_, err := pool.Exec(ctx, "INSERT INTO plume.memberships (workspace_id, user_id, role) VALUES ($1, $2, $3)", workspace.ID, id, role)
		want(err, nil)
		return id
	}
	operator := addMember(usernames[2], "operator")
	reviewer := addMember(usernames[3], "reviewer")
	assertDefault := func(workspaceID int64, expected string) {
		t.Helper()
		id, err := repo.ClassificationStandardDefault(ctx, workspaceID)
		want(err, nil)
		if id != expected {
			t.Fatalf("workspace %d default = %q, want %q", workspaceID, id, expected)
		}
	}
	assertDefault(workspace.ID, classificationstandards.DefaultID)
	assertDefault(otherWorkspace.ID, classificationstandards.DefaultID)
	want(repo.SetClassificationStandardDefault(ctx, workspace.ID, operator, "energy-grading-2026"), store.ErrForbidden)
	want(repo.SetClassificationStandardDefault(ctx, workspace.ID, reviewer, "energy-grading-2026"), store.ErrForbidden)
	want(repo.SetClassificationStandardDefault(ctx, workspace.ID, otherOwner.ID, "energy-grading-2026"), store.ErrNotFound)
	want(repo.SetClassificationStandardDefault(ctx, workspace.ID, owner.ID, "unknown-standard-2026"), classificationstandards.ErrSelection)
	assertDefault(workspace.ID, classificationstandards.DefaultID)
	want(repo.SetClassificationStandardDefault(ctx, workspace.ID, owner.ID, "education-jyt-0661-2025"), nil)
	assertDefault(workspace.ID, "education-jyt-0661-2025")
	assertDefault(otherWorkspace.ID, classificationstandards.DefaultID)
	want(repo.SetClassificationStandardDefault(ctx, otherWorkspace.ID, owner.ID, "energy-grading-2026"), store.ErrNotFound)
	assertDefault(otherWorkspace.ID, classificationstandards.DefaultID)
	_, err = pool.Exec(ctx, "UPDATE plume.workspaces SET classification_standard_id = 'removed-standard-2020' WHERE id = $1", otherWorkspace.ID)
	want(err, nil)
	assertDefault(otherWorkspace.ID, classificationstandards.DefaultID)

	asset, err := repo.CreateAsset(ctx, workspace.ID, "Reviewed dataset", "fixture", "Data")
	want(err, nil)
	foreignAsset, err := repo.CreateAsset(ctx, otherWorkspace.ID, "Other dataset", "fixture", "Data")
	want(err, nil)
	input := store.AssetClassificationInput{
		Category: "personal", BusinessLevel: "confidential", StatutoryCategory: "important",
		Basis:      "Approved education coverage and impact assessment",
		StandardID: "education-jyt-0661-2025", LevelCode: "l4", RuleID: "education-l4-scale",
	}
	wantReference, err := classificationstandards.Select(input.StandardID, input.LevelCode, input.RuleID, input.StatutoryCategory)
	want(err, nil)
	if wantReference.LevelLabel != "L4 重要数据" || wantReference.RuleClause != "6.2、表2 L4" || wantReference.RuleCriterion == "" || wantReference.RuleEvidence == "" {
		t.Fatalf("fixture lost the expected education rule and grade: %+v", wantReference)
	}
	_, err = repo.UpdateAssetClassification(ctx, workspace.ID, reviewer, asset.ID, input)
	want(err, store.ErrForbidden)
	_, err = repo.UpdateAssetClassification(ctx, workspace.ID, otherOwner.ID, asset.ID, input)
	want(err, store.ErrNotFound)
	_, err = repo.UpdateAssetClassification(ctx, workspace.ID, owner.ID, foreignAsset.ID, input)
	want(err, store.ErrNotFound)
	_, err = repo.GetAssetClassification(ctx, otherWorkspace.ID, otherOwner.ID, asset.ID)
	want(err, store.ErrNotFound)

	invalid := input
	invalid.StatutoryCategory = "core"
	_, err = repo.UpdateAssetClassification(ctx, workspace.ID, owner.ID, asset.ID, invalid)
	want(err, classificationstandards.ErrSelection)
	invalid = input
	invalid.LevelCode = "l5"
	invalid.StatutoryCategory = "core"
	_, err = repo.UpdateAssetClassification(ctx, workspace.ID, owner.ID, asset.ID, invalid)
	want(err, classificationstandards.ErrSelection)
	invalid = input
	invalid.RuleID = "not-a-rule"
	_, err = repo.UpdateAssetClassification(ctx, workspace.ID, owner.ID, asset.ID, invalid)
	want(err, classificationstandards.ErrSelection)
	initial, err := repo.GetAssetClassification(ctx, workspace.ID, reviewer, asset.ID)
	want(err, nil)
	if initial.Revision != 0 || initial.Standard != nil || len(initial.History) != 0 {
		t.Fatalf("rejected selections changed the asset: %+v", initial)
	}

	first, err := repo.UpdateAssetClassification(ctx, workspace.ID, operator, asset.ID, input)
	want(err, nil)
	if first.Revision != 1 || !reflect.DeepEqual(first.Standard, wantReference) || len(first.History) != 1 ||
		!reflect.DeepEqual(first.History[0].Standard, wantReference) || first.History[0].Actor != usernames[2] {
		t.Fatalf("first determination did not preserve the complete selected standard: %+v", first)
	}
	frozenHistory := first.History[0]
	want(repo.SetClassificationStandardDefault(ctx, workspace.ID, owner.ID, "energy-grading-2026"), nil)
	input.Revision = first.Revision
	input.Category, input.StatutoryCategory = "business", "core"
	input.Basis = "Approved energy scope and consumption coverage"
	input.StandardID, input.LevelCode, input.RuleID = "energy-grading-2026", "core", "energy-power-core"
	nextReference, err := classificationstandards.Select(input.StandardID, input.LevelCode, input.RuleID, input.StatutoryCategory)
	want(err, nil)
	second, err := repo.UpdateAssetClassification(ctx, workspace.ID, owner.ID, asset.ID, input)
	want(err, nil)
	if second.Revision != 2 || !reflect.DeepEqual(second.Standard, nextReference) || len(second.History) != 2 ||
		!reflect.DeepEqual(second.History[0].Standard, nextReference) || second.History[0].PreviousStatutoryCategory != "important" ||
		!reflect.DeepEqual(second.History[1], frozenHistory) {
		t.Fatalf("replacement determination changed the older rule or grade: %+v", second)
	}
	_, err = repo.UpdateAssetClassification(ctx, workspace.ID, owner.ID, asset.ID, input)
	want(err, store.ErrConflict)

	// Older clients omit standard fields. Preserve the selected version and rule.
	legacyInput := store.AssetClassificationInput{
		Revision: second.Revision, Category: "business", BusinessLevel: "confidential",
		StatutoryCategory: "core", Basis: "Updated review basis from an older client",
	}
	third, err := repo.UpdateAssetClassification(ctx, workspace.ID, owner.ID, asset.ID, legacyInput)
	want(err, nil)
	if third.Revision != 3 || !reflect.DeepEqual(third.Standard, nextReference) || len(third.History) != 3 ||
		!reflect.DeepEqual(third.History[0].Standard, nextReference) ||
		!reflect.DeepEqual(third.History[1], second.History[0]) || !reflect.DeepEqual(third.History[2], frozenHistory) {
		t.Fatalf("legacy update lost a standard snapshot: %+v", third)
	}
	legacyInput.Revision = third.Revision
	legacyInput.StatutoryCategory = "general"
	_, err = repo.UpdateAssetClassification(ctx, workspace.ID, owner.ID, asset.ID, legacyInput)
	want(err, classificationstandards.ErrSelection)
	stored, err := repo.GetAssetClassification(ctx, workspace.ID, reviewer, asset.ID)
	want(err, nil)
	if !reflect.DeepEqual(stored, third) {
		t.Fatalf("rejected legacy level change mutated persisted history: %+v", stored)
	}
	page, err := repo.ListAssetClassifications(ctx, workspace.ID, reviewer, store.AssetClassificationFilter{})
	want(err, nil)
	if len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0].Standard, nextReference) {
		t.Fatalf("classification list lost the selected rule or leaked another workspace: %+v", page)
	}

	// Assets without a previous standard remain writable by older clients.
	legacyAsset, err := repo.CreateAsset(ctx, workspace.ID, "Legacy dataset", "fixture", "Data")
	want(err, nil)
	legacyInput.Revision = 0
	legacyInput.Basis = "Legacy classification without a selected standard"
	legacyDetail, err := repo.UpdateAssetClassification(ctx, workspace.ID, owner.ID, legacyAsset.ID, legacyInput)
	want(err, nil)
	if legacyDetail.Revision != 1 || legacyDetail.Standard != nil || len(legacyDetail.History) != 1 || legacyDetail.History[0].Standard != nil {
		t.Fatalf("legacy determination was assigned an unselected default standard: %+v", legacyDetail)
	}
}
