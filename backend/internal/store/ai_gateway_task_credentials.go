package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/hjh006/scarlet-plume/backend/pkg/workloadidentity"
	"github.com/jackc/pgx/v5"
)

const GatewayTaskCredentialBoundary = "只读核对当前任务祖先链、凭据生命周期及可确认的应用、主体和目录门槛；不代表具体模型、文档、工具、预算、审批或源端执行已获准。未知签发事实不回填，也不表示有效历史凭据已被运行时拒绝。本版本不支持续期、替换绑定或延长任务；到期、撤销和终止仍按原验证拒绝新执行。"

type GatewayTaskCredentialIssue struct {
	TaskID  string `json:"taskId,omitempty"`
	Code    string `json:"code"`
	State   string `json:"state"`
	Message string `json:"message"`
}
type GatewayTaskCredentialNode struct {
	OriginalCredentialID   int64                        `json:"originalCredentialId"`
	LeaseRevision          int64                        `json:"leaseRevision"`
	TaskID                 string                       `json:"taskId"`
	Depth                  int                          `json:"depth"`
	TaskRevision           int64                        `json:"taskRevision"`
	TaskStatus             string                       `json:"taskStatus"`
	TaskExpiresAt          time.Time                    `json:"taskExpiresAt"`
	ScopeDigest            string                       `json:"scopeDigest"`
	Credential             GatewayCredential            `json:"credential"`
	CredentialState        string                       `json:"credentialState"`
	BindingStatus          string                       `json:"bindingStatus"`
	CurrentBindingRevision *int64                       `json:"currentBindingRevision,omitempty"`
	Blockers               []GatewayTaskCredentialIssue `json:"blockers"`
	RenewalBlockers        []GatewayTaskCredentialIssue `json:"renewalBlockers"`
}
type GatewayTaskCredentialAuthorization struct {
	GatewayEnabled                  bool       `json:"gatewayEnabled"`
	CatalogStatus                   string     `json:"catalogStatus"`
	GatewayApplicationStatus        string     `json:"gatewayApplicationStatus"`
	PrincipalStatus                 string     `json:"principalStatus"`
	PrincipalAuthorizationExpiresAt *time.Time `json:"principalAuthorizationExpiresAt,omitempty"`
}
type GatewayTaskCredentialStatus struct {
	TaskID                string                             `json:"taskId"`
	RootTaskID            string                             `json:"rootTaskId"`
	ObservedAt            time.Time                          `json:"observedAt"`
	RenewalSupported      bool                               `json:"renewalSupported"`
	CredentialChainStatus string                             `json:"credentialChainStatus"`
	Blockers              []GatewayTaskCredentialIssue       `json:"blockers"`
	RenewalBlockers       []GatewayTaskCredentialIssue       `json:"renewalBlockers"`
	AncestorPath          []GatewayTaskCredentialNode        `json:"ancestorPath"`
	Authorization         GatewayTaskCredentialAuthorization `json:"authorization"`
	Boundary              string                             `json:"boundary"`
}

