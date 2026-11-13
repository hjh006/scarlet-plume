package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

const endpointSessionExecutor = "windows_clipboard_session_guard"
const endpointSessionTarget = "interactive_session_clipboard"

var endpointSessionModes = []string{"monitor", "clear_sensitive", "clear_all"}
var endpointSessionCategories = []string{"email", "phone", "national_id", "financial_account"}
var endpointSessionCodes = map[string]string{
	"session_observed": "observed", "session_no_selected_match": "not_required", "session_clear_intent": "unknown", "session_cleared_after_change": "cleared_after_change", "session_sequence_changed": "stale", "session_changed": "stale", "session_clipboard_busy": "busy", "session_unavailable": "unavailable", "session_format_unsupported": "unsupported", "session_capture_unknown": "unknown", "session_clear_failed": "unknown", "session_sequence_unknown": "unknown", "session_policy_inactive": "unknown",
}

func validEndpointSessionTarget(channel, target, config string, refs []string) bool {
	return channel == "clipboard" && target == endpointSessionTarget && digestPattern.MatchString(config) && len(refs) == 1 && digestPattern.MatchString(refs[0])
}
func validEndpointSessionCapability(os string, c EndpointCapability) bool {
	return os == "windows" && c.Channel == "clipboard" && c.Executor == endpointSessionExecutor && c.Privilege == "interactive_user" && validEndpointSessionTarget(c.Channel, c.TargetScope, c.ConfigDigest, c.TargetRefs)
}
func endChangedEndpointSessions(ctx context.Context, tx pgx.Tx, ws, sourceID int64, in EndpointHeartbeatInput) error {
	d := EndpointDevice{EndpointHeartbeatInput: in}
	c, digest := endpointCapability(d, "clipboard", "session_response")
	rows, err := tx.Query(ctx, `SELECT id,body FROM plume.endpoint_channel_policies WHERE workspace_id=$1 AND source_id=$2 AND scope='session_response' AND ended_at IS NULL AND status IN ('draft','approved')`, ws, sourceID)
	if err != nil {
		return err
	}
	type pending struct {
		id int64
		p  EndpointPolicy
	}
	policies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pending, error) {
		var p pending
		err := row.Scan(&p.id, &p.p)
		return p, err
	})
	if err != nil {
		return err
	}
	for _, p := range policies {
		if !c.Supported || !validEndpointSessionCapability(in.OS, c) || p.p.CapabilityDigest != digest {
			if _, err = tx.Exec(ctx, `UPDATE plume.endpoint_channel_policies SET ended_at=COALESCE(ended_at,clock_timestamp()) WHERE workspace_id=$1 AND id=$2`, ws, p.id); err != nil {
				return err
			}
		}
	}
	return nil
}
func refreshEndpointSession(p *EndpointPolicy, d EndpointDevice, c EndpointCapability) {
	if p.Scope != "session_response" {
		return
	}
	same := len(p.TargetRefs) == 1 && len(c.TargetRefs) == 1 && p.TargetRefs[0] == c.TargetRefs[0]
	p.SessionCurrent = &same
	p.Current = p.Current && p.EndedAt == nil
	p.CanReview = p.CanReview && p.EndedAt == nil
	p.CurrentReason = "active"
	switch {
	case p.Status == "revoked" || p.Status == "superseded":
		p.CurrentReason = p.Status
	case !same:
		p.CurrentReason = "session_changed"
	case p.ConfigDigest != c.ConfigDigest:
		p.CurrentReason = "configuration_changed"
	case p.EndedAt != nil:
		p.CurrentReason = "ended"
	case !p.ExpiresAt.After(time.Now()):
		p.CurrentReason = "expired"
	case !d.Current:
		p.CurrentReason = "source_unavailable"
	case !p.Current && p.Status == "draft":
		p.CurrentReason = "awaiting_approval"
	case !p.Current:
		p.CurrentReason = "capability_changed"
	}
	if p.LatestReceipt != nil && p.LatestReceipt.Status == "stopped" {
		p.Current = false
		p.CanReview = false
		p.CurrentReason = "stopped"
		p.DeploymentState = "stopped"
		return
	}
	if !p.Current {
		if p.ApprovedAt != nil {
			p.DeploymentState = "awaiting_stop"
		} else {
			p.DeploymentState = "unavailable"
		}
		return
	}
	if p.LatestReceipt != nil && p.LatestReceipt.Status == "loaded" {
		p.DeploymentState = "listening"
	}
}
func validEndpointSessionReceipt(in *EndpointReceiptInput) bool {
	if !validEndpointSessionTarget(in.Channel, in.TargetScope, in.ConfigDigest, in.TargetRefs) || in.Executor != endpointSessionExecutor || in.Phase != "" || in.LeaseRef != "" || in.OperationRef != "" || in.Action != "" || in.PriorStateDigest != "" || in.StateDigest != "" || in.VerificationMethod != "" || in.VerificationRef != "" {
		return false
	}
	return in.Status == "loaded" && in.Code == "session_guard_loaded" || in.Status == "stopped" && in.Code == "session_guard_stopped" || in.Status == "failed" && in.Code == "session_guard_failed" || in.Status == "unsupported" && in.Code == "session_guard_unsupported"
}

