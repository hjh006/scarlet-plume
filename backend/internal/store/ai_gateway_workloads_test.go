package store

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/jackc/pgx/v5"
)

func workloadStoreFixture(t *testing.T, f assessmentSourceFixture) (GatewayWorkloadBinding, func(string) string) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	keys, _ := json.Marshal(map[string]any{"keys": []any{map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": "synthetic-key-1", "use": "sig", "alg": "EdDSA", "x": encode(pub)}}})
	in := GatewayWorkloadInput{Name: "Synthetic workload", ApplicationID: "app", Issuer: "https://issuer.example.invalid/workload", Audience: "scarlet-workload:" + f.ws.Slug, Subject: "spiffe://example.invalid/agent/support", JWKS: keys, MaxTTLSeconds: 60}
	binding, err := f.repo.SaveGatewayWorkload(t.Context(), f.ws.ID, f.user.ID, 0, 0, in, true)
	if err != nil {
		t.Fatal(err)
	}
	return binding, func(jti string) string {
		now := time.Now().UTC()
		header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": "synthetic-key-1", "typ": "JWT"})
		body, _ := json.Marshal(map[string]any{"iss": in.Issuer, "aud": in.Audience, "sub": in.Subject, "iat": now.Unix(), "exp": now.Unix() + 120, "jti": jti, "roles": []string{"ignored-owner"}, "applicationId": "ignored-app"})
		raw := encode(header) + "." + encode(body)
		return raw + "." + encode(ed25519.Sign(key, []byte(raw)))
	}
}

func workloadAuthorizeInput(in GatewayAuthorizeInput, token string) GatewayAuthorizeInput {
	sum := sha256.Sum256([]byte(token))
	in.TokenHash = hex.EncodeToString(sum[:])
	return in
}

func TestGatewayWorkloadExchangeMapsTrustedAppAndRevokesAllIssuedTokens(t *testing.T) {
	f, original := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	binding, sign := workloadStoreFixture(t, f)
	ctx := t.Context()
	viewer := gatewayTestMember(t, f, "viewer")
	if _, err := f.repo.SaveGatewayWorkload(ctx, f.ws.ID, viewer, 0, 0, binding.GatewayWorkloadInput, true); !errors.Is(err, ErrForbidden) {
		t.Fatal("viewer configured issuer trust", err)
	}
	page, err := f.repo.GatewayWorkloads(ctx, f.ws.ID, viewer, "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != binding.ID {
		t.Fatal("read-only binding inventory", err)
	}
	jwt := sign("exchange-once-0001")
	issued, err := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, binding.ID, jwt)
	if err != nil || issued.Token == "" || issued.Credential.ExpiresAt == nil || issued.Credential.WorkloadBindingID == nil || *issued.Credential.WorkloadBindingID != binding.ID || issued.ExpiresAt.After(time.Now().Add(61*time.Second)) {
		t.Fatal("short-lived token issuance failed", issued.Credential, err)
	}
	in := workloadAuthorizeInput(original, issued.Token)
	resolved, err := f.repo.ResolveGateway(ctx, GatewayResolveInput{WorkspaceSlug: f.ws.Slug, TokenHash: in.TokenHash})
	if err != nil || resolved.ApplicationID != "app" || resolved.PrincipalID != "principal" {
		t.Fatal("JWT claims overrode trusted app/principal", resolved, err)
	}
	if _, err = f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, binding.ID, jwt); !errors.Is(err, ErrConflict) {
		t.Fatal("assertion replay issued another credential", err)
	}
	other := assessmentSourceTestStore(t)
	if _, err = f.repo.ExchangeGatewayWorkloadToken(ctx, other.ws.Slug, binding.ID, sign("cross-workspace-0001")); !errors.Is(err, ErrWorkloadAssertion) {
		t.Fatal("issuer binding escaped workspace", err)
	}
	updated, err := f.repo.SaveGatewayWorkload(ctx, f.ws.ID, f.user.ID, binding.ID, 1, binding.GatewayWorkloadInput, true)
	if err != nil || updated.Revision != 2 {
		t.Fatal(updated, err)
	}
	if _, err = f.repo.ResolveGateway(ctx, GatewayResolveInput{WorkspaceSlug: f.ws.Slug, TokenHash: in.TokenHash}); !errors.Is(err, ErrForbidden) {
		t.Fatal("modified trust retained old credential", err)
	}
	if _, err = f.repo.SaveGatewayWorkload(ctx, f.ws.ID, f.user.ID, binding.ID, 1, binding.GatewayWorkloadInput, true); !errors.Is(err, ErrConflict) {
		t.Fatal("stale workload CAS accepted", err)
	}
	second, err := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, binding.ID, sign("exchange-once-0002"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.RevokeGatewayWorkload(ctx, f.ws.ID, f.user.ID, binding.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, binding.ID, sign("exchange-once-0003")); !errors.Is(err, ErrWorkloadAssertion) {
		t.Fatal("revoked binding still minted credentials", err)
	}
	short := workloadAuthorizeInput(original, second.Token)
	if _, err = f.repo.ResolveGateway(ctx, GatewayResolveInput{WorkspaceSlug: f.ws.Slug, TokenHash: short.TokenHash}); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked binding's issued credential still resolved", err)
	}
	if _, err = f.repo.ResolveGateway(ctx, GatewayResolveInput{WorkspaceSlug: f.ws.Slug, TokenHash: original.TokenHash}); err != nil {
		t.Fatal("workload revocation altered unrelated legacy credential", err)
	}
	audit, err := f.repo.GatewayAudit(ctx, f.ws.ID, f.user.ID, 0, 500)
	if err != nil || !audit.Valid {
		t.Fatal("workload audit chain invalid", err)
	}
	var sawExchange bool
	for _, e := range audit.Items {
		if e.Action == "gateway.workload_token_exchanged" {
			sawExchange = true
			if strings.Contains(string(e.Payload), jwt) || strings.Contains(string(e.Payload), issued.Token) || strings.Contains(string(e.Payload), "exchange-once-0001") || !strings.Contains(string(e.Payload), binding.Issuer) || !strings.Contains(string(e.Payload), "jtiDigest") {
				t.Fatal("workload evidence leaked a raw assertion/token or lost provenance")
			}
		}
	}
	if !sawExchange {
		t.Fatal("no issuance provenance")
	}
}

