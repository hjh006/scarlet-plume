package store

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func securityRuntimeAuditFixture(t *testing.T, app, digest string) aisecurity.AuditRecord {
	t.Helper()
	var record aisecurity.AuditRecord
	raw := fmt.Sprintf(`{"eventId":"%s","requestId":"accepted-request-1","stage":"completed","observedAt":%q,"decision":{"outcome":"allow","code":"allowed","rules":[],"catalogVersion":"ai-security-v1","catalogDigest":%q,"requestDigest":"%s","kind":"model","applicationId":%q,"principalId":"user-a","allowedDocumentIds":[],"categories":[],"requiresApproval":false,"coverage":[]},"execution":{"attempted":true,"outcome":"succeeded","httpStatus":200}}`, strings.Repeat("a", 32), time.Now().UTC().Format(time.RFC3339Nano), digest, strings.Repeat("c", 64), app)
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestAISecurityRuntimeEvidenceIndependentSourceAndHistoricalVersion(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	config := aisecurity.DefaultConfig()
	config.Applications = []aisecurity.Application{{ID: "app-a", Name: "Approved enterprise AI", Enabled: true, PrincipalIDs: []string{}, DocumentIDs: []string{}, ModelIDs: []string{}, ToolIDs: []string{}}}
	digest := aisecurity.ConfigDigest(config)
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `INSERT INTO plume.ai_security_configurations(workspace_id,revision,configuration,updated_by,published_revision,published_configuration,published_digest,published_by,published_at) VALUES($1,1,$2,$3,1,$2,$4,$3,now())`, f.ws.ID, body, f.user.ID, digest); err != nil {
		t.Fatal(err)
	}
	tokenHash := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("source/%d", f.ws.ID))))
	source, err := f.repo.CreateAISecurityRuntimeSource(ctx, f.ws.ID, f.user.ID, AISecurityRuntimeSourceInput{Name: "Customer runtime", ApplicationID: "app-a", CatalogDigest: digest}, tokenHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.CreateAISecurityRuntimeSource(ctx, f.ws.ID, f.user.ID, AISecurityRuntimeSourceInput{Name: "Unknown application", ApplicationID: "other-app", CatalogDigest: digest}, strings.Repeat("d", 64)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unpublished application accepted: %v", err)
	}
	record := securityRuntimeAuditFixture(t, "app-a", digest)
	first, err := f.repo.ReceiveAISecurityRuntimeEvent(ctx, tokenHash, record)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := f.repo.ReceiveAISecurityRuntimeEvent(ctx, tokenHash, record)
	if err != nil || !replayed.Replayed || replayed.ID != first.ID {
		t.Fatalf("idempotent receipt failed: %+v %v", replayed, err)
	}
	conflict := record
	conflict.Execution.Outcome = "failed"
	conflict.Decision.Outcome = "deny"
	if _, err = f.repo.ReceiveAISecurityRuntimeEvent(ctx, tokenHash, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting replay accepted: %v", err)
	}
	foreign := record
	foreign.Decision.ApplicationID = "app-b"
	if _, err = f.repo.ReceiveAISecurityRuntimeEvent(ctx, tokenHash, foreign); !errors.Is(err, ErrForbidden) {
		t.Fatalf("source escaped application: %v", err)
	}
	foreign = record
	foreign.Decision.CatalogDigest = strings.Repeat("e", 64)
	if _, err = f.repo.ReceiveAISecurityRuntimeEvent(ctx, tokenHash, foreign); !errors.Is(err, ErrForbidden) {
		t.Fatalf("source escaped pinned catalogue: %v", err)
	}
	view, err := f.repo.AISecurityRuntimeEvidence(ctx, f.ws.ID, f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Sources) != 1 || view.Sources[0].ReceiptCount != 1 || !view.Sources[0].Current || len(view.Events) != 1 || view.Events[0].Stage != "completed" {
		t.Fatalf("invalid execution evidence %+v", view)
	}
	if len(view.Requests) != 1 || view.Requests[0].Status != "incomplete" || view.Requests[0].RuntimeCompatibility != "unversioned" || view.Events[0].RuntimeCompatibility != "unversioned" {
		t.Fatalf("legacy standalone completion promoted to versioned full execution chain: %+v", view.Requests)
	}
	config.Applications[0].Name = "New published configuration"
	newDigest := aisecurity.ConfigDigest(config)
	body, _ = json.Marshal(config)
	if _, err = f.pool.Exec(ctx, `UPDATE plume.ai_security_configurations SET revision=2,configuration=$2,published_revision=2,published_configuration=$2,published_digest=$3 WHERE workspace_id=$1`, f.ws.ID, body, newDigest); err != nil {
		t.Fatal(err)
	}
	view, err = f.repo.AISecurityRuntimeEvidence(ctx, f.ws.ID, f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Sources[0].Current || view.Events[0].Current {
		t.Fatal("historical configuration promoted to current effect")
	}
	if err = f.repo.RevokeAISecurityRuntimeSource(ctx, f.ws.ID, f.user.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.ReceiveAISecurityRuntimeEvent(ctx, tokenHash, record); !errors.Is(err, ErrAutomationUnauthorized) {
		t.Fatalf("revoked token still accepted: %v", err)
	}
	view, err = f.repo.AISecurityRuntimeEvidence(ctx, f.ws.ID, f.user.ID)
	if err != nil || len(view.Events) != 1 || view.Sources[0].RevokedAt == nil {
		t.Fatalf("revocation destroyed historical evidence: %v", err)
	}
}

func TestAISecurityRuntimeRecordRejectsUnboundedAndFutureClaims(t *testing.T) {
	now := time.Now()
	record := securityRuntimeAuditFixture(t, "app", strings.Repeat("f", 64))
	if !validAISecurityRuntimeRecord(record, now) {
		t.Fatal("valid record rejected")
	}
	record.ObservedAt = now.Add(time.Hour)
	if validAISecurityRuntimeRecord(record, now) {
		t.Fatal("future record accepted")
	}
	record.ObservedAt = now
	record.Decision.Coverage = []string{strings.Repeat("x", 2001)}
	if validAISecurityRuntimeRecord(record, now) {
		t.Fatal("oversize metadata accepted")
	}
	record.Decision.Coverage = nil
	record.RuntimeProtocolVersion = aisecurity.RuntimeProtocolVersion
	record.InspectionVersion = aisecurity.InspectionVersion
	if !validAISecurityRuntimeRecord(record, now) {
		t.Fatal("supported profile rejected")
	}
	record.RuntimeProtocolVersion = "unrecognized-next-version"
	if !validAISecurityRuntimeRecord(record, now) || aiRuntimeCompatibility(record) != "unsupported" {
		t.Fatal("unknown profile report must remain evidence with an explicit compatibility gap")
	}
	record.InspectionVersion = "multiline\nversion"
	if validAISecurityRuntimeRecord(record, now) {
		t.Fatal("malformed version accepted")
	}
}
