package store

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

type ApplicationSourceInput struct {
	Name          string  `json:"name"`
	AssetIDs      []int64 `json:"assetIds"`
	MaxAgeMinutes int     `json:"maxAgeMinutes"`
}
type ApplicationSource struct {
	ApplicationSourceInput
	ID             int64      `json:"id"`
	Status         string     `json:"status"`
	LastObservedAt *time.Time `json:"lastObservedAt,omitempty"`
	RevokedAt      *time.Time `json:"revokedAt,omitempty"`
}
type ApplicationObservation struct {
	AssetID int64             `json:"assetId"`
	Facts   map[string]string `json:"facts"`
}
type ApplicationSubmission struct {
	BatchID    string                   `json:"batchId"`
	ObservedAt time.Time                `json:"observedAt"`
	Status     string                   `json:"status"`
	Items      []ApplicationObservation `json:"items"`
}
type ApplicationReceipt struct {
	ID       int64  `json:"id"`
	Status   string `json:"status"`
	Replayed bool   `json:"replayed"`
}

var applicationFactKeys = []string{"application_name", "application_vendor", "application_model", "application_region", "application_retention", "application_training_use", "application_service_audience", "application_knowledge_bases", "application_tools", "application_data_categories"}

var applicationCredentialValue = regexp.MustCompile(`(?i)(?:\bbearer\s+\S{8,}|(?:api[_ -]?key|access[_ -]?token|refresh[_ -]?token|client[_ -]?secret|password|passwd|authorization|credential)["']?\s*[:=]\s*\S+|-----BEGIN[^\r\n]*PRIVATE KEY-----|\b(?:sk-(?:proj-|ant-)?|gh[opusr]_|github_pat_|xox[baprs]-|AIza)[A-Za-z0-9_-]{12,}|\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})`)
var applicationCloudAccessKey = regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)
var applicationBasicAuthorization = regexp.MustCompile(`(?i)\bbasic\s+([A-Za-z0-9+/]+={0,2})`)
var applicationMetadataURL = regexp.MustCompile(`https?://[^\s<>"']+`)

func validApplicationFact(value string) bool {
	if !validOptionalActivityText(value, 1000) || strings.IndexFunc(value, unicode.IsControl) >= 0 || applicationCredentialValue.MatchString(value) || applicationCloudAccessKey.MatchString(value) {
		return false
	}
	for _, match := range applicationBasicAuthorization.FindAllStringSubmatch(value, -1) {
		decoded, err := base64.StdEncoding.DecodeString(match[1])
		if err == nil && strings.ContainsRune(string(decoded), ':') {
			return false
		}
	}
	for _, raw := range applicationMetadataURL.FindAllString(value, -1) {
		u, err := url.Parse(raw)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return false
		}
	}
	return true
}

func validApplicationSubmission(in ApplicationSubmission) bool {
	if !validCheckText(in.BatchID, 100) || !slices.Contains([]string{"success", "error"}, in.Status) || len(in.Items) > 50 || in.ObservedAt.IsZero() || in.ObservedAt.After(time.Now().Add(5*time.Minute)) || in.Status == "error" && len(in.Items) != 0 {
		return false
	}
	seen := map[int64]bool{}
	for _, item := range in.Items {
		if item.AssetID < 1 || seen[item.AssetID] || len(item.Facts) == 0 || len(item.Facts) > len(applicationFactKeys) {
			return false
		}
		seen[item.AssetID] = true
		for key, value := range item.Facts {
			if !slices.Contains(applicationFactKeys, key) || !validApplicationFact(value) {
				return false
			}
		}
	}
	return true
}

const applicationSourceSelect = `SELECT s.id,s.name,s.asset_ids,s.max_age_minutes,s.revoked_at,b.observed_at,
 CASE WHEN s.revoked_at IS NOT NULL THEN 'revoked'
 WHEN NOT EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=s.workspace_id AND m.user_id=s.actor_id AND m.role='owner') THEN 'revoked'
 WHEN b.id IS NULL THEN 'never_run' WHEN b.status<>'success' THEN 'failed'
 WHEN b.observed_at+make_interval(mins=>s.max_age_minutes)<now() THEN 'stale' ELSE 'current' END
 FROM plume.application_sources s LEFT JOIN plume.application_batches b ON b.source_id=s.id AND b.id=s.latest_batch_id AND b.workspace_id=s.workspace_id WHERE s.workspace_id=$1`

