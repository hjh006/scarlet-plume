package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/jackc/pgx/v5"
)

type RuntimeRetestContext struct {
	SubjectRef   string `json:"subjectRef,omitempty"`
	ClientIP     string `json:"clientIp,omitempty"`
	TransportHop string `json:"transportHop,omitempty"`
	Location     string `json:"location,omitempty"`
}
type RuntimeRetestScope struct {
	OriginalPolicy *RuntimeDetectionPolicy `json:"originalPolicy,omitempty"`
	Version        int                     `json:"version"`
	Complete       bool                    `json:"complete"`
	PolicyID       int64                   `json:"policyId"`
	PolicyRevision int                     `json:"policyRevision"`
	Subjects       []string                `json:"subjects"`
	Contexts       []RuntimeRetestContext  `json:"contexts"`
}
type RuntimeAutomaticRetest struct {
	Warnings       []string               `json:"warnings,omitempty"`
	OriginalPolicy RuntimeDetectionPolicy `json:"originalPolicy"`
	AdoptedPolicy  RuntimeDetectionPolicy `json:"adoptedPolicy"`
	Version        int                    `json:"version"`
	PolicyID       int64                  `json:"policyId"`
	PolicyRevision int                    `json:"policyRevision"`
	ScopeKey       string                 `json:"scopeKey"`
	ScopeDigest    string                 `json:"scopeDigest"`
	WindowStart    time.Time              `json:"windowStart"`
	WindowEnd      time.Time              `json:"windowEnd"`
	Requests       int                    `json:"requests"`
	Observed       int64                  `json:"observed"`
	Subjects       []RuntimeDetection     `json:"subjects,omitempty"`
}
type RuntimeRetestState struct {
	State          string     `json:"state"`
	Reason         string     `json:"reason"`
	NextEligibleAt *time.Time `json:"nextEligibleAt,omitempty"`
}

