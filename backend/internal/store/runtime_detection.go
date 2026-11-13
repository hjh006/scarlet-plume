package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/jackc/pgx/v5"
)

var runtimeDetectionRules = []string{"sensitive_response", "credential_response", "missing_auth", "subject_bulk_access", "error_spike", "off_hours", "sql_injection_signal", "xss_signal", "path_traversal_signal", "over_collection", "credential_cleartext_transport", "subject_multi_ip", "subject_multi_region", "client_region_mismatch", "recipient_scope", "destination_scope", "volume_limit", "subject_repeated_access"}

type RuntimeDetectionPolicy struct {
	ScopeKey               string   `json:"scopeKey,omitempty"`
	SourceID               int64    `json:"sourceId,omitempty"`
	Method                 string   `json:"method,omitempty"`
	Route                  string   `json:"route,omitempty"`
	SubjectRef             string   `json:"subjectRef,omitempty"`
	ClientIP               string   `json:"clientIp,omitempty"`
	Direction              string   `json:"direction,omitempty"`
	AllowlistConfigured    bool     `json:"allowlistConfigured,omitempty"`
	AllowedCategories      []string `json:"allowedCategories,omitempty"`
	AllowedCountries       []string `json:"allowedCountries,omitempty"`
	AllowedClientCountries []string `json:"allowedClientCountries,omitempty"`
	AllowedRecipients      []string `json:"allowedRecipients,omitempty"`
	AllowedDomains         []string `json:"allowedDomains,omitempty"`
	MaxVolume              int64    `json:"maxVolume,omitempty"`
	VolumeUnit             string   `json:"volumeUnit,omitempty"`

	ID                    int64      `json:"id"`
	Rule                  string     `json:"rule"`
	Revision              int        `json:"revision"`
	Enabled               bool       `json:"enabled"`
	WindowMinutes         int        `json:"windowMinutes"`
	Threshold             int        `json:"threshold"`
	MinRequests           int        `json:"minRequests"`
	ErrorPercent          int        `json:"errorPercent"`
	StartHour             int        `json:"startHour"`
	EndHour               int        `json:"endHour"`
	TimezoneOffsetMinutes int        `json:"timezoneOffsetMinutes"`
	Categories            []string   `json:"categories"`
	CreatedAt             *time.Time `json:"createdAt,omitempty"`
}

type RuntimeDetection struct {
	RetestScope     *RuntimeRetestScope     `json:"retestScope,omitempty"`
	AutomaticRetest *RuntimeAutomaticRetest `json:"automaticRetest,omitempty"`
	SourceID        int64                   `json:"sourceId"`
	Source          string                  `json:"source"`
	Policy          RuntimeDetectionPolicy  `json:"policy"`
	WindowStart     time.Time               `json:"windowStart"`
	WindowEnd       time.Time               `json:"windowEnd"`
	Requests        int                     `json:"requests"`
	Matched         int64                   `json:"matched"`
	Observed        int64                   `json:"observed"`
	SubjectRef      string                  `json:"subjectRef,omitempty"`
	Categories      []string                `json:"categories"`
	Warnings        []string                `json:"warnings"`
}

func defaultRuntimeDetectionPolicies() []RuntimeDetectionPolicy {
	result := make([]RuntimeDetectionPolicy, 0, len(runtimeDetectionRules))
	for _, rule := range runtimeDetectionRules {
		p := RuntimeDetectionPolicy{Rule: rule, Enabled: true, WindowMinutes: 5, Threshold: 1, MinRequests: 1, ErrorPercent: 50, StartHour: 22, EndHour: 8, TimezoneOffsetMinutes: 480, Categories: []string{"email", "phone", "national_id", "financial_account", "personal_name", "address", "ip_address"}}
		if slices.Contains(extendedRuntimeRules, rule) {
			p.Enabled = false
			p.AllowedCategories = []string{}
			p.Direction = "request"
		}
		if rule == "subject_bulk_access" {
			p.Enabled = false
			p.Threshold = 100
			p.MinRequests = 20
		}
		if rule == "error_spike" {
			p.Enabled = false
			p.Threshold = 10
			p.MinRequests = 20
		}
		if rule == "off_hours" {
			p.Enabled = false
		}
		result = append(result, p)
	}
	return result
}