// Original approval bounds admit cached/offline source facts. A later server
// revocation changes current protection, never the observed historical record.
func endpointSessionOriginalWindow(p EndpointPolicy, at time.Time) bool {
	return p.ApprovedAt != nil && !at.Before(*p.ApprovedAt) && !at.After(p.ExpiresAt) && p.Status != "draft" && p.Status != "rejected"
}
func recordEndpointSessionReceipt(ctx context.Context, tx pgx.Tx, ws int64, p EndpointPolicy, in EndpointReceiptInput) error {
	if !endpointNativeCapabilityMatches(p, EndpointCapability{TargetScope: in.TargetScope, TargetRefs: in.TargetRefs, ConfigDigest: in.ConfigDigest}) || p.Executor != endpointSessionExecutor || p.ApprovedAt == nil || in.ObservedAt.Before(*p.ApprovedAt) {
		return ErrConflict
	}
	if in.Status == "loaded" && !endpointSessionOriginalWindow(p, in.ObservedAt) {
		return ErrConflict
	}
	if in.Status == "stopped" {
		_, err := tx.Exec(ctx, `UPDATE plume.endpoint_channel_policies SET ended_at=COALESCE(ended_at,$3) WHERE workspace_id=$1 AND id=$2`, ws, p.ID, in.ObservedAt)
		return err
	}
	return nil
}
func validEndpointSessionEvent(in *EndpointSessionEventInput) bool {
	in.ObservedAt = in.ObservedAt.UTC().Truncate(time.Microsecond)
	in.ResponseAt = in.ResponseAt.UTC().Truncate(time.Microsecond)
	now := time.Now()
	if in.ObservedAt.Before(now.Add(-7*24*time.Hour)) || in.ResponseAt.Before(now.Add(-7*24*time.Hour)) || in.ObservedAt.After(now.Add(time.Minute)) || in.ResponseAt.After(now.Add(time.Minute)) || in.ResponseAt.Before(in.ObservedAt) || in.ResponseAt.Sub(in.ObservedAt) > 24*time.Hour || in.PolicyID < 1 || in.PolicyRevision < 1 || in.Scope != "session_response" || in.Executor != endpointSessionExecutor || !slices.Contains([]string{"complete", "partial", "unknown"}, in.Capture) || len(in.Categories) > 4 {
		return false
	}
	for _, v := range []string{in.EventRef, in.OperationRef, in.DeviceRef, in.SessionRef, in.PolicyDigest, in.ConfigDigest} {
		if !digestPattern.MatchString(v) {
			return false
		}
	}
	for _, v := range []string{in.SequenceRef, in.CurrentSequenceRef, in.ContentRef} {
		if v != "" && !digestPattern.MatchString(v) {
			return false
		}
	}
	if expected, ok := endpointSessionCodes[in.Code]; !ok || expected != in.Status {
		return false
	}
	seen := map[string]bool{}
	for _, category := range in.Categories {
		if seen[category] || !slices.Contains(endpointSessionCategories, category) {
			return false
		}
		seen[category] = true
	}
	sort.Strings(in.Categories)
	if in.Categories == nil {
		in.Categories = []string{}
	}
	if in.Code == "session_clear_intent" && in.SequenceRef == "" {
		return false
	}
	if in.SequenceMatched && (in.SequenceRef == "" || in.CurrentSequenceRef != in.SequenceRef) {
		return false
	}
	switch in.Status {
	case "cleared_after_change":
		return in.ClearAttempted && in.SequenceMatched && in.ClipboardEmpty && in.SequenceRef != ""
	case "not_required":
		if in.Capture != "complete" || in.SequenceRef == "" {
			return false
		}
	case "observed":
		if in.SequenceRef == "" {
			return false
		}
	case "stale":
		if in.Code == "session_sequence_changed" && (in.SequenceRef == "" || in.CurrentSequenceRef == "" || in.SequenceRef == in.CurrentSequenceRef || in.SequenceMatched) {
			return false
		}
	}
	if in.ClipboardEmpty {
		return false
	}
	if in.ClearAttempted {
		return in.Code == "session_clear_failed" && in.SequenceMatched
	}
	return true
}
func endpointSessionDecisionAllowed(p EndpointPolicy, in EndpointSessionEventInput) bool {
	if in.Status == "observed" && p.Mode != "monitor" {
		return false
	}
	hit := slices.ContainsFunc(in.Categories, func(c string) bool { return slices.Contains(p.Categories, c) })
	if in.Status == "not_required" {
		return p.Mode == "clear_sensitive" && !hit
	}
	if in.Code == "session_clear_intent" || in.ClearAttempted {
		if p.Mode == "monitor" {
			return false
		}
		if p.Mode == "clear_sensitive" {
			return hit && in.ContentRef != "" && in.Capture != "unknown"
		}
	}
	return true
}
func sessionIntentMatches(a, b EndpointSessionEventInput) bool {
	return a.OperationRef == b.OperationRef && a.DeviceRef == b.DeviceRef && a.SessionRef == b.SessionRef && a.PolicyID == b.PolicyID && a.PolicyRevision == b.PolicyRevision && a.PolicyDigest == b.PolicyDigest && a.ConfigDigest == b.ConfigDigest && a.Scope == b.Scope && a.Executor == b.Executor && a.SequenceRef == b.SequenceRef && a.ContentRef == b.ContentRef && a.Capture == b.Capture && slices.Equal(a.Categories, b.Categories) && a.ObservedAt.Equal(b.ObservedAt) && !b.ResponseAt.Before(a.ResponseAt)
}
func (s *Store) ReceiveEndpointSessionEvent(ctx context.Context, digest string, in EndpointSessionEventInput) (EndpointSessionEvent, bool, error) {
	if !validEndpointSessionEvent(&in) {
		return EndpointSessionEvent{}, false, ErrInvalid
	}
	var out EndpointSessionEvent
	var replay bool
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		source, ws, _, err := operationsSourceActor(ctx, tx, "security_events", digest)
		if err != nil {
			return err
		}
		hash := assessmentDigest(in)
		var oldHash string
		err = tx.QueryRow(ctx, `SELECT id,event,created_at,snapshot_digest,input_digest,(snapshot->>'historical')::boolean,(snapshot->>'currentResponse')::boolean FROM plume.endpoint_session_events WHERE source_id=$1 AND event_ref=$2`, source.ID, in.EventRef).Scan(&out.ID, &out.EndpointSessionEventInput, &out.ReceivedAt, &out.SnapshotDigest, &oldHash, &out.Historical, &out.CurrentResponse)
		if err == nil {
			if hash != oldHash {
				return ErrConflict
			}
			out.SourceID = source.ID
			replay = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		p, err := readEndpointPolicy(ctx, tx, ws, in.PolicyID)
		if err != nil {
			return err
		}
		if p.SourceID != source.ID || p.Scope != "session_response" || p.Executor != in.Executor || p.DeviceRef != in.DeviceRef || p.Revision != in.PolicyRevision || p.Digest != in.PolicyDigest || len(p.TargetRefs) != 1 || p.TargetRefs[0] != in.SessionRef || p.ConfigDigest != in.ConfigDigest {
			return ErrConflict
		}
		d, err := readEndpointDevice(ctx, tx, ws, source.ID)
		if err != nil {
			return err
		}
		if d.DeviceRef != in.DeviceRef || d.OS != "windows" {
			return ErrConflict
		}
		if p.ApprovedAt == nil || in.ObservedAt.Before(*p.ApprovedAt) {
			return ErrConflict
		}
		if !endpointSessionDecisionAllowed(p, in) {
			return ErrInvalid
		}
		asOf := in.ResponseAt
		if in.ClearAttempted {
			var intent EndpointSessionEventInput
			err = tx.QueryRow(ctx, `SELECT event FROM plume.endpoint_session_events WHERE source_id=$1 AND policy_id=$2 AND operation_ref=$3 AND event->>'code'='session_clear_intent' ORDER BY id LIMIT 1`, source.ID, p.ID, in.OperationRef).Scan(&intent)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrEvidenceRequired
			}
			if err != nil {
				return err
			}
			if !sessionIntentMatches(intent, in) {
				return ErrConflict
			}
			asOf = intent.ResponseAt
		}
		valid := endpointSessionOriginalWindow(p, asOf)
		// A stopped/expired scope can still preserve an explicit no-action outcome.
		if !valid && (in.ClearAttempted || in.Code == "session_clear_intent" || !slices.Contains([]string{"stale", "unknown", "busy", "unavailable", "unsupported"}, in.Status)) {
			return ErrConflict
		}
		if p.ApprovedAt == nil {
			return ErrConflict
		}
		if in.Code == "session_clear_intent" || in.ClearAttempted {
			var loaded bool
			err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT receipt->>'status'='loaded' FROM plume.endpoint_channel_receipts WHERE workspace_id=$1 AND source_id=$2 AND policy_id=$3 AND (receipt->>'observedAt')::timestamptz<=$4 ORDER BY (receipt->>'observedAt')::timestamptz DESC,id DESC LIMIT 1),false)`, ws, source.ID, p.ID, asOf).Scan(&loaded)
			if err != nil {
				return err
			}
			if !loaded {
				return ErrEvidenceRequired
			}
		}
		if err = refreshEndpointPolicy(ctx, tx, ws, 0, "", &p); err != nil {
			return err
		}
		responseValid, err := endpointPolicyAt(ctx, tx, ws, p, in.ResponseAt)
		if err != nil {
			return err
		}
		out.EndpointSessionEventInput = in
		out.SourceID = source.ID
		if err = tx.QueryRow(ctx, `SELECT nextval('plume.endpoint_session_events_id_seq'),clock_timestamp()`).Scan(&out.ID, &out.ReceivedAt); err != nil {
			return err
		}
		out.Historical = !responseValid || !p.Current || out.ReceivedAt.Sub(in.ResponseAt) > endpointHeartbeatTTL
		out.CurrentResponse = !out.Historical && in.Status == "cleared_after_change"
		snapshot := struct {
			Version         int                  `json:"version"`
			Policy          EndpointPolicy       `json:"policy"`
			Event           EndpointSessionEvent `json:"event"`
			Historical      bool                 `json:"historical"`
			CurrentResponse bool                 `json:"currentResponse"`
		}{1, p, out, out.Historical, out.CurrentResponse}
		out.SnapshotDigest = assessmentDigest(snapshot)
		_, err = tx.Exec(ctx, `INSERT INTO plume.endpoint_session_events(id,workspace_id,source_id,policy_id,event_ref,operation_ref,input_digest,event,snapshot,snapshot_digest,created_at) OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, out.ID, ws, source.ID, p.ID, in.EventRef, in.OperationRef, hash, in, snapshot, out.SnapshotDigest, out.ReceivedAt)
		return err
	})
	return out, replay, storeError(err)
}
func readEndpointSessionEvents(ctx context.Context, tx pgx.Tx, ws, policyID int64, limit int) ([]EndpointSessionEvent, int64, error) {
	out := []EndpointSessionEvent{}
	var total int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM plume.endpoint_session_events WHERE workspace_id=$1 AND policy_id=$2`, ws, policyID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := tx.Query(ctx, `SELECT id,source_id,event,created_at,snapshot_digest,(snapshot->>'historical')::boolean,(snapshot->>'currentResponse')::boolean FROM plume.endpoint_session_events WHERE workspace_id=$1 AND policy_id=$2 ORDER BY id DESC LIMIT $3`, ws, policyID, limit)
	if err != nil {
		return nil, 0, err
	}
	out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (EndpointSessionEvent, error) {
		var e EndpointSessionEvent
		err := row.Scan(&e.ID, &e.SourceID, &e.EndpointSessionEventInput, &e.ReceivedAt, &e.SnapshotDigest, &e.Historical, &e.CurrentResponse)
		return e, err
	})
	if out == nil {
		out = []EndpointSessionEvent{}
	}
	return out, total, err
}
func endpointSessionCoverage(ctx context.Context, tx pgx.Tx, ws, policyID, total int64, truncated bool) ([]string, error) {
	missing := []string{"仅记录收到的会话变化事件，完整变化次数与遗漏分母未知。", "变化后清除存在竞态窗口；不能证明清除前未被其他应用读取或粘贴，也不覆盖其他登录会话。", "capture仅描述所取得文本，HTML及自定义等其他格式未因此全部检查；清除后不保存或恢复正文。"}
	if total == 0 {
		missing = append(missing, "尚无会话响应事件；监听载入不表示观察或清除已发生。")
	}
	if truncated {
		missing = append(missing, "超过当前导出或详情条数上限，事件清单不完整。")
	}
	var pending int64
	err := tx.QueryRow(ctx, `SELECT count(*) FROM plume.endpoint_session_events i WHERE i.workspace_id=$1 AND i.policy_id=$2 AND i.event->>'code'='session_clear_intent' AND NOT EXISTS(SELECT 1 FROM plume.endpoint_session_events r WHERE r.source_id=i.source_id AND r.policy_id=i.policy_id AND r.operation_ref=i.operation_ref AND r.event->>'code'<>'session_clear_intent')`, ws, policyID).Scan(&pending)
	var historical int64
	if err == nil {
		err = tx.QueryRow(ctx, `SELECT count(*) FROM plume.endpoint_session_events WHERE workspace_id=$1 AND policy_id=$2 AND snapshot->>'historical'='true'`, ws, policyID).Scan(&historical)
	}
	if historical > 0 {
		missing = append(missing, fmt.Sprintf("%d 条为历史或迟到的来源事实，不证明当前授权有效或当前已清除。", historical))
	}
	if pending > 0 {
		missing = append(missing, fmt.Sprintf("%d 个操作仅有清除意图、尚无最终结果，是否清除仍未知。", pending))
	}
	return missing, err
}

// Keep legacy snapshot JSON stable while making the new session coverage fields
// explicit even when no event has arrived.
func (s EndpointEvidenceSnapshot) MarshalJSON() ([]byte, error) {
	type legacy EndpointEvidenceSnapshot
	if s.Policy.Scope != "session_response" {
		return json.Marshal(legacy(s))
	}
	return json.Marshal(struct {
		legacy
		SessionEvents          []EndpointSessionEvent `json:"sessionEvents"`
		SessionEventsTotal     int64                  `json:"sessionEventsTotal"`
		SessionEventsTruncated bool                   `json:"sessionEventsTruncated"`
		SessionCoverageMissing []string               `json:"sessionCoverageMissing"`
	}{legacy(s), s.SessionEvents, s.SessionEventsTotal, s.SessionEventsTruncated, s.SessionCoverageMissing})
}
