// Package gatewaygovernance defines the content-free audit export wire format.
package gatewaygovernance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

type AuditEvent struct {
	Sequence     int64           `json:"sequence"`
	WorkspaceID  int64           `json:"workspaceId"`
	ActorID      int64           `json:"actorId"`
	Action       string          `json:"action"`
	Target       string          `json:"target"`
	Payload      json.RawMessage `json:"payload"`
	OccurredAt   time.Time       `json:"occurredAt"`
	PreviousHash string          `json:"previousHash"`
	Hash         string          `json:"hash"`
}

type AuditPage struct {
	ProviderID     string       `json:"providerId,omitempty"`
	RouteSequences []int64      `json:"routeSequences,omitempty"`
	WorkspaceID    int64        `json:"workspaceId"`
	Items          []AuditEvent `json:"items"`
	NextCursor     int64        `json:"nextCursor"`
	Valid          bool         `json:"valid"`
	PreviousHash   string       `json:"previousHash"`
	HeadHash       string       `json:"headHash"`
	HeadSequence   int64        `json:"headSequence"`
}

// HashEvent is the persistent v1 chain contract. The fixed field order, empty
// trailing hash property, UTC RFC3339 timestamp and encoding/json compact/HTML
// escaping are immutable. Payload object member order is preserved. Add a new
// versioned algorithm to bind new wire fields; changing this breaks old archives.
func HashEvent(e AuditEvent) string {
	type auditHashV1 struct {
		Sequence     int64           `json:"sequence"`
		WorkspaceID  int64           `json:"workspaceId"`
		ActorID      int64           `json:"actorId"`
		Action       string          `json:"action"`
		Target       string          `json:"target"`
		Payload      json.RawMessage `json:"payload"`
		OccurredAt   time.Time       `json:"occurredAt"`
		PreviousHash string          `json:"previousHash"`
		Hash         string          `json:"hash"`
	}
	value := auditHashV1{Sequence: e.Sequence, WorkspaceID: e.WorkspaceID, ActorID: e.ActorID, Action: e.Action, Target: e.Target, Payload: e.Payload, OccurredAt: e.OccurredAt.UTC(), PreviousHash: e.PreviousHash}
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// HashEventV1 names the immutable persistent chain algorithm explicitly.
func HashEventV1(e AuditEvent) string { return HashEvent(e) }