func readRuntimeDetectionPolicies(ctx context.Context, tx pgx.Tx, workspaceID int64) ([]RuntimeDetectionPolicy, error) {
	result := defaultRuntimeDetectionPolicies()
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON(rule,scope_key) id,rule,revision,enabled,config,created_at,scope_key FROM plume.runtime_detection_policies WHERE workspace_id=$1 ORDER BY rule,scope_key,revision DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p RuntimeDetectionPolicy
		var raw []byte
		var at time.Time
		var scopeKey string
		if err := rows.Scan(&p.ID, &p.Rule, &p.Revision, &p.Enabled, &raw, &at, &scopeKey); err != nil {
			return nil, err
		}
		id, rule, revision, enabled := p.ID, p.Rule, p.Revision, p.Enabled
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		p.ID, p.Rule, p.Revision, p.Enabled, p.CreatedAt, p.ScopeKey = id, rule, revision, enabled, &at, scopeKey
		if scopeKey != "" {
			result = append(result, p)
			continue
		}
		for i := range result {
			if result[i].Rule == rule && result[i].ScopeKey == scopeKey {
				result[i] = p
			}
		}
	}
	return result, rows.Err()
}

func (s *Store) RuntimeDetectionPolicies(ctx context.Context, workspaceID, actorID int64) ([]RuntimeDetectionPolicy, error) {
	var result []RuntimeDetectionPolicy
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		var err error
		result, err = readRuntimeDetectionPolicies(ctx, tx, workspaceID)
		return err
	})
	return result, storeError(err)
}

