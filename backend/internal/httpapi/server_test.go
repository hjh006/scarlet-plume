package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/managed"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestManifestValidation(t *testing.T) {
	base := manifest{Version: 1, Provider: "postgresql", Source: "CRM", BatchID: "batch-1", Assets: []store.CollectedAsset{
		{SourceRef: "public.b", Name: "B"}, {SourceRef: "public.a", Name: "A"},
	}}
	first, ok := base.digest()
	if !ok {
		t.Fatal("valid snapshot rejected")
	}
	base.Assets[0], base.Assets[1] = base.Assets[1], base.Assets[0]
	if reordered, ok := base.digest(); !ok || first != reordered {
		t.Fatal("identical snapshot with reordered rows has a different digest")
	}
	base.Assets = append(base.Assets, base.Assets[0])
	if _, ok := base.digest(); ok {
		t.Fatal("duplicate asset reference accepted")
	}
	base.Assets = nil
	if _, ok := base.digest(); ok {
		t.Fatal("missing asset array accepted")
	}
	base.Assets = []store.CollectedAsset{}
	if _, ok := base.digest(); !ok {
		t.Fatal("empty snapshot rejected")
	}
}

func TestUnauthenticatedAndCrossOrigin(t *testing.T) {
	handler := New(nil, "https://example.invalid", true)
	for _, tc := range []struct {
		method string
		origin string
		path   string
		want   int
	}{
		{"GET", "", "/api/v1/session", 401},
		{"POST", "https://other.invalid", "/api/v1/session", 403},
		{"DELETE", "", "/api/v1/session", 403},
		{"POST", "https://example.invalid", "/api/v1/workspaces/demo/imports", 401},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Origin", tc.origin)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.want || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s returned %d, expected %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
}

func TestPostgreSQLWorkflow(t *testing.T) {
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
	owner, other, reviewer := "web-owner-"+suffix, "web-other-"+suffix, "web-reviewer-"+suffix
	a, b := "web-a-"+suffix, "web-b-"+suffix
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM plume.workspaces WHERE slug = ANY($1)", []string{a, b}); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM plume.users WHERE username = ANY($1)", []string{owner, other, reviewer}); err != nil {
			t.Error(err)
		}
	})
	password := "test-only-passphrase"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Bootstrap(ctx, owner, hash, a, "A"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Bootstrap(ctx, other, hash, b, "B"); err != nil {
		t.Fatal(err)
	}
	const origin = "https://example.invalid"
	manager, err := managed.New(repo, strings.Repeat("01", 32), "managed.example", false)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithManaged(repo, origin, true, manager)
	do := func(method, path, body string, cookie *http.Cookie, expected int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != expected {
			t.Fatalf("%s %s returned %d, want %d: %s", method, path, w.Code, expected, w.Body)
		}
		return w
	}
	session := "/api/v1/session"
	do("POST", session, fmt.Sprintf(`{"username":%q,"password":"bad"}`, owner), nil, 401)
	login := do("POST", session, fmt.Sprintf(`{"username":%q,"password":%q}`, owner, password), nil, 204)
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly ||
		cookies[0].Name != "__Host-scarlet-session" || cookies[0].Path != "/" {
		t.Fatal("invalid session cookie")
	}
	cookie := cookies[0]
	do("GET", session, "", cookie, 200)
	otherWorkspace := "/api/v1/workspaces/" + b
	do("GET", otherWorkspace+"/assets", "", cookie, 404)
	do("POST", otherWorkspace+"/imports", "{}", cookie, 404)

	workspace := "/api/v1/workspaces/" + a
	importPath := workspace + "/imports"
	first := `{"version":1,"provider":"postgresql","source":"CRM","batchId":"first","assets":[{"sourceRef":"public.orders","name":"orders"}]}`
	firstResult := do("POST", importPath, first, cookie, 200)
	retry := do("POST", importPath, first, cookie, 200)
	var original, replayed store.ImportRun
	if json.Unmarshal(firstResult.Body.Bytes(), &original) != nil ||
		json.Unmarshal(retry.Body.Bytes(), &replayed) != nil ||
		original.Replayed || !replayed.Replayed {
		t.Fatal("responses did not distinguish a replay")
	}
	original.Replayed = true
	if original != replayed {
		t.Fatal("same batch did not replay the original result")
	}
	do("POST", importPath, `{"version":1,"provider":"postgresql","source":"CRM","batchId":"first","assets":[{"sourceRef":"public.orders","name":"different"}]}`, cookie, 409)
	do("POST", importPath, `{"version":1,"provider":"postgresql","source":"CRM","batchId":"new","assets":[{"sourceRef":"public.orders","name":"orders","sample":"raw"}]}`, cookie, 400)

	rec := do("GET", workspace+"/assets", "", cookie, 200)
	var page struct{ Items []store.Asset }
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil ||
		len(page.Items) != 1 || page.Items[0].SourceRef != "public.orders" {
		t.Fatalf("unexpected asset page: %s", rec.Body)
	}
	if err := repo.ProvisionReviewer(ctx, a, reviewer, hash); err != nil {
		t.Fatal(err)
	}
	if err := repo.ProvisionReviewer(ctx, a, owner, hash); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("reviewer provision unexpectedly accepted the workspace owner: %v", err)
	}
	reviewerLogin := do("POST", session, fmt.Sprintf(`{"username":%q,"password":%q}`, reviewer, password), nil, 204)
	reviewerCookie := reviewerLogin.Result().Cookies()[0]
	checks := workspace + "/checks"
	target := strings.Repeat("a", 64)
	makeCheck := fmt.Sprintf(`{"assetId":%d,"requirementCode":"G03","ownerTeam":"QA","note":"指定直接协作者","feishuTargetDigest":%q}`, page.Items[0].ID, target)
	do("GET", workspace+"/requirements", "", cookie, 200)
	do("POST", checks, makeCheck, reviewerCookie, 403)
	created := do("POST", checks, makeCheck, cookie, 201)
	var check store.Check
	if err := json.Unmarshal(created.Body.Bytes(), &check); err != nil || check.ID < 1 {
		t.Fatalf("check was not created: %s", created.Body)
	}
	path := fmt.Sprintf("%s/%d", checks, check.ID)
	do("GET", fmt.Sprintf("%s/checks/%d/export", otherWorkspace, check.ID), "", cookie, 404)
	do("POST", path+"/submit", "{}", cookie, 409)

	snapshot := func(target string, present bool, observedAt time.Time) string {
		permission := ""
		if present {
			permission = "edit"
		}
		return fmt.Sprintf(`{"version":1,"targetDigest":%q,"observedAt":%q,"present":%t,"permission":%q}`,
			target, observedAt.UTC().Format(time.RFC3339Nano), present, permission)
	}
	readDetail := func() store.CheckDetail {
		t.Helper()
		reply := do("GET", path, "", reviewerCookie, 200)
		var detail store.CheckDetail
		if err := json.Unmarshal(reply.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		return detail
	}
	baseline := snapshot(target, true, time.Now())
	do("POST", path+"/feishu", snapshot(strings.Repeat("b", 64), true, time.Now()), cookie, 400)
	do("POST", path+"/feishu", snapshot(target, true, time.Now().Add(time.Second)), cookie, 400)
	do("POST", path+"/feishu", baseline, cookie, 204)
	do("POST", path+"/feishu", baseline, cookie, 409)
	do("POST", path+"/submit", "{}", cookie, 204)
	detail := readDetail()
	if !detail.CanReview || detail.CanVerify || detail.ReviewRevision < 1 {
		t.Fatalf("unexpected permissions for a still-present grant: %+v", detail)
	}
	review := func(revision int64, decision string) string {
		return fmt.Sprintf(`{"reviewRevision":%d,"decision":%q,"note":"核对测试协作者的直接权限"}`, revision, decision)
	}
	firstRevision := detail.ReviewRevision
	do("POST", path+"/review", review(firstRevision, "changes"), cookie, 403)
	do("POST", path+"/review", review(firstRevision, "verified"), reviewerCookie, 409)
	do("POST", path+"/review", review(firstRevision, "changes"), reviewerCookie, 204)
	do("POST", path+"/submit", "{}", cookie, 409)
	remediation := fmt.Sprintf(`{"kind":"remediation","sourceSystem":"飞书测试夹具","sourceRecord":"remediation-test","summary":"模拟移除指定协作者的直接授权","observedAt":%q}`,
		time.Now().UTC().Format(time.RFC3339Nano))
	do("POST", path+"/evidence", remediation, cookie, 204)
	do("POST", path+"/feishu", snapshot(target, false, time.Now()), cookie, 204)
	do("POST", path+"/submit", "{}", cookie, 204)
	detail = readDetail()
	if !detail.CanReview || !detail.CanVerify || detail.ReviewRevision <= firstRevision {
		t.Fatalf("unexpected permissions for an absent grant: %+v", detail)
	}
	do("POST", path+"/review", review(firstRevision, "verified"), reviewerCookie, 409)
	do("POST", path+"/review", review(detail.ReviewRevision, "verified"), reviewerCookie, 204)
	detail = readDetail()
	if detail.Status != "verified" || len(detail.Evidence) != 3 {
		t.Fatalf("unexpected verified check: %+v", detail)
	}
	export := do("GET", path+"/export", "", reviewerCookie, 200)
	if !strings.Contains(export.Header().Get("Content-Disposition"), "attachment") ||
		!strings.Contains(export.Body.String(), `"memberPresent":false`) ||
		!strings.Contains(export.Body.String(), `"action":"verified"`) {
		t.Fatalf("invalid audit export: %s", export.Body)
	}
	activities := workspace + "/activities"
	for _, scenario := range []struct {
		kind        string
		rule        string
		templateKey string
	}{
		{"sharing", "T01", "sharing-baseline"},
		{"cross_border", "T03", "cross-border-baseline"},
		{"ai", "X01", "ai-use-baseline"},
	} {
		input := fmt.Sprintf(`{"assetId":%d,"name":%q,"kind":%q,"purpose":"核验处理边界","recipient":"测试接收方","region":"测试地域","retentionPolicy":"按审批期限"}`,
			page.Items[0].ID, scenario.kind, scenario.kind)
		do("POST", activities, input, reviewerCookie, 403)
		createdActivity := do("POST", activities, input, cookie, 201)
		var activity store.Activity
		if err := json.Unmarshal(createdActivity.Body.Bytes(), &activity); err != nil || activity.ID < 1 {
			t.Fatalf("activity was not created: %s", createdActivity.Body)
		}
		do("GET", fmt.Sprintf("%s/%d", activities, activity.ID), "", reviewerCookie, 200)
		do("GET", fmt.Sprintf("%s/activities/%d", otherWorkspace, activity.ID), "", cookie, 404)
		templateResponse := do("GET", workspace+"/templates?kind="+scenario.kind, "", reviewerCookie, 200)
		var library struct{ Items []store.Template }
		if err := json.Unmarshal(templateResponse.Body.Bytes(), &library); err != nil {
			t.Fatalf("invalid template list: %s", templateResponse.Body)
		}
		var template store.Template
		for _, candidate := range library.Items {
			if candidate.Key == scenario.templateKey && candidate.BuiltIn {
				template = candidate
				break
			}
		}
		if template.ID == 0 || len(template.Items) < 3 {
			t.Fatalf("expected builtin scenario template: %s", templateResponse.Body)
		}
		item := template.Items[0]
		reply := do("POST", checks, fmt.Sprintf(`{"assetId":%d,"activityId":%d,"requirementCode":%q,"ownerTeam":"QA","note":"核对证据引用","templateRevisionId":%d,"templateItemKey":%q}`,
			activity.AssetID, activity.ID, scenario.rule, template.ID, item.Key), cookie, 201)
		var linked store.Check
		if err := json.Unmarshal(reply.Body.Bytes(), &linked); err != nil || linked.Activity == nil || linked.Activity.ID != activity.ID ||
			linked.Template == nil || linked.Template.RevisionID != template.ID || linked.Template.Item != item {
			t.Fatalf("activity link was not returned: %s", reply.Body)
		}
		result := do("GET", fmt.Sprintf("%s?activityId=%d&status=draft", checks, activity.ID), "", reviewerCookie, 200)
		var scoped struct{ Items []store.Check }
		if err := json.Unmarshal(result.Body.Bytes(), &scoped); err != nil || len(scoped.Items) != 1 || scoped.Items[0].ID != linked.ID {
			t.Fatalf("unexpected linked check list: %s", result.Body)
		}
		download := do("GET", fmt.Sprintf("%s/%d/export", checks, linked.ID), "", reviewerCookie, 200)
		var exported struct{ Activity store.Activity }
		if err := json.Unmarshal(download.Body.Bytes(), &exported); err != nil ||
			exported.Activity.ID != activity.ID || exported.Activity.Recipient != "测试接收方" {
			t.Fatalf("activity was not preserved in export: %s", download.Body)
		}
	}
	const customTemplate = `{"key":"workspace-ai","title":"工作区 AI 模板","kind":"ai","items":[{"key":"policy","title":"模型网关策略","requirementCode":"G03","applicability":"存在模型网关","evidenceRequirements":"策略版本和测试编号","acceptanceCriteria":"受限请求被拒绝"}]}`
	do("POST", workspace+"/templates", customTemplate, reviewerCookie, 403)
	published := do("POST", workspace+"/templates", customTemplate, cookie, 201)
	var version store.Template
	if err := json.Unmarshal(published.Body.Bytes(), &version); err != nil || version.Version != 1 || version.BuiltIn {
		t.Fatalf("custom template publication failed: %s", published.Body)
	}
	do("GET", fmt.Sprintf("%s/templates/%d", workspace, version.ID), "", reviewerCookie, 200)
	do("POST", workspace+"/templates", customTemplate, cookie, 409)
	do("GET", activities+"?kind=invalid", "", cookie, 400)
	do("GET", checks+"?activityId=-1", "", cookie, 400)
	jobs := workspace + "/automation/jobs"
	const jobInput = `{"provider":"postgresql","source":"定时目录"}`
	do("POST", jobs, jobInput, reviewerCookie, 403)
	jobResponse := do("POST", jobs, jobInput, cookie, 201)
	var createdJob struct {
		Job   store.AutomationJob
		Token string
	}
	if err := json.Unmarshal(jobResponse.Body.Bytes(), &createdJob); err != nil ||
		createdJob.Job.ID == 0 || !strings.HasPrefix(createdJob.Token, "spl_") {
		t.Fatal("could not create a scoped automation job")
	}
	observedAt := time.Now().UTC().Format(time.RFC3339Nano)
	delivery := fmt.Sprintf(`{"runId":"verify-run","provider":"postgresql","observedAt":%q,"assets":{"version":1,"provider":"postgresql","source":"定时目录","batchId":"local-verify","assets":[{"name":"自动采集表","sourceRef":"public.automatic"}]}}`, observedAt)
	send := func(token string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", automationRunPath, strings.NewReader(delivery))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("automation run got HTTP %d, want %d: %s", w.Code, status, w.Body)
		}
		return w
	}
	send("", 401)
	run := send(createdJob.Token, 200)
	replay := send(createdJob.Token, 200)
	if !strings.Contains(run.Body.String(), `"createdCount":1`) ||
		!strings.Contains(replay.Body.String(), `"replayed":true`) {
		t.Fatal("automatic asset delivery did not import once")
	}
	list := do("GET", jobs, "", reviewerCookie, 200)
	if strings.Contains(list.Body.String(), createdJob.Token) {
		t.Fatal("list disclosed the automation credential")
	}
	jobPath := fmt.Sprintf("%s/%d", jobs, createdJob.Job.ID)
	do("GET", jobPath+"/runs", "", reviewerCookie, 200)
	do("DELETE", jobPath, "", cookie, 204)
	send(createdJob.Token, 401)
	managedPath := workspace + "/automation/managed"
	const managedInput = `{"source":"PostgreSQL managed fixture","host":"managed.example","port":5432,"database":"inventory","user":"metadata_reader","password":"fixture-only-password","schemas":["inventory"],"intervalMinutes":5,"enabled":false}`
	do("POST", managedPath+"/jobs", managedInput, reviewerCookie, 403)
	do("POST", managedPath+"/jobs", strings.ReplaceAll(managedInput, "managed.example", "unapproved.example"), cookie, 400)
	jobRecord := do("POST", managedPath+"/jobs", managedInput, cookie, 201)
	var configured store.AutomationJob
	if err := json.Unmarshal(jobRecord.Body.Bytes(), &configured); err != nil || configured.Managed == nil ||
		configured.Managed.IntervalMinutes != 5 || strings.Contains(jobRecord.Body.String(), "fixture-only-password") {
		t.Fatal("managed task did not save a safe configuration")
	}
	queuedPath := fmt.Sprintf("%s/jobs/%d/run", managedPath, configured.ID)
	do("POST", queuedPath, "", reviewerCookie, 403)
	queued := do("POST", queuedPath, "", cookie, 202)
	if !strings.Contains(queued.Body.String(), `"queued":true`) {
		t.Fatal("managed task did not acknowledge a queued run")
	}
	if _, err := pool.Exec(ctx, `UPDATE plume.memberships AS m SET role = 'viewer'
		FROM plume.workspaces AS w WHERE m.workspace_id = w.id AND w.slug = $1`, a); err != nil {
		t.Fatal(err)
	}
	do("GET", workspace+"/integrations", "", cookie, 200)
	do("POST", importPath, first, cookie, 403)
	do("POST", workspace+"/assets", `{"name":"record","system":"CRM","ownerTeam":""}`, cookie, 403)
	do("DELETE", session, "", cookie, 204)
	do("GET", session, "", cookie, 401)
}
