package store_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgreSQLTemplates(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("could not configure template test database")
	}
	t.Cleanup(pool.Close)
	repo := store.New(pool)
	want := func(err, target error) {
		t.Helper()
		if !errors.Is(err, target) {
			t.Fatalf("got %v, want %v", err, target)
		}
	}
	want(repo.Migrate(ctx), nil)

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	slug, foreignSlug := "templates-"+suffix, "other-templates-"+suffix
	ownerName, foreignName, operatorName := "owner-"+suffix, "other-"+suffix, "operator-"+suffix
	builtInKey := "builtin-" + suffix
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM plume.workspaces WHERE slug = ANY($1)", []string{slug, foreignSlug}); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM plume.check_templates WHERE workspace_id IS NULL AND template_key = $1", builtInKey); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM plume.users WHERE username = ANY($1)", []string{ownerName, foreignName, operatorName}); err != nil {
			t.Error(err)
		}
	})
	want(repo.Bootstrap(ctx, ownerName, "unused", slug, "Templates"), nil)
	want(repo.Bootstrap(ctx, foreignName, "unused", foreignSlug, "Foreign templates"), nil)
	owner, err := repo.FindUser(ctx, ownerName)
	want(err, nil)
	foreignOwner, err := repo.FindUser(ctx, foreignName)
	want(err, nil)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	want(err, nil)
	foreignWorkspace, err := repo.Membership(ctx, foreignOwner.ID, foreignSlug)
	want(err, nil)
	var operatorID int64
	want(pool.QueryRow(ctx, "INSERT INTO plume.users (username,password_hash) VALUES ($1,'unused') RETURNING id",
		operatorName).Scan(&operatorID), nil)
	_, err = pool.Exec(ctx, "INSERT INTO plume.memberships (workspace_id,user_id,role) VALUES ($1,$2,'operator')",
		workspace.ID, operatorID)
	want(err, nil)

	var builtInID int64
	want(pool.QueryRow(ctx, `
		INSERT INTO plume.check_templates (template_key, title, kind, version)
		VALUES ($1, 'Built in', 'sharing', 1) RETURNING id`, builtInKey).Scan(&builtInID), nil)
	_, err = pool.Exec(ctx, `
		INSERT INTO plume.check_template_items (template_revision_id, item_key, position, title,
			requirement_code, applicability, evidence_requirements, acceptance_criteria)
		VALUES ($1, 'access', 1, 'Access', 'T01', 'External sharing', 'Permissions', 'Approved scope')`, builtInID)
	want(err, nil)

	asset, err := repo.CreateAsset(ctx, workspace.ID, "Share", "Drive", "Security")
	want(err, nil)
	otherAsset, err := repo.CreateAsset(ctx, workspace.ID, "Unrelated", "Drive", "Security")
	want(err, nil)
	foreignAsset, err := repo.CreateAsset(ctx, foreignWorkspace.ID, "Foreign", "Drive", "Security")
	want(err, nil)
	sharing, err := repo.CreateActivity(ctx, workspace.ID, owner.ID,
		store.ActivityInput{AssetID: asset.ID, Name: "Share", Purpose: "Approved sharing", Kind: "sharing"})
	want(err, nil)
	ai, err := repo.CreateActivity(ctx, workspace.ID, owner.ID,
		store.ActivityInput{AssetID: asset.ID, Name: "AI", Purpose: "Model evaluation", Kind: "ai"})
	want(err, nil)
	foreignActivity, err := repo.CreateActivity(ctx, foreignWorkspace.ID, foreignOwner.ID,
		store.ActivityInput{AssetID: foreignAsset.ID, Name: "Foreign", Purpose: "Other sharing", Kind: "sharing"})
	want(err, nil)

	item := store.TemplateItem{Key: "authorization", Title: "Authorization", RequirementCode: "T01",
		Applicability: "External parties", EvidenceRequirements: "Approved permissions", AcceptanceCriteria: "Only approved recipients"}
	input := store.TemplateInput{Key: "custom-share", Title: "Custom sharing", Kind: "sharing", Items: []store.TemplateItem{item}}
	_, err = repo.PublishTemplate(ctx, workspace.ID, operatorID, input)
	want(err, store.ErrForbidden)
	invalid := input
	invalid.Items = []store.TemplateItem{item, item}
	_, err = repo.PublishTemplate(ctx, workspace.ID, owner.ID, invalid)
	want(err, store.ErrInvalid)
	invalid = input
	invalid.Items = []store.TemplateItem{item}
	invalid.Items[0].RequirementCode = "UNKNOWN"
	_, err = repo.PublishTemplate(ctx, workspace.ID, owner.ID, invalid)
	want(err, store.ErrInvalid)
	reserved := input
	reserved.Key = builtInKey
	_, err = repo.PublishTemplate(ctx, workspace.ID, owner.ID, reserved)
	want(err, store.ErrConflict)

	first, err := repo.PublishTemplate(ctx, workspace.ID, owner.ID, input)
	want(err, nil)
	if first.Version != 1 || first.BuiltIn || len(first.Items) != 1 {
		t.Fatalf("unexpected first revision: %+v", first)
	}
	_, err = repo.Template(ctx, foreignWorkspace.ID, first.ID)
	want(err, store.ErrNotFound)
	public, err := repo.Template(ctx, foreignWorkspace.ID, builtInID)
	want(err, nil)
	if !public.BuiltIn || len(public.Items) != 1 {
		t.Fatalf("unexpected built-in: %+v", public)
	}

	checkInput := store.CheckInput{AssetID: asset.ID, ActivityID: &sharing.ID, RequirementCode: "T01",
		OwnerTeam: "Security", Note: "Applicable to approved recipient", TemplateRevisionID: &first.ID, TemplateItemKey: item.Key}
	invalidCheck := checkInput
	invalidCheck.ActivityID = nil
	_, err = repo.CreateCheck(ctx, workspace.ID, operatorID, invalidCheck)
	want(err, store.ErrInvalid)
	invalidCheck = checkInput
	invalidCheck.AssetID = otherAsset.ID
	_, err = repo.CreateCheck(ctx, workspace.ID, operatorID, invalidCheck)
	want(err, store.ErrNotFound)
	invalidCheck = checkInput
	invalidCheck.ActivityID = &ai.ID
	_, err = repo.CreateCheck(ctx, workspace.ID, operatorID, invalidCheck)
	want(err, store.ErrInvalid)
	invalidCheck = checkInput
	invalidCheck.RequirementCode = "X01"
	_, err = repo.CreateCheck(ctx, workspace.ID, operatorID, invalidCheck)
	want(err, store.ErrInvalid)
	invalidCheck = checkInput
	invalidCheck.ActivityID, invalidCheck.AssetID = &foreignActivity.ID, foreignAsset.ID
	_, err = repo.CreateCheck(ctx, foreignWorkspace.ID, foreignOwner.ID, invalidCheck)
	want(err, store.ErrNotFound)
	check, err := repo.CreateCheck(ctx, workspace.ID, operatorID, checkInput)
	want(err, nil)

	input.BaseRevisionID = &first.ID
	input.Items[0].AcceptanceCriteria = "New version with stricter evidence"
	type publication struct {
		template store.Template
		err      error
	}
	outcomes := make(chan publication, 2)
	for range 2 {
		go func() {
			revision, err := repo.PublishTemplate(ctx, workspace.ID, owner.ID, input)
			outcomes <- publication{revision, err}
		}()
	}
	published, conflict := <-outcomes, <-outcomes
	if published.err != nil {
		published, conflict = conflict, published
	}
	want(published.err, nil)
	want(conflict.err, store.ErrConflict)
	if published.template.Version != 2 {
		t.Fatalf("unexpected revision version: %d", published.template.Version)
	}
	latest, err := repo.ListTemplates(ctx, workspace.ID, "sharing")
	want(err, nil)
	for _, visible := range latest {
		if visible.Key == first.Key && visible.ID != published.template.ID {
			t.Fatalf("list returned superseded revision: %+v", visible)
		}
	}

	detail, err := repo.CheckDetail(ctx, workspace.ID, owner.ID, check.ID)
	want(err, nil)
	if detail.Template == nil || detail.Template.RevisionID != first.ID || detail.Template.Item != item {
		t.Fatalf("old check lost original criteria: %+v", detail.Template)
	}
	want(repo.AddEvidence(ctx, workspace.ID, operatorID, check.ID, store.EvidenceInput{
		Kind: "observation", SourceSystem: "Ticket", SourceRecord: "T1", Summary: "Approved recipients", ObservedAt: time.Now(),
	}), nil)
	want(repo.SubmitCheck(ctx, workspace.ID, operatorID, check.ID), nil)
}
