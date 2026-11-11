package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

func TestFlowSampleRealPostgreSQLAndOTLP(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	db, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect sample test database")
	}
	defer db.Close(context.Background())
	var exists bool
	if err := db.QueryRow(ctx, "SELECT to_regnamespace('inventory') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Skip("requires disposable database without inventory schema")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(ctx, `CREATE SCHEMA inventory; CREATE TABLE inventory.classification_contacts(id bigint PRIMARY KEY,contact_value text NOT NULL); INSERT INTO inventory.classification_contacts VALUES(1,'real-query-one@example.test'),(2,'real-query-two@example.test')`)
	if err != nil {
		t.Fatal(err)
	}
	payloads := make(chan []byte, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-otlp-token" || r.Header.Get("Content-Type") != "application/x-protobuf" {
			t.Error("incorrect controlled telemetry delivery")
		}
		received, _ := io.ReadAll(r.Body)
		payloads <- received
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()
	app := &sampleApp{database: tx, databaseName: db.Config().Database, password: "sample-fixture-password", salt: strings.Repeat("salt", 10), source: "Sample source", endpoint: receiver.URL, token: "fixture-otlp-token", client: receiver.Client()}
	unauthorized := httptest.NewRecorder()
	app.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/contacts", nil))
	if unauthorized.Code != 401 || len(payloads) != 0 {
		t.Fatal("sample query or capture bypassed application authentication")
	}
	request := httptest.NewRequest(http.MethodGet, "/api/contacts", nil)
	request.SetBasicAuth(fixtureUser, app.password)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "real-query-one@example.test") {
		t.Fatal("authenticated sample did not return the real database query result")
	}
	received := <-payloads
	observations, err := runtimesecurity.ParseOTLP(received)
	if err != nil || len(observations) != 1 {
		t.Fatalf("sample did not emit valid standard OTLP: %v", err)
	}
	o := observations[0]
	if len(o.TraceRef) != 32 || len(o.SpanRef) != 16 || len(o.SubjectRef) != 64 || len(o.DatabaseTargets) != 1 || o.DatabaseTargets[0].Source != app.source || o.DatabaseTargets[0].Database != db.Config().Database || o.DatabaseTargets[0].Table != "classification_contacts" || o.DatabaseTargets[0].Operation != "SELECT" {
		t.Fatalf("real source relationship missing: %+v", o)
	}
	for _, location := range []string{"database", "response"} {
		if !slices.ContainsFunc(o.DataFields, func(field runtimesecurity.DataField) bool {
			return field.Location == location && field.Method == "format" && field.Category == "email" && field.Count == 2 && field.Observed == 2
		}) {
			t.Errorf("missing %s source-derived format count", location)
		}
	}
	for _, secret := range []string{"real-query-one@example.test", "real-query-two@example.test", fixtureQuery, app.password, fixtureUser, "fixture-otlp-token"} {
		if bytes.Contains(received, []byte(secret)) {
			t.Errorf("raw values, SQL or credentials escaped into OTLP: %s", secret)
		}
	}
}
