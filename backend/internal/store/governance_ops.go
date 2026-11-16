package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/governanceops"
	"github.com/jackc/pgx/v5"
)

type GovernanceOpsEvidence struct {
	ID             int64     `json:"id"`
	CheckID        int64     `json:"checkId"`
	Kind           string    `json:"kind"`
	SourceSystem   string    `json:"sourceSystem"`
	SourceRecord   string    `json:"sourceRecord"`
	Summary        string    `json:"summary"`
	ObservedAt     time.Time `json:"observedAt"`
	CreatedBy      int64     `json:"createdBy"`
	Provenance     string    `json:"provenance"`
	SnapshotDigest string    `json:"snapshotDigest"`
}
type GovernanceOpsEvent struct {
	ID             int64     `json:"id"`
	Revision       int64     `json:"revision"`
	Action         string    `json:"action"`
	Note           string    `json:"note"`
	ActorID        int64     `json:"actorId"`
	Actor          string    `json:"actor"`
	SnapshotDigest string    `json:"snapshotDigest"`
	CreatedAt      time.Time `json:"createdAt"`
}
type GovernanceOpsRecord struct {
	governanceops.Input
	ID                int64                      `json:"id"`
	Status            string                     `json:"status"`
	Regulations       []governanceops.Regulation `json:"regulations"`
	Evidence          []GovernanceOpsEvidence    `json:"evidence"`
	Contributors      []int64                    `json:"contributors"`
	CreatedBy         int64                      `json:"createdBy"`
	UpdatedBy         int64                      `json:"updatedBy"`
	Withdrawing       bool                       `json:"withdrawing"`
	CreatedAt         time.Time                  `json:"createdAt"`
	UpdatedAt         time.Time                  `json:"updatedAt"`
	Deadlines         []governanceops.Deadline   `json:"deadlines"`
	History           []GovernanceOpsEvent       `json:"history"`
	HistoryNextCursor int64                      `json:"historyNextCursor,omitempty"`
	HistoryHasMore    bool                       `json:"historyHasMore"`
	Boundary          string                     `json:"boundary"`
	EvidenceIssues    []string                   `json:"evidenceIssues"`
}
type GovernanceOpsPage struct {
	Items      []GovernanceOpsRecord `json:"items"`
	NextCursor int64                 `json:"nextCursor,omitempty"`
	HasMore    bool                  `json:"hasMore"`
}
type GovernanceOpsHistoryPage struct {
	Items      []GovernanceOpsEvent `json:"items"`
	NextCursor int64                `json:"nextCursor,omitempty"`
	HasMore    bool                 `json:"hasMore"`
}

const governanceOpsSelect = `SELECT id,status,revision,input,regulations,evidence,contributors,created_by,updated_by,withdrawing,created_at,updated_at FROM plume.governance_ops_records`

func scanGovernanceOps(row pgx.Row) (GovernanceOpsRecord, error) {
	var out GovernanceOpsRecord
	var revision int64
	err := row.Scan(&out.ID, &out.Status, &revision, &out.Input, &out.Regulations, &out.Evidence, &out.Contributors, &out.CreatedBy, &out.UpdatedBy, &out.Withdrawing, &out.CreatedAt, &out.UpdatedAt)
	out.Revision = revision
	out.Deadlines = governanceops.Deadlines(out.Input, time.Now())
	out.Boundary = governanceops.Boundary
	out.History = []GovernanceOpsEvent{}
	out.EvidenceIssues = governanceOpsEvidenceIssues(out.Input, out.Evidence)
	return out, storeError(err)
}
func readGovernanceOps(ctx context.Context, tx pgx.Tx, ws, id int64, lock bool) (GovernanceOpsRecord, error) {
	q := governanceOpsSelect + ` WHERE workspace_id=$1 AND id=$2`
	if lock {
		q += ` FOR UPDATE`
	}
	return scanGovernanceOps(tx.QueryRow(ctx, q, ws, id))
}
func governanceOpsError(err error) error {
	switch {
	case errors.Is(err, governanceops.ErrInvalid):
		return ErrInvalid
	case errors.Is(err, governanceops.ErrEvidence):
		return ErrEvidenceRequired
	case errors.Is(err, governanceops.ErrTransition):
		return ErrConflict
	}
	return storeError(err)
}

