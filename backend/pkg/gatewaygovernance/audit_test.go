package gatewaygovernance

import (
	"encoding/json"
	"testing"
	"time"
)

func TestHashEventV1Contract(t *testing.T) {
	event := AuditEvent{Sequence: 1, WorkspaceID: 42, ActorID: 7, Action: "gateway.call_reserved", Target: "reservation", Payload: json.RawMessage(`{"reservedMicros":10}`), OccurredAt: time.Unix(1, 0).UTC()}
	const expected = "f697a30e70a5890ce80f76f5f32cf11b17f6c521f587ee955b3616c80ae308ea"
	if got := HashEvent(event); got != expected {
		t.Fatalf("v1 audit hash contract changed: %s", got)
	}
	event.Hash = "an existing digest is excluded"
	event.OccurredAt = event.OccurredAt.In(time.FixedZone("offset", 8*3600))
	if got := HashEvent(event); got != expected {
		t.Fatal("v1 UTC/hash normalization changed")
	}
}
