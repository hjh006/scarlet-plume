package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestServiceFlowLinksRequireTwoAuthorizedSpanChains(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := New(pool)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("service-chain-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Service chains"))
	must(repo.Bootstrap(ctx, slug+"-other", "unused", slug+"-other", "Other"))
	owner, err := repo.FindUser(ctx, slug)
	must(err)
	otherUser, err := repo.FindUser(ctx, slug+"-other")
	must(err)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	other, err := repo.Membership(ctx, otherUser.ID, slug+"-other")
	must(err)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=ANY($1)`, []int64{ws.ID, other.ID})
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE username=ANY($1)`, []string{slug, slug + "-other"})
	})
	digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(slug+value))) }
	create := func(workspace, user int64, name, service string) (RuntimeSource, string) {
		t.Helper()
		token := digest(name)
		s, e := repo.CreateRuntimeSource(ctx, workspace, user, name, service, "otlp", token)
		must(e)
		return s, token
	}
	caller, atoken := create(ws.ID, owner.ID, "Caller collector", "Service A")
	receiver, btoken := create(ws.ID, owner.ID, "Receiver collector", "Service B")
	_, wrongToken := create(ws.ID, owner.ID, "Wrong alias collector", "Wrong alias")
	_, foreignToken := create(other.ID, otherUser.ID, "Foreign collector", "Service B")
	now := time.Now().UTC().Truncate(time.Microsecond)
	trace := strings.Repeat("1", 32)
	clientSpan := strings.Repeat("2", 16)
	a := runtimesecurity.Observation{Method: "GET", Route: "/chain", Status: 200, ObservedAt: now, TraceRef: trace, SpanRef: strings.Repeat("a", 16), ServiceCalls: []runtimesecurity.ServiceCall{{TargetService: "Service B", SpanRef: clientSpan}}, SubjectRef: strings.Repeat("e", 64)}
	b := runtimesecurity.Observation{Method: "GET", Route: "/orders", Status: 200, ObservedAt: now.Add(-20 * time.Millisecond), TraceRef: trace, SpanRef: strings.Repeat("b", 16), ParentSpanRef: clientSpan, DatabaseSystems: []string{"postgresql"}}
	send := func(token, batch string, o runtimesecurity.Observation) {
		t.Helper()
		_, e := repo.DeliverRuntimeBatch(ctx, "otlp", token, digest(batch), []runtimesecurity.Observation{o})
		must(e)
	}
	count := func(filter DataFlowFilter, want int) DataFlowGraph {
		t.Helper()
		g, e := repo.DataFlows(ctx, ws.ID, owner.ID, filter)
		must(e)
		n := 0
		for _, edge := range g.Edges {
			if edge.Relation == "service_call" {
				n++
				if len(edge.Evidence) != 2 {
					t.Fatal("missing dual source provenance")
				}
				if edge.Evidence[0].SourceID == edge.Evidence[1].SourceID {
					t.Fatal("same source masqueraded as two observers")
				}
				for _, ev := range edge.Evidence {
					if ev.Requests != 0 || ev.TraceRef != trace || ev.Details["clientSpanRef"] != clientSpan {
						t.Fatal("incorrect counting or missing span proof")
					}
				}
			}
		}
		if n != want {
			t.Fatalf("got %d links want %d", n, want)
		}
		return g
	}
	send(atoken, "caller", a)
	pending := count(DataFlowFilter{}, 0)
	if !containsServiceFlowWarning(pending, "unmatched_service_call") {
		t.Fatal("unmatched call candidate disappeared")
	}
	// Same trace is insufficient: source/service, parent, tenant and time all bind.
	send(wrongToken, "wrong-service", b)
	count(DataFlowFilter{}, 0)
	wrongParent := b
	wrongParent.ParentSpanRef = strings.Repeat("3", 16)
	send(btoken, "wrong-parent", wrongParent)
	count(DataFlowFilter{}, 0)
	wrongTrace := b
	wrongTrace.TraceRef = strings.Repeat("4", 32)
	send(btoken, "wrong-trace", wrongTrace)
	count(DataFlowFilter{}, 0)
	late := b
	late.ObservedAt = now.Add(-3 * time.Minute)
	send(btoken, "outside-window", late)
	count(DataFlowFilter{}, 0)
	send(foreignToken, "other-tenant", b)
	count(DataFlowFilter{}, 0)
	send(btoken, "receiver", b)
	count(DataFlowFilter{}, 1)
	// Cross-batch duplicates with the same source and span do not count twice.
	send(btoken, "receiver-duplicate", b)
	count(DataFlowFilter{}, 1)
	filtered := count(DataFlowFilter{Service: "Service A"}, 1)
	hasReceiver, hasReceiverDB := false, false
	for _, node := range filtered.Nodes {
		hasReceiver = hasReceiver || node.Kind == "api" && node.Service == "Service B"
	}
	for _, edge := range filtered.Edges {
		hasReceiverDB = hasReceiverDB || edge.Relation == "database_type"
	}
	if !hasReceiver || !hasReceiverDB {
		t.Fatal("verified downstream neighbor context was filtered away")
	}
	count(DataFlowFilter{TraceRef: strings.Repeat("5", 32)}, 0)
	count(DataFlowFilter{SubjectRef: a.SubjectRef}, 1)
	_, err = pool.Exec(ctx, `UPDATE plume.runtime_security_sources SET revoked_at=clock_timestamp() WHERE id=$1`, receiver.ID)
	must(err)
	count(DataFlowFilter{}, 0)
	_, err = pool.Exec(ctx, `UPDATE plume.runtime_security_sources SET revoked_at=NULL WHERE id=$1`, receiver.ID)
	must(err)
	count(DataFlowFilter{}, 1)
	_, err = pool.Exec(ctx, `UPDATE plume.memberships SET role='operator' WHERE workspace_id=$1 AND user_id=$2`, ws.ID, owner.ID)
	must(err)
	// DataFlows still permits a workspace operator, but a revoked source owner no longer corroborates links.
	count(DataFlowFilter{}, 0)
	_, err = pool.Exec(ctx, `UPDATE plume.memberships SET role='owner' WHERE workspace_id=$1 AND user_id=$2`, ws.ID, owner.ID)
	must(err)
	_, err = repo.DataFlows(ctx, other.ID, owner.ID, DataFlowFilter{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign caller accessed graph")
	}
	rows := make([][]any, 501)
	for i := range rows {
		entry := a
		entry.TraceRef = fmt.Sprintf("%032x", i+100)
		entry.ServiceCalls = []runtimesecurity.ServiceCall{{TargetService: "Unobserved service", SpanRef: fmt.Sprintf("%016x", i+200)}}
		raw, e := json.Marshal(entry)
		must(e)
		rows[i] = []any{ws.ID, caller.ID, "pair-budget", i, entry.ObservedAt, raw}
	}
	_, err = pool.CopyFrom(ctx, pgx.Identifier{"plume", "data_flow_observations"}, []string{"workspace_id", "source_id", "batch_id", "ordinal", "observed_at", "observation"}, pgx.CopyFromRows(rows))
	must(err)
	_, err = repo.DataFlows(ctx, ws.ID, owner.ID, DataFlowFilter{})
	if !errors.Is(err, ErrDataFlowLimit) {
		t.Fatalf("service pair limit was not explicit: %v", err)
	}
}
func containsServiceFlowWarning(g DataFlowGraph, value string) bool {
	for _, warning := range g.Warnings {
		if warning == value {
			return true
		}
	}
	return false
}
