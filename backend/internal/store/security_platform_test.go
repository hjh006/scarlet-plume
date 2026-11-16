package store

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
)

func TestSecurityPostureRequiresCurrentPermissionAndReviewedClassification(t *testing.T) {
	asset := Asset{ID: 1, Name: "Customers", Category: "sensitive_personal", BusinessLevel: "confidential", OwnerTeam: "IT"}
	classification := &ScenarioClassification{Usable: true}
	pg := &scenarioPostgresSource{Current: true, Complete: true, Inspection: PostgresInspection{ObservedAt: time.Now(), Table: collector.PostgreSQLTable{PublicSchemaUsage: true, PublicSelect: true}}}
	find := func(items []SecurityPostureFinding, code string) *SecurityPostureFinding {
		for i := range items {
			if items[i].Code == code {
				return &items[i]
			}
		}
		return nil
	}
	items := securityPostureFindings(asset, classification, pg)
	if f := find(items, "public_select"); f == nil || f.Severity != "high" || f.EvidenceState != "observed" {
		t.Fatalf("missing observed high exposure: %+v", items)
	}
	pg.Current = false
	items = securityPostureFindings(asset, classification, pg)
	if find(items, "public_select") != nil || find(items, "stale_permission_source") == nil {
		t.Fatalf("stale source used as current exposure: %+v", items)
	}
	pg.Current = true
	classification.Usable = false
	items = securityPostureFindings(asset, classification, pg)
	if f := find(items, "public_select"); f == nil || f.Severity != "medium" {
		t.Fatalf("unconfirmed classification raised severity: %+v", items)
	}
	pg.Inspection.Table.PublicSchemaUsage = false
	if find(securityPostureFindings(asset, classification, pg), "public_select") != nil {
		t.Fatal("PUBLIC grant without schema usage treated as readable")
	}
}

func TestSecurityPlatformNetworkRevisionIsolationAndFrozenEvidence(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	other := assessmentSourceTestStore(t)
	asset, err := f.repo.CreateAsset(ctx, f.ws.ID, "Customer records", "CRM", "Data team")
	if err != nil {
		t.Fatal(err)
	}
	in := SecurityNetworkBindingInput{Site: "Shanghai", Circuit: "circuit-01", VRF: "enterprise", ApplicationRef: "enterprise-ai", AssetIDs: []int64{asset.ID}, ActivityIDs: []int64{}, Region: "CN", Receiver: "Headquarters", Purpose: "Customer service\nAuthorized AI workflow", EvidenceRef: "approved-design-v1"}
	if _, err = f.repo.SaveSecurityNetworkBinding(ctx, other.ws.ID, f.user.ID, in); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace edit: %v", err)
	}
	foreign := in
	foreign.AssetIDs = []int64{asset.ID}
	if _, err = other.repo.SaveSecurityNetworkBinding(ctx, other.ws.ID, other.user.ID, foreign); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign asset linked: %v", err)
	}
	binding, err := f.repo.SaveSecurityNetworkBinding(ctx, f.ws.ID, f.user.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if binding.ID < 1 || binding.Revision != 1 || binding.EvidenceType != "declared" {
		t.Fatalf("invalid binding %+v", binding)
	}
	change, err := f.repo.CreateSecurityNetworkChange(ctx, f.ws.ID, f.user.ID, SecurityNetworkChangeInput{BindingID: binding.ID, ExpectedRevision: 1, ProposedRegion: "SG", ProposedApplicationRef: "new-model", Reason: "New regional model"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(change.AffectedAssetIDs, []int64{asset.ID}) || len(change.Warnings) < 3 {
		t.Fatalf("missing frozen impact %+v", change)
	}
	if _, err = f.repo.ReviewSecurityNetworkChange(ctx, f.ws.ID, f.user.ID, change.ID, "accepted", "self review"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("self review permitted: %v", err)
	}
	if _, err = f.pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role) VALUES($1,$2,'reviewer')`, f.ws.ID, other.user.ID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM plume.memberships WHERE workspace_id=$1 AND user_id=$2`, f.ws.ID, other.user.ID)
	}()
	reviewed, err := f.repo.ReviewSecurityNetworkChange(ctx, f.ws.ID, other.user.ID, change.ID, "accepted", "Reviewed planned business impact only\nNetwork execution requires separate evidence.")
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.Status != "accepted" || reviewed.Reviewer == nil || *reviewed.Reviewer != other.user.ID {
		t.Fatalf("review missing %+v", reviewed)
	}
	if _, err = f.repo.ReviewSecurityNetworkChange(ctx, f.ws.ID, other.user.ID, change.ID, "accepted", "replay"); !errors.Is(err, ErrConflict) {
		t.Fatalf("review replay accepted: %v", err)
	}
	if _, err = f.repo.SaveSecurityNetworkBinding(ctx, f.ws.ID, other.user.ID, in); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reviewer edited configuration: %v", err)
	}
	stale, err := f.repo.CreateSecurityNetworkChange(ctx, f.ws.ID, f.user.ID, SecurityNetworkChangeInput{BindingID: binding.ID, ExpectedRevision: 1, ProposedRegion: "EU", ProposedApplicationRef: "model-eu", Reason: "Proposal before revision"})
	if err != nil {
		t.Fatal(err)
	}
	in.ID = binding.ID
	in.ExpectedRevision = 1
	in.Region = "US"
	if _, err = f.repo.SaveSecurityNetworkBinding(ctx, f.ws.ID, f.user.ID, in); err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.SaveSecurityNetworkBinding(ctx, f.ws.ID, f.user.ID, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale overwrite accepted: %v", err)
	}
	if _, err = f.repo.ReviewSecurityNetworkChange(ctx, f.ws.ID, other.user.ID, stale.ID, "accepted", "stale binding"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale approval accepted: %v", err)
	}
	page, err := f.repo.SecurityPlatform(ctx, f.ws.ID, f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Capabilities) != 8 || page.Counts["assets"] != 1 || len(page.NetworkBindings) != 1 || page.NetworkBindings[0].ID != binding.ID {
		t.Fatalf("wrong platform scope %+v", page)
	}
	snapshot, err := f.repo.CaptureSecurityPlatform(ctx, f.ws.ID, f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.repo.SecurityPlatformSnapshot(ctx, f.ws.ID, f.user.ID, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if assessmentDigest(scenarioCanonicalJSON(raw)) != snapshot.Digest {
		t.Fatal("stored evidence digest changed through jsonb encoding")
	}
	if _, err = f.repo.SecurityPlatformSnapshot(ctx, other.ws.ID, other.user.ID, snapshot.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace snapshot leaked: %v", err)
	}
	if _, err = f.repo.CreateAsset(ctx, f.ws.ID, "Later asset", "ERP", "IT"); err != nil {
		t.Fatal(err)
	}
	var frozen SecurityPlatform
	if err = json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen.Counts["assets"] != 1 {
		t.Fatal("frozen snapshot mutated")
	}
	if _, err = f.repo.CaptureSecurityPlatform(ctx, f.ws.ID, other.user.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reviewer captured snapshot: %v", err)
	}
	for _, c := range frozen.Capabilities {
		if c.ID == "ai_data" && c.Stage != "needs_configuration" {
			t.Fatalf("unconfigured AI marked active: %+v", c)
		}
	}
}
