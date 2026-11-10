package store

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

type TemplateItem struct {
	Key                  string `json:"key"`
	Title                string `json:"title"`
	RequirementCode      string `json:"requirementCode"`
	Applicability        string `json:"applicability"`
	EvidenceRequirements string `json:"evidenceRequirements"`
	AcceptanceCriteria   string `json:"acceptanceCriteria"`
}

type TemplateInput struct {
	Key            string         `json:"key"`
	Title          string         `json:"title"`
	Kind           string         `json:"kind"`
	BaseRevisionID *int64         `json:"baseRevisionId,omitempty"`
	Items          []TemplateItem `json:"items"`
}

type Template struct {
	ID        int64          `json:"id"`
	Key       string         `json:"key"`
	Title     string         `json:"title"`
	Kind      string         `json:"kind"`
	Version   int            `json:"version"`
	BuiltIn   bool           `json:"builtIn"`
	Items     []TemplateItem `json:"items"`
	CreatedAt time.Time      `json:"createdAt"`
}

type TemplateReference struct {
	RevisionID int64        `json:"revisionId"`
	Key        string       `json:"key"`
	Title      string       `json:"title"`
	Kind       string       `json:"kind"`
	Version    int          `json:"version"`
	BuiltIn    bool         `json:"builtIn"`
	Item       TemplateItem `json:"item"`
}

var templateKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,59}$`)

func validTemplateKind(kind string) bool {
	return kind == "sharing" || kind == "cross_border" || kind == "ai"
}

func validateTemplate(input TemplateInput) bool {
	if !templateKeyPattern.MatchString(input.Key) || !validCheckText(input.Title, 100) ||
		!validTemplateKind(input.Kind) || (input.BaseRevisionID != nil && *input.BaseRevisionID < 1) ||
		len(input.Items) < 1 || len(input.Items) > 25 {
		return false
	}
	keys := make(map[string]bool, len(input.Items))
	for _, item := range input.Items {
		if !templateKeyPattern.MatchString(item.Key) || keys[item.Key] || !validCheckText(item.Title, 100) ||
			!validCheckText(item.RequirementCode, 12) || !validCheckText(item.Applicability, 1000) ||
			!validCheckText(item.EvidenceRequirements, 1000) || !validCheckText(item.AcceptanceCriteria, 1000) {
			return false
		}
		keys[item.Key] = true
	}
	return true
}

const templateColumns = `
	t.id, t.template_key, t.title, t.kind, t.version, t.workspace_id IS NULL, t.created_at,
	COALESCE((
		SELECT jsonb_agg(jsonb_build_object(
			'key', i.item_key, 'title', i.title, 'requirementCode', i.requirement_code,
			'applicability', i.applicability, 'evidenceRequirements', i.evidence_requirements,
			'acceptanceCriteria', i.acceptance_criteria
		) ORDER BY i.position)
		FROM plume.check_template_items i WHERE i.template_revision_id = t.id
	), '[]'::jsonb)