func freezeGovernanceOpsReferences(ctx context.Context, tx pgx.Tx, ws int64, in governanceops.Input) ([]GovernanceOpsEvidence, error) {
	if err := in.Validate(time.Now()); err != nil {
		return nil, governanceOpsError(err)
	}
	var n int
	var assetID int64
	if in.ActivityID > 0 {
		if err := tx.QueryRow(ctx, `SELECT asset_id FROM plume.activities WHERE workspace_id=$1 AND id=$2 FOR SHARE`, ws, in.ActivityID).Scan(&assetID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrInvalid
			}
			return nil, err
		}
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM plume.checks WHERE workspace_id=$1 AND id=ANY($2::bigint[]) AND ($3::bigint=0 OR (asset_id=$3 AND (activity_id IS NULL OR activity_id=$4)))`, ws, in.CheckIDs, assetID, in.ActivityID).Scan(&n); err != nil {
		return nil, err
	}
	if n != len(in.CheckIDs) {
		return nil, ErrInvalid
	}
	for _, id := range []int64{in.Details.IdentityEvidenceID, in.Details.ScaleEvidenceID} {
		if id > 0 && !slices.Contains(in.EvidenceIDs, id) {
			return nil, ErrInvalid
		}
	}
	for _, r := range in.Receipts {
		if !slices.Contains(in.EvidenceIDs, r.EvidenceID) {
			return nil, ErrInvalid
		}
	}
	rows, err := tx.Query(ctx, `SELECT e.id,e.check_id,e.kind,e.source_system,e.source_record,e.summary,e.observed_at,e.created_by FROM plume.check_evidence e JOIN plume.checks c ON c.id=e.check_id WHERE c.workspace_id=$1 AND e.id=ANY($2::bigint[]) AND ($3::bigint=0 OR (c.asset_id=$3 AND (c.activity_id IS NULL OR c.activity_id=$4))) ORDER BY e.id FOR SHARE OF e,c`, ws, in.EvidenceIDs, assetID, in.ActivityID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (GovernanceOpsEvidence, error) {
		var e GovernanceOpsEvidence
		err := row.Scan(&e.ID, &e.CheckID, &e.Kind, &e.SourceSystem, &e.SourceRecord, &e.Summary, &e.ObservedAt, &e.CreatedBy)
		e.Provenance = "recorded_evidence"
		b, _ := json.Marshal(e)
		h := sha256.Sum256(b)
		e.SnapshotDigest = hex.EncodeToString(h[:])
		return e, err
	})
	if err != nil {
		return nil, err
	}
	if len(out) != len(in.EvidenceIDs) {
		return nil, ErrInvalid
	}
	if out == nil {
		out = []GovernanceOpsEvidence{}
	}
	return out, nil
}

func governanceOpsEvidenceIssues(in governanceops.Input, evidence []GovernanceOpsEvidence) []string {
	out := []string{}
	byID := map[int64]GovernanceOpsEvidence{}
	for _, e := range evidence {
		byID[e.ID] = e
	}
	for _, r := range in.Receipts {
		var start *time.Time
		label := ""
		switch r.Purpose {
		case "identity_verification", "response_delivery", "rights_execution":
			start = in.Details.ReceivedAt
			label = "本次权利请求"
		case "consent_grant":
			start = in.Details.GrantedAt
			label = "本次同意取得"
		case "withdrawal_propagation":
			start = in.Details.WithdrawalAt
			label = "撤回请求"
		case "initial_report":
			start = in.Details.DetectedAt
			label = "事件发现或获知"
		case "final_report":
			start = in.Details.ResponseEndedAt
			label = "本次处置结束"
		case "audit_report":
			start = in.Details.CompletedAt
			label = "本次审计或评估完成"
		}
		if e, ok := byID[r.EvidenceID]; ok && start != nil && e.ObservedAt.Add(time.Microsecond).Before(*start) {
			// PostgreSQL stores microseconds; permit only that precision difference.
			out = append(out, fmt.Sprintf("证据#%d（%s）的观测早于%s，不能作为该阶段完成的回执。保留原证据并取得本次活动的新记录。", r.EvidenceID, r.Purpose, label))
		}
	}
	return out
}

func governanceContributors(ids []int64, actor int64, evidence []GovernanceOpsEvidence) []int64 {
	if !slices.Contains(ids, actor) {
		ids = append(ids, actor)
	}
	for _, e := range evidence {
		if !slices.Contains(ids, e.CreatedBy) {
			ids = append(ids, e.CreatedBy)
		}
	}
	return ids
}

func normalizedGovernanceInput(in governanceops.Input) governanceops.Input {
	in.Revision = 0
	if in.CheckIDs == nil {
		in.CheckIDs = []int64{}
	}
	if in.EvidenceIDs == nil {
		in.EvidenceIDs = []int64{}
	}
	if in.RegulationIDs == nil {
		in.RegulationIDs = []string{}
	}
	if in.Receipts == nil {
		in.Receipts = []governanceops.Receipt{}
	}
	return in
}
func appendGovernanceOpsEvent(ctx context.Context, tx pgx.Tx, ws, actor int64, out GovernanceOpsRecord, action, note string) error {
	// No credentials or unbounded metadata are accepted by the input contract.
	snapshot := struct {
		Input        governanceops.Input        `json:"input"`
		Status       string                     `json:"status"`
		Withdrawing  bool                       `json:"withdrawing"`
		Regulations  []governanceops.Regulation `json:"regulations"`
		Evidence     []GovernanceOpsEvidence    `json:"evidence"`
		Contributors []int64                    `json:"contributors"`
	}{out.Input, out.Status, out.Withdrawing, out.Regulations, out.Evidence, out.Contributors}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	h := sha256.Sum256(b)
	_, err = tx.Exec(ctx, `INSERT INTO plume.governance_ops_events(workspace_id,record_id,revision,action,actor_id,note,snapshot,snapshot_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, ws, out.ID, out.Revision, action, actor, note, b, hex.EncodeToString(h[:]))
	return err
}

