package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5"
)

func controlPGFixture(t *testing.T) (context.Context, *pgx.Conn, *controlAgent, store.ControlCommand) {
	t.Helper()
	dsn := os.Getenv("PLUME_CONTROL_TEST_DSN")
	if dsn == "" {
		dsn = os.Getenv("PLUME_STORE_TEST_DSN")
	}
	if dsn == "" {
		t.Skip("set PLUME_CONTROL_TEST_DSN or PLUME_STORE_TEST_DSN to disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	suffix := fmt.Sprint(time.Now().UnixNano())
	schema := "control_" + suffix
	table := "contacts \";--"
	role := "reader_" + suffix + "\";--"
	_, err = conn.Exec(ctx, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" NOLOGIN")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanup, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Error(err)
		}
		if _, err := conn.Exec(cleanup, "DROP ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
			t.Error(err)
		}
	})
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	target := pgx.Identifier{schema, table}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE TABLE "+target+"(value text)"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "GRANT SELECT,INSERT,UPDATE ON TABLE "+target+" TO "+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "GRANT DELETE ON TABLE "+target+" TO PUBLIC"); err != nil {
		t.Fatal(err)
	}
	a := &controlAgent{dbConfig: config, path: filepath.Join(t.TempDir(), "journal.json"), journal: map[string]journalEntry{}}
	command := store.ControlCommand{ID: 1, Revision: 2, Phase: "apply", Status: "approved", Lease: "fixture-lease", Spec: store.ControlSpec{Host: config.Host, Port: int(config.Port), Database: config.Database, Schema: schema, Table: table, Role: role, ExpectedSelect: true}, ExpiresAt: time.Now().Add(time.Minute)}
	return ctx, conn, a, command
}

func controlDirect(t *testing.T, ctx context.Context, conn *pgx.Conn, spec store.ControlSpec) bool {
	t.Helper()
	var selectPermission, grantable bool
	if err := conn.QueryRow(ctx, directSelectQuery, spec.Schema, spec.Table, spec.Role).Scan(&selectPermission, &grantable); err != nil {
		t.Fatal(err)
	}
	return selectPermission
}

func TestControlAgentRealPostgresQuotedTargetsAndExactRollback(t *testing.T) {
	ctx, conn, a, command := controlPGFixture(t)
	result := a.execute(ctx, command, controlJournalKey(command))
	if !result.Success || result.Code != "select_revoked" || result.HadSelect == nil || !*result.HadSelect || result.HasSelect == nil || *result.HasSelect || controlDirect(t, ctx, conn, command.Spec) {
		t.Fatalf("REVOKE postcondition failed: %+v", result)
	}
	assertOtherPermissions := func() {
		t.Helper()
		var insertPermission, updatePermission, publicDelete bool
		err := conn.QueryRow(ctx, `SELECT has_table_privilege($1,$2,'INSERT'),has_table_privilege($1,$2,'UPDATE'),EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN LATERAL aclexplode(c.relacl) acl WHERE n.nspname=$3 AND c.relname=$4 AND acl.grantee=0 AND acl.privilege_type='DELETE')`, command.Spec.Role, pgx.Identifier{command.Spec.Schema, command.Spec.Table}.Sanitize(), command.Spec.Schema, command.Spec.Table).Scan(&insertPermission, &updatePermission, &publicDelete)
		if err != nil {
			t.Fatal(err)
		}
		if !insertPermission || !updatePermission || !publicDelete {
			t.Fatal("SELECT control changed unrelated permissions")
		}
	}
	assertOtherPermissions()
	rollback := command
	rollback.Phase = "rollback"
	rollback.Status = "rollback_requested"
	rollback.Original = &result
	rollback.Lease = "rollback-lease"
	rollback.ExpiresAt = time.Now().Add(-time.Minute)
	restored := a.execute(ctx, rollback, controlJournalKey(rollback))
	if !restored.Success || restored.Code != "select_restored" || !controlDirect(t, ctx, conn, command.Spec) {
		t.Fatalf("explicit rollback failed to restore original direct SELECT: %+v", restored)
	}
	assertOtherPermissions()
	command.ID = 2
	result = a.execute(ctx, command, controlJournalKey(command))
	if !result.Success {
		t.Fatal("second isolated apply failed")
	}
	rollback.ID = 2
	rollback.Original = nil
	invalid := a.execute(ctx, rollback, controlJournalKey(rollback))
	if invalid.Success || invalid.Code != "scope_changed" || controlDirect(t, ctx, conn, command.Spec) {
		t.Fatal("rollback invented an original SELECT grant")
	}
	drift := command
	drift.ID = 3
	drift.Spec.Database += "_changed"
	if r := a.execute(ctx, drift, controlJournalKey(drift)); r.Success || r.Code != "configuration_mismatch" {
		t.Fatal("database configuration drift was executed")
	}
	if _, err := conn.Exec(ctx, "GRANT SELECT ON TABLE "+pgx.Identifier{command.Spec.Schema, command.Spec.Table}.Sanitize()+" TO "+pgx.Identifier{command.Spec.Role}.Sanitize()+" WITH GRANT OPTION"); err != nil {
		t.Fatal(err)
	}
	command.ID = 4
	if r := a.execute(ctx, command, controlJournalKey(command)); r.Success || r.Code != "scope_changed" || !controlDirect(t, ctx, conn, command.Spec) {
		t.Fatal("grant-option drift was silently rewritten")
	}
	if _, err := conn.Exec(ctx, "GRANT SELECT ON TABLE "+pgx.Identifier{command.Spec.Schema, command.Spec.Table}.Sanitize()+" TO PUBLIC"); err != nil {
		t.Fatal(err)
	}
	publicCommand := command
	publicCommand.ID = 5
	publicCommand.Spec.Role = "PUBLIC"
	publicResult := a.execute(ctx, publicCommand, controlJournalKey(publicCommand))
	if !publicResult.Success || controlDirect(t, ctx, conn, publicCommand.Spec) || !controlDirect(t, ctx, conn, command.Spec) {
		t.Fatalf("PUBLIC revoke touched a named role or failed: %+v", publicResult)
	}
	publicCommand.Phase = "rollback"
	publicCommand.Lease = "public-rollback"
	publicCommand.Original = &publicResult
	publicCommand.ExpiresAt = time.Now().Add(-time.Minute)
	if r := a.execute(ctx, publicCommand, controlJournalKey(publicCommand)); !r.Success || !controlDirect(t, ctx, conn, publicCommand.Spec) {
		t.Fatalf("PUBLIC original grant not restored: %+v", r)
	}
	assertOtherPermissions()
}

