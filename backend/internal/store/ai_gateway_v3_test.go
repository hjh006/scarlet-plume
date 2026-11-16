package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func gatewayV3Config(t *testing.T, f assessmentSourceFixture, fallback bool) GatewayConfig {
	t.Helper()
	c := gatewayTestConfig(1_000_000, 10000, 1_000_000)
	c.Applications[0].CachePolicy = GatewayCachePolicy{Enabled: true, TTLSeconds: 60, MaxEntries: 2, MaxBytes: 4096, KeyRef: "env:SCARLET_GATEWAY_TEST_CACHE_KEY", KeyVersion: "test-v1"}
	if fallback {
		p := c.Providers[0]
		p.ID = "alternate"
		p.ModelID = "alternate-model"
		p.Provider = "anthropic"
		p.UpstreamModel = "alternate-upstream"
		c.Providers[0].FallbackModelIDs = []string{p.ModelID}
		c.Providers = append(c.Providers, p)
		c.Applications[0].ModelIDs = append(c.Applications[0].ModelIDs, p.ModelID)
	}
	out, e := f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.user.ID, 1, c)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("SCARLET_GATEWAY_TEST_CACHE_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xa7}, 32)))
	return out
}
func gatewayV3CacheWrite(t *testing.T, f assessmentSourceFixture, in GatewayAuthorizeInput, label string) GatewayCachePut {
	t.Helper()
	in.RequestID = label
	in.RequestDigest = gatewayDigest(label)
	grant, e := f.repo.AuthorizeGateway(t.Context(), in)
	if e != nil {
		t.Fatal(e)
	}
	scope := GatewayCacheScope{WorkspaceSlug: in.WorkspaceSlug, TokenHash: in.TokenHash, ApplicationID: in.ApplicationID, PrincipalID: in.PrincipalID, ModelID: in.ModelID, CatalogDigest: in.CatalogDigest, InspectionVersion: aisecurity.InspectionVersion, InputHash: gatewayDigest(label), DocumentHash: gatewayDigest([]string{}), PolicyHash: in.CatalogDigest}
	put := GatewayCachePut{CacheScope: scope, ReservationID: grant.ReservationID, Output: `{"content":"safe response ` + label + `"}`}
	sum := sha256.Sum256([]byte(put.Output))
	put.OutputHash = fmt.Sprintf("%x", sum)
	settle := gatewayTestSettlement(in, grant, true, "succeeded")
	settle.Audit.InspectionVersion = aisecurity.InspectionVersion
	settle.Audit.CacheEvidence = &aisecurity.CacheEvidence{ModelID: in.ModelID, InputBytes: 2, OutputBytes: int64(len(put.Output)), InputHash: scope.InputHash, DocumentHash: scope.DocumentHash, PolicyHash: scope.PolicyHash, OutputHash: put.OutputHash, InspectionVersion: scope.InspectionVersion}
	if e = f.repo.SettleGateway(t.Context(), settle); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.PutGatewayCache(t.Context(), put); e != nil {
		t.Fatal(e)
	}
	return put
}
func TestGatewayV3CacheEncryptionScopePoisoningAndRevocation(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	gatewayV3Config(t, f, false)
	put := gatewayV3CacheWrite(t, f, in, "cache-v3-1")
	hit, e := f.repo.GetGatewayCache(t.Context(), put.CacheScope)
	if e != nil || !hit.Hit || hit.Output != put.Output {
		t.Fatalf("cache hit=%v err=%v", hit.Hit, e)
	}
	var ciphertext []byte
	if e = f.pool.QueryRow(t.Context(), `SELECT ciphertext FROM plume.ai_gateway_cache WHERE workspace_id=$1`, f.ws.ID).Scan(&ciphertext); e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(ciphertext, []byte(put.Output)) {
		t.Fatal("response stored in plaintext")
	}
	poison := put
	poison.InputHash = gatewayDigest("different request")
	if e = f.repo.PutGatewayCache(t.Context(), poison); !errors.Is(e, ErrForbidden) {
		t.Fatalf("cache poison accepted: %v", e)
	}
	missed, e := f.repo.GetGatewayCache(t.Context(), poison.CacheScope)
	if e != nil || missed.Hit {
		t.Fatalf("scope pollution: %v", e)
	}
	foreign := put.CacheScope
	foreign.PrincipalID = "other"
	if _, e = f.repo.GetGatewayCache(t.Context(), foreign); !errors.Is(e, ErrForbidden) {
		t.Fatalf("principal isolation: %v", e)
	}
	other, _ := gatewayTestSetup(t, 100, 10, 100)
	foreign = put.CacheScope
	foreign.WorkspaceSlug = other.ws.Slug
	if _, e = f.repo.GetGatewayCache(t.Context(), foreign); !errors.Is(e, ErrForbidden) {
		t.Fatalf("tenant isolation: %v", e)
	}
	n, e := f.repo.InvalidateGatewayCache(t.Context(), f.ws.ID, f.user.ID, in.ApplicationID)
	if e != nil || n != 1 {
		t.Fatalf("invalidate=%d %v", n, e)
	}
	if e = f.repo.PutGatewayCache(t.Context(), put); !errors.Is(e, ErrForbidden) {
		t.Fatalf("expired admission repopulated invalidated policy: %v", e)
	}
	hit, e = f.repo.GetGatewayCache(t.Context(), put.CacheScope)
	if e != nil || hit.Hit {
		t.Fatalf("invalidated cache hit=%v err=%v", hit.Hit, e)
	}
	var id int64
	if e = f.pool.QueryRow(t.Context(), `SELECT id FROM plume.ai_gateway_credentials WHERE workspace_id=$1`, f.ws.ID).Scan(&id); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.RevokeGatewayCredential(t.Context(), f.ws.ID, f.user.ID, id); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.GetGatewayCache(t.Context(), put.CacheScope); !errors.Is(e, ErrForbidden) {
		t.Fatalf("revoked credential cache bypass: %v", e)
	}
}
func TestGatewayV3CacheLRUTTLAndConcurrentLimits(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	gatewayV3Config(t, f, false)
	first := gatewayV3CacheWrite(t, f, in, "cache-lru-first")
	second := gatewayV3CacheWrite(t, f, in, "cache-lru-second")
	if _, e := f.repo.GetGatewayCache(t.Context(), first.CacheScope); e != nil {
		t.Fatal(e)
	}
	third := gatewayV3CacheWrite(t, f, in, "cache-lru-third")
	if out, e := f.repo.GetGatewayCache(t.Context(), second.CacheScope); e != nil || out.Hit {
		t.Fatalf("LRU retained evicted output: %v", e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- f.repo.PutGatewayCache(t.Context(), third) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var count, total int64
	if e := f.pool.QueryRow(t.Context(), `SELECT count(*),COALESCE(sum(size_bytes),0) FROM plume.ai_gateway_cache WHERE workspace_id=$1`, f.ws.ID).Scan(&count, &total); e != nil || count > 2 || total > 4096 {
		t.Fatalf("cache limits count=%d bytes=%d err=%v", count, total, e)
	}
	if _, e := f.pool.Exec(t.Context(), `UPDATE plume.ai_gateway_cache SET expires_at=clock_timestamp()-interval '1 second' WHERE workspace_id=$1`, f.ws.ID); e != nil {
		t.Fatal(e)
	}
	if out, e := f.repo.GetGatewayCache(t.Context(), third.CacheScope); e != nil || out.Hit {
		t.Fatalf("expired cache accepted: %v", e)
	}
}
func TestGatewayV3FallbackUnknownBudgetParentRaceAndSharedCircuit(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	gatewayV3Config(t, f, true)
	in.ParentRequestID = "parent-v3"
	in.RootModelID = in.ModelID
	first, e := f.repo.AuthorizeGateway(t.Context(), in)
	if e != nil {
		t.Fatal(e)
	}
	failed := gatewayTestSettlement(in, first, true, "failed")
	failed.Audit.Execution.HTTPStatus = 503
	failed.Audit.Decision.Outcome = "deny"
	if e = f.repo.SettleGateway(t.Context(), failed); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.SettleGateway(t.Context(), failed); e != nil {
		t.Fatal(e)
	}
	alt := in
	alt.ModelID = "alternate-model"
	alt.Attempt = 1
	alt.PreviousReservationID = first.ReservationID
	alt.RequestID = "fallback-v3"
	alt.RequestDigest = gatewayDigest(alt.RequestID)
	var wg sync.WaitGroup
	grants := make(chan GatewayAuthorization, 12)
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			candidate := alt
			candidate.RequestID = fmt.Sprintf("fallback-race-%d", i)
			g, e := f.repo.AuthorizeGateway(t.Context(), candidate)
			if e != nil {
				errs <- e
			} else {
				grants <- g
			}
		}()
	}
	wg.Wait()
	close(grants)
	close(errs)
	if len(grants) != 1 {
		t.Fatalf("parent cross-node duplicate attempts=%d", len(grants))
	}
	for e := range errs {
		if !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
	}
	second := <-grants
	gatewayAssertUsage(t, f, first.MaxCostMicros+second.MaxCostMicros, 0)
	bad := alt
	bad.ParentRequestID = "foreign-parent"
	bad.RequestID = "foreign-attempt"
	if _, e = f.repo.AuthorizeGateway(t.Context(), bad); !errors.Is(e, ErrForbidden) {
		t.Fatalf("parent substitution: %v", e)
	}
	for i := range 2 {
		r := in
		r.ParentRequestID = fmt.Sprintf("health-parent-%d", i)
		r.RequestID = fmt.Sprintf("health-request-%d", i)
		r.RequestDigest = gatewayDigest(r.RequestID)
		g, e := f.repo.AuthorizeGateway(t.Context(), r)
		if e != nil {
			t.Fatal(e)
		}
		settle := gatewayTestSettlement(r, g, true, "failed")
		settle.Audit.Execution.HTTPStatus = 503
		settle.Audit.Decision.Outcome = "deny"
		if e = f.repo.SettleGateway(t.Context(), settle); e != nil {
			t.Fatal(e)
		}
	}
	identity, e := f.repo.ResolveGateway(t.Context(), GatewayResolveInput{WorkspaceSlug: in.WorkspaceSlug, TokenHash: in.TokenHash})
	if e != nil || len(identity.OpenModelIDs) != 1 || identity.OpenModelIDs[0] != "model" {
		t.Fatalf("shared circuit identity=%+v err=%v", identity, e)
	}
	skip := alt
	skip.ParentRequestID = "circuit-skip-parent"
	skip.PreviousReservationID = ""
	skip.RequestID = "circuit-skip-attempt"
	if _, e = f.repo.AuthorizeGateway(t.Context(), skip); e != nil {
		t.Fatalf("governed circuit skip: %v", e)
	}
}

