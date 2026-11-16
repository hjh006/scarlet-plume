package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/jackc/pgx/v5/pgxpool"
)

func gatewayTestConfig(budget, rpm, tpm int64) GatewayConfig {
	return GatewayConfig{
		Enabled: true, CatalogDigest: strings.Repeat("a", 64),
		Applications: []GatewayApplication{{ID: "app", Name: "App", Enabled: true, PrincipalID: "principal", AllowRag: true, ModelIDs: []string{"model"}, ToolIDs: []string{"tool"}, BudgetMicros: budget, RequestsPerMinute: rpm, TokensPerMinute: tpm}},
		Providers:    []GatewayProvider{{ID: "provider", Name: "Fixed upstream", Provider: "openai", ModelID: "model", UpstreamModel: "fixed-model", EndpointRef: "env:SCARLET_GATEWAY_BIFROST", SecretRef: "env:SCARLET_GATEWAY_KEY", Enabled: true, Region: "us-east-1", ResponseFormat: "openai_chat", InputMicrosPerMillion: 1_000_000, OutputMicrosPerMillion: 1_000_000}},
	}
}
func gatewayTestSetup(t *testing.T, budget, rpm, tpm int64) (assessmentSourceFixture, GatewayAuthorizeInput) {
	t.Helper()
	f := assessmentSourceTestStore(t)
	if _, e := f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.user.ID, 0, gatewayTestConfig(budget, rpm, tpm)); e != nil {
		t.Fatal(e)
	}
	_, token, e := f.repo.CreateGatewayCredential(t.Context(), f.ws.ID, f.user.ID, "app", "test credential")
	if e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256([]byte(token))
	return f, GatewayAuthorizeInput{WorkspaceSlug: f.ws.Slug, TokenHash: hex.EncodeToString(hash[:]), ApplicationID: "app", PrincipalID: "principal", Kind: "model", RequestID: "request-0000000001", ModelID: "model", RequestDigest: strings.Repeat("b", 64), CatalogDigest: strings.Repeat("a", 64), InputBytes: 2, MaxOutputTokens: 8}
}
func gatewayTestSettlement(in GatewayAuthorizeInput, a GatewayAuthorization, attempted bool, outcome string) GatewaySettlement {
	return GatewaySettlement{
		WorkspaceSlug: in.WorkspaceSlug, ReservationID: a.ReservationID, RequestDigest: in.RequestDigest,
		Audit: &aisecurity.AuditRecord{AttemptID: "attempt-000000001", EventID: "event-" + a.ReservationID, RequestID: in.RequestID, Stage: "completed", ObservedAt: time.Now().UTC(), Decision: aisecurity.Decision{Outcome: "allow", RequestDigest: in.RequestDigest, CatalogDigest: in.CatalogDigest, ApplicationID: in.ApplicationID, PrincipalID: in.PrincipalID, Kind: in.Kind}, Execution: aisecurity.Execution{Attempted: attempted, Outcome: outcome}},
	}
}
func gatewayAssertUsage(t *testing.T, f assessmentSourceFixture, reserved, spent int64) {
	t.Helper()
	out, e := f.repo.GatewayOverview(t.Context(), f.ws.ID, f.user.ID)
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Usage) != 1 || out.Usage[0].ReservedMicros != reserved || out.Usage[0].SpentMicros != spent {
		t.Fatalf("usage=%+v want reserved=%d spent=%d", out.Usage, reserved, spent)
	}
}

