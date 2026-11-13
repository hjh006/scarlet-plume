package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type assessmentRecord struct {
	AssessmentDetail
	ActivityDigest string
	FactsDigest    string
	EditorIDs      []int64
	CheckIDs       []int64
	Frozen         json.RawMessage
}

const assessmentSelect = `SELECT a.id,a.kind,a.title,a.status,a.activity_id,a.template_revision_id,a.creator_id,a.updated_at,a.due_at,a.stale,
 a.revision,u.username,COALESCE(v.username,''),a.review_note,a.activity_snapshot,a.facts,a.answers,a.reassessment_pending,a.facts_refreshed_at,a.refresh_minutes,
 a.activity_digest,a.facts_digest,a.editor_ids,a.check_ids,a.frozen_report
 FROM plume.assessments a JOIN plume.users u ON u.id=a.creator_id LEFT JOIN plume.users v ON v.id=a.reviewer_id`

func scanAssessment(row pgx.Row) (assessmentRecord, error) {
	var r assessmentRecord
	err := row.Scan(&r.ID, &r.Kind, &r.Title, &r.Status, &r.ActivityID, &r.TemplateRevisionID, &r.CreatorID, &r.UpdatedAt, &r.DueAt, &r.Stale,
		&r.Revision, &r.Creator, &r.Reviewer, &r.ReviewNote, &r.Activity, &r.Facts, &r.Answers, &r.ReassessmentPending, &r.FactsRefreshedAt, &r.RefreshMinutes,
		&r.ActivityDigest, &r.FactsDigest, &r.EditorIDs, &r.CheckIDs, &r.Frozen)
	return r, storeError(err)
}
func readAssessmentRecord(ctx context.Context, tx pgx.Tx, workspaceID, id int64, lock bool) (assessmentRecord, error) {
	query := assessmentSelect + ` WHERE a.workspace_id=$1 AND a.id=$2`
	if lock {
		query += " FOR UPDATE OF a"
	}
	return scanAssessment(tx.QueryRow(ctx, query, workspaceID, id))
}
func assessmentFactsDigest(facts []AssessmentFact) string {
	values := make([][4]string, 0, len(facts))
	for _, f := range facts {
		values = append(values, [4]string{f.Key, f.State, f.Value, f.Source})
	}
	return assessmentDigest(values)
}
func readAssessmentDetail(ctx context.Context, tx pgx.Tx, workspaceID, actorID int64, r assessmentRecord, role string) (AssessmentDetail, error) {
	detail := r.AssessmentDetail
	var err error
	detail.Template, err = readAssessmentTemplate(ctx, tx, workspaceID, detail.TemplateRevisionID)
	if err != nil {
		return detail, err
	}
	actual, err := scanActivity(tx.QueryRow(ctx, activitySelect+` WHERE p.workspace_id=$1 AND p.id=$2`, workspaceID, detail.ActivityID))
	if err != nil {
		return detail, err
	}
	detail.Stale = detail.Stale || assessmentActivityDigest(actual) != r.ActivityDigest
	detail.Checks = []AssessmentCheck{}
	rows, err := tx.Query(ctx, `SELECT id,requirement_code,status,owner_team FROM plume.checks WHERE workspace_id=$1 AND id=ANY($2::bigint[]) ORDER BY id`, workspaceID, r.CheckIDs)
	if err != nil {
		return detail, err
	}
	detail.Checks, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (AssessmentCheck, error) {
		var c AssessmentCheck
		err := row.Scan(&c.ID, &c.RequirementCode, &c.Status, &c.OwnerTeam)
		return c, err
	})
	if err != nil {
		return detail, err
	}
	selectedEvidence := []int64{}
	for _, answer := range detail.Answers {
		selectedEvidence = append(selectedEvidence, answer.EvidenceIDs...)
	}
	rows, err = tx.Query(ctx, `SELECT e.id,c.id,e.kind,e.source_system,e.source_record,e.summary,e.observed_at FROM plume.check_evidence e JOIN plume.checks c ON c.id=e.check_id
 WHERE c.workspace_id=$1 AND c.asset_id=$2 AND (c.activity_id=$3 OR c.activity_id IS NULL) ORDER BY (e.id=ANY($4::bigint[])) DESC,e.id DESC LIMIT 201`, workspaceID, detail.Activity.AssetID, detail.ActivityID, selectedEvidence)
	if err != nil {
		return detail, err
	}
	detail.Evidence, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (AssessmentEvidence, error) {
		var e AssessmentEvidence
		err := row.Scan(&e.ID, &e.CheckID, &e.Kind, &e.Source, &e.Record, &e.Summary, &e.ObservedAt)
		return e, err
	})
	if err != nil {
		return detail, err
	}
	if len(detail.Evidence) > 200 {
		detail.Evidence = detail.Evidence[:200]
		detail.EvidenceTruncated = true
	}
	rows, err = tx.Query(ctx, `SELECT e.id,e.action,COALESCE(u.username,'system'),e.note,e.created_at FROM plume.assessment_events e LEFT JOIN plume.users u ON u.id=e.actor_id WHERE e.assessment_id=$1 ORDER BY e.id DESC LIMIT 100`, detail.ID)
	if err != nil {
		return detail, err
	}
	detail.Events, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (AssessmentEvent, error) {
		var e AssessmentEvent
		err := row.Scan(&e.ID, &e.Action, &e.Actor, &e.Note, &e.CreatedAt)
		return e, err
	})
	if err != nil {
		return detail, err
	}
	detail.AutomaticApplicability = automaticAssessmentApplicability(detail.Kind, detail.Activity)
	detail.Gaps, detail.Risks, detail.Applicability = evaluateAssessment(detail)
	detail.CanEdit = checkEditor(role) && (detail.Status == "draft" || detail.Status == "needs_work")
	detail.CanReview = (role == "owner" || role == "reviewer") && detail.Status == "submitted" && actorID != detail.CreatorID && !slices.Contains(r.EditorIDs, actorID)
	if detail.CanReview {
		var contributed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.check_evidence e JOIN plume.checks c ON c.id=e.check_id WHERE c.workspace_id=$1 AND e.created_by=$2 AND (e.id=ANY($3::bigint[]) OR c.id=ANY($4::bigint[])))`, workspaceID, actorID, selectedEvidence, r.CheckIDs).Scan(&contributed); err != nil {
			return detail, err
		}
		detail.CanReview = !contributed
	}
	detail.CanApprove = detail.CanReview && !detail.Stale && len(detail.Gaps) == 0 && len(detail.Risks) == 0
	detail.Basis = "报告采用所列模板版本、活动快照、答题及来源证据。适用性和法规义务按实际业务条件核验，组织治理建议在题目中单独标记。"
	return detail, nil
}
func assessmentReportSnapshot(detail AssessmentDetail) AssessmentDetail {
	selected := map[int64]bool{}
	for _, answer := range detail.Answers {
		for _, id := range answer.EvidenceIDs {
			selected[id] = true
		}
	}
	evidence := []AssessmentEvidence{}
	for _, record := range detail.Evidence {
		if selected[record.ID] {
			evidence = append(evidence, record)
		}
	}
	detail.Evidence = evidence
	detail.EvidenceTruncated = false
	detail.CanEdit = false
	detail.CanReview = false
	detail.CanApprove = false
	return detail
}
func automaticAssessmentApplicability(kind string, activity Activity) AssessmentApplicability {
	result := AssessmentApplicability{State: "unknown", Reason: "现有采集无法确认全部业务与法律触发条件，需取证核验。", Source: "business_fact"}
	if kind == "ai" && activity.Kind == "ai" || kind == "cross_border" && activity.Kind == "cross_border" {
		result = AssessmentApplicability{State: "applicable", Reason: "登记的处理活动类型符合该专项评估范围；具体法定义务仍按各题条件核验。", Source: "activity", Record: fmt.Sprint(activity.ID)}
	}
	return result
}
func initialAssessmentAnswers(t AssessmentTemplate, activity Activity, facts []AssessmentFact) []AssessmentAnswer {
	answers := []AssessmentAnswer{}
	for _, q := range t.Questions {
		if q.Key == "applicable" && q.Kind == "single" && slices.Contains(q.Options, "yes") && automaticAssessmentApplicability(t.Kind, activity).State == "applicable" {
			answers = append(answers, AssessmentAnswer{Key: q.Key, Value: "yes", Note: "根据登记活动类型自动填入，范围见来源事实。"})
			continue
		}
		if q.Kind != "text" || q.FactKey == "" {
			continue
		}
		for _, f := range facts {
			if f.Key == q.FactKey && f.State == "known" {
				answers = append(answers, AssessmentAnswer{Key: q.Key, Value: f.Value, Note: "自动采用来源事实：" + f.Source + " / " + f.Record})
				break
			}
		}
	}
	return answers
}
func evaluateAssessment(d AssessmentDetail) ([]AssessmentGap, []AssessmentGap, string) {
	gaps, risks := []AssessmentGap{}, []AssessmentGap{}
	machineApplicable := (d.Kind == "ai" && d.Activity.Kind == "ai") || (d.Kind == "cross_border" && d.Activity.Kind == "cross_border")
	answers := map[string]AssessmentAnswer{}
	for _, a := range d.Answers {
		answers[a.Key] = a
	}
	applicable := "unknown"
	gate := answers["applicable"]
	if gate.Value == "yes" {
		applicable = "applicable"
	}
	if gate.Value == "no" && strings.TrimSpace(gate.Note) != "" {
		applicable = "not_applicable"
		if machineApplicable {
			risks = append(risks, AssessmentGap{Key: "applicable", Title: "适用性与登记事实冲突", Reason: "活动类型与不适用答复不一致，需要先纠正活动范围或核验结论。"})
		}
	}
	if gate.Value == "no" && strings.TrimSpace(gate.Note) == "" {
		gaps = append(gaps, AssessmentGap{Key: "applicable", Title: "适用性依据", Reason: "不适用结论需要填写具体理由和证据引用。"})
	}
	for _, q := range d.Template.Questions {
		if !q.Required {
			continue
		}
		if applicable == "not_applicable" && q.Key != "applicable" && q.Kind != "evidence" {
			continue
		}
		a, exists := answers[q.Key]
		missing := !exists
		switch q.Kind {
		case "single":
			missing = missing || a.Value == "" || a.Value == "unknown"
		case "multi":
			missing = missing || len(a.Values) == 0 || slices.Contains(a.Values, "unknown")
		case "text":
			missing = missing || strings.TrimSpace(a.Value) == ""
		case "number":
			missing = missing || a.Number == nil
		case "evidence":
			missing = missing || len(a.EvidenceIDs) == 0
		}
		if missing {
			gaps = append(gaps, AssessmentGap{Key: q.Key, Title: q.Title, Reason: "缺少经核验的业务答复或取证记录。"})
		}
	}
	for _, c := range d.Checks {
		if c.Status != "verified" {
			risks = append(risks, AssessmentGap{Key: fmt.Sprint(c.ID), Title: "关联整改检查", Reason: "检查尚未完成独立复核。"})
		}
	}
	return gaps, risks, applicable
}
func (s *Store) ListAssessments(ctx context.Context, workspaceID, actorID int64, kind, status string) ([]Assessment, error) {
	return s.ListScopedAssessments(ctx, workspaceID, actorID, kind, status, 0, 0)
}
func (s *Store) ListScopedAssessments(ctx context.Context, workspaceID, actorID int64, kind, status string, assetID, activityID int64) ([]Assessment, error) {
	if assetID < 0 || activityID < 0 {
		return nil, ErrInvalid
	}
	if kind != "" && !validAssessmentKind(kind) {
		return nil, ErrInvalid
	}
	if status != "" && !slices.Contains([]string{"draft", "submitted", "needs_work", "approved"}, status) {
		return nil, ErrInvalid
	}
	result := []Assessment{}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		var assetExists bool
		var activityAssetID int64
		if err := tx.QueryRow(ctx, `SELECT ($2=0::bigint OR EXISTS(SELECT 1 FROM plume.assets WHERE workspace_id=$1 AND id=$2)), COALESCE((SELECT asset_id FROM plume.activities WHERE workspace_id=$1 AND id=$3),0)`, workspaceID, assetID, activityID).Scan(&assetExists, &activityAssetID); err != nil {
			return err
		}
		if !assetExists || activityID > 0 && activityAssetID == 0 {
			return ErrNotFound
		}
		if assetID > 0 && activityID > 0 && activityAssetID != assetID {
			return ErrInvalid
		}
		rows, err := tx.Query(ctx, assessmentSelect+` WHERE a.workspace_id=$1 AND ($2='' OR a.kind=$2) AND ($3='' OR a.status=$3) AND ($4=0::bigint OR a.activity_id=$4) AND ($5=0::bigint OR EXISTS(SELECT 1 FROM plume.activities p WHERE p.workspace_id=a.workspace_id AND p.id=a.activity_id AND p.asset_id=$5)) ORDER BY a.id DESC LIMIT 100`, workspaceID, kind, status, activityID, assetID)
		if err != nil {
			return err
		}
		for rows.Next() {
			r, err := scanAssessment(rows)
			if err != nil {
				rows.Close()
				return err
			}
			result = append(result, r.Assessment)
		}
		err = rows.Err()
		rows.Close()
		return err
	})
	return result, storeError(err)
}
func (s *Store) Assessment(ctx context.Context, workspaceID, actorID, id int64) (AssessmentDetail, error) {
	var result AssessmentDetail
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, false)
		if err != nil {
			return err
		}
		r, err := readAssessmentRecord(ctx, tx, workspaceID, id, false)
		if err != nil {
			return err
		}
		result, err = readAssessmentDetail(ctx, tx, workspaceID, actorID, r, role)
		return err
	})
	return result, storeError(err)
}
func (s *Store) CreateAssessment(ctx context.Context, workspaceID, actorID int64, in AssessmentInput) (AssessmentDetail, error) {
	if in.ActivityID < 1 || in.TemplateRevisionID < 1 || !validCheckText(in.Title, 100) || in.DueAt.IsZero() || in.DueAt.Before(time.Now().Add(-time.Minute)) {
		return AssessmentDetail{}, ErrInvalid
	}
	if in.RefreshMinutes == 0 {
		in.RefreshMinutes = 1440
	}
	if in.RefreshMinutes < 5 || in.RefreshMinutes > 43200 {
		return AssessmentDetail{}, ErrInvalid
	}
	var id int64
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		template, err := readAssessmentTemplate(ctx, tx, workspaceID, in.TemplateRevisionID)
		if err != nil {
			return err
		}
		if template.State != "published" {
			return ErrInvalid
		}
		if err := lockAssessmentActivityCreation(ctx, tx, workspaceID, in.ActivityID); err != nil {
			return err
		}
		activity, err := scanActivity(tx.QueryRow(ctx, activitySelect+` WHERE p.workspace_id=$1 AND p.id=$2 FOR SHARE OF p`, workspaceID, in.ActivityID))
		if err != nil {
			return err
		}
		facts, err := assessmentFacts(ctx, tx, workspaceID, activity)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `INSERT INTO plume.assessments(workspace_id,activity_id,template_revision_id,creator_id,kind,title,activity_snapshot,activity_digest,facts,facts_digest,answers,editor_ids,due_at,refresh_minutes,next_refresh_at)
  VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$13,ARRAY[$4::bigint],$11,$12,clock_timestamp()+make_interval(mins=>$12)) RETURNING id`, workspaceID, in.ActivityID, in.TemplateRevisionID, actorID, template.Kind, in.Title, activity, assessmentActivityDigest(activity), facts, assessmentFactsDigest(facts), in.DueAt, in.RefreshMinutes, initialAssessmentAnswers(template, activity, facts)).Scan(&id)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO plume.assessment_events(assessment_id,actor_id,action)VALUES($1,$2,'created')`, id, actorID)
		return err
	})
	if err != nil {
		return AssessmentDetail{}, storeError(err)
	}
	return s.Assessment(ctx, workspaceID, actorID, id)
}
func validateAssessmentAnswers(template AssessmentTemplate, answers []AssessmentAnswer) bool {
	if len(answers) > len(template.Questions) {
		return false
	}
	questions := map[string]AssessmentQuestion{}
	for _, q := range template.Questions {
		questions[q.Key] = q
	}
	seen := map[string]bool{}
	evidenceRefs := map[int64]bool{}
	for _, a := range answers {
		q, exists := questions[a.Key]
		if !exists || seen[a.Key] || !validOptionalActivityText(a.Note, 1000) || len(a.EvidenceIDs) > 25 {
			return false
		}
		seen[a.Key] = true
		switch q.Kind {
		case "single":
			if a.Value != "" && !slices.Contains(q.Options, a.Value) {
				return false
			}
			if len(a.Values) > 0 || a.Number != nil || len(a.EvidenceIDs) > 0 {
				return false
			}
		case "multi":
			if a.Value != "" || a.Number != nil || len(a.EvidenceIDs) > 0 || len(a.Values) > len(q.Options) {
				return false
			}
			opts := map[string]bool{}
			for _, v := range a.Values {
				if !slices.Contains(q.Options, v) || opts[v] {
					return false
				}
				opts[v] = true
			}
		case "text":
			if !validOptionalActivityText(a.Value, 2000) || a.Number != nil || len(a.Values) > 0 || len(a.EvidenceIDs) > 0 {
				return false
			}
		case "number":
			if a.Value != "" || len(a.Values) > 0 || len(a.EvidenceIDs) > 0 {
				return false
			}
			if a.Number != nil && (math.IsNaN(*a.Number) || math.IsInf(*a.Number, 0) || *a.Number < 0 || *a.Number > 1e15 || q.Key == "subject_count" && math.Trunc(*a.Number) != *a.Number) {
				return false
			}
		case "evidence":
			if a.Value != "" || a.Number != nil || len(a.Values) > 0 {
				return false
			}
			ids := map[int64]bool{}
			for _, id := range a.EvidenceIDs {
				if id < 1 || ids[id] {
					return false
				}
				ids[id] = true
				evidenceRefs[id] = true
				if len(evidenceRefs) > 100 {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

func refreshAssessmentFacts(ctx context.Context, tx pgx.Tx, workspaceID, actorID, id int64, acknowledge bool) error {
	r, err := readAssessmentRecord(ctx, tx, workspaceID, id, true)
	if err != nil {
		return err
	}
	activity, err := scanActivity(tx.QueryRow(ctx, activitySelect+` WHERE p.workspace_id=$1 AND p.id=$2 FOR SHARE OF p`, workspaceID, r.ActivityID))
	if err != nil {
		return err
	}
	facts, err := assessmentFacts(ctx, tx, workspaceID, activity)
	if err != nil {
		return err
	}
	activityChanged := assessmentActivityDigest(activity) != r.ActivityDigest
	factsChanged := assessmentFactsDigest(facts) != r.FactsDigest
	oldStale := r.Stale
	editable := r.Status == "draft" || r.Status == "needs_work"
	if acknowledge && !editable {
		return ErrConflict
	}
	if editable && (acknowledge || !activityChanged) {
		r.Facts = facts
		r.FactsDigest = assessmentFactsDigest(facts)
		if acknowledge {
			r.Activity = activity
			r.ActivityDigest = assessmentActivityDigest(activity)
			r.Stale = false
			r.ReassessmentPending = false
			if activityChanged {
				r.Answers = []AssessmentAnswer{}
				r.CheckIDs = []int64{}
			}
		}
	} else if activityChanged || factsChanged {
		r.Stale = true
		r.ReassessmentPending = true
	}
	if r.Stale && !oldStale {
		if _, err := tx.Exec(ctx, `INSERT INTO plume.assessment_reassessment_tasks(assessment_id,reason)VALUES($1,'facts_or_activity_changed')ON CONFLICT(assessment_id)DO UPDATE SET state='pending',reason=excluded.reason,resolved_at=NULL`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO plume.assessment_events(assessment_id,actor_id,action,note)VALUES($1,NULLIF($2::bigint,0),'stale','facts_or_activity_changed')`, id, actorID); err != nil {
			return err
		}
	}
	if acknowledge {
		if !slices.Contains(r.EditorIDs, actorID) {
			r.EditorIDs = append(r.EditorIDs, actorID)
		}
		if _, err := tx.Exec(ctx, `UPDATE plume.assessment_reassessment_tasks SET state='resolved',resolved_at=clock_timestamp()WHERE assessment_id=$1`, id); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE plume.assessments SET activity_snapshot=$3,activity_digest=$4,facts=$5,facts_digest=$6,answers=$7,check_ids=$8,editor_ids=$12,
 stale=$9,reassessment_pending=$10,revision=revision+CASE WHEN $11 THEN 1 ELSE 0 END,facts_refreshed_at=clock_timestamp(),next_refresh_at=clock_timestamp()+make_interval(mins=>refresh_minutes),updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, workspaceID, id, r.Activity, r.ActivityDigest, r.Facts, r.FactsDigest, r.Answers, r.CheckIDs, r.Stale, r.ReassessmentPending, activityChanged || factsChanged || acknowledge, r.EditorIDs)
	return err
}
func (s *Store) RefreshAssessment(ctx context.Context, workspaceID, actorID, id int64) (AssessmentDetail, error) {
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		return refreshAssessmentFacts(ctx, tx, workspaceID, actorID, id, true)
	})
	if err != nil {
		return AssessmentDetail{}, storeError(err)
	}
	return s.Assessment(ctx, workspaceID, actorID, id)
}
func (s *Store) SaveAssessmentAnswers(ctx context.Context, workspaceID, actorID, id int64, in AssessmentAnswersInput) (AssessmentDetail, error) {
	if len(in.CheckIDs) > 25 {
		return AssessmentDetail{}, ErrInvalid
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		r, err := readAssessmentRecord(ctx, tx, workspaceID, id, true)
		if err != nil {
			return err
		}
		if r.Revision != in.Revision || r.Status != "draft" && r.Status != "needs_work" || r.Stale {
			return ErrConflict
		}
		activity, err := scanActivity(tx.QueryRow(ctx, activitySelect+` WHERE p.workspace_id=$1 AND p.id=$2 FOR SHARE OF p`, workspaceID, r.ActivityID))
		if err != nil {
			return err
		}
		if assessmentActivityDigest(activity) != r.ActivityDigest {
			return ErrConflict
		}
		template, err := readAssessmentTemplate(ctx, tx, workspaceID, r.TemplateRevisionID)
		if err != nil {
			return err
		}
		if !validateAssessmentAnswers(template, in.Answers) {
			return ErrInvalid
		}
		for _, answer := range in.Answers {
			for _, evidenceID := range answer.EvidenceIDs {
				var exists bool
				err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.check_evidence e JOIN plume.checks c ON c.id=e.check_id WHERE e.id=$1 AND c.workspace_id=$2 AND c.asset_id=$3 AND (c.activity_id IS NULL OR c.activity_id=$4))`, evidenceID, workspaceID, activity.AssetID, activity.ID).Scan(&exists)
				if err != nil {
					return err
				}
				if !exists {
					return ErrNotFound
				}
			}
		}
		checks := map[int64]bool{}
		for _, checkID := range in.CheckIDs {
			if checkID < 1 || checks[checkID] {
				return ErrInvalid
			}
			checks[checkID] = true
			var exists bool
			err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.checks WHERE id=$1 AND workspace_id=$2 AND asset_id=$3 AND (activity_id IS NULL OR activity_id=$4))`, checkID, workspaceID, activity.AssetID, activity.ID).Scan(&exists)
			if err != nil {
				return err
			}
			if !exists {
				return ErrNotFound
			}
		}
		if in.CheckIDs == nil {
			in.CheckIDs = []int64{}
		}
		if in.Answers == nil {
			in.Answers = []AssessmentAnswer{}
		}
		if _, err := tx.Exec(ctx, `UPDATE plume.assessments SET answers=$3,check_ids=$4,editor_ids=CASE WHEN $5::bigint=ANY(editor_ids)THEN editor_ids ELSE array_append(editor_ids,$5::bigint)END,revision=revision+1,updated_at=clock_timestamp()WHERE workspace_id=$1 AND id=$2`, workspaceID, id, in.Answers, in.CheckIDs, actorID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO plume.assessment_events(assessment_id,actor_id,action)VALUES($1,$2,'answers_saved')`, id, actorID)
		return err
	})
	if err != nil {
		return AssessmentDetail{}, storeError(err)
	}
	return s.Assessment(ctx, workspaceID, actorID, id)
}
func (s *Store) ReviewAssessment(ctx context.Context, workspaceID, actorID, id int64, in AssessmentReviewInput) (AssessmentDetail, error) {
	if !slices.Contains([]string{"submit", "return", "approve"}, in.Action) || !validOptionalActivityText(in.Note, 1000) || in.Action != "submit" && !validCheckText(in.Note, 1000) {
		return AssessmentDetail{}, ErrInvalid
	}
	var workflowErr error
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		r, err := readAssessmentRecord(ctx, tx, workspaceID, id, true)
		if err != nil {
			return err
		}
		if r.Revision != in.Revision {
			return ErrConflict
		}
		if err := refreshAssessmentFacts(ctx, tx, workspaceID, actorID, id, false); err != nil {
			return err
		}
		r, err = readAssessmentRecord(ctx, tx, workspaceID, id, true)
		if err != nil {
			return err
		}
		d, err := readAssessmentDetail(ctx, tx, workspaceID, actorID, r, role)
		if err != nil {
			return err
		}
		action, status := in.Action, r.Status
		switch in.Action {
		case "submit":
			if !d.CanEdit {
				return ErrForbidden
			}
			if d.Stale {
				workflowErr = ErrConflict
				return nil
			}
			status = "submitted"
		case "return":
			if !d.CanReview {
				return ErrForbidden
			}
			status = "needs_work"
		case "approve":
			if !d.CanReview {
				return ErrForbidden
			}
			if !d.CanApprove {
				workflowErr = ErrConflict
				return nil
			}
			status = "approved"
		}
		reviewer := int64(0)
		if in.Action != "submit" {
			reviewer = actorID
		}
		if _, err := tx.Exec(ctx, `UPDATE plume.assessments SET status=$3,reviewer_id=NULLIF($4::bigint,0),review_note=$5,revision=revision+1,updated_at=clock_timestamp()WHERE workspace_id=$1 AND id=$2`, workspaceID, id, status, reviewer, in.Note); err != nil {
			return err
		}
		r, err = readAssessmentRecord(ctx, tx, workspaceID, id, false)
		if err != nil {
			return err
		}
		d, err = readAssessmentDetail(ctx, tx, workspaceID, actorID, r, role)
		if err != nil {
			return err
		}
		d = assessmentReportSnapshot(d)
		if _, err := tx.Exec(ctx, `INSERT INTO plume.assessment_events(assessment_id,actor_id,action,note,snapshot)VALUES($1,$2,$3,$4,$5)`, id, actorID, action, in.Note, d); err != nil {
			return err
		}
		if in.Action == "submit" || in.Action == "approve" {
			_, err = tx.Exec(ctx, `UPDATE plume.assessments SET frozen_report=$3 WHERE workspace_id=$1 AND id=$2`, workspaceID, id, d)
		}
		return err
	})
	if err != nil {
		return AssessmentDetail{}, storeError(err)
	}
	if workflowErr != nil {
		return AssessmentDetail{}, workflowErr
	}
	return s.Assessment(ctx, workspaceID, actorID, id)
}
func (s *Store) AssessmentReport(ctx context.Context, workspaceID, actorID, id int64) (AssessmentDetail, error) {
	var result AssessmentDetail
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, false)
		if err != nil {
			return err
		}
		r, err := readAssessmentRecord(ctx, tx, workspaceID, id, false)
		if err != nil {
			return err
		}
		live, err := readAssessmentDetail(ctx, tx, workspaceID, actorID, r, role)
		if err != nil {
			return err
		}
		result = live
		if (r.Status == "approved" || r.Status == "submitted") && len(r.Frozen) > 0 {
			if err := json.Unmarshal(r.Frozen, &result); err != nil {
				return err
			}
			result.Stale = live.Stale
			result.ReassessmentPending = live.ReassessmentPending
		}
		result = assessmentReportSnapshot(result)
		return nil
	})
	return result, storeError(err)
}
