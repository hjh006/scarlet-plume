package store

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationstandards"
)

const (
	classificationPageLimit    = 50
	classificationHistoryLimit = 50
	classificationCandidateCap = 20
)

type ClassificationCandidate struct {
	Name       string    `json:"name"`
	Category   string    `json:"category"`
	Source     string    `json:"source"`
	Location   string    `json:"location,omitempty"`
	ObservedAt time.Time `json:"observedAt"`
	RunID      string    `json:"runId,omitempty"`
	ScanID     *int64    `json:"scanId,omitempty"`
}

type AssetClassificationItem struct {
	Asset                  Asset                              `json:"asset"`
	Revision               int64                              `json:"revision"`
	Candidates             []ClassificationCandidate          `json:"candidates"`
	CandidateCount         int                                `json:"candidateCount"`
	SuggestedCategory      string                             `json:"suggestedCategory,omitempty"`
	SuggestedBusinessLevel string                             `json:"suggestedBusinessLevel,omitempty"`
	Standard               *classificationstandards.Reference `json:"standard,omitempty"`
}

type AssetClassificationHistory struct {
	ID                        int64                              `json:"id"`
	Revision                  int64                              `json:"revision"`
	Actor                     string                             `json:"actor"`
	CreatedAt                 time.Time                          `json:"createdAt"`
	Category                  string                             `json:"category"`
	BusinessLevel             string                             `json:"businessLevel"`
	StatutoryCategory         string                             `json:"statutoryCategory"`
	Basis                     string                             `json:"basis"`
	PreviousCategory          string                             `json:"previousCategory"`
	PreviousBusinessLevel     string                             `json:"previousBusinessLevel"`
	PreviousStatutoryCategory string                             `json:"previousStatutoryCategory"`
	PreviousBasis             string                             `json:"previousBasis"`
	Standard                  *classificationstandards.Reference `json:"standard,omitempty"`
}

type AssetClassificationDetail struct {
	AssetClassificationItem
	History          []AssetClassificationHistory `json:"history"`
	HistoryTruncated bool                         `json:"historyTruncated,omitempty"`
}

type AssetClassificationCounts struct {
	Total           int64 `json:"total"`
	Pending         int64 `json:"pending"`
	Reviewed        int64 `json:"reviewed"`
	ImportantOrCore int64 `json:"importantOrCore"`
}

type AssetClassificationPage struct {
	Items      []AssetClassificationItem `json:"items"`
	NextCursor *int64                    `json:"nextCursor"`
	Counts     AssetClassificationCounts `json:"counts"`
}

type AssetClassificationFilter struct {
	After             int64
	Status            string
	StatutoryCategory string
}

type AssetClassificationInput struct {
	Revision          int64
	Category          string
	BusinessLevel     string
	StatutoryCategory string
	Basis             string
	StandardID        string
	LevelCode         string
	RuleID            string
}

func validStatutoryCategory(category string) bool {
	switch category {
	case "unknown", "general", "important", "core":
		return true
	default:
		return false
	}
}

func validClassification(input AssetClassificationInput) bool {
	switch input.Category {
	case "unknown", "business", "personal", "sensitive_personal":
	default:
		return false
	}
	switch input.BusinessLevel {
	case "unknown", "internal", "confidential":
	default:
		return false
	}
	return input.Revision >= 0 && validStatutoryCategory(input.StatutoryCategory) && validCheckText(input.Basis, 400)
}

func scanClassificationAsset(row pgx.Row) (AssetClassificationItem, error) {
	item := AssetClassificationItem{Candidates: make([]ClassificationCandidate, 0)}
	var err error
	item.Asset, err = scanAsset(row, &item.Revision, &item.Standard)
	return item, err
}

