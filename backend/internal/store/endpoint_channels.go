package store

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const endpointHeartbeatTTL = 5 * time.Minute

var endpointModes = []string{"monitor", "block_sensitive", "block_all", "clear_sensitive", "clear_all"}
var endpointCategories = []string{"email", "phone", "national_id", "financial_account", "credential", "personal_name", "address", "ip_address", "business"}

func endpointExecutor(os, channel, scope string) string {
	if scope == "controlled_operations" && channel == "usb" {
		return "usb_copy_guard"
	}
	if os != "windows" {
		return ""
	}
	if scope == "session_response" && channel == "clipboard" {
		return endpointSessionExecutor
	}
	if scope == "native_control" {
		return endpointNativeExecutor(channel)
	}
	if scope == "controlled_operations" {
		return map[string]string{"print": "windows_print_gdi", "clipboard": "windows_clipboard_write"}[channel]
	}
	if scope == "observation" {
		return map[string]string{"usb": "windows_usb_volume", "print": "windows_print_spool", "clipboard": "windows_clipboard_listener"}[channel]
	}
	return ""
}
func normalizeEndpointHeartbeat(in *EndpointHeartbeatInput) bool {
	in.ObservedAt = in.ObservedAt.UTC().Truncate(time.Microsecond)
	if !digestPattern.MatchString(in.DeviceRef) || !slices.Contains([]string{"windows", "linux", "darwin"}, in.OS) || !validCheckText(in.AgentVersion, 80) || in.ObservedAt.Before(time.Now().Add(-endpointHeartbeatTTL)) || in.ObservedAt.After(time.Now().Add(time.Minute)) || len(in.Capabilities) > 10 || len(in.Capabilities) == 0 {
		return false
	}
	seen := map[string]bool{}
	for i := range in.Capabilities {
		c := &in.Capabilities[i]
		key := c.Channel + ":" + c.Scope
		if seen[key] || !slices.Contains([]string{"usb", "print", "clipboard"}, c.Channel) || !slices.Contains([]string{"observation", "controlled_operations", "native_control", "session_response"}, c.Scope) || len(c.Reason) > 300 || strings.ContainsAny(c.Reason, "\x00\r\n") {
			return false
		}
		seen[key] = true
		if c.Modes == nil {
			c.Modes = []string{}
		}
		if c.Privilege != "" && !slices.Contains([]string{"administrator", "system", "not_elevated", "unknown", "interactive_user"}, c.Privilege) {
			return false
		}
		if c.Scope == "native_control" {
			if c.Supported && (in.OS != "windows" || !slices.Contains([]string{"administrator", "system"}, c.Privilege) || !validEndpointTargets(c.Channel, c.TargetScope, c.ConfigDigest, c.TargetRefs) || len(c.Modes) != 1 || c.Modes[0] != "block_all") {
				return false
			}
			if !c.Supported && (c.TargetScope != "" || c.ConfigDigest != "" || len(c.TargetRefs) > 0) && !validEndpointTargets(c.Channel, c.TargetScope, c.ConfigDigest, c.TargetRefs) {
				return false
			}
		} else if c.Scope == "session_response" {
			if c.Supported && !validEndpointSessionCapability(in.OS, *c) {
				return false
			}
			if !c.Supported && (c.TargetScope != "" || c.ConfigDigest != "" || len(c.TargetRefs) > 0) && !validEndpointSessionTarget(c.Channel, c.TargetScope, c.ConfigDigest, c.TargetRefs) {
				return false
			}
		} else if c.TargetScope != "" || c.ConfigDigest != "" || len(c.TargetRefs) > 0 || c.Privilege != "" {
			return false
		}
		if !c.Supported {
			if len(c.Modes) != 0 || c.Executor != "" && c.Executor != endpointExecutor(in.OS, c.Channel, c.Scope) && !(c.Channel == "usb" && c.Executor == "usb_copy_guard") && !(c.Scope == "native_control" && c.Executor == endpointNativeExecutor(c.Channel)) && !(c.Scope == "session_response" && c.Channel == "clipboard" && c.Executor == endpointSessionExecutor) || !validCheckText(c.Reason, 300) {
				return false
			}
			continue
		}
		expected := endpointExecutor(in.OS, c.Channel, c.Scope)
		if expected == "" || expected != c.Executor || len(c.Modes) == 0 || len(c.Modes) > 3 {
			return false
		}
		modeSeen := map[string]bool{}
		for _, m := range c.Modes {
			if !slices.Contains(endpointModes, m) || modeSeen[m] || c.Scope == "observation" && m != "monitor" || c.Scope == "session_response" && !slices.Contains(endpointSessionModes, m) || c.Scope != "session_response" && (m == "clear_sensitive" || m == "clear_all") {
				return false
			}
			modeSeen[m] = true
		}
		sort.Strings(c.Modes)
	}
	sort.Slice(in.Capabilities, func(i, j int) bool {
		return in.Capabilities[i].Channel+in.Capabilities[i].Scope < in.Capabilities[j].Channel+in.Capabilities[j].Scope
	})
	return true
}
func endpointCapability(d EndpointDevice, channel, scope string) (EndpointCapability, string) {
	for _, c := range d.Capabilities {
		if c.Channel == channel && c.Scope == scope {
			return c, assessmentDigest(struct {
				OS, AgentVersion string
				Capability       EndpointCapability
			}{d.OS, d.AgentVersion, c})
		}
	}
	return EndpointCapability{}, ""
}
func readEndpointDevice(ctx context.Context, tx pgx.Tx, ws, sourceID int64) (EndpointDevice, error) {
	var d EndpointDevice
	err := tx.QueryRow(ctx, `SELECT d.heartbeat,d.source_id,s.name,d.last_seen_at,(s.revoked_at IS NULL AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=s.workspace_id AND m.user_id=s.actor_id AND m.role='owner')) FROM plume.endpoint_devices d JOIN plume.operations_sources s ON s.id=d.source_id WHERE d.workspace_id=$1 AND d.source_id=$2`, ws, sourceID).Scan(&d.EndpointHeartbeatInput, &d.SourceID, &d.SourceName, &d.LastSeenAt, &d.Current)
	if err != nil {
		return d, storeError(err)
	}
	d.Online = time.Since(d.LastSeenAt) <= endpointHeartbeatTTL
	d.Current = d.Current && d.Online
	return d, nil
}
func lockEndpointSource(ctx context.Context, tx pgx.Tx, ws, sourceID int64, requireCurrent bool) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT s.kind='security_events' AND s.revoked_at IS NULL AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=s.workspace_id AND m.user_id=s.actor_id AND m.role='owner') FROM plume.operations_sources s WHERE s.workspace_id=$1 AND s.id=$2 FOR NO KEY UPDATE`, ws, sourceID).Scan(&valid)
	if err != nil {
		return storeError(err)
	}
	if requireCurrent && !valid {
		return ErrEvidenceRequired
	}
	return nil
}
func (s *Store) EndpointHeartbeat(ctx context.Context, digest string, in EndpointHeartbeatInput) (EndpointDevice, error) {
	if !normalizeEndpointHeartbeat(&in) {
		return EndpointDevice{}, ErrInvalid
	}
	var result EndpointDevice
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		source, ws, _, err := operationsSourceActor(ctx, tx, "security_events", digest)
		if err != nil {
			return err
		}
		var old EndpointHeartbeatInput
		var oldHash string
		err = tx.QueryRow(ctx, `SELECT heartbeat,heartbeat_digest FROM plume.endpoint_devices WHERE source_id=$1`, source.ID).Scan(&old, &oldHash)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		hash := assessmentDigest(in)
		if err == nil {
			if old.DeviceRef != in.DeviceRef || old.OS != in.OS || in.ObservedAt.Before(old.ObservedAt) {
				return ErrConflict
			}
			if in.ObservedAt.Equal(old.ObservedAt) {
				if hash != oldHash {
					return ErrConflict
				}
				result, err = readEndpointDevice(ctx, tx, ws, source.ID)
				return err
			}
		}
		if err = endChangedEndpointSessions(ctx, tx, ws, source.ID, in); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO plume.endpoint_devices(workspace_id,source_id,device_ref,os,heartbeat,heartbeat_digest,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(source_id) DO UPDATE SET heartbeat=EXCLUDED.heartbeat,heartbeat_digest=EXCLUDED.heartbeat_digest,observed_at=EXCLUDED.observed_at,last_seen_at=clock_timestamp()`, ws, source.ID, in.DeviceRef, in.OS, in, hash, in.ObservedAt)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE plume.operations_sources SET last_seen=clock_timestamp() WHERE id=$1`, source.ID)
		if err != nil {
			return err
		}
		result, err = readEndpointDevice(ctx, tx, ws, source.ID)
		return err
	})
	return result, storeError(err)
}
func (s *Store) ListEndpointDevices(ctx context.Context, ws, actor int64) ([]EndpointDevice, error) {
	result := []EndpointDevice{}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, ws, actor, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT source_id FROM plume.endpoint_devices WHERE workspace_id=$1 ORDER BY source_id DESC LIMIT 100`, ws)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, id := range ids {
			d, err := readEndpointDevice(ctx, tx, ws, id)
			if err != nil {
				return err
			}
			result = append(result, d)
		}
		return nil
	})
	return result, storeError(err)
}

