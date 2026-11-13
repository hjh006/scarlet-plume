package httpapi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOperationsReportHTTPScopesExportsAndSchedules(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := t.Context()
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	repo := store.New(pool)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("operations-http-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "operations"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reviewer", "unused"))
	must(repo.Bootstrap(ctx, slug+"-foreign", "unused", slug+"-foreign", "foreign"))
	owner, e := repo.FindUser(ctx, slug)
	must(e)
	reviewer, e := repo.FindUser(ctx, slug+"-reviewer")
	must(e)
	foreign, e := repo.FindUser(ctx, slug+"-foreign")
	must(e)
	ws, e := repo.Membership(ctx, owner.ID, slug)
	must(e)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE slug=ANY($1)`, []string{slug, slug + "-foreign"})
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE id=ANY($1)`, []int64{owner.ID, reviewer.ID, foreign.ID})
	})
	session := func(id int64) string {
		token, e := sessionToken()
		must(e)
		must(repo.CreateSession(ctx, id, sessionHash(token), time.Now().Add(time.Hour)))
		return token
	}
	ownerToken, reviewToken, foreignToken := session(owner.ID), session(reviewer.ID), session(foreign.ID)
	handler := New(repo, "https://example.invalid", true)
	base := "/api/v1/workspaces/" + slug
	call := func(method, path, token, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, base+path, strings.NewReader(body))
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: token})
		}
		if method != "GET" {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "https://example.invalid")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s got%d want%d %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	service := "=SUM(1,2) <script>service()</script>"
	source, e := repo.CreateRuntimeSource(ctx, ws.ID, owner.ID, "fixture", service, "gateway", sessionHash(slug))
	must(e)
	now := time.Now().UTC()
	event := runtimesecurity.Observation{Method: "POST", Route: "/contacts", Status: 200, ObservedAt: now, ClientIP: "192.0.2.21", InspectedLocations: []string{"request"}, Transport: "http", TransportHop: "proxy_upstream", DataFields: []runtimesecurity.DataField{{Name: "password", Location: "request", Category: "credential", Method: "metadata", Count: 1, Observed: 1}}}
	_, e = repo.DeliverRuntimeBatch(ctx, "gateway", sessionHash(slug), sessionHash("fixture-batch"), []runtimesecurity.Observation{event})
	must(e)
	scope := store.RuntimeEvidenceScope{SourceID: source.ID, Service: service, From: now.Add(-time.Minute), To: now.Add(time.Microsecond)}
	title := "=SUM(1,2) <script>fixture()</script>"
	raw, _ := json.Marshal(store.OperationsReportInput{Title: title, Scope: scope})
	call("POST", "/operations-reports", reviewToken, string(raw), 403)
	call("GET", "/operations-reports", foreignToken, "", 404)
	call("GET", "/operations-reports?sourceId=1&sourceId=2", ownerToken, "", 400)
	call("GET", "/runtime-evidence?from=invalid", ownerToken, "", 400)
	var report store.OperationsInvestigationReport
	must(json.Unmarshal(call("POST", "/operations-reports", ownerToken, string(raw), 201).Body.Bytes(), &report))
	if report.ID == 0 || report.Window.Total != 1 || report.SnapshotDigest == "" || len(report.Analysis) == 0 {
		t.Fatal("report snapshot missing")
	}
	id := fmt.Sprint(report.ID)
	html := call("GET", "/operations-reports/"+id+"/export?format=html", ownerToken, "", 200).Body.String()
	if strings.Contains(html, "<script>") || !strings.Contains(html, "&lt;script&gt;") || !strings.Contains(html, report.SnapshotDigest) {
		t.Fatal("HTML escaping/evidence digest missing")
	}
	csvText := call("GET", "/operations-reports/"+id+"/export?format=csv", ownerToken, "", 200).Body.String()
	rows, e := csv.NewReader(strings.NewReader(strings.TrimPrefix(csvText, "\ufeff"))).ReadAll()
	must(e)
	if len(rows) < 4 || rows[1][2] != "'"+title || !strings.Contains(csvText, report.SnapshotDigest) {
		t.Fatal("CSV formula/evidence escaping missing")
	}
	var exported store.OperationsInvestigationReport
	must(json.Unmarshal(call("GET", "/operations-reports/"+id+"/export?format=json", reviewToken, "", 200).Body.Bytes(), &exported))
	if store.OperationsReportSnapshotDigest(exported) != report.SnapshotDigest {
		t.Fatal("exported evidence digest")
	}
	query := "?sourceId=" + fmt.Sprint(source.ID) + "&clientIp=192.0.2.21&service=" + url.QueryEscape(service) + "&from=" + url.QueryEscape(scope.From.Format(time.RFC3339Nano)) + "&to=" + url.QueryEscape(scope.To.Format(time.RFC3339Nano))
	var window store.RuntimeEvidenceWindow
	must(json.Unmarshal(call("GET", "/runtime-evidence"+query, ownerToken, "", 200).Body.Bytes(), &window))
	if window.Total != 1 {
		t.Fatal("runtime scope filters")
	}
	runScope := scope
	runScope.ClientIP = "192.0.2.21"
	runRaw, _ := json.Marshal(store.ScenarioCheckRunInput{Scenario: "personal", Scope: &runScope})
	var run store.ScenarioCheckRun
	must(json.Unmarshal(call("POST", "/scenario-check-runs", ownerToken, string(runRaw), 201).Body.Bytes(), &run))
	var history struct {
		Items []store.ScenarioCheckRun `json:"items"`
	}
	must(json.Unmarshal(call("GET", "/scenario-check-runs"+query+"&scenario=personal", ownerToken, "", 200).Body.Bytes(), &history))
	if len(history.Items) != 1 {
		t.Fatal("scope-filtered history missing")
	}
	scheduleRaw, _ := json.Marshal(store.OperationsReportScheduleInput{Title: "每日运营", Period: "daily", Timezone: "Asia/Shanghai", Scope: scope, Enabled: true})
	call("POST", "/operations-report-schedules", reviewToken, string(scheduleRaw), 403)
	var schedule store.OperationsReportSchedule
	must(json.Unmarshal(call("POST", "/operations-report-schedules", ownerToken, string(scheduleRaw), 201).Body.Bytes(), &schedule))
	call("POST", fmt.Sprintf("/operations-report-schedules/%d/run", schedule.ID), ownerToken, "", 201)
	call("GET", "/operations-report-schedules", reviewToken, "", 200)
}
