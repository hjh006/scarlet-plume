package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/jackc/pgx/v5"
)

func aiLoadPack(ctx context.Context, tx pgx.Tx, ws, id int64) (p AISecurityRegressionPack, summary AISecurityRegressionPackSummary, err error) {
	var raw []byte
	var digest string
	err = tx.QueryRow(ctx, `SELECT pack,digest,created_at FROM plume.ai_security_regression_packs WHERE workspace_id=$1 AND id=$2`, ws, id).Scan(&raw, &digest, &summary.CreatedAt)
	if err != nil {
		return p, summary, storeError(err)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil {
		return p, summary, ErrConflict
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return p, summary, ErrConflict
	}
	validated, e := ValidateAISecurityRegressionPack(p, true)
	if e != nil || validated.Pack.Digest != digest {
		return p, summary, ErrConflict
	}
	summary = aiPackSummary(id, p, summary.CreatedAt)
	return p, summary, nil
}

func (s *Store) AISecurityRegressionPacks(ctx context.Context, ws, actor int64) (items []AISecurityRegressionPackSummary, err error) {
	items = []AISecurityRegressionPackSummary{}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT id FROM plume.ai_security_regression_packs WHERE workspace_id=$1 ORDER BY id DESC LIMIT 100`, ws)
		if e != nil {
			return e
		}
		ids := []int64{}
		for rows.Next() {
			var id int64
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, id := range ids {
			_, summary, e := aiLoadPack(ctx, tx, ws, id)
			if e != nil {
				return e
			}
			items = append(items, summary)
		}
		return nil
	})
	return
}
func (s *Store) AISecurityRegressionPack(ctx context.Context, ws, actor, id int64) (out AISecurityRegressionPack, err error) {
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var e error
		out, _, e = aiLoadPack(ctx, tx, ws, id)
		return e
	})
	return
}
func (s *Store) ImportAISecurityRegressionPack(ctx context.Context, ws, actor int64, expected string, p AISecurityRegressionPack) (out AISecurityRegressionPackSummary, err error) {
	validated, e := ValidateAISecurityRegressionPack(p, true)
	if e != nil {
		return out, e
	}
	p = validated.Pack
	if !securityText(expected, 80, false) {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if e := aiEditor(ctx, tx, ws, actor); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `SELECT id FROM plume.workspaces WHERE id=$1 FOR UPDATE`, ws); e != nil {
			return e
		}
		var latest string
		e := tx.QueryRow(ctx, `SELECT version FROM plume.ai_security_regression_packs WHERE workspace_id=$1 AND pack_id=$2 ORDER BY id DESC LIMIT 1`, ws, p.PackID).Scan(&latest)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if latest != expected {
			return ErrConflict
		}
		var count int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM plume.ai_security_regression_packs WHERE workspace_id=$1`, ws).Scan(&count); e != nil {
			return e
		}
		if count >= 1000 {
			return ErrInvalid
		}
		raw, _ := json.Marshal(p)
		var id int64
		var at time.Time
		if e = tx.QueryRow(ctx, `INSERT INTO plume.ai_security_regression_packs(workspace_id,pack_id,version,digest,pack,created_by) VALUES($1,$2,$3,$4,$5,$6) RETURNING id,created_at`, ws, p.PackID, p.Version, p.Digest, raw, actor).Scan(&id, &at); e != nil {
			return storeError(e)
		}
		out = aiPackSummary(id, p, at)
		return nil
	})
	return
}

func (s *Store) RunAISecurityRegression(ctx context.Context, ws, actor, packID int64) (out AISecurityEvaluation, err error) {
	if packID < 0 {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if e := aiEditor(ctx, tx, ws, actor); e != nil {
			return e
		}
		v, e := aiConfiguration(ctx, tx, ws, true)
		if e != nil {
			return e
		}
		if packID == 0 {
			out = aiEvaluateConfiguration(v.Configuration, v.Revision, time.Now().UTC())
			out.GateVersion = "legacy"
			out.EngineVersion = aisecurity.RuntimeProtocolVersion
			out.CoverageGaps = []string{"versioned_pack_required"}
		} else {
			p, summary, e := aiLoadPack(ctx, tx, ws, packID)
			if e != nil {
				return e
			}
			out = aiEvaluateRegression(v.Configuration, v.Revision, p, summary, time.Now().UTC())
			var latest int64
			if e = tx.QueryRow(ctx, `SELECT id FROM plume.ai_security_regression_packs WHERE workspace_id=$1 AND pack_id=$2 ORDER BY id DESC LIMIT 1`, ws, p.PackID).Scan(&latest); e != nil {
				return e
			}
			if latest != packID {
				out.GateEligible = false
				out.Status = "failed"
				out.CoverageGaps = append(out.CoverageGaps, "test_pack_superseded")
			}
		}
		raw, e := json.Marshal(out)
		if e != nil {
			return e
		}
		return tx.QueryRow(ctx, `INSERT INTO plume.ai_security_evaluations(workspace_id,actor_id,configuration_revision,catalog_digest,status,result) VALUES($1,$2,$3,$4,$5,$6) RETURNING id,created_at`, ws, actor, v.Revision, out.CatalogDigest, out.Status, raw).Scan(&out.ID, &out.CreatedAt)
	})
	return
}
func aiLoadEvaluation(ctx context.Context, tx pgx.Tx, ws, id int64) (out AISecurityEvaluation, err error) {
	var raw []byte
	var at time.Time
	err = tx.QueryRow(ctx, `SELECT result,created_at FROM plume.ai_security_evaluations WHERE workspace_id=$1 AND id=$2`, ws, id).Scan(&raw, &at)
	if err != nil {
		return out, storeError(err)
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, ErrConflict
	}
	out.ID = id
	out.CreatedAt = at
	return
}
func (s *Store) CompareAISecurityRegression(ctx context.Context, ws, actor, from, to int64) (out AISecurityRegressionComparison, err error) {
	if from < 1 || to < 1 || from == to {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		a, e := aiLoadEvaluation(ctx, tx, ws, from)
		if e != nil {
			return e
		}
		b, e := aiLoadEvaluation(ctx, tx, ws, to)
		if e != nil {
			return e
		}
		out, e = aiCompareRegression(a, b)
		return e
	})
	return
}