func (s *Store) RuntimeDetectionPolicyHistory(ctx context.Context, workspaceID, actorID int64, rule string) ([]RuntimeDetectionPolicy, error) {
	if !slices.Contains(runtimeDetectionRules, rule) {
		return nil, ErrInvalid
	}
	result := []RuntimeDetectionPolicy{}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,revision,enabled,config,created_at FROM plume.runtime_detection_policies WHERE workspace_id=$1 AND rule=$2 ORDER BY revision DESC LIMIT 30`, workspaceID, rule)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var revision int
			var enabled bool
			var raw []byte
			var at time.Time
			if err := rows.Scan(&id, &revision, &enabled, &raw, &at); err != nil {
				return err
			}
			var p RuntimeDetectionPolicy
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			p.ID, p.Rule, p.Revision, p.Enabled, p.CreatedAt = id, rule, revision, enabled, &at
			result = append(result, p)
		}
		return rows.Err()
	})
	return result, storeError(err)
}

func validateRuntimeDetectionPolicy(p *RuntimeDetectionPolicy) error {
	if e := validateRuntimePolicyScope(p); e != nil {
		return e
	}
	if !slices.Contains(runtimeDetectionRules, p.Rule) || p.ID < 0 || p.Revision < 0 || p.WindowMinutes < 1 || p.WindowMinutes > 60 && p.Rule != "subject_repeated_access" || p.WindowMinutes > 1440 || p.Threshold < 1 || p.Threshold > 10000 || p.MinRequests < 1 || p.MinRequests > 10000 || p.ErrorPercent < 1 || p.ErrorPercent > 100 || p.StartHour < 0 || p.StartHour > 23 || p.EndHour < 0 || p.EndHour > 23 || p.StartHour == p.EndHour || p.TimezoneOffsetMinutes < -720 || p.TimezoneOffsetMinutes > 840 || len(p.Categories) > 8 {
		return ErrInvalid
	}
	p.Categories = slices.Clone(p.Categories)
	slices.Sort(p.Categories)
	p.Categories = slices.Compact(p.Categories)
	for _, category := range p.Categories {
		if !slices.Contains([]string{"email", "phone", "national_id", "financial_account", "credential", "personal_name", "address", "ip_address"}, category) {
			return ErrInvalid
		}
	}
	if p.Rule == "sensitive_response" && len(p.Categories) == 0 {
		return ErrInvalid
	}
	return nil
}

func (s *Store) SaveRuntimeDetectionPolicy(ctx context.Context, workspaceID, actorID int64, input RuntimeDetectionPolicy) (RuntimeDetectionPolicy, error) {
	if err := validateRuntimeDetectionPolicy(&input); err != nil {
		return RuntimeDetectionPolicy{}, err
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if role != "owner" {
			return ErrForbidden
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('plume:runtime-findings',$1))`, workspaceID); err != nil {
			return err
		}
		var revision int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(revision),0) FROM plume.runtime_detection_policies WHERE workspace_id=$1 AND rule=$2 AND scope_key=$3`, workspaceID, input.Rule, input.ScopeKey).Scan(&revision); err != nil {
			return err
		}
		if input.Revision != revision {
			return ErrConflict
		}
		if input.SourceID > 0 {
			var id int64
			if e := tx.QueryRow(ctx, `SELECT id FROM plume.runtime_security_sources WHERE workspace_id=$1 AND id=$2 AND revoked_at IS NULL`, workspaceID, input.SourceID).Scan(&id); e != nil {
				return e
			}
		}
		input.ID = 0
		input.Revision = revision + 1
		input.CreatedAt = nil
		if e := tx.QueryRow(ctx, `INSERT INTO plume.runtime_detection_policies(workspace_id,rule,revision,enabled,config,actor_id,scope_key) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id,created_at`, workspaceID, input.Rule, input.Revision, input.Enabled, input, actorID, input.ScopeKey).Scan(&input.ID, &input.CreatedAt); e != nil {
			return e
		}
		return invalidateExtendedRuntimeReviews(ctx, tx, workspaceID, actorID, 0, input.Rule, input.ScopeKey, "采用策略已修订或暂停，原自动复测失效，需重新核对整改范围。")
	})
	return input, storeError(err)
}

func runtimeDetectionTitle(rule string) string {
	if slices.Contains(extendedRuntimeRules, rule) {
		return runtimePolicyTitle(rule)
	}
	switch rule {
	case "sensitive_response":
		return "响应包含敏感数据格式信号"
	case "credential_response":
		return "响应包含疑似凭据字段"
	case "missing_auth":
		return "需认证接口出现匿名成功响应"
	case "subject_bulk_access":
		return "去标识主体访问频次超过阈值"
	case "error_spike":
		return "接口错误比例超过阈值"
	case "off_hours":
		return "非工作时段访问超过阈值"
	case "sql_injection_signal":
		return "请求出现疑似 SQL 注入模式"
	case "xss_signal":
		return "请求出现疑似 XSS 模式"
	case "path_traversal_signal":
		return "请求出现疑似路径穿越模式"
	}
	return "API 运行时信号"
}

func persistRuntimeDetectionSamples(ctx context.Context, tx pgx.Tx, batch runtimeFindingBatch, observations []runtimesecurity.Observation) error {
	_, err := tx.Exec(ctx, `INSERT INTO plume.runtime_detection_samples(workspace_id,source_id,batch_id,ordinal,observed_at,observation) SELECT $1,$2,$3,n-1,(o->>'observedAt')::timestamptz,o FROM jsonb_array_elements($4::jsonb) WITH ORDINALITY AS x(o,n) ON CONFLICT DO NOTHING`, batch.workspaceID, batch.sourceID, batch.batchID, observations)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM plume.runtime_detection_samples WHERE source_id=$1 AND (observed_at<clock_timestamp()-interval '25 hours' OR id IN(SELECT id FROM plume.runtime_detection_samples WHERE source_id=$1 ORDER BY observed_at DESC,id DESC OFFSET 20000))`, batch.sourceID)
	return err
}

func readRuntimeDetectionSamples(ctx context.Context, tx pgx.Tx, batch runtimeFindingBatch, end time.Time) ([]runtimesecurity.Observation, error) {
	// JSON timestamps round to PostgreSQL microseconds while pgx truncates the
	// bound. Read one extra microsecond at either edge; evaluation below still
	// uses the original JSON nanoseconds for the exact window and future cutoff.
	rows, err := tx.Query(ctx, `SELECT observation FROM plume.runtime_detection_samples WHERE source_id=$1 AND workspace_id=$2 AND observed_at >= $3::timestamptz-interval '25 hours 1 microsecond' AND observed_at <= $3::timestamptz+interval '1 microsecond' ORDER BY observed_at DESC,id DESC LIMIT 20000`, batch.sourceID, batch.workspaceID, end)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (runtimesecurity.Observation, error) {
		var o runtimesecurity.Observation
		err := r.Scan(&o)
		return o, err
	})
}

