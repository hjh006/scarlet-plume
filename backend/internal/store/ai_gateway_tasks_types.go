package store

import (
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

type GatewayTaskInput struct {
	ApplicationID      string                            `json:"applicationId"`
	PrincipalID        string                            `json:"principalId"`
	CredentialID       int64                             `json:"credentialId"`
	ParentTaskID       string                            `json:"parentTaskId,omitempty"`
	Purpose            string                            `json:"purpose"`
	CatalogDigest      string                            `json:"catalogDigest"`
	ModelIDs           []string                          `json:"modelIds"`
	ToolIDs            []string                          `json:"toolIds"`
	ToolScopes         []gatewaygovernance.TaskToolScope `json:"toolScopes"`
	DocumentIDs        []string                          `json:"documentIds"`
	AllowedCategories  []string                          `json:"allowedCategories"`
	MaxSteps           int64                             `json:"maxSteps"`
	BudgetMicros       int64                             `json:"budgetMicros"`
	MaxDelegationDepth int                               `json:"maxDelegationDepth"`
	ExpiresAt          time.Time                         `json:"expiresAt"`
}

type GatewayTask struct {
	GatewayTaskInput
	ID                 string                         `json:"id"`
	RootTaskID         string                         `json:"rootTaskId"`
	Depth              int                            `json:"depth"`
	Status             string                         `json:"status"`
	Revision           int64                          `json:"revision"`
	ScopeDigest        string                         `json:"scopeDigest"`
	StepsUsed          int64                          `json:"stepsUsed"`
	ReservedMicros     int64                          `json:"reservedMicros"`
	SpentMicros        int64                          `json:"spentMicros"`
	ObservedCategories []string                       `json:"observedCategories"`
	Sources            []gatewaygovernance.TaskSource `json:"sources"`
	CreatedAt          time.Time                      `json:"createdAt"`
	UpdatedAt          time.Time                      `json:"updatedAt"`
}

type GatewayTaskPage struct {
	Items      []GatewayTask `json:"items"`
	NextCursor string        `json:"nextCursor"`
	HasMore    bool          `json:"hasMore"`
}

type GatewayTaskStep struct {
	ID             int64                          `json:"id"`
	StepID         string                         `json:"stepId"`
	StepDigest     string                         `json:"stepDigest"`
	Kind           string                         `json:"kind"`
	ModelID        string                         `json:"modelId"`
	ToolID         string                         `json:"toolId"`
	DocumentIDs    []string                       `json:"documentIds"`
	Categories     []string                       `json:"categories"`
	Status         string                         `json:"status"`
	SourceEvidence []gatewaygovernance.TaskSource `json:"sourceEvidence"`
	CreatedAt      time.Time                      `json:"createdAt"`
	CompletedAt    *time.Time                     `json:"completedAt,omitempty"`
}

type GatewayTaskDetail struct {
	Task           GatewayTask       `json:"task"`
	Steps          []GatewayTaskStep `json:"steps"`
	StepsTruncated bool              `json:"stepsTruncated"`
}

type GatewayTaskStatusInput struct {
	ExpectedRevision int64  `json:"expectedRevision"`
	Status           string `json:"status"`
	Reason           string `json:"reason"`
}
