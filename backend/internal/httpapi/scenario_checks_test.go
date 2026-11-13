package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTechnicalScenarioChecksHTTPRemediationAndEvidenceExports(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
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
	slug := fmt.Sprintf("technical-http-%d", time.Now().UnixNano())
	foreignSlug := slug + "-foreign"
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Technical scenario HTTP"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reviewer", "unused"))
	must(repo.Bootstrap(ctx, foreignSlug, "unused", foreignSlug, "Other"))
	owner, err := repo.FindUser(ctx, slug)
	must(err)
	reviewer, err := repo.FindUser(ctx, slug+"-reviewer")
	must(err)
	foreign, err := repo.FindUser(ctx, foreignSlug)
	must(err)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	other, err := repo.Membership(ctx, foreign.ID, foreignSlug)
	must(err)
	var viewerID int64
	must(pool.QueryRow(ctx, `INSERT INTO plume.users(username,password_hash) VALUES($1,'unused') RETURNING id`, slug+"-viewer").Scan(&viewerID))
	_, err = pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role)VALUES($1,$2,'viewer')`, ws.ID, viewerID)
	must(err)
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=ANY($1)`, []int64{ws.ID, other.ID})
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE id=ANY($1)`, []int64{owner.ID, reviewer.ID, foreign.ID, viewerID})
	})
	session := func(id int64) string {
		token, e := sessionToken()
		must(e)
		must(repo.CreateSession(ctx, id, sessionHash(token), time.Now().Add(time.Hour)))
		return token
	}
	ownerCookie, reviewCookie, viewerCookie, foreignCookie := session(owner.ID), session(reviewer.ID), session(viewerID), session(foreign.ID)
	config := store.ManagedPostgresInput{Host: "fixture-db", Port: 5432, Database: "fixture_app", User: "reader", Schemas: []string{"inventory"}, IntervalMinutes: 5, Enabled: true, EncryptedPassword: []byte("never-export-this-credential")}
	job, err := repo.CreateManagedPostgresJob(ctx, ws.ID, owner.ID, "Technical source", sessionHash(slug+"-job"), config)
	must(err)
	table := collector.PostgreSQLTable{Asset: collector.Asset{SourceRef: "inventory.contacts", Name: `=SUM(1,2) <script>fixture()</script>`}, Schema: "inventory", Owner: "source_owner", MetadataStatus: "complete", Columns: []collector.PostgreSQLColumn{{Name: "email", Type: "text", BaseType: "text", Ordinal: 1, CanSelect: true}}, Grants: []collector.PostgreSQLGrant{{Grantee: "PUBLIC", Grantor: "source_owner", Public: true, Privilege: "SELECT", SchemaUsage: true}}, PublicSelect: true, PublicSchemaUsage: true, PublicSelectColumns: []string{}, Warnings: []string{}}
	collect := func(label string) {
		t.Helper()
		must(repo.QueueManagedPostgresJob(ctx, ws.ID, owner.ID, job.ID))
		claim, e := repo.ClaimManagedPostgresJob(ctx, slug+label, time.Minute)
		must(e)
		if claim == nil || claim.JobID != job.ID {
			t.Fatal("fixture source was not claimed")
		}
		_, e = repo.FinishManagedPostgresJob(ctx, *claim, store.AutomationDelivery{Digest: sessionHash(slug + label), ObservedAt: time.Now().UTC(), Assets: []store.CollectedAsset{{SourceRef: table.SourceRef, Name: table.Name}}, PostgresTables: []collector.PostgreSQLTable{table}})
		must(e)
	}
	collect("initial")
	assets, err := repo.ListAssets(ctx, ws.ID, 0, 10)
	must(err)
	if len(assets) != 1 {
		t.Fatal("expected one source asset")
	}
	asset := assets[0]
	controlToken := sessionHash(slug + "-executor")
	_, err = repo.CreateOperationsSource(ctx, ws.ID, owner.ID, store.OperationsSourceInput{Name: "Bound fixture executor", Kind: "postgres_control", OwnerTeam: "Security", JobID: job.ID}, controlToken)
	must(err)
	foreignAsset, err := repo.CreateAsset(ctx, other.ID, "Foreign asset", "Other", "Other")
	must(err)
	handler := New(repo, "https://console.invalid", true)
	base := "/api/v1/workspaces/" + slug
	call := func(method, path string, input any, cookie, origin string, status int) *httptest.ResponseRecorder {
		t.Helper()
		raw, e := json.Marshal(input)
		must(e)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: cookie})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s got %d want %d: %.250s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	request := func(method, path string, input any, cookie string, status int) *httptest.ResponseRecorder {
		return call(method, path, input, cookie, "https://console.invalid", status)
	}
	runsPath := base + "/scenario-check-runs"
	input := store.ScenarioCheckRunInput{Scenario: "domestic", Task: "self-check", AssetID: asset.ID}
	request("POST", runsPath, input, "", 401)
	call("POST", runsPath, input, ownerCookie, "", 403)
	request("POST", runsPath, input, viewerCookie, 403)
	bad := input
	bad.AssetID = foreignAsset.ID
	request("POST", runsPath, bad, ownerCookie, 404)
	request("POST", runsPath, map[string]any{"scenario": "domestic", "task": "self-check", "assetId": asset.ID, "sql": "REVOKE ALL"}, ownerCookie, 400)
	var run store.ScenarioCheckRun
	must(json.Unmarshal(request("POST", runsPath, input, ownerCookie, 201).Body.Bytes(), &run))
	finding := func(r store.ScenarioCheckRun, id string) store.ScenarioCheckFinding {
		for _, f := range r.Findings {
			if f.ID == id {
				return f
			}
		}
		t.Fatalf("missing finding %s", id)
		return store.ScenarioCheckFinding{}
	}
	acl := finding(run, "pg_public_select")
	if acl.Outcome != "non_compliant" || !acl.Remediation.Executable || run.Counts.NonCompliant < 1 || len(run.SnapshotDigest) != 64 || len(run.Evidence) == 0 || len(run.ReportRequirements) == 0 {
		t.Fatal("current positive ACL finding or evidence/report mapping missing")
	}
	if strings.Contains(string(request("GET", runsPath+"/"+fmt.Sprint(run.ID), nil, viewerCookie, 200).Body.Bytes()), "never-export-this-credential") {
		t.Fatal("source credential leaked into run")
	}
	request("GET", runsPath+"/"+fmt.Sprint(run.ID), nil, foreignCookie, 404)
	request("GET", runsPath+fmt.Sprintf("?assetId=%d", foreignAsset.ID), nil, ownerCookie, 404)
	request("GET", runsPath+"?scenario=personal&task=self-check", nil, ownerCookie, 400)
	request("GET", runsPath+"?scenario=domestic&scenario=personal", nil, ownerCookie, 400)
	var history struct {
		Items []store.ScenarioCheckRun `json:"items"`
	}
	must(json.Unmarshal(request("GET", runsPath+fmt.Sprintf("?scenario=domestic&task=self-check&assetId=%d", asset.ID), nil, viewerCookie, 200).Body.Bytes(), &history))
	if len(history.Items) != 1 || history.Items[0].ID != run.ID {
		t.Fatal("history lost exact source scope")
	}
	input.PreviousRunID = &run.ID
	var repeated store.ScenarioCheckRun
	must(json.Unmarshal(request("POST", runsPath, input, ownerCookie, 201).Body.Bytes(), &repeated))
	if repeated.RetestState != "same_source_observation" || finding(repeated, "pg_public_select").Outcome != "non_compliant" {
		t.Fatal("rerunning old evidence was described as a fresh successful retest")
	}
	remediationPath := runsPath + "/" + fmt.Sprint(run.ID) + "/findings/pg_public_select/remediation"
	proposal := map[string]any{"reason": "Synthetic approved access-control baseline", "durationMinutes": 10}
	request("POST", remediationPath, proposal, viewerCookie, 403)
	request("POST", remediationPath, map[string]any{"reason": "Unsafe target override", "durationMinutes": 10, "role": "source_owner"}, ownerCookie, 400)
	var action store.ControlAction
	must(json.Unmarshal(request("POST", remediationPath, proposal, ownerCookie, 201).Body.Bytes(), &action))
	if action.Status != "proposed" || action.Kind != "revoke_select" || action.Spec.Role != "PUBLIC" || action.CheckID == nil {
		t.Fatal("repair bypassed proposal approval or was not bound to a concrete check")
	}
	var duplicate store.ControlAction
	must(json.Unmarshal(request("POST", remediationPath, proposal, ownerCookie, 201).Body.Bytes(), &duplicate))
	if duplicate.ID != action.ID {
		t.Fatal("retry created a duplicate repair action")
	}
	approvePath := base + "/operations/controls/" + fmt.Sprint(action.ID) + "/approve"
	request("POST", approvePath, map[string]any{"revision": action.Revision}, ownerCookie, 403)
	must(json.Unmarshal(request("POST", approvePath, map[string]any{"revision": action.Revision}, reviewCookie, 200).Body.Bytes(), &action))
	command, err := repo.ClaimPostgresControl(ctx, controlToken, "fixture-lease")
	must(err)
	if command == nil || command.ID != action.ID {
		t.Fatal("bound source could not claim approved repair")
	}
	had, has := true, false
	must(repo.RecordControlReceipt(ctx, "postgres_control", controlToken, store.ControlReceipt{ActionID: command.ID, Revision: command.Revision, Lease: command.Lease, Result: store.ControlResult{Phase: "apply", Success: true, Code: "select_revoked", HadSelect: &had, HasSelect: &has, ObservedAt: time.Now().UTC()}}))
	var unchanged store.ScenarioCheckRun
	must(json.Unmarshal(request("GET", runsPath+"/"+fmt.Sprint(run.ID), nil, viewerCookie, 200).Body.Bytes(), &unchanged))
	if unchanged.SnapshotDigest != run.SnapshotDigest || finding(unchanged, "pg_public_select").Outcome != "non_compliant" || len(unchanged.Actions) != 1 {
		t.Fatal("repair receipt rewrote frozen inspection result")
	}
	// The report exports the original inspection and current linked action
	// capture separately. HTML is escaped, CSV remains literal, ZIP is hashed.
	exportPath := runsPath + "/" + fmt.Sprint(run.ID) + "/export?format="
	request("GET", exportPath+"zip", nil, foreignCookie, 404)
	request("GET", exportPath+"pdf", nil, ownerCookie, 400)
	html := request("GET", exportPath+"html", nil, viewerCookie, 200)
	if strings.Contains(html.Body.String(), "<script>fixture()</script>") || !strings.Contains(html.Body.String(), "&lt;script&gt;fixture()&lt;/script&gt;") || !strings.Contains(html.Body.String(), "监管与检查报告要求对照") {
		t.Fatal("HTML evidence report was unsafe or omitted report requirements")
	}
	var report scenarioCheckReport
	must(json.Unmarshal(request("GET", exportPath+"json", nil, viewerCookie, 200).Body.Bytes(), &report))
	if report.Run.SnapshotDigest != run.SnapshotDigest || len(report.Run.Actions) != 0 || len(report.LinkedActions) != 1 {
		t.Fatal("report mixed live actions into original run snapshot")
	}
	csvResponse := request("GET", exportPath+"csv", nil, viewerCookie, 200)
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(csvResponse.Body.String(), "\ufeff"))).ReadAll()
	must(err)
	if len(rows) < 2 || !strings.HasPrefix(rows[1][1], "'=SUM") {
		t.Fatal("CSV evidence names were interpreted as formulas")
	}
	zipResponse := request("GET", exportPath+"zip", nil, viewerCookie, 200)
	archive, err := zip.NewReader(bytes.NewReader(zipResponse.Body.Bytes()), int64(zipResponse.Body.Len()))
	must(err)
	files := map[string][]byte{}
	for _, file := range archive.File {
		r, e := file.Open()
		must(e)
		raw, e := io.ReadAll(r)
		must(e)
		must(r.Close())
		files[file.Name] = raw
		if strings.Contains(string(raw), "never-export-this-credential") {
			t.Fatal("credential leaked into evidence package")
		}
	}
	var manifest struct {
		Files             []scenarioEvidenceFile `json:"files"`
		RunSnapshotDigest string                 `json:"runSnapshotDigest"`
	}
	must(json.Unmarshal(files["manifest.json"], &manifest))
	if len(manifest.Files) < 5 || manifest.RunSnapshotDigest != run.SnapshotDigest {
		t.Fatal("evidence package manifest missing")
	}
	for _, file := range manifest.Files {
		raw, exists := files[file.Name]
		if !exists {
			t.Fatal("manifest file missing")
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != file.SHA256 || len(raw) != file.Bytes {
			t.Fatal("evidence package digest mismatch")
		}
	}
	// A genuinely newer successful source observation can establish the narrow
	// baseline result. The complete protection/processing conditions stay unknown.
	table.PublicSelect = false
	table.Grants = []collector.PostgreSQLGrant{}
	collect("after-repair")
	var retest store.ScenarioCheckRun
	must(json.Unmarshal(request("POST", runsPath, input, ownerCookie, 201).Body.Bytes(), &retest))
	if retest.RetestState != "new_source_observation" || finding(retest, "pg_public_select").Outcome != "compliant" || finding(retest, "protection_coverage").Outcome != "needs_verification" {
		t.Fatal("new source retest did not retain narrow technical coverage")
	}
	confirmedHTML := request("GET", runsPath+"/"+fmt.Sprint(retest.ID)+"/export?format=html", nil, ownerCookie, 200).Body.String()
	if !strings.Contains(confirmedHTML, "本项技术检查符合，无需发起修复。") {
		t.Fatal("compliant report still suggests a missing remediation capability")
	}
	_, err = pool.Exec(ctx, `UPDATE plume.postgres_asset_inspections SET source_revision=NULL WHERE asset_id=$1`, asset.ID)
	must(err)
	var legacy store.ScenarioCheckRun
	input.PreviousRunID = &retest.ID
	must(json.Unmarshal(request("POST", runsPath, input, ownerCookie, 201).Body.Bytes(), &legacy))
	if finding(legacy, "pg_public_select").Outcome != "needs_verification" {
		t.Fatal("source snapshot without configuration revision was treated as compliant")
	}
}
