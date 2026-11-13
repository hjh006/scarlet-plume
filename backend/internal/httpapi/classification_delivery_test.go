package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClassificationDeliveryHTTPAndAutomaticEventOutbox(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
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
	slug := fmt.Sprintf("delivery-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Delivery fixture"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reviewer", "unused"))
	must(repo.Bootstrap(ctx, slug+"-foreign", "unused", slug+"-foreign", "Other"))
	owner, e := repo.FindUser(ctx, slug)
	must(e)
	reviewer, e := repo.FindUser(ctx, slug+"-reviewer")
	must(e)
	foreign, e := repo.FindUser(ctx, slug+"-foreign")
	must(e)
	ws, e := repo.Membership(ctx, owner.ID, slug)
	must(e)
	foreignWS, e := repo.Membership(ctx, foreign.ID, slug+"-foreign")
	must(e)
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, e := pool.Exec(c, "DELETE FROM plume.workspaces WHERE slug=ANY($1)", []string{slug, slug + "-foreign"})
		if e != nil {
			t.Error(e)
		}
		_, e = pool.Exec(c, "DELETE FROM plume.users WHERE id=ANY($1)", []int64{owner.ID, reviewer.ID, foreign.ID})
		if e != nil {
			t.Error(e)
		}
	})
	cookie := func(id int64) string {
		token, e := sessionToken()
		must(e)
		must(repo.CreateSession(ctx, id, sessionHash(token), time.Now().Add(time.Hour)))
		return token
	}
	own, read, other := cookie(owner.ID), cookie(reviewer.ID), cookie(foreign.ID)
	handler := New(repo, "https://console.invalid", true)
	base := "/api/v1/workspaces/" + slug + "/classification"
	call := func(method, path string, input any, session, bearer string, origin bool, want int) *httptest.ResponseRecorder {
		t.Helper()
		raw, e := json.Marshal(input)
		must(e)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		if origin {
			r.Header.Set("Origin", "https://console.invalid")
		}
		if session != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: session})
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s got%d want%d %.200s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	targetInput := store.ClassificationDeliveryTargetInput{Name: "Original receiver", Endpoint: "http://127.0.0.1:59711/hook", Enabled: true, AutoEvents: true, MinSeverity: "high"}
	call("POST", base+"/delivery-targets", targetInput, read, "", true, 403)
	call("POST", base+"/delivery-targets", targetInput, own, "", false, 403)
	bad := targetInput
	bad.Endpoint += "?secret=value"
	call("POST", base+"/delivery-targets", bad, own, "", true, 400)
	var issued struct {
		Target store.ClassificationDeliveryTarget
		Token  string
	}
	must(json.Unmarshal(call("POST", base+"/delivery-targets", targetInput, own, "", true, 201).Body.Bytes(), &issued))
	if !strings.HasPrefix(issued.Token, classificationDeliveryTokenPrefix) || issued.Target.Revision != 1 {
		t.Fatal("target token missing")
	}
	listed := call("GET", base+"/delivery-targets", nil, read, "", true, 200).Body.String()
	if strings.Contains(listed, issued.Token) || strings.Contains(listed, "tokenDigest") {
		t.Fatal("delivery credential exposed")
	}
	call("POST", classificationDeliveryClaimPath, map[string]string{"leaseId": "lease-first-00001"}, "", "", false, 401)
	call("POST", classificationDeliveryClaimPath, map[string]string{"leaseId": "lease-first-00001"}, "", issued.Token, false, 204)
	asset, e := repo.CreateAsset(ctx, ws.ID, "Synthetic business dataset", "Fixture ERP", "Data")
	must(e)
	_, e = repo.UpdateAssetClassification(ctx, ws.ID, owner.ID, asset.ID, store.AssetClassificationInput{Revision: 0, Category: "business", BusinessLevel: "internal", StatutoryCategory: "general", Basis: "Synthetic fixture manually reviewed", StandardID: "gbt-43697-2024", LevelCode: "general", RuleID: "impact-assessment"})
	must(e)
	pub, e := repo.CreateClassificationPublication(ctx, ws.ID, owner.ID, store.ClassificationPublicationInput{Title: "Approved synthetic metadata", Items: []store.ClassificationAssetRevision{{AssetID: asset.ID, Revision: 1}}})
	must(e)
	path := base + "/publications/" + fmt.Sprint(pub.ID) + "/deliveries"
	call("POST", path, map[string]int64{"targetId": issued.Target.ID}, own, "", true, 409)
	_, e = repo.ReviewClassificationPublication(ctx, ws.ID, reviewer.ID, pub.ID, store.ClassificationPublicationReviewInput{Action: "publish", Note: "Independent synthetic review"})
	must(e)
	foreignTarget, e := repo.SaveClassificationDeliveryTarget(ctx, foreignWS.ID, foreign.ID, 0, targetInput, sessionHash("foreign-synthetic-delivery-token"), false)
	must(e)
	call("POST", path, map[string]int64{"targetId": foreignTarget.ID}, own, "", true, 404)
	call("POST", path, map[string]int64{"targetId": issued.Target.ID}, read, "", true, 403)
	var job, again store.ClassificationDelivery
	must(json.Unmarshal(call("POST", path, map[string]int64{"targetId": issued.Target.ID}, own, "", true, 201).Body.Bytes(), &job))
	must(json.Unmarshal(call("POST", path, map[string]int64{"targetId": issued.Target.ID}, own, "", true, 201).Body.Bytes(), &again))
	if job.ID != again.ID || job.Status != "queued" {
		t.Fatal("publication enqueue not idempotent")
	}
	var claim store.ClassificationDeliveryClaim
	must(json.Unmarshal(call("POST", classificationDeliveryClaimPath, map[string]string{"leaseId": "lease-first-00001"}, "", issued.Token, false, 200).Body.Bytes(), &claim))
	canonical, hash, e := store.CanonicalClassificationDelivery(claim.Payload)
	must(e)
	if hash != claim.PayloadDigest || len(canonical) == 0 || claim.Endpoint != targetInput.Endpoint {
		t.Fatal("claim payload or destination not fixed")
	}
	receipt := store.ClassificationDeliveryReceipt{DeliveryID: claim.ID, LeaseID: claim.LeaseID, PayloadDigest: hash, Status: "accepted", RemoteRef: "receiver-fixture-1"}
	altered := receipt
	altered.PayloadDigest = strings.Repeat("0", 64)
	call("POST", classificationDeliveryReceiptPath, altered, "", issued.Token, false, 409)
	var accepted store.ClassificationDelivery
	must(json.Unmarshal(call("POST", classificationDeliveryReceiptPath, receipt, "", issued.Token, false, 200).Body.Bytes(), &accepted))
	if accepted.Status != "accepted" || accepted.DeliveredAt == nil {
		t.Fatal("receipt missing")
	}
	call("POST", classificationDeliveryReceiptPath, receipt, "", issued.Token, false, 200)
	call("POST", classificationDeliveryClaimPath, map[string]string{"leaseId": "lease-none-000002"}, "", issued.Token, false, 204)
	var duplicates store.ClassificationDelivery
	must(json.Unmarshal(call("POST", path, map[string]int64{"targetId": issued.Target.ID}, own, "", true, 201).Body.Bytes(), &duplicates))
	if duplicates.ID != job.ID || duplicates.Status != "accepted" {
		t.Fatal("accepted publication redelivered")
	}
	eventDigest := sessionHash("fixture-event-source")
	_, e = repo.CreateOperationsSource(ctx, ws.ID, owner.ID, store.OperationsSourceInput{Name: "Synthetic event source", Kind: "security_events", OwnerTeam: "Security"}, eventDigest)
	must(e)
	event, _, e := repo.ReceiveSecurityEvent(ctx, eventDigest, store.SecurityEventInput{EventID: "event-high-1", ObservedAt: time.Now(), Category: "data_leak", Rule: "synthetic-export", Severity: "high", Service: "Fixture", Count: 3, WindowSeconds: 60})
	must(e)
	list, e := repo.ClassificationDeliveries(ctx, ws.ID, owner.ID, 0, issued.Target.ID)
	must(e)
	if len(list) != 2 || list[0].ResourceKind != "security_event" || list[0].ResourceID != event.ID {
		t.Fatal("explicit high event subscription not enqueued")
	}
	_, _, e = repo.ReceiveSecurityEvent(ctx, eventDigest, store.SecurityEventInput{EventID: "event-high-1", ObservedAt: event.ObservedAt, Category: "data_leak", Rule: "synthetic-export", Severity: "high", Service: "Fixture", Count: 3, WindowSeconds: 60})
	must(e)
	list, e = repo.ClassificationDeliveries(ctx, ws.ID, owner.ID, 0, issued.Target.ID)
	must(e)
	if len(list) != 2 {
		t.Fatal("source replay duplicated notification")
	}
	var eventClaim store.ClassificationDeliveryClaim
	must(json.Unmarshal(call("POST", classificationDeliveryClaimPath, map[string]string{"leaseId": "lease-event-00001"}, "", issued.Token, false, 200).Body.Bytes(), &eventClaim))
	failed := store.ClassificationDeliveryReceipt{DeliveryID: eventClaim.ID, LeaseID: eventClaim.LeaseID, PayloadDigest: eventClaim.PayloadDigest, Status: "failed", ErrorCode: "acknowledgement"}
	call("POST", classificationDeliveryReceiptPath, failed, "", issued.Token, false, 200)
	eventPath := "/api/v1/workspaces/" + slug + "/operations/events/" + fmt.Sprint(event.ID) + "/deliveries"
	var retry store.ClassificationDelivery
	must(json.Unmarshal(call("POST", eventPath, map[string]int64{"targetId": issued.Target.ID}, own, "", true, 201).Body.Bytes(), &retry))
	if retry.ID != eventClaim.ID || retry.Status != "queued" {
		t.Fatal("manual retry changed idempotency identity")
	}
	call("POST", classificationDeliveryClaimPath, map[string]string{"leaseId": "lease-event-00002"}, "", issued.Token, false, 200)
	var rotated struct {
		Target store.ClassificationDeliveryTarget
		Token  string
	}
	must(json.Unmarshal(call("POST", base+"/delivery-targets/"+fmt.Sprint(issued.Target.ID)+"/rotate", map[string]int64{"revision": 1}, own, "", true, 200).Body.Bytes(), &rotated))
	call("POST", classificationDeliveryClaimPath, map[string]string{"leaseId": "lease-old-000001"}, "", issued.Token, false, 401)
	call("POST", classificationDeliveryClaimPath, map[string]string{"leaseId": "lease-new-000001"}, "", rotated.Token, false, 204)
	targetInput.Revision = rotated.Target.Revision
	targetInput.Name = "Renamed receiver"
	targetInput.Endpoint = "http://127.0.0.1:59712/hook"
	call("PUT", base+"/delivery-targets/"+fmt.Sprint(issued.Target.ID), targetInput, own, "", true, 200)
	list, e = repo.ClassificationDeliveries(ctx, ws.ID, owner.ID, pub.ID, issued.Target.ID)
	must(e)
	if len(list) != 1 || list[0].TargetEndpoint != "http://127.0.0.1:59711/hook" || list[0].TargetName != "Original receiver" {
		t.Fatal("target edit rewrote prior delivery destination")
	}
	call("GET", base+"/deliveries", nil, other, "", true, 404)
}
