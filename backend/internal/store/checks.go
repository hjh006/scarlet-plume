package store

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var (
	ErrForbidden        = errors.New("forbidden")
	ErrEvidenceRequired = errors.New("current evidence required")
	ErrInvalid          = errors.New("invalid check input")
	digestPattern       = regexp.MustCompile("^[0-9a-f]{64}$")
)

type Requirement struct {
	Code      string `json:"code"`
	Title     string `json:"title"`
	Citation  string `json:"citation"`
	SourceURL string `json:"sourceUrl"`
	Summary   string `json:"summary"`
}

type CheckInput struct {
	AssetID            int64  `json:"assetId"`
	ActivityID         *int64 `json:"activityId,omitempty"`
	RequirementCode    string `json:"requirementCode"`
	OwnerTeam          string `json:"ownerTeam"`
	Note               string `json:"note"`
	FeishuTargetDigest string `json:"feishuTargetDigest,omitempty"`
	TemplateRevisionID *int64 `json:"templateRevisionId,omitempty"`
	TemplateItemKey    string `json:"templateItemKey,omitempty"`
}

type Check struct {
	DjangoID    *int64             `json:"djangoId,omitempty"`
	ID          int64              `json:"id"`
	AssetID     int64              `json:"assetId"`
	AssetName   string             `json:"assetName"`
	AssetSystem string             `json:"assetSystem"`
	Activity    *ActivityReference `json:"activity,omitempty"`
	Template    *TemplateReference `json:"template,omitempty"`
	Requirement Requirement        `json:"requirement"`
	OwnerTeam   string             `json:"ownerTeam"`
	Note        string             `json:"note"`
	Status      string             `json:"status"`
	CreatedAt   time.Time          `json:"createdAt"`
	UpdatedAt   time.Time          `json:"updatedAt"`

	FeishuTargetDigest string `json:"feishuTargetDigest,omitempty"`
	createdBy          int64
	submittedBy        *int64
}

type CheckDetail struct {
	Check
	Evidence               []Evidence   `json:"evidence"`
	Events                 []CheckEvent `json:"events"`
	CanAddEvidence         bool         `json:"canAddEvidence"`
	CanSubmit              bool         `json:"canSubmit"`
	CanReview              bool         `json:"canReview"`
	CanVerify              bool         `json:"canVerify"`
	ReviewRevision         int64        `json:"reviewRevision,omitempty"`
	PostgresJobID          *int64       `json:"postgresJobId,omitempty"`
	AvailablePostgresJobID *int64       `json:"availablePostgresJobId,omitempty"`
}

type EvidenceInput struct {
	Kind         string             `json:"kind"`
	SourceSystem string             `json:"sourceSystem"`
	SourceRecord string             `json:"sourceRecord"`
	Summary      string             `json:"summary"`
	ObservedAt   time.Time          `json:"observedAt"`
	Feishu       *FeishuObservation `json:"-"`
}

type Evidence struct {
	ID              int64                `json:"id"`
	Kind            string               `json:"kind"`
	SourceSystem    string               `json:"sourceSystem"`
	SourceRecord    string               `json:"sourceRecord"`
	Summary         string               `json:"summary"`
	ObservedAt      time.Time            `json:"observedAt"`
	CreatedAt       time.Time            `json:"createdAt"`
	RecordedBy      string               `json:"recordedBy"`
	AutomationJobID *int64               `json:"automationJobId,omitempty"`
	AutomationRunID string               `json:"automationRunId,omitempty"`
	Feishu          *FeishuObservation   `json:"feishu,omitempty"`
	Postgres        *PostgresObservation `json:"postgres,omitempty"`
}

type FeishuObservation struct {
	TargetDigest   string `json:"targetDigest"`
	SnapshotDigest string `json:"snapshotDigest"`
	MemberPresent  bool   `json:"memberPresent"`
	Permission     string `json:"permission,omitempty"`
}

