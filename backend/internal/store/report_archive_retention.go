package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrReportArchiveProtected = errors.New("report archive retention protection")
var ErrReportArchiveIntegrity = errors.New("report archive integrity verification failed")

const ReportArchiveRetentionBoundary = "用途未核定的归档默认受保护，存储会持续增长，请明确实际保留依据并安排独立复核。数量轮换仅处理明确放行、无保留hold且保留期已到的归档。本地SHA-256核验和准备导出记录不表示外部不可变存储、监管收件或浏览器成功保存。"

type ReportArchiveRetention struct {
	Revision     int64      `json:"revision"`
	State        string     `json:"state"`
	RetainUntil  *time.Time `json:"retainUntil,omitempty"`
	LegalHold    bool       `json:"legalHold"`
	HoldReason   string     `json:"holdReason"`
	Basis        string     `json:"basis"`
	UpdatedBy    *int64     `json:"updatedBy,omitempty"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	Contributors []int64    `json:"contributors"`
}
type ReportArchivePage struct {
	Items                 []ReportArchive `json:"items"`
	RetentionPerJob       int             `json:"retentionPerJob"`
	HasMore               bool            `json:"hasMore"`
	NextCursor            int64           `json:"nextCursor,omitempty"`
	Boundary              string          `json:"boundary"`
	ExternalStorageStatus string          `json:"externalStorageStatus"`
}
type ReportArchiveRetentionInput struct {
	Revision    int64      `json:"revision"`
	Action      string     `json:"action"`
	Basis       string     `json:"basis"`
	RetainUntil *time.Time `json:"retainUntil,omitempty"`
	EvidenceID  int64      `json:"evidenceId,omitempty"`
}
type ReportArchiveRetentionEvent struct {
	ID         int64          `json:"id"`
	Action     string         `json:"action"`
	ActorID    *int64         `json:"actorId,omitempty"`
	Actor      string         `json:"actor"`
	Note       string         `json:"note"`
	EvidenceID *int64         `json:"evidenceId,omitempty"`
	Snapshot   map[string]any `json:"snapshot"`
	CreatedAt  time.Time      `json:"createdAt"`
}
type ReportArchiveRetentionHistory struct {
	Items      []ReportArchiveRetentionEvent `json:"items"`
	NextCursor int64                         `json:"nextCursor,omitempty"`
	HasMore    bool                          `json:"hasMore"`
}
type ReportArchiveIntegrity struct {
	ArchiveID             int64  `json:"archiveId"`
	ContentDigest         string `json:"contentDigest"`
	SummaryDigest         string `json:"summaryDigest"`
	ContentBytes          int64  `json:"contentBytes"`
	IntegrityStatus       string `json:"integrityStatus"`
	Algorithm             string `json:"algorithm"`
	Canonicalization      string `json:"canonicalization"`
	ExternalStorageStatus string `json:"externalStorageStatus"`
	Boundary              string `json:"boundary"`
}

const reportArchiveInfoSelect = `SELECT a.automation_run_id,j.id,j.source,r.run_id,r.observed_at,a.generated_at,a.status,COALESCE(a.code,''),a.summary->'counts',a.summary->'identification',a.retention_revision,a.retention_state,a.retain_until,a.legal_hold,a.hold_reason,a.retention_basis,a.retention_updated_by,a.retention_updated_at,a.retention_contributors,COALESCE(a.content_digest,''),COALESCE(a.summary_digest,''),COALESCE(a.content_bytes,0) FROM plume.automation_report_archives a JOIN plume.automation_runs r ON r.id=a.automation_run_id JOIN plume.automation_jobs j ON j.id=r.job_id`

func scanReportArchiveInfo(row pgx.Row) (ReportArchive, error) {
	var a ReportArchive
	err := row.Scan(&a.ID, &a.JobID, &a.Source, &a.RunID, &a.ObservedAt, &a.GeneratedAt, &a.Status, &a.Code, &a.Counts, &a.Identification, &a.Retention.Revision, &a.Retention.State, &a.Retention.RetainUntil, &a.Retention.LegalHold, &a.Retention.HoldReason, &a.Retention.Basis, &a.Retention.UpdatedBy, &a.Retention.UpdatedAt, &a.Retention.Contributors, &a.ContentDigest, &a.SummaryDigest, &a.ContentBytes)
	a.IntegrityStatus = "not_available"
	if a.Status == "ready" {
		a.IntegrityStatus = "recorded"
	}
	return a, storeError(err)
}
func archiveRetentionError(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		if pe.ConstraintName == "report_archive_retention_guard" {
			return ErrReportArchiveProtected
		}
		if pe.ConstraintName == "report_archive_content_guard" {
			return ErrReportArchiveIntegrity
		}
	}
	return storeError(err)
}

func (s *Store) ListReportArchivePage(ctx context.Context, ws, actor, cursor int64, limit int) (ReportArchivePage, error) {
	out := ReportArchivePage{Items: []ReportArchive{}, RetentionPerJob: ReportArchiveRetentionPerJob, Boundary: ReportArchiveRetentionBoundary, ExternalStorageStatus: "unverified"}
	if cursor < 0 || limit < 1 || limit > 100 {
		return out, ErrInvalid
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, ws, actor, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, reportArchiveInfoSelect+` WHERE j.workspace_id=$1 AND ($2::bigint=0 OR a.automation_run_id<$2) ORDER BY a.automation_run_id DESC LIMIT $3`, ws, cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, e := scanReportArchiveInfo(rows)
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

func readArchiveRetentionEvidence(ctx context.Context, tx pgx.Tx, ws, id int64) (*GovernanceOpsEvidence, error) {
	if id < 1 {
		return nil, ErrEvidenceRequired
	}
	var e GovernanceOpsEvidence
	err := tx.QueryRow(ctx, `SELECT e.id,e.check_id,e.kind,e.source_system,e.source_record,e.summary,e.observed_at,e.created_by FROM plume.check_evidence e JOIN plume.checks c ON c.id=e.check_id WHERE c.workspace_id=$1 AND e.id=$2 FOR SHARE OF e,c`, ws, id).Scan(&e.ID, &e.CheckID, &e.Kind, &e.SourceSystem, &e.SourceRecord, &e.Summary, &e.ObservedAt, &e.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	e.Provenance = "recorded_evidence"
	b, _ := json.Marshal(e)
	h := sha256.Sum256(b)
	e.SnapshotDigest = hex.EncodeToString(h[:])
	return &e, nil
}

func (s *Store) SetReportArchiveRetention(ctx context.Context, ws, actor, id int64, in ReportArchiveRetentionInput) (ReportArchive, error) {
	var out ReportArchive
	if in.Revision < 1 || !validCheckText(in.Basis, 600) || in.EvidenceID < 0 || !slices.Contains([]string{"hold", "extend", "allow_rotation", "release_hold"}, in.Action) {
		return out, ErrInvalid
	}
	if in.RetainUntil != nil && (in.RetainUntil.IsZero() || in.RetainUntil.Year() > 2200 || in.Action != "extend") {
		return out, ErrInvalid
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, ws, actor, true)
		if err != nil {
			return err
		}
		out, err = scanReportArchiveInfo(tx.QueryRow(ctx, reportArchiveInfoSelect+` WHERE j.workspace_id=$1 AND a.automation_run_id=$2 FOR UPDATE OF a`, ws, id))
		if err != nil {
			return err
		}
		if out.Retention.Revision != in.Revision {
			return ErrConflict
		}
		review := in.Action == "allow_rotation" || in.Action == "release_hold"
		var evidence *GovernanceOpsEvidence
		if in.EvidenceID > 0 {
			evidence, err = readArchiveRetentionEvidence(ctx, tx, ws, in.EvidenceID)
			if err != nil {
				return err
			}
		}
		if review {
			if role != "owner" && role != "reviewer" {
				return ErrForbidden
			}
			if evidence == nil {
				return ErrEvidenceRequired
			}
			if slices.Contains(out.Retention.Contributors, actor) || evidence.CreatedBy == actor {
				return ErrForbidden
			}
			var heldAt *time.Time
			if err = tx.QueryRow(ctx, `SELECT max(created_at) FROM plume.report_archive_retention_events WHERE workspace_id=$1 AND archive_id=$2 AND action='hold'`, ws, id).Scan(&heldAt); err != nil {
				return err
			}
			if heldAt != nil && evidence.ObservedAt.Add(time.Microsecond).Before(*heldAt) {
				return ErrEvidenceRequired
			}
		} else if !checkEditor(role) {
			return ErrForbidden
		}
		now := time.Now()
		ret := out.Retention
		switch in.Action {
		case "hold":
			if ret.LegalHold {
				return ErrConflict
			}
			ret.LegalHold = true
			ret.HoldReason = in.Basis
		case "extend":
			if in.RetainUntil == nil || !in.RetainUntil.After(now) || (ret.RetainUntil != nil && !in.RetainUntil.After(*ret.RetainUntil)) {
				return ErrConflict
			}
			ret.RetainUntil = in.RetainUntil
			ret.State = "retained"
		case "release_hold":
			if !ret.LegalHold {
				return ErrConflict
			}
			ret.LegalHold = false
			ret.HoldReason = ""
		case "allow_rotation":
			if ret.LegalHold || (ret.RetainUntil != nil && ret.RetainUntil.After(now)) {
				return ErrReportArchiveProtected
			}
			ret.State = "rotation_allowed"
		}
		if !review {
			if !slices.Contains(ret.Contributors, actor) {
				ret.Contributors = append(ret.Contributors, actor)
			}
			if evidence != nil && !slices.Contains(ret.Contributors, evidence.CreatedBy) {
				ret.Contributors = append(ret.Contributors, evidence.CreatedBy)
			}
		}
		ret.Revision++
		ret.Basis = in.Basis
		ret.UpdatedBy = &actor
		err = tx.QueryRow(ctx, `UPDATE plume.automation_report_archives SET retention_revision=$2,retention_state=$3,retain_until=$4,legal_hold=$5,hold_reason=$6,retention_basis=$7,retention_updated_by=$8,retention_updated_at=clock_timestamp(),retention_contributors=$9 WHERE automation_run_id=$1 RETURNING retention_updated_at`, id, ret.Revision, ret.State, ret.RetainUntil, ret.LegalHold, ret.HoldReason, ret.Basis, actor, ret.Contributors).Scan(&ret.UpdatedAt)
		if err != nil {
			return err
		}
		out.Retention = ret
		snapshot := map[string]any{"retention": ret, "contentDigest": out.ContentDigest, "summaryDigest": out.SummaryDigest, "evidence": evidence, "evidenceBoundary": "existing recorded evidence; not independently verified legal authority or external storage"}
		_, err = tx.Exec(ctx, `INSERT INTO plume.report_archive_retention_events(workspace_id,archive_id,actor_id,action,note,evidence_id,snapshot)VALUES($1,$2,$3,$4,$5,NULLIF($6,0),$7)`, ws, id, actor, in.Action, in.Basis, in.EvidenceID, snapshot)
		return err
	})
	return out, archiveRetentionError(err)
}

func (s *Store) ReportArchiveRetentionHistory(ctx context.Context, ws, actor, id, cursor int64, limit int) (ReportArchiveRetentionHistory, error) {
	out := ReportArchiveRetentionHistory{Items: []ReportArchiveRetentionEvent{}}
	if id < 1 || cursor < 0 || limit < 1 || limit > 100 {
		return out, ErrInvalid
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, ws, actor, false); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.automation_report_archives a JOIN plume.automation_runs r ON r.id=a.automation_run_id JOIN plume.automation_jobs j ON j.id=r.job_id WHERE j.workspace_id=$1 AND a.automation_run_id=$2) OR EXISTS(SELECT 1 FROM plume.report_archive_retention_events WHERE workspace_id=$1 AND archive_id=$2)`, ws, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		rows, err := tx.Query(ctx, `SELECT e.id,e.action,e.actor_id,COALESCE(u.username,'system'),e.note,e.evidence_id,e.snapshot,e.created_at FROM plume.report_archive_retention_events e LEFT JOIN plume.users u ON u.id=e.actor_id WHERE e.workspace_id=$1 AND e.archive_id=$2 AND ($3::bigint=0 OR e.id<$3) ORDER BY e.id DESC LIMIT $4`, ws, id, cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e ReportArchiveRetentionEvent
			if err = rows.Scan(&e.ID, &e.Action, &e.ActorID, &e.Actor, &e.Note, &e.EvidenceID, &e.Snapshot, &e.CreatedAt); err != nil {
				return err
			}
			out.Items = append(out.Items, e)
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

// ReportArchiveVerifiedBytes checks the stored canonical JSONB bytes before any
// typed decode or export. The digest covers the full frozen report, not a subset.
func (s *Store) ReportArchiveVerifiedBytes(ctx context.Context, ws, actor, id int64) ([]byte, ReportArchiveIntegrity, error) {
	out := ReportArchiveIntegrity{ArchiveID: id, Algorithm: "SHA-256", Canonicalization: "PostgreSQL JSONB text / UTF-8", ExternalStorageStatus: "unverified", Boundary: ReportArchiveRetentionBoundary}
	var status, report, summary string
	err := s.pool.QueryRow(ctx, `SELECT a.status,COALESCE(a.report::text,''),COALESCE(a.summary::text,''),COALESCE(a.content_digest,''),COALESCE(a.summary_digest,''),COALESCE(a.content_bytes,0) FROM plume.automation_report_archives a JOIN plume.automation_runs r ON r.id=a.automation_run_id JOIN plume.automation_jobs j ON j.id=r.job_id JOIN plume.memberships m ON m.workspace_id=j.workspace_id AND m.user_id=$2 WHERE j.workspace_id=$1 AND a.automation_run_id=$3`, ws, actor, id).Scan(&status, &report, &summary, &out.ContentDigest, &out.SummaryDigest, &out.ContentBytes)
	if err != nil {
		return nil, out, storeError(err)
	}
	if status != "ready" {
		return nil, out, ErrReportArchiveFailed
	}
	h := sha256.Sum256([]byte(report))
	sHash := sha256.Sum256([]byte(summary))
	if hex.EncodeToString(h[:]) != out.ContentDigest || hex.EncodeToString(sHash[:]) != out.SummaryDigest || int64(len([]byte(report))) != out.ContentBytes {
		return nil, out, ErrReportArchiveIntegrity
	}
	out.IntegrityStatus = "verified"
	return []byte(report), out, nil
}

func (s *Store) RecordReportArchiveExport(ctx context.Context, ws, actor, id int64, archiveDigest, format, exportDigest string, n int64) (int64, error) {
	if !digestPattern.MatchString(archiveDigest) || !digestPattern.MatchString(exportDigest) || !slices.Contains([]string{"html", "json"}, format) || n < 1 {
		return 0, ErrInvalid
	}
	var receipt int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, ws, actor, false); err != nil {
			return err
		}
		var digest string
		if err := tx.QueryRow(ctx, `SELECT a.content_digest FROM plume.automation_report_archives a JOIN plume.automation_runs r ON r.id=a.automation_run_id JOIN plume.automation_jobs j ON j.id=r.job_id WHERE j.workspace_id=$1 AND a.automation_run_id=$2 AND a.status='ready' FOR SHARE OF a`, ws, id).Scan(&digest); err != nil {
			return err
		}
		if digest != archiveDigest {
			return ErrReportArchiveIntegrity
		}
		snapshot := map[string]any{"archiveDigest": archiveDigest, "format": format, "exportDigest": exportDigest, "bytes": n, "deliveryStatus": "prepared", "externalStorageStatus": "unverified"}
		return tx.QueryRow(ctx, `INSERT INTO plume.report_archive_retention_events(workspace_id,archive_id,actor_id,action,note,snapshot)VALUES($1,$2,$3,'export_prepared','服务器已准备导出字节；未确认浏览器保存或外部存储。',$4)RETURNING id`, ws, id, actor, snapshot).Scan(&receipt)
	})
	return receipt, storeError(err)
}