func runtimeContextKey(c RuntimeRetestContext) string {
	return c.SubjectRef + "\x00" + c.ClientIP + "\x00" + c.TransportHop + "\x00" + c.Location
}
func normalizeRuntimeRetestScope(s *RuntimeRetestScope) {
	slices.Sort(s.Subjects)
	s.Subjects = slices.Compact(s.Subjects)
	slices.SortFunc(s.Contexts, func(a, b RuntimeRetestContext) int {
		return strings.Compare(runtimeContextKey(a), runtimeContextKey(b))
	})
	s.Contexts = slices.Compact(s.Contexts)
	if len(s.Subjects) > 64 {
		s.Subjects = s.Subjects[:64]
		s.Complete = false
	}
	if len(s.Contexts) > 64 {
		s.Contexts = s.Contexts[:64]
		s.Complete = false
	}
}
func captureRuntimeRetestScope(p RuntimeDetectionPolicy, events []runtimesecurity.Observation, key runtimeRouteKey, subject string, from, to time.Time) *RuntimeRetestScope {
	original := p
	s := &RuntimeRetestScope{OriginalPolicy: &original, Version: 1, Complete: true, PolicyID: p.ID, PolicyRevision: p.Revision, Subjects: []string{}, Contexts: []RuntimeRetestContext{}}
	for _, o := range events {
		if o.Method != key.method || runtimesecurity.RouteKey(o.Route) != key.route || !runtimeObservationInScope(p, o) || o.ObservedAt.Before(from) || o.ObservedAt.After(to) {
			continue
		}
		if runtimeRuleUsesSubject(p.Rule) {
			if o.SubjectRef == "" {
				s.Complete = false
				continue
			}
			if subject == "" || o.SubjectRef == subject {
				s.Subjects = append(s.Subjects, o.SubjectRef)
			}
			continue
		}
		matched, _, _, _ := extendedRuntimePolicySignal(p, o)
		if matched == 0 {
			continue
		}
		c := RuntimeRetestContext{SubjectRef: o.SubjectRef, ClientIP: o.ClientIP}
		if p.Rule == "credential_cleartext_transport" {
			if o.TransportHop == "" {
				s.Complete = false
			}
			for _, field := range o.DataFields {
				if field.Category == "credential" && field.Count > 0 && slices.Contains(o.InspectedLocations, field.Location) {
					frame := c
					frame.TransportHop = o.TransportHop
					frame.Location = field.Location
					s.Contexts = append(s.Contexts, frame)
				}
			}
		} else {
			s.Contexts = append(s.Contexts, c)
		}
	}
	normalizeRuntimeRetestScope(s)
	if runtimeRuleUsesSubject(p.Rule) && len(s.Subjects) == 0 || !runtimeRuleUsesSubject(p.Rule) && len(s.Contexts) == 0 {
		s.Complete = false
	}
	return s
}
func mergeRuntimeRetestScope(old, next *RuntimeRetestScope) *RuntimeRetestScope {
	if old == nil {
		return next
	}
	if next == nil {
		copy := *old
		copy.Complete = false
		return &copy
	}
	merged := *old
	merged.Subjects = append(slices.Clone(old.Subjects), next.Subjects...)
	merged.Contexts = append(slices.Clone(old.Contexts), next.Contexts...)
	oldPolicy, oldValid := runtimeOriginalPolicy(old)
	nextPolicy, nextValid := runtimeOriginalPolicy(next)
	merged.Complete = old.Complete && next.Complete && old.Version == 1 && next.Version == 1 && oldValid && nextValid && runtimeRetestPolicyCompatible(oldPolicy, nextPolicy)
	normalizeRuntimeRetestScope(&merged)
	return &merged
}
func reconstructRuntimeRetestScope(p RuntimeDetectionPolicy, events []runtimesecurity.Observation, key runtimeRouteKey, first, latest time.Time) *RuntimeRetestScope {
	// Old merged cycles can only be recovered while their complete retained sample
	// history is available; never guess a missing triggering subject or hop.
	from := first.Add(-time.Duration(p.WindowMinutes) * time.Minute)
	earliest := time.Time{}
	for _, o := range events {
		if o.Method == key.method && runtimesecurity.RouteKey(o.Route) == key.route && runtimeObservationInScope(p, o) && (earliest.IsZero() || o.ObservedAt.Before(earliest)) {
			earliest = o.ObservedAt
		}
	}
	if len(events) >= 20000 || from.Before(time.Now().Add(-24*time.Hour)) || earliest.IsZero() || earliest.After(from) {
		return nil
	}
	return captureRuntimeRetestScope(p, events, key, "", from, latest)
}
func runtimeOriginalPolicy(scope *RuntimeRetestScope) (RuntimeDetectionPolicy, bool) {
	if scope == nil || scope.OriginalPolicy == nil {
		return RuntimeDetectionPolicy{}, false
	}
	p := *scope.OriginalPolicy
	return p, scope.PolicyID == p.ID && scope.PolicyRevision == p.Revision
}
func runtimeRetestPolicyCompatible(original, current RuntimeDetectionPolicy) bool {
	return original.Rule == current.Rule && original.ScopeKey == current.ScopeKey && original.SourceID == current.SourceID && original.Method == current.Method && runtimesecurity.RouteKey(original.Route) == runtimesecurity.RouteKey(current.Route) && original.SubjectRef == current.SubjectRef && original.ClientIP == current.ClientIP && original.Direction == current.Direction && (original.Rule != "volume_limit" || original.VolumeUnit == current.VolumeUnit)
}
func runtimeExtendedBoundary(remediated, positive time.Time, p RuntimeDetectionPolicy) time.Time {
	bound := remediated
	if positive.After(bound) {
		bound = positive
	}
	if p.CreatedAt != nil && p.CreatedAt.After(bound) {
		bound = *p.CreatedAt
	}
	return bound
}
func runtimeNegativeContextKnown(p RuntimeDetectionPolicy, o runtimesecurity.Observation) bool {
	if incompleteRuntimeCapture(o) {
		return false
	}
	switch p.Rule {
	case "over_collection":
		return p.AllowlistConfigured && slices.Contains(o.InspectedLocations, p.Direction)
	case "credential_cleartext_transport":
		return classificationFilterValue(o.Transport, "http", "https") && o.TransportHop != ""
	case "subject_multi_ip":
		return o.SubjectRef != "" && o.ClientIP != ""
	case "subject_multi_region":
		return o.SubjectRef != "" && o.Context != nil && o.Context.ClientCountry != ""
	case "subject_repeated_access":
		return o.SubjectRef != ""
	case "client_region_mismatch":
		return len(p.AllowedClientCountries) > 0 && o.Context != nil && o.Context.ClientCountry != ""
	case "recipient_scope":
		return len(p.AllowedRecipients) > 0 && o.Context != nil && o.Context.Recipient != ""
	case "destination_scope":
		return o.Context != nil && (len(p.AllowedCountries) == 0 || o.Context.DestinationCountry != "") && (len(p.AllowedDomains) == 0 || o.Context.DestinationDomain != "") && len(p.AllowedCountries)+len(p.AllowedDomains) > 0
	case "volume_limit":
		return o.Context != nil && o.Context.Volume != nil && o.Context.VolumeUnit == p.VolumeUnit
	}
	return false
}
func runtimeRetestContextObserved(c RuntimeRetestContext, o runtimesecurity.Observation) bool {
	if c.SubjectRef != "" && c.SubjectRef != o.SubjectRef || c.ClientIP != "" && c.ClientIP != o.ClientIP {
		return false
	}
	if c.Location == "" {
		return true
	}
	if c.TransportHop != o.TransportHop || o.Transport != "https" || !slices.Contains(o.InspectedLocations, c.Location) {
		return false
	}
	for _, f := range o.DataFields {
		if f.Category == "credential" && f.Count > 0 && f.Location == c.Location {
			return true
		}
	}
	return false
}
func runtimeExtendedNegativeProof(p RuntimeDetectionPolicy, scope *RuntimeRetestScope, events []runtimesecurity.Observation, key runtimeRouteKey, end, remediated, positive time.Time) (*RuntimeAutomaticRetest, string) {
	original, valid := runtimeOriginalPolicy(scope)
	if scope == nil || scope.Version != 1 || !scope.Complete || !valid || !runtimeRetestPolicyCompatible(original, p) {
		return nil, "原阳性范围、主体、凭据跳段或策略适用边界不完整，不能缩小范围自动清除合并发现。"
	}
	if (p.ID != original.ID || p.Revision != original.Revision) && (p.CreatedAt == nil || !remediated.After(*p.CreatedAt)) {
		return nil, "采用新策略版本前，需要晚于本次策略修订的真实新整改记录。"
	}
	if len(events) >= 20000 {
		return nil, "来源样本达到保留上限，不能把截断窗口当作完整复测。"
	}
	boundary := runtimeExtendedBoundary(remediated, positive, p)
	start := end.Add(-time.Duration(p.WindowMinutes) * time.Minute)
	if remediated.IsZero() || !start.After(boundary) || end.After(time.Now()) {
		return nil, "尚未取得完整且晚于最新整改、阳性及策略生效的观察窗口。"
	}
	window := []runtimesecurity.Observation{}
	anchor := false
	for _, o := range events {
		if o.Method != key.method || runtimesecurity.RouteKey(o.Route) != key.route || !runtimeObservationInScope(p, o) || o.ObservedAt.After(end) {
			continue
		}
		if o.ObservedAt.After(boundary) && !o.ObservedAt.After(start) && runtimeNegativeContextKnown(p, o) {
			known := true
			if p.Rule == "credential_cleartext_transport" {
				for _, frame := range scope.Contexts {
					known = known && slices.Contains(o.InspectedLocations, frame.Location)
				}
			}
			anchor = anchor || known
		}
		if o.ObservedAt.Before(start) {
			continue
		}
		if !runtimeNegativeContextKnown(p, o) {
			return nil, "同范围观察缺少完整捕获、主体/IP、跳段或声明维度；未知不作为零命中。"
		}
		if p.Rule == "credential_cleartext_transport" {
			for _, frame := range scope.Contexts {
				if !slices.Contains(o.InspectedLocations, frame.Location) {
					return nil, "凭据原方向缺少完整捕获，不能把未观察到凭据当作TLS整改。"
				}
			}
		}
		window = append(window, o)
	}
	if !anchor || len(window) < p.MinRequests {
		return nil, "窗口起点缺少整改后来源观察，或可观察请求分母不足。"
	}
	proof := &RuntimeAutomaticRetest{OriginalPolicy: original, AdoptedPolicy: p, Version: 1, PolicyID: p.ID, PolicyRevision: p.Revision, ScopeKey: p.ScopeKey, ScopeDigest: assessmentDigest(scope), WindowStart: start, WindowEnd: end, Requests: len(window)}
	if runtimeRuleUsesSubject(p.Rule) {
		subjects := slices.Clone(scope.Subjects)
		for _, o := range window {
			subjects = append(subjects, o.SubjectRef)
		}
		slices.Sort(subjects)
		subjects = slices.Compact(subjects)
		for _, subject := range subjects {
			d := evaluateRuntimeDetection(p, window, key, end, subject, false)
			if d.Requests < p.MinRequests || d.Observed == 0 || runtimeDetectionPositive(d) {
				return nil, "每个触发主体均须有明确新观察，且完整窗口内未再次达到同一策略；异主体不能替代。"
			}
			d.RetestScope = nil
			d.AutomaticRetest = nil
			proof.Subjects = append(proof.Subjects, d)
			proof.Observed += d.Observed
		}
	} else {
		d := evaluateRuntimeDetection(p, window, key, end, "", false)
		if d.Requests < p.MinRequests || d.Observed == 0 || runtimeDetectionPositive(d) {
			return nil, "该完整窗口仍命中同一策略，或缺少该规则的明确可观察分母。"
		}
		if p.Rule != "volume_limit" && d.Matched > 0 {
			return nil, "新窗口仍存在基线偏离，不能用低于告警阈值掩盖未消除命中。"
		}
		for _, c := range scope.Contexts {
			seen := false
			for _, o := range window {
				if runtimeRetestContextObserved(c, o) {
					seen = true
					break
				}
			}
			if !seen {
				return nil, "原阳性主体/IP或凭据方向/跳段缺少新证据，不能缩小范围后清除发现。"
			}
		}
		proof.Observed = d.Observed
	}
	for _, o := range window {
		proof.Warnings = append(proof.Warnings, o.CaptureWarnings...)
	}
	slices.Sort(proof.Warnings)
	proof.Warnings = slices.Compact(proof.Warnings)
	if proof.Observed == 0 {
		return nil, "没有适用请求，来源沉默不构成复测通过。"
	}
	return proof, ""
}
func runtimeExtendedSourceCurrent(ctx context.Context, tx pgx.Tx, ws, source int64) (bool, error) {
	var current bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.runtime_security_sources s JOIN plume.memberships m ON m.workspace_id=s.workspace_id AND m.user_id=s.actor_id WHERE s.workspace_id=$1 AND s.id=$2 AND s.revoked_at IS NULL AND m.role='owner' AND s.last_seen>clock_timestamp()-interval '1 day')`, ws, source).Scan(&current)
	return current, e
}
func appendExtendedRuntimeRetest(ctx context.Context, tx pgx.Tx, batch runtimeFindingBatch, c runtimeDetectionRetestCandidate, p RuntimeDetectionPolicy, samples, incoming []runtimesecurity.Observation) error {
	if c.sourceID != batch.sourceID {
		return nil
	}
	if p.ScopeKey != c.d.Policy.ScopeKey {
		return nil
	}
	scope := c.d.RetestScope
	if scope == nil {
		scope = reconstructRuntimeRetestScope(c.d.Policy, samples, c.key, c.firstSeen, c.positive)
		if scope != nil {
			c.d.RetestScope = scope
			if _, e := tx.Exec(ctx, `UPDATE plume.runtime_findings SET detection=jsonb_set(detection,'{retestScope}',$2::jsonb) WHERE id=$1`, c.id, scope); e != nil {
				return e
			}
		}
	}
	if scope != nil && scope.OriginalPolicy == nil && scope.PolicyID == c.d.Policy.ID && scope.PolicyRevision == c.d.Policy.Revision {
		copy := c.d.Policy
		scope.OriginalPolicy = &copy
		if _, err := tx.Exec(ctx, `UPDATE plume.runtime_findings SET detection=jsonb_set(detection,'{retestScope}',$2::jsonb) WHERE id=$1`, c.id, scope); err != nil {
			return err
		}
	}
	remediated, e := runtimeRemediationAt(ctx, tx, c.check.ID)
	if e != nil || remediated.IsZero() {
		return e
	}
	current, e := runtimeExtendedSourceCurrent(ctx, tx, batch.workspaceID, batch.sourceID)
	if e != nil || !current {
		return e
	}
	latest := time.Time{}
	for _, o := range incoming {
		if o.Method == c.key.method && runtimesecurity.RouteKey(o.Route) == c.key.route && runtimeObservationInScope(p, o) && o.ObservedAt.After(latest) {
			latest = o.ObservedAt
		}
	}
	if latest.IsZero() {
		return nil
	}
	proof, _ := runtimeExtendedNegativeProof(p, scope, samples, c.key, latest, remediated, c.positive)
	if proof == nil {
		return nil
	}
	if c.retest != nil && c.retest.After(remediated) && c.retest.After(c.positive) && c.policyID != nil && *c.policyID == p.ID && c.retest.After(time.Now().Add(-24*time.Hour)) {
		return nil
	}
	evidenceID, e := addEvidenceTx(ctx, tx, c.check, batch.actorID, EvidenceInput{Kind: "retest", SourceSystem: "API 运行时自动复测", SourceRecord: fmt.Sprintf("来源 #%d · 策略 #%d v%d · 批次 %s", batch.sourceID, p.ID, p.Revision, batch.batchID), Summary: fmt.Sprintf("原命中策略#%d v%d；本次采用#%d v%d。完整整改后窗口%s至%s；同范围请求%d，可观察分母%d，原触发主体/上下文均有新证据，按本次采用技术基线未再触发；等待独立复核。", proof.OriginalPolicy.ID, proof.OriginalPolicy.Revision, p.ID, p.Revision, proof.WindowStart.Format(time.RFC3339), proof.WindowEnd.Format(time.RFC3339), proof.Requests, proof.Observed), ObservedAt: latest}, true)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE plume.runtime_findings SET retest_at=$2,retest_evidence_id=$3,retest_policy_id=$4,detection=jsonb_set(detection,'{automaticRetest}',$5::jsonb) WHERE id=$1`, c.id, latest, evidenceID, p.ID, proof); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE plume.checks SET status='in_review',submitted_by=$2,updated_at=clock_timestamp() WHERE id=$1`, c.check.ID, batch.actorID); e != nil {
		return e
	}
	return recordCheckEvent(ctx, tx, c.check.ID, batch.actorID, "submitted", "完整来源自动复测通过，提交独立复核；尚未人工通过。")
}

func runtimeExtendedRetestStatus(ctx context.Context, tx pgx.Tx, check Check, finding *runtimeCheckFinding) (*RuntimeRetestState, error) {
	if finding == nil || !slices.Contains(extendedRuntimeRules, finding.Rule) {
		return nil, nil
	}
	state := &RuntimeRetestState{State: "awaiting_remediation", Reason: "登记真实整改或成功执行回执后，等待同范围完整新窗口自动复测。"}
	if finding.Detection == nil {
		return &RuntimeRetestState{State: "invalidated", Reason: "缺少采用策略和原阳性范围，无法自动复测。"}, nil
	}
	policies, e := readRuntimeDetectionPolicies(ctx, tx, finding.workspaceID)
	if e != nil {
		return nil, e
	}
	var p RuntimeDetectionPolicy
	for _, candidate := range policies {
		if candidate.Rule == finding.Rule && candidate.ScopeKey == finding.Detection.Policy.ScopeKey {
			p = candidate
		}
	}
	if !p.Enabled {
		return &RuntimeRetestState{State: "invalidated", Reason: "采用策略已修订或暂停，旧复测不可用于当前独立通过。"}, nil
	}
	if finding.Detection.SourceID != 0 && finding.Detection.SourceID != finding.SourceID {
		return &RuntimeRetestState{State: "invalidated", Reason: "旧发现的来源身份不一致，不能借另一来源复测。"}, nil
	}
	current, e := runtimeExtendedSourceCurrent(ctx, tx, finding.workspaceID, finding.SourceID)
	if e != nil {
		return nil, e
	}
	if !current {
		return &RuntimeRetestState{State: "invalidated", Reason: "来源已停用、授权失效或长期未接收有效观察，旧复测已失效。"}, nil
	}
	remediated, e := runtimeRemediationAt(ctx, tx, check.ID)
	if e != nil {
		return nil, e
	}
	if remediated.IsZero() {
		if finding.RetestAt != nil {
			state.State = "invalidated"
			state.Reason = "同范围新阳性或整改变化使旧复测失效；请登记新的整改并等待完整观察窗口。"
		}
		return state, nil
	}
	original := finding.Detection.Policy
	if policy, valid := runtimeOriginalPolicy(finding.Detection.RetestScope); valid {
		original = policy
	}
	if !runtimeRetestPolicyCompatible(original, p) {
		return &RuntimeRetestState{State: "invalidated", Reason: "策略改变了原来源、接口、主体/IP、方向或量纲边界，不能缩小原范围清除旧发现。"}, nil
	}
	if (p.ID != original.ID || p.Revision != original.Revision) && (p.CreatedAt == nil || !remediated.After(*p.CreatedAt)) {
		return &RuntimeRetestState{State: "invalidated", Reason: "策略已修订，需登记晚于本次修订的真实新整改，再取得当前版本完整观察窗口。"}, nil
	}
	boundary := runtimeExtendedBoundary(remediated, finding.LastSeen, p)
	eligible := boundary.Add(time.Duration(p.WindowMinutes) * time.Minute)
	state.NextEligibleAt = &eligible
	state.State = "awaiting_observations"
	state.Reason = "等待同来源、接口和原主体/IP适用范围的完整新窗口及明确可观察分母；来源沉默不算通过。"
	scope := finding.Detection.RetestScope
	proof := finding.Detection.AutomaticRetest

	if scope == nil || !scope.Complete {
		state.Reason = "原触发主体、IP或凭据跳段范围尚不完整，无法以局部无命中清除旧发现。"
		return state, nil
	}
	if proof == nil || finding.RetestAt == nil || finding.retestPolicyID == nil || finding.retestEvidenceID == nil {
		return state, nil
	}
	state.State = "invalidated"
	state.Reason = "旧复测早于最新整改/阳性，或采用范围已改变；需要新的完整复测。"
	if !proof.WindowEnd.After(time.Now().Add(-24 * time.Hour)) {
		state.Reason = "原自动复测观察已超过24小时，需要同范围完整新窗口刷新后重新独立复核。"
		return state, nil
	}

	if proof.Version != 1 || proof.OriginalPolicy.ID != original.ID || proof.OriginalPolicy.Revision != original.Revision || proof.AdoptedPolicy.ID != p.ID || proof.AdoptedPolicy.Revision != p.Revision || proof.PolicyID != p.ID || proof.PolicyRevision != p.Revision || proof.ScopeKey != p.ScopeKey || *finding.retestPolicyID != p.ID || proof.ScopeDigest != assessmentDigest(scope) || !proof.WindowStart.After(boundary) || !proof.WindowEnd.After(finding.LastSeen) || !proof.WindowEnd.After(remediated) || proof.WindowEnd.After(time.Now()) || !proof.WindowEnd.Truncate(time.Microsecond).Equal(finding.RetestAt.Truncate(time.Microsecond)) || proof.Requests < p.MinRequests || proof.Observed <= 0 {
		return state, nil
	}
	var valid bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.check_evidence WHERE id=$1 AND check_id=$2 AND kind='retest' AND observed_at=$3 AND created_at>$4)`, *finding.retestEvidenceID, check.ID, *finding.RetestAt, remediated).Scan(&valid)
	if e != nil {
		return nil, e
	}
	if !valid {
		return state, nil
	}
	state.State = "passed"
	state.Reason = "同范围完整新窗口自动复测通过，等待独立人员核验；尚未人工通过。"
	state.NextEligibleAt = nil
	if slices.Contains(proof.Warnings, "json_dynamic_keys") {
		state.Reason += "字段路径经掩码，类别统计未因此丢失，不能据此推断精确字段血缘。"
	}
	if check.Status == "verified" {
		state.State = "verified"
		state.Reason = "自动复测证据已由独立人员复核通过。"
	}
	return state, nil
}
func invalidateExtendedRuntimeReviews(ctx context.Context, tx pgx.Tx, ws, actor, sourceID int64, rule, scopeKey, reason string) error {
	rows, e := tx.Query(ctx, `SELECT c.id FROM plume.checks c JOIN plume.runtime_findings f ON f.workspace_id=c.workspace_id AND f.check_id=c.id WHERE c.workspace_id=$1 AND c.status='in_review' AND f.rule=ANY($2) AND ($3::bigint=0 OR f.source_id=$3) AND ($4='' OR (f.rule=$4 AND COALESCE(f.detection#>>'{policy,scopeKey}','')=$5)) ORDER BY c.id FOR NO KEY UPDATE OF c`, ws, extendedRuntimeRules, sourceID, rule, scopeKey)
	if e != nil {
		return e
	}
	ids, e := pgx.CollectRows(rows, func(r pgx.CollectableRow) (int64, error) { var id int64; e := r.Scan(&id); return id, e })
	if e != nil {
		return e
	}
	for _, id := range ids {
		if _, e = tx.Exec(ctx, `UPDATE plume.checks SET status='needs_work',submitted_by=NULL,updated_at=clock_timestamp() WHERE id=$1`, id); e != nil {
			return e
		}
		if e = recordCheckEvent(ctx, tx, id, actor, "changes", reason); e != nil {
			return e
		}
	}
	return nil
}
