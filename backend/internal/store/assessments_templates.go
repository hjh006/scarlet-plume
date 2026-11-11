package store

import (
	"context"
	"net/url"
	"slices"

	"github.com/jackc/pgx/v5"
)

func validAssessmentKind(kind string) bool {
	return slices.Contains([]string{"pia", "cross_border", "ai", "business_change"}, kind)
}
func assessmentCheckKind(kind string) string {
	if kind == "ai" || kind == "cross_border" {
		return kind
	}
	return "sharing"
}
func validateAssessmentTemplate(in AssessmentTemplateInput) bool {
	if !templateKeyPattern.MatchString(in.Key) || !validCheckText(in.Title, 100) || !validAssessmentKind(in.Kind) || len(in.Questions) == 0 || len(in.Questions) > 25 {
		return false
	}
	seen := map[string]bool{}
	for _, q := range in.Questions {
		if !templateKeyPattern.MatchString(q.Key) || seen[q.Key] || !validCheckText(q.Title, 100) || !validCheckText(q.Basis, 400) || !validCheckText(q.Applicability, 1000) || !validCheckText(q.Evidence, 1000) || !validCheckText(q.Acceptance, 1000) || !slices.Contains([]string{"conditional_law", "recommended"}, q.Obligation) || !slices.Contains([]string{"single", "multi", "text", "number", "evidence"}, q.Kind) {
			return false
		}
		source, err := url.Parse(q.BasisURL)
		if err != nil || source.Scheme != "https" || source.Hostname() == "" || source.User != nil {
			return false
		}
		if q.Kind == "single" || q.Kind == "multi" {
			if len(q.Options) < 2 || len(q.Options) > 20 {
				return false
			}
			options := map[string]bool{}
			for _, option := range q.Options {
				if !validCheckText(option, 80) || options[option] {
					return false
				}
				options[option] = true
			}
		}
		seen[q.Key] = true
	}
	return true
}

const assessmentTemplateSelect = `SELECT t.id,t.template_key,t.title,r.kind,t.version,t.workspace_id IS NULL,r.questions,r.disabled_at IS NOT NULL FROM plume.check_templates t JOIN plume.assessment_template_revisions r ON r.revision_id=t.id`