`

func scanTemplate(row pgx.Row) (Template, error) {
	var t Template
	err := row.Scan(&t.ID, &t.Key, &t.Title, &t.Kind, &t.Version, &t.BuiltIn, &t.CreatedAt, &t.Items)
	return t, storeError(err)
}

// ListTemplates returns the latest revision of each visible template.
func (s *Store) ListTemplates(ctx context.Context, workspaceID int64, kind string) ([]Template, error) {
	if kind != "" && !validTemplateKind(kind) {
		return nil, ErrInvalid
	}
	rows, err := s.pool.Query(ctx, "SELECT "+templateColumns+`
		FROM (
			SELECT DISTINCT ON (workspace_id, template_key) *
			FROM plume.check_templates
			WHERE (workspace_id IS NULL OR workspace_id = $1) AND ($2 = '' OR kind = $2)
			ORDER BY workspace_id, template_key, version DESC
		) t
		ORDER BY t.kind, t.workspace_id NULLS FIRST, t.template_key`, workspaceID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	templates := make([]Template, 0)
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		templates = append(templates, t)
	}
	return templates, rows.Err()
}

// Template retrieves a visible revision, including versions superseded by newer publications.
func (s *Store) Template(ctx context.Context, workspaceID, revisionID int64) (Template, error) {
	return scanTemplate(s.pool.QueryRow(ctx, "SELECT "+templateColumns+`
		FROM plume.check_templates t
		WHERE t.id = $2 AND (t.workspace_id IS NULL OR t.workspace_id = $1)`, workspaceID, revisionID))
}

// PublishTemplate creates an immutable revision. An update must name the latest
// workspace-owned revision, so two concurrent editors cannot silently overwrite each other.
func (s *Store) PublishTemplate(ctx context.Context, workspaceID, actorID int64, input TemplateInput) (Template, error) {
	if !validateTemplate(input) {
		return Template{}, ErrInvalid
	}
	var t Template
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if role != "owner" {
			return ErrForbidden
		}

		var latestID int64
		var latestKind string
		var latestVersion int
		err = tx.QueryRow(ctx, `
			SELECT id, kind, version FROM plume.check_templates
			WHERE workspace_id = $1 AND template_key = $2 ORDER BY version DESC LIMIT 1`,
			workspaceID, input.Key).Scan(&latestID, &latestKind, &latestVersion)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if input.BaseRevisionID != nil {
				return ErrConflict
			}
			var reserved bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (SELECT 1 FROM plume.check_templates WHERE workspace_id IS NULL AND template_key = $1)
			`, input.Key).Scan(&reserved); err != nil {
				return err
			}
			if reserved {
				return ErrConflict
			}
		case err != nil:
			return err
		case input.BaseRevisionID == nil || *input.BaseRevisionID != latestID || input.Kind != latestKind:
			return ErrConflict
		}

		t = Template{Key: input.Key, Title: input.Title, Kind: input.Kind, Version: latestVersion + 1, Items: input.Items}
		err = tx.QueryRow(ctx, `
			INSERT INTO plume.check_templates (workspace_id, template_key, title, kind, version, created_by)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at`, workspaceID, t.Key, t.Title, t.Kind,
			t.Version, actorID).Scan(&t.ID, &t.CreatedAt)
		if err != nil {
			return storeError(err)
		}
		for index, item := range input.Items {
			_, err := tx.Exec(ctx, `
				INSERT INTO plume.check_template_items (template_revision_id, item_key, position, title,
					requirement_code, applicability, evidence_requirements, acceptance_criteria)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				t.ID, item.Key, index+1, item.Title, item.RequirementCode, item.Applicability,
				item.EvidenceRequirements, item.AcceptanceCriteria)
			if err != nil {
				if errors.Is(storeError(err), ErrNotFound) {
					return ErrInvalid
				}
				return storeError(err)
			}
		}
		return nil
	})
	return t, err
}

func validateCheckTemplate(ctx context.Context, tx pgx.Tx, workspaceID int64, input CheckInput) error {
	var templateKind, activityKind, requirementCode string
	err := tx.QueryRow(ctx, `
		SELECT t.kind, a.kind, i.requirement_code
		FROM plume.check_templates t
		JOIN plume.check_template_items i ON i.template_revision_id = t.id AND i.item_key = $5
		JOIN plume.activities a ON a.workspace_id = $1 AND a.asset_id = $2 AND a.id = $3
		WHERE t.id = $4 AND (t.workspace_id IS NULL OR t.workspace_id = $1)`,
		workspaceID, input.AssetID, input.ActivityID, input.TemplateRevisionID, input.TemplateItemKey,
	).Scan(&templateKind, &activityKind, &requirementCode)
	if err != nil {
		return storeError(err)
	}
	if templateKind != activityKind || requirementCode != input.RequirementCode {
		return ErrInvalid
	}
	return nil
}