func TestControlAgentDurableReceiptRetryDoesNotRepeatSQL(t *testing.T) {
	ctx, conn, a, command := controlPGFixture(t)
	var receipts []store.ControlReceipt
	claims := 0
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-source-token" {
			t.Error("source authorization changed")
		}
		switch r.URL.Path {
		case "/api/v1/operations/control/claim":
			claims++
			command.Lease = fmt.Sprintf("lease-%d", claims)
			json.NewEncoder(w).Encode(map[string]any{"command": command})
		case "/api/v1/operations/control/receipts":
			var receipt store.ControlReceipt
			if err := json.NewDecoder(r.Body).Decode(&receipt); err != nil {
				t.Error(err)
			}
			receipts = append(receipts, receipt)
			if len(receipts) == 1 {
				w.WriteHeader(503)
				return
			}
			io.WriteString(w, `{"accepted":true}`)
		default:
			t.Error("unexpected platform path")
			w.WriteHeader(404)
		}
	}))
	defer platform.Close()
	a.origin, a.token, a.client = platform.URL, "fixture-source-token", platform.Client()
	if err := a.runOne(ctx); err == nil {
		t.Fatal("failed first receipt delivery was hidden")
	}
	if controlDirect(t, ctx, conn, command.Spec) {
		t.Fatal("initial SQL revoke did not execute")
	}
	data, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(a.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("durable journal is not private")
	}
	if a.dbConfig.Password != "" && strings.Contains(string(data), a.dbConfig.Password) {
		t.Fatal("durable journal contains a database credential")
	}
	// Simulate an independently restored privilege between execution and receipt
	// acknowledgement. A restarted agent must resend the frozen result only.
	if _, err := conn.Exec(ctx, "GRANT SELECT ON TABLE "+pgx.Identifier{command.Spec.Schema, command.Spec.Table}.Sanitize()+" TO "+pgx.Identifier{command.Spec.Role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	restarted := &controlAgent{origin: a.origin, token: a.token, client: a.client, path: a.path, dbConfig: a.dbConfig, journal: map[string]journalEntry{}}
	if err := json.Unmarshal(data, &restarted.journal); err != nil {
		t.Fatal(err)
	}
	if err := restarted.runOne(ctx); err != nil {
		t.Fatal(err)
	}
	if !controlDirect(t, ctx, conn, command.Spec) {
		t.Fatal("durable retry reran SQL and overwrote a later source change")
	}
	if len(receipts) != 2 {
		t.Fatal("expected failed and retried source receipts")
	}
	firstResult, _ := json.Marshal(receipts[0].Result)
	secondResult, _ := json.Marshal(receipts[1].Result)
	if string(firstResult) != string(secondResult) || receipts[0].Lease == receipts[1].Lease {
		t.Fatal("retry did not preserve frozen result with renewed lease")
	}
}

func TestControlRoleEscaping(t *testing.T) {
	if controlRole("PUBLIC") != "PUBLIC" || controlRole(`reader";--`) != `"reader"";--"` {
		t.Fatal("role names are not quoted as identifiers")
	}
}

func TestControlAgentOnceExitStatus(t *testing.T) {
	if os.Getenv("SCARLET_CONTROL_ONCE_TEST_HELPER") == "1" {
		flag.CommandLine = flag.NewFlagSet("control-agent", flag.ExitOnError)
		os.Args = []string{"control-agent", "-url", os.Getenv("SCARLET_CONTROL_TEST_ORIGIN"), "-local", "-once", "-journal", os.Getenv("SCARLET_CONTROL_TEST_JOURNAL")}
		main()
		os.Exit(0)
	}
	for _, status := range []int{200, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				io.WriteString(w, `{"command":null}`)
			}))
			defer platform.Close()
			command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestControlAgentOnceExitStatus$")
			command.Env = append(os.Environ(), "SCARLET_CONTROL_ONCE_TEST_HELPER=1", "SCARLET_CONTROL_TEST_ORIGIN="+platform.URL, "SCARLET_CONTROL_TEST_JOURNAL="+filepath.Join(t.TempDir(), "journal.json"), "SCARLET_OPERATIONS_TOKEN=fixture-token", "SCARLET_CONTROL_PG_DSN=host=127.0.0.1 port=1 dbname=fixture user=fixture sslmode=disable")
			output, err := command.CombinedOutput()
			if status == 200 && err != nil {
				t.Fatalf("empty successful once failed: %s", output)
			}
			if status != 200 {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 {
					t.Fatalf("failed -once must exit1: error=%v output=%s", err, output)
				}
			}
		})
	}
}