func TestGatewayConcurrentCrossNodeBudgetAndSettlement(t *testing.T) {
	f, in := gatewayTestSetup(t, 100, 1000, 1_000_000)
	second, e := pgxpool.New(t.Context(), os.Getenv("PLUME_STORE_TEST_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	nodes := []*Store{f.repo, New(second)}
	type admission struct {
		in  GatewayAuthorizeInput
		out GatewayAuthorization
	}
	admitted := make(chan admission, 32)
	errs := make(chan error, 32)
	var wg sync.WaitGroup
	for n := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := in
			req.RequestID = fmt.Sprintf("request-000000%04d", n)
			req.RequestDigest = gatewayDigest(req.RequestID)
			out, e := nodes[n%2].AuthorizeGateway(t.Context(), req)
			if e == nil {
				admitted <- admission{req, out}
			} else if !errors.Is(e, ErrGatewayBudget) {
				errs <- e
			}
		}()
	}
	wg.Wait()
	close(admitted)
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	var calls []admission
	for a := range admitted {
		calls = append(calls, a)
	}
	if len(calls) != 10 {
		t.Fatalf("admitted %d expected exactly 10", len(calls))
	}
	gatewayAssertUsage(t, f, 100, 0)
	for _, a := range calls {
		settlement := gatewayTestSettlement(a.in, a.out, true, "succeeded")
		for n := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if e := nodes[n].SettleGateway(t.Context(), settlement); e != nil {
					t.Error(e)
				}
			}()
		}
	}
	wg.Wait()
	gatewayAssertUsage(t, f, 0, 100)
	page, e := f.repo.GatewayAudit(t.Context(), f.ws.ID, f.user.ID, 0, 500)
	if e != nil || !page.Valid || len(page.Items) != 22 {
		t.Fatalf("audit events=%d valid=%v err=%v", len(page.Items), page.Valid, e)
	}
}
func TestGatewayReplayScopeRevocationAndTenantIsolation(t *testing.T) {
	f, in := gatewayTestSetup(t, 100, 10, 100)
	identity, e := f.repo.ResolveGateway(t.Context(), GatewayResolveInput{in.WorkspaceSlug, in.TokenHash})
	if e != nil || identity.ApplicationID != "app" || identity.PrincipalID != "principal" || identity.ConfigRevision != 1 || identity.CatalogDigest != in.CatalogDigest || len(identity.ToolIDs) != 1 || identity.ToolIDs[0] != "tool" {
		t.Fatalf("identity=%+v err=%v", identity, e)
	}
	first, e := f.repo.AuthorizeGateway(t.Context(), in)
	if e != nil || first.ConfigRevision != 1 {
		t.Fatalf("grant revision=%d err=%v", first.ConfigRevision, e)
	}
	replay, e := f.repo.AuthorizeGateway(t.Context(), in)
	if e != nil || !replay.Replayed || replay.ReservationID != first.ReservationID {
		t.Fatalf("replay=%+v err=%v", replay, e)
	}
	gatewayAssertUsage(t, f, 10, 0)
	cfg := gatewayTestConfig(100, 10, 100)
	if _, e = f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.user.ID, 1, cfg); e != nil {
		t.Fatal(e)
	}
	updated, e := f.repo.ResolveGateway(t.Context(), GatewayResolveInput{in.WorkspaceSlug, in.TokenHash})
	if e != nil || updated.ConfigRevision != 2 || updated.PrincipalID != identity.PrincipalID {
		t.Fatalf("live config revision %+v %v", updated, e)
	}
	changed := in
	changed.InputBytes++
	if _, e = f.repo.AuthorizeGateway(t.Context(), changed); !errors.Is(e, ErrConflict) {
		t.Fatalf("changed replay: %v", e)
	}
	for _, mutate := range []func(*GatewayAuthorizeInput){func(v *GatewayAuthorizeInput) { v.PrincipalID = "other" }, func(v *GatewayAuthorizeInput) { v.ApplicationID = "other" }, func(v *GatewayAuthorizeInput) { v.ModelID = "other" }, func(v *GatewayAuthorizeInput) { v.CatalogDigest = strings.Repeat("c", 64) }, func(v *GatewayAuthorizeInput) { v.Kind = "tool"; v.ToolID = "forbidden"; v.Action = "run" }} {
		v := in
		v.RequestID += "-denied"
		mutate(&v)
		if _, e = f.repo.AuthorizeGateway(t.Context(), v); !errors.Is(e, ErrForbidden) {
			t.Fatalf("scope failure %v", e)
		}
	}
	other := assessmentSourceTestStore(t)
	cross := in
	cross.WorkspaceSlug = other.ws.Slug
	if _, e = f.repo.AuthorizeGateway(t.Context(), cross); !errors.Is(e, ErrForbidden) {
		t.Fatalf("cross tenant authorize %v", e)
	}
	st := gatewayTestSettlement(in, first, true, "succeeded")
	st.WorkspaceSlug = other.ws.Slug
	if e = f.repo.SettleGateway(t.Context(), st); !errors.Is(e, ErrNotFound) {
		t.Fatalf("cross tenant settle %v", e)
	}
	if e = f.repo.RevokeGatewayCredential(t.Context(), f.ws.ID, f.user.ID, identity.CredentialID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.ResolveGateway(t.Context(), GatewayResolveInput{in.WorkspaceSlug, in.TokenHash}); !errors.Is(e, ErrForbidden) {
		t.Fatalf("revoked resolve %v", e)
	}
	in.RequestID += "-revoked"
	if _, e = f.repo.AuthorizeGateway(t.Context(), in); !errors.Is(e, ErrForbidden) {
		t.Fatalf("revoked authorize %v", e)
	}
}
func TestGatewayUnknownRetainsReservationAndRejectsFalseSettlement(t *testing.T) {
	f, in := gatewayTestSetup(t, 10, 10, 100)
	a, e := f.repo.AuthorizeGateway(t.Context(), in)
	if e != nil {
		t.Fatal(e)
	}
	settlement := gatewayTestSettlement(in, a, true, "unknown")
	if e = f.repo.SettleGateway(t.Context(), settlement); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.SettleGateway(t.Context(), settlement); e != nil {
		t.Fatal(e)
	}
	gatewayAssertUsage(t, f, 10, 0)
	reservations, e := f.repo.GatewayReservations(t.Context(), f.ws.ID, f.user.ID, "", 100)
	if e != nil || len(reservations) != 1 || reservations[0]["state"] != "unknown" {
		t.Fatalf("pending %v %v", reservations, e)
	}
	fake := gatewayTestSettlement(in, a, false, "not_attempted")
	fake.Audit.EventID += "fake"
	if e = f.repo.SettleGateway(t.Context(), fake); !errors.Is(e, ErrConflict) {
		t.Fatalf("unknown refunded: %v", e)
	}
	cfg := gatewayTestConfig(9, 10, 100)
	if _, e = f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.user.ID, 1, cfg); !errors.Is(e, ErrGatewayBudget) {
		t.Fatalf("lower budget bypass %v", e)
	}
	next := in
	next.RequestID += "-next"
	if _, e = f.repo.AuthorizeGateway(t.Context(), next); !errors.Is(e, ErrGatewayBudget) {
		t.Fatalf("unknown frees budget %v", e)
	}
	final := gatewayTestSettlement(in, a, true, "succeeded")
	final.Audit.EventID += "reconcile"
	if e = f.repo.SettleGateway(t.Context(), final); e != nil {
		t.Fatal(e)
	}
	gatewayAssertUsage(t, f, 0, 10)
	changed := final
	copyAudit := *final.Audit
	changed.Audit = &copyAudit
	changed.Audit.EventID += "second"
	if e = f.repo.SettleGateway(t.Context(), changed); !errors.Is(e, ErrConflict) {
		t.Fatalf("double debit %v", e)
	}
}
func TestGatewayRatesReleaseOnlyWithFinalEvidence(t *testing.T) {
	f, in := gatewayTestSetup(t, 100, 100, 10)
	a, e := f.repo.AuthorizeGateway(t.Context(), in)
	if e != nil {
		t.Fatal(e)
	}
	next := in
	next.RequestID += "next"
	if _, e = f.repo.AuthorizeGateway(t.Context(), next); !errors.Is(e, ErrGatewayRate) {
		t.Fatalf("token limit %v", e)
	}
	for _, contradiction := range []GatewaySettlement{gatewayTestSettlement(in, a, false, "succeeded"), gatewayTestSettlement(in, a, true, "not_attempted")} {
		if e = f.repo.SettleGateway(t.Context(), contradiction); !errors.Is(e, ErrInvalid) {
			t.Fatalf("contradictory execution evidence accepted %v", e)
		}
	}
	for _, stage := range []string{"prepared", "decision"} {
		v := gatewayTestSettlement(in, a, false, "not_attempted")
		v.Audit.Stage = stage
		if e = f.repo.SettleGateway(t.Context(), v); !errors.Is(e, ErrInvalid) {
			t.Fatalf("nonfinal %s accepted: %v", stage, e)
		}
	}
	st := gatewayTestSettlement(in, a, false, "not_attempted")
	if e = f.repo.SettleGateway(t.Context(), st); e != nil {
		t.Fatal(e)
	}
	gatewayAssertUsage(t, f, 0, 0)
	if _, e = f.repo.AuthorizeGateway(t.Context(), next); e != nil {
		t.Fatalf("released token capacity %v", e)
	}
	f2, in2 := gatewayTestSetup(t, 100, 1, 100)
	a2, e := f2.repo.AuthorizeGateway(t.Context(), in2)
	if e != nil {
		t.Fatal(e)
	}
	if e = f2.repo.SettleGateway(t.Context(), gatewayTestSettlement(in2, a2, false, "not_attempted")); e != nil {
		t.Fatal(e)
	}
	in2.RequestID += "next"
	if _, e = f2.repo.AuthorizeGateway(t.Context(), in2); !errors.Is(e, ErrGatewayRate) {
		t.Fatalf("RPM refund bypass %v", e)
	}
}
func TestGatewayRAGAndToolStillGovernedWithoutModelCharge(t *testing.T) {
	f, in := gatewayTestSetup(t, 0, 10, 100)
	in.Kind = "tool"
	in.ToolID = "tool"
	in.Action = "run"
	in.ModelID = ""
	in.MaxOutputTokens = 0
	a, e := f.repo.AuthorizeGateway(t.Context(), in)
	if e != nil || a.MaxCostMicros != 0 {
		t.Fatalf("tool admission %+v %v", a, e)
	}
	if e = f.repo.SettleGateway(t.Context(), gatewayTestSettlement(in, a, true, "succeeded")); e != nil {
		t.Fatal(e)
	}
	in.Kind = "rag"
	in.ToolID = ""
	in.Action = ""
	in.RequestID += "rag"
	if _, e = f.repo.AuthorizeGateway(t.Context(), in); e != nil {
		t.Fatal(e)
	}
	cfg := gatewayTestConfig(0, 10, 100)
	cfg.Applications[0].AllowRag = false
	if _, e = f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.user.ID, 1, cfg); e != nil {
		t.Fatal(e)
	}
	in.RequestID += "denied"
	if _, e = f.repo.AuthorizeGateway(t.Context(), in); !errors.Is(e, ErrForbidden) {
		t.Fatalf("RAG scope %v", e)
	}
	gatewayAssertUsage(t, f, 0, 0)
}
func gatewayTestMember(t *testing.T, f assessmentSourceFixture, role string) int64 {
	t.Helper()
	var id int64
	name := fmt.Sprintf("gateway-%s-%d", role, time.Now().UnixNano())
	if e := f.pool.QueryRow(t.Context(), `INSERT INTO plume.users(username,password_hash) VALUES($1,'unused') RETURNING id`, name).Scan(&id); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(t.Context(), `INSERT INTO plume.memberships(workspace_id,user_id,role) VALUES($1,$2,$3)`, f.ws.ID, id, role); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM plume.users WHERE id=$1`, id) })
	return id
}
func TestGatewayRBACAuditPaginationAndNoCredentialRecovery(t *testing.T) {
	f, _ := gatewayTestSetup(t, 100, 10, 100)
	viewer := gatewayTestMember(t, f, "viewer")
	operator := gatewayTestMember(t, f, "operator")
	if _, e := f.repo.GatewayOverview(t.Context(), f.ws.ID, viewer); e != nil {
		t.Fatal(e)
	}
	cfg := gatewayTestConfig(100, 10, 100)
	for _, actor := range []int64{viewer, operator} {
		if _, e := f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, actor, 1, cfg); !errors.Is(e, ErrForbidden) {
			t.Fatalf("role %d config %v", actor, e)
		}
	}
	if _, _, e := f.repo.CreateGatewayCredential(t.Context(), f.ws.ID, viewer, "app", "denied"); !errors.Is(e, ErrForbidden) {
		t.Fatalf("viewer credential %v", e)
	}
	cred, token, e := f.repo.CreateGatewayCredential(t.Context(), f.ws.ID, operator, "app", "operator credential")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.repo.RevokeGatewayCredential(t.Context(), f.ws.ID, viewer, cred.ID); !errors.Is(e, ErrForbidden) {
		t.Fatalf("viewer revoke %v", e)
	}
	overview, e := f.repo.GatewayOverview(t.Context(), f.ws.ID, viewer)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(overview)
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "tokenHash") {
		t.Fatal("credential recoverable from overview")
	}
	page, e := f.repo.GatewayAudit(t.Context(), f.ws.ID, viewer, 0, 1)
	if e != nil || !page.Valid || len(page.Items) != 1 || page.NextCursor != 1 {
		t.Fatalf("first page %+v %v", page, e)
	}
	next, e := f.repo.GatewayAudit(t.Context(), f.ws.ID, viewer, page.NextCursor, 500)
	if e != nil || !next.Valid || next.PreviousHash != page.Items[0].Hash || next.NextCursor != next.HeadSequence {
		t.Fatalf("next page %+v %v", next, e)
	}
	raw, _ = json.Marshal(next)
	if strings.Contains(string(raw), token) {
		t.Fatal("token leaked into audit")
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE plume.ai_gateway_audit SET action='tampered' WHERE workspace_id=$1 AND sequence=1`, f.ws.ID); e == nil {
		t.Fatal("audit UPDATE allowed")
	}
	if _, e = f.pool.Exec(t.Context(), `DELETE FROM plume.ai_gateway_audit WHERE workspace_id=$1 AND sequence=1`, f.ws.ID); e == nil {
		t.Fatal("audit DELETE allowed")
	}
	// Simulate an offline privileged truncation to verify the exported chain detects a missing tail.
	if _, e = f.pool.Exec(t.Context(), `ALTER TABLE plume.ai_gateway_audit DISABLE TRIGGER ai_gateway_audit_immutable`); e != nil {
		t.Fatal(e)
	}
	defer func() {
		_, _ = f.pool.Exec(context.Background(), `ALTER TABLE plume.ai_gateway_audit ENABLE TRIGGER ai_gateway_audit_immutable`)
	}()
	if _, e = f.pool.Exec(t.Context(), `DELETE FROM plume.ai_gateway_audit WHERE workspace_id=$1 AND sequence=$2`, f.ws.ID, next.HeadSequence); e != nil {
		t.Fatal(e)
	}
	broken, e := f.repo.GatewayAudit(t.Context(), f.ws.ID, viewer, 0, 500)
	if e != nil || broken.Valid {
		t.Fatalf("truncated chain accepted %+v %v", broken, e)
	}
}
func TestGatewayConfigAndOverflowValidation(t *testing.T) {
	c := gatewayTestConfig(100, 10, 100)
	if e := ValidateGatewayConfig(c); e != nil {
		t.Fatal(e)
	}
	for _, invalid := range []string{"app/path", strings.Repeat("x", 81)} {
		candidate := gatewayTestConfig(100, 10, 100)
		candidate.Applications[0].ID = invalid
		if e := ValidateGatewayConfig(candidate); !errors.Is(e, ErrInvalid) {
			t.Fatalf("unsupported runtime ID accepted %q", invalid)
		}
	}
	bad := c
	bad.CatalogDigest = ""
	if e := ValidateGatewayConfig(bad); !errors.Is(e, ErrInvalid) {
		t.Fatal("missing catalog binding accepted")
	}
	f, in := gatewayTestSetup(t, 100, 10, 100)
	in.InputBytes = 1 << 62
	in.MaxOutputTokens = 1 << 62
	if _, e := f.repo.AuthorizeGateway(t.Context(), in); !errors.Is(e, ErrInvalid) {
		t.Fatalf("overflow %v", e)
	}
}
