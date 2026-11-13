package store

import "time"

type APIValidationPlanInput struct {
	ProbePolicy     APIValidationProbePolicy `json:"probePolicy,omitempty"`
	Revision        int64                    `json:"revision"`
	SourceID        int64                    `json:"sourceId"`
	Name            string                   `json:"name"`
	Service         string                   `json:"service"`
	Method          string                   `json:"method"`
	Route           string                   `json:"route"`
	TargetOrigin    string                   `json:"targetOrigin"`
	ManifestRef     string                   `json:"manifestRef"`
	Rules           []string                 `json:"rules"`
	IntervalMinutes int                      `json:"intervalMinutes"`
}
type APIValidationPlan struct {
	CanApprove bool `json:"canApprove"`
	APIValidationPlanInput
	ID             int64      `json:"id"`
	State          string     `json:"state"`
	CreatedBy      int64      `json:"createdBy"`
	ApprovedBy     *int64     `json:"approvedBy,omitempty"`
	ApprovedAt     *time.Time `json:"approvedAt,omitempty"`
	NextRunAt      time.Time  `json:"nextRunAt"`
	LeaseExpiresAt *time.Time `json:"leaseExpiresAt,omitempty"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}
type APIValidationClaim struct {
	Mode           string            `json:"mode"`
	Plan           APIValidationPlan `json:"plan"`
	RunRef         string            `json:"runRef"`
	LeaseExpiresAt time.Time         `json:"leaseExpiresAt"`
}
type APIValidationResult struct {
	PlanID     int64                     `json:"planId"`
	Revision   int64                     `json:"revision"`
	RunRef     string                    `json:"runRef"`
	ObservedAt time.Time                 `json:"observedAt"`
	Mode       string                    `json:"mode"`
	Results    []APIValidationRuleResult `json:"results"`
}
type APIValidationRuleResult struct {
	AnonymousStatus          int      `json:"anonymousStatus,omitempty"`
	AnonymousObjectConfirmed bool     `json:"anonymousObjectConfirmed,omitempty"`
	PolicyConfirmed          bool     `json:"policyConfirmed,omitempty"`
	PolicyDigest             string   `json:"policyDigest,omitempty"`
	SensitiveChecked         int      `json:"sensitiveChecked,omitempty"`
	OwnerSensitiveCount      int      `json:"ownerSensitiveCount,omitempty"`
	AnonymousSensitiveCount  int      `json:"anonymousSensitiveCount,omitempty"`
	HeaderChecked            int      `json:"headerChecked,omitempty"`
	HeaderMismatches         []string `json:"headerMismatches,omitempty"`
	OtherAccountConfirmed    bool     `json:"otherAccountConfirmed"`
	OtherOwnedStatus         int      `json:"otherOwnedStatus,omitempty"`
	Rule                     string   `json:"rule"`
	Outcome                  string   `json:"outcome"`
	OwnerStatus              int      `json:"ownerStatus"`
	OtherStatus              int      `json:"otherStatus,omitempty"`
	OwnerConfirmed           bool     `json:"ownerConfirmed"`
	SameObject               bool     `json:"sameObject"`
	CredentialAccepted       bool     `json:"credentialAccepted"`
	WeakRuleMatched          bool     `json:"weakRuleMatched"`
	ComparisonComplete       bool     `json:"comparisonComplete"`
	Requests                 int      `json:"requests"`
	Gaps                     []string `json:"gaps"`
}
type APIValidationRun struct {
	Mode            string               `json:"mode"`
	ProjectionState string               `json:"projectionState"`
	CheckIDs        []int64              `json:"checkIds"`
	ID              int64                `json:"id"`
	PlanID          int64                `json:"planId"`
	Revision        int64                `json:"revision"`
	RunRef          string               `json:"runRef"`
	Plan            APIValidationPlan    `json:"plan"`
	State           string               `json:"state"`
	Result          *APIValidationResult `json:"result,omitempty"`
	EventIDs        []string             `json:"eventIds"`
	ErrorCode       string               `json:"errorCode,omitempty"`
	CreatedAt       time.Time            `json:"createdAt"`
	CompletedAt     *time.Time           `json:"completedAt,omitempty"`
}
