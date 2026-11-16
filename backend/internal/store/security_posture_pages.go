package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type SecurityPostureFilter struct {
	Query    string `json:"q"`
	Severity string `json:"severity"`
	Code     string `json:"code"`
}

type SecurityPostureAsset struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	SourceRef    string `json:"sourceRef"`
	FindingCount int    `json:"findingCount"`
}

type SecurityPosturePage struct {
	Items          []SecurityPostureFinding `json:"items"`
	Assets         []SecurityPostureAsset   `json:"assets"`
	MatchingAssets int64                    `json:"matchingAssets"`
	WatermarkID    int64                    `json:"watermarkId"`
	GeneratedAt    time.Time                `json:"generatedAt"`
	HasMore        bool                     `json:"hasMore"`
	NextCursor     string                   `json:"nextCursor,omitempty"`
	Coverage       string                   `json:"coverage"`
}

type securityPostureCursor struct {
	Version int    `json:"v"`
	After   int64  `json:"after"`
	Through int64  `json:"through"`
	Scope   string `json:"scope"`
}

func decodeSecurityPostureCursor(raw, scope string) (securityPostureCursor, error) {
	c := securityPostureCursor{Version: 1, Scope: scope}
	if raw == "" {
		return c, nil
	}
	if len(raw) > 512 {
		return c, ErrInvalid
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return c, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.Version != 1 || c.After < 1 || c.Through < c.After || c.Scope != scope {
		return c, ErrInvalid
	}
	return c, nil
}

// A cursor freezes the admitted asset ID range. Each page reads current source
// facts in its own repeatable-read transaction; it is not a cross-page snapshot.
func (s *Store) SecurityPosturePage(ctx context.Context, ws, actor int64, filter SecurityPostureFilter, cursor string, limit int) (SecurityPosturePage, error) {
	out := SecurityPosturePage{Items: []SecurityPostureFinding{}, Assets: []SecurityPostureAsset{}, GeneratedAt: time.Now().UTC(), Coverage: "按资产ID递增遍历所选范围；游标固定资产上界，各页采用读取时的分类与权限事实。资产数不是风险匹配数，也不代表完整来源覆盖。筛选无发现仍须核对未验证及未接入来源。"}
	filter.Query = strings.TrimSpace(filter.Query)
	if !securityText(filter.Query, 120, false) || limit < 1 || limit > 100 || !slices.Contains([]string{"", "high", "medium"}, filter.Severity) || !slices.Contains([]string{"", "classification_review", "missing_owner", "permission_coverage", "stale_permission_source", "public_select", "delegable_select"}, filter.Code) {
		return out, ErrInvalid
	}
	scope := aiOperationsScope("security-posture-v1", ws, filter)
	c, err := decodeSecurityPostureCursor(cursor, scope)
	if err != nil {
		return out, err
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if err := securityPlatformMember(ctx, tx, ws, actor, false); err != nil {
			return err
		}
		if cursor == "" {
			if err := tx.QueryRow(ctx, `SELECT COALESCE(max(id),0) FROM plume.assets WHERE workspace_id=$1`, ws).Scan(&c.Through); err != nil {
				return err
			}
		}
		out.WatermarkID = c.Through
		// position() treats SQL pattern characters as literal search text.
		const match = `workspace_id=$1 AND id<=$2 AND ($3='' OR position(lower($3) in lower(name||' '||system||' '||owner_team||' '||COALESCE(source_ref,'')))>0)`
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM plume.assets WHERE `+match, ws, c.Through, filter.Query).Scan(&out.MatchingAssets); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+assetColumns+` FROM plume.assets WHERE `+match+` AND id>$4 ORDER BY id LIMIT $5`, ws, c.Through, filter.Query, c.After, limit+1)
		if err != nil {
			return err
		}
		assets := []Asset{}
		for rows.Next() {
			a, err := scanAsset(rows)
			if err != nil {
				rows.Close()
				return err
			}
			assets = append(assets, a)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		out.HasMore = len(assets) > limit
		if out.HasMore {
			assets = assets[:limit]
		}
		for _, a := range assets {
			classification, err := scenarioClassification(ctx, tx, ws, a.ID)
			if err != nil {
				return err
			}
			permissions, err := readScenarioPostgresSource(ctx, tx, ws, a.ID)
			if err != nil {
				return err
			}
			findings := securityPostureFindings(a, classification, permissions)
			out.Assets = append(out.Assets, SecurityPostureAsset{ID: a.ID, Name: a.Name, SourceRef: a.SourceRef, FindingCount: len(findings)})
			for _, finding := range findings {
				if (filter.Severity == "" || finding.Severity == filter.Severity) && (filter.Code == "" || finding.Code == filter.Code) {
					out.Items = append(out.Items, finding)
				}
			}
		}
		if out.HasMore {
			c.After = assets[len(assets)-1].ID
			b, _ := json.Marshal(c)
			out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
		}
		return nil
	})
	return out, storeError(err)
}
