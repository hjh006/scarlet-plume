package store

import (
	"context"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationml"
	"github.com/jackc/pgx/v5"
)

type ClassificationKnowledgeInput struct {
	Name      string `json:"name"`
	SourceURL string `json:"sourceUrl"`
	Content   string `json:"content"`
	Enabled   bool   `json:"enabled"`
	AllowAI   bool   `json:"allowAi"`
	Revision  int64  `json:"revision"`
}
type ClassificationKnowledge struct {
	ClassificationKnowledgeInput
	ID        int64     `json:"id"`
	Digest    string    `json:"digest"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type ClassificationKnowledgeReference struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	SourceURL string  `json:"sourceUrl"`
	Revision  int64   `json:"revision"`
	Digest    string  `json:"digest"`
	Excerpt   string  `json:"excerpt"`
	Score     float64 `json:"score"`
}
type ClassificationKnowledgeSelection struct {
	ID       int64  `json:"id"`
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
}

const classificationKnowledgeColumns = "id,name,source_url,content,enabled,allow_ai,revision,digest,created_at,updated_at"

func scanClassificationKnowledge(row pgx.Row) (ClassificationKnowledge, error) {
	var d ClassificationKnowledge
	e := row.Scan(&d.ID, &d.Name, &d.SourceURL, &d.Content, &d.Enabled, &d.AllowAI, &d.Revision, &d.Digest, &d.CreatedAt, &d.UpdatedAt)
	return d, e
}
func readClassificationKnowledge(ctx context.Context, tx pgx.Tx, ws int64) ([]ClassificationKnowledge, error) {
	rows, e := tx.Query(ctx, "SELECT "+classificationKnowledgeColumns+" FROM plume.classification_knowledge WHERE workspace_id=$1 ORDER BY id LIMIT 100", ws)
	if e != nil {
		return nil, e
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClassificationKnowledge, error) { return scanClassificationKnowledge(row) })
}
func (s *Store) ClassificationKnowledge(ctx context.Context, ws, actor int64) ([]ClassificationKnowledge, error) {
	out := []ClassificationKnowledge{}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var e error
		out, e = readClassificationKnowledge(ctx, tx, ws)
		return e
	})
	return out, storeError(e)
}
func (s *Store) SaveClassificationKnowledge(ctx context.Context, ws, actor, id int64, input ClassificationKnowledgeInput) (ClassificationKnowledge, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Content = strings.TrimSpace(input.Content)
	input.SourceURL = strings.TrimSpace(input.SourceURL)
	if id < 0 || !validCheckText(input.Name, 120) || !utf8.ValidString(input.Content) || len(input.Content) == 0 || len(input.Content) > 32768 || len(input.SourceURL) > 2048 || strings.ContainsRune(input.Content, 0) {
		return ClassificationKnowledge{}, ErrInvalid
	}
	if input.SourceURL != "" {
		u, e := url.Parse(input.SourceURL)
		if e != nil || u.Hostname() == "" || u.Scheme != "https" && u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(input.SourceURL, "\r\n\x00") {
			return ClassificationKnowledge{}, ErrInvalid
		}
	}
	digest := classificationml.Digest(struct{ Name, SourceURL, Content string }{input.Name, input.SourceURL, input.Content})
	var out ClassificationKnowledge
	e := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if e := classificationAIEditor(ctx, tx, ws, actor, true); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, "SELECT id FROM plume.workspaces WHERE id=$1 FOR UPDATE", ws); e != nil {
			return e
		}
		if id == 0 {
			if input.Revision != 0 {
				return ErrInvalid
			}
			var count int
			if e := tx.QueryRow(ctx, "SELECT count(*) FROM plume.classification_knowledge WHERE workspace_id=$1", ws).Scan(&count); e != nil {
				return e
			}
			if count >= 100 {
				return ErrInvalid
			}
			var e error
			out, e = scanClassificationKnowledge(tx.QueryRow(ctx, "INSERT INTO plume.classification_knowledge(workspace_id,name,source_url,content,enabled,allow_ai,digest,created_by,updated_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8) RETURNING "+classificationKnowledgeColumns, ws, input.Name, input.SourceURL, input.Content, input.Enabled, input.AllowAI, digest, actor))
			return e
		}
		old, e := scanClassificationKnowledge(tx.QueryRow(ctx, "SELECT "+classificationKnowledgeColumns+" FROM plume.classification_knowledge WHERE workspace_id=$1 AND id=$2 FOR UPDATE", ws, id))
		if e != nil {
			return e
		}
		if old.Revision != input.Revision {
			return ErrConflict
		}
		out, e = scanClassificationKnowledge(tx.QueryRow(ctx, "UPDATE plume.classification_knowledge SET name=$3,source_url=$4,content=$5,enabled=$6,allow_ai=$7,digest=$8,revision=revision+1,updated_by=$9,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 RETURNING "+classificationKnowledgeColumns, ws, id, input.Name, input.SourceURL, input.Content, input.Enabled, input.AllowAI, digest, actor))
		return e
	})
	return out, storeError(e)
}

// Search is a deterministic local lexical retriever. It does not claim vector
// retrieval; selected snippets are exact substrings of the versioned document.
func (s *Store) SearchClassificationKnowledge(ctx context.Context, ws, actor, asset, revision int64) ([]ClassificationKnowledgeReference, error) {
	metadata, e := s.ClassificationAIInput(ctx, ws, actor, asset, revision)
	if e != nil {
		return nil, e
	}
	docs, e := s.ClassificationKnowledge(ctx, ws, actor)
	if e != nil {
		return nil, e
	}
	query := classificationml.Tokens(classificationMetadataText(metadata))
	out := []ClassificationKnowledgeReference{}
	for _, d := range docs {
		if !d.Enabled {
			continue
		}
		best := ""
		bestScore := 0.0
		paragraphs := strings.FieldsFunc(d.Content, func(r rune) bool { return r == '\n' || r == '\r' })
		for _, p := range paragraphs {
			runes := []rune(p)
			for start := 0; start < len(runes); start += 800 {
				end := min(start+1000, len(runes))
				excerpt := string(runes[start:end])
				tokens := classificationml.Tokens(d.Name + " " + excerpt)
				set := map[string]bool{}
				for _, t := range tokens {
					set[t] = true
				}
				matches := 0
				for _, q := range query {
					if set[q] {
						matches++
					}
				}
				score := float64(matches) / math.Sqrt(float64(max(1, len(tokens))))
				if score > bestScore {
					bestScore = score
					best = excerpt
				}
			}
		}
		if bestScore > 0 {
			out = append(out, ClassificationKnowledgeReference{ID: d.ID, Name: d.Name, SourceURL: d.SourceURL, Revision: d.Revision, Digest: d.Digest, Excerpt: best, Score: bestScore})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].ID < out[j].ID
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > 5 {
		out = out[:5]
	}
	return out, nil
}
func (s *Store) SelectClassificationKnowledge(ctx context.Context, ws, actor, asset, revision int64, selected []ClassificationKnowledgeSelection) ([]ClassificationKnowledgeReference, error) {
	if len(selected) == 0 || len(selected) > 5 {
		return nil, ErrInvalid
	}
	refs, e := s.SearchClassificationKnowledge(ctx, ws, actor, asset, revision)
	if e != nil {
		return nil, e
	}
	out := []ClassificationKnowledgeReference{}
	seen := map[int64]bool{}
	for _, v := range selected {
		if seen[v.ID] {
			return nil, ErrInvalid
		}
		seen[v.ID] = true
		found := false
		for _, r := range refs {
			if r.ID == v.ID {
				if r.Revision != v.Revision || r.Digest != v.Digest {
					return nil, ErrConflict
				}
				out = append(out, r)
				found = true
				break
			}
		}
		if !found {
			return nil, ErrConflict
		}
	}
	e = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error { return validateClassificationKnowledgeContext(ctx, tx, ws, out) })
	return out, storeError(e)
}
func validateClassificationKnowledgeContext(ctx context.Context, tx pgx.Tx, ws int64, refs []ClassificationKnowledgeReference) error {
	if len(refs) > 5 {
		return ErrInvalid
	}
	seen := map[int64]bool{}
	for _, r := range refs {
		if seen[r.ID] {
			return ErrInvalid
		}
		seen[r.ID] = true
		d, e := scanClassificationKnowledge(tx.QueryRow(ctx, "SELECT "+classificationKnowledgeColumns+" FROM plume.classification_knowledge WHERE workspace_id=$1 AND id=$2", ws, r.ID))
		if e != nil {
			return e
		}
		if !d.Enabled || !d.AllowAI {
			return ErrForbidden
		}
		if d.Revision != r.Revision || d.Digest != r.Digest || d.Name != r.Name || d.SourceURL != r.SourceURL || r.Excerpt == "" || !strings.Contains(d.Content, r.Excerpt) || math.IsNaN(r.Score) || math.IsInf(r.Score, 0) || r.Score <= 0 {
			return ErrConflict
		}
	}
	return nil
}