func scanAssessmentTemplate(row pgx.Row) (AssessmentTemplate, error) {
	var t AssessmentTemplate
	var disabled bool
	err := row.Scan(&t.ID, &t.Key, &t.Title, &t.Kind, &t.Revision, &t.BuiltIn, &t.Questions, &disabled)
	t.RevisionID = &t.ID
	t.State = "published"
	if disabled {
		t.State = "disabled"
	}
	return t, storeError(err)
}
func readAssessmentTemplate(ctx context.Context, tx pgx.Tx, workspaceID, id int64) (AssessmentTemplate, error) {
	return scanAssessmentTemplate(tx.QueryRow(ctx, assessmentTemplateSelect+` WHERE t.id=$2 AND (t.workspace_id IS NULL OR t.workspace_id=$1)`, workspaceID, id))
}
func (s *Store) ListAssessmentTemplates(ctx context.Context, workspaceID, actorID int64) ([]AssessmentTemplate, error) {
	result := []AssessmentTemplate{}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, assessmentTemplateSelect+` WHERE (t.workspace_id IS NULL OR t.workspace_id=$1) AND t.id IN(SELECT DISTINCT ON(workspace_id,template_key) id FROM plume.check_templates ORDER BY workspace_id,template_key,version DESC) ORDER BY r.kind,t.id`, workspaceID)
		if err != nil {
			return err
		}
		result, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (AssessmentTemplate, error) { return scanAssessmentTemplate(row) })
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT id,template_key,title,kind,revision,questions,state,published_revision_id FROM plume.assessment_template_drafts WHERE workspace_id=$1 AND state='draft' ORDER BY id`, workspaceID)
		if err != nil {
			return err
		}
		drafts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AssessmentTemplate, error) {
			var t AssessmentTemplate
			err := row.Scan(&t.ID, &t.Key, &t.Title, &t.Kind, &t.Revision, &t.Questions, &t.State, &t.RevisionID)
			return t, err
		})
		result = append(result, drafts...)
		return err
	})
	if result == nil {
		result = []AssessmentTemplate{}
	}
	return result, storeError(err)
}
func (s *Store) SaveAssessmentTemplate(ctx context.Context, workspaceID, actorID, id int64, in AssessmentTemplateInput) (AssessmentTemplate, error) {
	var result AssessmentTemplate
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if role != "owner" {
			return ErrForbidden
		}
		if in.BaseRevisionID != nil {
			original, err := readAssessmentTemplate(ctx, tx, workspaceID, *in.BaseRevisionID)
			if err != nil {
				return err
			}
			if len(in.Questions) == 0 {
				in.Questions = original.Questions
			}
			if in.Kind == "" {
				in.Kind = original.Kind
			}
		}
		if !validateAssessmentTemplate(in) {
			return ErrInvalid
		}
		for _, q := range in.Questions {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.requirements WHERE code=$1)`, q.RequirementCode).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrInvalid
			}
		}
		if id == 0 {
			err = tx.QueryRow(ctx, `INSERT INTO plume.assessment_template_drafts(workspace_id,template_key,title,kind,created_by,base_revision_id,questions)VALUES($1,$2,$3,$4,$5,$6,$7)
 ON CONFLICT(workspace_id,template_key) DO UPDATE SET title=excluded.title,questions=excluded.questions,base_revision_id=excluded.base_revision_id,state='draft',revision=assessment_template_drafts.revision+1,updated_at=clock_timestamp()
 WHERE assessment_template_drafts.state='published' AND assessment_template_drafts.published_revision_id=excluded.base_revision_id AND assessment_template_drafts.kind=excluded.kind
 RETURNING id`, workspaceID, in.Key, in.Title, in.Kind, actorID, in.BaseRevisionID, in.Questions).Scan(&id)
			if err == pgx.ErrNoRows {
				return ErrConflict
			}
		} else {
			tag, e := tx.Exec(ctx, `UPDATE plume.assessment_template_drafts SET title=$4,questions=$5,revision=revision+1,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 AND revision=$3 AND state='draft' AND template_key=$6 AND kind=$7`, workspaceID, id, in.Revision, in.Title, in.Questions, in.Key, in.Kind)
			err = e
			if err == nil && tag.RowsAffected() == 0 {
				return ErrConflict
			}
		}
		if err != nil {
			return storeError(err)
		}
		return tx.QueryRow(ctx, `SELECT id,template_key,title,kind,revision,questions,state FROM plume.assessment_template_drafts WHERE workspace_id=$1 AND id=$2`, workspaceID, id).Scan(&result.ID, &result.Key, &result.Title, &result.Kind, &result.Revision, &result.Questions, &result.State)
	})
	return result, storeError(err)
}
func (s *Store) PublishAssessmentTemplate(ctx context.Context, workspaceID, actorID, id, revision int64) (AssessmentTemplate, error) {
	var result AssessmentTemplate
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if role != "owner" {
			return ErrForbidden
		}
		var draft AssessmentTemplate
		err = tx.QueryRow(ctx, `SELECT id,template_key,title,kind,revision,questions,state FROM plume.assessment_template_drafts WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspaceID, id).Scan(&draft.ID, &draft.Key, &draft.Title, &draft.Kind, &draft.Revision, &draft.Questions, &draft.State)
		if err != nil {
			return storeError(err)
		}
		if draft.State != "draft" || draft.Revision != revision {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('assessment-template:'||$1::bigint::text||':'||$2,0))`, workspaceID, draft.Key); err != nil {
			return err
		}
		var reserved bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.check_templates WHERE workspace_id IS NULL AND template_key=$1)`, draft.Key).Scan(&reserved); err != nil {
			return err
		}
		if reserved {
			return ErrConflict
		}
		var next int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(version),0)+1 FROM plume.check_templates WHERE workspace_id=$1 AND template_key=$2`, workspaceID, draft.Key).Scan(&next); err != nil {
			return err
		}
		var published int64
		if err := tx.QueryRow(ctx, `INSERT INTO plume.check_templates(workspace_id,template_key,title,kind,version,created_by)VALUES($1,$2,$3,$4,$5,$6)RETURNING id`, workspaceID, draft.Key, draft.Title, assessmentCheckKind(draft.Kind), next, actorID).Scan(&published); err != nil {
			return storeError(err)
		}
		for i, q := range draft.Questions {
			_, err := tx.Exec(ctx, `INSERT INTO plume.check_template_items(template_revision_id,item_key,position,title,requirement_code,applicability,evidence_requirements,acceptance_criteria)VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, published, q.Key, i+1, q.Title, q.RequirementCode, q.Applicability, q.Evidence, q.Acceptance)
			if err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO plume.assessment_template_revisions(revision_id,workspace_id,kind,questions)VALUES($1,$2,$3,$4)`, published, workspaceID, draft.Kind, draft.Questions); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE plume.assessment_template_drafts SET state='published',published_revision_id=$3,revision=revision+1,updated_at=clock_timestamp()WHERE workspace_id=$1 AND id=$2`, workspaceID, id, published); err != nil {
			return err
		}
		result, err = readAssessmentTemplate(ctx, tx, workspaceID, published)
		return err
	})
	return result, storeError(err)
}
func (s *Store) DisableAssessmentTemplate(ctx context.Context, workspaceID, actorID, revisionID int64) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if role != "owner" {
			return ErrForbidden
		}
		tag, err := tx.Exec(ctx, `UPDATE plume.assessment_template_revisions SET disabled_at=COALESCE(disabled_at,clock_timestamp()) WHERE workspace_id=$1 AND revision_id=$2`, workspaceID, revisionID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}
