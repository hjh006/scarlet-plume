package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func TestGatewayDeniedRealClassificationProfilePersistsWithoutBilling(t *testing.T) {
	f, in := gatewayTestSetup(t, 100, 10, 100)
	c := aiTestConfig()
	c.Policy.RequiredInspectionVersion = aisecurity.InspectionVersion
	c.Policy.Inspection = &aisecurity.InspectionPolicy{Version: aisecurity.InspectionVersion, BuiltinPacks: []string{"credential-v1"}}
	cfg := gatewayTestConfig(100, 10, 100)
	cfg.CatalogDigest = aisecurity.ConfigDigest(c)
	cfg.Applications[0].ID, cfg.Applications[0].PrincipalID = "enterprise-app", "branch-user"
	if _, err := f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.user.ID, 1, cfg); err != nil {
		t.Fatal(err)
	}
	for index, input := range []string{"person@example.invalid", "api_key=syntheticcredential0123456789"} {
		now := time.Now().UTC()
		d := aisecurity.Evaluate(c, aisecurity.Request{RequestID: fmt.Sprintf("actual-classified-denial-%04d", index), Kind: "model", ApplicationID: "enterprise-app", PrincipalID: "branch-user", ModelID: "approved-model", Input: fmt.Sprintf(`{"prompt":%q}`, input), IssuedAt: now, ExpiresAt: now.Add(time.Minute)}, now)
		if d.Outcome != "deny" || len(d.Categories) != 1 {
			t.Fatal("fixture did not produce a real classified denial", d.Code)
		}
		profileIndex := slices.IndexFunc(d.Coverage, func(value string) bool {
			return strings.HasPrefix(value, "versioned_classification_profile:") && len(value) > 80
		})
		if profileIndex < 0 {
			t.Fatal("actual engine profile marker was not present")
		}
		a := aisecurity.AuditRecord{EventID: fmt.Sprintf("actual-classified-event-%04d", index), AttemptID: "actual-classified-attempt", RequestID: fmt.Sprintf("actual-classified-denial-%04d", index), Stage: "decision", ObservedAt: now, RuntimeProtocolVersion: aisecurity.RuntimeProtocolVersion, InspectionVersion: aisecurity.InspectionVersion, Decision: d, Execution: aisecurity.Execution{Outcome: "not_attempted"}}
		event := GatewayDeniedEvent{WorkspaceSlug: in.WorkspaceSlug, Audit: &a}
		if receipt, err := f.repo.RecordGatewayDenied(t.Context(), event); err != nil || !receipt.Accepted || !receipt.Verified || receipt.Replayed {
			t.Fatal("actual classified denial could not be recorded", receipt, err)
		}
		if receipt, err := f.repo.RecordGatewayDenied(t.Context(), event); err != nil || !receipt.Replayed {
			t.Fatal("real denial lost idempotence", receipt, err)
		}
		for _, coverage := range []string{"versioned_classification_profile:", "versioned_classification_profile:abc", "versioned_classification_profile:" + strings.Repeat("A", 64), "versioned_classification_profile:" + strings.Repeat("a", 63), "versioned_classification_profile:" + strings.Repeat("a", 65), "versioned_classification_profile:" + strings.Repeat("g", 64), "versioned_classification_profile:person@example.invalid", "other_long_profile_marker:" + strings.Repeat("a", 64), strings.Repeat("x", 200), "api_key=syntheticcredential0123456789"} {
			bad := a
			bad.Decision.Coverage = []string{coverage}
			if _, err := f.repo.RecordGatewayDenied(t.Context(), GatewayDeniedEvent{WorkspaceSlug: in.WorkspaceSlug, Audit: &bad}); !errors.Is(err, ErrInvalid) {
				t.Fatal("malformed or free-form coverage accepted", err)
			}
		}
		bad := a
		bad.Decision.Rules = []string{d.Coverage[profileIndex]}
		if _, err := f.repo.RecordGatewayDenied(t.Context(), GatewayDeniedEvent{WorkspaceSlug: in.WorkspaceSlug, Audit: &bad}); !errors.Is(err, ErrInvalid) {
			t.Fatal("profile exception escaped its coverage field", err)
		}
	}
	var reservations, spent, reserved int64
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM plume.ai_gateway_reservations WHERE workspace_id=$1),COALESCE(sum(spent_micros),0),COALESCE(sum(reserved_micros),0) FROM plume.ai_gateway_accounts WHERE workspace_id=$1`, f.ws.ID).Scan(&reservations, &spent, &reserved); err != nil || reservations != 0 || spent != 0 || reserved != 0 {
		t.Fatal("denial metadata changed accounting", reservations, spent, reserved, err)
	}
}
