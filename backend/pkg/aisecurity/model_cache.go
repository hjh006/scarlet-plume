package aisecurity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ModelCache stores only normalized outputs after the enforcement boundary.
// Implementations must enforce tenant scope, bounded TTL/size, and encryption.
type ModelCache interface {
	Get(context.Context, ModelCacheScope) ([]byte, bool, error)
	Put(context.Context, ModelCacheScope, []byte) error
}
type ModelCacheScope struct {
	InputBytes        int64  `json:"-"`
	ApplicationID     string `json:"applicationId"`
	PrincipalID       string `json:"principalId"`
	ModelID           string `json:"modelId"`
	CatalogDigest     string `json:"catalogDigest"`
	InspectionVersion string `json:"inspectionVersion"`
	InputHash         string `json:"inputHash"`
	DocumentHash      string `json:"documentHash"`
	PolicyHash        string `json:"policyHash"`
}
type CacheEvidence struct {
	InputBytes        int64  `json:"inputBytes"`
	OutputBytes       int64  `json:"outputBytes"`
	ModelID           string `json:"modelId"`
	InputHash         string `json:"inputHash"`
	DocumentHash      string `json:"documentHash"`
	PolicyHash        string `json:"policyHash"`
	OutputHash        string `json:"outputHash"`
	InspectionVersion string `json:"inspectionVersion"`
	CacheHit          bool   `json:"cacheHit"`
}
type cacheEvidenceKey struct{}

func modelCacheScope(r Request, d Decision) ModelCacheScope {
	docs, _ := json.Marshal(r.DocumentIDs)
	if len(r.sourceEvidence) > 0 || r.taskContext != nil {
		docs, _ = json.Marshal(struct {
			IDs     []string
			Sources []string
			Task    string
		}{r.DocumentIDs, sourceDigestList(r), taskContextCache(r)})
	}
	inputBytes := int64(len(sourceInput(r)))
	if r.modelWireBytes > 0 {
		inputBytes = r.modelWireBytes
	}
	return ModelCacheScope{InputBytes: inputBytes, ApplicationID: r.ApplicationID, PrincipalID: r.PrincipalID, ModelID: r.ModelID, CatalogDigest: d.CatalogDigest, InspectionVersion: InspectionVersion, InputHash: cacheTextDigest(r.Input), DocumentHash: cacheTextDigest(string(docs)), PolicyHash: d.CatalogDigest}
}
func cacheEvidence(scope ModelCacheScope, raw []byte, hit bool) *CacheEvidence {
	return &CacheEvidence{InputBytes: scope.InputBytes, OutputBytes: int64(len(raw)), ModelID: scope.ModelID, InputHash: scope.InputHash, DocumentHash: scope.DocumentHash, PolicyHash: scope.PolicyHash, OutputHash: cacheTextDigest(string(raw)), InspectionVersion: scope.InspectionVersion, CacheHit: hit}
}

func cacheTextDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
