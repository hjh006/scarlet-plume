package store

import (
	"context"
	"github.com/jackc/pgx/v5"
	"time"
)

type ReportAssessmentReference struct {
	ID                  int64     `json:"id"`
	Kind                string    `json:"kind"`
	Title               string    `json:"title"`
	Status              string    `json:"status"`
	ActivityID          int64     `json:"activityId"`
	TemplateRevisionID  int64     `json:"templateRevisionId"`
	Revision            int64     `json:"revision"`
	UpdatedAt           time.Time `json:"updatedAt"`
	Stale               bool      `json:"stale"`
	ReassessmentPending bool      `json:"reassessmentPending"`
}
type ReportAssessmentReferences struct {
	Items     []ReportAssessmentReference `json:"items"`
	Truncated bool                        `json:"truncated"`
	Basis     string                      `json:"basis"`
}

func readReportAssessmentReferences(ctx context.Context, tx pgx.Tx, ws int64, report *ComplianceReport) error {
	ids := []int64{}
	seen := map[int64]bool{}
	if report.Scope.ActivityID != 0 {
		ids = append(ids, report.Scope.ActivityID)
		seen[report.Scope.ActivityID] = true
	}
	for _, entry := range report.Checks {
		if entry.Check.Activity != nil && !seen[entry.Check.Activity.ID] {
			id := entry.Check.Activity.ID
			ids = append(ids, id)
			seen[id] = true
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, e := tx.Query(ctx, `SELECT id,kind,title,status,activity_id,template_revision_id,revision,updated_at,stale,reassessment_pending FROM(SELECT DISTINCT ON(activity_id,kind) id,kind,title,status,activity_id,template_revision_id,revision,updated_at,stale,reassessment_pending FROM plume.assessments WHERE workspace_id=$1 AND activity_id=ANY($2) ORDER BY activity_id,kind,id DESC) recent ORDER BY activity_id,kind LIMIT 201`, ws, ids)
	if e != nil {
		return e
	}
	items, e := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ReportAssessmentReference, error) {
		var r ReportAssessmentReference
		e := row.Scan(&r.ID, &r.Kind, &r.Title, &r.Status, &r.ActivityID, &r.TemplateRevisionID, &r.Revision, &r.UpdatedAt, &r.Stale, &r.ReassessmentPending)
		return r, e
	})
	if e != nil {
		return e
	}
	if len(items) == 0 {
		return nil
	}
	result := &ReportAssessmentReferences{Items: items, Basis: "仅冻结本报告关联活动各类型的最近专项评估引用与记录状态；采用模板版本及完整审核依据见该专项报告。此处不替代适用性复评或检查独立复核。"}
	if len(items) > 200 {
		result.Items = items[:200]
		result.Truncated = true
	}
	report.AssessmentReferences = result
	return nil
}
