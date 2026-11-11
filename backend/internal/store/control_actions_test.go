package store

import (
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

func operationsPostgresFixture(t *testing.T, f *operationsFixture) (AutomationJob, ManagedPostgresInput, Asset, Check) {
	t.Helper()
	ctx, repo, ws := f.ctx, f.repo, f.workspace
	config := ManagedPostgresInput{Host: "fixture-db", Port: 5432, Database: "app_db", User: "reader", Schemas: []string{"inventory"}, IntervalMinutes: 5, EncryptedPassword: []byte("fixture")}
	job, err := repo.CreateManagedPostgresJob(ctx, ws.ID, f.owner.ID, "Scoped PostgreSQL", operationsTestDigest(f.slug+"-pg"), config)
	f.must(t, err)
	table := collector.PostgreSQLTable{Asset: collector.Asset{SourceRef: "inventory.actual_contacts", Name: "actual_contacts"}, Schema: "inventory", Owner: "source_owner", MetadataStatus: "complete", Columns: []collector.PostgreSQLColumn{{Name: "value", Type: "text", BaseType: "text", Ordinal: 1, CanSelect: true}}, Grants: []collector.PostgreSQLGrant{{Grantee: "PUBLIC", Grantor: "source_owner", Public: true, Privilege: "SELECT", SchemaUsage: true}}, PublicSelect: true, PublicSchemaUsage: true, PublicSelectColumns: []string{}, Constraints: []collector.PostgreSQLConstraint{}, Warnings: []string{}}
	f.must(t, repo.QueueManagedPostgresJob(ctx, ws.ID, f.owner.ID, job.ID))
	claim, err := repo.ClaimManagedPostgresJob(ctx, f.slug+"-claim", time.Minute)
	f.must(t, err)
	if claim == nil || claim.JobID != job.ID {
		t.Fatal("could not claim fixture source")
	}
	_, err = repo.FinishManagedPostgresJob(ctx, *claim, AutomationDelivery{Digest: operationsTestDigest(f.slug + "-scan"), ObservedAt: time.Now().UTC(), Assets: []CollectedAsset{{SourceRef: table.SourceRef, Name: table.Name}}, PostgresTables: []collector.PostgreSQLTable{table}})
	f.must(t, err)
	assets, err := repo.ListAssets(ctx, ws.ID, 0, 20)
	f.must(t, err)
	if len(assets) != 1 {
		t.Fatalf("unexpected fixture assets: %+v", assets)
	}
	asset := assets[0]
	_, err = f.pool.Exec(ctx, `UPDATE plume.assets SET name='Display name is not a SQL identifier' WHERE id=$1`, asset.ID)
	f.must(t, err)
	check, err := repo.CreateCheck(ctx, ws.ID, f.owner.ID, CheckInput{AssetID: asset.ID, RequirementCode: "G03", OwnerTeam: "Security", Note: "Observed public table grant"})
	f.must(t, err)
	return job, config, asset, check
}

func TestControlActionsIndependentApprovalScopedReceiptsAndPostgresTTL(t *testing.T) {
	f := newOperationsFixture(t)
	ctx, repo, ws := f.ctx, f.repo, f.workspace
	job, config, asset, check := operationsPostgresFixture(t, f)
	token := operationsTestDigest(f.slug + "-control")
	source, err := repo.CreateOperationsSource(ctx, ws.ID, f.owner.ID, OperationsSourceInput{Name: "PostgreSQL control", Kind: "postgres_control", OwnerTeam: "Security", JobID: job.ID}, token)
	f.must(t, err)
	otherToken := operationsTestDigest(f.slug + "-other-control")
	_, err = repo.CreateOperationsSource(ctx, ws.ID, f.owner.ID, OperationsSourceInput{Name: "Other PostgreSQL control", Kind: "postgres_control", OwnerTeam: "Security", JobID: job.ID}, otherToken)
	f.must(t, err)
	input := ControlActionInput{Kind: "revoke_select", OperationsSourceID: source.ID, AssetID: asset.ID, CheckID: check.ID, Reason: "Remove observed direct table SELECT", DurationMinutes: 5}
	action, err := repo.CreateControlAction(ctx, ws.ID, f.owner.ID, input)
	f.must(t, err)
	if action.Spec.Host != config.Host || action.Spec.Database != config.Database || action.Spec.Schema != "inventory" || action.Spec.Table != "actual_contacts" || action.Spec.Role != "PUBLIC" || !action.Spec.ExpectedSelect {
		t.Fatalf("target not derived from real source metadata: %+v", action.Spec)
	}
	_, err = repo.CreateControlAction(ctx, ws.ID, f.viewer.ID, input)
	f.want(t, err, ErrForbidden)
	_, err = repo.ApproveControlAction(ctx, ws.ID, f.owner.ID, action.ID, action.Revision)
	f.want(t, err, ErrForbidden)
	f.must(t, repo.AddEvidence(ctx, ws.ID, f.contributor.ID, check.ID, EvidenceInput{Kind: "observation", SourceSystem: "fixture", SourceRecord: "contributed", Summary: "Contributor cannot independently approve", ObservedAt: time.Now().UTC()}))
	_, err = repo.ApproveControlAction(ctx, ws.ID, f.contributor.ID, action.ID, action.Revision)
	f.want(t, err, ErrForbidden)
	_, err = repo.ApproveControlAction(ctx, ws.ID, f.foreign.ID, action.ID, action.Revision)
	f.want(t, err, ErrNotFound)
	action, err = repo.ApproveControlAction(ctx, ws.ID, f.reviewer.ID, action.ID, action.Revision)
	f.must(t, err)
	if action.Status != "approved" || action.ApprovedBy != f.reviewer.Username {
		t.Fatal("independent approval missing")
	}
	other, err := repo.ClaimPostgresControl(ctx, otherToken, "other-lease")
	f.must(t, err)
	if other != nil {
		t.Fatal("another executor claimed scoped action")
	}
	command, err := repo.ClaimPostgresControl(ctx, token, "original-lease")
	f.must(t, err)
	if command == nil || command.ID != action.ID || command.Phase != "apply" || command.Lease != "original-lease" {
		t.Fatalf("bad command %+v", command)
	}
	had, has := true, false
	receipt := ControlReceipt{ActionID: command.ID, Revision: command.Revision, Lease: command.Lease, Result: ControlResult{Phase: "apply", Success: true, Code: "select_revoked", HadSelect: &had, HasSelect: &has, ObservedAt: time.Now().UTC()}}
	f.want(t, repo.RecordControlReceipt(ctx, "postgres_control", otherToken, receipt), ErrForbidden)
	wrongLease := receipt
	wrongLease.Lease = "incorrect-lease"
	f.want(t, repo.RecordControlReceipt(ctx, "postgres_control", token, wrongLease), ErrConflict)
	f.must(t, repo.RecordControlReceipt(ctx, "postgres_control", token, receipt))
	f.must(t, repo.RecordControlReceipt(ctx, "postgres_control", token, receipt))
	changed := receipt
	changed.Result.ObservedAt = changed.Result.ObservedAt.Add(time.Microsecond)
	f.want(t, repo.RecordControlReceipt(ctx, "postgres_control", token, changed), ErrConflict)
	action, err = repo.ControlAction(ctx, ws.ID, f.owner.ID, action.ID)
	f.must(t, err)
	if action.Status != "applied" {
		t.Fatal("execution receipt not recorded")
	}
	detail, err := repo.CheckDetail(ctx, ws.ID, f.owner.ID, check.ID)
	f.must(t, err)
	if detail.Status == "verified" {
		t.Fatal("execution receipt marked independent review complete")
	}
	found := 0
	for _, e := range detail.Evidence {
		if e.SourceSystem == "受控处置执行器" {
			found++
			if e.Kind != "remediation" {
				t.Fatal("execution receipt should only create remediation evidence")
			}
		}
	}
	if found != 1 {
		t.Fatalf("receipt replay duplicated/lost source evidence: %d", found)
	}
	f.must(t, repo.SubmitCheck(ctx, ws.ID, f.owner.ID, check.ID))
	readonly, err := repo.CheckDetail(ctx, ws.ID, f.reviewer.ID, check.ID)
	f.must(t, err)
	if readonly.CanVerify {
		t.Fatal("applied receipt qualified without fresh source collection")
	}
	_, err = repo.ComplianceReport(ctx, ws.ID, f.viewer.ID, 0)
	f.must(t, err)
	f.want(t, repo.ReviewCheck(ctx, ws.ID, f.reviewer.ID, check.ID, readonly.ReviewRevision, "verified", "Missing post-execution source scan"), ErrEvidenceRequired)
	f.must(t, repo.ReviewCheck(ctx, ws.ID, f.reviewer.ID, check.ID, readonly.ReviewRevision, "changes", "Collect fresh source evidence"))
	_, err = f.pool.Exec(ctx, `UPDATE plume.control_actions SET expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, action.ID)
	f.must(t, err)
	command, err = repo.ClaimPostgresControl(ctx, token, "expired-window")
	f.must(t, err)
	if command != nil {
		t.Fatal("PostgreSQL TTL automatically restored a revoked privilege")
	}
	action, err = repo.RollbackControlAction(ctx, ws.ID, f.owner.ID, action.ID, action.Revision, "Explicitly restore original approved grant")
	f.must(t, err)
	command, err = repo.ClaimPostgresControl(ctx, token, "explicit-rollback")
	f.must(t, err)
	if command == nil || command.Phase != "rollback" || command.Original == nil {
		t.Fatal("explicit rollback lacks original source result")
	}
	had, has = false, true
	receipt = ControlReceipt{ActionID: command.ID, Revision: command.Revision, Lease: command.Lease, Result: ControlResult{Phase: "rollback", Success: true, Code: "select_restored", HadSelect: &had, HasSelect: &has, ObservedAt: time.Now().UTC()}}
	f.must(t, repo.RecordControlReceipt(ctx, "postgres_control", token, receipt))
	action, err = repo.ControlAction(ctx, ws.ID, f.viewer.ID, action.ID)
	f.must(t, err)
	if action.Status != "rolled_back" {
		t.Fatal("explicit source rollback not recorded")
	}
}

func TestControlScopeChangedAndGatewayTTL(t *testing.T) {
	f := newOperationsFixture(t)
	ctx, repo, ws := f.ctx, f.repo, f.workspace
	job, config, asset, check := operationsPostgresFixture(t, f)
	token := operationsTestDigest(f.slug + "-control")
	source, err := repo.CreateOperationsSource(ctx, ws.ID, f.owner.ID, OperationsSourceInput{Name: "PostgreSQL control", Kind: "postgres_control", OwnerTeam: "Security", JobID: job.ID}, token)
	f.must(t, err)
	action, err := repo.CreateControlAction(ctx, ws.ID, f.owner.ID, ControlActionInput{Kind: "revoke_select", OperationsSourceID: source.ID, AssetID: asset.ID, CheckID: check.ID, Reason: "Scoped action", DurationMinutes: 5})
	f.must(t, err)
	action, err = repo.ApproveControlAction(ctx, ws.ID, f.reviewer.ID, action.ID, action.Revision)
	f.must(t, err)
	command, err := repo.ClaimPostgresControl(ctx, token, "before-scope-change")
	f.must(t, err)
	if command == nil {
		t.Fatal("missing original claim")
	}
	current, err := repo.ManagedPostgresJob(ctx, ws.ID, f.owner.ID, job.ID)
	f.must(t, err)
	config.Host = "replacement-db"
	_, err = repo.UpdateManagedPostgresJob(ctx, ws.ID, f.owner.ID, job.ID, current.Managed.Revision, config)
	f.must(t, err)
	_, err = f.pool.Exec(ctx, `UPDATE plume.control_actions SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, action.ID)
	f.must(t, err)
	_, err = repo.ClaimPostgresControl(ctx, token, "after-scope-change")
	f.want(t, err, ErrConflict)
	had, has := true, false
	f.want(t, repo.RecordControlReceipt(ctx, "postgres_control", token, ControlReceipt{ActionID: command.ID, Revision: command.Revision, Lease: command.Lease, Result: ControlResult{Phase: "apply", Success: true, Code: "select_revoked", HadSelect: &had, HasSelect: &has, ObservedAt: time.Now().UTC()}}), ErrConflict)
	gatewayToken := operationsTestDigest(f.slug + "-gateway")
	gateway, err := repo.CreateRuntimeSource(ctx, ws.ID, f.owner.ID, "Gateway", "Gateway app", "gateway", gatewayToken)
	f.must(t, err)
	_, err = repo.DeliverRuntimeBatch(ctx, "gateway", gatewayToken, operationsTestDigest("control-route"), []runtimesecurity.Observation{{Method: "GET", Route: "/contacts/{contact_id}", Status: 200, ObservedAt: time.Now().UTC()}})
	f.must(t, err)
	block, err := repo.CreateControlAction(ctx, ws.ID, f.owner.ID, ControlActionInput{Kind: "block_api", RuntimeSourceID: gateway.ID, Method: "GET", Route: "/contacts/{id}", Reason: "Temporarily block observed canonical route", DurationMinutes: 5})
	f.must(t, err)
	block, err = repo.ApproveControlAction(ctx, ws.ID, f.reviewer.ID, block.ID, block.Revision)
	f.must(t, err)
	commands, err := repo.RuntimeControlCommands(ctx, gatewayToken)
	f.must(t, err)
	if len(commands) != 1 || commands[0].Phase != "apply" {
		t.Fatal("gateway canonical target not dispatched")
	}
	f.must(t, repo.RecordControlReceipt(ctx, "gateway", gatewayToken, ControlReceipt{ActionID: block.ID, Revision: block.Revision, Result: ControlResult{Phase: "apply", Success: true, Code: "route_blocked", ObservedAt: time.Now().UTC()}}))
	_, err = f.pool.Exec(ctx, `UPDATE plume.control_actions SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, block.ID)
	f.must(t, err)
	commands, err = repo.RuntimeControlCommands(ctx, gatewayToken)
	f.must(t, err)
	if len(commands) != 1 || commands[0].Phase != "rollback" {
		t.Fatal("gateway TTL did not release temporary route block")
	}
	_, err = repo.RuntimeControlCommands(ctx, token)
	f.want(t, err, ErrAutomationUnauthorized)
}
