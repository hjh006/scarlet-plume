package store

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

func TestRuntimeCaptureReplayIdentity(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	input := runtimesecurity.Observation{Method: "get", Route: "/contacts/{id}", Status: 200, ObservedAt: now, TraceRef: strings.Repeat("1", 32), SpanRef: strings.Repeat("2", 16), SubjectRef: strings.Repeat("3", 64),
		DatabaseSystems: []string{"postgresql"},
		DatabaseTargets: []runtimesecurity.DatabaseTarget{{System: "PostgreSQL", Source: "PG sample", Database: "scarlet_sample", Schema: "inventory", Table: "contacts", Operation: "select"}, {System: "postgresql", Source: "PG sample", Database: "scarlet_sample", Schema: "inventory", Table: "orders", Operation: "SELECT"}},
		DataFields:      []runtimesecurity.DataField{{Name: "phone", Location: "request", Category: "phone", Count: 1, Observed: 2, Method: "format"}, {Name: "email", Location: "response", Category: "email", Count: 1, Observed: 2, Method: "format"}},
		CaptureWarnings: []string{"json_dynamic_keys", "json_depth_limit"}}
	before, _ := json.Marshal(input)
	_, digest, _, err := prepareRuntimeBatch("otlp", []runtimesecurity.Observation{input})
	if err != nil {
		t.Fatal(err)
	}
	clone := func() runtimesecurity.Observation {
		copy := input
		copy.DatabaseSystems = slices.Clone(input.DatabaseSystems)
		copy.DatabaseTargets = slices.Clone(input.DatabaseTargets)
		copy.DataFields = slices.Clone(input.DataFields)
		copy.CaptureWarnings = slices.Clone(input.CaptureWarnings)
		return copy
	}
	reordered := clone()
	reordered.Method = "GET"
	reordered.Route = "/contacts/{contact_id}"
	slices.Reverse(reordered.DatabaseTargets)
	slices.Reverse(reordered.DataFields)
	slices.Reverse(reordered.CaptureWarnings)
	reordered.DatabaseTargets = append(reordered.DatabaseTargets, reordered.DatabaseTargets[0])
	reordered.DataFields = append(reordered.DataFields, reordered.DataFields[0])
	_, replay, _, err := prepareRuntimeBatch("otlp", []runtimesecurity.Observation{reordered})
	if err != nil || digest != replay {
		t.Fatal("equivalent capture order, case, duplicates or route labels changed replay identity")
	}
	for name, mutate := range map[string]func(*runtimesecurity.Observation){
		"trace":     func(o *runtimesecurity.Observation) { o.TraceRef = strings.Repeat("4", 32) },
		"span":      func(o *runtimesecurity.Observation) { o.SpanRef = strings.Repeat("5", 16) },
		"subject":   func(o *runtimesecurity.Observation) { o.SubjectRef = strings.Repeat("6", 64) },
		"database":  func(o *runtimesecurity.Observation) { o.DatabaseTargets[0].Database = "different_database" },
		"source":    func(o *runtimesecurity.Observation) { o.DatabaseTargets[0].Source = "Another PG source" },
		"count":     func(o *runtimesecurity.Observation) { o.DataFields[0].Count = 2 },
		"direction": func(o *runtimesecurity.Observation) { o.DataFields[0].Location = "response" },
		"method":    func(o *runtimesecurity.Observation) { o.DataFields[0].Method = "metadata" },
		"scope":     func(o *runtimesecurity.Observation) { o.CaptureWarnings = append(o.CaptureWarnings, "json_size_limit") },
	} {
		t.Run(name, func(t *testing.T) {
			changed := clone()
			mutate(&changed)
			_, got, _, err := prepareRuntimeBatch("otlp", []runtimesecurity.Observation{changed})
			if err != nil || got == digest {
				t.Fatalf("changed capture was treated as the same replay: %v", err)
			}
		})
	}
	after, _ := json.Marshal(input)
	if string(before) != string(after) {
		t.Fatal("runtime preparation mutated its caller's slices")
	}
	if _, _, _, err := prepareRuntimeBatch("gateway", []runtimesecurity.Observation{input}); err != ErrInvalid {
		t.Fatal("gateway supplied an untrusted database target")
	}

	// Retained receipts created before capture fields existed must still replay
	// under the exact legacy JSON projection when those optional fields are absent.
	legacy := runtimesecurity.Observation{Method: "GET", Route: "/contacts/{id}", Status: 200, ObservedAt: now}
	_, got, _, err := prepareRuntimeBatch("gateway", []runtimesecurity.Observation{legacy})
	if err != nil {
		t.Fatal(err)
	}
	stamp, _ := json.Marshal(now)
	wire := `{"method":"GET","route":"/contacts/{}","status":200,"observedAt":` + string(stamp) + `}`
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(wire)))
	if got != want {
		t.Fatal("capture upgrade changed the replay digest of a legacy observation")
	}
}