func runtimePolicySignal(p RuntimeDetectionPolicy, o runtimesecurity.Observation, required bool) (int64, int64, []string) {
	if m, n, c, handled := extendedRuntimePolicySignal(p, o); handled {
		return m, n, c
	}
	if p.Rule == "missing_auth" {
		if o.AuthState == "" || o.AuthState == "unknown" {
			return 0, 0, nil
		}
		if o.AuthState == "anonymous" && o.Status >= 200 && o.Status < 300 && (required || o.AuthRequired != nil && *o.AuthRequired) {
			return 1, 1, nil
		}
		return 0, 1, nil
	}
	if strings.HasSuffix(p.Rule, "_signal") {
		kind := strings.TrimSuffix(p.Rule, "_signal")
		for _, signal := range o.SecuritySignals {
			if signal.Kind == kind {
				return signal.Count, signal.Observed, nil
			}
		}
		return 0, 0, nil
	}
	if p.Rule == "off_hours" {
		hour := o.ObservedAt.In(time.FixedZone("policy", p.TimezoneOffsetMinutes*60)).Hour()
		inside := hour >= p.StartHour && hour < p.EndHour
		if p.StartHour > p.EndHour {
			inside = hour >= p.StartHour || hour < p.EndHour
		}
		if inside {
			return 1, 1, nil
		}
		return 0, 1, nil
	}
	var matched, observed int64
	cats := []string{}
	for _, f := range o.DataFields {
		if f.Location != "response" {
			continue
		}
		if p.Rule == "credential_response" && f.Category == "credential" || p.Rule == "sensitive_response" && f.Method == "format" && slices.Contains(p.Categories, f.Category) {
			matched += f.Count
			observed += f.Observed
			if f.Count > 0 {
				cats = append(cats, f.Category)
			}
		}
	}
	return matched, observed, cats
}

func evaluateRuntimeDetection(p RuntimeDetectionPolicy, events []runtimesecurity.Observation, key runtimeRouteKey, end time.Time, subject string, required bool) RuntimeDetection {
	d := RuntimeDetection{Policy: p, WindowStart: end.Add(-time.Duration(p.WindowMinutes) * time.Minute), WindowEnd: end, Categories: []string{}, Warnings: []string{"connected_source_scope_only", "pattern_or_policy_signal_not_proven_vulnerability"}, SubjectRef: subject}
	seenValues := map[string]bool{}
	for _, o := range events {
		if !runtimeObservationInScope(p, o) {
			continue
		}
		if o.Method != key.method || runtimesecurity.RouteKey(o.Route) != key.route || o.ObservedAt.Before(d.WindowStart) || o.ObservedAt.After(end) || runtimeRuleUsesSubject(p.Rule) && (subject == "" || o.SubjectRef != subject) {
			continue
		}
		d.Requests++
		d.Warnings = append(d.Warnings, o.CaptureWarnings...)
		if p.Rule == "missing_auth" && (o.AuthState == "" || o.AuthState == "unknown") {
			d.Warnings = append(d.Warnings, "auth_state_unknown")
		}
		if p.Rule == "subject_bulk_access" {
			d.Matched++
			d.Observed++
			continue
		}
		if p.Rule == "error_spike" {
			d.Observed++
			if o.Status >= 400 {
				d.Matched++
			}
			continue
		}
		if p.Rule == "subject_multi_ip" && o.SubjectRef != "" && o.ClientIP != "" {
			seenValues[o.ClientIP] = true
		}
		if p.Rule == "subject_multi_region" && o.SubjectRef != "" && o.Context != nil && o.Context.ClientCountry != "" {
			seenValues[o.Context.ClientCountry] = true
		}
		m, n, c := runtimePolicySignal(p, o, required)
		d.Matched += m
		d.Observed += n
		d.Categories = append(d.Categories, c...)
	}
	if p.Rule == "subject_multi_ip" || p.Rule == "subject_multi_region" {
		d.Matched = int64(len(seenValues))
	}
	if slices.Contains(extendedRuntimeRules, p.Rule) {
		d.Warnings = append(d.Warnings, "configured_technical_baseline_not_automatic_legal_conclusion", "source_labels_not_automatically_verified")
	}
	if p.Rule == "credential_response" {
		d.Warnings = append(d.Warnings, "credential_field_name_signal_requires_validation")
	}
	switch p.Rule {
	case "sensitive_response":
		d.Warnings = append(d.Warnings, "recipient_authorization_not_verified", "external_destination_not_observed", "format_not_real_identity")
	case "missing_auth":
		d.Warnings = append(d.Warnings, "source_declared_auth_state")
	case "subject_bulk_access":
		d.Warnings = append(d.Warnings, "requests_not_rows_or_people", "subject_scoped_to_source")
	case "error_spike":
		d.Warnings = append(d.Warnings, "absolute_window_threshold_not_historical_baseline")
	case "off_hours":
		d.Warnings = append(d.Warnings, "fixed_offset_not_business_calendar")
	}
	if len(events) >= 20000 {
		d.Warnings = append(d.Warnings, "source_sample_limit_20000")
	}
	slices.Sort(d.Categories)
	d.Categories = slices.Compact(d.Categories)
	slices.Sort(d.Warnings)
	d.Warnings = slices.Compact(d.Warnings)
	if slices.Contains(extendedRuntimeRules, p.Rule) && runtimeDetectionPositive(d) {
		d.RetestScope = captureRuntimeRetestScope(p, events, key, subject, d.WindowStart, d.WindowEnd)
	}
	return d
}

