package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func TestGatewayCredentialIssuingRevisionAndExplicitRevocationFacts(t *testing.T) {
	f, _ := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	binding, sign := workloadStoreFixture(t, f)
	mint := func(jti string) GatewayWorkloadToken {
		t.Helper()
		v, e := f.repo.ExchangeGatewayWorkloadToken(t.Context(), f.ws.Slug, binding.ID, sign(jti))
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	read := func(id int64) GatewayCredential {
		t.Helper()
		page, e := f.repo.GatewayCredentials(t.Context(), f.ws.ID, f.user.ID, "", "", "", 100)
		if e != nil {
			t.Fatal(e)
		}
		for _, v := range page.Items {
			if v.ID == id {
				return v.GatewayCredential
			}
		}
		t.Fatal("credential metadata missing")
		return GatewayCredential{}
	}
	expire := func(id int64) {
		t.Helper()
		if _, e := f.pool.Exec(t.Context(), `UPDATE plume.ai_gateway_credentials SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, id); e != nil {
			t.Fatal(e)
		}
	}
	first := mint("provenance-first-0001")
	if first.Credential.WorkloadBindingRevision == nil || *first.Credential.WorkloadBindingRevision != 1 || first.Credential.RevokedAt != nil || first.Credential.RevocationReason != "" {
		t.Fatal("issuance provenance not stamped from locked binding")
	}
	if _, e := f.pool.Exec(t.Context(), `UPDATE plume.ai_gateway_credentials SET workload_binding_revision=2 WHERE id=$1`, first.Credential.ID); e == nil {
		t.Fatal("issuing revision can be rewritten")
	}
	expire(first.Credential.ID)
	second := mint("provenance-second-0002")
	var storedEnabled bool
	if e := f.pool.QueryRow(t.Context(), `SELECT enabled FROM plume.ai_gateway_credentials WHERE id=$1`, first.Credential.ID).Scan(&storedEnabled); e != nil || storedEnabled {
		t.Fatal("expiry maintenance did not disable expired credential", e)
	}
	before := read(first.Credential.ID)
	if before.RevokedAt != nil || before.RevocationReason != "" || before.WorkloadBindingRevision == nil || *before.WorkloadBindingRevision != 1 {
		t.Fatal("natural expiry fabricated a manual revocation", before)
	}
	if e := f.repo.RevokeGatewayCredential(t.Context(), f.ws.ID, f.user.ID, first.Credential.ID); e != nil {
		t.Fatal(e)
	}
	manual := read(first.Credential.ID)
	if manual.RevokedAt == nil || manual.RevocationReason != "manual" {
		t.Fatal("explicit revocation of already-expired credential lost", manual)
	}
	if e := f.repo.RevokeGatewayCredential(t.Context(), f.ws.ID, f.user.ID, first.Credential.ID); e != nil {
		t.Fatal(e)
	}
	if again := read(first.Credential.ID); again.RevokedAt == nil || !again.RevokedAt.Equal(*manual.RevokedAt) {
		t.Fatal("repeated revocation rewrote original fact")
	}
	expire(second.Credential.ID)
	_ = mint("provenance-third-0003")
	updated, e := f.repo.SaveGatewayWorkload(t.Context(), f.ws.ID, f.user.ID, binding.ID, 1, binding.GatewayWorkloadInput, true)
	if e != nil || updated.Revision != 2 {
		t.Fatal(e)
	}
	changed := read(second.Credential.ID)
	if changed.RevokedAt == nil || changed.RevocationReason != "binding_changed" || changed.WorkloadBindingRevision == nil || *changed.WorkloadBindingRevision != 1 {
		t.Fatal("binding update missed disabled expired token or rewrote mint revision", changed)
	}
	if kept := read(first.Credential.ID); kept.RevocationReason != "manual" || !kept.RevokedAt.Equal(*manual.RevokedAt) {
		t.Fatal("binding update replaced an earlier explicit cause")
	}
	fourth := mint("provenance-fourth-0004")
	if fourth.Credential.WorkloadBindingRevision == nil || *fourth.Credential.WorkloadBindingRevision != 2 {
		t.Fatal("new binding revision not stamped")
	}
	expire(fourth.Credential.ID)
	_ = mint("provenance-fifth-0005")
	if _, e = f.repo.RevokeGatewayWorkload(t.Context(), f.ws.ID, f.user.ID, binding.ID, 2); e != nil {
		t.Fatal(e)
	}
	revoked := read(fourth.Credential.ID)
	if revoked.RevocationReason != "binding_revoked" || revoked.RevokedAt == nil {
		t.Fatal("binding revocation missed already-disabled credential", revoked)
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE plume.ai_gateway_credentials SET revoked_at=NULL,revocation_reason=NULL WHERE id=$1`, fourth.Credential.ID); e == nil {
		t.Fatal("recorded revocation can be erased")
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE plume.ai_gateway_credentials SET enabled=true WHERE id=$1`, fourth.Credential.ID); e == nil {
		t.Fatal("revoked credential can be re-enabled")
	}
}

func taskCredentialPublishedFixture(t *testing.T, f assessmentSourceFixture, expiry *time.Time) string {
	t.Helper()
	c := aisecurity.DefaultConfig()
	c.Policy.AllowedModelRegions = []string{"CN"}
	c.Applications = []aisecurity.Application{{ID: "app", Enabled: true, PrincipalIDs: []string{"principal"}, ModelIDs: []string{"model"}, ToolIDs: []string{"tool"}, DocumentIDs: []string{"doc"}}}
	c.Principals = []aisecurity.Principal{{ID: "principal", Enabled: true, ApplicationIDs: []string{"app"}, AuthorizationExpiresAt: expiry}}
	c.Documents = []aisecurity.Document{{ID: "doc", Version: "v1", ACLVersion: "v1", KnowledgeBaseID: "knowledge", Enabled: true, AllowedPrincipalIDs: []string{"principal"}}}
	c.Models = []aisecurity.Model{{ID: "model", EndpointRef: "model-endpoint", Region: "CN", Enabled: true, AllowedApplicationIDs: []string{"app"}}}
	c.Tools = []aisecurity.Tool{{ID: "tool", EndpointRef: "tool-endpoint", Enabled: true, AllowedApplicationIDs: []string{"app"}, AllowedPrincipalIDs: []string{"principal"}, Actions: []string{"close"}, Targets: []string{"ticket-17"}}}
	if e := aisecurity.ValidateConfig(c); e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(c)
	if e != nil {
		t.Fatal(e)
	}
	digest := aisecurity.ConfigDigest(c)
	if _, e = f.pool.Exec(t.Context(), `INSERT INTO plume.ai_security_configurations(workspace_id,revision,configuration,updated_by,published_revision,published_configuration,published_digest,published_by,published_at)VALUES($1,1,$2,$3,1,$2,$4,$3,clock_timestamp())`, f.ws.ID, raw, f.user.ID, digest); e != nil {
		t.Fatal(e)
	}
	cfg := gatewayTestConfig(1_000_000, 10000, 1_000_000)
	cfg.CatalogDigest = digest
	cfg.Providers[0].Region = "CN"
	if _, e = f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.user.ID, 1, cfg); e != nil {
		t.Fatal(e)
	}
	return digest
}

func TestGatewayTaskCredentialStatusAncestorsAreReadOnlyAndDoNotRenew(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	in.CatalogDigest = taskCredentialPublishedFixture(t, f, nil)
	binding, sign := workloadStoreFixture(t, f)
	issued, e := f.repo.ExchangeGatewayWorkloadToken(t.Context(), f.ws.Slug, binding.ID, sign("task-chain-provenance-0001"))
	if e != nil {
		t.Fatal(e)
	}
	rootInput := taskTestInput(t, f, in)
	rootInput.CredentialID = issued.Credential.ID
	root, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, rootInput)
	if e != nil {
		t.Fatal(e)
	}
	childInput := taskTestInput(t, f, in)
	childInput.ParentTaskID = root.ID
	childInput.MaxDelegationDepth = 2
	childInput.ExpiresAt = root.ExpiresAt.Add(-time.Minute)
	child, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, childInput)
	if e != nil {
		t.Fatal(e)
	}
	viewer := gatewayTestMember(t, f, "viewer")
	before, e := f.repo.GatewayTaskDetail(t.Context(), f.ws.ID, viewer, child.ID, 100)
	if e != nil {
		t.Fatal(e)
	}
	beforeBytes, _ := json.Marshal(before)
	diagnostic, e := f.repo.GatewayTaskCredentialStatus(t.Context(), f.ws.ID, viewer, child.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !diagnostic.RenewalSupported || diagnostic.CredentialChainStatus != "current" || len(diagnostic.AncestorPath) != 2 || diagnostic.AncestorPath[0].TaskID != root.ID || diagnostic.AncestorPath[1].TaskID != child.ID || diagnostic.AncestorPath[0].BindingStatus != "current" {
		t.Fatal("ancestor diagnostics not bounded/trusted", diagnostic)
	}
	after, e := f.repo.GatewayTaskDetail(t.Context(), f.ws.ID, viewer, child.ID, 100)
	if e != nil {
		t.Fatal(e)
	}
	afterBytes, _ := json.Marshal(after)
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatal("read-only diagnosis mutated task scope, status, steps or budget")
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE plume.ai_gateway_credentials SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, issued.Credential.ID); e != nil {
		t.Fatal(e)
	}
	diagnostic, e = f.repo.GatewayTaskCredentialStatus(t.Context(), f.ws.ID, viewer, child.ID)
	if e != nil {
		t.Fatal(e)
	}
	if diagnostic.CredentialChainStatus != "blocked" || diagnostic.AncestorPath[0].CredentialState != "expired" || diagnostic.AncestorPath[1].CredentialState != "active" || diagnostic.AncestorPath[0].Credential.RevokedAt != nil {
		t.Fatal("expired ancestor obscured by live child or false manual cause", diagnostic)
	}
	if _, e = f.repo.CheckGatewayTask(t.Context(), taskTestFact(t, f, child, in, "diagnostic-expired-ancestor-0001")); !errors.Is(e, ErrForbidden) {
		t.Fatal("diagnostic no longer matches actual expired-chain rejection", e)
	}
	fresh, e := f.repo.ExchangeGatewayWorkloadToken(t.Context(), f.ws.Slug, binding.ID, sign("task-chain-provenance-0002"))
	if e != nil {
		t.Fatal(e)
	}
	diagnostic, e = f.repo.GatewayTaskCredentialStatus(t.Context(), f.ws.ID, viewer, child.ID)
	if e != nil {
		t.Fatal(e)
	}
	if diagnostic.CredentialChainStatus != "blocked" || diagnostic.AncestorPath[0].Credential.ID != issued.Credential.ID || diagnostic.AncestorPath[0].ScopeDigest != root.ScopeDigest || fresh.Credential.ID == issued.Credential.ID {
		t.Fatal("new token silently rebound task authority")
	}
	if e = f.repo.RevokeGatewayCredential(t.Context(), f.ws.ID, f.user.ID, issued.Credential.ID); e != nil {
		t.Fatal(e)
	}
	diagnostic, e = f.repo.GatewayTaskCredentialStatus(t.Context(), f.ws.ID, viewer, child.ID)
	if e != nil || diagnostic.AncestorPath[0].CredentialState != "revoked" || diagnostic.AncestorPath[0].Credential.RevocationReason != "manual" {
		t.Fatal("explicit ancestor revocation not explained", e)
	}
	foreign := assessmentSourceTestStore(t)
	if _, e = f.repo.GatewayTaskCredentialStatus(t.Context(), f.ws.ID, foreign.user.ID, child.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("non-member read task credential diagnostics", e)
	}
	if _, e = f.repo.GatewayTaskCredentialStatus(t.Context(), foreign.ws.ID, foreign.user.ID, child.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("task escaped workspace", e)
	}
	projection, _ := json.Marshal(diagnostic)
	if bytes.Contains(projection, []byte(issued.Token)) || bytes.Contains(projection, []byte(in.TokenHash)) || bytes.Contains(projection, []byte("token_hash")) {
		t.Fatal("diagnostics recovered a credential or hash")
	}
}

func TestGatewayTaskCredentialStatusUnknownPublicationLegacyAndPrincipalExpiry(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	input := taskTestInput(t, f, in)
	task, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	unknown, e := f.repo.GatewayTaskCredentialStatus(t.Context(), f.ws.ID, f.user.ID, task.ID)
	if e != nil || unknown.CredentialChainStatus != "unknown" || unknown.Authorization.PrincipalStatus != "unknown" {
		t.Fatal("missing publication became a false authorization", e, unknown)
	}
	expiry := time.Now().Add(-time.Hour)
	in.CatalogDigest = taskCredentialPublishedFixture(t, f, &expiry)
	input = taskTestInput(t, f, in)
	task, e = f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	diagnostic, e := f.repo.GatewayTaskCredentialStatus(t.Context(), f.ws.ID, f.user.ID, task.ID)
	if e != nil || diagnostic.AncestorPath[0].CredentialState != "active" || diagnostic.CredentialChainStatus != "blocked" || diagnostic.Authorization.PrincipalStatus != "blocked" {
		t.Fatal("active key became task authorization despite expired principal", e, diagnostic)
	}
	if !slices.ContainsFunc(diagnostic.Blockers, func(i GatewayTaskCredentialIssue) bool { return i.Code == "principal_authorization_expired" }) {
		t.Fatal("principal expiry reason missing")
	}
	binding, _ := workloadStoreFixture(t, f)
	var legacyID int64
	if e = f.pool.QueryRow(t.Context(), `INSERT INTO plume.ai_gateway_credentials(workspace_id,application_id,name,prefix,token_hash,workload_binding_id,expires_at)VALUES($1,'app','legacy provenance fixture','sgw_legacy',$2,$3,clock_timestamp()+interval '2 minutes')RETURNING id`, f.ws.ID, gatewayDigest(fmt.Sprint(f.ws.ID, "legacy")), binding.ID).Scan(&legacyID); e != nil {
		t.Fatal(e)
	}
	input.CredentialID = legacyID
	legacyTask, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	legacy, e := f.repo.GatewayTaskCredentialStatus(t.Context(), f.ws.ID, f.user.ID, legacyTask.ID)
	if e != nil || legacy.AncestorPath[0].Credential.WorkloadBindingRevision != nil || legacy.AncestorPath[0].BindingStatus != "unknown" || legacy.AncestorPath[0].CredentialState != "active" {
		t.Fatal("legacy issuing revision was invented or active key denied", e, legacy)
	}
	if !slices.ContainsFunc(legacy.RenewalBlockers, func(i GatewayTaskCredentialIssue) bool {
		return i.TaskID == legacyTask.ID && i.Code == "mint_revision_unknown"
	}) {
		t.Fatal("unknown mint proof lost")
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE plume.ai_gateway_credentials SET enabled=false WHERE id=$1`, legacyID); e != nil {
		t.Fatal(e)
	}
	legacy, e = f.repo.GatewayTaskCredentialStatus(t.Context(), f.ws.ID, f.user.ID, legacyTask.ID)
	if e != nil || legacy.AncestorPath[0].CredentialState != "disabled_unknown" || legacy.AncestorPath[0].Credential.RevokedAt != nil {
		t.Fatal("unknown historical disabled cause was invented", e)
	}
}
