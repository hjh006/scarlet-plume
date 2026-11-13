package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationml"
	"github.com/jackc/pgx/v5"
)

type ClassificationTrainingInput struct {
	Name     string                     `json:"name"`
	MinScore float64                    `json:"minScore"`
	Examples []classificationml.Example `json:"examples"`
}
type ClassificationTrainingModel struct {
	ID            int64                       `json:"id"`
	Name          string                      `json:"name"`
	Algorithm     string                      `json:"algorithm"`
	MinScore      float64                     `json:"minScore"`
	TrainCount    int                         `json:"trainCount"`
	DatasetDigest string                      `json:"datasetDigest"`
	ModelDigest   string                      `json:"modelDigest"`
	Evaluation    classificationml.Evaluation `json:"evaluation"`
	CreatedBy     int64                       `json:"createdBy"`
	Actor         string                      `json:"actor"`
	CreatedAt     time.Time                   `json:"createdAt"`
	model         classificationml.Model
}
type ClassificationTrainingPrediction struct {
	classificationml.Prediction
	ID            int64     `json:"id"`
	ModelID       int64     `json:"modelId"`
	ModelDigest   string    `json:"modelDigest"`
	AssetID       int64     `json:"assetId"`
	AssetRevision int64     `json:"assetRevision"`
	CreatedBy     int64     `json:"createdBy"`
	CreatedAt     time.Time `json:"createdAt"`
}

const classificationTrainingColumns = "m.id,m.name,m.model,m.dataset_digest,m.model_digest,m.evaluation,m.created_by,u.username,m.created_at"

func scanClassificationTraining(row pgx.Row) (ClassificationTrainingModel, error) {
	var m ClassificationTrainingModel
	e := row.Scan(&m.ID, &m.Name, &m.model, &m.DatasetDigest, &m.ModelDigest, &m.Evaluation, &m.CreatedBy, &m.Actor, &m.CreatedAt)
	m.Algorithm = m.model.Algorithm
	m.MinScore = m.model.MinScore
	m.TrainCount = m.model.TrainCount
	return m, e
}
func (s *Store) TrainClassificationModel(ctx context.Context, ws, actor int64, input ClassificationTrainingInput) (ClassificationTrainingModel, error) {
	input.Name = strings.TrimSpace(input.Name)
	if !validCheckText(input.Name, 120) {
		return ClassificationTrainingModel{}, ErrInvalid
	}
	if input.MinScore == 0 {
		input.MinScore = 0.65
	}
	model, ev, e := classificationml.Train(input.Examples, input.MinScore)
	if e != nil {
		return ClassificationTrainingModel{}, ErrInvalid
	}
	out := ClassificationTrainingModel{Name: input.Name, Algorithm: model.Algorithm, MinScore: model.MinScore, TrainCount: model.TrainCount, DatasetDigest: classificationml.Digest(input.Examples), ModelDigest: classificationml.Digest(model), Evaluation: ev, CreatedBy: actor, model: model}
	e = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if e := classificationAIEditor(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, "SELECT id FROM plume.workspaces WHERE id=$1 FOR UPDATE", ws); e != nil {
			return e
		}
		var count int
		if e := tx.QueryRow(ctx, "SELECT count(*) FROM plume.classification_training_models WHERE workspace_id=$1", ws).Scan(&count); e != nil {
			return e
		}
		if count >= 100 {
			return ErrInvalid
		}
		if e := tx.QueryRow(ctx, "INSERT INTO plume.classification_training_models(workspace_id,name,model,dataset,dataset_digest,model_digest,evaluation,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,created_at", ws, input.Name, model, input.Examples, out.DatasetDigest, out.ModelDigest, ev, actor).Scan(&out.ID, &out.CreatedAt); e != nil {
			return e
		}
		return tx.QueryRow(ctx, "SELECT username FROM plume.users WHERE id=$1", actor).Scan(&out.Actor)
	})
	return out, storeError(e)
}
func (s *Store) ClassificationTrainingModels(ctx context.Context, ws, actor int64) ([]ClassificationTrainingModel, error) {
	out := []ClassificationTrainingModel{}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, "SELECT "+classificationTrainingColumns+" FROM plume.classification_training_models m JOIN plume.users u ON u.id=m.created_by WHERE m.workspace_id=$1 ORDER BY m.id DESC LIMIT 100", ws)
		if e != nil {
			return e
		}
		out, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClassificationTrainingModel, error) {
			return scanClassificationTraining(row)
		})
		return e
	})
	return out, storeError(e)
}
func (s *Store) ClassificationTrainingModel(ctx context.Context, ws, actor, id int64) (ClassificationTrainingModel, error) {
	var out ClassificationTrainingModel
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var e error
		out, e = scanClassificationTraining(tx.QueryRow(ctx, "SELECT "+classificationTrainingColumns+" FROM plume.classification_training_models m JOIN plume.users u ON u.id=m.created_by WHERE m.workspace_id=$1 AND m.id=$2", ws, id))
		return e
	})
	return out, storeError(e)
}
func classificationMetadataText(m ClassificationAIMetadata) string {
	var b strings.Builder
	b.WriteString(m.Name)
	b.WriteByte(' ')
	b.WriteString(m.System)
	b.WriteByte(' ')
	b.WriteString(m.Kind)
	for _, f := range m.Fields {
		b.WriteByte(' ')
		b.WriteString(f.Name)
		b.WriteByte(' ')
		b.WriteString(f.Type)
	}
	return b.String()
}
func (s *Store) PredictClassificationModel(ctx context.Context, ws, actor, modelID, assetID, revision int64) (ClassificationTrainingPrediction, error) {
	model, e := s.ClassificationTrainingModel(ctx, ws, actor, modelID)
	if e != nil {
		return ClassificationTrainingPrediction{}, e
	}
	metadata, e := s.ClassificationAIInput(ctx, ws, actor, assetID, revision)
	if e != nil {
		return ClassificationTrainingPrediction{}, e
	}
	out := ClassificationTrainingPrediction{Prediction: model.model.Predict(classificationMetadataText(metadata)), ModelID: modelID, ModelDigest: model.ModelDigest, AssetID: assetID, AssetRevision: revision, CreatedBy: actor}
	e = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if e := classificationAIEditor(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var current int64
		if e := tx.QueryRow(ctx, "SELECT classification_revision FROM plume.assets WHERE workspace_id=$1 AND id=$2 FOR NO KEY UPDATE", ws, assetID).Scan(&current); e != nil {
			return e
		}
		if current != revision {
			return ErrConflict
		}
		raw, e := json.Marshal(out.Prediction)
		if e != nil {
			return e
		}
		return tx.QueryRow(ctx, "INSERT INTO plume.classification_training_predictions(workspace_id,model_id,asset_id,asset_revision,snapshot,created_by) VALUES($1,$2,$3,$4,$5,$6) RETURNING id,created_at", ws, modelID, assetID, revision, raw, actor).Scan(&out.ID, &out.CreatedAt)
	})
	return out, storeError(e)
}
