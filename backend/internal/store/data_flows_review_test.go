package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

func TestDataFlowExactTargetRequiresCurrentPostgreSQLInspection(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := New(pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("flow-current-pg-%d", time.Now().UnixNano())
	user := slug + "-owner"
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := pool.Exec(cleanup, "DELETE FROM plume.workspaces WHERE slug=$1", slug)
		if err != nil {
			t.Error(err)
		}
		_, err = pool.Exec(cleanup, "DELETE FROM plume.users WHERE username=$1", user)
		if err != nil {
			t.Error(err)
		}
	})
	must(repo.Bootstrap(ctx, user, "unused", slug, "Current PostgreSQL graph"))
	actor, err := repo.FindUser(ctx, user)
	must(err)
	workspace, err := repo.Membership(ctx, actor.ID, slug)
	must(err)
	digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
	config := ManagedPostgresInput{Host: "sample-db", Port: 5432, Database: "scarlet_sample", User: "sample_reader", Schemas: []string{"inventory"}, IntervalMinutes: 5, EncryptedPassword: []byte("fixture")}
	job, err := repo.CreateManagedPostgresJob(ctx, workspace.ID, actor.ID, "Exact PostgreSQL source", digest(slug), config)
	must(err)
	table := collector.PostgreSQLTable{Asset: collector.Asset{SourceRef: "inventory.contacts", Name: "contacts"}, Schema: "inventory", Owner: "source_owner", MetadataStatus: "complete", Columns: []collector.PostgreSQLColumn{{Name: "contact_value", Type: "text", BaseType: "text", CanSelect: true, Ordinal: 1}}, Constraints: []collector.PostgreSQLConstraint{}, Grants: []collector.PostgreSQLGrant{}, PublicSelectColumns: []string{}, Warnings: []string{}, ContentAnalysis: &collector.PostgreSQLContentAnalysis{Status: "partial", Method: "bounded_prefix_aggregate", RowLimit: 200, RowsScanned: 2, Warnings: []string{"row_security_filtered_scope"}, Columns: []collector.PostgreSQLColumnStatistics{{Column: "contact_value", Status: "complete", NonNullRows: 2, Matches: []collector.PostgreSQLPatternCount{{Category: "email", Count: 1}}}}}}
	collect := func(name string, tables []collector.PostgreSQLTable, failure string) {
		t.Helper()
		must(repo.QueueManagedPostgresJob(ctx, workspace.ID, actor.ID, job.ID))
		claim, err := repo.ClaimManagedPostgresJob(ctx, slug+name, time.Minute)
		must(err)
		if claim == nil || claim.JobID != job.ID {
			t.Fatal("could not claim source fixture")
		}
		input := AutomationDelivery{Digest: digest(name), ObservedAt: time.Now().UTC(), FailureCode: failure}
		if failure == "" {
			input.Assets = []CollectedAsset{}
			input.PostgresTables = tables
			for _, table := range tables {
				input.Assets = append(input.Assets, CollectedAsset{SourceRef: table.SourceRef, Name: table.Name})
			}
		}
		_, err = repo.FinishManagedPostgresJob(ctx, *claim, input)
		must(err)
	}
	collect("initial", []collector.PostgreSQLTable{table}, "")
	token := digest(slug + "-otlp")
	_, err = repo.CreateRuntimeSource(ctx, workspace.ID, actor.ID, "Application probe", "Contacts app", "otlp", token)
	must(err)
	event := runtimesecurity.Observation{Method: "GET", Route: "/contacts", Status: 200, ObservedAt: time.Now().UTC(), TraceRef: strings.Repeat("1", 32), SpanRef: strings.Repeat("2", 16), DatabaseTargets: []runtimesecurity.DatabaseTarget{{System: "postgresql", Source: job.Source, Database: config.Database, Schema: "inventory", Table: "contacts", Operation: "SELECT"}}}
	_, err = repo.DeliverRuntimeBatch(ctx, "otlp", token, digest("exact-source-event"), []runtimesecurity.Observation{event})
	must(err)
	assertTarget := func(phase string, want bool) DataFlowGraph {
		t.Helper()
		graph, err := repo.DataFlows(ctx, workspace.ID, actor.ID, DataFlowFilter{})
		must(err)
		found := slices.ContainsFunc(graph.Edges, func(edge DataFlowEdge) bool { return edge.Relation == "database_target" })
		if found != want {
			t.Errorf("%s: exact target=%t, want %t; old source metadata must not verify the current target", phase, found, want)
		}
		return graph
	}
	initial := assertTarget("initial", true)
	var tableNode DataFlowNode
	for _, node := range initial.Nodes {
		if node.Kind == "table" {
			tableNode = node
		}
	}
	if !slices.Contains(tableNode.Categories, "email") {
		t.Error("generic field's real positive content statistics disappeared from the table tag")
	}
	if !slices.ContainsFunc(tableNode.Evidence, func(e DataFlowEvidence) bool { return slices.Contains(e.Warnings, "row_security_filtered_scope") }) {
		t.Error("content-observation scope warning disappeared from table evidence")
	}
	collect("missing-table", []collector.PostgreSQLTable{}, "")
	assertTarget("missing-table", false)
	collect("restored", []collector.PostgreSQLTable{table}, "")
	assertTarget("restored", true)
	collect("failed", nil, "collection_timeout")
	assertTarget("failed", false)
	snapshots, err := repo.ListDataFlowSnapshots(ctx, workspace.ID, actor.ID)
	must(err)
	if len(snapshots) == 0 {
		t.Fatal("source changes did not create automatic data-flow history")
	}
	latest, err := repo.DataFlowSnapshot(ctx, workspace.ID, actor.ID, snapshots[0].ID)
	must(err)
	if slices.ContainsFunc(latest.Graph.Edges, func(edge DataFlowEdge) bool { return edge.Relation == "database_target" }) {
		t.Fatal("failed collection did not freeze removal of the stale exact target")
	}
}