func (s *Store) ListAssetClassifications(ctx context.Context, workspaceID, actorID int64, filter AssetClassificationFilter) (AssetClassificationPage, error) {
	if filter.After < 0 || filter.Status != "" && filter.Status != "pending" && filter.Status != "reviewed" ||
		filter.StatutoryCategory != "" && !validStatutoryCategory(filter.StatutoryCategory) {
		return AssetClassificationPage{}, ErrInvalid
	}
	var page AssetClassificationPage
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			SELECT count(*), count(*) FILTER (WHERE classification_revision = 0),
			       count(*) FILTER (WHERE classification_revision > 0),
			       count(*) FILTER (WHERE statutory_category IN ('important', 'core'))
			FROM plume.assets WHERE workspace_id = $1
		`, workspaceID).Scan(&page.Counts.Total, &page.Counts.Pending, &page.Counts.Reviewed, &page.Counts.ImportantOrCore); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+assetColumns+`, classification_revision, classification_standard
			FROM plume.assets WHERE workspace_id = $1 AND id > $2
			  AND CASE $3::text WHEN 'pending' THEN classification_revision = 0
			                    WHEN 'reviewed' THEN classification_revision > 0 ELSE true END
			  AND ($4::text = '' OR statutory_category = $4)
			ORDER BY id LIMIT $5
		`, workspaceID, filter.After, filter.Status, filter.StatutoryCategory, classificationPageLimit+1)
		if err != nil {
			return err
		}
		page.Items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (AssetClassificationItem, error) {
			return scanClassificationAsset(row)
		})
		if err != nil {
			return err
		}
		if len(page.Items) > classificationPageLimit {
			page.Items = page.Items[:classificationPageLimit]
			cursor := page.Items[classificationPageLimit-1].Asset.ID
			page.NextCursor = &cursor
		}
		return loadClassificationCandidates(ctx, tx, workspaceID, page.Items)
	})
	return page, storeError(err)
}

func (s *Store) GetAssetClassification(ctx context.Context, workspaceID, actorID, assetID int64) (AssetClassificationDetail, error) {
	var detail AssetClassificationDetail
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		item, err := scanClassificationAsset(tx.QueryRow(ctx, "SELECT "+assetColumns+`, classification_revision, classification_standard
			FROM plume.assets WHERE workspace_id = $1 AND id = $2`, workspaceID, assetID))
		if err != nil {
			return err
		}
		detail, err = classificationDetail(ctx, tx, workspaceID, item)
		return err
	})
	return detail, storeError(err)
}

func (s *Store) UpdateAssetClassification(ctx context.Context, workspaceID, actorID, assetID int64, input AssetClassificationInput) (AssetClassificationDetail, error) {
	input.Basis = strings.TrimSpace(input.Basis)
	if assetID < 1 || !validClassification(input) {
		return AssetClassificationDetail{}, ErrInvalid
	}
	var selected *classificationstandards.Reference
	if input.StandardID != "" {
		var err error
		selected, err = classificationstandards.Select(input.StandardID, input.LevelCode, input.RuleID, input.StatutoryCategory)
		if err != nil {
			return AssetClassificationDetail{}, err
		}
	} else if input.LevelCode != "" || input.RuleID != "" {
		return AssetClassificationDetail{}, classificationstandards.ErrSelection
	}
	var detail AssetClassificationDetail
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		previous, err := scanClassificationAsset(tx.QueryRow(ctx, "SELECT "+assetColumns+`, classification_revision, classification_standard
			FROM plume.assets WHERE workspace_id = $1 AND id = $2 FOR NO KEY UPDATE`, workspaceID, assetID))
		if err != nil {
			return err
		}
		if previous.Revision != input.Revision {
			return ErrConflict
		}
		reference := selected
		if reference == nil {
			reference = previous.Standard
			if reference != nil && reference.StatutoryCategory != "" && reference.StatutoryCategory != input.StatutoryCategory {
				return classificationstandards.ErrSelection
			}
		}
		item, err := scanClassificationAsset(tx.QueryRow(ctx, `
			UPDATE plume.assets
			SET category = $3, business_level = $4, statutory_category = $5, classification_basis = $6,
			    classification_revision = classification_revision + 1, classification_standard = $7
			WHERE workspace_id = $1 AND id = $2
			RETURNING `+assetColumns+", classification_revision, classification_standard",
			workspaceID, assetID, input.Category, input.BusinessLevel, input.StatutoryCategory, input.Basis, reference))
		if err != nil {
			return err
		}
		old := previous.Asset
		if _, err := tx.Exec(ctx, `
			INSERT INTO plume.asset_classification_history
			    (workspace_id, asset_id, revision, actor_id, category, business_level, statutory_category, basis,
			     previous_category, previous_business_level, previous_statutory_category, previous_basis, standard_reference)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		`, workspaceID, assetID, item.Revision, actorID, input.Category, input.BusinessLevel, input.StatutoryCategory, input.Basis,
			old.Category, old.BusinessLevel, old.StatutoryCategory, old.ClassificationBasis, reference); err != nil {
			return err
		}
		detail, err = classificationDetail(ctx, tx, workspaceID, item)
		return err
	})
	return detail, storeError(err)
}

func classificationDetail(ctx context.Context, tx pgx.Tx, workspaceID int64, item AssetClassificationItem) (AssetClassificationDetail, error) {
	items := []AssetClassificationItem{item}
	if err := loadClassificationCandidates(ctx, tx, workspaceID, items); err != nil {
		return AssetClassificationDetail{}, err
	}
	rows, err := tx.Query(ctx, `
		SELECT h.id, h.revision, u.username, h.created_at, h.category, h.business_level, h.statutory_category, h.basis,
		       h.previous_category, h.previous_business_level, h.previous_statutory_category, h.previous_basis, h.standard_reference
		FROM plume.asset_classification_history h JOIN plume.users u ON u.id = h.actor_id
		WHERE h.workspace_id = $1 AND h.asset_id = $2 ORDER BY h.revision DESC LIMIT $3
	`, workspaceID, item.Asset.ID, classificationHistoryLimit+1)
	if err != nil {
		return AssetClassificationDetail{}, err
	}
	history, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AssetClassificationHistory, error) {
		var record AssetClassificationHistory
		err := row.Scan(&record.ID, &record.Revision, &record.Actor, &record.CreatedAt,
			&record.Category, &record.BusinessLevel, &record.StatutoryCategory, &record.Basis,
			&record.PreviousCategory, &record.PreviousBusinessLevel, &record.PreviousStatutoryCategory, &record.PreviousBasis, &record.Standard)
		return record, err
	})
	if err != nil {
		return AssetClassificationDetail{}, err
	}
	return AssetClassificationDetail{
		AssetClassificationItem: items[0],
		History:                 history[:min(len(history), classificationHistoryLimit)],
		HistoryTruncated:        len(history) > classificationHistoryLimit,
	}, nil
}

func loadClassificationCandidates(ctx context.Context, tx pgx.Tx, workspaceID int64, items []AssetClassificationItem) error {
	ids := make([]int64, 0, len(items))
	positions := make(map[int64]int, len(items))
	for i, item := range items {
		switch item.Asset.SourceProvider {
		case "postgresql", "openapi":
			ids = append(ids, item.Asset.ID)
			positions[item.Asset.ID] = i
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		WITH scoped_assets AS (
			SELECT id, source_provider, source FROM plume.assets WHERE workspace_id = $1 AND id = ANY($2)
		), latest_openapi AS (
			SELECT services.source, latest.id, latest.created_at, latest.operations
			FROM (SELECT DISTINCT source FROM scoped_assets WHERE source_provider = 'openapi') services
			CROSS JOIN LATERAL (
				SELECT s.id, s.created_at, s.operations FROM plume.api_security_scans s
				WHERE s.workspace_id = $1 AND s.source = services.source ORDER BY s.id DESC LIMIT 1
			) latest
		), candidates AS (
			SELECT a.id AS asset_id, field->>'name' AS name, field->>'sensitivity' AS category,
			       'postgresql'::text AS provider, 'column'::text AS location, r.observed_at,
			       r.run_id, NULL::bigint AS scan_id
			FROM scoped_assets a
			JOIN plume.postgres_asset_inspections i ON i.asset_id = a.id
			JOIN plume.automation_jobs j ON j.id = i.job_id AND j.workspace_id = $1
			JOIN plume.automation_runs r ON r.id = i.automation_run_id AND r.job_id = j.id
			CROSS JOIN LATERAL jsonb_array_elements(
				CASE WHEN jsonb_typeof(i.inspection->'columns') = 'array' THEN i.inspection->'columns' ELSE '[]'::jsonb END
			) field
			WHERE a.source_provider = 'postgresql'
			UNION ALL
			SELECT a.id, field->>'name', field->>'category', 'openapi', COALESCE(field->>'location', ''),
			       latest.created_at, '', latest.id
			FROM latest_openapi latest
			CROSS JOIN LATERAL jsonb_array_elements(latest.operations) operation
			JOIN scoped_assets a ON a.source_provider = 'openapi' AND a.source = latest.source AND a.id::text = operation->>'assetId'
			CROSS JOIN LATERAL jsonb_array_elements(
				CASE WHEN jsonb_typeof(operation->'sensitiveFields') = 'array' THEN operation->'sensitiveFields' ELSE '[]'::jsonb END
			) field
		), ranked AS (
			SELECT *, count(*) OVER (PARTITION BY asset_id) AS candidate_count,
			       bool_or(category IN ('national_id', 'financial_account')) OVER (PARTITION BY asset_id) AS sensitive_personal,
			       bool_or(category IN ('email', 'phone', 'personal_name', 'address', 'ip_address')) OVER (PARTITION BY asset_id) AS personal,
			       bool_or(category = 'credential') OVER (PARTITION BY asset_id) AS credential,
			       row_number() OVER (PARTITION BY asset_id ORDER BY
			           (category IN ('national_id', 'financial_account', 'credential')) DESC, name, location, category) AS position
			FROM candidates
			WHERE char_length(name) BETWEEN 1 AND 240
			  AND category IN ('email', 'phone', 'national_id', 'financial_account', 'credential', 'personal_name', 'address', 'ip_address')
		)
		SELECT asset_id, name, category, provider, location, observed_at, run_id, scan_id,
		       candidate_count, sensitive_personal, personal, credential
		FROM ranked WHERE position <= $3 ORDER BY asset_id, position
	`, workspaceID, ids, classificationCandidateCap)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var candidate ClassificationCandidate
		var assetID int64
		var count int
		var sensitive, personal, credential bool
		if err := rows.Scan(&assetID, &candidate.Name, &candidate.Category, &candidate.Source, &candidate.Location,
			&candidate.ObservedAt, &candidate.RunID, &candidate.ScanID, &count, &sensitive, &personal, &credential); err != nil {
			return err
		}
		item := &items[positions[assetID]]
		item.Candidates = append(item.Candidates, candidate)
		item.CandidateCount = count
		switch {
		case sensitive:
			item.SuggestedCategory = "sensitive_personal"
		case personal:
			item.SuggestedCategory = "personal"
		}
		if sensitive || credential {
			item.SuggestedBusinessLevel = "confidential"
		} else if personal {
			item.SuggestedBusinessLevel = "internal"
		}
	}
	return rows.Err()
}
