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

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

var extendedHTTPRules = []string{"over_collection", "credential_cleartext_transport", "subject_multi_ip", "subject_multi_region", "client_region_mismatch", "recipient_scope", "destination_scope", "volume_limit", "subject_repeated_access"}

type extendedHTTPFixture struct {
	pool                                                       *pgxpool.Pool
	repo                                                       *store.Store
	handler                                                    http.Handler
	workspace                                                  store.Workspace
	owner, reviewer, viewer, foreign                           store.User
	ownerSession, reviewSession, viewerSession, foreignSession string
	policy                                                     store.RuntimeDetectionPolicy
	source                                                     store.RuntimeSource
	sourceToken                                                string
	checkID                                                    int64
	base, anchor, end                                          time.Time
	good                                                       runtimesecurity.Observation
	sequence                                                   int
}

func extendedHTTPMust(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func (f *extendedHTTPFixture) request(t *testing.T, method, path, token, body, origin string, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/api/v1/workspaces/"+f.workspace.Slug+path, strings.NewReader(body))
	if token != "" {
		r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: token})
	}
	if method != "GET" {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s returned %d want%d body=%s", method, path, w.Code, status, w.Body.String())
	}
	return w
}
func (f *extendedHTTPFixture) detail(t *testing.T, token string) store.CheckDetail {
	t.Helper()
	w := f.request(t, "GET", fmt.Sprintf("/checks/%d", f.checkID), token, "", "", 200)
	if strings.Contains(w.Body.String(), "raw-fixture-person-needle") {
		t.Fatal("rejected raw value appeared in stored response")
	}
	var d store.CheckDetail
	extendedHTTPMust(t, json.Unmarshal(w.Body.Bytes(), &d))
	return d
}
func cloneExtendedHTTPObservation(o runtimesecurity.Observation) runtimesecurity.Observation {
	o.DataFields = append([]runtimesecurity.DataField(nil), o.DataFields...)
	o.InspectedLocations = append([]string(nil), o.InspectedLocations...)
	if o.Context != nil {
		copy := *o.Context
		o.Context = &copy
		if copy.Volume != nil {
			n := *copy.Volume
			o.Context.Volume = &n
		}
	}
	return o
}
func (f *extendedHTTPFixture) deliver(t *testing.T, at time.Time, events ...runtimesecurity.Observation) {
	t.Helper()
	f.sequence++
	for i := range events {
		events[i].ObservedAt = at
	}
	receipt, e := f.repo.DeliverRuntimeBatch(t.Context(), "gateway", f.sourceToken, sessionHash(fmt.Sprintf("fixture-batch-%d", f.sequence)), events)
	extendedHTTPMust(t, e)
	if receipt.Accepted != len(events) {
		t.Fatal("fixture source delivery incomplete", receipt)
	}
}
func runtimeExtendedHTTPFixture(t *testing.T, rule string) *extendedHTTPFixture {
	t.Helper()
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	pool, e := pgxpool.New(t.Context(), dsn)
	extendedHTTPMust(t, e)
	t.Cleanup(pool.Close)
	repo := store.New(pool)
	extendedHTTPMust(t, repo.Migrate(t.Context()))
	slug := fmt.Sprintf("extended-http-%d", time.Now().UnixNano())
	extendedHTTPMust(t, repo.Bootstrap(t.Context(), slug, "unused", slug, "Extended HTTP fixture"))
	extendedHTTPMust(t, repo.ProvisionReviewer(t.Context(), slug, slug+"-reviewer", "unused"))
	extendedHTTPMust(t, repo.Bootstrap(t.Context(), slug+"-other", "unused", slug+"-other", "Other workspace"))
	owner, e := repo.FindUser(t.Context(), slug)
	extendedHTTPMust(t, e)
	reviewer, e := repo.FindUser(t.Context(), slug+"-reviewer")
	extendedHTTPMust(t, e)
	foreign, e := repo.FindUser(t.Context(), slug+"-other")
	extendedHTTPMust(t, e)
	ws, e := repo.Membership(t.Context(), owner.ID, slug)
	extendedHTTPMust(t, e)
	var viewerID int64
	extendedHTTPMust(t, pool.QueryRow(t.Context(), `INSERT INTO plume.users(username,password_hash) VALUES($1,'unused') RETURNING id`, slug+"-viewer").Scan(&viewerID))
	_, e = pool.Exec(t.Context(), `INSERT INTO plume.memberships(workspace_id,user_id,role) VALUES($1,$2,'viewer')`, ws.ID, viewerID)
	extendedHTTPMust(t, e)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM plume.workspaces WHERE slug=ANY($1)`, []string{slug, slug + "-other"})
		_, _ = pool.Exec(ctx, `DELETE FROM plume.users WHERE id=ANY($1)`, []int64{owner.ID, reviewer.ID, foreign.ID, viewerID})
	})
	session := func(id int64) string {
		token, e := sessionToken()
		extendedHTTPMust(t, e)
		extendedHTTPMust(t, repo.CreateSession(t.Context(), id, sessionHash(token), time.Now().Add(time.Hour)))
		return token
	}
	f := &extendedHTTPFixture{pool: pool, repo: repo, handler: New(repo, "https://example.invalid", true), workspace: ws, owner: owner, reviewer: reviewer, viewer: store.User{ID: viewerID}, foreign: foreign, ownerSession: session(owner.ID), reviewSession: session(reviewer.ID), viewerSession: session(viewerID), foreignSession: session(foreign.ID), sourceToken: sessionHash(slug + "-source")}
	f.source, e = repo.CreateRuntimeSource(t.Context(), ws.ID, owner.ID, "Scoped source", "HTTP retest app", "gateway", f.sourceToken)
	extendedHTTPMust(t, e)
	policies, e := repo.RuntimeDetectionPolicies(t.Context(), ws.ID, owner.ID)
	extendedHTTPMust(t, e)
	for _, p := range policies {
		if p.Rule == rule {
			f.policy = p
			break
		}
	}
	p := f.policy
	if p.Rule == "" {
		t.Fatal("missing extended rule", rule)
	}
	p.Enabled = true
	p.SourceID = f.source.ID
	p.ScopeKey = "http-retest"
	p.Method = "POST"
	p.Route = "/extended-http/" + rule
	p.WindowMinutes = 1
	p.MinRequests = 1
	p.Threshold = 1
	switch rule {
	case "over_collection":
		p.Direction = "request"
		p.AllowlistConfigured = true
		p.AllowedCategories = []string{"phone"}
	case "subject_multi_ip", "subject_multi_region", "subject_repeated_access":
		p.Threshold = 2
	case "client_region_mismatch":
		p.AllowedClientCountries = []string{"CN"}
	case "recipient_scope":
		p.AllowedRecipients = []string{"registered"}
	case "destination_scope":
		p.AllowedCountries = []string{"CN"}
		p.AllowedDomains = []string{"registered.example"}
	case "volume_limit":
		p.MaxVolume = 100
		p.VolumeUnit = "records"
	}
	raw, e := json.Marshal(p)
	extendedHTTPMust(t, e)
	extendedHTTPMust(t, json.Unmarshal(f.request(t, "PUT", "/api-security/runtime/policies", f.ownerSession, string(raw), "https://example.invalid", 200).Body.Bytes(), &f.policy))
	var now time.Time
	extendedHTTPMust(t, pool.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now))
	f.base = now.Add(-5 * time.Minute).Truncate(time.Microsecond)
	f.anchor = f.base.Add(70 * time.Second)
	f.end = f.base.Add(131 * time.Second)
	// Backdate only this isolated synthetic fixture; production retains wall time.
	_, e = pool.Exec(t.Context(), `UPDATE plume.runtime_detection_policies SET created_at=$2 WHERE id=$1`, f.policy.ID, f.base.Add(-time.Minute))
	extendedHTTPMust(t, e)
	volume := int64(20)
	f.good = runtimesecurity.Observation{Method: "POST", Route: p.Route, Status: 200, ObservedAt: f.base, SubjectRef: strings.Repeat("a", 64), ClientIP: "192.0.2.1", Transport: "https", TransportHop: "proxy_upstream", InspectedLocations: []string{"request", "response"}, Context: &runtimesecurity.RuntimeContext{ClientCountry: "CN", DestinationCountry: "CN", DestinationDomain: "registered.example", Recipient: "registered", Volume: &volume, VolumeUnit: "records"}, DataFields: []runtimesecurity.DataField{{Name: "phone", Location: "request", Category: "phone", Method: "format", Count: 1, Observed: 1}}}
	bad := cloneExtendedHTTPObservation(f.good)
	positive := []runtimesecurity.Observation{bad}
	switch rule {
	case "over_collection":
		bad.DataFields = []runtimesecurity.DataField{{Name: "email", Location: "request", Category: "email", Method: "format", Count: 1, Observed: 1}}
	case "credential_cleartext_transport":
		bad.Transport = "http"
		bad.DataFields = []runtimesecurity.DataField{{Name: "password", Location: "request", Category: "credential", Method: "metadata", Count: 1, Observed: 1}}
		f.good.DataFields = append([]runtimesecurity.DataField(nil), bad.DataFields...)
	case "subject_multi_ip":
		other := cloneExtendedHTTPObservation(bad)
		other.ClientIP = "192.0.2.2"
		positive = append(positive, other)
	case "subject_multi_region":
		other := cloneExtendedHTTPObservation(bad)
		other.Context.ClientCountry = "US"
		positive = append(positive, other)
	case "subject_repeated_access":
		positive = append(positive, bad)
	case "client_region_mismatch":
		bad.Context.ClientCountry = "US"
	case "recipient_scope":
		bad.Context.Recipient = "unregistered"
	case "destination_scope":
		bad.Context.DestinationCountry = "US"
		bad.Context.DestinationDomain = "external.example"
	case "volume_limit":
		v := int64(200)
		bad.Context.Volume = &v
	}
	positive[0] = bad
	f.deliver(t, f.base, positive...)
	findings, e := repo.ListRuntimeFindings(t.Context(), ws.ID)
	extendedHTTPMust(t, e)
	for _, item := range findings {
		if item.Rule == rule && item.SourceID == f.source.ID {
			f.checkID = item.CheckID
		}
	}
	if f.checkID == 0 {
		t.Fatal("actual source positive did not create G03 finding", rule)
	}
	_, e = pool.Exec(t.Context(), `UPDATE plume.checks SET created_at=$2 WHERE id=$1`, f.checkID, f.base.Add(time.Second))
	extendedHTTPMust(t, e)
	return f
}
func (f *extendedHTTPFixture) pass(t *testing.T) {
	t.Helper()
	at := f.base.Add(10 * time.Second)
	raw, e := json.Marshal(store.EvidenceInput{Kind: "remediation", SourceSystem: "HTTP synthetic correction", SourceRecord: "fixture:fixed", Summary: "Scoped technical configuration corrected", ObservedAt: at})
	extendedHTTPMust(t, e)
	f.request(t, "POST", fmt.Sprintf("/checks/%d/evidence", f.checkID), f.ownerSession, string(raw), "https://example.invalid", 204)
	_, e = f.pool.Exec(t.Context(), `UPDATE plume.check_evidence SET created_at=$2 WHERE check_id=$1 AND kind='remediation'`, f.checkID, at)
	extendedHTTPMust(t, e)
	f.deliver(t, f.anchor, cloneExtendedHTTPObservation(f.good))
	f.deliver(t, f.end, cloneExtendedHTTPObservation(f.good))
	d := f.detail(t, f.reviewSession)
	if d.Status != "in_review" || d.RuntimeRetest == nil || d.RuntimeRetest.State != "passed" || !d.CanReview || !d.CanVerify {
		t.Fatalf("complete source auto retest did not submit for independent review: %+v", d)
	}
}
func TestExtendedRuntimeNineRulesHTTPAutomaticRetestAndIndependentReview(t *testing.T) {
	for _, rule := range extendedHTTPRules {
		t.Run(rule, func(t *testing.T) {
			f := runtimeExtendedHTTPFixture(t, rule)
			path := fmt.Sprintf("/checks/%d", f.checkID)
			draft := f.detail(t, f.ownerSession)
			if draft.Status != "draft" || draft.CanSubmit || draft.RuntimeRetest == nil || draft.RuntimeRetest.State != "awaiting_remediation" {
				t.Fatal("draft bypassed automatic retest", draft)
			}
			f.request(t, "POST", path+"/submit", f.ownerSession, "{}", "https://example.invalid", 409)
			f.request(t, "POST", path+"/evidence", f.ownerSession, `{"kind":"remediation","rawValue":"raw-fixture-person-needle"}`, "https://example.invalid", 400)
			f.request(t, "POST", path+"/evidence", f.viewerSession, "{}", "https://example.invalid", 403)
			f.request(t, "POST", path+"/submit", f.ownerSession, "{}", "https://evil.invalid", 403)
			f.request(t, "GET", path, f.foreignSession, "", "", 404)
			f.pass(t)
			passed := f.detail(t, f.reviewSession)
			owner := f.detail(t, f.ownerSession)
			if owner.CanVerify || owner.CanReview {
				t.Fatal("creator can independently review own automatic proof")
			}
			body := fmt.Sprintf(`{"reviewRevision":%d,"decision":"verified","note":"Reviewed scoped source and complete window"}`, passed.ReviewRevision)
			f.request(t, "POST", path+"/review", f.ownerSession, body, "https://example.invalid", 403)
			f.request(t, "POST", path+"/review", f.viewerSession, body, "https://example.invalid", 403)
			f.request(t, "POST", path+"/review", f.reviewSession, body, "https://evil.invalid", 403)
			f.request(t, "POST", path+"/review", f.reviewSession, body, "https://example.invalid", 204)
			verified := f.detail(t, f.reviewSession)
			if verified.Status != "verified" || verified.RuntimeRetest == nil || verified.RuntimeRetest.State != "verified" {
				t.Fatal("independent HTTP review not retained", verified)
			}
			exported := f.request(t, "GET", path+"/export", f.reviewSession, "", "", 200)
			if !strings.Contains(exported.Header().Get("Content-Disposition"), "attachment;") {
				t.Fatal("check export missing download header")
			}
			var report struct {
				Check          store.Check               `json:"check"`
				RuntimeRetest  *store.RuntimeRetestState `json:"runtimeRetest"`
				RuntimeFinding *store.RuntimeFinding     `json:"runtimeFinding"`
			}
			extendedHTTPMust(t, json.Unmarshal(exported.Body.Bytes(), &report))
			if report.RuntimeFinding == nil || report.RuntimeFinding.Detection == nil || report.RuntimeFinding.Detection.AutomaticRetest == nil {
				t.Fatal("existing nested proof contract lost")
			}
			proof := report.RuntimeFinding.Detection.AutomaticRetest
			if report.Check.Status != "verified" || report.RuntimeRetest == nil || report.RuntimeRetest.State != "verified" || report.RuntimeRetest.Reason == "" || proof == nil || proof.Version != 1 || proof.OriginalPolicy.ID != f.policy.ID || proof.AdoptedPolicy.ID != f.policy.ID || proof.OriginalPolicy.Revision != f.policy.Revision || proof.AdoptedPolicy.Revision != f.policy.Revision || proof.OriginalPolicy.Rule != rule || proof.AdoptedPolicy.Rule != rule || proof.PolicyRevision != f.policy.Revision || proof.ScopeKey != f.policy.ScopeKey || proof.ScopeDigest == "" || proof.Requests == 0 || proof.Observed == 0 || !proof.WindowStart.Equal(f.end.Add(-time.Minute)) || !proof.WindowEnd.Equal(f.end) {
				t.Fatalf("verified export lost automatic retest scope/version/window: %+v", report)
			}
			for _, secret := range []string{"raw-fixture-person-needle", f.sourceToken, f.ownerSession, f.reviewSession} {
				if strings.Contains(exported.Body.String(), secret) {
					t.Fatal("body/token escaped into check export")
				}
			}
			f.request(t, "GET", path+"/export", f.foreignSession, "", "", 404)

		})
	}
}
func TestExtendedRuntimeHTTPReviewInvalidation(t *testing.T) {
	for _, variant := range []string{"new-positive", "paused-policy", "revoked-source", "demoted-source-owner"} {
		t.Run(variant, func(t *testing.T) {
			f := runtimeExtendedHTTPFixture(t, "over_collection")
			f.pass(t)
			passed := f.detail(t, f.reviewSession)
			switch variant {
			case "new-positive":
				bad := cloneExtendedHTTPObservation(f.good)
				bad.DataFields = []runtimesecurity.DataField{{Name: "email", Location: "request", Category: "email", Method: "format", Count: 1, Observed: 1}}
				f.deliver(t, f.end.Add(time.Second), bad)
			case "paused-policy":
				p := f.policy
				p.Enabled = false
				raw, e := json.Marshal(p)
				extendedHTTPMust(t, e)
				f.request(t, "PUT", "/api-security/runtime/policies", f.ownerSession, string(raw), "https://example.invalid", 200)
			case "revoked-source":
				f.request(t, "DELETE", fmt.Sprintf("/api-security/runtime/sources/%d", f.source.ID), f.ownerSession, "", "https://example.invalid", 204)
			case "demoted-source-owner":
				_, e := f.pool.Exec(t.Context(), `UPDATE plume.memberships SET role='operator' WHERE workspace_id=$1 AND user_id=$2`, f.workspace.ID, f.owner.ID)
				extendedHTTPMust(t, e)
			}
			current := f.detail(t, f.reviewSession)
			if current.CanVerify || current.RuntimeRetest == nil || current.RuntimeRetest.State == "passed" || current.RuntimeRetest.State == "verified" {
				t.Fatal("invalidated source/policy/positive retained independent pass", current)
			}
			body := fmt.Sprintf(`{"reviewRevision":%d,"decision":"verified","note":"Old proof must be rejected"}`, passed.ReviewRevision)
			f.request(t, "POST", fmt.Sprintf("/checks/%d/review", f.checkID), f.reviewSession, body, "https://example.invalid", 409)
		})
	}
}
