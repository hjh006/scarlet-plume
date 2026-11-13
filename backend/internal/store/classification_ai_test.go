package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestClassificationAIProfilesMetadataAndSuggestions(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	repo := store.New(pool)
	want := func(got, expected error) {
		t.Helper()
		if !errors.Is(got, expected) {
			t.Fatalf("got %v want %v", got, expected)
		}
	}
	want(repo.Migrate(ctx), nil)
	slug := fmt.Sprintf("classification-ai-%d", time.Now().UnixNano())
	names := []string{slug, slug + "-operator", slug + "-reviewer", slug + "-other"}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, e := pool.Exec(cleanup, `DELETE FROM plume.workspaces WHERE slug=ANY($1)`, []string{slug, slug + "-other"})
		if e != nil {
			t.Error(e)
		}
		_, e = pool.Exec(cleanup, `DELETE FROM plume.users WHERE username=ANY($1)`, names)
		if e != nil {
			t.Error(e)
		}
	})
	want(repo.Bootstrap(ctx, names[0], "unused", slug, "AI settings"), nil)
	owner, e := repo.FindUser(ctx, names[0])
	want(e, nil)
	ws, e := repo.Membership(ctx, owner.ID, slug)
	want(e, nil)
	member := func(name, role string) int64 {
		t.Helper()
		var id int64
		want(pool.QueryRow(ctx, `INSERT INTO plume.users(username,password_hash) VALUES($1,'unused') RETURNING id`, name).Scan(&id), nil)
		_, e := pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role) VALUES($1,$2,$3)`, ws.ID, id, role)
		want(e, nil)
		return id
	}
	operator, reviewer := member(names[1], "operator"), member(names[2], "reviewer")
	want(repo.Bootstrap(ctx, names[3], "unused", slug+"-other", "Other"), nil)
	other, e := repo.FindUser(ctx, names[3])
	want(e, nil)
	otherWS, e := repo.Membership(ctx, other.ID, slug+"-other")
	want(e, nil)
	settings, e := repo.GetClassificationEngineSettings(ctx, ws.ID, reviewer)
	want(e, nil)
	if settings.Mode != "local" || settings.DefaultProfileID != nil || settings.Revision != 0 {
		t.Fatalf("local engine not default %+v", settings)
	}
	_, e = repo.GetClassificationEngineSettings(ctx, ws.ID, other.ID)
	want(e, store.ErrNotFound)
	profileInput := store.ClassificationAIProfileInput{Name: "Customer choice", BaseURL: "https://models.example.test/v1", Protocol: "responses", Model: "chosen-model", Enabled: true, AllowMetadata: true, ReplaceAPIKey: true, EncryptedAPIKey: []byte("sealed-test-key")}
	_, e = repo.SaveClassificationAIProfile(ctx, ws.ID, operator, 0, profileInput)
	want(e, store.ErrForbidden)
	bad := profileInput
	bad.BaseURL = "https://user:password@models.example.test/v1"
	_, e = repo.SaveClassificationAIProfile(ctx, ws.ID, owner.ID, 0, bad)
	want(e, store.ErrInvalid)
	profile, e := repo.SaveClassificationAIProfile(ctx, ws.ID, owner.ID, 0, profileInput)
	want(e, nil)
	if !profile.HasAPIKey || len(profile.EncryptedAPIKey) != 0 || profile.Revision != 1 {
		t.Fatalf("profile response leaked ciphertext %+v", profile)
	}
	list, e := repo.ListClassificationAIProfiles(ctx, ws.ID, reviewer)
	want(e, nil)
	encoded, _ := json.Marshal(list)
	if len(list) != 1 || len(list[0].EncryptedAPIKey) != 0 || bytes.Contains(encoded, []byte("sealed-test-key")) || bytes.Contains(encoded, []byte("encrypted")) {
		t.Fatal("profile list exposed secret material")
	}
	_, e = repo.GetClassificationAIProfileSecret(ctx, ws.ID, reviewer, profile.ID)
	want(e, store.ErrForbidden)
	_, e = repo.GetClassificationAIProfileSecret(ctx, otherWS.ID, other.ID, profile.ID)
	want(e, store.ErrNotFound)
	secret, e := repo.GetClassificationAIProfileSecret(ctx, ws.ID, operator, profile.ID)
	want(e, nil)
	if string(secret.EncryptedAPIKey) != "sealed-test-key" {
		t.Fatal("internal secret not preserved")
	}
	secretJSON, _ := json.Marshal(secret)
	if bytes.Contains(secretJSON, []byte("sealed-test-key")) {
		t.Fatal("secret field lacks JSON redaction")
	}
	change := profileInput
	change.Revision = 1
	change.ReplaceAPIKey = false
	change.EncryptedAPIKey = nil
	change.BaseURL = "https://another.example.test/v1"
	_, e = repo.SaveClassificationAIProfile(ctx, ws.ID, owner.ID, profile.ID, change)
	want(e, store.ErrInvalid)
	change.BaseURL = profile.BaseURL
	change.Model = "new-model"
	profile, e = repo.SaveClassificationAIProfile(ctx, ws.ID, owner.ID, profile.ID, change)
	want(e, nil)
	if !profile.HasAPIKey || profile.Revision != 2 {
		t.Fatal("omitted secret did not preserve existing key")
	}
	_, e = repo.SaveClassificationAIProfile(ctx, ws.ID, owner.ID, profile.ID, change)
	want(e, store.ErrConflict)
	disabledInput := profileInput
	disabledInput.Name = "No consent"
	disabledInput.AllowMetadata = false
	disabled, e := repo.SaveClassificationAIProfile(ctx, ws.ID, owner.ID, 0, disabledInput)
	want(e, nil)
	hybrid := store.ClassificationEngineSettingsInput{Mode: "hybrid", DefaultProfileID: &disabled.ID}
	_, e = repo.SaveClassificationEngineSettings(ctx, ws.ID, owner.ID, hybrid)
	want(e, store.ErrInvalid)
	hybrid.DefaultProfileID = &profile.ID
	_, e = repo.SaveClassificationEngineSettings(ctx, ws.ID, operator, hybrid)
	want(e, store.ErrForbidden)
	_, e = repo.SaveClassificationEngineSettings(ctx, otherWS.ID, other.ID, hybrid)
	want(e, store.ErrNotFound)
	settings, e = repo.SaveClassificationEngineSettings(ctx, ws.ID, owner.ID, hybrid)
	want(e, nil)
	if settings.Revision != 1 {
		t.Fatal("initial engine revision must be one")
	}
	_, e = repo.SaveClassificationEngineSettings(ctx, ws.ID, owner.ID, hybrid)
	want(e, store.ErrConflict)
	asset, e := repo.CreateAsset(ctx, ws.ID, strings.Repeat("A", 350), "CRM", "Data")
	want(e, nil)
	_, e = pool.Exec(ctx, `INSERT INTO plume.catalog_entries(workspace_id,asset_id,kind,source,source_ref,metadata) VALUES($1,$2,'table','fixture','customers','{"comment":"SECRET-RAW-RECORD","credential":"DO-NOT-SEND"}')`, ws.ID, asset.ID)
	want(e, nil)
	_, e = pool.Exec(ctx, `WITH created AS(INSERT INTO plume.assets(workspace_id,name,system) SELECT $1,'field-'||n,'CRM' FROM generate_series(1,55) n RETURNING id,name) INSERT INTO plume.catalog_entries(workspace_id,asset_id,kind,parent_ref,source,source_ref,field_name,metadata) SELECT $1,id,'field','customers','fixture',name,name,jsonb_build_object('type',repeat('t',150),'comment','SECRET-RAW-RECORD','sample','DO-NOT-SEND') FROM created`, ws.ID)
	want(e, nil)
	_, e = repo.ClassificationAIInput(ctx, ws.ID, reviewer, asset.ID, 0)
	want(e, store.ErrForbidden)
	_, e = repo.ClassificationAIInput(ctx, otherWS.ID, other.ID, asset.ID, 0)
	want(e, store.ErrNotFound)
	_, e = repo.ClassificationAIInput(ctx, ws.ID, operator, asset.ID, 8)
	want(e, store.ErrConflict)
	metadata, e := repo.ClassificationAIInput(ctx, ws.ID, operator, asset.ID, 0)
	want(e, nil)
	safe, _ := json.Marshal(metadata)
	if len(metadata.Fields) != 50 || len(metadata.Name) != 300 || len(metadata.Fields[0].Type) != 120 || bytes.Contains(safe, []byte("SECRET")) || bytes.Contains(safe, []byte("DO-NOT-SEND")) || bytes.Contains(safe, []byte("comment")) {
		t.Fatalf("metadata bounds or allowlist broken %s", safe)
	}
	_, e = pool.Exec(ctx, `UPDATE plume.catalog_entries SET field_name=repeat('界',120),metadata=jsonb_build_object('type',repeat('字',120)) WHERE workspace_id=$1 AND kind='field'`, ws.ID)
	want(e, nil)
	unicodeMetadata, e := repo.ClassificationAIInput(ctx, ws.ID, operator, asset.ID, 0)
	want(e, nil)
	bounded, _ := json.Marshal(unicodeMetadata)
	if len(bounded) > 24<<10 || len(unicodeMetadata.Fields) < 1 || len(unicodeMetadata.Fields) >= 50 {
		t.Fatal("UTF-8 metadata exceeded the byte budget")
	}
	suggestion := store.ClassificationAISuggestionInput{AssetID: asset.ID, AssetRevision: 0, ProfileID: profile.ID, ProfileRevision: profile.Revision, Model: profile.Model, Category: "personal", BusinessLevel: "confidential", Rationale: "Metadata-only optional suggestion"}
	_, e = repo.SaveClassificationAISuggestion(ctx, ws.ID, reviewer, suggestion)
	want(e, store.ErrForbidden)
	_, e = repo.SaveClassificationAISuggestion(ctx, otherWS.ID, other.ID, suggestion)
	want(e, store.ErrConflict)
	// The workspace default is a starting selection; another explicitly
	// consented, enabled profile can be chosen for an individual request.
	alternateProfileInput := disabledInput
	alternateProfileInput.AllowMetadata = true
	alternateProfileInput.Revision = disabled.Revision
	alternateProfile, e := repo.SaveClassificationAIProfile(ctx, ws.ID, owner.ID, disabled.ID, alternateProfileInput)
	want(e, nil)
	alternateSuggestion := suggestion
	alternateSuggestion.ProfileID = alternateProfile.ID
	alternateSuggestion.ProfileRevision = alternateProfile.Revision
	alternateSuggestion.Model = alternateProfile.Model
	_, e = repo.SaveClassificationAISuggestion(ctx, ws.ID, operator, alternateSuggestion)
	want(e, nil)
	saved, e := repo.SaveClassificationAISuggestion(ctx, ws.ID, operator, suggestion)
	want(e, nil)
	if saved.Actor != names[1] || saved.ID < 1 {
		t.Fatalf("missing suggestion provenance %+v", saved)
	}
	actual, e := repo.GetAssetClassification(ctx, ws.ID, reviewer, asset.ID)
	want(e, nil)
	if actual.Revision != 0 || actual.Asset.Category != "unknown" || actual.Asset.StatutoryCategory != "unknown" || len(actual.History) != 0 {
		t.Fatal("AI suggestion automatically changed classification")
	}
	suggestions, e := repo.ListClassificationAISuggestions(ctx, ws.ID, reviewer, asset.ID)
	want(e, nil)
	if len(suggestions) != 2 || suggestions[0].ID != saved.ID {
		t.Fatal("suggestion not persisted")
	}
	_, e = repo.ListClassificationAISuggestions(ctx, otherWS.ID, other.ID, asset.ID)
	want(e, store.ErrNotFound)
	settings, e = repo.SaveClassificationEngineSettings(ctx, ws.ID, owner.ID, store.ClassificationEngineSettingsInput{Mode: "local", Revision: settings.Revision})
	want(e, nil)
	_, e = repo.SaveClassificationAISuggestion(ctx, ws.ID, operator, suggestion)
	want(e, store.ErrConflict)
	settings, e = repo.SaveClassificationEngineSettings(ctx, ws.ID, owner.ID, store.ClassificationEngineSettingsInput{Mode: "hybrid", DefaultProfileID: &profile.ID, Revision: settings.Revision})
	want(e, nil)
	change.Revision = profile.Revision
	change.Model = "changed-during-call"
	profile, e = repo.SaveClassificationAIProfile(ctx, ws.ID, owner.ID, profile.ID, change)
	want(e, nil)
	_, e = repo.SaveClassificationAISuggestion(ctx, ws.ID, operator, suggestion)
	want(e, store.ErrConflict)
	change.Revision = profile.Revision
	change.Enabled = false
	profile, e = repo.SaveClassificationAIProfile(ctx, ws.ID, owner.ID, profile.ID, change)
	want(e, nil)
	suggestion.ProfileRevision = profile.Revision
	suggestion.Model = profile.Model
	_, e = repo.SaveClassificationAISuggestion(ctx, ws.ID, operator, suggestion)
	want(e, store.ErrConflict)
	change.Revision = profile.Revision
	change.Enabled = true
	change.AllowMetadata = false
	profile, e = repo.SaveClassificationAIProfile(ctx, ws.ID, owner.ID, profile.ID, change)
	want(e, nil)
	suggestion.ProfileRevision = profile.Revision
	_, e = repo.SaveClassificationAISuggestion(ctx, ws.ID, operator, suggestion)
	want(e, store.ErrConflict)
	change.Revision = profile.Revision
	change.AllowMetadata = true
	profile, e = repo.SaveClassificationAIProfile(ctx, ws.ID, owner.ID, profile.ID, change)
	want(e, nil)
	suggestion.ProfileRevision = profile.Revision
	suggestion.Model = profile.Model
	_, e = repo.UpdateAssetClassification(ctx, ws.ID, operator, asset.ID, store.AssetClassificationInput{Revision: 0, Category: "business", BusinessLevel: "internal", StatutoryCategory: "general", Basis: "Human determination"})
	want(e, nil)
	_, e = repo.SaveClassificationAISuggestion(ctx, ws.ID, operator, suggestion)
	want(e, store.ErrConflict)
	suggestions, e = repo.ListClassificationAISuggestions(ctx, ws.ID, reviewer, asset.ID)
	want(e, nil)
	if len(suggestions) != 2 {
		t.Fatal("stale AI responses were persisted")
	}
	clear := change
	clear.Revision = profile.Revision
	clear.ReplaceAPIKey = true
	clear.EncryptedAPIKey = nil
	clear.BaseURL = "https://new-destination.example.test/v1"
	profile, e = repo.SaveClassificationAIProfile(ctx, ws.ID, owner.ID, profile.ID, clear)
	want(e, nil)
	if profile.HasAPIKey {
		t.Fatal("explicit clear retained the old destination key")
	}
}