func runtimeDetectionPositive(d RuntimeDetection) bool {
	threshold := int64(d.Policy.Threshold)
	if d.Policy.Rule == "volume_limit" {
		threshold = d.Policy.MaxVolume + 1
	}
	return d.Policy.Enabled && d.Requests >= d.Policy.MinRequests && d.Matched >= threshold && (d.Policy.Rule != "error_spike" || d.Observed > 0 && d.Matched*100 >= int64(d.Policy.ErrorPercent)*d.Observed)
}

func detectionEvidenceSummary(d RuntimeDetection) string {
	warnings := strings.Join(d.Warnings, ",")
	if len(warnings) > 180 {
		warnings = warnings[:180]
	}
	return fmt.Sprintf("%s。策略 %s v%d（#%d）；窗口 %s 至 %s；同来源请求 %d，命中计数 %d / 可观察分母 %d，类别 %s。范围/缺口：%s。信号不证明漏洞、越权、真实人数或已发生泄露。", runtimeDetectionTitle(d.Policy.Rule), d.Policy.Rule, d.Policy.Revision, d.Policy.ID, d.WindowStart.Format(time.RFC3339), d.WindowEnd.Format(time.RFC3339), d.Requests, d.Matched, d.Observed, strings.Join(d.Categories, ","), warnings)
}

