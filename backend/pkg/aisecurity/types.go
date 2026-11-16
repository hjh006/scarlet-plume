// Package aisecurity enforces bounded, catalog-backed AI authorization rules.
// A Decision is a policy result, never evidence that an upstream action ran.
package aisecurity

import "time"

const RuntimeProtocolVersion = "scarlet-ai-runtime-v2"
const InspectionVersion = "scarlet-detectors-v1"
const LegacyInspectionVersion = "datasafety-four-format-v1"

type Config struct {
	Version      string        `json:"version"`
	Applications []Application `json:"applications"`
	Principals   []Principal   `json:"principals"`
	Documents    []Document    `json:"documents"`
	Models       []Model       `json:"models"`
	Tools        []Tool        `json:"tools"`
	Policy       Policy        `json:"policy"`
	Approvals    []Approval    `json:"approvals"`
}
type Application struct {
	RequireTask     bool     `json:"requireTask,omitempty"`
	Version         string   `json:"version,omitempty"`
	SourceRef       string   `json:"sourceRef,omitempty"`
	IntegrityDigest string   `json:"integrityDigest,omitempty"`
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Enabled         bool     `json:"enabled"`
	PrincipalIDs    []string `json:"principalIds"`
	DocumentIDs     []string `json:"documentIds"`
	ModelIDs        []string `json:"modelIds"`
	ToolIDs         []string `json:"toolIds"`
}
type Principal struct {
	AuthorizationExpiresAt *time.Time `json:"authorizationExpiresAt,omitempty"`
	ID                     string     `json:"id"`
	Name                   string     `json:"name"`
	Enabled                bool       `json:"enabled"`
	ApplicationIDs         []string   `json:"applicationIds"`
}

// Document holds only source metadata. Content is resolved in the customer runtime.
type Document struct {
	UsePolicy              *DataUsePolicy `json:"usePolicy,omitempty"`
	AuthorizationExpiresAt *time.Time     `json:"authorizationExpiresAt,omitempty"`
	IntegrityDigest        string         `json:"integrityDigest,omitempty"`
	ID                     string         `json:"id"`
	Name                   string         `json:"name"`
	Version                string         `json:"version"`
	ACLVersion             string         `json:"aclVersion"`
	KnowledgeBaseID        string         `json:"knowledgeBaseId"`
	SourceRef              string         `json:"sourceRef"`
	AllowedPrincipalIDs    []string       `json:"allowedPrincipalIds"`
	Categories             []string       `json:"categories"`
	Enabled                bool           `json:"enabled"`
}
type Model struct {
	Handling                *DataHandling `json:"handling,omitempty"`
	Media                   *MediaPolicy  `json:"media,omitempty"`
	ResponseFormat          string        `json:"responseFormat,omitempty"`
	UpstreamModel           string        `json:"upstreamModel,omitempty"`
	AllowedInputCategories  []string      `json:"allowedInputCategories"`
	AllowedOutputCategories []string      `json:"allowedOutputCategories"`
	Version                 string        `json:"version,omitempty"`
	SourceRef               string        `json:"sourceRef,omitempty"`
	IntegrityDigest         string        `json:"integrityDigest,omitempty"`
	ID                      string        `json:"id"`
	Name                    string        `json:"name"`
	Provider                string        `json:"provider"`
	EndpointRef             string        `json:"endpointRef"`
	Region                  string        `json:"region"`
	Enabled                 bool          `json:"enabled"`
	AllowedApplicationIDs   []string      `json:"allowedApplicationIds"`
}
type Tool struct {
	Handling               *DataHandling       `json:"handling,omitempty"`
	AllowedInputCategories []string            `json:"allowedInputCategories,omitempty"`
	ProtocolVersion        string              `json:"protocolVersion,omitempty"`
	DefinitionDigests      map[string]string   `json:"definitionDigests,omitempty"`
	Protocol               string              `json:"protocol,omitempty"`
	TargetArgument         string              `json:"targetArgument,omitempty"`
	Version                string              `json:"version,omitempty"`
	SourceRef              string              `json:"sourceRef,omitempty"`
	IntegrityDigest        string              `json:"integrityDigest,omitempty"`
	ID                     string              `json:"id"`
	Name                   string              `json:"name"`
	EndpointRef            string              `json:"endpointRef"`
	Enabled                bool                `json:"enabled"`
	AllowedApplicationIDs  []string            `json:"allowedApplicationIds"`
	AllowedPrincipalIDs    []string            `json:"allowedPrincipalIds"`
	Actions                []string            `json:"actions"`
	Targets                []string            `json:"targets"`
	AllowedArguments       map[string][]string `json:"allowedArguments"`
	RequiredArguments      []string            `json:"requiredArguments"`
	HighImpact             bool                `json:"highImpact"`
}
type Policy struct {
	Inspection                     *InspectionPolicy `json:"inspection,omitempty"`
	RequirePinnedSourceContent     bool              `json:"requirePinnedSourceContent,omitempty"`
	RequireDataHandling            bool              `json:"requireDataHandling,omitempty"`
	RequiredRuntimeProtocolVersion string            `json:"requiredRuntimeProtocolVersion,omitempty"`
	RequiredInspectionVersion      string            `json:"requiredInspectionVersion,omitempty"`
	RequireAuthorizationLeases     bool              `json:"requireAuthorizationLeases,omitempty"`
	Version                        string            `json:"version"`
	MaxRequestAgeSeconds           int               `json:"maxRequestAgeSeconds"`
	MaxInputBytes                  int               `json:"maxInputBytes"`
	MaxOutputBytes                 int               `json:"maxOutputBytes"`
	AllowedInputCategories         []string          `json:"allowedInputCategories"`
	AllowedOutputCategories        []string          `json:"allowedOutputCategories"`
	AllowedModelRegions            []string          `json:"allowedModelRegions"`
	BlockedInputPatterns           []string          `json:"blockedInputPatterns"`
	BlockedOutputPatterns          []string          `json:"blockedOutputPatterns"`
}

