package store

import (
	"time"
)

const TaskLeaseBoundary = "原授权credentialId与任务范围摘要保持不变；当前执行凭据独立记录。换绑不延长任务、不重置步骤、预算、来源或审批。切换后旧epoch不能准入或释放结果；退役旧key的后续撤销只影响仍使用它的租约，停止当前任务应取消任务或撤销当前凭据/绑定。"

type GatewayTaskExecutionLease struct {
	CredentialID int64     `json:"credentialId"`
	Revision     int64     `json:"revision"`
	Mode         string    `json:"mode"`
	UpdatedAt    time.Time `json:"updatedAt"`
}
type TaskLeaseExpected struct {
	TaskID        string `json:"taskId"`
	TaskRevision  int64  `json:"taskRevision"`
	LeaseRevision int64  `json:"leaseRevision"`
	CredentialID  int64  `json:"credentialId"`
}
type TaskLeaseTarget struct {
	TaskID          string `json:"taskId"`
	NewCredentialID int64  `json:"newCredentialId"`
}
type TaskLeaseIssue struct {
	Code           string `json:"code"`
	TaskID         string `json:"taskId,omitempty"`
	StepID         string `json:"stepId,omitempty"`
	ReservationID  string `json:"reservationId,omitempty"`
	State          string `json:"state,omitempty"`
	ReservedMicros *int64 `json:"reservedMicros,omitempty"`
	Message        string `json:"message"`
}
type TaskLeaseError struct {
	Code     string           `json:"code"`
	Message  string           `json:"error"`
	Blockers []TaskLeaseIssue `json:"blockers"`
	Recovery []string         `json:"recovery"`
	cause    error
}

func (e *TaskLeaseError) Error() string { return e.Message }
func (e *TaskLeaseError) Unwrap() error { return e.cause }
func leaseError(code, message string, cause error, issues ...TaskLeaseIssue) error {
	return &TaskLeaseError{Code: code, Message: message, Blockers: issues, Recovery: []string{"重新读取当前租约与祖先链；不要覆盖新的CAS修订。", "未决执行先按真实来源回执核对；续期和换绑不会自动释放占额。"}, cause: cause}
}

