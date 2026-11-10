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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestRuntimeMonitorRoutes(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("could not configure test database")
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
	slug := fmt.Sprintf("runtime-monitor-http-%d", time.Now().UnixNano())
	user := slug + "-owner"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := pool.Exec(ctx, "DELETE FROM plume.workspaces WHERE slug = $1", slug)
		if err != nil {
			t.Error(err)
		}
		_, err = pool.Exec(ctx, "DELETE FROM plume.users WHERE username = $1", user)
		if err != nil {
			t.Error(err)
		}
	})
	must(repo.Bootstrap(ctx, user, "unused", slug, "Runtime monitor HTTP"))
	owner, err := repo.FindUser(ctx, user)
	must(err)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	token, err := sessionToken()
	must(err)
	must(repo.CreateSession(ctx, owner.ID, sessionHash(token), time.Now().Add(time.Hour)))
	deliveryToken := sessionHash(slug + "-token")
	_, err = repo.CreateRuntimeSource(ctx, workspace.ID, owner.ID, "HTTP fixture", "Orders", "gateway", deliveryToken)
	must(err)
	_, err = repo.DeliverRuntimeBatch(ctx, "gateway", deliveryToken, sessionHash("http batch"), []runtimesecurity.Observation{{
		Method: "GET", Route: "/orders/{id}", Status: 503, ObservedAt: time.Now().Add(-time.Minute),
	}})
	must(err)

	handler := New(repo, "https://example.invalid", true)
	base := "/api/v1/workspaces/" + slug
	get := func(path string, authenticated bool, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, base+path, nil)
		if authenticated {
			req.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: token})
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != status || res.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s returned %d, want %d", path, res.Code, status)
		}
		return res
	}
	const monitorPath = "/api-security/runtime/monitor"
	const auditPath = "/audit/runtime"
	get(monitorPath, false, http.StatusUnauthorized)
	get(auditPath+"/export", false, http.StatusUnauthorized)
	get(monitorPath+"?hours=2", true, http.StatusBadRequest)
	get(auditPath+"?sourceId=-1", true, http.StatusBadRequest)
	get(auditPath+"?status=5xx", true, http.StatusBadRequest)
	monitor := get(monitorPath+"?hours=1", true, http.StatusOK)
	var data store.RuntimeMonitor
	must(json.Unmarshal(monitor.Body.Bytes(), &data))
	if data.Totals.Requests != 1 || data.Totals.Responses5xx != 1 {
		t.Fatalf("unexpected monitor totals: %+v", data.Totals)
	}
	const query = "?hours=24&method=get&status=503&route=orders"
	list := get(auditPath+query, true, http.StatusOK)
	export := get(auditPath+"/export"+query, true, http.StatusOK)
	if !strings.Contains(export.Header().Get("Content-Disposition"), "attachment") || list.Body.String() != export.Body.String() {
		t.Fatal("audit export did not use the same filtered projection")
	}
	var audit store.RuntimeAudit
	must(json.Unmarshal(export.Body.Bytes(), &audit))
	if len(audit.Items) != 1 || audit.Items[0].Route != "/orders/{id}" || audit.Items[0].Status != 503 {
		t.Fatalf("unexpected exported audit: %+v", audit)
	}
}
