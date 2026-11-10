package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgreSQLComplianceReport(t *testing.T) {
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
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := fmt.Sprint(time.Now().UnixNano())
	slug, foreignSlug := "report-"+suffix, "report-foreign-"+suffix
	ownerName, reviewerName, foreignName := "owner-"+suffix, "reviewer-"+suffix, "foreign-"+suffix
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM plume.workspaces WHERE slug = ANY($1)", []string{slug, foreignSlug}); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM plume.users WHERE username = ANY($1)", []string{ownerName, reviewerName, foreignName}); err != nil {
			t.Error(err)
		}
	})
	const passwordHash = "fixture-without-password-login"
	if err := repo.Bootstrap(ctx, ownerName, passwordHash, slug, "审计工作区"); err != nil {
		t.Fatal(err)
	}
	if err := repo.ProvisionReviewer(ctx, slug, reviewerName, passwordHash); err != nil {
		t.Fatal(err)
	}
	if err := repo.Bootstrap(ctx, foreignName, passwordHash, foreignSlug, "其他工作区"); err != nil {
		t.Fatal(err)
	}
	owner, err := repo.FindUser(ctx, ownerName)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := repo.FindUser(ctx, reviewerName)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := repo.FindUser(ctx, foreignName)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	if err != nil {
		t.Fatal(err)
	}
	foreignWorkspace, err := repo.Membership(ctx, foreign.ID, foreignSlug)
	if err != nil {
		t.Fatal(err)
	}
	activityFor := func(workspace store.Workspace, actor store.User, name string) store.Activity {
		t.Helper()
		asset, err := repo.CreateAsset(ctx, workspace.ID, name+"资产", "源系统", "团队")
		if err != nil {
			t.Fatal(err)
		}
		activity, err := repo.CreateActivity(ctx, workspace.ID, actor.ID, store.ActivityInput{
			AssetID: asset.ID, Name: name, Kind: "sharing", Purpose: "测试共享流程", Recipient: "测试接收方",
		})
		if err != nil {
			t.Fatal(err)
		}
		return activity
	}
	activity := activityFor(workspace, owner, "共享事项")
	otherActivity := activityFor(foreignWorkspace, foreign, "其他工作区的活动")
	template := store.TemplateInput{
		Key: "test-sharing", Title: "共享模板初版", Kind: "sharing", Items: []store.TemplateItem{{
			Key: "test", Title: "约定接收范围", RequirementCode: "T01", Applicability: "向供应商共享",
			EvidenceRequirements: "登记合同及配置引用", AcceptanceCriteria: "权限与约定一致",
		}},
	}
	version, err := repo.PublishTemplate(ctx, workspace.ID, owner.ID, template)
	if err != nil {
		t.Fatal(err)
	}
	newCheck := func(linked bool, note string) store.Check {
		t.Helper()
		input := store.CheckInput{AssetID: activity.AssetID, RequirementCode: "T01", OwnerTeam: "团队", Note: note}
		if linked {
			input.ActivityID = &activity.ID
			input.TemplateRevisionID, input.TemplateItemKey = &version.ID, "test"
		}
		check, err := repo.CreateCheck(ctx, workspace.ID, owner.ID, input)
		if err != nil {
			t.Fatal(err)
		}
		return check
	}
	addEvidence := func(checkID int64, kind, summary string) {
		t.Helper()
		var now time.Time
		if err := pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
			t.Fatal(err)
		}
		if err := repo.AddEvidence(ctx, workspace.ID, owner.ID, checkID, store.EvidenceInput{
			Kind: kind, SourceSystem: "源系统", SourceRecord: "ref-" + kind, Summary: summary, ObservedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	submit := func(checkID int64) {
		t.Helper()
		if err := repo.SubmitCheck(ctx, workspace.ID, owner.ID, checkID); err != nil {
			t.Fatal(err)
		}
	}
	review := func(checkID int64, decision, note string) {
		t.Helper()
		detail, err := repo.CheckDetail(ctx, workspace.ID, reviewer.ID, checkID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.ReviewCheck(ctx, workspace.ID, reviewer.ID, checkID, detail.ReviewRevision, decision, note); err != nil {
			t.Fatal(err)
		}
	}

	newCheck(false, "手工待取证")
	pending := newCheck(false, "手工待复核")
	addEvidence(pending.ID, "observation", "待复核取证")
	submit(pending.ID)
	needsWork := newCheck(false, "手工待整改")
	addEvidence(needsWork.ID, "observation", "缺少批准")
	submit(needsWork.ID)
	review(needsWork.ID, "changes", "补齐批准凭据")

	verified := newCheck(true, `<script>alert("unsafe")</script>`)
	template.BaseRevisionID = &version.ID
	template.Title = "后续模板版本"
	if _, err := repo.PublishTemplate(ctx, workspace.ID, owner.ID, template); err != nil {
		t.Fatal(err)
	}
	addEvidence(verified.ID, "observation", "发现越权")
	submit(verified.ID)
	review(verified.ID, "changes", "撤回越权授权")
	addEvidence(verified.ID, "remediation", "完成撤权")
	addEvidence(verified.ID, "retest", "未授权访问已被拒绝")
	submit(verified.ID)
	review(verified.ID, "verified", "独立复核通过")

	token, err := sessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateSession(ctx, reviewer.ID, sessionHash(token), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	handler := New(repo, "https://example.invalid", true)
	path := "/api/v1/workspaces/" + slug + "/reports/compliance"
	get := func(path string, authenticated bool, expected int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: token})
		}
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, r)
		if result.Code != expected || result.Header().Get("Cache-Control") != "no-store" {
			body := result.Body.String()
			t.Fatalf("GET %s returned %d, want %d: %.500s", path, result.Code, expected, body)
		}
		return result
	}
	get(path, false, 401)
	get(fmt.Sprintf("%s?activityId=%d", path, otherActivity.ID), true, 404)
	get(path+"?activityId=invalid", true, 400)
	get(path+"/export?format=csv", true, 400)

	result := get(path, true, 200)
	var report store.ComplianceReport
	if err := json.Unmarshal(result.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	wantCounts := store.ReportCounts{Total: 4, Draft: 1, InReview: 1, NeedsWork: 1, Verified: 1, Templated: 1,
		Evidence: 5, Remediations: 1, Retests: 1, Reviews: 3}
	if report.Counts != wantCounts || len(report.Checks) != 4 || report.Scope.ActivityID != 0 {
		t.Fatalf("report counts or scope mismatch: %+v, %+v", report.Counts, report.Scope)
	}
	item := report.Checks[0]
	if item.Check.ID != verified.ID || item.Check.Template == nil || item.Check.Template.Version != 1 ||
		item.Check.Template.Item.AcceptanceCriteria != "权限与约定一致" || len(item.Evidence) != 3 ||
		item.LatestReview == nil || item.LatestReview.Actor != reviewerName || item.LatestReview.Note != "独立复核通过" {
		t.Fatalf("report lost pinned template, evidence or review: %+v", item)
	}

	query := fmt.Sprintf("&activityId=%d", activity.ID)
	scopedJSON := get(path+"/export?format=json"+query, true, 200)
	var scoped store.ComplianceReport
	if err := json.Unmarshal(scopedJSON.Body.Bytes(), &scoped); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(scopedJSON.Header().Get("Content-Disposition"), ".json\"") ||
		scoped.Scope.Activity == nil || scoped.Scope.Activity.Recipient != "测试接收方" || scoped.Counts.Total != 1 {
		t.Fatalf("unexpected scoped export: %+v", scoped)
	}
	html := get(path+"/export?format=html"+query, true, 200)
	if !strings.HasSuffix(html.Header().Get("Content-Disposition"), ".html\"") ||
		!strings.HasPrefix(html.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(html.Header().Get("Content-Security-Policy"), "default-src 'none'") ||
		!strings.Contains(html.Body.String(), "&lt;script&gt;") ||
		strings.Contains(html.Body.String(), "<script>") ||
		!strings.Contains(html.Body.String(), "共享模板初版") ||
		strings.Contains(html.Body.String(), "后续模板版本") {
		t.Fatal("HTML export did not preserve safe rendering or pinned version")
	}

	// Exercise both scope limits without adding many individual HTTP requests.
	if _, err := pool.Exec(ctx, `
		INSERT INTO plume.check_events (check_id, actor_id, action)
		SELECT $1, $2, 'evidence' FROM generate_series(1, $3::int)
	`, verified.ID, owner.ID, store.ReportTimelineLimit); err != nil {
		t.Fatal(err)
	}
	get(path+"/export?format=html"+query, true, 422)
	if _, err := pool.Exec(ctx, `
		INSERT INTO plume.checks (workspace_id, asset_id, requirement_code, owner_team, note, created_by)
		SELECT $1, $2, 'T01', '团队', '限制测试', $3 FROM generate_series(1, $4::int)
	`, workspace.ID, activity.AssetID, owner.ID, store.ReportCheckLimit); err != nil {
		t.Fatal(err)
	}
	get(path, true, 422)
}
