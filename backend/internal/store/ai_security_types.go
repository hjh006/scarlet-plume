package store

import (
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"time"
)

type AISecurityConfiguration struct {
	Configuration     aisecurity.Config `json:"configuration"`
	Revision          int64             `json:"revision"`
	UpdatedBy         int64             `json:"updatedBy"`
	UpdatedAt         time.Time         `json:"updatedAt"`
	PublishedRevision int64             `json:"publishedRevision"`
	PublishedDigest   string            `json:"publishedDigest"`
	PublishedAt       *time.Time        `json:"publishedAt,omitempty"`
}
type AISecurityDecisionRecord struct {
	ID                    int64               `json:"id"`
	Mode                  string              `json:"mode"`
	ConfigurationRevision int64               `json:"configurationRevision"`
	CreatedAt             time.Time           `json:"createdAt"`
	Decision              aisecurity.Decision `json:"decision"`
}
type AISecurityEvaluationCase struct {
	CatalogScope     string `json:"catalogScope,omitempty"`
	ID               string `json:"id,omitempty"`
	Probe            string `json:"probe,omitempty"`
	Dimension        string `json:"dimension,omitempty"`
	DefinitionDigest string `json:"definitionDigest,omitempty"`
	Status           string `json:"status,omitempty"`
	Name             string `json:"name"`
	Expected         string `json:"expected"`
	Actual           string `json:"actual"`
	Passed           bool   `json:"passed"`
	Code             string `json:"code"`
}
type AISecurityPublicationReview struct {
	PackDigest            string     `json:"packDigest,omitempty"`
	EngineVersion         string     `json:"engineVersion,omitempty"`
	RecheckedAt           *time.Time `json:"recheckedAt,omitempty"`
	ActorID               int64      `json:"actorId"`
	ConfigurationRevision int64      `json:"configurationRevision"`
	Note                  string     `json:"note"`
	CreatedAt             time.Time  `json:"createdAt"`
}
type AISecurityEvaluation struct {
	Pack                  *AISecurityRegressionPackSummary `json:"pack,omitempty"`
	GateVersion           string                           `json:"gateVersion,omitempty"`
	EngineVersion         string                           `json:"engineVersion,omitempty"`
	InspectionVersion     string                           `json:"inspectionVersion,omitempty"`
	RequiredDimensions    []string                         `json:"requiredDimensions,omitempty"`
	CoveredDimensions     []string                         `json:"coveredDimensions,omitempty"`
	CoverageGaps          []string                         `json:"coverageGaps"`
	GateEligible          bool                             `json:"gateEligible"`
	Diagnostics           []AISecurityEvaluationCase       `json:"diagnostics,omitempty"`
	PublicationReviews    []AISecurityPublicationReview    `json:"publicationReviews,omitempty"`
	ID                    int64                            `json:"id"`
	ConfigurationRevision int64                            `json:"configurationRevision"`
	CatalogDigest         string                           `json:"catalogDigest"`
	Status                string                           `json:"status"`
	Scope                 []string                         `json:"scope"`
	Cases                 []AISecurityEvaluationCase       `json:"cases"`
	CreatedAt             time.Time                        `json:"createdAt"`
}
type AISecurityApproval struct {
	LeaseChainDigest      string            `json:"leaseChainDigest,omitempty"`
	LeaseProtocolVersion  string            `json:"leaseProtocolVersion,omitempty"`
	TaskID                string            `json:"taskId,omitempty"`
	ID                    int64             `json:"id"`
	ConfigurationRevision int64             `json:"configurationRevision"`
	RequestDigest         string            `json:"requestDigest"`
	CatalogDigest         string            `json:"catalogDigest"`
	RequestID             string            `json:"requestId"`
	IssuedAt              time.Time         `json:"issuedAt"`
	ApplicationID         string            `json:"applicationId"`
	PrincipalID           string            `json:"principalId"`
	ToolID                string            `json:"toolId"`
	Action                string            `json:"action"`
	Target                string            `json:"target"`
	Arguments             map[string]string `json:"arguments"`
	Status                string            `json:"status"`
	RequestedBy           int64             `json:"requestedBy"`
	ReviewedBy            *int64            `json:"reviewedBy,omitempty"`
	ExpiresAt             time.Time         `json:"expiresAt"`
	CreatedAt             time.Time         `json:"createdAt"`
	Note                  string            `json:"note"`
}
type AISecurityCoverage struct {
	Mode           string   `json:"mode"`
	Limitations    []string `json:"limitations"`
	TrustedSources int      `json:"trustedSources"`
	Assets         int      `json:"assets"`
}
type AISecurityBundle struct {
	HistoryCoverage map[string]AISecurityHistoryWindow `json:"historyCoverage"`
	AISecurityConfiguration
	Decisions   []AISecurityDecisionRecord `json:"decisions"`
	Evaluations []AISecurityEvaluation     `json:"evaluations"`
	Approvals   []AISecurityApproval       `json:"approvals"`
	Coverage    AISecurityCoverage         `json:"coverage"`
}
