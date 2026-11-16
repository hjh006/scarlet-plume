package store

import "github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"

// This contract is written only by admission, inside the persisted reservation.
// It is absent from public request/authorization DTOs. Text-only inputs also pin
// it because their provider response may contain media.
type gatewayReservationEvidence struct {
	GatewayAuthorization
	RequiredMediaInspectionVersion string `json:"requiredMediaInspectionVersion,omitempty"`
}

func gatewayCurrentMediaMetadata(digests []string, version string) bool {
	return gatewayMediaMetadata(digests, version) && (len(digests) == 0 || version == aisecurity.MediaInspectionVersion)
}
func gatewayReservationMediaCompatible(saved gatewayReservationEvidence, d aisecurity.Decision) bool {
	if len(d.MediaDigests) == 0 {
		return true
	}
	required := saved.RequiredMediaInspectionVersion
	if required == "" {
		// Missing means a reservation written before the v2 contract. Preserve its
		// historical v1 report; never infer the new minimum-confidence semantics.
		required = aisecurity.LegacyMediaInspectionVersion
	}
	return (required == aisecurity.MediaInspectionVersion || required == aisecurity.LegacyMediaInspectionVersion) && d.MediaInspectionVersion == required
}