func runtimeDetectionRequired(ctx context.Context, tx pgx.Tx, batch runtimeFindingBatch) (map[runtimeRouteKey]bool, error) {
	rows, err := tx.Query(ctx, `SELECT op.method,op.path,op.auth FROM (SELECT operations FROM plume.api_security_scans WHERE workspace_id=$1 AND source=$2 ORDER BY id DESC LIMIT 1) s CROSS JOIN LATERAL jsonb_to_recordset(s.operations) op(method text,path text,auth text)`, batch.workspaceID, batch.service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[runtimeRouteKey]bool{}
	for rows.Next() {
		var method, path, auth string
		if err := rows.Scan(&method, &path, &auth); err != nil {
			return nil, err
		}
		key := runtimeRouteKey{method, runtimesecurity.RouteKey(path)}
		result[key] = result[key] || auth == "required"
	}
	return result, rows.Err()
}

func persistRuntimeDetection(ctx context.Context, tx pgx.Tx, batch runtimeFindingBatch, scanID int64, observations []runtimesecurity.Observation) (bool, error) {
	if err := persistRuntimeDetectionSamples(ctx, tx, batch, observations); err != nil {
		return false, err
	}
	policies, err := readRuntimeDetectionPolicies(ctx, tx, batch.workspaceID)
	if err != nil {
		return false, err
	}
	required, err := runtimeDetectionRequired(ctx, tx, batch)
	if err != nil {
		return false, err
	}
	var end time.Time
	keys := map[runtimeRouteKey]runtimesecurity.Observation{}
	for _, o := range observations {
		if o.ObservedAt.After(end) {
			end = o.ObservedAt
		}
		key := runtimeRouteKey{o.Method, runtimesecurity.RouteKey(o.Route)}
		if current, ok := keys[key]; !ok || o.ObservedAt.After(current.ObservedAt) {
			keys[key] = o
		}
	}
	samples, err := readRuntimeDetectionSamples(ctx, tx, batch, end)
	if err != nil {
		return false, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM plume.runtime_findings WHERE workspace_id=$1`, batch.workspaceID).Scan(&count); err != nil {
		return false, err
	}
	known := map[string]bool{}
	rows, err := tx.Query(ctx, `SELECT DISTINCT method,route_key,rule,scope_key FROM plume.runtime_findings WHERE workspace_id=$1 AND service=$2 AND COALESCE((detection->>'sourceId')::bigint,source_id)=$3`, batch.workspaceID, batch.service, batch.sourceID)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var method, route, rule, scopeKey string
		if err := rows.Scan(&method, &route, &rule, &scopeKey); err != nil {
			rows.Close()
			return false, err
		}
		known[method+"\x00"+route+"\x00"+rule+"\x00"+scopeKey] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	ordered := make([]runtimeRouteKey, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	slices.SortFunc(ordered, func(a, b runtimeRouteKey) int { return strings.Compare(a.method+" "+a.route, b.method+" "+b.route) })
	limited := len(samples) >= 20000
	created := 0
	for _, key := range ordered {
		latest := keys[key]
		for _, policy := range policies {
			if !policy.Enabled || policy.SourceID > 0 && policy.SourceID != batch.sourceID {
				continue
			}
			subjects := []string{""}
			if runtimeRuleUsesSubject(policy.Rule) {
				subjects = []string{}
				for _, o := range observations {
					if o.Method == key.method && runtimesecurity.RouteKey(o.Route) == key.route && o.SubjectRef != "" {
						subjects = append(subjects, o.SubjectRef)
					}
				}
				slices.Sort(subjects)
				subjects = slices.Compact(subjects)
			}
			for _, subject := range subjects {
				d := evaluateRuntimeDetection(policy, samples, key, end, subject, required[key])
				d.SourceID, d.Source = batch.sourceID, batch.sourceName
				if !runtimeDetectionPositive(d) {
					continue
				}
				candidate := runtimeFindingCandidate{key: key, rule: policy.Rule, route: latest.Route, status: latest.Status, firstSeen: latest.ObservedAt, observedAt: latest.ObservedAt, detection: &d}
				identity := key.method + "\x00" + key.route + "\x00" + policy.Rule + "\x00" + policy.ScopeKey
				if (count+created >= maxRuntimeFindingCycles || created >= maxRuntimeFindingCandidates) && !known[identity] {
					limited = true
					continue
				}
				isNew, previous, err := updateRuntimeFinding(ctx, tx, batch, candidate)
				if err != nil {
					return false, err
				}
				if !isNew {
					continue
				}
				if count+created >= maxRuntimeFindingCycles || created >= maxRuntimeFindingCandidates {
					limited = true
					continue
				}
				if err := createRuntimeFinding(ctx, tx, batch, scanID, previous, candidate); err != nil {
					return false, err
				}
				created++
				known[identity] = true
			}
		}
	}
	if err := persistRuntimeDetectionRetests(ctx, tx, batch, policies, required, samples, observations, end); err != nil {
		return false, err
	}
	return limited, nil
}

func runtimeDetectionRetestAllowed(ctx context.Context, tx pgx.Tx, finding *runtimeCheckFinding, remediatedAt time.Time) (bool, error) {
	if slices.Contains(extendedRuntimeRules, finding.Rule) {
		state, e := runtimeExtendedRetestStatus(ctx, tx, Check{ID: finding.CheckID, Status: finding.CheckStatus}, finding)
		return state != nil && (state.State == "passed" || state.State == "verified"), e
	}
	if finding.retestPolicyID == nil {
		return false, nil
	}
	policies, err := readRuntimeDetectionPolicies(ctx, tx, finding.workspaceID)
	if err != nil {
		return false, err
	}
	for _, policy := range policies {
		if policy.Rule == finding.Rule && finding.Detection != nil && policy.ScopeKey == finding.Detection.Policy.ScopeKey {
			if !policy.Enabled || policy.ID != *finding.retestPolicyID || policy.CreatedAt != nil && !finding.RetestAt.After(*policy.CreatedAt) {
				return false, nil
			}
			var valid bool
			err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.check_evidence WHERE id=$1 AND check_id=$2 AND kind='retest' AND observed_at=$3 AND created_at>$4)`, *finding.retestEvidenceID, finding.CheckID, *finding.RetestAt, remediatedAt).Scan(&valid)
			return valid, err
		}
	}
	return false, nil
}