func TestGatewayV3CacheHitMetadataAndMediaVersionPreserved(t *testing.T) {
	f, in := gatewayTestSetup(t, 100, 10, 100)
	gatewayV3Config(t, f, false)
	event := gatewayTestDenied(in, GatewayAuthorization{})
	event.TokenHash = in.TokenHash
	event.ModelID = in.ModelID
	event.ConfigRevision = 2
	event.Audit.Stage = "completed"
	event.Audit.Decision.Outcome = "allow"
	event.Audit.Decision.Code = "allowed"
	event.Audit.Execution.CacheHit = true
	event.Audit.CacheEvidence = &aisecurity.CacheEvidence{ModelID: in.ModelID, InputBytes: 2, OutputBytes: 1, InputHash: in.RequestDigest, DocumentHash: in.RequestDigest, PolicyHash: in.CatalogDigest, OutputHash: in.RequestDigest, InspectionVersion: aisecurity.InspectionVersion, CacheHit: true}
	result, e := f.repo.RecordGatewayDenied(t.Context(), event)
	if e != nil || !result.Accepted || !result.Verified {
		t.Fatalf("cache terminal metadata rejected: %+v %v", result, e)
	}
	gatewayAssertUsage(t, f, 0, 0)
	replay, e := f.repo.RecordGatewayDenied(t.Context(), event)
	if e != nil || !replay.Replayed {
		t.Fatalf("cache transport replay: %+v %v", replay, e)
	}
	copied := *event.Audit
	duplicate := event
	duplicate.Audit = &copied
	duplicate.Audit.EventID = "cache-another-node-event"
	if _, e = f.repo.RecordGatewayDenied(t.Context(), duplicate); !errors.Is(e, ErrConflict) {
		t.Fatalf("cross-node cache nonce replay: %v", e)
	}
	if _, e = f.repo.AuthorizeGateway(t.Context(), in); !errors.Is(e, ErrConflict) {
		t.Fatalf("cache-to-provider nonce replay: %v", e)
	}
	providerFirst := in
	providerFirst.RequestID = "provider-first-request"
	if _, e = f.repo.AuthorizeGateway(t.Context(), providerFirst); e != nil {
		t.Fatal(e)
	}
	duplicate.Audit.EventID = "cache-after-provider-event"
	duplicate.Audit.RequestID = providerFirst.RequestID
	if _, e = f.repo.RecordGatewayDenied(t.Context(), duplicate); !errors.Is(e, ErrConflict) {
		t.Fatalf("provider-to-cache nonce replay: %v", e)
	}
	event.Audit.EventID = "media-future-event"
	event.Audit.Stage = "decision"
	event.Audit.Decision.Outcome = "deny"
	event.Audit.Execution.CacheHit = false
	event.Audit.CacheEvidence = nil
	event.Audit.Decision.MediaDigests = []string{in.RequestDigest}
	event.Audit.Decision.MediaInspectionVersion = "future-media-v2"
	result, e = f.repo.RecordGatewayDenied(t.Context(), event)
	if e != nil || result.Verified {
		t.Fatalf("unknown media marked verified: %+v %v", result, e)
	}
	page, e := f.repo.GatewayAudit(t.Context(), f.ws.ID, f.user.ID, 0, 500)
	if e != nil || !page.Valid {
		t.Fatal(e)
	}
	encoded, e := json.Marshal(page)
	if e != nil || !bytes.Contains(encoded, []byte("mediaDigests")) || !bytes.Contains(encoded, []byte("cacheEvidence")) {
		t.Fatal("new receipt evidence silently dropped")
	}
	event.Audit.EventID = "malformed-media-event"
	event.Audit.Decision.MediaDigests = []string{"raw media content"}
	if _, e = f.repo.RecordGatewayDenied(t.Context(), event); !errors.Is(e, ErrInvalid) {
		t.Fatalf("media plaintext accepted: %v", e)
	}
}

