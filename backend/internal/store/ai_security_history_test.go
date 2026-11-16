package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/jackc/pgx/v5"
)

func TestAISecurityHistoryReachesBeyondPreviewsAndPreservesHistoricalGates(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	cfg := aiTestConfig()
	if _, err := f.repo.SaveAISecurityConfiguration(ctx, f.ws.ID, f.user.ID, 0, cfg); err != nil {
		t.Fatal(err)
	}
	decision, _ := json.Marshal(aisecurity.Decision{Kind: "model", Outcome: "allow", CatalogVersion: "historical-v1", CatalogDigest: aisecurity.ConfigDigest(cfg)})
	if _, err := f.pool.Exec(ctx, `INSERT INTO plume.ai_security_decisions(workspace_id,actor_id,configuration_revision,decision) SELECT $1,$2,1,$3 FROM generate_series(1,103)`, f.ws.ID, f.user.ID, decision); err != nil {
		t.Fatal(err)
	}
	evaluation, _ := json.Marshal(AISecurityEvaluation{ConfigurationRevision: 1, CatalogDigest: aisecurity.ConfigDigest(cfg), Status: "passed", GateEligible: true, GateVersion: "historical-gate", EngineVersion: "historical-engine", InspectionVersion: "historical-inspector", Scope: []string{}, Cases: []AISecurityEvaluationCase{}, CoverageGaps: []string{}})
	if _, err := f.pool.Exec(ctx, `INSERT INTO plume.ai_security_evaluations(workspace_id,actor_id,configuration_revision,catalog_digest,status,result) SELECT $1,$2,1,$3,'passed',$4 FROM generate_series(1,33)`, f.ws.ID, f.user.ID, aisecurity.ConfigDigest(cfg), evaluation); err != nil {
		t.Fatal(err)
	}
	rawRequest, _ := json.Marshal(aisecurity.Request{RequestID: "synthetic-history-0001", Kind: "tool", ApplicationID: "enterprise-app", PrincipalID: "branch-user", ToolID: "approved-tool", Action: "update", Target: "crm-approved", Arguments: map[string]string{"record": "approved"}, IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour)})
	if _, err := f.pool.Exec(ctx, `INSERT INTO plume.ai_security_approvals(workspace_id,configuration_revision,catalog_digest,request_digest,request_metadata,requested_by,status,expires_at) SELECT $1,1,$2,$3,$4,$5,'proposed',clock_timestamp()+interval '1 hour' FROM generate_series(1,103)`, f.ws.ID, aisecurity.ConfigDigest(cfg), strings.Repeat("b", 64), rawRequest, f.user.ID); err != nil {
		t.Fatal(err)
	}
	for i := range 101 {
		pack := BaselineAISecurityRegressionPack()
		pack.Version = fmt.Sprintf("history-v%d", i)
		pack.Digest = aiPackDigest(pack)
		raw, _ := json.Marshal(pack)
		if _, err := f.pool.Exec(ctx, `INSERT INTO plume.ai_security_regression_packs(workspace_id,pack_id,version,digest,pack,created_by) VALUES($1,$2,$3,$4,$5,$6)`, f.ws.ID, pack.PackID, pack.Version, pack.Digest, raw, f.user.ID); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := f.repo.AISecurityBundle(ctx, f.ws.ID, f.user.ID)
	if err != nil || len(bundle.Decisions) != 100 || len(bundle.Evaluations) != 30 || len(bundle.Approvals) != 100 {
		t.Fatal("legacy preview contract changed", err)
	}
	for _, collection := range []string{"decisions", "evaluations", "approvals"} {
		window := bundle.HistoryCoverage[collection]
		if !window.HasMore || window.NextCursor == "" {
			t.Fatal("preview hides remaining history", collection, window)
		}
	}
	q := AISecurityHistoryQuery{Limit: 50}
	first, err := f.repo.AISecurityDecisions(ctx, f.ws.ID, f.user.ID, q)
	if err != nil || !first.HasMore || len(first.Items) != 50 {
		t.Fatal("decision first page", err)
	}
	q.Before, q.Kind = first.NextCursor, "rag"
	if _, err = f.repo.AISecurityDecisions(ctx, f.ws.ID, f.user.ID, q); !errors.Is(err, ErrInvalid) {
		t.Fatal("history cursor reused after filter change", err)
	}
	if _, err = f.repo.AISecurityApprovalsPage(ctx, f.ws.ID, f.user.ID, AISecurityHistoryQuery{Before: first.NextCursor, Limit: 50}); !errors.Is(err, ErrInvalid) {
		t.Fatal("history cursor reused across collections", err)
	}
	other := assessmentSourceTestStore(t)
	if _, err = f.repo.AISecurityDecisions(ctx, other.ws.ID, other.user.ID, AISecurityHistoryQuery{Before: first.NextCursor, Limit: 50}); !errors.Is(err, ErrInvalid) {
		t.Fatal("history cursor escaped workspace", err)
	}
	if _, err = f.repo.AISecurityDecision(ctx, other.ws.ID, other.user.ID, first.Items[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("history detail escaped workspace", err)
	}
	remaining, err := f.repo.AISecurityDecisions(ctx, f.ws.ID, f.user.ID, AISecurityHistoryQuery{Limit: 50, Before: bundle.HistoryCoverage["decisions"].NextCursor})
	if err != nil || len(remaining.Items) != 3 || remaining.HasMore {
		t.Fatal("preview continuation not usable", remaining, err)
	}
	for _, collection := range []string{"decisions", "evaluations", "approvals", "regression-packs"} {
		cursor, count := "", 0
		seen := map[int64]bool{}
		for {
			q := AISecurityHistoryQuery{Limit: 50, Before: cursor}
			ids, more, next := []int64{}, false, ""
			switch collection {
			case "decisions":
				p, e := f.repo.AISecurityDecisions(ctx, f.ws.ID, f.user.ID, q)
				err, more, next = e, p.HasMore, p.NextCursor
				for _, item := range p.Items {
					ids = append(ids, item.ID)
				}
			case "evaluations":
				p, e := f.repo.AISecurityEvaluationsPage(ctx, f.ws.ID, f.user.ID, q)
				err, more, next = e, p.HasMore, p.NextCursor
				for _, item := range p.Items {
					ids = append(ids, item.ID)
				}
			case "approvals":
				p, e := f.repo.AISecurityApprovalsPage(ctx, f.ws.ID, f.user.ID, q)
				err, more, next = e, p.HasMore, p.NextCursor
				for _, item := range p.Items {
					ids = append(ids, item.ID)
				}
			case "regression-packs":
				p, e := f.repo.AISecurityRegressionPackPage(ctx, f.ws.ID, f.user.ID, q)
				err, more, next = e, p.HasMore, p.NextCursor
				for _, item := range p.Items {
					ids = append(ids, item.ID)
				}
			}
			if err != nil {
				t.Fatal(collection, err)
			}
			for i, id := range ids {
				if seen[id] || i > 0 && ids[i-1] <= id {
					t.Fatal("unstable history keyset", collection)
				}
				seen[id] = true
			}
			count += len(ids)
			if !more {
				if next != "" {
					t.Fatal("terminal cursor")
				}
				break
			}
			cursor = next
		}
		if expected := map[string]int{"decisions": 103, "evaluations": 33, "approvals": 103, "regression-packs": 101}[collection]; count != expected {
			t.Fatal("history lost after preview", collection, count)
		}
	}
	item, err := f.repo.AISecurityDecision(ctx, f.ws.ID, f.user.ID, first.Items[0].ID)
	if err != nil || item.Decision.CatalogVersion != "historical-v1" || item.CreatedAt.IsZero() || item.Mode != "evaluation" {
		t.Fatal("historical decision rewritten", err)
	}
	evals, err := f.repo.AISecurityEvaluationsPage(ctx, f.ws.ID, f.user.ID, AISecurityHistoryQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	history, err := f.repo.AISecurityEvaluation(ctx, f.ws.ID, f.user.ID, evals.Items[0].ID)
	if err != nil || history.EngineVersion != "historical-engine" || history.InspectionVersion != "historical-inspector" || !history.GateEligible {
		t.Fatal("historical evaluation recomputed", err)
	}
	reviewer := gatewayTestMember(t, f, "reviewer")
	if _, err = f.repo.PublishAISecurity(ctx, f.ws.ID, reviewer, 1, history.ID, "Synthetic historical gate must remain ineligible today"); !errors.Is(err, ErrEvidenceRequired) {
		t.Fatal("history read promoted an obsolete gate", err)
	}
}

func TestAISecurityHistoryDoesNotExpandExecutableApprovalLimit(t *testing.T) {
	f := assessmentSourceTestStore(t)
	cfg := aiTestConfig()
	raw, _ := json.Marshal(aisecurity.Request{RequestID: "synthetic-approval-cap", ApplicationID: "enterprise-app", PrincipalID: "branch-user", ToolID: "approved-tool", Action: "update", Target: "crm-approved", IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour)})
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO plume.ai_security_approvals(workspace_id,configuration_revision,catalog_digest,request_digest,request_metadata,requested_by,reviewed_by,status,expires_at) SELECT $1,1,$2,$3,$4,$5,$5,'approved',clock_timestamp()+interval '1 hour' FROM generate_series(1,1001)`, f.ws.ID, aisecurity.ConfigDigest(cfg), strings.Repeat("b", 64), raw, f.user.ID); err != nil {
		t.Fatal(err)
	}
	count, cursor := 0, ""
	for {
		p, err := f.repo.AISecurityApprovalsPage(t.Context(), f.ws.ID, f.user.ID, AISecurityHistoryQuery{Limit: 200, Before: cursor, Status: "approved"})
		if err != nil {
			t.Fatal(err)
		}
		count += len(p.Items)
		if !p.HasMore {
			break
		}
		cursor = p.NextCursor
	}
	if count != 1001 {
		t.Fatal("history must reach records beyond the execution cap", count)
	}
	err := pgx.BeginTxFunc(t.Context(), f.pool, pgx.TxOptions{}, func(tx pgx.Tx) error { return aiAttachApprovals(t.Context(), tx, f.ws.ID, 1, &cfg) })
	if !errors.Is(err, ErrRuntimeLimit) {
		t.Fatal("history pagination changed executable approval cap", err)
	}
}