func (s *Store) SaveGovernanceOps(ctx context.Context, ws, actor, id int64, in governanceops.Input) (GovernanceOpsRecord, error) {
	var out GovernanceOpsRecord
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, ws, actor, true)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		evidence, err := freezeGovernanceOpsReferences(ctx, tx, ws, in)
		if err != nil {
			return err
		}
		if id == 0 {
			if in.Revision != 0 {
				return ErrConflict
			}
			// Serialize workspace creation budgets to avoid concurrent limit bypass.
			if _, err = tx.Exec(ctx, `SELECT id FROM plume.workspaces WHERE id=$1 FOR UPDATE`, ws); err != nil {
				return err
			}
			var n int
			if err = tx.QueryRow(ctx, `SELECT count(*) FROM plume.governance_ops_records WHERE workspace_id=$1`, ws).Scan(&n); err != nil {
				return err
			}
			if n >= 5000 {
				return ErrRuntimeLimit
			}
			out = GovernanceOpsRecord{Input: normalizedGovernanceInput(in), Status: "draft", Regulations: governanceops.Adopted(in.RegulationIDs), Evidence: evidence, Contributors: governanceContributors(nil, actor, evidence), CreatedBy: actor, UpdatedBy: actor}
			err = tx.QueryRow(ctx, `INSERT INTO plume.governance_ops_records(workspace_id,kind,input,regulations,evidence,contributors,created_by,updated_by)VALUES($1,$2,$3,$4,$5,$6,$7,$7)RETURNING id,revision,created_at,updated_at`, ws, in.Kind, out.Input, out.Regulations, out.Evidence, out.Contributors, actor).Scan(&out.ID, &out.Revision, &out.CreatedAt, &out.UpdatedAt)
			if err != nil {
				return err
			}
			return appendGovernanceOpsEvent(ctx, tx, ws, actor, out, "created", "")
		}
		old, err := readGovernanceOps(ctx, tx, ws, id, true)
		if err != nil {
			return err
		}
		if old.Revision != in.Revision || old.Kind != in.Kind || !slices.Contains([]string{"draft", "in_progress", "needs_work", "withdrawal_pending"}, old.Status) {
			return ErrConflict
		}
		if old.Withdrawing && !sameConsentGrant(old.Input, in) {
			return ErrConflict
		}
		out = old
		out.Input = normalizedGovernanceInput(in)
		out.Revision = old.Revision + 1
		out.Evidence = evidence
		out.UpdatedBy = actor
		// Freeze already-adopted revisions rather than rewriting historic sources.
		out.Regulations = []governanceops.Regulation{}
		for _, rid := range in.RegulationIDs {
			var adopted *governanceops.Regulation
			for _, r := range old.Regulations {
				if r.ID == rid {
					v := r
					adopted = &v
					break
				}
			}
			if adopted != nil {
				out.Regulations = append(out.Regulations, *adopted)
			} else if r, ok := governanceops.RegulationByID(rid); ok {
				out.Regulations = append(out.Regulations, r)
			}
		}
		out.Contributors = governanceContributors(out.Contributors, actor, evidence)
		err = tx.QueryRow(ctx, `UPDATE plume.governance_ops_records SET revision=$3,input=$4,regulations=$5,evidence=$6,contributors=$7,updated_by=$8,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 RETURNING updated_at`, ws, id, out.Revision, out.Input, out.Regulations, out.Evidence, out.Contributors, actor).Scan(&out.UpdatedAt)
		if err != nil {
			return err
		}
		return appendGovernanceOpsEvent(ctx, tx, ws, actor, out, "updated", "")
	})
	if err != nil {
		return out, governanceOpsError(err)
	}
	return s.GovernanceOpsRecord(ctx, ws, actor, out.ID)
}
func sameConsentGrant(old, in governanceops.Input) bool {
	a, b := old.Details, in.Details
	if a.SubjectRef != b.SubjectRef || a.Purpose != b.Purpose || a.LawfulBasis != b.LawfulBasis || a.NoticeVersion != b.NoticeVersion || (a.GrantedAt == nil) != (b.GrantedAt == nil) {
		return false
	}
	if a.GrantedAt != nil && !a.GrantedAt.Equal(*b.GrantedAt) {
		return false
	}
	if (a.WithdrawalAt == nil) != (b.WithdrawalAt == nil) || (a.WithdrawalAt != nil && !a.WithdrawalAt.Equal(*b.WithdrawalAt)) {
		return false
	}
	for _, sys := range a.Systems {
		if !slices.Contains(b.Systems, sys) {
			return false
		}
	}
	return in.Applicability == "applicable" && old.Applicability == in.Applicability
}

