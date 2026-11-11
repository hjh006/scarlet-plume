package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestDataFlowHTTPBoundariesAndFrozenSnapshots(t *testing.T) {
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
	repo := store.New(pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("flow-http-%d", time.Now().UnixNano())
	foreignSlug := slug + "-foreign"
	ownerName, viewerName, foreignName := slug+"-owner", slug+"-viewer", slug+"-foreign-owner"
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := pool.Exec(cleanup, "DELETE FROM plume.workspaces WHERE slug=ANY($1)", []string{slug, foreignSlug})
		if err != nil {
			t.Error(err)
		}
		_, err = pool.Exec(cleanup, "DELETE FROM plume.users WHERE username=ANY($1)", []string{ownerName, viewerName, foreignName})
		if err != nil {
			t.Error(err)
		}
	})
	must(repo.Bootstrap(ctx, ownerName, "unused", slug, "Flow HTTP"))
	must(repo.ProvisionReviewer(ctx, slug, viewerName, "unused"))
	must(repo.Bootstrap(ctx, foreignName, "unused", foreignSlug, "Foreign flow HTTP"))
	owner, err := repo.FindUser(ctx, ownerName)
	must(err)
	viewer, err := repo.FindUser(ctx, viewerName)
	must(err)
	foreign, err := repo.FindUser(ctx, foreignName)
	must(err)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	foreignWorkspace, err := repo.Membership(ctx, foreign.ID, foreignSlug)
	must(err)
	_, err = pool.Exec(ctx, "UPDATE plume.memberships SET role='viewer' WHERE workspace_id=$1 AND user_id=$2", workspace.ID, viewer.ID)
	must(err)
	service := "Flow <service>"
	token := sessionHash(slug + "-source")
	source, err := repo.CreateRuntimeSource(ctx, workspace.ID, owner.ID, "Flow source", service, "otlp", token)
	must(err)
	observation := runtimesecurity.Observation{Method: "GET", Route: "/contacts", Status: 200, ObservedAt: time.Now().UTC(), TraceRef: strings.Repeat("1", 32), SpanRef: strings.Repeat("2", 16), SubjectRef: strings.Repeat("a", 64), DataFields: []runtimesecurity.DataField{{Name: "contact_value", Location: "response", Category: "email", Method: "format", Count: 1, Observed: 1}}}
	_, err = repo.DeliverRuntimeBatch(ctx, "otlp", token, sessionHash("flow-http-initial"), []runtimesecurity.Observation{observation})
	must(err)
	otherService := strings.Repeat("业务", 20)
	otherToken := sessionHash(slug + "-other-source")
	_, err = repo.CreateRuntimeSource(ctx, workspace.ID, owner.ID, "Other flow source", otherService, "otlp", otherToken)
	must(err)
	_, err = repo.DeliverRuntimeBatch(ctx, "otlp", otherToken, sessionHash("flow-http-unrelated"), []runtimesecurity.Observation{{Method: "GET", Route: "/unrelated", Status: 200, ObservedAt: time.Now().UTC(), TraceRef: strings.Repeat("b", 32), SpanRef: strings.Repeat("c", 16), SubjectRef: strings.Repeat("d", 64)}})
	must(err)
	foreignSnapshot, err := repo.CaptureDataFlowSnapshot(ctx, foreignWorkspace.ID, foreign.ID, store.DataFlowFilter{})
	must(err)
	session := func(userID int64) string {
		t.Helper()
		value, err := sessionToken()
		must(err)
		must(repo.CreateSession(ctx, userID, sessionHash(value), time.Now().Add(time.Hour)))
		return value
	}
	ownerToken, viewerToken := session(owner.ID), session(viewer.ID)
	handler := New(repo, "https://example.invalid", true)
	base := "/api/v1/workspaces/" + slug + "/data-flows"
	call := func(method, path, sessionToken, body, origin string, status int) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		if sessionToken != "" {
			request.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: sessionToken})
		}
		if method != http.MethodGet {
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != status || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s returned %d, want %d: %.300s", method, path, response.Code, status, response.Body.String())
		}
		return response
	}
	get := func(path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		return call(http.MethodGet, path, viewerToken, "", "", status)
	}
	call(http.MethodGet, base, "", "", "", http.StatusUnauthorized)
	call(http.MethodPost, base+"/snapshots", ownerToken, `{}`, "https://untrusted.invalid", http.StatusForbidden)
	call(http.MethodPost, base+"/snapshots", viewerToken, `{}`, "https://example.invalid", http.StatusForbidden)
	call(http.MethodPost, base+"/snapshots", ownerToken, `{"payload":{"nodes":[]}}`, "https://example.invalid", http.StatusBadRequest)
	call(http.MethodPost, base+"/snapshots", ownerToken, `{"origin":"runtime"}`, "https://example.invalid", http.StatusBadRequest)
	call(http.MethodPost, base+"/snapshots", ownerToken, `{"service":"`+strings.Repeat("x", 5000)+`"}`, "https://example.invalid", http.StatusRequestEntityTooLarge)
	get(base+"?traceRef=private-user", http.StatusBadRequest)
	get(base+"?subjectRef="+strings.Repeat("a", 32), http.StatusBadRequest)
	get(base+"?subjectRef="+strings.Repeat("a", 32)+"&traceRef="+strings.Repeat("a", 32), http.StatusBadRequest)
	get(base+"/compare?from=0&to=1", http.StatusBadRequest)
	get(base+"/compare", http.StatusBadRequest)
	get(base+"/snapshots/not-a-number", http.StatusBadRequest)
	get(fmt.Sprintf("%s/snapshots/%d", base, foreignSnapshot.ID), http.StatusNotFound)
	get("/api/v1/workspaces/"+foreignSlug+"/data-flows", http.StatusNotFound)
	query := "?service=" + url.QueryEscape(service)
	var countBefore, countAfter int64
	must(pool.QueryRow(ctx, "SELECT count(*) FROM plume.data_flow_snapshots WHERE workspace_id=$1", workspace.ID).Scan(&countBefore))
	var graph, exported store.DataFlowGraph
	must(json.Unmarshal(get(base+query, http.StatusOK).Body.Bytes(), &graph))
	export := get(base+"/export"+query, http.StatusOK)
	if export.Header().Get("Content-Disposition") != `attachment; filename="scarlet-data-flows.json"` {
		t.Fatal("graph export lost its attachment header")
	}
	must(json.Unmarshal(export.Body.Bytes(), &exported))
	if graph.Scope.Service != service || len(graph.Nodes) == 0 || !reflect.DeepEqual(graph.Nodes, exported.Nodes) || !reflect.DeepEqual(graph.Edges, exported.Edges) {
		t.Fatal("filtered graph/export scope or source facts differed")
	}
	get(base+"?service="+url.QueryEscape(otherService), http.StatusOK)
	var traced, missingTrace store.DataFlowGraph
	must(json.Unmarshal(get(base+"?traceRef="+observation.TraceRef, http.StatusOK).Body.Bytes(), &traced))
	for _, node := range traced.Nodes {
		if node.Service == otherService {
			t.Fatal("trace investigation included another trace's static API relationships")
		}
	}
	must(json.Unmarshal(get(base+"?traceRef="+strings.Repeat("f", 32), http.StatusOK).Body.Bytes(), &missingTrace))
	if len(missingTrace.Nodes) != 0 || len(missingTrace.Edges) != 0 {
		t.Fatal("a trace with no observations returned an unrelated static graph")
	}
	var investigation store.DataFlowInvestigation
	must(json.Unmarshal(get(base+"/investigation"+query+"&traceRef="+observation.TraceRef+"&subjectRef="+observation.SubjectRef, http.StatusOK).Body.Bytes(), &investigation))
	if len(investigation.Items) != 1 || investigation.Items[0].SourceID != source.ID || investigation.Items[0].TraceRef != observation.TraceRef {
		t.Fatal("investigation lost source-scoped capture evidence")
	}
	must(pool.QueryRow(ctx, "SELECT count(*) FROM plume.data_flow_snapshots WHERE workspace_id=$1", workspace.ID).Scan(&countAfter))
	if countBefore != countAfter {
		t.Fatal("read-only graph, export or investigation created a snapshot")
	}
	filterJSON, _ := json.Marshal(store.DataFlowFilter{Service: service})
	var saved, replayed store.DataFlowSnapshot
	must(json.Unmarshal(call(http.MethodPost, base+"/snapshots", ownerToken, string(filterJSON), "https://example.invalid", http.StatusOK).Body.Bytes(), &saved))
	must(json.Unmarshal(call(http.MethodPost, base+"/snapshots", ownerToken, string(filterJSON), "https://example.invalid", http.StatusOK).Body.Bytes(), &replayed))
	if saved.ID != replayed.ID || saved.Scope.Service != service {
		t.Fatal("unchanged scoped snapshot was not idempotent")
	}
	var list struct {
		Items []store.DataFlowSnapshotSummary `json:"items"`
		Limit int                             `json:"limit"`
	}
	must(json.Unmarshal(get(base+"/snapshots", http.StatusOK).Body.Bytes(), &list))
	if list.Limit != 30 || len(list.Items) == 0 {
		t.Fatal("snapshot history response incompatible with the UI")
	}
	_, err = repo.DeliverRuntimeBatch(ctx, "otlp", token, sessionHash("flow-http-later"), []runtimesecurity.Observation{{Method: "GET", Route: "/later", Status: 200, ObservedAt: time.Now().UTC(), DataFields: observation.DataFields}})
	must(err)
	var historical store.DataFlowSnapshot
	must(json.Unmarshal(get(fmt.Sprintf("%s/snapshots/%d", base, saved.ID), http.StatusOK).Body.Bytes(), &historical))
	if !reflect.DeepEqual(saved, historical) {
		t.Fatal("later runtime data changed a frozen snapshot")
	}
	full, err := repo.CaptureDataFlowSnapshot(ctx, workspace.ID, owner.ID, store.DataFlowFilter{})
	must(err)
	get(fmt.Sprintf("%s/compare?from=%d&to=%d", base, saved.ID, full.ID), http.StatusBadRequest)
	get(fmt.Sprintf("%s/compare?from=%d&to=%d", base, saved.ID, foreignSnapshot.ID), http.StatusNotFound)
	var same store.DataFlowComparison
	must(json.Unmarshal(get(fmt.Sprintf("%s/compare?from=%d&to=%d", base, saved.ID, saved.ID), http.StatusOK).Body.Bytes(), &same))
	if len(same.AddedNodes)+len(same.RemovedNodes)+len(same.ChangedNodes)+len(same.AddedEdges)+len(same.RemovedEdges)+len(same.ChangedEdges) != 0 {
		t.Fatal("comparison invented changes within the same immutable snapshot")
	}
}
