package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationstandards"
	"github.com/jackc/pgx/v5"
)

type ClassificationPublicationInput struct {
	Title string                        `json:"title"`
	Note  string                        `json:"note"`
	Items []ClassificationAssetRevision `json:"items"`
}
type ClassificationPublicationReviewInput struct {
	Action string `json:"action"`
	Note   string `json:"note"`
}
type ClassificationPublication struct {
	ID             int64                         `json:"id"`
	Title          string                        `json:"title"`
	Note           string                        `json:"note"`
	Status         string                        `json:"status"`
	Creator        string                        `json:"creator"`
	CreatedBy      int64                         `json:"createdBy"`
	CreatedAt      time.Time                     `json:"createdAt"`
	Reviewer       string                        `json:"reviewer,omitempty"`
	ReviewedBy     *int64                        `json:"reviewedBy,omitempty"`
	ReviewedAt     *time.Time                    `json:"reviewedAt,omitempty"`
	ReviewNote     string                        `json:"reviewNote"`
	SnapshotDigest string                        `json:"snapshotDigest"`
	ItemCount      int                           `json:"itemCount"`
	Items          []ClassificationWorkbenchItem `json:"items,omitempty"`
}

const classificationPublicationColumns = `p.id,p.title,p.note,p.status,p.created_by,creator.username,p.created_at,p.reviewed_by,COALESCE(reviewer.username,''),p.reviewed_at,p.review_note,p.snapshot_digest,(SELECT count(*) FROM plume.classification_publication_items i WHERE i.publication_id=p.id)`
const classificationPublicationFrom = ` FROM plume.classification_publications p JOIN plume.users creator ON creator.id=p.created_by LEFT JOIN plume.users reviewer ON reviewer.id=p.reviewed_by `

func scanClassificationPublication(row pgx.Row) (ClassificationPublication, error) {
	var p ClassificationPublication
	e := row.Scan(&p.ID, &p.Title, &p.Note, &p.Status, &p.CreatedBy, &p.Creator, &p.CreatedAt, &p.ReviewedBy, &p.Reviewer, &p.ReviewedAt, &p.ReviewNote, &p.SnapshotDigest, &p.ItemCount)
	return p, e
}

func readClassificationPublication(ctx context.Context, tx pgx.Tx, ws, id int64) (ClassificationPublication, error) {
	p, e := scanClassificationPublication(tx.QueryRow(ctx, `SELECT `+classificationPublicationColumns+classificationPublicationFrom+` WHERE p.workspace_id=$1 AND p.id=$2`, ws, id))
	if e != nil {
		return p, e
	}
	rows, e := tx.Query(ctx, `SELECT snapshot FROM plume.classification_publication_items WHERE workspace_id=$1 AND publication_id=$2 ORDER BY asset_id`, ws, id)
	if e != nil {
		return p, e
	}
	p.Items, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClassificationWorkbenchItem, error) {
		var item ClassificationWorkbenchItem
		e := row.Scan(&item)
		return item, e
	})
	return p, e
}