type CheckEvent struct {
	ID        int64     `json:"id"`
	Action    string    `json:"action"`
	Actor     string    `json:"actor"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"createdAt"`
}

const checkSelect = `
	SELECT c.id, c.asset_id, a.name, a.system,
	       COALESCE(p.id, 0), COALESCE(p.name, ''), COALESCE(p.kind, ''),
	       r.code, r.title, r.citation, r.source_url, r.summary,
	       c.owner_team, c.note, c.status, c.created_at, c.updated_at,
	       COALESCE(c.feishu_target_digest, ''), c.created_by, c.submitted_by, c.django_id,
	       COALESCE(t.id, 0), COALESCE(t.template_key, ''), COALESCE(t.title, ''), COALESCE(t.kind, ''),
	       COALESCE(t.version, 0), t.workspace_id IS NULL,
	       COALESCE(i.item_key, ''), COALESCE(i.title, ''), COALESCE(i.requirement_code, ''),
	       COALESCE(i.applicability, ''), COALESCE(i.evidence_requirements, ''), COALESCE(i.acceptance_criteria, '')
	FROM plume.checks c
	JOIN plume.assets a ON a.workspace_id = c.workspace_id AND a.id = c.asset_id
	JOIN plume.requirements r ON r.code = c.requirement_code
	LEFT JOIN plume.activities p ON p.workspace_id = c.workspace_id AND p.asset_id = c.asset_id AND p.id = c.activity_id
	LEFT JOIN plume.check_templates t ON t.id = c.template_revision_id AND (t.workspace_id IS NULL OR t.workspace_id = c.workspace_id)
	LEFT JOIN plume.check_template_items i ON i.template_revision_id = t.id AND i.item_key = c.template_item_key
`

func scanCheck(row pgx.Row) (Check, error) {
	var c Check
	var activity ActivityReference
	var template TemplateReference
	err := row.Scan(&c.ID, &c.AssetID, &c.AssetName, &c.AssetSystem,
		&activity.ID, &activity.Name, &activity.Kind,
		&c.Requirement.Code, &c.Requirement.Title, &c.Requirement.Citation,
		&c.Requirement.SourceURL, &c.Requirement.Summary, &c.OwnerTeam, &c.Note, &c.Status,
		&c.CreatedAt, &c.UpdatedAt, &c.FeishuTargetDigest, &c.createdBy, &c.submittedBy, &c.DjangoID,
		&template.RevisionID, &template.Key, &template.Title, &template.Kind, &template.Version, &template.BuiltIn,
		&template.Item.Key, &template.Item.Title, &template.Item.RequirementCode, &template.Item.Applicability,
		&template.Item.EvidenceRequirements, &template.Item.AcceptanceCriteria)
	if activity.ID != 0 {
		c.Activity = &activity
	}
	if template.RevisionID != 0 {
		c.Template = &template
	}
	return c, storeError(err)
}

func (s *Store) ListRequirements(ctx context.Context) ([]Requirement, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT code, title, citation, source_url, summary FROM plume.requirements ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	requirements := make([]Requirement, 0)
	for rows.Next() {
		var r Requirement
		if err := rows.Scan(&r.Code, &r.Title, &r.Citation, &r.SourceURL, &r.Summary); err != nil {
			return nil, err
		}
		requirements = append(requirements, r)
	}
	return requirements, rows.Err()
}

func (s *Store) ListChecks(ctx context.Context, workspaceID int64, status string, before int64, limit int) ([]Check, error) {
	return s.listChecks(ctx, workspaceID, status, 0, before, limit)
}

