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
	"github.com/hjh006/scarlet-plume/backend/pkg/governanceops"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGovernanceOpsHTTPAuthStrictInputAndIndependentReview(t *testing.T) {
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
	repo := store.New(pool)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("govops-http-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Governance HTTP"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reviewer", "unused"))
	owner, err := repo.FindUser(ctx, slug)
	must(err)
	reviewer, err := repo.FindUser(ctx, slug+"-reviewer")
	must(err)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=$1`, ws.ID)
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE id=ANY($1::bigint[])`, []int64{owner.ID, reviewer.ID})
	})
	session := func(id int64) string {
		token, e := sessionToken()
		must(e)
		must(repo.CreateSession(ctx, id, sessionHash(token), time.Now().Add(time.Hour)))
		return token
	}
	ownerCookie, reviewCookie := session(owner.ID), session(reviewer.ID)
	handler := New(repo, "https://console.invalid", true)
	base := "/api/v1/workspaces/" + slug + "/governance-ops"
	call := func(method, path, cookie, origin string, body any, status int) *httptest.ResponseRecorder {
		t.Helper()
		b, e := json.Marshal(body)
		must(e)
		r := httptest.NewRequest(method, path, strings.NewReader(string(b)))
		r.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: cookie})
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s got %d want %d %.400s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	call("GET", base+"/catalog", "", "", nil, 401)
	catalog := call("GET", base+"/catalog", reviewCookie, "", nil, 200)
	if !strings.Contains(catalog.Body.String(), "cn-small-pi-2026") {
		t.Fatal("missing versioned catalog")
	}
	call("GET", base+"/records?limit=101", ownerCookie, "", nil, 400)
	call("GET", base+"/records?kind=invalid", ownerCookie, "", nil, 400)
	call("POST", base+"/records", ownerCookie, "https://foreign.invalid", map[string]any{}, 403)
	bad := map[string]any{"kind": "audit", "title": "bad", "owner": "privacy", "applicability": "unknown", "applicabilityReason": "pending", "details": map[string]any{"scope": "audit", "rawPrompt": "must not store arbitrary metadata"}}
	call("POST", base+"/records", ownerCookie, "https://console.invalid", bad, 400)
	asset, err := repo.CreateAsset(ctx, ws.ID, "Records", "fixture", "privacy")
	must(err)
	check, err := repo.CreateCheck(ctx, ws.ID, owner.ID, store.CheckInput{AssetID: asset.ID, RequirementCode: "P01", OwnerTeam: "privacy", Note: "source evidence"})
	must(err)
	now := time.Now().UTC().Add(-time.Minute)
	must(repo.AddEvidence(ctx, ws.ID, owner.ID, check.ID, store.EvidenceInput{Kind: "observation", SourceSystem: "fixture", SourceRecord: "response-1", Summary: "Manually recorded response receipt", ObservedAt: now}))
	var evidenceID int64
	must(pool.QueryRow(ctx, `SELECT id FROM plume.check_evidence WHERE check_id=$1`, check.ID).Scan(&evidenceID))
	in := governanceops.Input{Kind: "rights_request", Title: "Copy request", Owner: "privacy", CheckIDs: []int64{check.ID}, EvidenceIDs: []int64{evidenceID}, RegulationIDs: []string{"cn-pipl-2021"}, Applicability: "applicable", ApplicabilityReason: "Verified covered processing", Details: governanceops.Details{RequestType: "copy", SubjectRef: strings.Repeat("a", 64), ReceivedAt: &now, IdentityStatus: "verified", IdentityEvidenceID: evidenceID, Outcome: "Response delivered; execution evidence is recorded separately", Resolution: "fulfilled"}, Receipts: []governanceops.Receipt{{Purpose: "identity_verification", EvidenceID: evidenceID}, {Purpose: "response_delivery", EvidenceID: evidenceID}}}
	call("POST", base+"/records", reviewCookie, "https://console.invalid", in, 403)
	var record store.GovernanceOpsRecord
	must(json.Unmarshal(call("POST", base+"/records", ownerCookie, "https://console.invalid", in, 201).Body.Bytes(), &record))
	path := fmt.Sprintf("%s/records/%d", base, record.ID)
	act := func(name, cookie string, status int) {
		t.Helper()
		w := call("POST", path+"/actions", cookie, "https://console.invalid", governanceops.Action{Revision: record.Revision, Action: name, Note: "Reviewed sources and decision scope"}, status)
		if status == 200 {
			must(json.Unmarshal(w.Body.Bytes(), &record))
		}
	}
	act("start", ownerCookie, 200)
	act("submit", ownerCookie, 200)
	act("approve", ownerCookie, 403)
	act("approve", reviewCookie, 200)
	if record.Status != "completed" || record.Revision != 4 || len(record.History) != 4 || record.Evidence[0].Provenance != "recorded_evidence" {
		t.Fatal("incorrect final record", record)
	}
	call("POST", path+"/actions", ownerCookie, "https://console.invalid", governanceops.Action{Revision: 1, Action: "cancel", Note: "stale"}, 409)
	call("GET", path+"/history?limit=1", reviewCookie, "", nil, 200)
	call("GET", path+"/history?cursor=bad", reviewCookie, "", nil, 400)
}
