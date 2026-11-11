package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/hjh006/scarlet-plume/backend/internal/apisecurity"
)

const apiSecuritySummaryColumns = `id, source, created_at, version, operation_count, sensitive_field_count,
	finding_count, check_count, unresolved_references`

type APISecurityScanSummary struct {
	ID                   int64     `json:"id"`
	Source               string    `json:"source"`
	CreatedAt            time.Time `json:"createdAt"`
	Version              string    `json:"version"`
	OperationCount       int       `json:"operationCount"`
	SensitiveFieldCount  int       `json:"sensitiveFieldCount"`
	FindingCount         int       `json:"findingCount"`
	CheckCount           int       `json:"checkCount"`
	UnresolvedReferences int       `json:"unresolvedReferences"`
	Replayed             bool      `json:"replayed,omitempty"`
}

type APISecurityScan struct {
	APISecurityScanSummary
	Operations []apisecurity.Operation `json:"operations"`
}

func apiSecuritySummaryFields(scan *APISecurityScanSummary) []any {
	return []any{&scan.ID, &scan.Source, &scan.CreatedAt, &scan.Version, &scan.OperationCount,
		&scan.SensitiveFieldCount, &scan.FindingCount, &scan.CheckCount, &scan.UnresolvedReferences}
}

func (s *Store) ListAPISecurityScans(ctx context.Context, workspaceID int64) ([]APISecurityScanSummary, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+apiSecuritySummaryColumns+`
		FROM plume.api_security_scans WHERE workspace_id = $1 ORDER BY id DESC LIMIT 25
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (APISecurityScanSummary, error) {
		var scan APISecurityScanSummary
		err := row.Scan(apiSecuritySummaryFields(&scan)...)
		return scan, err
	})
}

func (s *Store) APISecurityScan(ctx context.Context, workspaceID, id int64) (APISecurityScan, error) {
	var scan APISecurityScan
	err := s.pool.QueryRow(ctx, "SELECT "+apiSecuritySummaryColumns+`, operations
		FROM plume.api_security_scans WHERE workspace_id = $1 AND id = $2
	`, workspaceID, id).Scan(append(apiSecuritySummaryFields(&scan.APISecurityScanSummary), &scan.Operations)...)
	return scan, storeError(err)
}

func (s *Store) ImportAPISecurityScan(ctx context.Context, workspaceID, actorID int64, source, digest string, analysis apisecurity.Scan) (APISecurityScan, error) {
	scan, assets, err := prepareAPISecurityScan(source, digest, analysis)
	if err != nil {
		return APISecurityScan{}, err
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		// Serialize replays and operation bindings for this workspace and source.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('plume:api-security:' || $2, $1))`,
			workspaceID, source); err != nil {
			return err
		}
		var latest APISecurityScan
		var latestDigest string
		err = tx.QueryRow(ctx, "SELECT "+apiSecuritySummaryColumns+`, operations, scan_digest
			FROM plume.api_security_scans
			WHERE workspace_id = $1 AND source = $2 ORDER BY id DESC LIMIT 1
		`, workspaceID, source).Scan(append(apiSecuritySummaryFields(&latest.APISecurityScanSummary), &latest.Operations, &latestDigest)...)
		if err == nil && latestDigest == digest {
			latest.Replayed = true
			scan = latest
			return nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO plume.api_security_scans (workspace_id, actor_id, source, scan_digest, version,
			    operation_count, sensitive_field_count, finding_count, unresolved_references)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			RETURNING id, created_at
		`, workspaceID, actorID, source, digest, scan.Version, scan.OperationCount, scan.SensitiveFieldCount,
			scan.FindingCount, scan.UnresolvedReferences).Scan(&scan.ID, &scan.CreatedAt); err != nil {
			return err
		}
		if _, err := importAssetsTx(ctx, tx, workspaceID, actorID, "openapi", source, fmt.Sprintf("api-scan-%d", scan.ID), digest, assets); err != nil {
			return err
		}
		if err := persistAPISecurityChecks(ctx, tx, workspaceID, actorID, assets, &scan); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE plume.api_security_scans SET operations = $2, check_count = $3 WHERE id = $1`,
			scan.ID, scan.Operations, scan.CheckCount)
		if err != nil {
			return err
		}
		return captureDataFlowSnapshot(ctx, tx, workspaceID, actorID, "openapi")
	})
	if err != nil {
		return APISecurityScan{}, storeError(err)
	}
	return scan, nil
}

