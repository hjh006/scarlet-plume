package store

import (
	"errors"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
	"time"
)

var (
	ErrGatewayBudget    = errors.New("gateway budget exceeded")
	ErrGatewayRate      = errors.New("gateway rate limit exceeded")
	ErrGatewayCircuit   = errors.New("gateway route circuit open")
	ErrGatewayTaskSteps = errors.New("gateway task step limit exceeded")
)

type GatewayCachePolicy struct {
	Enabled    bool   `json:"enabled"`
	TTLSeconds int64  `json:"ttlSeconds"`
	MaxEntries int64  `json:"maxEntries"`
	MaxBytes   int64  `json:"maxBytes"`
	KeyRef     string `json:"keyRef"`
	KeyVersion string `json:"keyVersion"`
}

type GatewayApplication struct {
	RequireTask       bool               `json:"requireTask,omitempty"`
	CachePolicy       GatewayCachePolicy `json:"cachePolicy"`
	AllowRag          bool               `json:"allowRag"`
	ID                string             `json:"id"`
	Name              string             `json:"name"`
	Enabled           bool               `json:"enabled"`
	PrincipalID       string             `json:"principalId"`
	ModelIDs          []string           `json:"modelIds"`
	ToolIDs           []string           `json:"toolIds"`
	BudgetMicros      int64              `json:"budgetMicros"`
	RequestsPerMinute int64              `json:"requestsPerMinute"`
	TokensPerMinute   int64              `json:"tokensPerMinute"`
}

type GatewayProvider struct {
	FallbackModelIDs       []string `json:"fallbackModelIds"`
	Region                 string   `json:"region"`
	ResponseFormat         string   `json:"responseFormat"`
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	Provider               string   `json:"provider"`
	ModelID                string   `json:"modelId"`
	UpstreamModel          string   `json:"upstreamModel"`
	EndpointRef            string   `json:"endpointRef"`
	SecretRef              string   `json:"secretRef"`
	Enabled                bool     `json:"enabled"`
	InputMicrosPerMillion  int64    `json:"inputMicrosPerMillion"`
	OutputMicrosPerMillion int64    `json:"outputMicrosPerMillion"`
}

type GatewayConfig struct {
	CatalogDigest string               `json:"catalogDigest"`
	Revision      int64                `json:"revision"`
	Enabled       bool                 `json:"enabled"`
	Applications  []GatewayApplication `json:"applications"`
	Providers     []GatewayProvider    `json:"providers"`
}

type GatewayUsage struct {
	ApplicationID  string `json:"applicationId"`
	ReservedMicros int64  `json:"reservedMicros"`
	SpentMicros    int64  `json:"spentMicros"`
}

type GatewayCredential struct {
	ExpiresAt         *time.Time `json:"expiresAt,omitempty"`
	WorkloadBindingID *int64     `json:"workloadBindingId,omitempty"`
	ID                int64      `json:"id"`
	ApplicationID     string     `json:"applicationId"`
	Name              string     `json:"name"`
	Prefix            string     `json:"prefix"`
	Enabled           bool       `json:"enabled"`
	CreatedAt         time.Time  `json:"createdAt"`
}

type GatewayNode struct {
	Capabilities []string  `json:"capabilities"`
	Version      string    `json:"version"`
	ID           string    `json:"id"`
	Status       string    `json:"status"`
	Failures     int       `json:"failures"`
	LastSeenAt   time.Time `json:"lastSeenAt"`
}

type GatewayRouteHealth struct {
	ProviderID  string     `json:"providerId"`
	Failures    int        `json:"failures"`
	OpenUntil   *time.Time `json:"openUntil"`
	LastOutcome string     `json:"lastOutcome"`
}
type GatewayOverview struct {
	RouteHealth       []GatewayRouteHealth     `json:"routeHealth"`
	Config            GatewayConfig            `json:"config"`
	Usage             []GatewayUsage           `json:"usage"`
	Credentials       []GatewayCredential      `json:"credentials"`
	CredentialPreview GatewayCredentialPreview `json:"credentialPreview"`
	Nodes             []GatewayNode            `json:"nodes"`
	Runtime           struct {
		Configured bool `json:"configured"`
	} `json:"runtime"`
}

