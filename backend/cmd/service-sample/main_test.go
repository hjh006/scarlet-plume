package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/jackc/pgx/v5"
)

func TestServiceSampleRealHTTPTracePropagationAndPostgreSQLReads(t *testing.T) {
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
		t.Skip("requires disposable database without inventory schema")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(ctx, `CREATE SCHEMA inventory;CREATE TABLE inventory.classification_contacts(id int PRIMARY KEY);INSERT INTO inventory.classification_contacts VALUES(1),(2);CREATE TABLE inventory.orders(id int PRIMARY KEY);INSERT INTO inventory.orders VALUES(1),(2),(3)`)
	if err != nil {
		t.Fatal(err)
	}
	type delivery struct {
		token   string
		payload []byte
	}
	delivered := make(chan delivery, 2)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, e := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if e != nil {
			t.Error(e)
		}
		delivered <- delivery{r.Header.Get("Authorization"), body}
		w.WriteHeader(200)
	}))
	defer collector.Close()
	password := "synthetic-password-never-export"
	b := &sample{db: tx, databaseName: db.Config().Database, role: "b", password: password, token: "fixture-b-token", source: "PG actual source", endpoint: collector.URL, client: collector.Client()}
	downstream := httptest.NewServer(b)
	defer downstream.Close()
	a := &sample{db: tx, databaseName: db.Config().Database, role: "a", password: password, token: "fixture-a-token", source: "PG actual source", endpoint: collector.URL, peer: "Service B", downstream: downstream.URL + "/api/orders", client: downstream.Client()}
	unauthorized := httptest.NewRecorder()
	a.ServeHTTP(unauthorized, httptest.NewRequest("GET", "/api/chain", nil))
	if unauthorized.Code != 401 || len(delivered) != 0 {
		t.Fatal("sample authentication bypassed")
	}
	request := httptest.NewRequest("GET", "/api/chain", nil)
	request.SetBasicAuth("scarlet-demo", password)
	response := httptest.NewRecorder()
	a.ServeHTTP(response, request)
	var counts struct{ Rows, Downstream int64 }
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	counts.Rows = int64(result["rowsRead"].(float64))
	counts.Downstream = int64(result["downstreamRows"].(float64))
	if response.Code != 200 || counts.Rows != 2 || counts.Downstream != 3 {
		t.Fatalf("real table reads missing: status%d", response.Code)
	}
	observations := map[string]runtimesecurity.Observation{}
	for range 2 {
		select {
		case item := <-delivered:
			parsed, e := runtimesecurity.ParseOTLP(item.payload)
			if e != nil || len(parsed) != 1 {
				t.Fatalf("invalid per-service OTLP: %v", e)
			}
			observations[item.token] = parsed[0]
			for _, raw := range []string{password, "scarlet-demo", "fixture-a-token", "fixture-b-token", "SELECT count(*)"} {
				if bytes.Contains(item.payload, []byte(raw)) {
					t.Fatal("raw credentials or SQL exported")
				}
			}
		case <-ctx.Done():
			t.Fatal("missing source delivery")
		}
	}
	left, right := observations["Bearer fixture-a-token"], observations["Bearer fixture-b-token"]
	if left.TraceRef == "" || left.TraceRef != right.TraceRef || len(left.ServiceCalls) != 1 || right.ParentSpanRef != left.ServiceCalls[0].SpanRef || left.ServiceCalls[0].TargetService != "Service B" || len(left.DatabaseTargets) != 1 || left.DatabaseTargets[0].Table != "classification_contacts" || len(right.DatabaseTargets) != 1 || right.DatabaseTargets[0].Table != "orders" {
		t.Fatal("two actual HTTP services did not preserve the parent/client/database chain")
	}
}