func prepareAPISecurityScan(source, digest string, analysis apisecurity.Scan) (APISecurityScan, []CollectedAsset, error) {
	if !validAutomationAssetSource(source) || !digestPattern.MatchString(digest) ||
		!validCheckText(analysis.Version, 32) || len(analysis.Operations) == 0 ||
		len(analysis.Operations) > apisecurity.MaxOperations || analysis.UnresolvedReferences < 0 {
		return APISecurityScan{}, nil, ErrInvalid
	}
	scan := APISecurityScan{
		APISecurityScanSummary: APISecurityScanSummary{
			Source: source, Version: analysis.Version, OperationCount: len(analysis.Operations),
			UnresolvedReferences: analysis.UnresolvedReferences,
		},
		Operations: make([]apisecurity.Operation, len(analysis.Operations)),
	}
	assets := make([]CollectedAsset, len(analysis.Operations))
	seen := make(map[string]bool, len(analysis.Operations))
	for i, operation := range analysis.Operations {
		switch operation.Method {
		case "GET", "PUT", "POST", "DELETE", "OPTIONS", "HEAD", "PATCH", "TRACE":
		default:
			return APISecurityScan{}, nil, ErrInvalid
		}
		if !strings.HasPrefix(operation.Path, "/") || !validCheckText(operation.Path, 240) || strings.ContainsAny(operation.Path, "?#") ||
			strings.IndexFunc(operation.Path, unicode.IsControl) >= 0 {
			return APISecurityScan{}, nil, ErrInvalid
		}
		name := operation.Method + " " + operation.Path
		if seen[name] {
			return APISecurityScan{}, nil, ErrInvalid
		}
		seen[name] = true
		assets[i] = CollectedAsset{Name: name, SourceRef: name}
		scan.Operations[i] = apisecurity.Operation{
			Method: operation.Method, Path: operation.Path, Auth: operation.Auth, Deprecated: operation.Deprecated,
			SensitiveFields: append([]apisecurity.SensitiveField{}, operation.SensitiveFields...),
			Findings:        append([]apisecurity.Finding{}, operation.Findings...),
		}
		scan.SensitiveFieldCount += len(operation.SensitiveFields)
		scan.FindingCount += len(operation.Findings)
	}
	if payload, err := json.Marshal(scan.Operations); err != nil || len(payload) > 1<<20 {
		return APISecurityScan{}, nil, ErrInvalid
	}
	return scan, assets, nil
}

func persistAPISecurityChecks(ctx context.Context, tx pgx.Tx, workspaceID, actorID int64, assets []CollectedAsset, scan *APISecurityScan) error {
	refs := make([]string, len(assets))
	for i, asset := range assets {
		refs[i] = asset.SourceRef
	}
	rows, err := tx.Query(ctx, `
		SELECT id, source_ref FROM plume.assets
		WHERE workspace_id = $1 AND source_provider = 'openapi' AND source = $2 AND source_ref = ANY($3)
	`, workspaceID, scan.Source, refs)
	if err != nil {
		return err
	}
	defer rows.Close()
	assetIDs := make(map[string]int64, len(assets))
	for rows.Next() {
		var id int64
		var ref string
		if err := rows.Scan(&id, &ref); err != nil {
			return err
		}
		assetIDs[ref] = id
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(assetIDs) != len(assets) {
		return ErrInvalid
	}
	for i, asset := range assets {
		operation := &scan.Operations[i]
		if err := persistAPISecurityCheck(ctx, tx, workspaceID, actorID, assetIDs[asset.SourceRef], scan, operation); err != nil {
			return err
		}
		if operation.CheckID != 0 {
			scan.CheckCount++
		}
	}
	return nil
}

func persistAPISecurityCheck(ctx context.Context, tx pgx.Tx, workspaceID, actorID, assetID int64, scan *APISecurityScan, operation *apisecurity.Operation) error {
	facts, err := json.Marshal(operation)
	if err != nil {
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(facts))
	var check Check
	var previousDigest string
	err = tx.QueryRow(ctx, `
		SELECT c.id, c.status, b.fact_digest
		FROM plume.api_security_check_bindings b
		JOIN plume.checks c ON c.id = b.check_id AND c.workspace_id = $1
		WHERE b.asset_id = $2 FOR NO KEY UPDATE OF c
	`, workspaceID, assetID).Scan(&check.ID, &check.Status, &previousDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		if len(operation.Findings) == 0 {
			operation.AssetID = assetID
			return nil
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO plume.checks (workspace_id, asset_id, requirement_code, owner_team, note, created_by)
			VALUES ($1, $2, 'G03', '自动采集', '根据 OpenAPI 定义生成。请核实接口授权和敏感数据处理。', $3)
			RETURNING id, status
		`, workspaceID, assetID, actorID).Scan(&check.ID, &check.Status); err != nil {
			return err
		}
		if err := recordCheckEvent(ctx, tx, check.ID, actorID, "created", "由 OpenAPI 自动检查生成"); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	operation.AssetID, operation.CheckID = assetID, check.ID
	if check.Status != "draft" || previousDigest == digest {
		return nil
	}
	summary := fmt.Sprintf("从 OpenAPI 定义识别 %d 个可能敏感的字段和 %d 项检查信号。",
		len(operation.SensitiveFields), len(operation.Findings))
	for _, finding := range operation.Findings {
		title := finding.Title + "。"
		if !validCheckText(summary+title, 500) {
			break
		}
		summary += title
	}
	summary += "仅分析接口定义，运行时行为需复核。"
	if _, err := addEvidenceTx(ctx, tx, check, actorID, EvidenceInput{
		Kind: "observation", SourceSystem: "OpenAPI 自动检查", SourceRecord: fmt.Sprintf("API 扫描 #%d", scan.ID),
		Summary: summary, ObservedAt: scan.CreatedAt,
	}, true); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO plume.api_security_check_bindings (asset_id, check_id, fact_digest)
		VALUES ($1, $2, $3) ON CONFLICT (asset_id) DO UPDATE SET fact_digest = excluded.fact_digest
	`, assetID, check.ID, digest)
	return err
}