func (s *Store) ActGovernanceOps(ctx context.Context, ws, actor, id int64, a governanceops.Action) (GovernanceOpsRecord, error) {
	var out GovernanceOpsRecord
	if !a.Valid() {
		return out, ErrInvalid
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, ws, actor, true)
		if err != nil {
			return err
		}
		out, err = readGovernanceOps(ctx, tx, ws, id, true)
		if err != nil {
			return err
		}
		if out.Revision != a.Revision {
			return ErrConflict
		}
		review := a.Action == "approve" || a.Action == "request_changes"
		if review {
			if (role != "owner" && role != "reviewer") || slices.Contains(out.Contributors, actor) {
				return ErrForbidden
			}
		} else if !checkEditor(role) {
			return ErrForbidden
		}
		if a.Action == "submit" || a.Action == "approve" {
			if _, err = freezeGovernanceOpsReferences(ctx, tx, ws, out.Input); err != nil {
				return err
			}
			if out.Applicability == "applicable" && len(governanceOpsEvidenceIssues(out.Input, out.Evidence)) > 0 {
				return ErrEvidenceRequired
			}
		}
		status, err := governanceops.NextStatus(out.Input, out.Status, a.Action, out.Withdrawing, time.Now())
		if err != nil {
			return governanceOpsError(err)
		}
		if a.Action == "withdraw" {
			out.Withdrawing = true
			now := time.Now().UTC()
			out.Details.WithdrawalAt = &now
		}
		out.Status = status
		out.Revision++
		out.UpdatedBy = actor
		// Submit/start/cancel/withdraw are contributions too, including evidence
		// added by a different author in an earlier revision.
		if !review && !slices.Contains(out.Contributors, actor) {
			out.Contributors = append(out.Contributors, actor)
		}
		err = tx.QueryRow(ctx, `UPDATE plume.governance_ops_records SET status=$3,revision=$4,input=$5,contributors=$6,updated_by=$7,withdrawing=$8,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 RETURNING updated_at`, ws, id, out.Status, out.Revision, normalizedGovernanceInput(out.Input), out.Contributors, actor, out.Withdrawing).Scan(&out.UpdatedAt)
		if err != nil {
			return err
		}
		return appendGovernanceOpsEvent(ctx, tx, ws, actor, out, a.Action, a.Note)
	})
	if err != nil {
		return out, governanceOpsError(err)
	}
	return s.GovernanceOpsRecord(ctx, ws, actor, id)
}

