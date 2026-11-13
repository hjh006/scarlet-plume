package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hjh006/scarlet-plume/backend/internal/apisecurity"
	"github.com/jackc/pgx/v5"
)

// Browser annotations are explicit human mappings, never observed API lineage
// or an automatic classification determination. No raw page/input value exists
// in this contract.
type BrowserAnnotationPage struct {
	Origin string `json:"origin"`
	Path   string `json:"path"`
	Title  string `json:"title"`
}
type BrowserAnnotationField struct {
	Name         string                      `json:"name"`
	Label        string                      `json:"label"`
	ElementKind  string                      `json:"elementKind"`
	Locator      string                      `json:"locator"`
	Category     string                      `json:"category"`
	TargetField  string                      `json:"targetField"`
	Note         string                      `json:"note"`
	APICandidate *BrowserAnnotationCandidate `json:"apiCandidate,omitempty"`
}
type BrowserAnnotationCandidate struct {
	Index    int    `json:"index"`
	ScanID   int64  `json:"scanId"`
	Method   string `json:"method"`
	Route    string `json:"route"`
	Field    string `json:"field"`
	Location string `json:"location"`
	Match    string `json:"match"`
	Status   string `json:"status"`
}
type BrowserAnnotationInput struct {
	AssetID int64                    `json:"assetId"`
	Page    BrowserAnnotationPage    `json:"page"`
	Fields  []BrowserAnnotationField `json:"fields"`
}
type BrowserAnnotation struct {
	BrowserAnnotationInput
	ID             int64     `json:"id"`
	AssetName      string    `json:"assetName"`
	Actor          string    `json:"actor"`
	CreatedAt      time.Time `json:"createdAt"`
	SnapshotDigest string    `json:"snapshotDigest"`
	Replayed       bool      `json:"replayed,omitempty"`
}

func browserAnnotationText(value string, max int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= max && strings.IndexFunc(value, unicode.IsControl) < 0
}
func validateBrowserAnnotation(input *BrowserAnnotationInput) error {
	input.Page.Origin = strings.TrimSpace(input.Page.Origin)
	input.Page.Path = strings.TrimSpace(input.Page.Path)
	input.Page.Title = strings.TrimSpace(input.Page.Title)
	u, e := url.Parse(input.Page.Origin)
	if input.AssetID < 1 || e != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !browserAnnotationText(input.Page.Origin, 240) || !strings.HasPrefix(input.Page.Path, "/") || strings.ContainsAny(input.Page.Path, "?#") || !browserAnnotationText(input.Page.Path, 400) || !browserAnnotationText(input.Page.Title, 120) || len(input.Fields) < 1 || len(input.Fields) > 100 {
		return ErrInvalid
	}
	input.Page.Origin = u.Scheme + "://" + u.Host
	for i := range input.Fields {
		f := &input.Fields[i]
		f.Name = strings.TrimSpace(f.Name)
		f.Label = strings.TrimSpace(f.Label)
		f.Category = strings.TrimSpace(f.Category)
		f.TargetField = strings.TrimSpace(f.TargetField)
		f.Note = strings.TrimSpace(f.Note)
		if (f.Name == "" && f.Label == "") || !browserAnnotationText(f.Name, 120) || !browserAnnotationText(f.Label, 120) || !browserAnnotationText(f.Locator, 200) || !browserAnnotationText(f.Category, 80) || !browserAnnotationText(f.TargetField, 160) || !browserAnnotationText(f.Note, 240) {
			return ErrInvalid
		}
		switch f.ElementKind {
		case "input", "select", "textarea", "label", "th", "columnheader":
		default:
			return ErrInvalid
		}
	}
	return nil
}