func TestDataFlowSnapshotContentionDoesNotRejectIngest(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := New(pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("flow-lock-%d", time.Now().UnixNano())
	username := slug + "-owner"
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := pool.Exec(cleanup, "DELETE FROM plume.workspaces WHERE slug=$1", slug)
		if err != nil {
			t.Error(err)
		}
		_, err = pool.Exec(cleanup, "DELETE FROM plume.users WHERE username=$1", username)
		if err != nil {
			t.Error(err)
		}
	})
	must(repo.Bootstrap(ctx, username, "unused", slug, "Snapshot lock fixture"))
	actor, err := repo.FindUser(ctx, username)
	must(err)
	workspace, err := repo.Membership(ctx, actor.ID, slug)
	must(err)
	digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
	token := digest(slug + "-token")
	source, err := repo.CreateRuntimeSource(ctx, workspace.ID, actor.ID, "Contention source", "Contention service", "gateway", token)
	must(err)
	lock, err := pool.Begin(ctx)
	must(err)
	defer lock.Rollback(context.Background())
	_, err = lock.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('plume:data-flow:'||$1::bigint::text,0))`, workspace.ID)
	must(err)
	deliveryCtx, deliveryCancel := context.WithTimeout(ctx, 4*time.Second)
	defer deliveryCancel()
	receipt, err := repo.DeliverRuntimeBatch(deliveryCtx, "gateway", token, digest("snapshot-contention"), []runtimesecurity.Observation{{Method: "GET", Route: "/observed", Status: 200, ObservedAt: time.Now().UTC()}})
	if err != nil || receipt.Accepted != 1 {
		t.Fatalf("snapshot lock contention rejected otherwise valid ingest: %+v %v", receipt, err)
	}
	var requests int64
	must(pool.QueryRow(ctx, "SELECT requests FROM plume.runtime_security_routes WHERE source_id=$1", source.ID).Scan(&requests))
	if requests != 1 {
		t.Fatal("snapshot lock contention lost accepted request telemetry")
	}
	var state string
	must(pool.QueryRow(ctx, "SELECT status FROM plume.data_flow_snapshot_status WHERE workspace_id=$1", workspace.ID).Scan(&state))
	if state != "error" {
		t.Fatal("snapshot contention was not recorded separately from successful ingest")
	}
}

func TestDataFlowInvestigationMatchesFocusedNodeBeforeTruncation(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := New(pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("flow-focus-%d", time.Now().UnixNano())
	username := slug + "-owner"
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := pool.Exec(cleanup, "DELETE FROM plume.workspaces WHERE slug=$1", slug)
		if err != nil {
			t.Error(err)
		}
		_, err = pool.Exec(cleanup, "DELETE FROM plume.users WHERE username=$1", username)
		if err != nil {
			t.Error(err)
		}
	})
	must(repo.Bootstrap(ctx, username, "unused", slug, "Focused investigation fixture"))
	actor, err := repo.FindUser(ctx, username)
	must(err)
	workspace, err := repo.Membership(ctx, actor.ID, slug)
	must(err)
	digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
	config := ManagedPostgresInput{Host: "sample-db", Port: 5432, Database: "scarlet_sample", User: "sample_reader", Schemas: []string{"inventory"}, IntervalMinutes: 5, EncryptedPassword: []byte("fixture")}
	job, err := repo.CreateManagedPostgresJob(ctx, workspace.ID, actor.ID, "Focus PostgreSQL source", digest(slug), config)
	must(err)
	tables := []collector.PostgreSQLTable{}
	assets := []CollectedAsset{}
	for _, name := range []string{"contacts", "orders"} {
		tables = append(tables, collector.PostgreSQLTable{Asset: collector.Asset{SourceRef: "inventory." + name, Name: name}, Schema: "inventory", Owner: "source_owner", MetadataStatus: "complete", Columns: []collector.PostgreSQLColumn{{Name: "value", Type: "text", BaseType: "text", CanSelect: true, Ordinal: 1}}, Constraints: []collector.PostgreSQLConstraint{}, Grants: []collector.PostgreSQLGrant{}, PublicSelectColumns: []string{}, Warnings: []string{}})
		assets = append(assets, CollectedAsset{SourceRef: "inventory." + name, Name: name})
	}
	must(repo.QueueManagedPostgresJob(ctx, workspace.ID, actor.ID, job.ID))
	claim, err := repo.ClaimManagedPostgresJob(ctx, slug, time.Minute)
	must(err)
	if claim == nil || claim.JobID != job.ID {
		t.Fatal("could not claim source fixture")
	}
	_, err = repo.FinishManagedPostgresJob(ctx, *claim, AutomationDelivery{Digest: digest("focused-pg"), ObservedAt: time.Now().UTC(), Assets: assets, PostgresTables: tables})
	must(err)
	const service = "Focused contacts application"
	token := digest(slug + "-one")
	otherToken := digest(slug + "-two")
	source, err := repo.CreateRuntimeSource(ctx, workspace.ID, actor.ID, "Primary application probe", service, "otlp", token)
	must(err)
	_, err = repo.CreateRuntimeSource(ctx, workspace.ID, actor.ID, "Second application probe", service, "otlp", otherToken)
	must(err)
	now := time.Now().UTC()
	event := func(table, alias, subject string, status int) runtimesecurity.Observation {
		return runtimesecurity.Observation{Method: "GET", Route: "/contacts/{id}", Status: status, ObservedAt: now, SubjectRef: subject, DatabaseSystems: []string{"postgresql"}, DatabaseTargets: []runtimesecurity.DatabaseTarget{{System: "postgresql", Source: alias, Database: config.Database, Schema: "inventory", Table: table, Operation: "SELECT"}}}
	}
	first := event("contacts", job.Source, strings.Repeat("a", 64), 200)
	second := event("orders", job.Source, strings.Repeat("b", 64), 201)
	second.Route = "/contacts/{contact_id}"
	wrongSource := event("contacts", "Unmatched PostgreSQL alias", strings.Repeat("c", 64), 202)
	typeOnly := event("contacts", job.Source, "", 204)
	typeOnly.DatabaseTargets = nil
	_, err = repo.DeliverRuntimeBatch(ctx, "otlp", token, digest("focused-first-source"), []runtimesecurity.Observation{first, second, wrongSource, typeOnly})
	must(err)
	otherSource := event("contacts", job.Source, first.SubjectRef, 203)
	_, err = repo.DeliverRuntimeBatch(ctx, "otlp", otherToken, digest("focused-second-source"), []runtimesecurity.Observation{otherSource})
	must(err)
	graph, err := repo.DataFlows(ctx, workspace.ID, actor.ID, DataFlowFilter{})
	must(err)
	index := slices.IndexFunc(graph.Nodes, func(node DataFlowNode) bool {
		return node.Kind == "table" && node.Label == "scarlet_sample.inventory.contacts"
	})
	if index < 0 {
		t.Fatal("current PostgreSQL table missing from graph")
	}
	tableFocus := graph.Nodes[index].ID
	assertStatuses := func(focus string, want []int) {
		t.Helper()
		result, err := repo.InvestigateDataFlows(ctx, workspace.ID, actor.ID, DataFlowFilter{Focus: focus})
		must(err)
		got := []int{}
		for _, item := range result.Items {
			got = append(got, item.Status)
		}
		slices.Sort(got)
		if result.Truncated || !slices.Equal(got, want) {
			t.Fatalf("focus %s: statuses=%v truncated=%t, want %v without truncation", focus, got, result.Truncated, want)
		}
	}
	assertStatuses(tableFocus, []int{200, 203})
	assertStatuses("api:"+flowID(service, "GET", runtimesecurity.RouteKey(first.Route)), []int{200, 201, 202, 203, 204})
	assertStatuses("application:"+flowID(service), []int{200, 201, 202, 203, 204})
	assertStatuses("subject:"+flowID(fmt.Sprint(source.ID), first.SubjectRef), []int{200})
	assertStatuses("database_type:"+flowID(fmt.Sprint(source.ID), service, "postgresql"), []int{200, 201, 202, 204})
	assertStatuses("table:999999999", []int{})
	// More than 500 newer unrelated calls must not hide an older exact table match.
	deliverMany := func(table string) {
		t.Helper()
		for offset := 0; offset < 501; offset += 250 {
			observations := make([]runtimesecurity.Observation, min(250, 501-offset))
			for i := range observations {
				observations[i] = event(table, job.Source, "", 200)
				observations[i].ObservedAt = now.Add(time.Second)
			}
			_, err := repo.DeliverRuntimeBatch(ctx, "otlp", token, digest(fmt.Sprintf("focused-many-%s-%d", table, offset)), observations)
			must(err)
		}
	}
	deliverMany("orders")
	assertStatuses(tableFocus, []int{200, 203})
	deliverMany("contacts")
	result, err := repo.InvestigateDataFlows(ctx, workspace.ID, actor.ID, DataFlowFilter{Focus: tableFocus})
	must(err)
	if len(result.Items) != 500 || !result.Truncated {
		t.Fatalf("matching-event limit: items=%d truncated=%t, want 500 and true", len(result.Items), result.Truncated)
	}
	for _, item := range result.Items {
		if len(item.DatabaseTargets) != 1 || item.DatabaseTargets[0].Table != "contacts" || item.DatabaseTargets[0].Source != job.Source {
			t.Fatal("focused investigation admitted unrelated table or source")
		}
	}
}