func (s *Store) ListClassificationPublications(ctx context.Context, ws, actor int64) ([]ClassificationPublication, error) {
	result := []ClassificationPublication{}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT `+classificationPublicationColumns+classificationPublicationFrom+` WHERE p.workspace_id=$1 ORDER BY p.id DESC LIMIT 50`, ws)
		if e != nil {
			return e
		}
		result, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClassificationPublication, error) {
			return scanClassificationPublication(row)
		})
		return e
	})
	return result, storeError(e)
}

func (s *Store) GetClassificationPublication(ctx context.Context, ws, actor, id int64) (ClassificationPublication, error) {
	var p ClassificationPublication
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var e error
		p, e = readClassificationPublication(ctx, tx, ws, id)
		return e
	})
	return p, storeError(e)
}

func lockClassificationPublicationAssets(ctx context.Context, tx pgx.Tx, ws int64, ids []int64) error {
	// A taxonomy edit takes an exclusive workspace lock. Keep the tree stable
	// while reading the reviewed asset snapshots and checking their lineage.
	if _, e := tx.Exec(ctx, `SELECT id FROM plume.workspaces WHERE id=$1 FOR SHARE`, ws); e != nil {
		return e
	}
	// Collector submissions lock their source before touching assets. Follow
	// that order here so a publication cannot deadlock an arriving poll.
	rows, e := tx.Query(ctx, `SELECT ds.id FROM plume.discovery_sources ds WHERE ds.workspace_id=$1 AND ds.id IN(SELECT source_id FROM plume.catalog_entries WHERE workspace_id=$1 AND asset_id=ANY($2)) ORDER BY ds.id FOR SHARE`, ws, ids)
	if e != nil {
		return e
	}
	_, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (int64, error) { var id int64; e := row.Scan(&id); return id, e })
	if e != nil {
		return e
	}
	rows, e = tx.Query(ctx, `SELECT id FROM plume.assets WHERE workspace_id=$1 AND id=ANY($2) ORDER BY id FOR NO KEY UPDATE`, ws, ids)
	if e != nil {
		return e
	}
	locked, e := pgx.CollectRows(rows, func(row pgx.CollectableRow) (int64, error) { var id int64; e := row.Scan(&id); return id, e })
	if e != nil {
		return e
	}
	if len(locked) != len(ids) {
		return ErrNotFound
	}
	return nil
}

func validClassificationPublicationItem(item ClassificationWorkbenchItem) error {
	if item.Revision < 1 || item.Origin != "manual" || item.Asset.Category == "unknown" || item.Asset.BusinessLevel == "unknown" || item.Standard == nil {
		return ErrInvalid
	}
	if item.NeedsReview || item.TaxonomyStale || item.Catalog != nil && !item.Catalog.Current || item.Automatic != nil && (item.Automatic.Stale || item.Automatic.Comparison == "conflict") {
		return ErrConflict
	}
	return nil
}

func (s *Store) CreateClassificationPublication(ctx context.Context, ws, actor int64, input ClassificationPublicationInput) (ClassificationPublication, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Note = strings.TrimSpace(input.Note)
	selected, e := classificationRevisionItems(input.Items)
	if e != nil || utf8.RuneCountInString(input.Title) < 1 || utf8.RuneCountInString(input.Title) > 120 || utf8.RuneCountInString(input.Note) > 1000 || strings.ContainsAny(input.Title, "\r\n\x00") || strings.ContainsRune(input.Note, 0) {
		return ClassificationPublication{}, ErrInvalid
	}
	ids := make([]int64, len(selected))
	for i, v := range selected {
		ids[i] = v.AssetID
	}
	var result ClassificationPublication
	e = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, ws, actor, true)
		if e != nil {
			return e
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		if e = lockClassificationPublicationAssets(ctx, tx, ws, ids); e != nil {
			return e
		}
		items, e := readClassificationWorkbenchItems(ctx, tx, ws, ids)
		if e != nil {
			return e
		}
		actors := make([]int64, len(items))
		standards, e := classificationstandards.List()
		if e != nil {
			return e
		}
		for i, item := range items {
			if item.Revision != selected[i].Revision {
				return ErrConflict
			}
			if e = validClassificationPublicationItem(item); e != nil {
				return e
			}
			for _, standard := range standards {
				if standard.ID == item.Standard.ID {
					copy := standard
					items[i].StandardDefinition = &copy
					break
				}
			}
			if items[i].StandardDefinition == nil {
				return ErrConflict
			}
			if e = tx.QueryRow(ctx, `SELECT actor_id FROM plume.asset_classification_history WHERE workspace_id=$1 AND asset_id=$2 AND revision=$3 AND origin='manual'`, ws, item.Asset.ID, item.Revision).Scan(&actors[i]); e != nil {
				return e
			}
		}
		encoded, e := json.Marshal(items)
		if e != nil {
			return e
		}
		digest := sha256.Sum256(encoded)
		var id int64
		if e = tx.QueryRow(ctx, `INSERT INTO plume.classification_publications(workspace_id,title,note,created_by,snapshot_digest) VALUES($1,$2,$3,$4,$5) RETURNING id`, ws, input.Title, input.Note, actor, hex.EncodeToString(digest[:])).Scan(&id); e != nil {
			return e
		}
		for i, item := range items {
			if _, e = tx.Exec(ctx, `INSERT INTO plume.classification_publication_items(workspace_id,publication_id,asset_id,revision,confirmed_by,snapshot,facts) VALUES($1,$2,$3,$4,$5,$6,$7)`, ws, id, item.Asset.ID, item.Revision, actors[i], item, item.Facts); e != nil {
				return e
			}
		}
		result, e = readClassificationPublication(ctx, tx, ws, id)
		return e
	})
	return result, storeError(e)
}

func (s *Store) ReviewClassificationPublication(ctx context.Context, ws, actor, id int64, input ClassificationPublicationReviewInput) (ClassificationPublication, error) {
	input.Note = strings.TrimSpace(input.Note)
	if id < 1 || input.Action != "publish" && input.Action != "reject" || utf8.RuneCountInString(input.Note) > 1000 || strings.ContainsRune(input.Note, 0) {
		return ClassificationPublication{}, ErrInvalid
	}
	var result ClassificationPublication
	e := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, ws, actor, true)
		if e != nil {
			return e
		}
		if role != "owner" && role != "reviewer" {
			return ErrForbidden
		}
		var creator int64
		var status string
		if e = tx.QueryRow(ctx, `SELECT created_by,status FROM plume.classification_publications WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, id).Scan(&creator, &status); e != nil {
			return e
		}
		if creator == actor {
			return ErrForbidden
		}
		if status != "submitted" {
			return ErrConflict
		}
		frozen, e := readClassificationPublication(ctx, tx, ws, id)
		if e != nil {
			return e
		}
		ids := make([]int64, len(frozen.Items))
		for i, item := range frozen.Items {
			ids[i] = item.Asset.ID
		}
		if e = lockClassificationPublicationAssets(ctx, tx, ws, ids); e != nil {
			if errors.Is(e, ErrNotFound) {
				if input.Action == "publish" {
					return ErrConflict
				}
			} else {
				return e
			}
		}
		var independent bool
		if e = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM plume.classification_publication_items i LEFT JOIN plume.assets a ON a.id=i.asset_id AND a.workspace_id=i.workspace_id LEFT JOIN plume.asset_classification_history h ON h.workspace_id=a.workspace_id AND h.asset_id=a.id AND h.revision=a.classification_revision AND h.origin='manual' WHERE i.workspace_id=$1 AND i.publication_id=$2 AND (i.confirmed_by=$3 OR h.actor_id=$3))`, ws, id, actor).Scan(&independent); e != nil {
			return e
		}
		if !independent {
			return ErrForbidden
		}
		if input.Action == "publish" {
			current, e := readClassificationWorkbenchItems(ctx, tx, ws, ids)
			if e != nil {
				return e
			}
			if len(current) != len(frozen.Items) {
				return ErrConflict
			}
			for i, item := range current {
				if item.Revision != frozen.Items[i].Revision {
					return ErrConflict
				}
				if e = validClassificationPublicationItem(item); e != nil {
					return e
				}
				var before, now any
				if e = json.Unmarshal(frozen.Items[i].Facts, &before); e != nil {
					return e
				}
				if e = json.Unmarshal(item.Facts, &now); e != nil {
					return e
				}
				if !reflect.DeepEqual(before, now) {
					return ErrConflict
				}
			}
			status = "published"
		} else {
			status = "rejected"
		}
		if _, e = tx.Exec(ctx, `UPDATE plume.classification_publications SET status=$3,reviewed_by=$4,reviewed_at=clock_timestamp(),review_note=$5 WHERE workspace_id=$1 AND id=$2`, ws, id, status, actor, input.Note); e != nil {
			return e
		}
		result, e = readClassificationPublication(ctx, tx, ws, id)
		return e
	})
	return result, storeError(e)
}