func (s *Store) GatewayTaskCredentialStatus(ctx context.Context, ws, actor int64, id string) (out GatewayTaskCredentialStatus, err error) {
	out = GatewayTaskCredentialStatus{TaskID: id, CredentialChainStatus: "current", AncestorPath: []GatewayTaskCredentialNode{}, Blockers: []GatewayTaskCredentialIssue{}, RenewalSupported: true, RenewalBlockers: []GatewayTaskCredentialIssue{}, Boundary: TaskLeaseBoundary}
	if !gatewayRuntimeID.MatchString(id) {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		if _, e := gatewayTask(ctx, tx, ws, id); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&out.ObservedAt); e != nil {
			return e
		}
		chain, e := gatewayTaskChain(ctx, tx, ws, id)
		if e != nil {
			if errors.Is(e, ErrForbidden) || errors.Is(e, ErrNotFound) || errors.Is(e, ErrInvalid) {
				out.CredentialChainStatus = "unknown"
				out.Blockers = append(out.Blockers, GatewayTaskCredentialIssue{Code: "ancestor_chain_invalid", State: "unknown", Message: "祖先链不完整、循环或超出深度上限，无法确认当前凭据链。"})
				return nil
			}
			return e
		}
		out.RootTaskID = chain[0].ID
		leaf := chain[len(chain)-1]
		cfg, e := gatewayConfig(ctx, tx, ws, "")
		if e != nil {
			return e
		}
		out.Authorization.GatewayEnabled = cfg.Enabled
		out.Authorization.CatalogStatus = "unknown"
		out.Authorization.GatewayApplicationStatus = "unknown"
		out.Authorization.PrincipalStatus = "unknown"
		add := func(code, state, message string) {
			out.Blockers = append(out.Blockers, GatewayTaskCredentialIssue{Code: code, State: state, Message: message})
		}
		if cfg.Revision == 0 {
			add("gateway_configuration_unknown", "unknown", "没有当前网关配置，不能确认应用门槛。")
		} else if !cfg.Enabled {
			add("gateway_disabled", "blocked", "当前网关已停用。")
		}
		var gatewayApp *GatewayApplication
		for i := range cfg.Applications {
			if cfg.Applications[i].ID == leaf.ApplicationID {
				gatewayApp = &cfg.Applications[i]
				break
			}
		}
		if cfg.Revision > 0 {
			if gatewayApp == nil || !gatewayApp.Enabled || gatewayApp.PrincipalID != leaf.PrincipalID {
				out.Authorization.GatewayApplicationStatus = "blocked"
				add("gateway_application_unavailable", "blocked", "网关应用不存在、已停用或主体映射已变化。")
			} else {
				out.Authorization.GatewayApplicationStatus = "current"
			}
		}
		catalog, digest, e := publishedAISecurityCatalog(ctx, tx, ws)
		if errors.Is(e, ErrNotFound) {
			add("published_catalog_unknown", "unknown", "未取得已发布的AI运行目录，无法核定当前主体与应用的运行时授权。")
		} else if errors.Is(e, ErrConflict) {
			add("published_catalog_integrity_unknown", "unknown", "已发布目录摘要不能确认，无法采用该目录作出授权结论。")
		} else if e != nil {
			return e
		} else if aisecurity.ValidateConfig(catalog) != nil {
			add("published_catalog_invalid", "blocked", "已发布目录未通过运行时结构校验。")
		} else {
			out.Authorization.CatalogStatus = "current"
			if cfg.Revision == 0 || cfg.CatalogDigest != digest {
				out.Authorization.CatalogStatus = "changed"
				add("gateway_catalog_changed", "blocked", "网关目录引用与当前已发布的运行目录不一致。")
			}
			var app *aisecurity.Application
			var principal *aisecurity.Principal
			for i := range catalog.Applications {
				if catalog.Applications[i].ID == leaf.ApplicationID {
					app = &catalog.Applications[i]
					break
				}
			}
			for i := range catalog.Principals {
				if catalog.Principals[i].ID == leaf.PrincipalID {
					principal = &catalog.Principals[i]
					break
				}
			}
			out.Authorization.PrincipalStatus = "current"
			if app == nil || !app.Enabled || principal == nil || !principal.Enabled {
				out.Authorization.PrincipalStatus = "blocked"
				add("application_or_principal_unavailable", "blocked", "当前发布目录中的应用或主体不存在或已停用。")
			} else {
				out.Authorization.PrincipalAuthorizationExpiresAt = principal.AuthorizationExpiresAt
				if !slices.Contains(app.PrincipalIDs, principal.ID) || !slices.Contains(principal.ApplicationIDs, app.ID) {
					out.Authorization.PrincipalStatus = "blocked"
					add("application_principal_not_allowed", "blocked", "当前发布目录未双向授予该应用与主体的关联权限。")
				}
				if principal.AuthorizationExpiresAt == nil && catalog.Policy.RequireAuthorizationLeases {
					out.Authorization.PrincipalStatus = "blocked"
					add("principal_authorization_lease_missing", "blocked", "当前策略要求主体授权有效期，但未登记该有效期。")
				} else if principal.AuthorizationExpiresAt != nil && !principal.AuthorizationExpiresAt.After(out.ObservedAt) {
					out.Authorization.PrincipalStatus = "blocked"
					add("principal_authorization_expired", "blocked", "主体授权有效期已到，凭据有效不延长主体授权。")
				}
			}
		}
		for _, task := range chain {
			node := GatewayTaskCredentialNode{OriginalCredentialID: task.CredentialID, LeaseRevision: task.ExecutionLease.Revision, TaskID: task.ID, Depth: task.Depth, TaskRevision: task.Revision, TaskStatus: task.Status, TaskExpiresAt: task.ExpiresAt, ScopeDigest: task.ScopeDigest, CredentialState: "unknown", BindingStatus: "not_applicable", Blockers: []GatewayTaskCredentialIssue{}, RenewalBlockers: []GatewayTaskCredentialIssue{}}
			issue := func(code, state, message string) {
				node.Blockers = append(node.Blockers, GatewayTaskCredentialIssue{TaskID: task.ID, Code: code, State: state, Message: message})
			}
			if task.Status != "active" {
				issue("task_terminal", "blocked", "该任务已完成或取消，不能由新凭据恢复。")
			}
			if !task.ExpiresAt.After(out.ObservedAt) {
				issue("task_expired", "blocked", "任务自身有效期已到，短期凭据不延长任务有效期。")
			}
			if task.ApplicationID != leaf.ApplicationID || task.PrincipalID != leaf.PrincipalID {
				issue("ancestor_identity_changed", "blocked", "祖先任务的应用或主体与当前任务不一致。")
			}
			if cfg.Revision > 0 && task.CatalogDigest != cfg.CatalogDigest {
				issue("task_catalog_changed", "blocked", "该任务冻结目录与当前网关目录不一致。")
				out.Authorization.CatalogStatus = "changed"
			}
			var rawEnabled bool
			e = tx.QueryRow(ctx, `SELECT id,application_id,name,prefix,enabled,created_at,expires_at,workload_binding_id,workload_binding_revision,revoked_at,COALESCE(revocation_reason,'') FROM plume.ai_gateway_credentials WHERE workspace_id=$1 AND id=$2`, ws, task.ExecutionLease.CredentialID).Scan(&node.Credential.ID, &node.Credential.ApplicationID, &node.Credential.Name, &node.Credential.Prefix, &rawEnabled, &node.Credential.CreatedAt, &node.Credential.ExpiresAt, &node.Credential.WorkloadBindingID, &node.Credential.WorkloadBindingRevision, &node.Credential.RevokedAt, &node.Credential.RevocationReason)
			if errors.Is(e, pgx.ErrNoRows) {
				node.Credential.ID = task.ExecutionLease.CredentialID
				issue("credential_missing", "blocked", "本工作区没有该任务绑定的凭据，不能采用其他工作区的凭据。")
			} else if e != nil {
				return e
			} else {
				expired := node.Credential.ExpiresAt != nil && !node.Credential.ExpiresAt.After(out.ObservedAt)
				node.Credential.Enabled = rawEnabled && !expired && node.Credential.RevokedAt == nil
				switch {
				case node.Credential.RevokedAt != nil:
					node.CredentialState = "revoked"
					issue("credential_revoked", "blocked", "凭据已有明确撤销事实；到期或新令牌均不解除该撤销。")
				case expired:
					node.CredentialState = "expired"
					issue("credential_expired", "blocked", "绑定凭据已到期；没有记录的历史停用原因保持未知，不从到期推断曾经人工撤销或自然清理。")
				case !rawEnabled:
					node.CredentialState = "disabled_unknown"
					issue("credential_disabled_unknown", "blocked", "凭据已停用但没有可确认的历史撤销原因，不推断为自然到期或人工撤销。")
				default:
					node.CredentialState = "active"
				}
				if node.Credential.ApplicationID != task.ApplicationID {
					issue("credential_application_mismatch", "blocked", "绑定凭据与任务应用不一致。")
				}
				if node.Credential.WorkloadBindingID != nil {
					node.BindingStatus = "unknown"
					binding, be := scanGatewayWorkload(tx.QueryRow(ctx, `SELECT `+gatewayWorkloadColumns+` FROM plume.ai_gateway_workload_bindings WHERE workspace_id=$1 AND id=$2`, ws, *node.Credential.WorkloadBindingID))
					if errors.Is(be, ErrNotFound) {
						issue("workload_binding_unknown", "unknown", "无法取得本工作区的工作负载信任绑定。")
					} else if be != nil {
						return be
					} else {
						revision := binding.Revision
						node.CurrentBindingRevision = &revision
						switch {
						case !binding.Enabled:
							node.BindingStatus = "disabled"
							issue("workload_binding_disabled", "blocked", "工作负载信任绑定已停用。")
						case binding.ApplicationID != task.ApplicationID:
							node.BindingStatus = "changed"
							issue("workload_binding_application_changed", "blocked", "工作负载信任绑定的应用已变化。")
						case node.Credential.WorkloadBindingRevision == nil:
							node.RenewalBlockers = append(node.RenewalBlockers, GatewayTaskCredentialIssue{TaskID: task.ID, Code: "mint_revision_unknown", State: "unknown", Message: "历史签发revision未知，不以当前revision回填，也不能据此证明未来续期身份连续。"})
						case *node.Credential.WorkloadBindingRevision != binding.Revision:
							node.BindingStatus = "changed"
							issue("workload_binding_revision_changed", "blocked", "工作负载绑定revision已变化，原签发revision不能被改写。")
						default:
							if _, ve := workloadidentity.New(binding.policy()); ve != nil {
								issue("workload_trust_configuration_unknown", "unknown", "当前信任配置不能通过导入公钥配置校验，未向外部issuer查询或推断信任成功。")
							} else {
								node.BindingStatus = "current"
							}
						}
					}
				}
			}
			out.AncestorPath = append(out.AncestorPath, node)
			out.Blockers = append(out.Blockers, node.Blockers...)
			out.RenewalBlockers = append(out.RenewalBlockers, node.RenewalBlockers...)
		}
		for _, b := range out.Blockers {
			if b.State == "blocked" {
				out.CredentialChainStatus = "blocked"
				break
			}
			if b.State == "unknown" {
				out.CredentialChainStatus = "unknown"
			}
		}
		return nil
	})
	return out, storeError(err)
}
