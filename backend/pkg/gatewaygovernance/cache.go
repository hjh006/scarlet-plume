package gatewaygovernance

// CacheScope is metadata only. Runtime nodes hash the full generation request,
// selected documents and current inspection policy before calling the cache.
type CacheScope struct {
	WorkspaceSlug     string `json:"workspaceSlug"`
	TokenHash         string `json:"tokenHash"`
	ApplicationID     string `json:"applicationId"`
	PrincipalID       string `json:"principalId"`
	ModelID           string `json:"modelId"`
	CatalogDigest     string `json:"catalogDigest"`
	InspectionVersion string `json:"inspectionVersion"`
	InputHash         string `json:"inputHash"`
	DocumentHash      string `json:"documentHash"`
	PolicyHash        string `json:"policyHash"`
}
type CacheResult struct {
	Hit        bool   `json:"hit"`
	Output     string `json:"output,omitempty"`
	OutputHash string `json:"outputHash,omitempty"`
}
type CachePut struct {
	CacheScope
	ReservationID string `json:"reservationId"`
	Output        string `json:"output"`
	OutputHash    string `json:"outputHash"`
}
