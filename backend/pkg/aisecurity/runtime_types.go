package aisecurity

import "time"

// AuditRecord contains no request/response body, arguments, endpoint URLs or credentials.
type AuditRecord struct {
	CacheEvidence          *CacheEvidence `json:"cacheEvidence,omitempty"`
	AttemptID              string         `json:"attemptId,omitempty"`
	RuntimeProtocolVersion string         `json:"runtimeProtocolVersion,omitempty"`
	InspectionVersion      string         `json:"inspectionVersion,omitempty"`
	MCPProtocolVersion     string         `json:"mcpProtocolVersion,omitempty"`
	EventID                string         `json:"eventId"`
	RequestID              string         `json:"requestId"`
	Stage                  string         `json:"stage"`
	ObservedAt             time.Time      `json:"observedAt"`
	Decision               Decision       `json:"decision"`
	Execution              Execution      `json:"execution"`
}
type Execution struct {
	// OutputReleased is an optional positive delivery fact. An absent value is
	// not proof that the client received no bytes; completed records processing.
	OutputReleased bool   `json:"outputReleased,omitempty"`
	CacheHit       bool   `json:"cacheHit,omitempty"`
	Attempted      bool   `json:"attempted"`
	Outcome        string `json:"outcome"`
	HTTPStatus     int    `json:"httpStatus,omitempty"`
}
