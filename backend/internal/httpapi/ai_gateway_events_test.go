package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func TestGatewayDeniedEventsMachineHTTP(t *testing.T) {
	f := newIdentityHTTPFixture(t)
	cfg := gatewayHTTPConfig()
	if _, e := f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.owner.ID, 0, cfg); e != nil {
		t.Fatal(e)
	}
	token := strings.Repeat("e", 40)
	t.Setenv("SCARLET_GATEWAY_CONTROL_TOKENS", gatewayHTTPJSON(t, map[string]string{f.ws.Slug: token}))
	path := "/api/v1/internal/ai-gateway/events"
	in := store.GatewayDeniedEvent{WorkspaceSlug: f.ws.Slug, Audit: &aisecurity.AuditRecord{AttemptID: "deny-http-attempt-0001", EventID: "deny-http-event-0001", RequestID: "deny-http-request-0001", Stage: "decision", RuntimeProtocolVersion: aisecurity.RuntimeProtocolVersion, InspectionVersion: aisecurity.InspectionVersion, ObservedAt: time.Now().UTC(), Decision: aisecurity.Decision{Outcome: "deny", Code: "model_input_data_denied", CatalogVersion: "catalog-v1", CatalogDigest: cfg.CatalogDigest, RequestDigest: strings.Repeat("b", 64), ApplicationID: "app", PrincipalID: "principal", Kind: "model", Rules: []string{"model_input_data_denied"}}, Execution: aisecurity.Execution{Outcome: "not_attempted"}}}
	body := gatewayHTTPJSON(t, in)
	identityHTTPRequest(t, f.handler, "POST", path, body, nil, "", 401)
	accepted := identityHTTPRequest(t, f.handler, "POST", path, body, nil, token, 200)
	var receipt store.GatewayDeniedReceipt
	if e := json.Unmarshal(accepted.Body.Bytes(), &receipt); e != nil || !receipt.Verified || receipt.Replayed {
		t.Fatalf("denied receipt %+v %v", receipt, e)
	}
	replay := identityHTTPRequest(t, f.handler, "POST", path, body, nil, token, 200)
	if e := json.Unmarshal(replay.Body.Bytes(), &receipt); e != nil || !receipt.Replayed {
		t.Fatal("denied replay not deduplicated")
	}
	cross := in
	cross.WorkspaceSlug = "another-workspace"
	identityHTTPRequest(t, f.handler, "POST", path, gatewayHTTPJSON(t, cross), nil, token, 503)
	var arbitrary map[string]any
	_ = json.Unmarshal([]byte(body), &arbitrary)
	arbitrary["audit"].(map[string]any)["rawBody"] = "person@example.com"
	identityHTTPRequest(t, f.handler, "POST", path, gatewayHTTPJSON(t, arbitrary), nil, token, 400)
	in.Audit.EventID += "future"
	in.Audit.RuntimeProtocolVersion = "future-runtime-v99"
	future := identityHTTPRequest(t, f.handler, "POST", path, gatewayHTTPJSON(t, in), nil, token, 200)
	if e := json.Unmarshal(future.Body.Bytes(), &receipt); e != nil || receipt.Verified || len(receipt.VerificationReasons) == 0 {
		t.Fatal("unknown version marked verified")
	}
	page, e := f.repo.GatewayAudit(t.Context(), f.ws.ID, f.owner.ID, 0, 500)
	if e != nil || !page.Valid || len(page.Items) != 3 || page.Items[1].Action != "gateway.call_denied" || page.Items[2].Action != "gateway.call_denied" {
		t.Fatalf("machine denial not exported %+v %v", page, e)
	}
}