func (s *Store) GovernanceOpsRecord(ctx context.Context, ws, actor, id int64) (GovernanceOpsRecord, error) {
	var out GovernanceOpsRecord
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, ws, actor, false); err != nil {
			return err
		}
		var err error
		out, err = readGovernanceOps(ctx, tx, ws, id, false)
		if err != nil {
			return err
		}
		p, err := readGovernanceOpsHistory(ctx, tx, ws, id, 0, 50)
		if err != nil {
			return err
		}
		out.History = p.Items
		out.HistoryHasMore = p.HasMore
		out.HistoryNextCursor = p.NextCursor
		return nil
	})
	return out, storeError(err)
}

func (s *Store) ListGovernanceOps(ctx context.Context, ws, actor int64, kind, status string, cursor int64, limit int) (GovernanceOpsPage, error) {
	out := GovernanceOpsPage{Items: []GovernanceOpsRecord{}}
	if cursor < 0 || limit < 1 || limit > 100 || (kind != "" && !slices.Contains(governanceops.Kinds, kind)) || (status != "" && !slices.Contains(governanceops.Statuses, status)) {
		return out, ErrInvalid
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, ws, actor, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, governanceOpsSelect+` WHERE workspace_id=$1 AND ($2='' OR kind=$2) AND ($3='' OR status=$3) AND ($4::bigint=0 OR id<$4) ORDER BY id DESC LIMIT $5`, ws, kind, status, cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, e := scanGovernanceOps(rows)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, v)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if len(out.Items) > limit {
			out.HasMore = true
			out.Items = out.Items[:limit]
			out.NextCursor = out.Items[len(out.Items)-1].ID
		}
		return nil
	})
	return out, storeError(err)
}

func readGovernanceOpsHistory(ctx context.Context, tx pgx.Tx, ws, id, cursor int64, limit int) (GovernanceOpsHistoryPage, error) {
	out := GovernanceOpsHistoryPage{Items: []GovernanceOpsEvent{}}
	rows, err := tx.Query(ctx, `SELECT e.id,e.revision,e.action,e.note,e.actor_id,u.username,e.snapshot_digest,e.created_at FROM plume.governance_ops_events e JOIN plume.users u ON u.id=e.actor_id WHERE e.workspace_id=$1 AND e.record_id=$2 AND ($3::bigint=0 OR e.id<$3) ORDER BY e.id DESC LIMIT $4`, ws, id, cursor, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v GovernanceOpsEvent
		if err = rows.Scan(&v.ID, &v.Revision, &v.Action, &v.Note, &v.ActorID, &v.Actor, &v.SnapshotDigest, &v.CreatedAt); err != nil {
			return out, err
		}
		out.Items = append(out.Items, v)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		out.HasMore = true
		out.Items = out.Items[:limit]
		out.NextCursor = out.Items[len(out.Items)-1].ID
	}
	return out, nil
}
func (s *Store) GovernanceOpsHistory(ctx context.Context, ws, actor, id, cursor int64, limit int) (GovernanceOpsHistoryPage, error) {
	var out GovernanceOpsHistoryPage
	if cursor < 0 || limit < 1 || limit > 100 {
		return out, ErrInvalid
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, ws, actor, false); err != nil {
			return err
		}
		if _, err := readGovernanceOps(ctx, tx, ws, id, false); err != nil {
			return err
		}
		var err error
		out, err = readGovernanceOpsHistory(ctx, tx, ws, id, cursor, limit)
		return err
	})
	return out, storeError(err)
}
