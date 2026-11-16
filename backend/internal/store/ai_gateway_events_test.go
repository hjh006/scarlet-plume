package store

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func gatewayTestDenied(in GatewayAuthorizeInput, grant GatewayAuthorization) GatewayDeniedEvent {
	st := gatewayTestSettlement(in, grant, false, "not_attempted")
	a := st.Audit
	a.Stage = "decision"
	a.Decision.Outcome = "deny"
	a.Decision.Code = "model_input_data_denied"
	a.Decision.CatalogVersion = "catalog-v1"
	a.Decision.Rules = []string{"model_input_data_denied"}
	a.Decision.Coverage = []string{"bounded_four_format_categories"}
	a.RuntimeProtocolVersion = aisecurity.RuntimeProtocolVersion
	a.InspectionVersion = aisecurity.InspectionVersion
	return GatewayDeniedEvent{WorkspaceSlug: in.WorkspaceSlug, Audit: a}
}
func TestGatewayDeniedEventsDedupWithoutReservationOrBillingEffects(t *testing.T) {
	f, in := gatewayTestSetup(t, 100, 10, 100)
	grant, e := f.repo.AuthorizeGateway(t.Context(), in)
	if e != nil {
		t.Fatal(e)
	}
	denied := gatewayTestDenied(in, grant)
	var wg sync.WaitGroup
	receipts := make(chan GatewayDeniedReceipt, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, e := f.repo.RecordGatewayDenied(t.Context(), denied)
			if e != nil {
				t.Error(e)
				return
			}
			receipts <- receipt
		}()
	}
	wg.Wait()
	close(receipts)
	appended := 0
	for receipt := range receipts {
		if !receipt.Accepted || !receipt.Verified || len(receipt.VerificationReasons) > 0 {
			t.Errorf("receipt=%+v", receipt)
		}
		if !receipt.Replayed {
			appended++
		}
	}
	if appended != 1 {
		t.Fatalf("duplicate events appended %d times", appended)
	}
	gatewayAssertUsage(t, f, 10, 0)
	var requests, tokens, count int64
	if e = f.pool.QueryRow(t.Context(), `SELECT sum(requests),sum(tokens) FROM plume.ai_gateway_windows WHERE workspace_id=$1`, f.ws.ID).Scan(&requests, &tokens); e != nil {
		t.Fatal(e)
	}
	if e = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM plume.ai_gateway_reservations WHERE workspace_id=$1`, f.ws.ID).Scan(&count); e != nil {
		t.Fatal(e)
	}
	if requests != 1 || tokens != 10 || count != 1 {
		t.Fatalf("denial changed quota requests=%d tokens=%d reservations=%d", requests, tokens, count)
	}
	page, e := f.repo.GatewayAudit(t.Context(), f.ws.ID, f.user.ID, 0, 500)
	if e != nil || !page.Valid || len(page.Items) != 4 || page.Items[3].Action != "gateway.call_denied" {
		t.Fatalf("denied chain %+v %v", page, e)
	}
	var payload struct {
		Verified       bool   `json:"verified"`
		BillingEffect  string `json:"billingEffect"`
		EvidenceStatus string `json:"evidenceStatus"`
	}
	if e = json.Unmarshal(page.Items[3].Payload, &payload); e != nil || !payload.Verified || payload.BillingEffect != "none" || payload.EvidenceStatus != "verified_denial_metadata" {
		t.Fatalf("denied evidence %+v %v", payload, e)
	}
	changed := denied
	copyAudit := *denied.Audit
	changed.Audit = &copyAudit
	changed.Audit.Decision.Code = "changed_denial"
	if _, e = f.repo.RecordGatewayDenied(t.Context(), changed); !errors.Is(e, ErrConflict) {
		t.Fatalf("event ID content change %v", e)
	}
	// Separate denial deduplication cannot make a pending reservation look settled.
	if e = f.repo.SettleGateway(t.Context(), gatewayTestSettlement(in, grant, true, "succeeded")); e != nil {
		t.Fatal(e)
	}
	gatewayAssertUsage(t, f, 0, 10)
}
func TestGatewayDeniedUnknownVersionsRemainUnverifiedAndMetadataOnly(t *testing.T) {
	f, in := gatewayTestSetup(t, 100, 10, 100)
	denied := gatewayTestDenied(in, GatewayAuthorization{ReservationID: "no-reservation"})
	denied.Audit.RuntimeProtocolVersion = "future-runtime-v99"
	denied.Audit.InspectionVersion = "future-inspector-v99"
	denied.Audit.MCPProtocolVersion = "future-mcp-v99"
	denied.Audit.Decision.CatalogDigest = strings.Repeat("c", 64)
	denied.Audit.Decision.PrincipalID = "unknown-principal"
	receipt, e := f.repo.RecordGatewayDenied(t.Context(), denied)
	if e != nil || receipt.Verified || !receipt.Accepted {
		t.Fatalf("unverified metadata %+v %v", receipt, e)
	}
	for _, reason := range []string{"runtime_version_unrecognized", "inspection_version_unrecognized", "mcp_version_unrecognized", "catalog_not_current", "identity_not_current"} {
		if !slices.Contains(receipt.VerificationReasons, reason) {
			t.Errorf("missing %s", reason)
		}
	}
	gatewayAssertUsage(t, f, 0, 0)
	rows, e := f.repo.GatewayReservations(t.Context(), f.ws.ID, f.user.ID, "", 100)
	if e != nil || len(rows) != 0 {
		t.Fatalf("denial manufactured reservation %v %v", rows, e)
	}
	for _, mutate := range []func(*aisecurity.AuditRecord){func(a *aisecurity.AuditRecord) { a.Stage = "completed" }, func(a *aisecurity.AuditRecord) { a.Decision.Outcome = "allow" }, func(a *aisecurity.AuditRecord) { a.Execution.Attempted = true }, func(a *aisecurity.AuditRecord) { a.Execution.Outcome = "succeeded" }, func(a *aisecurity.AuditRecord) { a.Decision.Code = "raw body with spaces" }, func(a *aisecurity.AuditRecord) { a.Decision.Rules = []string{"person@example.com"} }} {
		bad := denied
		copyAudit := *denied.Audit
		bad.Audit = &copyAudit
		mutate(bad.Audit)
		if _, e = f.repo.RecordGatewayDenied(t.Context(), bad); !errors.Is(e, ErrInvalid) {
			t.Fatalf("invalid metadata accepted %v", e)
		}
	}
	other := assessmentSourceTestStore(t)
	cross := denied
	cross.WorkspaceSlug = other.ws.Slug
	receipt, e = other.repo.RecordGatewayDenied(t.Context(), cross)
	if e != nil || receipt.Verified {
		t.Fatalf("foreign identity treated as verified %+v %v", receipt, e)
	}
	// The authenticated machine chooses its own workspace: an event never writes into another workspace implicitly.
	page, e := f.repo.GatewayAudit(t.Context(), f.ws.ID, f.user.ID, 0, 500)
	if e != nil || len(page.Items) != 3 {
		t.Fatalf("foreign event changed original chain %+v %v", page, e)
	}
}