func TestGatewayV3CacheTerminalSharesRatesAndZeroBudget(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rpm, tpm int64
		want     error
	}{{"zero-rpm", 0, 100, ErrGatewayRate}, {"zero-tpm", 100, 0, ErrGatewayRate}, {"tight-tpm", 100, 2, ErrGatewayRate}, {"zero-provider-budget", 1, 100, nil}} {
		t.Run(tc.name, func(t *testing.T) {
			f, in := gatewayTestSetup(t, 100, 10, 100)
			cfg := gatewayV3Config(t, f, false)
			cfg.Applications[0].RequestsPerMinute = tc.rpm
			cfg.Applications[0].TokensPerMinute = tc.tpm
			cfg.Applications[0].BudgetMicros = 0
			if _, e := f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.user.ID, 2, cfg); e != nil {
				t.Fatal(e)
			}
			event := gatewayTestDenied(in, GatewayAuthorization{})
			event.TokenHash = in.TokenHash
			event.ConfigRevision = 3
			event.ModelID = in.ModelID
			event.Audit.Stage = "completed"
			event.Audit.Decision.Outcome = "allow"
			event.Audit.Decision.Code = "allowed"
			event.Audit.Execution.CacheHit = true
			event.Audit.CacheEvidence = &aisecurity.CacheEvidence{ModelID: in.ModelID, InputBytes: 2, OutputBytes: 1, InputHash: in.RequestDigest, DocumentHash: in.RequestDigest, PolicyHash: in.CatalogDigest, OutputHash: in.RequestDigest, InspectionVersion: aisecurity.InspectionVersion, CacheHit: true}
			_, e := f.repo.RecordGatewayDenied(t.Context(), event)
			if !errors.Is(e, tc.want) {
				t.Fatalf("cache rate err=%v want=%v", e, tc.want)
			}
			if tc.want == nil {
				var requests, tokens int64
				if e = f.pool.QueryRow(t.Context(), `SELECT requests,tokens FROM plume.ai_gateway_windows WHERE workspace_id=$1`, f.ws.ID).Scan(&requests, &tokens); e != nil || requests != 1 || tokens != 3 {
					t.Fatalf("cache shared rates requests=%d tokens=%d err=%v", requests, tokens, e)
				}
				event.Audit.EventID = "second-cache-event"
				event.Audit.RequestID = "second-cache-request"
				if _, e = f.repo.RecordGatewayDenied(t.Context(), event); !errors.Is(e, ErrGatewayRate) {
					t.Fatalf("cache exceeded shared RPM: %v", e)
				}
			}
			gatewayAssertUsage(t, f, 0, 0)
		})
	}
}