// Approval is generated by a trusted independent approver, never accepted from a runtime request.
type Approval struct {
	LeaseChainDigest     string    `json:"leaseChainDigest,omitempty"`
	LeaseProtocolVersion string    `json:"leaseProtocolVersion,omitempty"`
	ID                   string    `json:"id"`
	RequestDigest        string    `json:"requestDigest"`
	CatalogDigest        string    `json:"catalogDigest"`
	ApplicationID        string    `json:"applicationId"`
	PrincipalID          string    `json:"principalId"`
	ToolID               string    `json:"toolId"`
	Action               string    `json:"action"`
	Target               string    `json:"target"`
	ApprovedBy           string    `json:"approvedBy"`
	ExpiresAt            time.Time `json:"expiresAt"`
	Revoked              bool      `json:"revoked"`
}

// PrincipalID/ApplicationID are authenticated runtime context. The wire runtime rejects them.
// The control-plane preview may pass them explicitly but must not treat that preview as enforcement.
type Request struct {
	taskContext           *TaskContext
	sourceInputProjection *string
	sourceInputCategories []string
	sourceEvidence        []SourceEvidence
	sourceInputBytes      int64
	sourceDocuments       map[string]LocalDocument
	sourceModelInput      string
	modelWireBytes        int64
	TaskID                string `json:"taskId,omitempty"`
	mediaInputProjection  *string
	mediaOutputProjection *string
	mediaInputCategories  []string
	mediaOutputCategories []string
	mediaCoverage         []string
	mediaDigests          []string
	mediaInputBytes       int64
	RequestID             string            `json:"requestId"`
	Kind                  string            `json:"kind"`
	ApplicationID         string            `json:"applicationId"`
	PrincipalID           string            `json:"principalId"`
	DocumentIDs           []string          `json:"documentIds,omitempty"`
	ModelID               string            `json:"modelId,omitempty"`
	ToolID                string            `json:"toolId,omitempty"`
	Action                string            `json:"action,omitempty"`
	Target                string            `json:"target,omitempty"`
	Arguments             map[string]string `json:"arguments,omitempty"`
	Input                 string            `json:"input,omitempty"`
	Output                string            `json:"output,omitempty"`
	ApprovalID            string            `json:"approvalId,omitempty"`
	IssuedAt              time.Time         `json:"issuedAt"`
	ExpiresAt             time.Time         `json:"expiresAt"`
}
type Decision struct {
	LeaseChainDigest       string   `json:"leaseChainDigest,omitempty"`
	LeaseProtocolVersion   string   `json:"leaseProtocolVersion,omitempty"`
	TaskID                 string   `json:"taskId,omitempty"`
	TaskPurpose            string   `json:"taskPurpose,omitempty"`
	TaskScopeDigest        string   `json:"taskScopeDigest,omitempty"`
	SourceDigests          []string `json:"sourceDigests,omitempty"`
	MediaDigests           []string `json:"mediaDigests,omitempty"`
	MediaInspectionVersion string   `json:"mediaInspectionVersion,omitempty"`
	Outcome                string   `json:"outcome"`
	Code                   string   `json:"code"`
	Rules                  []string `json:"rules"`
	CatalogVersion         string   `json:"catalogVersion"`
	CatalogDigest          string   `json:"catalogDigest"`
	RequestDigest          string   `json:"requestDigest"`
	Kind                   string   `json:"kind"`
	ApplicationID          string   `json:"applicationId"`
	PrincipalID            string   `json:"principalId"`
	AllowedDocumentIDs     []string `json:"allowedDocumentIds"`
	Categories             []string `json:"categories"`
	RequiresApproval       bool     `json:"requiresApproval"`
	Coverage               []string `json:"coverage"`
}

func DefaultConfig() Config {
	return Config{Version: "ai-security-v1", Applications: []Application{}, Principals: []Principal{}, Documents: []Document{}, Models: []Model{}, Tools: []Tool{}, Approvals: []Approval{}, Policy: Policy{Version: "ai-policy-v1", MaxRequestAgeSeconds: 300, MaxInputBytes: 65536, MaxOutputBytes: 65536, AllowedInputCategories: []string{}, AllowedOutputCategories: []string{}, AllowedModelRegions: []string{}, BlockedInputPatterns: []string{}, BlockedOutputPatterns: []string{}}}
}
