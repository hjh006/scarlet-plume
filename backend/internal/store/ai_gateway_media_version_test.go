package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

func mediaVersionInput(in GatewayAuthorizeInput, version string) GatewayAuthorizeInput {
	in.InputBytes = 64
	in.ProjectionBytes = 10
	in.MediaBytes = 32
	in.MediaDigests = []string{strings.Repeat("d", 64)}
	in.MediaInspectionVersion = version
	return in
}
func mediaVersionSettlement(in GatewayAuthorizeInput, g GatewayAuthorization, version string, attempted bool, outcome string) GatewaySettlement {
	out := gatewayTestSettlement(in, g, attempted, outcome)
	out.Audit.RuntimeProtocolVersion = aisecurity.RuntimeProtocolVersion
	out.Audit.InspectionVersion = aisecurity.InspectionVersion
	out.Audit.Decision.MediaDigests = []string{strings.Repeat("d", 64)}
	out.Audit.Decision.MediaInspectionVersion = version
	return out
}
func TestGatewayMediaCurrentAdmissionAndFrozenOutputContract(t *testing.T) {
	f, base := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	ctx := t.Context()
	for _, version := range []string{aisecurity.LegacyMediaInspectionVersion, "future-unverified-media"} {
		bad := mediaVersionInput(base, version)
		if _, err := f.repo.AuthorizeGateway(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Fatal("non-current evidence entered new admission", version, err)
		}
	}
	in := mediaVersionInput(base, aisecurity.MediaInspectionVersion)
	g, err := f.repo.AuthorizeGateway(ctx, in)
	if err != nil {
		t.Fatal("current v2 evidence cannot enter actual reservation", err)
	}
	var required string
	if err = f.pool.QueryRow(ctx, `SELECT authorized->>'requiredMediaInspectionVersion' FROM plume.ai_gateway_reservations WHERE workspace_id=$1 AND id=$2`, f.ws.ID, g.ReservationID).Scan(&required); err != nil || required != aisecurity.MediaInspectionVersion {
		t.Fatal("server did not freeze current output media contract", required, err)
	}
	wire, _ := json.Marshal(g)
	if strings.Contains(string(wire), "requiredMediaInspectionVersion") {
		t.Fatal("private server contract exposed as public authorization control")
	}
	old := mediaVersionSettlement(in, g, aisecurity.LegacyMediaInspectionVersion, true, "succeeded")
	if err = f.repo.SettleGateway(ctx, old); !errors.Is(err, ErrForbidden) {
		t.Fatal("new media call completed with old confidence semantics", err)
	}
	gatewayAssertUsage(t, f, g.MaxCostMicros, 0)
	current := mediaVersionSettlement(in, g, aisecurity.MediaInspectionVersion, true, "succeeded")
	if err = f.repo.SettleGateway(ctx, current); err != nil {
		t.Fatal(err)
	}
	gatewayAssertUsage(t, f, 0, g.MaxCostMicros)
	if err = f.repo.SettleGateway(ctx, current); err != nil {
		t.Fatal("same original v2 receipt not idempotent", err)
	}
}
func TestGatewayMediaTextInputCannotReturnLegacyOutputEvidence(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	ctx := t.Context()
	g, err := f.repo.AuthorizeGateway(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	old := mediaVersionSettlement(in, g, aisecurity.LegacyMediaInspectionVersion, true, "succeeded")
	if err = f.repo.SettleGateway(ctx, old); !errors.Is(err, ErrForbidden) {
		t.Fatal("text input bypassed current media output contract", err)
	}
	gatewayAssertUsage(t, f, g.MaxCostMicros, 0)
	current := mediaVersionSettlement(in, g, aisecurity.MediaInspectionVersion, true, "succeeded")
	if err = f.repo.SettleGateway(ctx, current); err != nil {
		t.Fatal("current media output from text input rejected", err)
	}
	gatewayAssertUsage(t, f, 0, g.MaxCostMicros)
}

// Model the exact persisted JSON format written by a pre-v2 control plane.
// This is a fixture conversion, not a production path for creating v1 calls.
func legacyMediaReservation(t *testing.T, f assessmentSourceFixture, in GatewayAuthorizeInput) (GatewayAuthorizeInput, GatewayAuthorization) {
	t.Helper()
	ctx := t.Context()
	current := mediaVersionInput(in, aisecurity.MediaInspectionVersion)
	g, err := f.repo.AuthorizeGateway(ctx, current)
	if err != nil {
		t.Fatal(err)
	}
	old := mediaVersionInput(in, aisecurity.LegacyMediaInspectionVersion)
	g.MediaInspectionVersion = aisecurity.LegacyMediaInspectionVersion
	raw, _ := json.Marshal(g)
	if _, err = f.pool.Exec(ctx, `UPDATE plume.ai_gateway_reservations SET authorized=$3,request_hash=$4 WHERE workspace_id=$1 AND id=$2`, f.ws.ID, g.ReservationID, raw, gatewayDigest(old)); err != nil {
		t.Fatal(err)
	}
	return old, g
}
func TestGatewayMediaLegacyReservationKeepsOriginalFactsAndUnknownFees(t *testing.T) {
	for _, outcome := range []string{"succeeded", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			f, base := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
			ctx := t.Context()
			in, g := legacyMediaReservation(t, f, base)
			relabeled := mediaVersionSettlement(in, g, aisecurity.MediaInspectionVersion, true, outcome)
			if err := f.repo.SettleGateway(ctx, relabeled); !errors.Is(err, ErrForbidden) {
				t.Fatal("historical v1 reservation was relabeled v2", err)
			}
			gatewayAssertUsage(t, f, g.MaxCostMicros, 0)
			old := mediaVersionSettlement(in, g, aisecurity.LegacyMediaInspectionVersion, true, outcome)
			if err := f.repo.SettleGateway(ctx, old); err != nil {
				t.Fatal("historical execution lost settlement", err)
			}
			var state, recordedVersion string
			var marker bool
			if err := f.pool.QueryRow(ctx, `SELECT state,last_audit->'decision'->>'mediaInspectionVersion',authorized ? 'requiredMediaInspectionVersion' FROM plume.ai_gateway_reservations WHERE workspace_id=$1 AND id=$2`, f.ws.ID, g.ReservationID).Scan(&state, &recordedVersion, &marker); err != nil || recordedVersion != aisecurity.LegacyMediaInspectionVersion || marker {
				t.Fatal("historical media fact rewritten", state, recordedVersion, marker, err)
			}
			if outcome == "succeeded" {
				if state != "settled" {
					t.Fatal(state)
				}
				gatewayAssertUsage(t, f, 0, g.MaxCostMicros)
			} else {
				if state != "unknown" {
					t.Fatal(state)
				}
				gatewayAssertUsage(t, f, g.MaxCostMicros, 0)
			}
			if aiRuntimeCompatibility(*old.Audit) != "unsupported" {
				t.Fatal("old v1 report claimed current minimum-confidence verification")
			}
		})
	}
}
func TestGatewayMediaLegacyTextReservationCanSettleOriginalV1Output(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	ctx := t.Context()
	g, err := f.repo.AuthorizeGateway(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(g)
	if _, err = f.pool.Exec(ctx, `UPDATE plume.ai_gateway_reservations SET authorized=$3 WHERE workspace_id=$1 AND id=$2`, f.ws.ID, g.ReservationID, raw); err != nil {
		t.Fatal(err)
	}
	current := mediaVersionSettlement(in, g, aisecurity.MediaInspectionVersion, true, "succeeded")
	if err = f.repo.SettleGateway(ctx, current); !errors.Is(err, ErrForbidden) {
		t.Fatal("legacy unknown contract silently upgraded", err)
	}
	old := mediaVersionSettlement(in, g, aisecurity.LegacyMediaInspectionVersion, true, "succeeded")
	if err = f.repo.SettleGateway(ctx, old); err != nil {
		t.Fatal("legacy text-to-media fee fact dropped", err)
	}
	gatewayAssertUsage(t, f, 0, g.MaxCostMicros)
}

func TestGatewayMediaLegacyTaskCanCompleteOriginalSettledReceipt(t *testing.T) {
	f, base := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	ctx := t.Context()
	task, err := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, taskTestInput(t, f, base))
	if err != nil {
		t.Fatal(err)
	}
	fact := taskTestFact(t, f, task, base, "legacy-media-task-step")
	if _, err = f.repo.AdmitGatewayTask(ctx, fact); err != nil {
		t.Fatal(err)
	}
	base.TaskContext = fact.TaskContext
	base.RequestID = fact.TaskStepID
	base.RequestDigest = fact.TaskStepDigest
	in, g := legacyMediaReservation(t, f, base)
	receipt := taskTestSettlement(t, f, in, g, true, "succeeded")
	receipt.Audit.Decision.MediaDigests = in.MediaDigests
	receipt.Audit.Decision.MediaInspectionVersion = aisecurity.LegacyMediaInspectionVersion
	if err = f.repo.SettleGateway(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(receipt.Audit)
	if _, err = f.repo.CompleteGatewayTask(ctx, gatewaygovernance.TaskCompleteInput{TaskCheckInput: fact, Audit: raw}); err != nil {
		t.Fatal("historical v1 task lost completion after valid settlement", err)
	}
	detail, err := f.repo.GatewayTaskDetail(ctx, f.ws.ID, f.user.ID, task.ID, 10)
	if err != nil || detail.Task.ScopeDigest != task.ScopeDigest || detail.Task.SpentMicros != g.MaxCostMicros || detail.Task.ReservedMicros != 0 || detail.Steps[0].Status != "completed" {
		t.Fatal("historical task facts changed", detail, err)
	}
	var version string
	if err = f.pool.QueryRow(ctx, `SELECT payload::jsonb->'audit'->'decision'->>'mediaInspectionVersion' FROM plume.ai_gateway_audit WHERE workspace_id=$1 AND action='gateway.task_step_completed' AND target=$2 ORDER BY sequence DESC LIMIT 1`, f.ws.ID, task.ID).Scan(&version); err != nil || version != aisecurity.LegacyMediaInspectionVersion {
		t.Fatal("task history silently promoted old confidence evidence", version, err)
	}
}
