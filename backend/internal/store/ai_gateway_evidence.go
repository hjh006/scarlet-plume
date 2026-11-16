package store

import "github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"

// Additional receipt fields are validated before both ingestion paths persist
// the complete record. The evidence remains a node report, never a bill.
func validGatewayRuntimeEvidence(a aisecurity.AuditRecord) bool {
	if !gatewayTaskMetadata(a.Decision) {
		return false
	}
	if !gatewayMediaMetadata(a.Decision.MediaDigests, a.Decision.MediaInspectionVersion) {
		return false
	}
	if a.Execution.OutputReleased && (a.Stage != "completed" || a.Decision.Outcome != "allow" || a.Execution.Outcome != "succeeded" && !a.Execution.CacheHit) {
		return false
	}
	if a.Execution.CacheHit && (a.Stage != "completed" || a.Decision.Outcome != "allow" || a.Decision.Kind != "model" || a.Execution.Attempted || a.Execution.Outcome != "not_attempted" || a.Execution.HTTPStatus != 0 || a.CacheEvidence == nil || !a.CacheEvidence.CacheHit) {
		return false
	}
	if a.CacheEvidence != nil {
		c := a.CacheEvidence
		if c.InputBytes < 1 || c.InputBytes > 16_777_216 || c.OutputBytes < 1 || c.OutputBytes > 1_048_576 {
			return false
		}
		if a.Decision.Kind != "model" || !gatewayRuntimeID.MatchString(c.ModelID) || !gatewayHash.MatchString(c.InputHash) || !gatewayHash.MatchString(c.DocumentHash) || !gatewayHash.MatchString(c.PolicyHash) || !gatewayHash.MatchString(c.OutputHash) || c.InspectionVersion != a.InspectionVersion || !gatewayRuntimeID.MatchString(c.InspectionVersion) || c.CacheHit != a.Execution.CacheHit {
			return false
		}
	}
	return true
}

func gatewayTaskMetadata(d aisecurity.Decision) bool {
	if d.TaskID != "" || d.TaskPurpose != "" || d.TaskScopeDigest != "" {
		if !gatewayRuntimeID.MatchString(d.TaskID) || !gatewayRuntimeID.MatchString(d.TaskPurpose) || !gatewayHash.MatchString(d.TaskScopeDigest) {
			return false
		}
	}
	if len(d.SourceDigests) > 64 {
		return false
	}
	seen := map[string]bool{}
	for _, hash := range d.SourceDigests {
		if !gatewayHash.MatchString(hash) || seen[hash] {
			return false
		}
		seen[hash] = true
	}
	return true
}

func gatewayMediaMetadata(digests []string, version string) bool {
	if len(digests) > 32 || version != "" && !gatewayRuntimeID.MatchString(version) || len(digests) > 0 && version == "" || len(digests) == 0 && version != "" {
		return false
	}
	seen := map[string]bool{}
	for _, d := range digests {
		if !gatewayHash.MatchString(d) || seen[d] {
			return false
		}
		seen[d] = true
	}
	return true
}