type TaskLeaseIdentity struct {
	BindingID int64  `json:"bindingId"`
	Revision  int64  `json:"revision"`
	Issuer    string `json:"issuer"`
	Audience  string `json:"audience"`
	Subject   string `json:"subject"`
}
type TaskLeaseCredentialPlan struct {
	TaskID         string             `json:"taskId"`
	Source         GatewayCredential  `json:"source"`
	Target         GatewayCredential  `json:"target"`
	SourceIdentity *TaskLeaseIdentity `json:"sourceIdentity,omitempty"`
	TargetIdentity *TaskLeaseIdentity `json:"targetIdentity,omitempty"`
}
type TaskLeaseNode struct {
	Identity               *TaskLeaseIdentity        `json:"identity,omitempty"`
	TaskID                 string                    `json:"taskId"`
	TaskRevision           int64                     `json:"taskRevision"`
	ScopeDigest            string                    `json:"scopeDigest"`
	TaskStatus             string                    `json:"taskStatus"`
	TaskExpiresAt          time.Time                 `json:"taskExpiresAt"`
	ApplicationID          string                    `json:"applicationId"`
	PrincipalID            string                    `json:"principalId"`
	OriginalCredentialID   int64                     `json:"originalCredentialId"`
	Lease                  GatewayTaskExecutionLease `json:"lease"`
	Credential             GatewayCredential         `json:"credential"`
	BindingStatus          string                    `json:"bindingStatus"`
	CurrentBindingRevision *int64                    `json:"currentBindingRevision,omitempty"`
	Blockers               []TaskLeaseIssue          `json:"blockers"`
}
type TaskLeaseView struct {
	TaskID               string           `json:"taskId"`
	RootTaskID           string           `json:"rootTaskId"`
	LeaseChainDigest     string           `json:"leaseChainDigest"`
	LeaseProtocolVersion string           `json:"leaseProtocolVersion"`
	ObservedAt           time.Time        `json:"observedAt"`
	Chain                []TaskLeaseNode  `json:"chain"`
	Blockers             []TaskLeaseIssue `json:"blockers"`
	CanPropose           bool             `json:"canPropose"`
	Boundary             string           `json:"boundary"`
}
type TaskLeaseProposalInput struct {
	OperationID              string              `json:"operationId"`
	ExpectedLeaseChainDigest string              `json:"expectedLeaseChainDigest"`
	ExpectedChain            []TaskLeaseExpected `json:"expectedChain"`
	Changes                  []TaskLeaseTarget   `json:"changes"`
	Reason                   string              `json:"reason"`
	ExpiresAt                *time.Time          `json:"expiresAt,omitempty"`
}
type TaskLeaseRenewalInput struct {
	OperationID              string              `json:"operationId"`
	ExpectedLeaseChainDigest string              `json:"expectedLeaseChainDigest"`
	ExpectedChain            []TaskLeaseExpected `json:"expectedChain"`
	ReplaceTaskIDs           []string            `json:"replaceTaskIds"`
}
type TaskLeaseReviewInput struct {
	ExpectedRevision int64  `json:"expectedRevision"`
	Decision         string `json:"decision"`
	Note             string `json:"note"`
}
type TaskLeaseProposal struct {
	CredentialPlan           []TaskLeaseCredentialPlan `json:"credentialPlan"`
	ID                       int64                     `json:"id"`
	TaskID                   string                    `json:"taskId"`
	Status                   string                    `json:"status"`
	Revision                 int64                     `json:"revision"`
	OperationID              string                    `json:"operationId"`
	Reason                   string                    `json:"reason"`
	Note                     string                    `json:"note"`
	ProposedBy               int64                     `json:"proposedBy"`
	ReviewedBy               *int64                    `json:"reviewedBy,omitempty"`
	CreatedAt                time.Time                 `json:"createdAt"`
	ExpiresAt                time.Time                 `json:"expiresAt"`
	AppliedAt                *time.Time                `json:"appliedAt,omitempty"`
	ExpectedLeaseChainDigest string                    `json:"expectedLeaseChainDigest"`
	ExpectedChain            []TaskLeaseExpected       `json:"expectedChain"`
	Changes                  []TaskLeaseTarget         `json:"changes"`
	ProposalDigest           string                    `json:"proposalDigest"`
	CanReview                bool                      `json:"canReview"`
	Blockers                 []TaskLeaseIssue          `json:"blockers"`
}
type TaskLeaseApplied struct {
	TaskID           string `json:"taskId"`
	OldCredentialID  int64  `json:"oldCredentialId"`
	NewCredentialID  int64  `json:"newCredentialId"`
	OldLeaseRevision int64  `json:"oldLeaseRevision"`
	NewLeaseRevision int64  `json:"newLeaseRevision"`
}
type TaskLeaseChange struct {
	ID                int64              `json:"id"`
	OperationID       string             `json:"operationId"`
	Mode              string             `json:"mode"`
	TaskID            string             `json:"taskId"`
	ChangedTaskIDs    []string           `json:"changedTaskIds"`
	ActorID           *int64             `json:"actorId,omitempty"`
	ActorCredentialID *int64             `json:"actorCredentialId,omitempty"`
	ProposalID        *int64             `json:"proposalId,omitempty"`
	Reason            string             `json:"reason"`
	BeforeDigest      string             `json:"beforeDigest"`
	AfterDigest       string             `json:"afterDigest"`
	Changes           []TaskLeaseApplied `json:"changes"`
	CreatedAt         time.Time          `json:"createdAt"`
}
type TaskLeaseChangePage struct {
	Items      []TaskLeaseChange `json:"items"`
	Limit      int               `json:"limit"`
	HasMore    bool              `json:"hasMore"`
	NextCursor string            `json:"nextCursor"`
}
type TaskLeaseProposalPage struct {
	Items      []TaskLeaseProposal `json:"items"`
	Limit      int                 `json:"limit"`
	HasMore    bool                `json:"hasMore"`
	NextCursor string              `json:"nextCursor"`
}
type TaskLeaseRenewalResult struct {
	Change   TaskLeaseChange `json:"change"`
	Lease    TaskLeaseView   `json:"lease"`
	Replayed bool            `json:"replayed"`
}
