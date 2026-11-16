package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
	"github.com/hjh006/scarlet-plume/backend/pkg/workloadidentity"
	"github.com/jackc/pgx/v5"
)

func gatewayTaskLeaseDigest(chain []GatewayTask) string {
	type pin struct {
		TaskID       string
		ScopeDigest  string
		CredentialID int64
		Revision     int64
	}
	pins := []pin{}
	for _, t := range chain {
		pins = append(pins, pin{t.ID, t.ScopeDigest, t.ExecutionLease.CredentialID, t.ExecutionLease.Revision})
	}
	return gatewayDigest(struct {
		Version string
		Chain   []pin
	}{gatewaygovernance.TaskLeaseProtocolVersion, pins})
}
func gatewayTaskLeasePin(in gatewaygovernance.TaskContext, chain []GatewayTask, required bool) error {
	if in.TaskID == "" {
		return nil
	}
	if in.LeaseProtocolVersion != gatewaygovernance.TaskLeaseProtocolVersion || (required && !gatewayHash.MatchString(in.LeaseChainDigest)) || (in.LeaseChainDigest != "" && in.LeaseChainDigest != gatewayTaskLeaseDigest(chain)) {
		return ErrForbidden
	}
	return nil
}

type leaseCredential struct {
	GatewayCredential
	StoredEnabled    bool
	ExpiryDisabledAt *time.Time
	Binding          *GatewayWorkloadBinding
}