func (s *Store) CreateBrowserAnnotation(ctx context.Context, ws, actor int64, input BrowserAnnotationInput) (BrowserAnnotation, error) {
	if e := validateBrowserAnnotation(&input); e != nil {
		return BrowserAnnotation{}, e
	}
	encoded, e := json.Marshal(input)
	if e != nil {
		return BrowserAnnotation{}, ErrInvalid
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	result := BrowserAnnotation{BrowserAnnotationInput: input, SnapshotDigest: digest}
	e = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, ws, actor, true)
		if e != nil {
			return e
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		if e = tx.QueryRow(ctx, `SELECT name FROM plume.assets WHERE workspace_id=$1 AND id=$2 FOR SHARE`, ws, input.AssetID).Scan(&result.AssetName); e != nil {
			return e
		}
		for _, f := range input.Fields {
			if f.APICandidate != nil {
				if e = verifyBrowserAPICandidate(ctx, tx, ws, input.AssetID, f, *f.APICandidate); e != nil {
					return e
				}
			}
		}
		// Replays do not mutate the original attribution or the imported snapshot.
		e = tx.QueryRow(ctx, `INSERT INTO plume.browser_annotations(workspace_id,asset_id,actor_id,page,fields,snapshot_digest) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(workspace_id,snapshot_digest) DO NOTHING RETURNING id,created_at`, ws, input.AssetID, actor, input.Page, input.Fields, digest).Scan(&result.ID, &result.CreatedAt)
		if e == pgx.ErrNoRows {
			result.Replayed = true
			return tx.QueryRow(ctx, `SELECT b.id,b.created_at,u.username FROM plume.browser_annotations b JOIN plume.users u ON u.id=b.actor_id WHERE b.workspace_id=$1 AND b.snapshot_digest=$2`, ws, digest).Scan(&result.ID, &result.CreatedAt, &result.Actor)
		}
		if e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT username FROM plume.users WHERE id=$1`, actor).Scan(&result.Actor)
	})
	return result, storeError(e)
}

func browserFieldKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
func verifyBrowserAPICandidate(ctx context.Context, tx pgx.Tx, ws, assetID int64, f BrowserAnnotationField, c BrowserAnnotationCandidate) error {
	if c.ScanID < 1 || c.Match != "exact_name" || c.Status != "candidate" {
		return ErrInvalid
	}
	if browserFieldKey(c.Field) == "" || (browserFieldKey(c.Field) != browserFieldKey(f.Name) && browserFieldKey(c.Field) != browserFieldKey(f.Label)) {
		return ErrInvalid
	}
	var operations []apisecurity.Operation
	if e := tx.QueryRow(ctx, `SELECT operations FROM plume.api_security_scans WHERE workspace_id=$1 AND id=$2`, ws, c.ScanID).Scan(&operations); e != nil {
		return e
	}
	for _, op := range operations {
		if op.AssetID == assetID && op.Method == c.Method && op.Path == c.Route {
			for _, field := range op.SensitiveFields {
				if field.Name == c.Field && field.Location == c.Location {
					return nil
				}
			}
		}
	}
	return ErrInvalid
}

// MatchBrowserAnnotation returns candidates from an explicitly chosen imported
// OpenAPI asset only. Equal field names do not establish observed lineage.
func (s *Store) MatchBrowserAnnotation(ctx context.Context, ws, actor int64, input BrowserAnnotationInput) ([]BrowserAnnotationCandidate, error) {
	if e := validateBrowserAnnotation(&input); e != nil {
		return nil, e
	}
	result := []BrowserAnnotationCandidate{}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, ws, actor, false)
		if e != nil {
			return e
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		var id int64
		if e = tx.QueryRow(ctx, `SELECT id FROM plume.assets WHERE workspace_id=$1 AND id=$2`, ws, input.AssetID).Scan(&id); e != nil {
			return e
		}
		marker, _ := json.Marshal([]map[string]int64{{"assetId": input.AssetID}})
		var scanID int64
		var ops []apisecurity.Operation
		e = tx.QueryRow(ctx, `SELECT id,operations FROM plume.api_security_scans WHERE workspace_id=$1 AND operations @> $2::jsonb ORDER BY id DESC LIMIT 1`, ws, string(marker)).Scan(&scanID, &ops)
		if e == pgx.ErrNoRows {
			return nil
		}
		if e != nil {
			return e
		}
		for i, f := range input.Fields {
			for _, op := range ops {
				if op.AssetID != input.AssetID {
					continue
				}
				for _, field := range op.SensitiveFields {
					key := browserFieldKey(field.Name)
					if key != "" && (key == browserFieldKey(f.Name) || key == browserFieldKey(f.Label)) {
						result = append(result, BrowserAnnotationCandidate{Index: i, ScanID: scanID, Method: op.Method, Route: op.Path, Field: field.Name, Location: field.Location, Match: "exact_name", Status: "candidate"})
					}
				}
			}
		}
		return nil
	})
	return result, storeError(e)
}

func (s *Store) ListBrowserAnnotations(ctx context.Context, ws, actor, assetID, before int64) ([]BrowserAnnotation, error) {
	if assetID < 0 || before < 0 {
		return nil, ErrInvalid
	}
	result := []BrowserAnnotation{}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		if assetID > 0 {
			var id int64
			if e := tx.QueryRow(ctx, `SELECT id FROM plume.assets WHERE workspace_id=$1 AND id=$2`, ws, assetID).Scan(&id); e != nil {
				return e
			}
		}
		rows, e := tx.Query(ctx, `SELECT b.id,b.asset_id,a.name,b.page,b.fields,u.username,b.created_at,b.snapshot_digest FROM plume.browser_annotations b JOIN plume.assets a ON a.id=b.asset_id JOIN plume.users u ON u.id=b.actor_id WHERE b.workspace_id=$1 AND ($2::bigint=0 OR b.asset_id=$2) AND ($3::bigint=0 OR b.id<$3) ORDER BY b.id DESC LIMIT 50`, ws, assetID, before)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var item BrowserAnnotation
			if e = rows.Scan(&item.ID, &item.AssetID, &item.AssetName, &item.Page, &item.Fields, &item.Actor, &item.CreatedAt, &item.SnapshotDigest); e != nil {
				return e
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, storeError(e)
}
func (s *Store) GetBrowserAnnotation(ctx context.Context, ws, actor, id int64) (BrowserAnnotation, error) {
	if id < 1 {
		return BrowserAnnotation{}, ErrInvalid
	}
	var item BrowserAnnotation
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT b.id,b.asset_id,a.name,b.page,b.fields,u.username,b.created_at,b.snapshot_digest FROM plume.browser_annotations b JOIN plume.assets a ON a.id=b.asset_id JOIN plume.users u ON u.id=b.actor_id WHERE b.workspace_id=$1 AND b.id=$2`, ws, id).Scan(&item.ID, &item.AssetID, &item.AssetName, &item.Page, &item.Fields, &item.Actor, &item.CreatedAt, &item.SnapshotDigest)
	})
	return item, storeError(e)
}