// These DTOs form the authenticated control-plane protocol used by every gateway node.
// A request ID is unique within a workspace for its entire retention period.
type GatewayAuthorizeInput struct {
	gatewaygovernance.TaskContext
	SourceDigests          []string `json:"sourceDigests,omitempty"`
	ProjectionBytes        int64    `json:"projectionBytes,omitempty"`
	MediaBytes             int64    `json:"mediaBytes,omitempty"`
	MediaDigests           []string `json:"mediaDigests,omitempty"`
	MediaInspectionVersion string   `json:"mediaInspectionVersion,omitempty"`

	ParentRequestID       string `json:"parentRequestId,omitempty"`
	RootModelID           string `json:"rootModelId,omitempty"`
	PreviousReservationID string `json:"previousReservationId,omitempty"`
	Attempt               int    `json:"attempt,omitempty"`
	WorkspaceSlug         string `json:"workspaceSlug"`
	TokenHash             string `json:"tokenHash"`
	ApplicationID         string `json:"applicationId"`
	PrincipalID           string `json:"principalId"`
	Kind                  string `json:"kind"`
	RequestID             string `json:"requestId"`
	ModelID               string `json:"modelId"`
	ToolID                string `json:"toolId"`
	Action                string `json:"action"`
	Target                string `json:"target"`
	RequestDigest         string `json:"requestDigest"`
	CatalogDigest         string `json:"catalogDigest"`
	InputBytes            int64  `json:"inputBytes"`
	MaxOutputTokens       int64  `json:"maxOutputTokens"`
}

type GatewayAuthorization struct {
	gatewaygovernance.TaskContext
	SourceDigests          []string `json:"sourceDigests,omitempty"`
	ProjectionBytes        int64    `json:"projectionBytes,omitempty"`
	MediaBytes             int64    `json:"mediaBytes,omitempty"`
	MediaDigests           []string `json:"mediaDigests,omitempty"`
	MediaInspectionVersion string   `json:"mediaInspectionVersion,omitempty"`

	ParentRequestID       string   `json:"parentRequestId,omitempty"`
	RootModelID           string   `json:"rootModelId,omitempty"`
	PreviousReservationID string   `json:"previousReservationId,omitempty"`
	Attempt               int      `json:"attempt,omitempty"`
	FallbackModelIDs      []string `json:"fallbackModelIds"`
	ConfigRevision        int64    `json:"configRevision"`
	Region                string   `json:"region"`
	ResponseFormat        string   `json:"responseFormat"`
	RequestID             string   `json:"requestId"`
	RequestDigest         string   `json:"requestDigest"`
	CatalogDigest         string   `json:"catalogDigest"`
	Kind                  string   `json:"kind"`
	CredentialID          int64    `json:"credentialId"`
	ReservationID         string   `json:"reservationId"`
	ApplicationID         string   `json:"applicationId"`
	PrincipalID           string   `json:"principalId"`
	ModelID               string   `json:"modelId"`
	ProviderID            string   `json:"providerId"`
	Provider              string   `json:"provider"`
	UpstreamModel         string   `json:"upstreamModel"`
	EndpointRef           string   `json:"endpointRef"`
	SecretRef             string   `json:"secretRef"`
	MaxCostMicros         int64    `json:"maxCostMicros"`
	MaxTokens             int64    `json:"maxTokens"`
	Replayed              bool     `json:"replayed"`
}

type GatewaySettlement struct {
	WorkspaceSlug string                  `json:"workspaceSlug"`
	ReservationID string                  `json:"reservationId"`
	RequestDigest string                  `json:"requestDigest"`
	Audit         *aisecurity.AuditRecord `json:"audit"`
}

type GatewayAuditEvent = gatewaygovernance.AuditEvent
type GatewayAuditPage = gatewaygovernance.AuditPage

type GatewayResolveInput struct {
	WorkspaceSlug string `json:"workspaceSlug"`
	TokenHash     string `json:"tokenHash"`
}
type GatewayIdentity struct {
	RequireTask    bool                `json:"requireTask,omitempty"`
	OpenModelIDs   []string            `json:"openModelIds"`
	Fallbacks      map[string][]string `json:"fallbacks"`
	ToolIDs        []string            `json:"toolIds"`
	CatalogDigest  string              `json:"catalogDigest"`
	ConfigRevision int64               `json:"configRevision"`
	CredentialID   int64               `json:"credentialId"`
	ApplicationID  string              `json:"applicationId"`
	PrincipalID    string              `json:"principalId"`
}

// GatewayDeniedEvent is metadata-only and has no reservation or billing fields.
type GatewayDeniedEvent struct {
	gatewaygovernance.TaskContext
	TokenHash             string                  `json:"tokenHash,omitempty"`
	ConfigRevision        int64                   `json:"configRevision,omitempty"`
	ModelID               string                  `json:"modelId,omitempty"`
	ParentRequestID       string                  `json:"parentRequestId,omitempty"`
	RootModelID           string                  `json:"rootModelId,omitempty"`
	PreviousReservationID string                  `json:"previousReservationId,omitempty"`
	Attempt               int                     `json:"attempt,omitempty"`
	WorkspaceSlug         string                  `json:"workspaceSlug"`
	Audit                 *aisecurity.AuditRecord `json:"audit"`
}

type GatewayDeniedReceipt struct {
	Accepted            bool     `json:"accepted"`
	Replayed            bool     `json:"replayed"`
	Verified            bool     `json:"verified"`
	VerificationReasons []string `json:"verificationReasons"`
}
