package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func TestGatewayMediaVersionHTTPRejectsCallerContractAndPinsServerVersion(t *testing.T) {
	f := newIdentityHTTPFixture(t)
	machine := "/api/v1/internal/ai-gateway"
	cfg := gatewayHTTPConfig()
	cfg.Applications[0].BudgetMicros = 10000
	cfg.Applications[0].TokensPerMinute = 10000
	if _, err := f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.owner.ID, 0, cfg); err != nil {
		t.Fatal(err)
	}
	_, token, err := f.repo.CreateGatewayCredential(t.Context(), f.ws.ID, f.owner.ID, "app", "Media contract fixture")
	if err != nil {
		t.Fatal(err)
	}
	control := strings.Repeat("synthetic-media-control-", 3)
	t.Setenv("SCARLET_GATEWAY_CONTROL_TOKENS", gatewayHTTPJSON(t, map[string]string{f.ws.Slug: control}))
	input := store.GatewayAuthorizeInput{WorkspaceSlug: f.ws.Slug, TokenHash: sessionHash(token), ApplicationID: "app", PrincipalID: "principal", Kind: "model", RequestID: "media-version-http-request", ModelID: "model", RequestDigest: strings.Repeat("b", 64), CatalogDigest: cfg.CatalogDigest, InputBytes: 128, ProjectionBytes: 8, MediaBytes: 64, MediaDigests: []string{strings.Repeat("d", 64)}, MediaInspectionVersion: aisecurity.LegacyMediaInspectionVersion, MaxOutputTokens: 8}
	body := gatewayHTTPJSON(t, input)
	identityHTTPRequest(t, f.handler, "POST", machine+"/authorize", body, nil, control, 400)
	forged := strings.TrimSuffix(body, "}") + `,"requiredMediaInspectionVersion":"` + aisecurity.MediaInspectionVersion + `"}`
	identityHTTPRequest(t, f.handler, "POST", machine+"/authorize", forged, nil, control, 400)
	input.MediaInspectionVersion = aisecurity.MediaInspectionVersion
	body = gatewayHTTPJSON(t, input)
	forged = strings.TrimSuffix(body, "}") + `,"requiredMediaInspectionVersion":"` + aisecurity.LegacyMediaInspectionVersion + `"}`
	identityHTTPRequest(t, f.handler, "POST", machine+"/authorize", forged, nil, control, 400)
	response := identityHTTPRequest(t, f.handler, "POST", machine+"/authorize", body, nil, control, 200)
	if strings.Contains(response.Body.String(), "requiredMediaInspectionVersion") {
		t.Fatal("public authorization exposed a caller-settable contract")
	}
	var admission store.GatewayAuthorization
	if json.Unmarshal(response.Body.Bytes(), &admission) != nil {
		t.Fatal("authorization response")
	}
	audit := aisecurity.AuditRecord{AttemptID: "media-version-http-attempt", EventID: "media-version-http-event", RequestID: input.RequestID, Stage: "completed", RuntimeProtocolVersion: aisecurity.RuntimeProtocolVersion, InspectionVersion: aisecurity.InspectionVersion, ObservedAt: time.Now().UTC(), Decision: aisecurity.Decision{Outcome: "allow", Kind: "model", ApplicationID: "app", PrincipalID: "principal", RequestDigest: input.RequestDigest, CatalogDigest: cfg.CatalogDigest, MediaDigests: input.MediaDigests, MediaInspectionVersion: aisecurity.LegacyMediaInspectionVersion}, Execution: aisecurity.Execution{Attempted: true, Outcome: "succeeded", HTTPStatus: 200}}
	settlement := store.GatewaySettlement{WorkspaceSlug: f.ws.Slug, ReservationID: admission.ReservationID, RequestDigest: input.RequestDigest, Audit: &audit}
	body = gatewayHTTPJSON(t, settlement)
	identityHTTPRequest(t, f.handler, "POST", machine+"/settle", body, nil, control, 403)
	forged = strings.TrimSuffix(body, "}") + `,"requiredMediaInspectionVersion":"` + aisecurity.LegacyMediaInspectionVersion + `"}`
	identityHTTPRequest(t, f.handler, "POST", machine+"/settle", forged, nil, control, 400)
	audit.Decision.MediaInspectionVersion = aisecurity.MediaInspectionVersion
	identityHTTPRequest(t, f.handler, "POST", machine+"/settle", gatewayHTTPJSON(t, settlement), nil, control, 200)
}
