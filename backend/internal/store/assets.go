package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type Asset struct {
	ID                  int64     `json:"id"`
	Name                string    `json:"name"`
	System              string    `json:"system"`
	OwnerTeam           string    `json:"ownerTeam"`
	SourceProvider      string    `json:"sourceProvider,omitempty"`
	Source              string    `json:"source"`
	SourceRef           string    `json:"sourceRef"`
	Category            string    `json:"category"`
	BusinessLevel       string    `json:"businessLevel"`
	StatutoryCategory   string    `json:"statutoryCategory"`
	ClassificationBasis string    `json:"classificationBasis"`
	Description         string    `json:"description"`
	CreatedAt           time.Time `json:"createdAt"`
}

type ImportRun struct {
	ID        int64     `json:"id"`
	Provider  string    `json:"provider"`
	Source    string    `json:"source"`
	BatchID   string    `json:"batchId"`
	Created   int       `json:"created"`
	Existing  int       `json:"existing"`
	Replayed  bool      `json:"replayed,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type CollectedAsset struct {
	SourceRef string `json:"sourceRef"`
	Name      string `json:"name"`
}

type OverviewCounts struct {
	Assets                int64 `json:"assets"`
	Activities            int64 `json:"activities"`
	Checks                int64 `json:"checks"`
	Draft                 int64 `json:"draft"`
	InReview              int64 `json:"inReview"`
	NeedsWork             int64 `json:"needsWork"`
	Verified              int64 `json:"verified"`
	ActiveSources         int64 `json:"activeSources"`
	OpenRuntimeFindings   int64 `json:"openRuntimeFindings"`
	PendingClassification int64 `json:"pendingClassification"`
}

const assetColumns = "id, name, system, owner_team, COALESCE(source_provider, ''), COALESCE(source, ''), COALESCE(source_ref, ''), " +
	"category, business_level, statutory_category, classification_basis, description, created_at"
const importColumns = "id, provider, source, batch_id, created_count, existing_count, created_at"

func scanAsset(row pgx.Row, extra ...any) (Asset, error) {
	var asset Asset
	destinations := []any{&asset.ID, &asset.Name, &asset.System, &asset.OwnerTeam, &asset.SourceProvider, &asset.Source, &asset.SourceRef,
		&asset.Category, &asset.BusinessLevel, &asset.StatutoryCategory, &asset.ClassificationBasis, &asset.Description, &asset.CreatedAt}
	err := row.Scan(append(destinations, extra...)...)
	return asset, storeError(err)
}

func (s *Store) ListAssets(ctx context.Context, workspaceID, after int64, limit int) ([]Asset, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+assetColumns+`
		FROM plume.assets WHERE workspace_id = $1 AND id > $2 ORDER BY id LIMIT $3
	`, workspaceID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	assets := make([]Asset, 0)
	for rows.Next() {
		asset, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	return assets, rows.Err()
}

func (s *Store) Asset(ctx context.Context, workspaceID, assetID int64) (Asset, error) {
	return scanAsset(s.pool.QueryRow(ctx, "SELECT "+assetColumns+" FROM plume.assets WHERE workspace_id = $1 AND id = $2",
		workspaceID, assetID))
}

func (s *Store) AssetCount(ctx context.Context, workspaceID int64) (int64, error) {
	var count int64
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM plume.assets WHERE workspace_id = $1", workspaceID).Scan(&count)
	return count, err
}

func (s *Store) OverviewCounts(ctx context.Context, workspaceID int64) (OverviewCounts, error) {
	var counts OverviewCounts
	err := s.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM plume.assets WHERE workspace_id = $1),
		       (SELECT count(*) FROM plume.activities WHERE workspace_id = $1),
		       count(*),
		       count(*) FILTER (WHERE status = 'draft'),
		       count(*) FILTER (WHERE status = 'in_review'),
		       count(*) FILTER (WHERE status = 'needs_work'),
		       count(*) FILTER (WHERE status = 'verified'),
		       (SELECT count(*) FROM plume.automation_jobs WHERE workspace_id = $1 AND revoked_at IS NULL)
		       + (SELECT count(*) FROM plume.runtime_security_sources WHERE workspace_id = $1 AND revoked_at IS NULL)
 + (SELECT count(*) FROM plume.discovery_sources WHERE workspace_id=$1)
 + (SELECT count(*) FROM plume.operations_sources WHERE workspace_id=$1 AND revoked_at IS NULL)
 + (SELECT count(*) FROM plume.application_sources WHERE workspace_id=$1 AND revoked_at IS NULL),
		       (SELECT count(*) FROM plume.runtime_findings f JOIN plume.checks c ON c.id = f.check_id AND c.workspace_id = f.workspace_id
		        WHERE f.workspace_id = $1 AND c.status <> 'verified'),
		       (SELECT count(*) FROM plume.assets WHERE workspace_id = $1 AND (classification_revision = 0 OR classification_origin = 'automatic'))
		FROM plume.checks WHERE workspace_id = $1
	`, workspaceID).Scan(&counts.Assets, &counts.Activities, &counts.Checks, &counts.Draft,
		&counts.InReview, &counts.NeedsWork, &counts.Verified, &counts.ActiveSources,
		&counts.OpenRuntimeFindings, &counts.PendingClassification)
	return counts, err
}