func TestGatewayWorkloadConcurrentExchangeCreatesExactlyOneCredential(t *testing.T) {
	f, _ := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	binding, sign := workloadStoreFixture(t, f)
	assertion := sign("concurrent-exchange-0001")
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := f.repo.ExchangeGatewayWorkloadToken(t.Context(), f.ws.Slug, binding.ID, assertion)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	ok, conflicts := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflicts != 7 {
		t.Fatal("concurrent assertion replay escaped unique fence", ok, conflicts)
	}
}

func TestGatewayWorkloadCredentialExpiryDeniesResolveAdmissionCacheTasksAndApproval(t *testing.T) {
	f, original := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	gatewayV3Config(t, f, false)
	binding, sign := workloadStoreFixture(t, f)
	issued, err := f.repo.ExchangeGatewayWorkloadToken(t.Context(), f.ws.Slug, binding.ID, sign("expiry-enforcement-0001"))
	if err != nil {
		t.Fatal(err)
	}
	in := workloadAuthorizeInput(original, issued.Token)
	input := taskTestInput(t, f, in)
	task, err := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	fact := taskTestFact(task, in, "expiry-task-step-0001")
	if _, err = f.repo.CheckGatewayTask(t.Context(), fact); err != nil {
		t.Fatal("live short credential cannot access task", err)
	}
	cache := GatewayCacheScope{WorkspaceSlug: f.ws.Slug, TokenHash: in.TokenHash, ApplicationID: "app", PrincipalID: "principal", ModelID: "model", CatalogDigest: in.CatalogDigest, InspectionVersion: aisecurity.InspectionVersion, InputHash: strings.Repeat("b", 64), DocumentHash: strings.Repeat("c", 64), PolicyHash: in.CatalogDigest}
	if _, err = f.repo.GetGatewayCache(t.Context(), cache); err != nil {
		t.Fatal("live short credential cannot inspect cache", err)
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE plume.ai_gateway_credentials SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, issued.Credential.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.ResolveGateway(t.Context(), GatewayResolveInput{WorkspaceSlug: f.ws.Slug, TokenHash: in.TokenHash}); !errors.Is(err, ErrForbidden) {
		t.Fatal("expired credential resolved", err)
	}
	if _, err = f.repo.AuthorizeGateway(t.Context(), in); !errors.Is(err, ErrForbidden) {
		t.Fatal("expired credential authorized provider", err)
	}
	if _, err = f.repo.GetGatewayCache(t.Context(), cache); !errors.Is(err, ErrForbidden) {
		t.Fatal("expired credential read cache", err)
	}
	if _, err = f.repo.CheckGatewayTask(t.Context(), fact); !errors.Is(err, ErrForbidden) {
		t.Fatal("expired credential checked task", err)
	}
	if _, err = f.repo.AdmitGatewayTask(t.Context(), fact); !errors.Is(err, ErrForbidden) {
		t.Fatal("expired credential admitted task step", err)
	}
	if _, err = f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input); !errors.Is(err, ErrForbidden) {
		t.Fatal("expired credential bound another task", err)
	}
	// Approval context also checks the task's persisted credential rather than
	// treating the editor's console session as authority for an expired workload.
	err = pgx.BeginTxFunc(t.Context(), f.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, e := gatewayApprovalTaskContext(t.Context(), tx, f.ws.ID, aisecurity.Request{TaskID: task.ID}, in.CatalogDigest, nil)
		return e
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatal("approval context resurrected expired credential", err)
	}
	view, err := f.repo.GatewayOverview(t.Context(), f.ws.ID, f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range view.Credentials {
		if c.ID == issued.Credential.ID && c.Enabled {
			t.Fatal("credential inventory labels expired token as active")
		}
	}
}

func TestGatewayWorkloadReplayTTLAndBindingPaginationAreBounded(t *testing.T) {
	f, _ := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	binding, sign := workloadStoreFixture(t, f)
	for i := range 505 {
		digest := fmt.Sprintf("%064x", i+1)
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO plume.ai_gateway_workload_exchanges(workspace_id,binding_id,issuer_digest,jti_digest,expires_at) VALUES($1,$2,$3,$4,clock_timestamp()-interval '1 day')`, f.ws.ID, binding.ID, strings.Repeat("a", 64), digest); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.repo.ExchangeGatewayWorkloadToken(t.Context(), f.ws.Slug, binding.ID, sign("bounded-cleanup-0001")); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM plume.ai_gateway_workload_exchanges WHERE workspace_id=$1 AND expires_at<clock_timestamp()`, f.ws.ID).Scan(&remaining); err != nil || remaining != 5 {
		t.Fatal("cleanup was not capped at 500", remaining, err)
	}
	in := binding.GatewayWorkloadInput
	in.Subject += "/another"
	if _, err := f.repo.SaveGatewayWorkload(t.Context(), f.ws.ID, f.user.ID, 0, 0, in, true); err != nil {
		t.Fatal(err)
	}
	page, err := f.repo.GatewayWorkloads(t.Context(), f.ws.ID, f.user.ID, "", 1)
	if err != nil || !page.HasMore || page.NextCursor == "" {
		t.Fatal("workload history pagination", page, err)
	}
	last, err := f.repo.GatewayWorkloads(t.Context(), f.ws.ID, f.user.ID, page.NextCursor, 1)
	if err != nil || last.HasMore || len(last.Items) != 1 || last.Items[0].ID != binding.ID {
		t.Fatal("workload last page", last, err)
	}
	other := assessmentSourceTestStore(t)
	if _, err := f.repo.GatewayWorkloads(t.Context(), other.ws.ID, other.user.ID, page.NextCursor, 1); !errors.Is(err, ErrInvalid) {
		t.Fatal("workload cursor escaped workspace", err)
	}
}
