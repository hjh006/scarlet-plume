package store

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func runtimeOperationsFixture(t *testing.T) (assessmentSourceFixture, AISecurityRuntimeSource, string, string) {
	t.Helper()
	f := assessmentSourceTestStore(t)
	c := aisecurity.DefaultConfig()
	c.Applications = []aisecurity.Application{{ID: "app-a", Enabled: true}}
	digest := aisecurity.ConfigDigest(c)
	body, _ := json.Marshal(c)
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO plume.ai_security_configurations(workspace_id,revision,configuration,updated_by,published_revision,published_configuration,published_digest,published_by,published_at) VALUES($1,1,$2,$3,1,$2,$4,$3,now())`, f.ws.ID, body, f.user.ID, digest); err != nil {
		t.Fatal(err)
	}
	token := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("source/%d", f.ws.ID))))
	source, err := f.repo.CreateAISecurityRuntimeSource(t.Context(), f.ws.ID, f.user.ID, AISecurityRuntimeSourceInput{Name: "Synthetic runtime", ApplicationID: "app-a", CatalogDigest: digest}, token)
	if err != nil || source.TokenVersion != 1 {
		t.Fatal(source, err)
	}
	return f, source, token, digest
}

func TestRuntimeReceiptKeysetPagesFiltersAndRotationPreserveHistory(t *testing.T) {
	f, source, token, digest := runtimeOperationsFixture(t)
	ctx := t.Context()
	for i := 1; i <= 7; i++ {
		r := securityRuntimeAuditFixture(t, "app-a", digest)
		r.EventID = fmt.Sprintf("%032x", i)
		if i%2 == 0 {
			r.Decision.Kind = "rag"
		}
		if _, err := f.repo.ReceiveAISecurityRuntimeEvent(ctx, token, r); err != nil {
			t.Fatal(err)
		}
	}
	q := AISecurityRuntimeEvidenceQuery{Limit: 2, SourceID: source.ID, Kind: "model", Outcome: "allow"}
	first, err := f.repo.AISecurityRuntimeEvidencePage(ctx, f.ws.ID, f.user.ID, q)
	if err != nil || len(first.Events) != 2 || !first.HasMore || first.NextCursor == "" || first.Events[0].ID <= first.Events[1].ID {
		t.Fatal("first page", first, err)
	}
	q.Before = first.NextCursor
	second, err := f.repo.AISecurityRuntimeEvidencePage(ctx, f.ws.ID, f.user.ID, q)
	if err != nil || len(second.Events) != 2 || second.HasMore || second.NextCursor != "" || second.Events[0].ID >= first.Events[1].ID {
		t.Fatal("second page", second, err)
	}
	q.Kind = "rag"
	if _, err = f.repo.AISecurityRuntimeEvidencePage(ctx, f.ws.ID, f.user.ID, q); !errors.Is(err, ErrInvalid) {
		t.Fatal("filter cursor escaped", err)
	}
	other := assessmentSourceTestStore(t)
	q.Kind = "model"
	if _, err = f.repo.AISecurityRuntimeEvidencePage(ctx, other.ws.ID, other.user.ID, q); !errors.Is(err, ErrInvalid) {
		t.Fatal("workspace cursor escaped", err)
	}
	viewer := gatewayTestMember(t, f, "viewer")
	if _, err = f.repo.RotateAISecurityRuntimeSource(ctx, f.ws.ID, viewer, source.ID, 1, strings.Repeat("e", 64)); !errors.Is(err, ErrForbidden) {
		t.Fatal("viewer rotated credentials", err)
	}
	rotated, err := f.repo.RotateAISecurityRuntimeSource(ctx, f.ws.ID, f.user.ID, source.ID, 1, strings.Repeat("e", 64))
	if err != nil || rotated.TokenVersion != 2 || rotated.ID != source.ID || rotated.ReceiptCount != 7 {
		t.Fatal("rotation", rotated, err)
	}
	if _, err = f.repo.RotateAISecurityRuntimeSource(ctx, f.ws.ID, f.user.ID, source.ID, 1, strings.Repeat("f", 64)); !errors.Is(err, ErrConflict) {
		t.Fatal("stale CAS accepted", err)
	}
	r := securityRuntimeAuditFixture(t, "app-a", digest)
	if _, err = f.repo.ReceiveAISecurityRuntimeEvent(ctx, token, r); !errors.Is(err, ErrAutomationUnauthorized) {
		t.Fatal("old credential accepted after rotation", err)
	}
	if _, err = f.repo.ReceiveAISecurityRuntimeEvent(ctx, strings.Repeat("e", 64), r); err != nil {
		t.Fatal("new credential rejected", err)
	}
	view, err := f.repo.AISecurityRuntimeEvidence(ctx, f.ws.ID, f.user.ID)
	if err != nil || len(view.Events) != 8 || view.Sources[0].TokenVersion != 2 {
		t.Fatal("rotation lost history", err)
	}
	if err = f.repo.RevokeAISecurityRuntimeSource(ctx, f.ws.ID, f.user.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.RotateAISecurityRuntimeSource(ctx, f.ws.ID, f.user.ID, source.ID, 2, strings.Repeat("f", 64)); !errors.Is(err, ErrConflict) {
		t.Fatal("revoked credential resurrected", err)
	}
	if _, err = f.repo.ReceiveAISecurityRuntimeEvent(ctx, strings.Repeat("e", 64), r); !errors.Is(err, ErrAutomationUnauthorized) {
		t.Fatal("revoked rotated credential accepted", err)
	}
}

func TestRuntimeSourceRotationConcurrentCAS(t *testing.T) {
	f, source, _, _ := runtimeOperationsFixture(t)
	var wg sync.WaitGroup
	errorsOut := make(chan error, 2)
	for _, hash := range []string{strings.Repeat("b", 64), strings.Repeat("c", 64)} {
		wg.Go(func() {
			_, err := f.repo.RotateAISecurityRuntimeSource(t.Context(), f.ws.ID, f.user.ID, source.ID, 1, hash)
			errorsOut <- err
		})
	}
	wg.Wait()
	close(errorsOut)
	ok, conflicts := 0, 0
	for err := range errorsOut {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatal("concurrent rotation escaped CAS", ok, conflicts)
	}
}
