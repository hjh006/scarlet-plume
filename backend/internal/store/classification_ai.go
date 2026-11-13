package store

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type ClassificationAIProfile struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	BaseURL         string    `json:"baseUrl"`
	Protocol        string    `json:"protocol"`
	Model           string    `json:"model"`
	Enabled         bool      `json:"enabled"`
	AllowMetadata   bool      `json:"allowMetadata"`
	Revision        int64     `json:"revision"`
	HasAPIKey       bool      `json:"hasApiKey"`
	CreatedBy       int64     `json:"createdBy"`
	UpdatedBy       int64     `json:"updatedBy"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	EncryptedAPIKey []byte    `json:"-"`
}
type ClassificationAIProfileInput struct {
	Name            string `json:"name"`
	BaseURL         string `json:"baseUrl"`
	Protocol        string `json:"protocol"`
	Model           string `json:"model"`
	Enabled         bool   `json:"enabled"`
	AllowMetadata   bool   `json:"allowMetadata"`
	Revision        int64  `json:"revision"`
	EncryptedAPIKey []byte `json:"-"`
	ReplaceAPIKey   bool   `json:"-"`
}
type ClassificationEngineSettings struct {
	Mode             string `json:"mode"`
	DefaultProfileID *int64 `json:"defaultProfileId"`
	Revision         int64  `json:"revision"`
}
type ClassificationEngineSettingsInput = ClassificationEngineSettings
type ClassificationAIField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}
type ClassificationAIMetadata struct {
	AssetID              int64                   `json:"assetId"`
	Revision             int64                   `json:"revision"`
	Name                 string                  `json:"name"`
	System               string                  `json:"system"`
	Kind                 string                  `json:"kind"`
	Fields               []ClassificationAIField `json:"fields"`
	CurrentCategory      string                  `json:"currentCategory"`
	CurrentBusinessLevel string                  `json:"currentBusinessLevel"`
}
type ClassificationAISuggestionInput struct {
	AssetID          int64                              `json:"assetId"`
	AssetRevision    int64                              `json:"assetRevision"`
	ProfileID        int64                              `json:"profileId"`
	ProfileRevision  int64                              `json:"profileRevision"`
	Model            string                             `json:"model"`
	Category         string                             `json:"category"`
	BusinessLevel    string                             `json:"businessLevel"`
	Rationale        string                             `json:"rationale"`
	KnowledgeContext []ClassificationKnowledgeReference `json:"knowledgeContext,omitempty"`
}
type ClassificationAISuggestion struct {
	ClassificationAISuggestionInput
	ID        int64     `json:"id"`
	CreatedBy int64     `json:"createdBy"`
	Actor     string    `json:"actor"`
	CreatedAt time.Time `json:"createdAt"`
}

const classificationAIProfileColumns = `id,name,base_url,protocol,model,enabled,allow_metadata,revision,octet_length(encrypted_api_key)>0,created_by,updated_by,created_at,updated_at`

func scanClassificationAIProfile(row pgx.Row, secret bool) (ClassificationAIProfile, error) {
	var p ClassificationAIProfile
	dest := []any{&p.ID, &p.Name, &p.BaseURL, &p.Protocol, &p.Model, &p.Enabled, &p.AllowMetadata, &p.Revision, &p.HasAPIKey, &p.CreatedBy, &p.UpdatedBy, &p.CreatedAt, &p.UpdatedAt}
	if secret {
		dest = append(dest, &p.EncryptedAPIKey)
	}
	e := row.Scan(dest...)
	return p, e
}
func classificationAIEditor(ctx context.Context, tx pgx.Tx, ws, actor int64, owner bool) error {
	role, e := checkRole(ctx, tx, ws, actor, true)
	if e != nil {
		return e
	}
	if owner && role != "owner" || !owner && !checkEditor(role) {
		return ErrForbidden
	}
	return nil
}
func (s *Store) ListClassificationAIProfiles(ctx context.Context, ws, actor int64) ([]ClassificationAIProfile, error) {
	items := []ClassificationAIProfile{}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT `+classificationAIProfileColumns+` FROM plume.classification_ai_profiles WHERE workspace_id=$1 ORDER BY id LIMIT 50`, ws)
		if e != nil {
			return e
		}
		items, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClassificationAIProfile, error) {
			return scanClassificationAIProfile(row, false)
		})
		return e
	})
	return items, storeError(e)
}
func (s *Store) SaveClassificationAIProfile(ctx context.Context, ws, actor, id int64, input ClassificationAIProfileInput) (ClassificationAIProfile, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	input.Model = strings.TrimSpace(input.Model)
	u, e := url.Parse(input.BaseURL)
	if id < 0 || input.Revision < 0 || !validCheckText(input.Name, 80) || !validCheckText(input.Model, 160) || len(input.BaseURL) > 2048 || e != nil || u.Hostname() == "" || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(input.BaseURL, "\r\n\x00") || input.Protocol != "chat_completions" && input.Protocol != "responses" || len(input.EncryptedAPIKey) > 16384 || !input.ReplaceAPIKey && len(input.EncryptedAPIKey) > 0 {
		return ClassificationAIProfile{}, ErrInvalid
	}
	var out ClassificationAIProfile
	e = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if e := classificationAIEditor(ctx, tx, ws, actor, true); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `SELECT id FROM plume.workspaces WHERE id=$1 FOR UPDATE`, ws); e != nil {
			return e
		}
		key := input.EncryptedAPIKey
		if key == nil {
			key = []byte{}
		}
		if id == 0 {
			if input.Revision != 0 {
				return ErrInvalid
			}
			var count int
			if e := tx.QueryRow(ctx, `SELECT count(*) FROM plume.classification_ai_profiles WHERE workspace_id=$1`, ws).Scan(&count); e != nil {
				return e
			}
			if count >= 50 {
				return ErrInvalid
			}
			var e error
			out, e = scanClassificationAIProfile(tx.QueryRow(ctx, `INSERT INTO plume.classification_ai_profiles(workspace_id,name,base_url,protocol,model,enabled,allow_metadata,encrypted_api_key,created_by,updated_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9) RETURNING `+classificationAIProfileColumns, ws, input.Name, input.BaseURL, input.Protocol, input.Model, input.Enabled, input.AllowMetadata, key, actor), false)
			return e
		}
		old, e := scanClassificationAIProfile(tx.QueryRow(ctx, `SELECT `+classificationAIProfileColumns+`,encrypted_api_key FROM plume.classification_ai_profiles WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, id), true)
		if e != nil {
			return e
		}
		if old.Revision != input.Revision {
			return ErrConflict
		}
		if !input.ReplaceAPIKey {
			if old.HasAPIKey && old.BaseURL != input.BaseURL {
				return ErrInvalid
			}
			key = old.EncryptedAPIKey
		}
		out, e = scanClassificationAIProfile(tx.QueryRow(ctx, `UPDATE plume.classification_ai_profiles SET name=$3,base_url=$4,protocol=$5,model=$6,enabled=$7,allow_metadata=$8,encrypted_api_key=$9,revision=revision+1,updated_by=$10,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 RETURNING `+classificationAIProfileColumns, ws, id, input.Name, input.BaseURL, input.Protocol, input.Model, input.Enabled, input.AllowMetadata, key, actor), false)
		return e
	})
	return out, storeError(e)
}
func (s *Store) GetClassificationAIProfileSecret(ctx context.Context, ws, actor, id int64) (ClassificationAIProfile, error) {
	var out ClassificationAIProfile
	e := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if e := classificationAIEditor(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var e error
		out, e = scanClassificationAIProfile(tx.QueryRow(ctx, `SELECT `+classificationAIProfileColumns+`,encrypted_api_key FROM plume.classification_ai_profiles WHERE workspace_id=$1 AND id=$2`, ws, id), true)
		return e
	})
	return out, storeError(e)
}
func readClassificationEngineSettings(ctx context.Context, tx pgx.Tx, ws int64) (ClassificationEngineSettings, error) {
	out := ClassificationEngineSettings{Mode: "local"}
	var e error
	e = tx.QueryRow(ctx, `SELECT mode,default_profile_id,revision FROM plume.classification_engine_settings WHERE workspace_id=$1`, ws).Scan(&out.Mode, &out.DefaultProfileID, &out.Revision)
	if e == pgx.ErrNoRows {
		return ClassificationEngineSettings{Mode: "local"}, nil
	}
	return out, e
}
func (s *Store) GetClassificationEngineSettings(ctx context.Context, ws, actor int64) (ClassificationEngineSettings, error) {
	var out ClassificationEngineSettings
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var e error
		out, e = readClassificationEngineSettings(ctx, tx, ws)
		return e
	})
	return out, storeError(e)
}
func (s *Store) SaveClassificationEngineSettings(ctx context.Context, ws, actor int64, input ClassificationEngineSettingsInput) (ClassificationEngineSettings, error) {
	if input.Revision < 0 || input.Mode != "local" && input.Mode != "hybrid" || input.Mode == "local" && input.DefaultProfileID != nil || input.Mode == "hybrid" && (input.DefaultProfileID == nil || *input.DefaultProfileID < 1) {
		return ClassificationEngineSettings{}, ErrInvalid
	}
	var out ClassificationEngineSettings
	e := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if e := classificationAIEditor(ctx, tx, ws, actor, true); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `SELECT id FROM plume.workspaces WHERE id=$1 FOR UPDATE`, ws); e != nil {
			return e
		}
		old, e := readClassificationEngineSettings(ctx, tx, ws)
		if e != nil {
			return e
		}
		if old.Revision != input.Revision {
			return ErrConflict
		}
		if input.Mode == "hybrid" {
			var allowed bool
			e := tx.QueryRow(ctx, `SELECT enabled AND allow_metadata AND model<>'' FROM plume.classification_ai_profiles WHERE workspace_id=$1 AND id=$2`, ws, *input.DefaultProfileID).Scan(&allowed)
			if e != nil {
				return e
			}
			if !allowed {
				return ErrInvalid
			}
		}
		e = tx.QueryRow(ctx, `INSERT INTO plume.classification_engine_settings(workspace_id,mode,default_profile_id,updated_by) VALUES($1,$2,$3,$4) ON CONFLICT(workspace_id) DO UPDATE SET mode=EXCLUDED.mode,default_profile_id=EXCLUDED.default_profile_id,revision=classification_engine_settings.revision+1,updated_by=EXCLUDED.updated_by,updated_at=clock_timestamp() RETURNING mode,default_profile_id,revision`, ws, input.Mode, input.DefaultProfileID, actor).Scan(&out.Mode, &out.DefaultProfileID, &out.Revision)
		return e
	})
	return out, storeError(e)
}

func (s *Store) ClassificationAIInput(ctx context.Context, ws, actor, assetID, revision int64) (ClassificationAIMetadata, error) {
	if assetID < 1 || revision < 0 {
		return ClassificationAIMetadata{}, ErrInvalid
	}
	out := ClassificationAIMetadata{Fields: []ClassificationAIField{}}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if e := classificationAIEditor(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		e := tx.QueryRow(ctx, `SELECT a.id,a.classification_revision,left(a.name,300),left(a.system,120),COALESCE(ce.kind,'asset'),a.category,a.business_level FROM plume.assets a LEFT JOIN plume.catalog_entries ce ON ce.asset_id=a.id AND ce.workspace_id=a.workspace_id WHERE a.workspace_id=$1 AND a.id=$2`, ws, assetID).Scan(&out.AssetID, &out.Revision, &out.Name, &out.System, &out.Kind, &out.CurrentCategory, &out.CurrentBusinessLevel)
		if e != nil {
			return e
		}
		if out.Revision != revision {
			return ErrConflict
		}
		rows, e := tx.Query(ctx, `SELECT left(f.field_name,120),left(COALESCE(f.metadata->>'type',''),120) FROM plume.catalog_entries parent JOIN plume.catalog_entries f ON f.workspace_id=parent.workspace_id AND f.source=parent.source AND (f.id=parent.id AND parent.kind='field' OR f.parent_ref=parent.source_ref AND parent.kind='table') AND f.kind='field' AND f.source_id IS NOT DISTINCT FROM parent.source_id AND f.run_id IS NOT DISTINCT FROM parent.run_id WHERE parent.workspace_id=$1 AND parent.asset_id=$2 ORDER BY f.id LIMIT 50`, ws, assetID)
		if e != nil {
			return e
		}
		out.Fields, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClassificationAIField, error) {
			var f ClassificationAIField
			e := row.Scan(&f.Name, &f.Type)
			return f, e
		})
		if e != nil {
			return e
		}
		if len(out.Fields) == 0 {
			rows, e = tx.Query(ctx, `SELECT left(field->>'name',120),left(COALESCE(field->>'type',''),120) FROM plume.postgres_asset_inspections pi CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(pi.inspection->'columns')='array' THEN pi.inspection->'columns' ELSE '[]'::jsonb END) field JOIN plume.assets a ON a.id=pi.asset_id WHERE a.workspace_id=$1 AND a.id=$2 ORDER BY field->>'name' LIMIT 50`, ws, assetID)
			if e != nil {
				return e
			}
			out.Fields, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClassificationAIField, error) {
				var f ClassificationAIField
				e := row.Scan(&f.Name, &f.Type)
				return f, e
			})
			return e
		}
		return nil
	})
	if e == nil {
		// Count encoded bytes as metadata identifiers can contain multibyte or
		// escaped characters. Keep the exact preview below the client budget.
		for len(out.Fields) > 0 {
			encoded, _ := json.Marshal(out)
			if len(encoded) <= 24<<10 {
				break
			}
			out.Fields = out.Fields[:len(out.Fields)-1]
		}
	}
	return out, storeError(e)
}
func (s *Store) SaveClassificationAISuggestion(ctx context.Context, ws, actor int64, input ClassificationAISuggestionInput) (ClassificationAISuggestion, error) {
	input.Rationale = strings.TrimSpace(input.Rationale)
	if input.AssetID < 1 || input.AssetRevision < 0 || input.ProfileID < 1 || input.ProfileRevision < 1 || !validCheckText(input.Rationale, 1000) || utf8.RuneCountInString(input.Model) > 160 || !classificationFilterValue(input.Category, "unknown", "business", "personal", "sensitive_personal") || !classificationFilterValue(input.BusinessLevel, "unknown", "internal", "confidential") {
		return ClassificationAISuggestion{}, ErrInvalid
	}
	out := ClassificationAISuggestion{ClassificationAISuggestionInput: input, CreatedBy: actor}
	e := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if e := classificationAIEditor(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `SELECT id FROM plume.workspaces WHERE id=$1 FOR SHARE`, ws); e != nil {
			return e
		}
		settings, e := readClassificationEngineSettings(ctx, tx, ws)
		if e != nil {
			return e
		}
		if settings.Mode != "hybrid" {
			return ErrConflict
		}
		var profileRevision int64
		var model string
		var allowed bool
		e = tx.QueryRow(ctx, `SELECT revision,model,enabled AND allow_metadata FROM plume.classification_ai_profiles WHERE workspace_id=$1 AND id=$2`, ws, input.ProfileID).Scan(&profileRevision, &model, &allowed)
		if e != nil {
			return e
		}
		if !allowed || profileRevision != input.ProfileRevision || model != input.Model {
			return ErrConflict
		}
		var revision int64
		if e = tx.QueryRow(ctx, `SELECT classification_revision FROM plume.assets WHERE workspace_id=$1 AND id=$2 FOR NO KEY UPDATE`, ws, input.AssetID).Scan(&revision); e != nil {
			return e
		}
		if revision != input.AssetRevision {
			return ErrConflict
		}
		if e = validateClassificationKnowledgeContext(ctx, tx, ws, input.KnowledgeContext); e != nil {
			return e
		}
		knowledge := input.KnowledgeContext
		if knowledge == nil {
			knowledge = []ClassificationKnowledgeReference{}
		}
		if e = tx.QueryRow(ctx, `INSERT INTO plume.classification_ai_suggestions(workspace_id,asset_id,asset_revision,profile_id,profile_revision,model,category,business_level,rationale,created_by,knowledge_context) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id,created_at`, ws, input.AssetID, input.AssetRevision, input.ProfileID, input.ProfileRevision, input.Model, input.Category, input.BusinessLevel, input.Rationale, actor, knowledge).Scan(&out.ID, &out.CreatedAt); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT username FROM plume.users WHERE id=$1`, actor).Scan(&out.Actor)
	})
	return out, storeError(e)
}
func (s *Store) ListClassificationAISuggestions(ctx context.Context, ws, actor, assetID int64) ([]ClassificationAISuggestion, error) {
	items := []ClassificationAISuggestion{}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var exists bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.assets WHERE workspace_id=$1 AND id=$2)`, ws, assetID).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return ErrNotFound
		}
		rows, e := tx.Query(ctx, `SELECT s.id,s.asset_id,s.asset_revision,s.profile_id,s.profile_revision,s.model,s.category,s.business_level,s.rationale,s.created_by,u.username,s.created_at,s.knowledge_context FROM plume.classification_ai_suggestions s JOIN plume.users u ON u.id=s.created_by WHERE s.workspace_id=$1 AND s.asset_id=$2 ORDER BY s.id DESC LIMIT 20`, ws, assetID)
		if e != nil {
			return e
		}
		items, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClassificationAISuggestion, error) {
			var s ClassificationAISuggestion
			e := row.Scan(&s.ID, &s.AssetID, &s.AssetRevision, &s.ProfileID, &s.ProfileRevision, &s.Model, &s.Category, &s.BusinessLevel, &s.Rationale, &s.CreatedBy, &s.Actor, &s.CreatedAt, &s.KnowledgeContext)
			if len(s.KnowledgeContext) == 0 {
				s.KnowledgeContext = nil
			}
			return s, e
		})
		return e
	})
	return items, storeError(e)
}
