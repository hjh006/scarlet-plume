package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/apivalidator"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestAPIValidatorRealTwoAccountsObjectsAndPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	db, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var exists bool
	if err := db.QueryRow(ctx, `SELECT to_regnamespace('inventory') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Skip("requires disposable database without inventory")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `CREATE SCHEMA inventory;CREATE TABLE inventory.customers(id bigint PRIMARY KEY);INSERT INTO inventory.customers VALUES(1),(2)`); err != nil {
		t.Fatal(err)
	}
	mode := filepath.Join(t.TempDir(), "mode")
	if err := os.WriteFile(mode, []byte("vulnerable"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VALIDATION_TEST_A_USER", "validation-a")
	t.Setenv("VALIDATION_TEST_A_PASSWORD", "weak1234")
	t.Setenv("VALIDATION_TEST_B_USER", "validation-b")
	t.Setenv("VALIDATION_TEST_B_PASSWORD", "provided-strong-fixture-password")
	app := &sample{tx, "validation-a", "weak1234", "validation-b", "provided-strong-fixture-password", mode}
	server := httptest.NewServer(app)
	defer server.Close()
	client, err := apivalidator.PrivateClient("")
	if err != nil {
		t.Fatal(err)
	}
	binding := apivalidator.Binding{Ref: "fixture-v1", TargetOrigin: server.URL, Route: "/api/vulnerable/objects/{id}", Rules: []string{"bola", "weak_credential"}, ObjectField: "objectId", OwnerField: "owner", AccountA: apivalidator.Account{UsernameEnv: "VALIDATION_TEST_A_USER", PasswordEnv: "VALIDATION_TEST_A_PASSWORD", ObjectPath: "/api/vulnerable/objects/fixture-a", ExpectedObject: "fixture-a", ExpectedOwner: "validation-a"}, AccountB: apivalidator.Account{UsernameEnv: "VALIDATION_TEST_B_USER", PasswordEnv: "VALIDATION_TEST_B_PASSWORD", ObjectPath: "/api/vulnerable/objects/fixture-b", ExpectedObject: "fixture-b", ExpectedOwner: "validation-b"}}
	manifest := apivalidator.Manifest{SourceID: 7, Bindings: []apivalidator.Binding{binding}}
	claim := store.APIValidationClaim{Mode: "validation", RunRef: strings.Repeat("1", 64), Plan: store.APIValidationPlan{ID: 2, APIValidationPlanInput: store.APIValidationPlanInput{Revision: 1, SourceID: 7, Method: "GET", Route: binding.Route, TargetOrigin: server.URL, ManifestRef: binding.Ref, Rules: binding.Rules}}}
	found := apivalidator.Execute(ctx, client, manifest, claim)
	if len(found.Results) != 2 || found.Results[0].Outcome != "vulnerable" || !found.Results[0].SameObject || !found.Results[0].OtherAccountConfirmed || found.Results[0].Requests != 3 || found.Results[1].Outcome != "vulnerable" || !found.Results[1].WeakRuleMatched {
		t.Fatalf("real source authorization/credential probe failed: %+v", found.Results)
	}
	encoded, _ := json.Marshal(found)
	for _, raw := range []string{"weak1234", "provided-strong-fixture-password", "validation-a", "validation-b", "fixture-a", "fixture-b", server.URL, "SELECT"} {
		if strings.Contains(string(encoded), raw) {
			t.Fatal("raw local credentials, IDs, response or target escaped result")
		}
	}
	if err := os.WriteFile(mode, []byte("fixed"), 0600); err != nil {
		t.Fatal(err)
	}
	fixed := apivalidator.Execute(ctx, client, manifest, claim)
	if fixed.Results[0].Outcome != "not_observed" || fixed.Results[0].OtherStatus != 403 || !fixed.Results[0].ComparisonComplete {
		t.Fatal("actual fixed authorization did not produce a strong negative")
	}
	app.aPassword = "provided-rotated-strong-fixture-password"
	t.Setenv("VALIDATION_TEST_A_PASSWORD", app.aPassword)
	rotated := apivalidator.Execute(ctx, client, manifest, claim)
	if rotated.Results[1].Outcome != "not_observed" || !rotated.Results[1].CredentialAccepted || rotated.Results[1].WeakRuleMatched {
		t.Fatal("explicit rotated provided credential did not validate")
	}
	claim.Mode = "connection"
	connection := apivalidator.Execute(ctx, client, manifest, claim)
	for _, r := range connection.Results {
		if r.Outcome != "inconclusive" {
			t.Fatal("connection-only probe emitted security conclusion")
		}
	}
	claim.Mode = "validation"
	manifest.SourceID = 8
	mismatch := apivalidator.Execute(ctx, client, manifest, claim)
	for _, r := range mismatch.Results {
		if r.Requests != 0 || r.Outcome != "inconclusive" {
			t.Fatal("source manifest mismatch still executed")
		}
	}
}