func incompleteRuntimeCapture(o runtimesecurity.Observation) bool {
	for _, warning := range o.CaptureWarnings {
		if slices.Contains([]string{"json_size_limit", "json_depth_limit", "json_field_limit", "json_invalid", "json_read_error", "json_encoding_unsupported", "invalid_data_fields", "capture_item_limit", "invalid_security_context", "invalid_runtime_context", "legacy_audit_projection_without_capture_evidence"}, warning) {
			return true
		}
	}
	return false
}

func runtimeBulkWindowHasPositive(p RuntimeDetectionPolicy, samples []runtimesecurity.Observation, key runtimeRouteKey, end time.Time) bool {
	// One route-level cycle can contain several source-scoped subjects. A quiet
	// triggering subject cannot clear another observed subject above the limit.
	subjects := map[string]int{}
	for _, o := range samples {
		if o.Method == key.method && runtimesecurity.RouteKey(o.Route) == key.route && o.SubjectRef != "" && !o.ObservedAt.Before(end.Add(-time.Duration(p.WindowMinutes)*time.Minute)) && !o.ObservedAt.After(end) {
			subjects[o.SubjectRef]++
			if subjects[o.SubjectRef] >= max(p.Threshold, p.MinRequests) {
				return true
			}
		}
	}
	return false
}

type runtimeDetectionRetestCandidate struct {
	id        int64
	check     Check
	rule      string
	key       runtimeRouteKey
	route     string
	positive  time.Time
	retest    *time.Time
	d         RuntimeDetection
	policyID  *int64
	firstSeen time.Time
	sourceID  int64
}