func validateEndpointPolicy(in *EndpointPolicyInput) bool {
	if in.SourceID < 1 || !digestPattern.MatchString(in.DeviceRef) || !slices.Contains([]string{"usb", "print", "clipboard"}, in.Channel) || !slices.Contains([]string{"controlled_operations", "native_control", "session_response"}, in.Scope) || !slices.Contains(endpointModes, in.Mode) || !validCheckText(in.Reason, 400) || in.DurationMinutes < 1 || in.DurationMinutes > 1440 || in.Revision < 0 || len(in.Categories) > 9 {
		return false
	}
	if in.Scope == "native_control" {
		if in.Mode != "block_all" || len(in.Categories) != 0 || !validEndpointTargets(in.Channel, in.TargetScope, in.ConfigDigest, in.TargetRefs) {
			return false
		}
	} else if in.Scope == "session_response" {
		if !validEndpointSessionTarget(in.Channel, in.TargetScope, in.ConfigDigest, in.TargetRefs) || !slices.Contains(endpointSessionModes, in.Mode) {
			return false
		}
		if in.Mode != "clear_sensitive" && len(in.Categories) > 0 {
			return false
		}
		if in.Mode == "clear_sensitive" {
			if len(in.Categories) == 0 {
				return false
			}
			for _, c := range in.Categories {
				if !slices.Contains(endpointSessionCategories, c) {
					return false
				}
			}
		}
	} else if in.Mode == "clear_sensitive" || in.Mode == "clear_all" || in.TargetScope != "" || in.ConfigDigest != "" || len(in.TargetRefs) > 0 {
		return false
	}
	if in.Categories == nil {
		in.Categories = []string{}
	}
	seen := map[string]bool{}
	for _, c := range in.Categories {
		if seen[c] || !slices.Contains(endpointCategories, c) {
			return false
		}
		seen[c] = true
	}
	sort.Strings(in.Categories)
	return in.Mode != "block_sensitive" || len(in.Categories) > 0
}
func readEndpointPolicy(ctx context.Context, tx pgx.Tx, ws, id int64) (EndpointPolicy, error) {
	var p EndpointPolicy
	err := tx.QueryRow(ctx, `SELECT p.body,p.actor_id,p.digest,p.status,COALESCE(u.username,''),p.approved_at,p.decision_note,p.ended_at FROM plume.endpoint_channel_policies p LEFT JOIN plume.users u ON u.id=p.approved_by WHERE p.workspace_id=$1 AND p.id=$2`, ws, id).Scan(&p, &p.CreatorID, &p.Digest, &p.Status, &p.Approver, &p.ApprovedAt, &p.DecisionNote, &p.EndedAt)
	return p, storeError(err)
}
func refreshEndpointPolicy(ctx context.Context, tx pgx.Tx, ws, actor int64, role string, p *EndpointPolicy) error {
	d, err := readEndpointDevice(ctx, tx, ws, p.SourceID)
	if err != nil {
		return err
	}
	c, h := endpointCapability(d, p.Channel, p.Scope)
	valid := ((p.Scope != "native_control" && p.Scope != "session_response") || endpointNativeCapabilityMatches(*p, c)) && d.Current && d.DeviceRef == p.DeviceRef && d.OS == p.OS && c.Supported && h == p.CapabilityDigest && c.Executor == p.Executor && slices.Contains(c.Modes, p.Mode)
	unexpired := p.ExpiresAt.After(time.Now())
	p.Current = p.Status == "approved" && valid && unexpired
	p.CanReview = p.Status == "draft" && p.CreatorID != actor && (role == "owner" || role == "reviewer") && valid && unexpired
	p.DeploymentState = "pending"
	if !unexpired {
		p.DeploymentState = "expired"
	} else if !p.Current {
		p.DeploymentState = "unavailable"
	}
	var r EndpointReceipt
	err = tx.QueryRow(ctx, `SELECT id,receipt,source_id,created_at,snapshot_digest FROM plume.endpoint_channel_receipts WHERE workspace_id=$1 AND policy_id=$2 ORDER BY (receipt->>'observedAt')::timestamptz DESC,id DESC LIMIT 1`, ws, p.ID).Scan(&r.ID, &r.EndpointReceiptInput, &r.SourceID, &r.ReceivedAt, &r.SnapshotDigest)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		p.LatestReceipt = &r
		if p.Current {
			p.DeploymentState = r.Status
		}
	}
	refreshEndpointSession(p, d, c)
	return refreshEndpointNative(ctx, tx, ws, p)
}
func (s *Store) CreateEndpointPolicy(ctx context.Context, ws, actor int64, in EndpointPolicyInput) (EndpointPolicy, error) {
	if !validateEndpointPolicy(&in) {
		return EndpointPolicy{}, ErrInvalid
	}
	var p EndpointPolicy
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, ws, actor, true)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		if err = lockEndpointSource(ctx, tx, ws, in.SourceID, true); err != nil {
			return err
		}
		d, err := readEndpointDevice(ctx, tx, ws, in.SourceID)
		if err != nil {
			return err
		}
		if !d.Current || d.DeviceRef != in.DeviceRef {
			return ErrEvidenceRequired
		}
		var revision int64
		if err = tx.QueryRow(ctx, `SELECT COALESCE(max(revision),0) FROM plume.endpoint_channel_policies WHERE source_id=$1 AND channel=$2 AND scope=$3`, in.SourceID, in.Channel, in.Scope).Scan(&revision); err != nil {
			return err
		}
		if revision != in.Revision {
			return ErrConflict
		}
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM plume.endpoint_channel_policies WHERE workspace_id=$1 AND status='draft'`, ws).Scan(&count); err != nil {
			return err
		}
		if count >= 100 {
			return ErrRuntimeLimit
		}
		p.EndpointPolicyInput = in
		p.Revision++
		p.SourceName = d.SourceName
		p.OS = d.OS
		p.CreatorID = actor
		c, h := endpointCapability(d, p.Channel, p.Scope)
		if (p.Scope == "native_control" || p.Scope == "session_response") && (!c.Supported || !endpointNativeCapabilityMatches(p, c)) {
			return ErrEvidenceRequired
		}
		p.Executor = c.Executor
		p.Capability = c
		p.AgentVersion = d.AgentVersion
		p.CapabilityDigest = h
		if err = tx.QueryRow(ctx, `SELECT nextval('plume.endpoint_channel_policies_id_seq'),clock_timestamp(),username FROM plume.users WHERE id=$1`, actor).Scan(&p.ID, &p.CreatedAt, &p.Creator); err != nil {
			return err
		}
		p.CreatedAt = p.CreatedAt.UTC().Truncate(time.Microsecond)
		p.ExpiresAt = p.CreatedAt.Add(time.Duration(p.DurationMinutes) * time.Minute)
		p.Digest = assessmentDigest(p)
		_, err = tx.Exec(ctx, `INSERT INTO plume.endpoint_channel_policies(id,workspace_id,source_id,channel,scope,revision,actor_id,body,digest,status,expires_at,created_at) OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'draft',$10,$11)`, p.ID, ws, p.SourceID, p.Channel, p.Scope, p.Revision, actor, p, p.Digest, p.ExpiresAt, p.CreatedAt)
		if err != nil {
			return err
		}
		p.Status = "draft"
		return refreshEndpointPolicy(ctx, tx, ws, actor, role, &p)
	})
	return p, storeError(err)
}
func (s *Store) ListEndpointPolicies(ctx context.Context, ws, actor, sourceID int64, channel string) ([]EndpointPolicy, error) {
	result := []EndpointPolicy{}
	if sourceID < 0 || channel != "" && !slices.Contains([]string{"usb", "print", "clipboard"}, channel) {
		return result, ErrInvalid
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, ws, actor, false)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id FROM plume.endpoint_channel_policies WHERE workspace_id=$1 AND ($2::bigint=0 OR source_id=$2) AND ($3='' OR channel=$3) ORDER BY id DESC LIMIT 100`, ws, sourceID, channel)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, id := range ids {
			p, err := readEndpointPolicy(ctx, tx, ws, id)
			if err != nil {
				return err
			}
			if err = refreshEndpointPolicy(ctx, tx, ws, actor, role, &p); err != nil {
				return err
			}
			result = append(result, p)
		}
		return nil
	})
	return result, storeError(err)
}
func (s *Store) DecideEndpointPolicy(ctx context.Context, ws, actor, id, revision int64, action, note string) (EndpointPolicy, error) {
	if revision < 1 || !slices.Contains([]string{"approve", "reject", "revoke"}, action) || action != "approve" && !validCheckText(note, 400) || action == "approve" && note != "" {
		return EndpointPolicy{}, ErrInvalid
	}
	var p EndpointPolicy
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, ws, actor, true)
		if err != nil {
			return err
		}
		p, err = readEndpointPolicy(ctx, tx, ws, id)
		if err != nil {
			return err
		}
		// Every writer locks source first; source-token requests use the same lock.
		if err = lockEndpointSource(ctx, tx, ws, p.SourceID, action == "approve"); err != nil {
			return err
		}
		p, err = readEndpointPolicy(ctx, tx, ws, id)
		if err != nil {
			return err
		}
		if p.Revision != revision {
			return ErrConflict
		}
		if action == "revoke" {
			if role != "owner" {
				return ErrForbidden
			}
			if p.Status != "approved" && p.Status != "draft" {
				return ErrConflict
			}
			_, err = tx.Exec(ctx, `UPDATE plume.endpoint_channel_policies SET status='revoked',decision_note=$3,ended_at=COALESCE(ended_at,clock_timestamp()) WHERE workspace_id=$1 AND id=$2`, ws, id, note)
		} else {
			if role != "owner" && role != "reviewer" || p.CreatorID == actor {
				return ErrForbidden
			}
			if p.Status != "draft" {
				return ErrConflict
			}
			if action == "approve" {
				if err = refreshEndpointPolicy(ctx, tx, ws, actor, role, &p); err != nil {
					return err
				}
				if !p.CanReview {
					return ErrEvidenceRequired
				}
				// Approving an older draft cannot replace a newer approved revision.
				var newer bool
				if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.endpoint_channel_policies WHERE source_id=$1 AND channel=$2 AND scope=$3 AND revision>$4 AND status IN ('approved','superseded'))`, p.SourceID, p.Channel, p.Scope, p.Revision).Scan(&newer); err != nil {
					return err
				}
				if newer {
					return ErrConflict
				}
				_, err = tx.Exec(ctx, `UPDATE plume.endpoint_channel_policies SET status='superseded',ended_at=COALESCE(ended_at,clock_timestamp()) WHERE source_id=$1 AND channel=$2 AND scope=$3 AND status='approved'`, p.SourceID, p.Channel, p.Scope)
				if err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `UPDATE plume.endpoint_channel_policies SET status='approved',approved_by=$3,approved_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, ws, id, actor)
			} else {
				_, err = tx.Exec(ctx, `UPDATE plume.endpoint_channel_policies SET status='rejected',decision_note=$3 WHERE workspace_id=$1 AND id=$2`, ws, id, note)
			}
		}
		if err != nil {
			return err
		}
		p, err = readEndpointPolicy(ctx, tx, ws, id)
		if err != nil {
			return err
		}
		if action == "approve" && p.Scope == "native_control" {
			if err = createEndpointNativeLease(ctx, tx, ws, p); err != nil {
				return err
			}
		}
		return refreshEndpointPolicy(ctx, tx, ws, actor, role, &p)
	})
	return p, storeError(err)
}
func (s *Store) EndpointPoliciesForAgent(ctx context.Context, digest, deviceRef string) (EndpointPolicyBundle, error) {
	result := EndpointPolicyBundle{DeviceRef: deviceRef, Policies: []EndpointPolicy{}, NativeCommands: []EndpointNativeCommand{}}
	if !digestPattern.MatchString(deviceRef) {
		return result, ErrInvalid
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		source, ws, _, err := operationsSourceActor(ctx, tx, "security_events", digest)
		if err != nil {
			return err
		}
		d, err := readEndpointDevice(ctx, tx, ws, source.ID)
		if err != nil {
			return err
		}
		if d.DeviceRef != deviceRef {
			return ErrConflict
		}
		if !d.Current {
			return ErrEvidenceRequired
		}
		rows, err := tx.Query(ctx, `SELECT id FROM plume.endpoint_channel_policies WHERE source_id=$1 AND status='approved' AND expires_at>clock_timestamp() ORDER BY id`, source.ID)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, id := range ids {
			p, err := readEndpointPolicy(ctx, tx, ws, id)
			if err != nil {
				return err
			}
			if err = refreshEndpointPolicy(ctx, tx, ws, 0, "", &p); err != nil {
				return err
			}
			if p.Current {
				result.Policies = append(result.Policies, p)
			}
		}
		result.NativeCommands, err = endpointNativeCommands(ctx, tx, ws, source.ID)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&result.IssuedAt)
	})
	return result, storeError(err)
}
