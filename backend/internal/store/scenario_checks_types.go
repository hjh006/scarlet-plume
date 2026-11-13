package store

import (
	"encoding/json"
	"time"
)

type ScenarioCheckRunInput struct {
	Scope         *RuntimeEvidenceScope `json:"scope,omitempty"`
	Scenario      string                `json:"scenario"`
	Task          string                `json:"task"`
	AssetID       int64                 `json:"assetId"`
	ActivityID    int64                 `json:"activityId"`
	PreviousRunID *int64                `json:"previousRunId,omitempty"`
}
type ScenarioCheckBasis struct {
	Title   string `json:"title"`
	Clause  string `json:"clause"`
	Version string `json:"version"`
	URL     string `json:"url"`
}
type ScenarioCheckCounts struct {
	Compliant         int `json:"compliant"`
	NonCompliant      int `json:"nonCompliant"`
	NeedsVerification int `json:"needsVerification"`
}
type ScenarioCheckRemediation struct {
	Advice     string              `json:"advice"`
	Executable bool                `json:"executable"`
	Kind       string              `json:"kind,omitempty"`
	Reason     string              `json:"reason"`
	Target     *ControlActionInput `json:"target,omitempty"`
}
type ScenarioCheckFinding struct {
	ID                string                   `json:"id"`
	Title             string                   `json:"title"`
	Outcome           string                   `json:"outcome"`
	RequirementCode   string                   `json:"requirementCode"`
	Basis             ScenarioCheckBasis       `json:"basis"`
	RuleVersion       string                   `json:"ruleVersion"`
	Expected          string                   `json:"expected"`
	Observed          string                   `json:"observed"`
	EvidenceRefs      []string                 `json:"evidenceRefs"`
	Coverage          string                   `json:"coverage"`
	MissingConditions []string                 `json:"missingConditions"`
	Remediation       ScenarioCheckRemediation `json:"remediation"`
	CheckID           *int64                   `json:"checkId,omitempty"`
	ActionID          *int64                   `json:"actionId,omitempty"`
}
type ScenarioCheckEvidence struct {
	ID         string          `json:"id"`
	Source     string          `json:"source"`
	Record     string          `json:"record"`
	ObservedAt *time.Time      `json:"observedAt,omitempty"`
	Digest     string          `json:"digest"`
	Scope      string          `json:"scope"`
	Snapshot   json.RawMessage `json:"snapshot"`
}
type ScenarioCheckReportRequirement struct {
	Key                  string             `json:"key"`
	Title                string             `json:"title"`
	Basis                ScenarioCheckBasis `json:"basis"`
	Applicability        string             `json:"applicability"`
	RequiredDeliverables []string           `json:"requiredDeliverables"`
	FindingIDs           []string           `json:"findingIds"`
	EvidenceRefs         []string           `json:"evidenceRefs"`
	Coverage             string             `json:"coverage"`
	Status               string             `json:"status"`
	MissingConditions    []string           `json:"missingConditions"`
	ExternalSteps        []string           `json:"externalSteps"`
	RetentionNote        string             `json:"retentionNote"`
}
type ScenarioCheckActionLink struct {
	FindingID string         `json:"findingId"`
	ActionID  int64          `json:"actionId"`
	Status    string         `json:"status"`
	Revision  int64          `json:"revision"`
	CreatedAt time.Time      `json:"createdAt"`
	Result    *ControlResult `json:"result,omitempty"`
}
type ScenarioCheckRun struct {
	Scope              *RuntimeEvidenceScope            `json:"scope,omitempty"`
	ID                 int64                            `json:"id"`
	Scenario           string                           `json:"scenario"`
	Task               string                           `json:"task"`
	AssetID            int64                            `json:"assetId"`
	AssetName          string                           `json:"assetName"`
	ActivityID         int64                            `json:"activityId,omitempty"`
	AssetRevision      int64                            `json:"assetRevision"`
	ActivityRevision   int64                            `json:"activityRevision,omitempty"`
	Status             string                           `json:"status"`
	Outcome            string                           `json:"outcome"`
	Mode               string                           `json:"mode"`
	EngineVersion      string                           `json:"engineVersion"`
	CreatedAt          time.Time                        `json:"createdAt"`
	CreatedBy          int64                            `json:"createdBy"`
	Actor              string                           `json:"actor"`
	Counts             ScenarioCheckCounts              `json:"counts"`
	Findings           []ScenarioCheckFinding           `json:"findings,omitempty"`
	Evidence           []ScenarioCheckEvidence          `json:"evidence,omitempty"`
	ReportRequirements []ScenarioCheckReportRequirement `json:"reportRequirements,omitempty"`
	SnapshotDigest     string                           `json:"snapshotDigest"`
	PreviousRunID      *int64                           `json:"previousRunId,omitempty"`
	RetestState        string                           `json:"retestState"`
	Warnings           []string                         `json:"warnings"`
	Actions            []ScenarioCheckActionLink        `json:"actions,omitempty"`
}