func (s *Store) ListRecentAssets(ctx context.Context, workspaceID int64, limit int) ([]Asset, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+assetColumns+`
		FROM plume.assets WHERE workspace_id = $1 ORDER BY id DESC LIMIT $2
	`, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Asset, error) {
		return scanAsset(row)
	})
}

func (s *Store) CreateAsset(ctx context.Context, workspaceID int64, name, system, ownerTeam string) (Asset, error) {
	return scanAsset(s.pool.QueryRow(ctx, `
		INSERT INTO plume.assets (workspace_id, name, system, owner_team)
		VALUES ($1, $2, $3, $4) RETURNING `+assetColumns, workspaceID, name, system, ownerTeam))
}

func (s *Store) ListImports(ctx context.Context, workspaceID int64) ([]ImportRun, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+importColumns+`
		FROM plume.import_runs WHERE workspace_id = $1 ORDER BY id DESC LIMIT 20
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	runs := make([]ImportRun, 0)
	for rows.Next() {
		var run ImportRun
		if err := rows.Scan(&run.ID, &run.Provider, &run.Source, &run.BatchID, &run.Created, &run.Existing, &run.CreatedAt); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Store) ImportAssets(
	ctx context.Context, workspaceID, userID int64, provider, source, batchID, digest string, assets []CollectedAsset,
) (ImportRun, error) {
	var run ImportRun
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		var member bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM plume.memberships WHERE workspace_id = $1 AND user_id = $2
				AND role IN ('owner', 'operator'))
		`, workspaceID, userID).Scan(&member); err != nil {
			return err
		}
		if !member {
			return ErrNotFound
		}

		var err error
		run, err = importAssetsTx(ctx, tx, workspaceID, userID, provider, source, batchID, digest, assets)
		return err
	})
	if err != nil {
		return ImportRun{}, storeError(err)
	}
	return run, nil
}

func importAssetsTx(ctx context.Context, tx pgx.Tx, workspaceID, userID int64, provider, source, batchID, digest string, assets []CollectedAsset) (ImportRun, error) {
	var run ImportRun
	err := tx.QueryRow(ctx, `
		INSERT INTO plume.import_runs (workspace_id, actor_id, provider, source, batch_id, payload_digest)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (workspace_id, provider, source, batch_id) DO NOTHING
		RETURNING `+importColumns, workspaceID, userID, provider, source, batchID, digest).
		Scan(&run.ID, &run.Provider, &run.Source, &run.BatchID, &run.Created, &run.Existing, &run.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		var existingDigest string
		if err := tx.QueryRow(ctx, "SELECT "+importColumns+`, payload_digest FROM plume.import_runs
			WHERE workspace_id = $1 AND provider = $2 AND source = $3 AND batch_id = $4
		`, workspaceID, provider, source, batchID).
			Scan(&run.ID, &run.Provider, &run.Source, &run.BatchID, &run.Created, &run.Existing, &run.CreatedAt, &existingDigest); err != nil {
			return ImportRun{}, err
		}
		if existingDigest != digest {
			return ImportRun{}, ErrConflict
		}
		run.Replayed = true
		return run, nil
	}
	if err != nil {
		return ImportRun{}, err
	}

	refs := make([]string, len(assets))
	names := make([]string, len(assets))
	for i, asset := range assets {
		refs[i], names[i] = asset.SourceRef, asset.Name
	}
	// Sort by reference so overlapping batches take uniqueness locks in the
	// same order. Within one batch the first label wins for each reference.
	inserted, err := tx.Exec(ctx, `
		WITH incoming AS (
			SELECT DISTINCT ON (source_ref) source_ref, name
			FROM unnest($4::text[], $5::text[]) WITH ORDINALITY AS input(source_ref, name, position)
			ORDER BY source_ref, position
		)
		INSERT INTO plume.assets (workspace_id, name, system, source_provider, source, source_ref)
		SELECT $1, name, $3, $2, $3, source_ref FROM incoming ORDER BY source_ref
		ON CONFLICT (workspace_id, source_provider, source, source_ref) DO NOTHING
	`, workspaceID, provider, source, refs, names)
	if err != nil {
		return ImportRun{}, err
	}
	run.Created = int(inserted.RowsAffected())
	run.Existing = len(assets) - run.Created
	_, err = tx.Exec(ctx, `
		UPDATE plume.import_runs SET created_count = $2, existing_count = $3 WHERE id = $1
	`, run.ID, run.Created, run.Existing)
	return run, err
}
