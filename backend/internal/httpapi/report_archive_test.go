package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestPostgreSQLAutomaticReportArchives(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("could not configure test database")
	}
	t.Cleanup(pool.Close)
	repo := store.New(pool)
	checkErr := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	checkErr(repo.Migrate(ctx))

	slug := fmt.Sprintf("archive-%d", time.Now().UnixNano())
	otherSlug, username := slug+"-other", slug+"-owner"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM plume.workspaces WHERE slug = ANY($1)", []string{slug, otherSlug}); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM plume.users WHERE username = $1", username); err != nil {
			t.Error(err)
		}
	})
	checkErr(repo.Bootstrap(ctx, username, "unused", slug, "Archive fixtures"))
	checkErr(repo.Bootstrap(ctx, username, "unused", otherSlug, "Other workspace"))
	owner, err := repo.FindUser(ctx, username)
	checkErr(err)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	checkErr(err)
	digest := func(input string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(input))) }
	cfg := store.ManagedPostgresInput{
		Host: "sample-db", Port: 5432, Database: "sample", User: "reader", Schemas: []string{"inventory"},
		IntervalMinutes: 5, AutoChecks: true, AutoReports: true, EncryptedPassword: []byte("ciphertext-fixture"),
	}
	job, err := repo.CreateManagedPostgresJob(ctx, workspace.ID, owner.ID, "Catalog <sample>", digest(slug), cfg)
	checkErr(err)
	table := collector.PostgreSQLTable{
		Asset:  collector.Asset{SourceRef: "inventory.customers", Name: "customers"},
		Schema: "inventory", Owner: "source_admin", PublicSchemaUsage: true,
		Columns: []collector.PostgreSQLColumn{{Name: "email", Type: "text", Ordinal: 1, Sensitivity: "email"}},
		Grants: []collector.PostgreSQLGrant{
			{Grantee: "PUBLIC", Grantor: "source_admin", Public: true, Privilege: "SELECT", Column: "email", SchemaUsage: true},
		},
		PublicSelectColumns: []string{"email"},
	}
	collect := func(runID string) store.AutomationRun {
		t.Helper()
		checkErr(repo.QueueManagedPostgresJob(ctx, workspace.ID, owner.ID, job.ID))
		claim, err := repo.ClaimManagedPostgresJob(ctx, runID, time.Minute)
		checkErr(err)
		if claim == nil || claim.JobID != job.ID {
			t.Fatalf("expected own managed job, got %+v", claim)
		}
		run, err := repo.FinishManagedPostgresJob(ctx, *claim, store.AutomationDelivery{
			Digest: digest(runID), ObservedAt: time.Now(),
			Assets:         []store.CollectedAsset{{SourceRef: table.SourceRef, Name: table.Name}},
			PostgresTables: []collector.PostgreSQLTable{table},
		})
		checkErr(err)
		return run
	}
	token, err := sessionToken()
	checkErr(err)
	checkErr(repo.CreateSession(ctx, owner.ID, sessionHash(token), time.Now().Add(time.Hour)))
	handler := New(repo, "https://example.invalid", true)
	pathFor := func(slug string) string { return "/api/v1/workspaces/" + slug + "/reports/archives" }
	path := pathFor(slug)
	get := func(path string, authenticated bool, expected int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if authenticated {
			req.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: token})
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != expected || res.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s returned %d, want %d: %.500s", path, res.Code, expected, res.Body.String())
		}
		return res
	}
	first := collect("first")
	firstPath := fmt.Sprintf("%s/%d", path, first.ID)
	get(path, false, 401)
	get(fmt.Sprintf("%s/%d", pathFor(otherSlug), first.ID), true, 404)
	get(path+"/invalid", true, 400)
	get(firstPath+"/export?format=csv", true, 400)

	var initial store.ComplianceReport
	original := get(firstPath, true, 200).Body.String()
	checkErr(json.Unmarshal([]byte(original), &initial))
	if initial.Scope.Source == nil || initial.Scope.Source.RunID != first.RunID || initial.Counts.Draft != 2 ||
		initial.Counts.Evidence != 2 || initial.Identification == nil || initial.Identification.SensitiveCandidates != 1 ||
		initial.Identification.PublicColumnSelect != 1 || len(initial.Checks) != 2 {
		t.Fatalf("archive omitted run, automatic checks or identification: %+v", initial)
	}
	var classification store.ReportCheck
	for _, check := range initial.Checks {
		if check.AutomaticEvaluation == nil || check.AutomaticEvaluation.Assessment != "review_required" {
			t.Fatalf("missing assessment: %+v", check)
		}
		if check.Check.Requirement.Code == "G02" {
			classification = check
		}
	}
	if classification.Check.ID == 0 {
		t.Fatal("archive omitted the classification check")
	}
	checkErr(repo.SubmitCheck(ctx, workspace.ID, owner.ID, classification.Check.ID))
	table.Columns[0].Sensitivity = ""
	second := collect("second")
	var updated store.ComplianceReport
	checkErr(json.Unmarshal(get(fmt.Sprintf("%s/%d", path, second.ID), true, 200).Body.Bytes(), &updated))
	for _, entry := range updated.Checks {
		if entry.Check.ID == classification.Check.ID && (entry.AutomaticEvaluation == nil ||
			entry.AutomaticEvaluation.Assessment != "no_signal" || entry.Check.Status != "in_review" ||
			len(entry.Evidence) != 1 || entry.Evidence[0].AutomationRunID != "first") {
			t.Fatalf("current evaluation was conflated with review or historical evidence: %+v", entry)
		}
	}
	if updated.Identification == nil || updated.Identification.SensitiveCandidates != 0 ||
		get(firstPath+"/export?format=json", true, 200).Body.String() != original {
		t.Fatal("archive changed after another run")
	}
	html := get(firstPath+"/export?format=html", true, 200)
	if !strings.HasSuffix(html.Header().Get("Content-Disposition"), ".html\"") ||
		!strings.Contains(html.Body.String(), "本次自动判定") ||
		!strings.Contains(html.Body.String(), "Catalog &lt;sample&gt;") {
		t.Fatal("automatic report HTML failed to render scope and rule output safely")
	}

	_, err = pool.Exec(ctx, `
		WITH extra_checks AS (
			INSERT INTO plume.checks (workspace_id, asset_id, requirement_code, owner_team, note, status, created_by)
			SELECT $1, $2, 'G02', 'Data', 'Scope limit fixture', 'in_review', $3
			FROM generate_series(1, $4::int) RETURNING id
		)
		INSERT INTO plume.postgres_check_bindings (check_id, job_id, source_host, source_port, source_database)
		SELECT id, $5, $6, $7, $8 FROM extra_checks
	`, workspace.ID, classification.Check.AssetID, owner.ID, store.ReportCheckLimit, job.ID, cfg.Host, cfg.Port, cfg.Database)
	checkErr(err)
	tooLarge := collect("too-large")
	latest, err := repo.GetAssetPostgresInspection(ctx, workspace.ID, owner.ID, classification.Check.AssetID)
	checkErr(err)
	if latest.RunID != tooLarge.RunID || tooLarge.Status != "success" {
		t.Fatal("report generation failure rolled back collection")
	}
	for i := 0; i < store.ReportArchiveRetentionPerJob-2; i++ {
		collect(fmt.Sprintf("retention-%d", i))
	}
	var list struct {
		Items           []store.ReportArchive `json:"items"`
		RetentionPerJob int                   `json:"retentionPerJob"`
	}
	checkErr(json.Unmarshal(get(path, true, 200).Body.Bytes(), &list))
	if len(list.Items) != store.ReportArchiveRetentionPerJob || list.RetentionPerJob != store.ReportArchiveRetentionPerJob ||
		list.Items[0].Status != "failed" || list.Items[0].Code != "scope_too_large" || list.Items[0].Counts != nil ||
		list.Items[0].Identification != nil || list.Items[len(list.Items)-1].ID != second.ID ||
		list.Items[len(list.Items)-1].Counts == nil || list.Items[len(list.Items)-1].Identification == nil {
		t.Fatalf("unexpected archive retention or failure state: %+v", list)
	}
	get(fmt.Sprintf("%s/%d", path, tooLarge.ID), true, 422)
	get(firstPath, true, 404)
}
