package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestSecurityNetworkPagesHTTPTraverseSelectCurrentBindingAndIsolateTenants(t *testing.T) {
	f := newIdentityHTTPFixture(t)
	other := newIdentityHTTPFixture(t)
	ctx := t.Context()
	base := "/api/v1/workspaces/" + f.ws.Slug + "/security-platform"
	otherBase := "/api/v1/workspaces/" + other.ws.Slug + "/security-platform"
	if _, err := f.pool.Exec(ctx, `INSERT INTO plume.security_network_bindings(workspace_id,actor_id,configuration) SELECT $1,$2,jsonb_build_object('site','Synthetic branch-'||n,'circuit','L-'||n,'applicationRef','approved-app','assetIds','[]'::jsonb,'activityIds','[]'::jsonb,'region','CN','purpose','Approved synthetic scope','evidenceRef','synthetic-source') FROM generate_series(1,253) n`, f.ws.ID, f.owner.ID); err != nil {
		t.Fatal(err)
	}
	response := identityHTTPRequest(t, f.handler, "GET", base+"/network-bindings?limit=50", "", f.viewerCookie, "", 200)
	var first store.SecurityNetworkBindingPage
	if json.Unmarshal(response.Body.Bytes(), &first) != nil || len(first.Items) != 50 || !first.HasMore || first.NextCursor == "" || first.Limit != 50 || first.GeneratedAt.IsZero() || first.Coverage == "" {
		t.Fatal("binding page DTO")
	}
	lastID := first.Items[0].ID
	detailURL := base + "/network-bindings/" + strconv.FormatInt(lastID, 10)
	detail := identityHTTPRequest(t, f.handler, "GET", detailURL, "", f.viewerCookie, "", 200)
	var binding store.SecurityNetworkBinding
	if json.Unmarshal(detail.Body.Bytes(), &binding) != nil || binding.ID != lastID || binding.Revision != 1 || binding.EvidenceType != "declared" {
		t.Fatal("binding current detail")
	}
	update := binding.SecurityNetworkBindingInput
	update.ExpectedRevision = 1
	update.Region = "SG"
	identityHTTPRequest(t, f.handler, "POST", base+"/network-bindings", gatewayHTTPJSON(t, update), f.viewerCookie, "", 403)
	identityHTTPRequest(t, f.handler, "POST", base+"/network-bindings", gatewayHTTPJSON(t, update), f.cookie, "", 200)
	updated := identityHTTPRequest(t, f.handler, "GET", detailURL, "", f.viewerCookie, "", 200)
	if json.Unmarshal(updated.Body.Bytes(), &binding) != nil || binding.Revision != 2 || binding.Region != "SG" {
		t.Fatal("selected binding does not expose live revision")
	}
	identityHTTPRequest(t, f.handler, "POST", base+"/network-bindings", gatewayHTTPJSON(t, update), f.cookie, "", 409)
	seen := map[int64]bool{}
	page := first
	for {
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatal("duplicate binding across pages")
			}
			seen[item.ID] = true
		}
		if !page.HasMore {
			break
		}
		r := identityHTTPRequest(t, f.handler, "GET", base+"/network-bindings?limit=50&cursor="+page.NextCursor, "", f.viewerCookie, "", 200)
		if json.Unmarshal(r.Body.Bytes(), &page) != nil {
			t.Fatal("binding continuation DTO")
		}
	}
	if len(seen) != 253 {
		t.Fatal("binding HTTP list still truncated", len(seen))
	}
	identityHTTPRequest(t, f.handler, "GET", base+"/network-bindings?q=changed&cursor="+first.NextCursor, "", f.viewerCookie, "", 400)
	identityHTTPRequest(t, f.handler, "GET", otherBase+"/network-bindings?cursor="+first.NextCursor, "", other.cookie, "", 400)
	identityHTTPRequest(t, f.handler, "GET", otherBase+"/network-bindings/"+strconv.FormatInt(lastID, 10), "", other.cookie, "", 404)
	identityHTTPRequest(t, f.handler, "GET", detailURL, "", other.cookie, "", 404)
	identityHTTPRequest(t, f.handler, "GET", base+"/network-bindings/0", "", f.cookie, "", 400)
	identityHTTPRequest(t, f.handler, "GET", base+"/network-bindings/999999999", "", f.cookie, "", 404)
	input := store.SecurityNetworkChangeInput{BindingID: lastID, ExpectedRevision: 2, ProposedRegion: "EU", ProposedApplicationRef: "approved-app-v2", Reason: "Synthetic regional review"}
	for range 3 {
		identityHTTPRequest(t, f.handler, "POST", base+"/network-changes", gatewayHTTPJSON(t, input), f.cookie, "", 201)
	}
	changeURL := base + "/network-changes?bindingId=" + strconv.FormatInt(lastID, 10) + "&status=pending_review&limit=1"
	changed := identityHTTPRequest(t, f.handler, "GET", changeURL, "", f.viewerCookie, "", 200)
	var changes store.SecurityNetworkChangePage
	if json.Unmarshal(changed.Body.Bytes(), &changes) != nil || len(changes.Items) != 1 || !changes.HasMore || changes.Items[0].BindingRevision != 2 || changes.Items[0].Binding.Region != "SG" {
		t.Fatal("frozen proposal page DTO")
	}
	identityHTTPRequest(t, f.handler, "GET", changeURL+"&cursor="+changes.NextCursor, "", f.viewerCookie, "", 200)
	identityHTTPRequest(t, f.handler, "GET", base+"/network-changes?status=accepted&cursor="+changes.NextCursor, "", f.viewerCookie, "", 400)
	identityHTTPRequest(t, f.handler, "GET", base+"/network-changes?cursor="+first.NextCursor, "", f.viewerCookie, "", 400)
	body := []byte(`{"findings":[]}`)
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if _, err := f.pool.Exec(ctx, `INSERT INTO plume.security_platform_snapshots(workspace_id,actor_id,digest,snapshot,created_at) SELECT $1,$2,$3,$4,$5::timestamptz+n*interval '1 day' FROM generate_series(0,32) n`, f.ws.ID, f.owner.ID, digest, body, start); err != nil {
		t.Fatal(err)
	}
	snapshotURL := base + "/snapshots?limit=5&createdFrom=2026-10-11T00:00:00Z&createdTo=2026-10-21T00:00:00Z"
	snapshotResponse := identityHTTPRequest(t, f.handler, "GET", snapshotURL, "", f.viewerCookie, "", 200)
	var snapshots store.SecurityPlatformSnapshotPage
	if json.Unmarshal(snapshotResponse.Body.Bytes(), &snapshots) != nil || len(snapshots.Items) != 5 || !snapshots.HasMore || snapshots.NextCursor == "" || snapshots.Coverage == "" {
		t.Fatal("snapshot time page DTO")
	}
	identityHTTPRequest(t, f.handler, "GET", snapshotURL+"&cursor="+snapshots.NextCursor, "", f.viewerCookie, "", 200)
	identityHTTPRequest(t, f.handler, "GET", base+"/snapshots?createdFrom=2026-10-12T00:00:00Z&cursor="+snapshots.NextCursor, "", f.viewerCookie, "", 400)
	frozen := identityHTTPRequest(t, f.handler, "GET", base+"/snapshots/"+strconv.FormatInt(snapshots.Items[0].ID, 10), "", f.viewerCookie, "", 200)
	if !strings.Contains(frozen.Header().Get("Content-Disposition"), "attachment") || strings.TrimSpace(frozen.Body.String()) != string(body) {
		t.Fatal("existing snapshot download contract changed")
	}
	for _, collection := range []string{"network-bindings", "network-changes", "snapshots"} {
		identityHTTPRequest(t, f.handler, "GET", base+"/"+collection, "", nil, "", 401)
		identityHTTPRequest(t, f.handler, "GET", base+"/"+collection, "", other.cookie, "", 404)
		for _, query := range []string{"limit=", "limit=0", "limit=201", "limit=abc", "limit=1&limit=2", "cursor=invalid", "cursor=a&cursor=b", "extra=true", "limit=%GG"} {
			identityHTTPRequest(t, f.handler, "GET", base+"/"+collection+"?"+query, "", f.viewerCookie, "", 400)
		}
	}
	for _, suffix := range []string{"/network-bindings?q=%FF", "/network-bindings?status=accepted", "/network-changes?bindingId=0", "/network-changes?bindingId=-1", "/network-changes?bindingId=oops", "/network-changes?status=approved", "/network-changes?status=accepted&status=rejected", "/snapshots?createdFrom=", "/snapshots?createdFrom=2026-10-01", "/snapshots?createdFrom=2026-10-11T00:00:00Z&createdTo=2026-10-11T00:00:00Z", "/snapshots?createdFrom=2026-10-12T00:00:00Z&createdTo=2026-10-11T00:00:00Z"} {
		identityHTTPRequest(t, f.handler, "GET", base+suffix, "", f.viewerCookie, "", 400)
	}
	ownerChanges := identityHTTPRequest(t, f.handler, "GET", changeURL, "", f.cookie, "", 200)
	var ownerPage store.SecurityNetworkChangePage
	if json.Unmarshal(ownerChanges.Body.Bytes(), &ownerPage) != nil || len(ownerPage.Items) != 1 || !ownerPage.Items[0].SubmittedByCurrentUser || changes.Items[0].SubmittedByCurrentUser {
		t.Fatal("self-review actor projection inconsistent")
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role) VALUES($1,$2,'reviewer')`, f.ws.ID, other.owner.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM plume.memberships WHERE workspace_id=$1 AND user_id=$2`, f.ws.ID, other.owner.ID)
	})
	independent := identityHTTPRequest(t, f.handler, "GET", changeURL, "", other.cookie, "", 200)
	var independentPage store.SecurityNetworkChangePage
	if json.Unmarshal(independent.Body.Bytes(), &independentPage) != nil || len(independentPage.Items) != 1 || independentPage.Items[0].SubmittedByCurrentUser {
		t.Fatal("independent reviewer actor projection inconsistent")
	}
	previewResponse := identityHTTPRequest(t, f.handler, "GET", base+"/export", "", f.viewerCookie, "", 200)
	var preview store.SecurityPlatform
	if json.Unmarshal(previewResponse.Body.Bytes(), &preview) != nil || len(preview.NetworkBindings) != 250 || len(preview.Snapshots) != 30 || len(preview.PreviewCoverage) != 3 || !preview.PreviewCoverage["networkBindings"].HasMore || !preview.PreviewCoverage["snapshots"].HasMore {
		t.Fatal("overview/export scope missing")
	}
	for _, item := range preview.NetworkBindings {
		if item.ID == lastID {
			t.Fatal("test did not select binding outside old preview")
		}
	}
}

func TestSecurityNetworkPageQueryStrictLimitsAndParameters(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if _, limit, err := securityNetworkPageQuery(r, "q"); err != nil || limit != 50 {
		t.Fatal("default page limit", limit, err)
	}
	for _, query := range []string{"limit=", "limit=201", "limit=1&limit=2", "q=a&q=b", "wrong=value", "q=%GG"} {
		r := httptest.NewRequest("GET", "/?"+query, nil)
		if _, _, err := securityNetworkPageQuery(r, "q"); err == nil {
			t.Fatal("invalid query accepted", query)
		}
	}
}
