package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type ClassificationTaxonomyNode struct {
	ID          int64     `json:"id"`
	ParentID    *int64    `json:"parentId"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Enabled     bool      `json:"enabled"`
	Revision    int64     `json:"revision"`
	Path        string    `json:"path"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type ClassificationTaxonomyReference struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Revision int64  `json:"revision"`
	// A lineage signature also catches moves between identically named branches.
	Signature string `json:"signature"`
}

type ClassificationTaxonomyInput struct {
	ParentID    *int64 `json:"parentId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Revision    int64  `json:"revision"`
}

func readClassificationTaxonomy(ctx context.Context, tx pgx.Tx, ws int64) ([]ClassificationTaxonomyNode, error) {
	rows, err := tx.Query(ctx, `SELECT id,parent_id,name,description,enabled,revision,updated_at FROM plume.classification_taxonomy WHERE workspace_id=$1 ORDER BY id`, ws)
	if err != nil {
		return nil, err
	}
	nodes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClassificationTaxonomyNode, error) {
		var n ClassificationTaxonomyNode
		e := row.Scan(&n.ID, &n.ParentID, &n.Name, &n.Description, &n.Enabled, &n.Revision, &n.UpdatedAt)
		return n, e
	})
	if err != nil {
		return nil, err
	}
	for i := range nodes {
		ref, _, e := classificationTaxonomyReference(nodes, nodes[i].ID)
		if e != nil {
			return nil, e
		}
		nodes[i].Path = ref.Path
	}
	return nodes, nil
}

func classificationTaxonomyReference(nodes []ClassificationTaxonomyNode, id int64) (*ClassificationTaxonomyReference, bool, error) {
	byID := make(map[int64]ClassificationTaxonomyNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	leaf, ok := byID[id]
	if !ok {
		return nil, false, ErrInvalid
	}
	names, signature := []string{}, []string{}
	seen := map[int64]bool{}
	enabled := true
	n := leaf
	for {
		if seen[n.ID] || len(names) >= 5 {
			return nil, false, ErrInvalid
		}
		seen[n.ID] = true
		names = append(names, n.Name)
		signature = append(signature, fmt.Sprintf("%d:%d", n.ID, n.Revision))
		enabled = enabled && n.Enabled
		if n.ParentID == nil {
			break
		}
		var exists bool
		n, exists = byID[*n.ParentID]
		if !exists {
			return nil, false, ErrInvalid
		}
	}
	slices.Reverse(names)
	slices.Reverse(signature)
	return &ClassificationTaxonomyReference{ID: leaf.ID, Name: leaf.Name, Path: strings.Join(names, " / "), Revision: leaf.Revision, Signature: strings.Join(signature, "/")}, enabled, nil
}

func (s *Store) ListClassificationTaxonomy(ctx context.Context, ws, actor int64) ([]ClassificationTaxonomyNode, error) {
	var nodes []ClassificationTaxonomyNode
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var e error
		nodes, e = readClassificationTaxonomy(ctx, tx, ws)
		return e
	})
	return nodes, storeError(err)
}

func (s *Store) SaveClassificationTaxonomy(ctx context.Context, ws, actor, id int64, input ClassificationTaxonomyInput) (ClassificationTaxonomyNode, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if id < 0 || input.Revision < 0 || utf8.RuneCountInString(input.Name) < 1 || utf8.RuneCountInString(input.Name) > 80 || utf8.RuneCountInString(input.Description) > 400 || strings.ContainsAny(input.Name, "\r\n\x00") || strings.ContainsRune(input.Description, 0) || input.ParentID != nil && *input.ParentID < 1 {
		return ClassificationTaxonomyNode{}, ErrInvalid
	}
	var result ClassificationTaxonomyNode
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, ws, actor, true)
		if e != nil {
			return e
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		// Serialize edits in a workspace so concurrent moves cannot create a cycle.
		if _, e = tx.Exec(ctx, `SELECT id FROM plume.workspaces WHERE id=$1 FOR UPDATE`, ws); e != nil {
			return e
		}
		nodes, e := readClassificationTaxonomy(ctx, tx, ws)
		if e != nil {
			return e
		}
		index := -1
		for i, n := range nodes {
			if n.ID == id {
				index = i
			}
		}
		if id == 0 {
			if len(nodes) >= 200 || input.Revision != 0 {
				return ErrInvalid
			}
			id = -1
		} else if index < 0 {
			return ErrNotFound
		} else if nodes[index].Revision != input.Revision {
			return ErrConflict
		}
		proposed := ClassificationTaxonomyNode{ID: id, ParentID: input.ParentID, Name: input.Name, Description: input.Description, Enabled: input.Enabled, Revision: input.Revision + 1}
		if index < 0 {
			nodes = append(nodes, proposed)
		} else {
			nodes[index] = proposed
		}
		// Validate every descendant's depth as well as the edited node's parents.
		for _, n := range nodes {
			if _, _, e = classificationTaxonomyReference(nodes, n.ID); e != nil {
				return e
			}
		}
		if id == -1 {
			e = tx.QueryRow(ctx, `INSERT INTO plume.classification_taxonomy(workspace_id,parent_id,name,description,enabled,updated_by) VALUES($1,$2,$3,$4,$5,$6) RETURNING id,updated_at`, ws, input.ParentID, input.Name, input.Description, input.Enabled, actor).Scan(&proposed.ID, &proposed.UpdatedAt)
		} else {
			e = tx.QueryRow(ctx, `UPDATE plume.classification_taxonomy SET parent_id=$3,name=$4,description=$5,enabled=$6,revision=revision+1,updated_by=$7,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 RETURNING updated_at`, ws, id, input.ParentID, input.Name, input.Description, input.Enabled, actor).Scan(&proposed.UpdatedAt)
		}
		if e != nil {
			return e
		}
		if index < 0 {
			nodes[len(nodes)-1] = proposed
		} else {
			nodes[index] = proposed
		}
		ref, _, e := classificationTaxonomyReference(nodes, proposed.ID)
		if e != nil {
			return e
		}
		proposed.Path = ref.Path
		result = proposed
		return nil
	})
	return result, storeError(err)
}

func loadClassificationTaxonomy(ctx context.Context, tx pgx.Tx, ws int64, items []AssetClassificationItem) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]int64, len(items))
	positions := map[int64]int{}
	for i, item := range items {
		ids[i] = item.Asset.ID
		positions[item.Asset.ID] = i
	}
	rows, e := tx.Query(ctx, `SELECT id,classification_taxonomy FROM plume.assets WHERE workspace_id=$1 AND id=ANY($2)`, ws, ids)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var ref *ClassificationTaxonomyReference
		if e = rows.Scan(&id, &ref); e != nil {
			return e
		}
		items[positions[id]].Taxonomy = ref
	}
	if e = rows.Err(); e != nil {
		return e
	}
	rows.Close()
	nodes, e := readClassificationTaxonomy(ctx, tx, ws)
	if e != nil {
		return e
	}
	for i := range items {
		if items[i].Taxonomy != nil {
			current, enabled, e := classificationTaxonomyReference(nodes, items[i].Taxonomy.ID)
			items[i].TaxonomyStale = e != nil || !enabled || current != nil && *current != *items[i].Taxonomy
		}
	}
	return nil
}
