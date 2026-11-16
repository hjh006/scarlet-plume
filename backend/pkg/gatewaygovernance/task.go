package gatewaygovernance

import (
	"encoding/json"
	"time"
)

// TaskLeaseProtocolVersion requires every task execution to pin the complete
// ancestor execution-lease chain before admission and keep it until delivery.
const TaskLeaseProtocolVersion = "scarlet-task-lease-v1"

// TaskContext is derived by the trusted gateway from the selected task and the
// original logical request. It is not a caller-supplied identity or label.
type TaskContext struct {
	TaskID               string `json:"taskId,omitempty"`
	TaskStepID           string `json:"taskStepId,omitempty"`
	TaskStepDigest       string `json:"taskStepDigest,omitempty"`
	LeaseChainDigest     string `json:"leaseChainDigest,omitempty"`
	LeaseProtocolVersion string `json:"leaseProtocolVersion,omitempty"`
}

type TaskSource struct {
	DocumentID    string `json:"documentId"`
	Version       string `json:"version"`
	ACLVersion    string `json:"aclVersion"`
	ContentDigest string `json:"contentDigest"`
}

type TaskToolScope struct {
	ToolID  string   `json:"toolId"`
	Actions []string `json:"actions"`
	Targets []string `json:"targets"`
}

type TaskCheckInput struct {
	TaskContext
	ApprovalID    string   `json:"approvalId,omitempty"`
	RequestID     string   `json:"requestId,omitempty"`
	RequestDigest string   `json:"requestDigest,omitempty"`
	WorkspaceSlug string   `json:"workspaceSlug"`
	TokenHash     string   `json:"tokenHash"`
	ApplicationID string   `json:"applicationId"`
	PrincipalID   string   `json:"principalId"`
	CatalogDigest string   `json:"catalogDigest"`
	Kind          string   `json:"kind"`
	ModelID       string   `json:"modelId,omitempty"`
	ToolID        string   `json:"toolId,omitempty"`
	Action        string   `json:"action,omitempty"`
	Target        string   `json:"target,omitempty"`
	DocumentIDs   []string `json:"documentIds,omitempty"`
	Categories    []string `json:"categories,omitempty"`
}

type TaskGrant struct {
	Allowed              bool            `json:"allowed"`
	Required             bool            `json:"required"`
	TaskID               string          `json:"taskId,omitempty"`
	Revision             int64           `json:"revision,omitempty"`
	ScopeDigest          string          `json:"scopeDigest,omitempty"`
	LeaseChainDigest     string          `json:"leaseChainDigest,omitempty"`
	LeaseProtocolVersion string          `json:"leaseProtocolVersion,omitempty"`
	Purpose              string          `json:"purpose,omitempty"`
	Categories           []string        `json:"categories"`
	ModelIDs             []string        `json:"modelIds"`
	ToolIDs              []string        `json:"toolIds"`
	ToolScopes           []TaskToolScope `json:"toolScopes"`
	DocumentIDs          []string        `json:"documentIds"`
	SourceDocumentIDs    []string        `json:"sourceDocumentIds"`
	Sources              []TaskSource    `json:"sources"`
	ExpiresAt            *time.Time      `json:"expiresAt,omitempty"`
	Admitted             bool            `json:"admitted"`
	Replayed             bool            `json:"replayed"`
}

// RawMessage avoids importing the runtime package back into its governance
// transport. The controller decodes and validates the strict AuditRecord.
type TaskCompleteInput struct {
	TaskCheckInput
	Audit   json.RawMessage `json:"audit"`
	Sources []TaskSource    `json:"sources,omitempty"`
}