func readTaskLeaseCredential(ctx context.Context, tx pgx.Tx, ws, id int64, lock bool) (c leaseCredential, err error) {
	q := `SELECT id,application_id,name,prefix,enabled,created_at,expires_at,workload_binding_id,workload_binding_revision,revoked_at,COALESCE(revocation_reason,''),expiry_disabled_at,(enabled AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp())) FROM plume.ai_gateway_credentials WHERE workspace_id=$1 AND id=$2`
	if lock {
		q += ` FOR SHARE`
	}
	err = tx.QueryRow(ctx, q, ws, id).Scan(&c.ID, &c.ApplicationID, &c.Name, &c.Prefix, &c.StoredEnabled, &c.CreatedAt, &c.ExpiresAt, &c.WorkloadBindingID, &c.WorkloadBindingRevision, &c.RevokedAt, &c.RevocationReason, &c.ExpiryDisabledAt, &c.Enabled)
	if err != nil {
		return c, storeError(err)
	}
	if c.WorkloadBindingID != nil {
		b, e := scanGatewayWorkload(tx.QueryRow(ctx, `SELECT `+gatewayWorkloadColumns+` FROM plume.ai_gateway_workload_bindings WHERE workspace_id=$1 AND id=$2`, ws, *c.WorkloadBindingID))
		if e != nil {
			return c, e
		}
		c.Binding = &b
	}
	return c, nil
}
func (c leaseCredential) live(now time.Time) bool {
	return c.StoredEnabled && c.RevokedAt == nil && (c.ExpiresAt == nil || c.ExpiresAt.After(now))
}
func (c leaseCredential) identity() (string, error) {
	if c.Binding == nil || c.WorkloadBindingRevision == nil || !c.Binding.Enabled || *c.WorkloadBindingRevision != c.Binding.Revision || c.Binding.ApplicationID != c.ApplicationID {
		return "", leaseError("workload_identity_unverified", "无法确认工作负载签发身份或当前信任修订。", ErrForbidden)
	}
	if _, err := workloadidentity.New(c.Binding.policy()); err != nil {
		return "", leaseError("workload_identity_unverified", "当前工作负载公钥信任配置无法确认。", ErrForbidden)
	}
	return gatewayDigest(struct {
		ID, Revision              int64
		Issuer, Audience, Subject string
	}{c.Binding.ID, c.Binding.Revision, c.Binding.Issuer, c.Binding.Audience, c.Binding.Subject}), nil
}
func (c leaseCredential) identitySnapshot() *TaskLeaseIdentity {
	if c.Binding == nil {
		return nil
	}
	return &TaskLeaseIdentity{BindingID: c.Binding.ID, Revision: c.Binding.Revision, Issuer: c.Binding.Issuer, Audience: c.Binding.Audience, Subject: c.Binding.Subject}
}
func leaseAuthority(ctx context.Context, tx pgx.Tx, ws int64, chain []GatewayTask, now time.Time) (GatewayConfig, error) {
	cfg, err := gatewayConfig(ctx, tx, ws, "")
	if err != nil {
		return cfg, err
	}
	pub, digest, err := publishedAISecurityCatalog(ctx, tx, ws)
	if err != nil {
		return cfg, leaseError("current_catalog_unverified", "未取得可确认的当前发布目录，不能切换执行凭据。", ErrForbidden)
	}
	if aisecurity.ValidateConfig(pub) != nil {
		return cfg, leaseError("current_catalog_unverified", "当前已发布目录结构无法确认。", ErrForbidden)
	}
	leaf := chain[len(chain)-1]
	app, ok := gatewayTaskApp(cfg, leaf.ApplicationID)
	if !cfg.Enabled || !ok || app.PrincipalID != leaf.PrincipalID || cfg.CatalogDigest != digest {
		return cfg, leaseError("scope_changed", "当前网关应用、主体或目录已变化。", ErrForbidden)
	}
	appOK, principalOK := false, false
	for _, a := range pub.Applications {
		if a.ID == leaf.ApplicationID && a.Enabled && slices.Contains(a.PrincipalIDs, leaf.PrincipalID) {
			appOK = true
		}
	}
	for _, p := range pub.Principals {
		if p.ID == leaf.PrincipalID && p.Enabled && slices.Contains(p.ApplicationIDs, leaf.ApplicationID) && (p.AuthorizationExpiresAt == nil && !pub.Policy.RequireAuthorizationLeases || p.AuthorizationExpiresAt != nil && p.AuthorizationExpiresAt.After(now)) {
			principalOK = true
		}
	}
	if !appOK || !principalOK {
		return cfg, leaseError("principal_authorization_unavailable", "当前应用或主体授权缺失、停用或到期。", ErrForbidden)
	}
	for _, t := range chain {
		if t.Status != "active" || !t.ExpiresAt.After(now) || t.ApplicationID != leaf.ApplicationID || t.PrincipalID != leaf.PrincipalID || t.CatalogDigest != digest || !gatewayTaskSubset(t.ModelIDs, app.ModelIDs) || !gatewayTaskSubset(t.ToolIDs, app.ToolIDs) {
			return cfg, leaseError("task_scope_unavailable", "任务或祖先已终止、到期或采用范围已变化。", ErrForbidden, TaskLeaseIssue{TaskID: t.ID, Code: "task_scope_unavailable", State: "blocked", Message: "原任务授权不因新令牌恢复或扩张。"})
		}
	}
	return cfg, nil
}
func taskLeaseViewTx(ctx context.Context, tx pgx.Tx, ws, actor int64, id string) (out TaskLeaseView, err error) {
	chain, err := gatewayTaskChain(ctx, tx, ws, id)
	if err != nil {
		return out, err
	}
	out = TaskLeaseView{TaskID: id, RootTaskID: chain[0].ID, LeaseChainDigest: gatewayTaskLeaseDigest(chain), LeaseProtocolVersion: gatewaygovernance.TaskLeaseProtocolVersion, Chain: []TaskLeaseNode{}, Blockers: []TaskLeaseIssue{}, Boundary: TaskLeaseBoundary}
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&out.ObservedAt); err != nil {
		return out, err
	}
	if _, e := leaseAuthority(ctx, tx, ws, chain, out.ObservedAt); e != nil {
		var le *TaskLeaseError
		if errors.As(e, &le) {
			out.Blockers = append(out.Blockers, TaskLeaseIssue{Code: le.Code, State: "blocked", Message: le.Message})
		} else {
			return out, e
		}
	}
	for _, t := range chain {
		n := TaskLeaseNode{TaskID: t.ID, TaskRevision: t.Revision, ScopeDigest: t.ScopeDigest, TaskStatus: t.Status, TaskExpiresAt: t.ExpiresAt, ApplicationID: t.ApplicationID, PrincipalID: t.PrincipalID, OriginalCredentialID: t.CredentialID, Lease: t.ExecutionLease, BindingStatus: "not_applicable", Blockers: []TaskLeaseIssue{}}
		c, e := readTaskLeaseCredential(ctx, tx, ws, t.ExecutionLease.CredentialID, false)
		if e != nil {
			return out, e
		}
		c.Enabled = c.live(out.ObservedAt)
		n.Credential = c.GatewayCredential
		n.Identity = c.identitySnapshot()
		if !c.live(out.ObservedAt) {
			n.Blockers = append(n.Blockers, TaskLeaseIssue{TaskID: t.ID, Code: "credential_inactive", State: "blocked", Message: "当前执行凭据已到期、停用或撤销；不能跳过该祖先。"})
		}
		if c.Binding != nil {
			r := c.Binding.Revision
			n.CurrentBindingRevision = &r
			n.BindingStatus = "unknown"
			if _, e = c.identity(); e == nil {
				n.BindingStatus = "current"
			} else if !c.Binding.Enabled {
				n.BindingStatus = "disabled"
			} else if c.WorkloadBindingRevision != nil && *c.WorkloadBindingRevision != r {
				n.BindingStatus = "changed"
			}
		}
		out.Chain = append(out.Chain, n)
		out.Blockers = append(out.Blockers, n.Blockers...)
	}
	if e := taskLeasePendingPreviousEpoch(ctx, tx, ws, chain, ""); e != nil {
		var le *TaskLeaseError
		if errors.As(e, &le) {
			out.Blockers = append(out.Blockers, le.Blockers...)
		} else {
			return out, e
		}
	}
	if actor > 0 {
		role, e := checkRole(ctx, tx, ws, actor, false)
		if e != nil {
			return out, e
		}
		out.CanPropose = checkEditor(role) && len(out.Blockers) == 0
	}
	return out, nil
}
func (s *Store) GatewayTaskLeaseView(ctx context.Context, ws, actor int64, id string) (out TaskLeaseView, err error) {
	if !gatewayRuntimeID.MatchString(id) {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var e error
		out, e = taskLeaseViewTx(ctx, tx, ws, actor, id)
		return e
	})
	return out, storeError(err)
}
func machineLeaseCredential(ctx context.Context, tx pgx.Tx, slug, hash string) (ws int64, c leaseCredential, err error) {
	if !gatewayText(slug, 128) || !gatewayHash.MatchString(hash) {
		return 0, c, ErrForbidden
	}
	var id int64
	if err = tx.QueryRow(ctx, `SELECT c.workspace_id,c.id FROM plume.ai_gateway_credentials c JOIN plume.workspaces w ON w.id=c.workspace_id WHERE w.slug=$1 AND c.token_hash=$2 AND c.enabled AND c.revoked_at IS NULL AND c.expires_at>clock_timestamp()`, slug, hash).Scan(&ws, &id); err != nil {
		return 0, c, ErrForbidden
	}
	c, err = readTaskLeaseCredential(ctx, tx, ws, id, false)
	if err != nil {
		return 0, c, err
	}
	_, err = c.identity()
	return ws, c, err
}
func (s *Store) GatewayTaskLeasePreflight(ctx context.Context, slug, hash, id string) (out TaskLeaseView, err error) {
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		ws, fresh, e := machineLeaseCredential(ctx, tx, slug, hash)
		if e != nil {
			return e
		}
		chain, e := gatewayTaskChain(ctx, tx, ws, id)
		if e != nil {
			return ErrForbidden
		}
		leaf := chain[len(chain)-1]
		old, e := readTaskLeaseCredential(ctx, tx, ws, leaf.ExecutionLease.CredentialID, false)
		if e != nil {
			return ErrForbidden
		}
		a, e := old.identity()
		if e != nil {
			return e
		}
		b, e := fresh.identity()
		if e != nil || a != b || fresh.ApplicationID != leaf.ApplicationID {
			return ErrForbidden
		}
		out, e = taskLeaseViewTx(ctx, tx, ws, 0, id)
		if e != nil {
			return e
		}
		// Machine preflight reveals CAS/lease metadata only, never task scope
		// contents, target names, credential prefixes or the raw workload subject.
		scrubMachineTaskLeaseView(&out)
		return nil
	})
	return out, storeError(err)
}
func scrubMachineTaskLeaseView(out *TaskLeaseView) {
	for i := range out.Chain {
		out.Chain[i].Identity = nil
		c := out.Chain[i].Credential
		out.Chain[i].Credential = GatewayCredential{ID: c.ID, ApplicationID: c.ApplicationID, Enabled: c.Enabled, ExpiresAt: c.ExpiresAt, WorkloadBindingID: c.WorkloadBindingID, WorkloadBindingRevision: c.WorkloadBindingRevision, RevokedAt: c.RevokedAt, RevocationReason: c.RevocationReason}
	}
}
func leaseExpectedMatches(chain []GatewayTask, digest string, expected []TaskLeaseExpected) bool {
	if digest != gatewayTaskLeaseDigest(chain) || len(chain) != len(expected) {
		return false
	}
	for i, t := range chain {
		e := expected[i]
		if e.TaskID != t.ID || e.TaskRevision != t.Revision || e.CredentialID != t.ExecutionLease.CredentialID || e.LeaseRevision != t.ExecutionLease.Revision {
			return false
		}
	}
	return true
}
func invalidateCredentialLeaseProposals(ctx context.Context, tx pgx.Tx, ws, id int64, reason string) error {
	_, err := tx.Exec(ctx, `UPDATE plume.ai_gateway_task_lease_proposals SET status='invalidated',revision=revision+1,note=$3 WHERE workspace_id=$1 AND status='proposed' AND ($2=ANY(source_ids) OR $2=ANY(target_ids))`, ws, id, reason)
	return err
}

// CompleteRenewedGatewayTaskStep is a metadata reconciliation endpoint, never
// an execution or output-delivery permission. Header authentication supplies the
// fresh key; tenant/application/principal/catalog are derived by the server.
func (s *Store) CompleteRenewedGatewayTaskStep(ctx context.Context, slug, hash, id string, in gatewaygovernance.TaskCompleteInput) (out gatewaygovernance.TaskGrant, err error) {
	var task GatewayTask
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		ws, c, e := machineLeaseCredential(ctx, tx, slug, hash)
		if e != nil {
			return e
		}
		task, e = gatewayTask(ctx, tx, ws, id)
		if e != nil {
			return ErrForbidden
		}
		if task.ExecutionLease.CredentialID != c.ID {
			return ErrForbidden
		}
		return nil
	})
	if err != nil {
		return out, storeError(err)
	}
	in.WorkspaceSlug = slug
	in.TokenHash = hash
	in.ApplicationID = task.ApplicationID
	in.PrincipalID = task.PrincipalID
	in.CatalogDigest = task.CatalogDigest
	in.TaskID = id
	return s.CompleteGatewayTask(ctx, in)
}
