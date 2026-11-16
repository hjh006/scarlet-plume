package httpapi

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func TestAISecurityHistoryHTTPPagesDetailsExportsAndVersionProvenance(t *testing.T) {
	f := newIdentityHTTPFixture(t)
	ctx := t.Context()
	base := "/api/v1/workspaces/" + f.ws.Slug + "/ai-security"
	decision := gatewayHTTPJSON(t, aisecurity.Decision{Kind: "model", Outcome: "allow", CatalogVersion: "historical-catalog", CatalogDigest: strings.Repeat("a", 64)})
	if _, err := f.pool.Exec(ctx, `INSERT INTO plume.ai_security_decisions(workspace_id,actor_id,configuration_revision,decision) SELECT $1,$2,1,$3 FROM generate_series(1,3)`, f.ws.ID, f.owner.ID, decision); err != nil {
		t.Fatal(err)
	}
	evaluation := gatewayHTTPJSON(t, store.AISecurityEvaluation{ConfigurationRevision: 1, Status: "passed", EngineVersion: "historical-engine", InspectionVersion: "historical-inspector", GateVersion: "historical-gate", GateEligible: true})
	if _, err := f.pool.Exec(ctx, `INSERT INTO plume.ai_security_evaluations(workspace_id,actor_id,configuration_revision,catalog_digest,status,result) SELECT $1,$2,1,$3,'passed',$4 FROM generate_series(1,3)`, f.ws.ID, f.owner.ID, strings.Repeat("a", 64), evaluation); err != nil {
		t.Fatal(err)
	}
	rawRequest := gatewayHTTPJSON(t, aisecurity.Request{RequestID: "synthetic-http-history", Kind: "tool", ApplicationID: "app", PrincipalID: "principal", ToolID: "tool", IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour)})
	if _, err := f.pool.Exec(ctx, `INSERT INTO plume.ai_security_approvals(workspace_id,configuration_revision,catalog_digest,request_digest,request_metadata,requested_by,status,expires_at) SELECT $1,1,$2,$2,$3,$4,'proposed',clock_timestamp()+interval '1 hour' FROM generate_series(1,3)`, f.ws.ID, strings.Repeat("a", 64), rawRequest, f.owner.ID); err != nil {
		t.Fatal(err)
	}
	previous := ""
	for i := range 3 {
		pack := store.BaselineAISecurityRegressionPack()
		pack.Version, pack.Digest = fmt.Sprintf("history-http-v%d", i), ""
		validated, err := store.ValidateAISecurityRegressionPack(pack, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.repo.ImportAISecurityRegressionPack(ctx, f.ws.ID, f.owner.ID, previous, validated.Pack); err != nil {
			t.Fatal(err)
		}
		previous = pack.Version
	}
	type page struct {
		Items      []json.RawMessage `json:"items"`
		Limit      int               `json:"limit"`
		HasMore    bool              `json:"hasMore"`
		NextCursor string            `json:"nextCursor"`
		Scope      struct {
			Mode     string    `json:"mode"`
			ReadAt   time.Time `json:"readAt"`
			Snapshot string    `json:"snapshot"`
			HasMore  bool      `json:"hasMore"`
		} `json:"scope"`
	}
	for _, collection := range []string{"decisions", "evaluations", "approvals", "regression-packs"} {
		identityHTTPRequest(t, f.handler, "GET", base+"/"+collection, "", nil, "", 401)
		first := identityHTTPRequest(t, f.handler, "GET", base+"/"+collection+"?limit=1", "", f.viewerCookie, "", 200)
		var p page
		if json.Unmarshal(first.Body.Bytes(), &p) != nil || len(p.Items) != 1 || !p.HasMore || p.NextCursor == "" || p.Limit != 1 {
			t.Fatal("history page metadata", collection)
		}
		if collection == "regression-packs" && (!strings.Contains(first.Body.String(), "requiredDimensions") || !strings.Contains(first.Body.String(), "latestGate") || !strings.Contains(first.Body.String(), "inspectionVersion")) {
			t.Fatal("legacy pack metadata removed")
		}
		identityHTTPRequest(t, f.handler, "GET", base+"/"+collection+"?limit=1&before="+p.NextCursor, "", f.viewerCookie, "", 200)
		var id struct {
			ID int64 `json:"id"`
		}
		if json.Unmarshal(p.Items[0], &id) != nil || id.ID < 1 {
			t.Fatal("record ID missing")
		}
		detail := identityHTTPRequest(t, f.handler, "GET", base+"/"+collection+"/"+strconv.FormatInt(id.ID, 10), "", f.viewerCookie, "", 200)
		if collection == "evaluations" && (!strings.Contains(detail.Body.String(), "historical-engine") || !strings.Contains(detail.Body.String(), "historical-inspector")) {
			t.Fatal("historical versions replaced with current engine")
		}
		if collection == "decisions" && strings.Contains(detail.Body.String(), `"engineVersion"`) {
			t.Fatal("unknown historic decision engine fabricated")
		}
		identityHTTPRequest(t, f.handler, "GET", base+"/"+collection+"/999999999", "", f.viewerCookie, "", 404)
		identityHTTPRequest(t, f.handler, "GET", base+"/"+collection+"/0", "", f.viewerCookie, "", 400)
		exported := identityHTTPRequest(t, f.handler, "GET", base+"/"+collection+"/export?limit=1", "", f.viewerCookie, "", 200)
		var export page
		if json.Unmarshal(exported.Body.Bytes(), &export) != nil || len(export.Items) != 1 || export.Scope.Mode != "filtered_page" || export.Scope.Snapshot != "re_read_at_export_time" || export.Scope.ReadAt.IsZero() || !export.Scope.HasMore || !strings.Contains(exported.Header().Get("Content-Disposition"), "attachment") {
			t.Fatal("page export falsely implies full history", collection)
		}
		for _, query := range []string{"limit=", "limit=0", "limit=201", "limit=1&limit=2", "before=invalid", "extra=true"} {
			identityHTTPRequest(t, f.handler, "GET", base+"/"+collection+"?"+query, "", f.viewerCookie, "", 400)
		}
	}
	for _, path := range []string{"decisions?kind=output", "decisions?outcome=unknown", "decisions?status=passed", "approvals?status=expired", "approvals?kind=tool", "evaluations?status=success", "regression-packs?status=passed"} {
		identityHTTPRequest(t, f.handler, "GET", base+"/"+path, "", f.viewerCookie, "", 400)
	}
	filtered := identityHTTPRequest(t, f.handler, "GET", base+"/decisions?kind=model&outcome=allow&limit=1", "", f.viewerCookie, "", 200)
	var p page
	if json.Unmarshal(filtered.Body.Bytes(), &p) != nil {
		t.Fatal("filtered DTO")
	}
	identityHTTPRequest(t, f.handler, "GET", base+"/decisions?kind=rag&outcome=allow&before="+p.NextCursor, "", f.viewerCookie, "", 400)
	identityHTTPRequest(t, f.handler, "GET", base+"/approvals?before="+p.NextCursor, "", f.viewerCookie, "", 400)
	legacy := identityHTTPRequest(t, f.handler, "GET", base+"/export", "", f.viewerCookie, "", 200)
	var overview struct {
		Scope struct {
			Mode            string                                   `json:"mode"`
			HistoryCoverage map[string]store.AISecurityHistoryWindow `json:"historyCoverage"`
		} `json:"scope"`
	}
	if json.Unmarshal(legacy.Body.Bytes(), &overview) != nil || overview.Scope.Mode != "bounded_preview" || len(overview.Scope.HistoryCoverage) != 3 || overview.Scope.HistoryCoverage["evaluations"].Limit != 30 {
		t.Fatal("legacy export omits bounded preview scope")
	}
}
