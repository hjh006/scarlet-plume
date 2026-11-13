package store

import (
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
	"time"
)

type OperationsScope struct {
	JobID    int64    `json:"jobId,omitempty"`
	Source   string   `json:"source,omitempty"`
	Host     string   `json:"host,omitempty"`
	Port     int      `json:"port,omitempty"`
	Database string   `json:"database,omitempty"`
	Schemas  []string `json:"schemas,omitempty"`
}
type OperationsSource struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	OwnerTeam string          `json:"ownerTeam"`
	Scope     OperationsScope `json:"scope"`
	CreatedAt time.Time       `json:"createdAt"`
	LastSeen  *time.Time      `json:"lastSeen,omitempty"`
	RevokedAt *time.Time      `json:"revokedAt,omitempty"`
}
type OperationsSourceInput struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	OwnerTeam string `json:"ownerTeam"`
	JobID     int64  `json:"jobId,omitempty"`
}
type SecurityEventInput struct {
	DataContext   *datasafety.Context `json:"dataContext,omitempty"`
	Outcome       string              `json:"outcome"`
	EventID       string              `json:"eventId"`
	ObservedAt    time.Time           `json:"observedAt"`
	Category      string              `json:"category"`
	Rule          string              `json:"rule"`
	Severity      string              `json:"severity"`
	AssetID       int64               `json:"assetId,omitempty"`
	Service       string              `json:"service,omitempty"`
	Method        string              `json:"method,omitempty"`
	Route         string              `json:"route,omitempty"`
	Count         int64               `json:"count"`
	WindowSeconds int                 `json:"windowSeconds"`
}
type OperationsSecurityEvent struct {
	SecurityEventInput
	ID          int64  `json:"id"`
	SourceID    int64  `json:"sourceId"`
	Source      string `json:"source"`
	CheckID     int64  `json:"checkId"`
	CheckStatus string `json:"checkStatus"`
}
type ControlSpec struct {
	Service        string `json:"service,omitempty"`
	Method         string `json:"method,omitempty"`
	Route          string `json:"route,omitempty"`
	Host           string `json:"host,omitempty"`
	Port           int    `json:"port,omitempty"`
	Database       string `json:"database,omitempty"`
	Schema         string `json:"schema,omitempty"`
	Table          string `json:"table,omitempty"`
	Role           string `json:"role,omitempty"`
	ExpectedSelect bool   `json:"expectedSelect"`
}
type ControlActionInput struct {
	Kind               string `json:"kind"`
	RuntimeSourceID    int64  `json:"runtimeSourceId,omitempty"`
	OperationsSourceID int64  `json:"operationsSourceId,omitempty"`
	AssetID            int64  `json:"assetId,omitempty"`
	CheckID            int64  `json:"checkId,omitempty"`
	Method             string `json:"method,omitempty"`
	Route              string `json:"route,omitempty"`
	Role               string `json:"role,omitempty"`
	Reason             string `json:"reason"`
	DurationMinutes    int    `json:"durationMinutes"`
}
type ControlResult struct {
	Phase      string    `json:"phase"`
	Success    bool      `json:"success"`
	Code       string    `json:"code"`
	HadSelect  *bool     `json:"hadSelect,omitempty"`
	HasSelect  *bool     `json:"hasSelect,omitempty"`
	ObservedAt time.Time `json:"observedAt"`
}
type ControlAction struct {
	ID                 int64          `json:"id"`
	Kind               string         `json:"kind"`
	Actor              string         `json:"actor"`
	ApprovedBy         string         `json:"approvedBy,omitempty"`
	RuntimeSourceID    *int64         `json:"runtimeSourceId,omitempty"`
	OperationsSourceID *int64         `json:"operationsSourceId,omitempty"`
	AssetID            *int64         `json:"assetId,omitempty"`
	CheckID            *int64         `json:"checkId,omitempty"`
	Spec               ControlSpec    `json:"spec"`
	Reason             string         `json:"reason"`
	Status             string         `json:"status"`
	Revision           int64          `json:"revision"`
	Result             *ControlResult `json:"result,omitempty"`
	ExpiresAt          time.Time      `json:"expiresAt"`
	CreatedAt          time.Time      `json:"createdAt"`
	UpdatedAt          time.Time      `json:"updatedAt"`
	Events             []ControlEvent `json:"events,omitempty"`
}
type ControlEvent struct {
	Action    string    `json:"action"`
	Actor     string    `json:"actor"`
	Note      string    `json:"note"`
	Automated bool      `json:"automated"`
	CreatedAt time.Time `json:"createdAt"`
}
type ControlCommand struct {
	ID        int64          `json:"id"`
	Revision  int64          `json:"revision"`
	Phase     string         `json:"phase"`
	Status    string         `json:"status"`
	Lease     string         `json:"lease,omitempty"`
	Spec      ControlSpec    `json:"spec"`
	ExpiresAt time.Time      `json:"expiresAt"`
	Original  *ControlResult `json:"original,omitempty"`
}
type ControlReceipt struct {
	ActionID int64         `json:"actionId"`
	Revision int64         `json:"revision"`
	Lease    string        `json:"lease,omitempty"`
	Result   ControlResult `json:"result"`
}