func persistRuntimeDetectionRetests(ctx context.Context, tx pgx.Tx, batch runtimeFindingBatch, policies []RuntimeDetectionPolicy, required map[runtimeRouteKey]bool, samples, incoming []runtimesecurity.Observation, end time.Time) error {
	rows, err := tx.Query(ctx, `SELECT f.id,c.id,c.status,f.rule,f.method,f.route_key,f.route,f.last_positive_at,f.retest_at,f.detection,f.retest_policy_id,f.first_seen,f.source_id FROM plume.runtime_findings f JOIN plume.checks c ON c.id=f.check_id AND c.workspace_id=f.workspace_id WHERE f.workspace_id=$1 AND f.service=$2 AND COALESCE((f.detection->>'sourceId')::bigint,f.source_id)=$3 AND (c.status='needs_work' OR (f.rule=ANY($5::text[]) AND c.status IN ('draft','in_review'))) AND f.rule <> ALL($4::text[]) ORDER BY c.id FOR NO KEY UPDATE OF c`, batch.workspaceID, batch.service, batch.sourceID, []string{"unregistered_success", "deprecated_success"}, extendedRuntimeRules)
	if err != nil {
		return err
	}
	checks, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (runtimeDetectionRetestCandidate, error) {
		var c runtimeDetectionRetestCandidate
		err := r.Scan(&c.id, &c.check.ID, &c.check.Status, &c.rule, &c.key.method, &c.key.route, &c.route, &c.positive, &c.retest, &c.d, &c.policyID, &c.firstSeen, &c.sourceID)
		return c, err
	})
	if err != nil {
		return err
	}
	for _, c := range checks {
		var p RuntimeDetectionPolicy
		for _, policy := range policies {
			if policy.Rule == c.rule && policy.ScopeKey == c.d.Policy.ScopeKey {
				p = policy
			}
		}
		if !p.Enabled {
			continue
		}
		if slices.Contains(extendedRuntimeRules, p.Rule) {
			if err := appendExtendedRuntimeRetest(ctx, tx, batch, c, p, samples, incoming); err != nil {
				return err
			}
			continue
		}
		remediated, err := runtimeRemediationAt(ctx, tx, c.check.ID)
		if err != nil {
			return err
		}
		if remediated.IsZero() {
			continue
		}
		var latest time.Time
		known := false
		for _, o := range incoming {
			if o.Method != c.key.method || runtimesecurity.RouteKey(o.Route) != c.key.route || !o.ObservedAt.After(remediated) || !o.ObservedAt.After(c.positive) {
				continue
			}
			if p.Rule == "subject_bulk_access" && o.SubjectRef != c.d.SubjectRef {
				continue
			}
			clear := false
			switch p.Rule {
			case "sensitive_response", "credential_response":
				clear = slices.Contains(o.InspectedLocations, "response") && !incompleteRuntimeCapture(o)
			case "missing_auth":
				clear = o.AuthState == "authenticated" || o.AuthState == "anonymous" && (o.Status == 401 || o.Status == 403)
			case "sql_injection_signal", "xss_signal", "path_traversal_signal":
				kind := strings.TrimSuffix(p.Rule, "_signal")
				for _, signal := range o.SecuritySignals {
					if signal.Kind == kind && signal.Count == 0 && signal.Observed > 0 && slices.Contains(o.InspectedLocations, "request") && !incompleteRuntimeCapture(o) {
						clear = true
					}
				}
			default:
				clear = true
			}
			if clear && o.ObservedAt.After(latest) {
				latest = o.ObservedAt
				known = true
			}
		}
		if !known || p.CreatedAt != nil && !latest.After(*p.CreatedAt) {
			continue
		}
		d := evaluateRuntimeDetection(p, samples, c.key, latest, c.d.SubjectRef, required[c.key])
		d.SourceID, d.Source = batch.sourceID, batch.sourceName
		if slices.Contains(d.Warnings, "source_sample_limit_20000") {
			continue
		}
		if runtimeDetectionPositive(d) || d.Requests < p.MinRequests {
			continue
		}
		if p.Rule == "subject_bulk_access" && runtimeBulkWindowHasPositive(p, samples, c.key, latest) {
			continue
		}
		if slices.Contains([]string{"sensitive_response", "credential_response", "missing_auth", "sql_injection_signal", "xss_signal", "path_traversal_signal"}, p.Rule) && d.Matched > 0 {
			continue
		}
		if (p.Rule == "subject_bulk_access" || p.Rule == "error_spike" || p.Rule == "off_hours") && !d.WindowStart.After(remediated) {
			continue
		}
		if c.retest != nil && c.retest.After(remediated) && c.retest.After(c.positive) && c.policyID != nil && *c.policyID == p.ID {
			continue
		}
		evidenceID, err := addEvidenceTx(ctx, tx, c.check, batch.actorID, EvidenceInput{Kind: "retest", SourceSystem: "API 运行时自动复测", SourceRecord: fmt.Sprintf("来源 #%d · 策略 #%d v%d · 批次 %s", batch.sourceID, p.ID, p.Revision, batch.batchID), Summary: "整改后同来源的新鲜可观察范围内未再达到该规则。" + detectionEvidenceSummary(d), ObservedAt: latest}, true)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE plume.runtime_findings SET retest_at=$2,retest_evidence_id=$3,retest_policy_id=$4 WHERE id=$1`, c.id, latest, evidenceID, p.ID); err != nil {
			return err
		}
	}
	return nil
}
