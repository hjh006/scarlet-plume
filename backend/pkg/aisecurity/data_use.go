package aisecurity

import (
	"slices"
	"time"
)

// DataUsePolicy constrains source data wherever it goes, including later task
// steps. An omitted policy preserves legacy category controls. A present policy
// with no purposes permits no use; zero retention requires zero declared retention.
type DataUsePolicy struct {
	AllowedPurposes   []string `json:"allowedPurposes"`
	MaxRetentionHours int      `json:"maxRetentionHours"`
	AllowTraining     bool     `json:"allowTraining"`
}

// DataHandling is an operator-approved destination contract with an expiring
// evidence reference, not independent attestation of a remote provider's conduct.
type DataHandling struct {
	RetentionHours         int       `json:"retentionHours"`
	TrainingUse            string    `json:"trainingUse"`
	EvidenceRef            string    `json:"evidenceRef"`
	AuthorizationExpiresAt time.Time `json:"authorizationExpiresAt"`
}

func validateDataUse(p *DataUsePolicy) bool {
	return p == nil || list(p.AllowedPurposes, 64, validID) && p.MaxRetentionHours >= 0 && p.MaxRetentionHours <= 8760
}

func validateDataHandling(h *DataHandling) bool {
	return h == nil || h.RetentionHours >= 0 && h.RetentionHours <= 8760 && slices.Contains([]string{"prohibited", "permitted"}, h.TrainingUse) && validID(h.EvidenceRef) && !h.AuthorizationExpiresAt.IsZero()
}

func currentDataHandling(h *DataHandling, now time.Time) bool {
	return h != nil && validateDataHandling(h) && h.AuthorizationExpiresAt.After(now)
}

func sourceUseAllowed(doc Document, purpose string, handling *DataHandling, destination bool, now time.Time) string {
	if doc.UsePolicy == nil {
		return ""
	}
	if purpose == "" || !slices.Contains(doc.UsePolicy.AllowedPurposes, purpose) {
		return "source_purpose_denied"
	}
	if !destination {
		return ""
	}
	if !currentDataHandling(handling, now) {
		return "destination_handling_missing_or_expired"
	}
	if handling.RetentionHours > doc.UsePolicy.MaxRetentionHours {
		return "source_retention_denied"
	}
	if !doc.UsePolicy.AllowTraining && handling.TrainingUse != "prohibited" {
		return "source_training_use_denied"
	}
	return ""
}