func (s *Store) ListActivityChecks(ctx context.Context, workspaceID, activityID int64, status string, before int64, limit int) ([]Check, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM plume.activities WHERE workspace_id = $1 AND id = $2)
	`, workspaceID, activityID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	return s.listChecks(ctx, workspaceID, status, activityID, before, limit)
}

func (s *Store) listChecks(ctx context.Context, workspaceID int64, status string, activityID, before int64, limit int) ([]Check, error) {
	rows, err := s.pool.Query(ctx, checkSelect+`
		WHERE c.workspace_id = $1 AND ($2 = '' OR c.status = $2)
		  AND ($3 = 0::bigint OR c.activity_id = $3) AND ($4 = 0::bigint OR c.id < $4)
		ORDER BY c.id DESC LIMIT $5`, workspaceID, status, activityID, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	checks := make([]Check, 0)
	for rows.Next() {
		check, err := scanCheck(rows)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	return checks, rows.Err()
}

func validCheckText(value string, length int) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= length && !strings.ContainsRune(value, 0)
}

func (s *Store) CreateCheck(ctx context.Context, workspaceID, actorID int64, input CheckInput) (Check, error) {
	if input.AssetID < 1 || (input.ActivityID != nil && *input.ActivityID < 1) || !validCheckText(input.RequirementCode, 12) ||
		!validCheckText(input.OwnerTeam, 80) || !validCheckText(input.Note, 1000) ||
		(input.FeishuTargetDigest != "" && (input.RequirementCode != "G03" || !digestPattern.MatchString(input.FeishuTargetDigest))) ||
		(input.TemplateRevisionID == nil && input.TemplateItemKey != "") ||
		(input.TemplateRevisionID != nil && (*input.TemplateRevisionID < 1 || input.ActivityID == nil ||
			!templateKeyPattern.MatchString(input.TemplateItemKey))) {
		return Check{}, ErrInvalid
	}

	var check Check
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		if input.TemplateRevisionID != nil {
			if err := validateCheckTemplate(ctx, tx, workspaceID, input); err != nil {
				return err
			}
		}
		var id int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO plume.checks (workspace_id, asset_id, requirement_code, owner_team, note, created_by,
				feishu_target_digest, activity_id, template_revision_id, template_item_key)
			VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9, NULLIF($10, '')) RETURNING id`,
			workspaceID, input.AssetID, input.RequirementCode, input.OwnerTeam, input.Note, actorID,
			input.FeishuTargetDigest, input.ActivityID, input.TemplateRevisionID, input.TemplateItemKey,
		).Scan(&id); err != nil {
			return storeError(err)
		}
		if err := recordCheckEvent(ctx, tx, id, actorID, "created", ""); err != nil {
			return err
		}
		check, err = scanCheck(tx.QueryRow(ctx, checkSelect+" WHERE c.workspace_id = $1 AND c.id = $2", workspaceID, id))
		return err
	})
	return check, err
}

func checkRole(ctx context.Context, tx pgx.Tx, workspaceID, actorID int64, lock bool) (string, error) {
	query := "SELECT role FROM plume.memberships WHERE workspace_id = $1 AND user_id = $2"
	if lock {
		query += " FOR SHARE"
	}
	var role string
	err := tx.QueryRow(ctx, query, workspaceID, actorID).Scan(&role)
	return role, storeError(err)
}

func checkEditor(role string) bool { return role == "owner" || role == "operator" }

func evidenceOpen(check Check) bool { return check.Status == "draft" || check.Status == "needs_work" }

func recordCheckEvent(ctx context.Context, tx pgx.Tx, checkID, actorID int64, action, note string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO plume.check_events (check_id, actor_id, action, note) VALUES ($1, $2, $3, $4)
	`, checkID, actorID, action, note)
	return err
}

func checkEdit(ctx context.Context, tx pgx.Tx, workspaceID, actorID, checkID int64) (Check, error) {
	role, err := checkRole(ctx, tx, workspaceID, actorID, true)
	if err != nil {
		return Check{}, err
	}
	check, err := scanCheck(tx.QueryRow(ctx, checkSelect+" WHERE c.workspace_id = $1 AND c.id = $2 FOR NO KEY UPDATE OF c", workspaceID, checkID))
	if err != nil {
		return Check{}, err
	}
	if !checkEditor(role) {
		return Check{}, ErrForbidden
	}
	if !evidenceOpen(check) {
		return Check{}, ErrConflict
	}
	return check, nil
}