func scanApplicationSource(row pgx.Row) (ApplicationSource, error) {
	var result ApplicationSource
	err := row.Scan(&result.ID, &result.Name, &result.AssetIDs, &result.MaxAgeMinutes, &result.RevokedAt, &result.LastObservedAt, &result.Status)
	return result, storeError(err)
}
func (s *Store) ListApplicationSources(ctx context.Context, ws, actor int64) ([]ApplicationSource, error) {
	result := []ApplicationSource{}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, ws, actor, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, applicationSourceSelect+` ORDER BY s.id DESC LIMIT 200`, ws)
		if err != nil {
			return err
		}
		result, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ApplicationSource, error) { return scanApplicationSource(row) })
		return err
	})
	return result, storeError(err)
}
func (s *Store) CreateApplicationSource(ctx context.Context, ws, actor int64, in ApplicationSourceInput, digest string) (ApplicationSource, error) {
	var result ApplicationSource
	in.Name = strings.TrimSpace(in.Name)
	if in.MaxAgeMinutes == 0 {
		in.MaxAgeMinutes = 60
	}
	if !validCheckText(in.Name, 80) || len(in.AssetIDs) < 1 || len(in.AssetIDs) > 50 || in.MaxAgeMinutes < 5 || in.MaxAgeMinutes > 43200 || !digestPattern.MatchString(digest) {
		return result, ErrInvalid
	}
	seen := map[int64]bool{}
	for _, id := range in.AssetIDs {
		if id < 1 || seen[id] {
			return result, ErrInvalid
		}
		seen[id] = true
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, ws, actor, true)
		if err != nil {
			return err
		}
		if role != "owner" {
			return ErrForbidden
		}
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM plume.assets WHERE workspace_id=$1 AND id=ANY($2)`, ws, in.AssetIDs).Scan(&count); err != nil {
			return err
		}
		if count != len(in.AssetIDs) {
			return ErrNotFound
		}
		var id int64
		err = tx.QueryRow(ctx, `INSERT INTO plume.application_sources(workspace_id,actor_id,name,asset_ids,max_age_minutes,token_digest)VALUES($1,$2,$3,$4,$5,$6)RETURNING id`, ws, actor, in.Name, in.AssetIDs, in.MaxAgeMinutes, digest).Scan(&id)
		if err != nil {
			return err
		}
		result, err = scanApplicationSource(tx.QueryRow(ctx, applicationSourceSelect+` AND s.id=$2`, ws, id))
		return err
	})
	return result, storeError(err)
}
func (s *Store) RevokeApplicationSource(ctx context.Context, ws, actor, id int64) error {
	return storeError(pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, ws, actor, true)
		if err != nil {
			return err
		}
		if role != "owner" {
			return ErrForbidden
		}
		tag, err := tx.Exec(ctx, `UPDATE plume.application_sources SET revoked_at=COALESCE(revoked_at,now()) WHERE workspace_id=$1 AND id=$2`, ws, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return nil
	}))
}
func (s *Store) ReceiveApplicationObservation(ctx context.Context, digest string, in ApplicationSubmission) (ApplicationReceipt, error) {
	var result ApplicationReceipt
	if !digestPattern.MatchString(digest) || !validApplicationSubmission(in) {
		return result, ErrInvalid
	}
	if in.Items == nil {
		in.Items = []ApplicationObservation{}
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ws, source, actor int64
		var scope []int64
		var age int
		var latest *int64
		err := tx.QueryRow(ctx, `SELECT id,workspace_id,actor_id,asset_ids,max_age_minutes,latest_batch_id FROM plume.application_sources WHERE token_digest=$1 AND revoked_at IS NULL FOR UPDATE`, digest).Scan(&source, &ws, &actor, &scope, &age, &latest)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		if err != nil {
			return err
		}
		role, err := checkRole(ctx, tx, ws, actor, false)
		if err != nil || role != "owner" {
			return ErrForbidden
		}
		var oldDigest string
		err = tx.QueryRow(ctx, `SELECT id,payload_digest,status FROM plume.application_batches WHERE source_id=$1 AND batch_id=$2`, source, in.BatchID).Scan(&result.ID, &oldDigest, &result.Status)
		payloadDigest := assessmentDigest(in)
		if err == nil {
			if oldDigest != payloadDigest {
				return ErrConflict
			}
			result.Replayed = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if in.ObservedAt.Before(time.Now().Add(-time.Duration(age) * time.Minute)) {
			return ErrInvalid
		}
		if latest != nil {
			var at time.Time
			if err = tx.QueryRow(ctx, `SELECT observed_at FROM plume.application_batches WHERE source_id=$1 AND id=$2`, source, *latest).Scan(&at); err != nil {
				return err
			}
			if in.ObservedAt.Before(at) {
				return ErrConflict
			}
		}
		for _, item := range in.Items {
			if !slices.Contains(scope, item.AssetID) {
				return ErrForbidden
			}
			var exists bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.assets WHERE workspace_id=$1 AND id=$2)`, ws, item.AssetID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrNotFound
			}
		}
		err = tx.QueryRow(ctx, `INSERT INTO plume.application_batches(workspace_id,source_id,batch_id,payload_digest,status,observed_at,items)VALUES($1,$2,$3,$4,$5,$6,$7)RETURNING id`, ws, source, in.BatchID, payloadDigest, in.Status, in.ObservedAt, in.Items).Scan(&result.ID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE plume.application_sources SET latest_batch_id=$2 WHERE id=$1`, source, result.ID)
		result.Status = in.Status
		return err
	})
	return result, storeError(err)
}

// Application configuration is a source observation, never a legal or contractual conclusion.
func applicationAssessmentFacts(ctx context.Context, tx pgx.Tx, ws, asset int64) ([]AssessmentFact, error) {
	rows, err := tx.Query(ctx, `SELECT s.id,s.name,b.status,b.observed_at,b.items,
 b.observed_at+make_interval(mins=>s.max_age_minutes)>=now()
 FROM plume.application_sources s LEFT JOIN plume.application_batches b ON b.source_id=s.id AND b.id=s.latest_batch_id AND b.workspace_id=s.workspace_id
 WHERE s.workspace_id=$1 AND $2=ANY(s.asset_ids) AND s.revoked_at IS NULL
 AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=s.workspace_id AND m.user_id=s.actor_id AND m.role='owner') ORDER BY s.id`, ws, asset)
	if err != nil {
		return nil, err
	}
	type observation struct {
		id      int64
		name    string
		status  *string
		at      *time.Time
		items   []ApplicationObservation
		current *bool
	}
	observations, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (observation, error) {
		var o observation
		err := row.Scan(&o.id, &o.name, &o.status, &o.at, &o.items, &o.current)
		return o, err
	})
	if err != nil {
		return nil, err
	}
	// One fact per key prevents downstream prefilling from choosing the first
	// source when two current sources disagree.
	claims := make(map[string][]AssessmentFact)
	records := []string{}
	for _, o := range observations {
		records = append(records, fmt.Sprintf("application:%d", o.id))
		var selected *ApplicationObservation
		if o.current != nil && *o.current && o.status != nil && *o.status == "success" {
			for i := range o.items {
				if o.items[i].AssetID == asset {
					selected = &o.items[i]
					break
				}
			}
		}
		for _, key := range applicationFactKeys {
			f := AssessmentFact{Key: key, State: "unknown", Source: "application_api", Record: fmt.Sprintf("application:%d", o.id), ObservedAt: o.at}
			if selected != nil {
				if value := strings.TrimSpace(selected.Facts[key]); value != "" && value != "unknown" {
					f.State = "known"
					f.Value = value
					f.Record = fmt.Sprintf("application:%d/config:%s", o.id, assessmentDigest(selected.Facts))
				}
			}
			if f.State == "known" {
				claims[key] = append(claims[key], f)
			}
		}
	}
	result := []AssessmentFact{}
	if len(observations) == 0 {
		return result, nil
	}
	for _, key := range applicationFactKeys {
		fact := AssessmentFact{Key: key, State: "unknown", Source: "application_api", Record: strings.Join(records, ",")}
		values := claims[key]
		if len(values) > 0 {
			fact = values[0]
			refs := []string{}
			conflict := false
			for _, value := range values {
				refs = append(refs, value.Record)
				conflict = conflict || value.Value != fact.Value
				if value.ObservedAt != nil && (fact.ObservedAt == nil || value.ObservedAt.Before(*fact.ObservedAt)) {
					fact.ObservedAt = value.ObservedAt
				}
			}
			fact.Record = strings.Join(refs, ",")
			if conflict {
				fact.State, fact.Source = "unknown", "application_api_conflict"
				// Claims remain inspectable, but cannot become an automatic answer.
				fact.Value = assessmentFactJSON(values)
			}
		}
		result = append(result, fact)
	}
	result = append(result, applicationConfigurationSummary(result, strings.Join(records, ",")))
	return result, nil
}

func applicationConfigurationSummary(facts []AssessmentFact, record string) AssessmentFact {
	result := AssessmentFact{Key: "application_configuration", State: "unknown", Source: "application_api", Record: record}
	labels := []string{"应用名称", "供应商", "模型", "配置地域", "配置留存", "训练使用设置", "服务对象声明", "知识库配置", "工具配置", "数据类别声明"}
	lines := []string{"应用/API返回的配置声明，尚需与合同和实际处理情况核对。"}
	known := false
	for i, key := range applicationFactKeys {
		value := "未获取"
		for _, fact := range facts {
			if fact.Key != key {
				continue
			}
			if fact.Source == "application_api_conflict" {
				result.Source = "application_api_conflict"
				return result
			}
			if fact.State == "known" {
				known, value = true, fact.Value
				if runes := []rune(value); len(runes) > 80 {
					value = string(runes[:80]) + "…（节选，完整值见来源事实）"
				}
				if fact.ObservedAt != nil && (result.ObservedAt == nil || fact.ObservedAt.Before(*result.ObservedAt)) {
					result.ObservedAt = fact.ObservedAt
				}
			}
			break
		}
		lines = append(lines, labels[i]+"："+value)
	}
	if known {
		result.State, result.Value = "known", strings.Join(lines, "\n")
	}
	return result
}
