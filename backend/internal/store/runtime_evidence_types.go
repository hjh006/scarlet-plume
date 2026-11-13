package store

import (
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/valuefingerprint"
	"time"
)

type RuntimeEvidenceScope struct {
	SourceID         int64                   `json:"sourceId"`
	Service          string                  `json:"service,omitempty"`
	Method           string                  `json:"method,omitempty"`
	Route            string                  `json:"route,omitempty"`
	SubjectRef       string                  `json:"subjectRef,omitempty"`
	ClientIP         string                  `json:"clientIp,omitempty"`
	From             time.Time               `json:"from"`
	To               time.Time               `json:"to"`
	ValueFingerprint *valuefingerprint.Token `json:"valueFingerprint,omitempty"`
}
type RuntimeValueMatch struct {
	SourceID   int64     `json:"sourceId"`
	Source     string    `json:"source"`
	Service    string    `json:"service"`
	ObservedAt time.Time `json:"observedAt"`
	Method     string    `json:"method"`
	Route      string    `json:"route"`
	SubjectRef string    `json:"subjectRef,omitempty"`
	ClientIP   string    `json:"clientIp,omitempty"`
	Locator    string    `json:"locator"`
	Category   string    `json:"category"`
	Record     string    `json:"record"`
}
type RuntimeEvidenceEntry struct {
	ID            int64                       `json:"id"`
	SourceID      int64                       `json:"sourceId"`
	Source        string                      `json:"source"`
	Service       string                      `json:"service"`
	Kind          string                      `json:"kind"`
	SourceCurrent bool                        `json:"sourceCurrent"`
	Observation   runtimesecurity.Observation `json:"observation"`
}
type RuntimeEvidenceWindow struct {
	Scope                   RuntimeEvidenceScope   `json:"scope"`
	Items                   []RuntimeEvidenceEntry `json:"items"`
	Total                   int64                  `json:"total"`
	Limit                   int                    `json:"limit"`
	Truncated               bool                   `json:"truncated"`
	RetentionDays           int                    `json:"retentionDays"`
	RetentionLimitPerSource int                    `json:"retentionLimitPerSource"`
	Warnings                []string               `json:"warnings"`
}
type OperationsReportInput struct {
	Title string               `json:"title"`
	Scope RuntimeEvidenceScope `json:"scope"`
}
type OperationsInvestigationReport struct {
	Analysis          []ScenarioCheckFinding  `json:"analysis"`
	ScheduleID        *int64                  `json:"scheduleId,omitempty"`
	Period            string                  `json:"period,omitempty"`
	Timezone          string                  `json:"timezone,omitempty"`
	ID                int64                   `json:"id"`
	Title             string                  `json:"title"`
	CreatedBy         int64                   `json:"createdBy"`
	Actor             string                  `json:"actor"`
	CreatedAt         time.Time               `json:"createdAt"`
	Scope             RuntimeEvidenceScope    `json:"scope"`
	Window            RuntimeEvidenceWindow   `json:"window"`
	Findings          []RuntimeFinding        `json:"findings"`
	Actions           []ControlAction         `json:"actions"`
	Evidence          []ScenarioCheckEvidence `json:"evidence"`
	Coverage          []string                `json:"coverage"`
	MissingConditions []string                `json:"missingConditions"`
	SnapshotDigest    string                  `json:"snapshotDigest"`
}

type OperationsReportScheduleInput struct {
	Title    string               `json:"title"`
	Period   string               `json:"period"`
	Timezone string               `json:"timezone"`
	Scope    RuntimeEvidenceScope `json:"scope"`
	Enabled  bool                 `json:"enabled"`
	Revision int64                `json:"revision"`
}
type OperationsReportSchedule struct {
	OperationsReportScheduleInput
	ID           int64     `json:"id"`
	NextRunAt    time.Time `json:"nextRunAt"`
	LastReportID *int64    `json:"lastReportId,omitempty"`
	CreatedBy    int64     `json:"createdBy"`
	UpdatedAt    time.Time `json:"updatedAt"`
}
