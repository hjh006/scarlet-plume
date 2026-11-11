package store

import (
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

func TestRuntimeDetectionSingleNanosecondObservationImmediatelyFindsSignal(t *testing.T) {
	f := newOperationsFixture(t)
	ctx, repo, ws := f.ctx, f.repo, f.workspace
	token := operationsTestDigest(f.slug + "-precision")
	source, err := repo.CreateRuntimeSource(ctx, ws.ID, f.owner.ID, "Precision probe", "Precision app", "otlp", token)
	f.must(t, err)
	stamp := time.Now().UTC().Truncate(time.Microsecond).Add(922 * time.Nanosecond)
	event := runtimesecurity.Observation{Method: "GET", Route: "/xss", Status: 200, ObservedAt: stamp, SecuritySignals: []runtimesecurity.SecuritySignal{{Kind: "xss", Location: "request", Count: 1, Observed: 1}}}
	_, err = repo.DeliverRuntimeBatch(ctx, "otlp", token, operationsTestDigest("single-nanosecond-signal"), []runtimesecurity.Observation{event})
	f.must(t, err)
	findings, err := repo.ListRuntimeFindings(ctx, ws.ID)
	f.must(t, err)
	if len(findings) != 1 || findings[0].Rule != "xss_signal" || findings[0].CheckStatus != "draft" || findings[0].SourceID != source.ID || findings[0].Detection == nil || findings[0].Detection.Matched != 1 || !findings[0].Detection.WindowEnd.Equal(stamp) {
		t.Fatalf("single signal was delayed/lost at microsecond bound: %+v", findings)
	}
	policy := defaultRuntimeDetectionPolicies()[7]
	if policy.Rule != "xss_signal" {
		t.Fatal("fixture policy changed")
	}
	future := event
	future.ObservedAt = stamp.Add(time.Nanosecond)
	_, err = f.pool.Exec(ctx, `INSERT INTO plume.runtime_detection_samples(workspace_id,source_id,batch_id,ordinal,observed_at,observation) VALUES($1,$2,$3,0,($4::jsonb->>'observedAt')::timestamptz,$4)`, ws.ID, source.ID, operationsTestDigest("future-one-nanosecond"), future)
	f.must(t, err)
	tx, err := f.pool.Begin(ctx)
	f.must(t, err)
	defer tx.Rollback(ctx)
	read, err := readRuntimeDetectionSamples(ctx, tx, runtimeFindingBatch{workspaceID: ws.ID, sourceID: source.ID}, stamp)
	f.must(t, err)
	if len(read) != 2 {
		t.Fatal("SQL precision margin did not include the rounded boundary candidates")
	}
	detection := evaluateRuntimeDetection(policy, []runtimesecurity.Observation{future}, runtimeRouteKey{"GET", "/xss"}, stamp, "", false)
	if detection.Requests != 0 || detection.Matched != 0 || runtimeDetectionPositive(detection) {
		t.Fatal("one-nanosecond future sample crossed the exact Go cutoff")
	}
	detection = evaluateRuntimeDetection(policy, read, runtimeRouteKey{"GET", "/xss"}, stamp, "", false)
	if detection.Requests != 1 || detection.Matched != 1 {
		t.Fatal("SQL margin changed the original-precision evaluation window")
	}
}
