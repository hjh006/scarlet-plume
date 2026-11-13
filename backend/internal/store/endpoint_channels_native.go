package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

func endpointNativeExecutor(channel string) string {
	return map[string]string{"usb": "windows_usb_removable_write", "print": "windows_print_queue_pause", "clipboard": "windows_rdp_clipboard_policy"}[channel]
}
func endpointNativeTarget(channel string) string {
	return map[string]string{"usb": "removable_storage_write", "print": "configured_print_queues", "clipboard": "rdp_clipboard_redirection"}[channel]
}
func validEndpointTargets(channel, scope, digest string, refs []string) bool {
	if scope != endpointNativeTarget(channel) || !digestPattern.MatchString(digest) || len(refs) < 1 || len(refs) > 20 || channel != "print" && len(refs) != 1 {
		return false
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		if !digestPattern.MatchString(ref) || seen[ref] {
			return false
		}
		seen[ref] = true
	}
	sort.Strings(refs)
	return true
}
func endpointNativeCapabilityMatches(p EndpointPolicy, c EndpointCapability) bool {
	return p.TargetScope == c.TargetScope && p.ConfigDigest == c.ConfigDigest && slices.Equal(p.TargetRefs, c.TargetRefs)
}
func readEndpointNativeLease(ctx context.Context, tx pgx.Tx, ws, id int64) (EndpointNativeLease, error) {
	var lease EndpointNativeLease
	err := tx.QueryRow(ctx, `SELECT manifest,state,prior_state_digest,planned_state_digest,prepared_at,applied_state_digest,updated_at FROM plume.endpoint_native_leases WHERE workspace_id=$1 AND policy_id=$2`, ws, id).Scan(&lease, &lease.State, &lease.PriorStateDigest, &lease.PlannedStateDigest, &lease.PreparedAt, &lease.AppliedStateDigest, &lease.UpdatedAt)
	return lease, err
}
func createEndpointNativeLease(ctx context.Context, tx pgx.Tx, ws int64, p EndpointPolicy) error {
	var pending int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM plume.endpoint_native_leases WHERE source_id=$1 AND state<>'rolled_back'`, p.SourceID).Scan(&pending); err != nil {
		return err
	}
	if pending >= 30 {
		return ErrRuntimeLimit
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	lease := EndpointNativeLease{LeaseRef: hex.EncodeToString(nonce[:]), IssuedAt: *p.ApprovedAt, ExpiresAt: p.ExpiresAt}
	lease.ApplyOperationRef = assessmentDigest([]string{lease.LeaseRef, p.Digest, "apply"})
	lease.RollbackOperationRef = assessmentDigest([]string{lease.LeaseRef, p.Digest, "rollback"})
	_, err := tx.Exec(ctx, `INSERT INTO plume.endpoint_native_leases(policy_id,workspace_id,source_id,channel,manifest,lease_ref) VALUES($1,$2,$3,$4,$5,$6)`, p.ID, ws, p.SourceID, p.Channel, lease, lease.LeaseRef)
	return err
}
func endpointNativeBlocked(ctx context.Context, tx pgx.Tx, p EndpointPolicy, at *time.Time) (bool, error) {
	var blocked bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.endpoint_native_leases WHERE source_id=$1 AND channel=$2 AND policy_id<$3 AND (state<>'rolled_back' OR $4::timestamptz IS NOT NULL AND latest_observed_at>$4))`, p.SourceID, p.Channel, p.ID, at).Scan(&blocked)
	return blocked, err
}
func refreshEndpointNative(ctx context.Context, tx pgx.Tx, ws int64, p *EndpointPolicy) error {
	if p.Scope != "native_control" {
		return nil
	}
	lease, err := readEndpointNativeLease(ctx, tx, ws, p.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		if p.Status == "approved" {
			p.Current = false
			p.DeploymentState = "unavailable"
		}
		return nil
	}
	if err != nil {
		return err
	}
	p.NativeLease = &lease
	p.DeploymentState = lease.State
	if lease.State == "rolled_back" {
		p.Current = false
		return nil
	}
	if !p.Current {
		if lease.State != "conflict" {
			p.DeploymentState = "rollback_pending"
		}
		return nil
	}
	blocked, err := endpointNativeBlocked(ctx, tx, *p, nil)
	if err != nil {
		return err
	}
	if blocked {
		p.DeploymentState = "awaiting_rollback"
	}
	return nil
}
func endpointNativeStopReason(p EndpointPolicy) string {
	if !p.ExpiresAt.After(time.Now()) {
		return "expired"
	}
	if p.Status == "revoked" || p.Status == "superseded" {
		return p.Status
	}
	if !p.Current {
		return "capability_changed"
	}
	return ""
}
func endpointNativeCommands(ctx context.Context, tx pgx.Tx, ws, sourceID int64) ([]EndpointNativeCommand, error) {
	commands := []EndpointNativeCommand{}
	rows, err := tx.Query(ctx, `SELECT policy_id FROM plume.endpoint_native_leases WHERE workspace_id=$1 AND source_id=$2 AND state<>'rolled_back' ORDER BY policy_id LIMIT 30`, ws, sourceID)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		p, err := readEndpointPolicy(ctx, tx, ws, id)
		if err != nil {
			return nil, err
		}
		if err = refreshEndpointPolicy(ctx, tx, ws, 0, "", &p); err != nil {
			return nil, err
		}
		if p.NativeLease == nil {
			return nil, ErrEvidenceRequired
		}
		command := EndpointNativeCommand{LeaseRef: p.NativeLease.LeaseRef, Policy: p}
		reason := endpointNativeStopReason(p)
		if reason != "" {
			command.Action = "rollback"
			command.Reason = reason
			command.OperationRef = p.NativeLease.RollbackOperationRef
		} else {
			blocked, err := endpointNativeBlocked(ctx, tx, p, nil)
			if err != nil {
				return nil, err
			}
			if blocked {
				continue
			}
			command.Action = "apply"
			command.Reason = "approved"
			command.OperationRef = p.NativeLease.ApplyOperationRef
		}
		commands = append(commands, command)
	}
	return commands, nil
}
func validEndpointNativeReceipt(in *EndpointReceiptInput) bool {
	if !slices.Contains([]string{"prepared", "configured", "readback", "functional_verified", "rolled_back", "conflict", "failed"}, in.Phase) || !slices.Contains([]string{"apply", "rollback"}, in.Action) || !digestPattern.MatchString(in.LeaseRef) || !digestPattern.MatchString(in.OperationRef) || !validEndpointTargets(in.Channel, in.TargetScope, in.ConfigDigest, in.TargetRefs) {
		return false
	}
	if in.Phase == "failed" || in.Phase == "conflict" {
		if in.PriorStateDigest != "" && !digestPattern.MatchString(in.PriorStateDigest) || in.StateDigest != "" && !digestPattern.MatchString(in.StateDigest) {
			return false
		}
	} else if !digestPattern.MatchString(in.PriorStateDigest) || !digestPattern.MatchString(in.StateDigest) {
		return false
	}
	switch in.Phase {
	case "prepared":
		if in.Action != "apply" || in.Status != "applied" || in.Code != "native_prepared" {
			return false
		}
	case "configured":
		if in.Action != "apply" || in.Status != "applied" || in.Code != "native_configured" {
			return false
		}
	case "readback":
		if in.Action != "apply" || in.Status != "applied" || in.Code != "native_readback_match" {
			return false
		}
	case "functional_verified":
		if in.Action != "apply" || in.Status != "applied" || in.Code != "native_functional_blocked" || !digestPattern.MatchString(in.VerificationRef) || in.VerificationMethod != map[string]string{"usb": "removable_write_probe", "print": "configured_queue_job_retained", "clipboard": "rdp_clipboard_probe"}[in.Channel] {
			return false
		}
	case "rolled_back":
		if in.Action != "rollback" || in.Status != "applied" || !slices.Contains([]string{"native_rolled_back", "native_not_applied"}, in.Code) || in.StateDigest != in.PriorStateDigest {
			return false
		}
	case "conflict":
		if in.Status != "failed" || in.Code != "native_conflict" {
			return false
		}
	case "failed":
		if !slices.Contains([]string{"failed", "unsupported"}, in.Status) || !slices.Contains([]string{"permission_denied", "executor_unavailable", "policy_expired", "configuration_mismatch", "execution_failed", "unsupported_scope", "unsupported_channel", "unsupported_mode"}, in.Code) {
			return false
		}
	}
	return in.Phase == "functional_verified" || in.VerificationMethod == "" && in.VerificationRef == ""
}
func recordEndpointNativeReceipt(ctx context.Context, tx pgx.Tx, ws int64, p EndpointPolicy, in EndpointReceiptInput) error {
	if !endpointNativeCapabilityMatches(p, EndpointCapability{TargetScope: in.TargetScope, TargetRefs: in.TargetRefs, ConfigDigest: in.ConfigDigest}) {
		return ErrConflict
	}
	lease, err := readEndpointNativeLease(ctx, tx, ws, p.ID)
	if err != nil {
		return storeError(err)
	}
	if lease.LeaseRef != in.LeaseRef || in.Action == "apply" && lease.ApplyOperationRef != in.OperationRef || in.Action == "rollback" && lease.RollbackOperationRef != in.OperationRef || in.ObservedAt.Before(lease.IssuedAt) {
		return ErrConflict
	}
	if lease.PriorStateDigest != "" && in.PriorStateDigest != "" && lease.PriorStateDigest != in.PriorStateDigest {
		return ErrConflict
	}
	var latest *time.Time
	if err = tx.QueryRow(ctx, `SELECT latest_observed_at FROM plume.endpoint_native_leases WHERE policy_id=$1`, p.ID).Scan(&latest); err != nil {
		return err
	}
	historical := latest != nil && in.ObservedAt.Before(*latest)
	if in.Action == "apply" {
		asOf := in.ObservedAt
		if in.Phase == "configured" {
			if lease.PreparedAt == nil {
				return ErrEvidenceRequired
			}
			if in.ObservedAt.Before(*lease.PreparedAt) || in.ObservedAt.Sub(*lease.PreparedAt) > 10*time.Minute {
				return ErrConflict
			}
			asOf = *lease.PreparedAt
		}
		valid, err := endpointPolicyAt(ctx, tx, ws, p, asOf)
		if err != nil {
			return err
		}
		if !valid && lease.PreparedAt != nil && in.Phase != "prepared" && !in.ObservedAt.Before(*lease.PreparedAt) && in.ObservedAt.Sub(*lease.PreparedAt) <= 10*time.Minute {
			valid, err = endpointPolicyAt(ctx, tx, ws, p, *lease.PreparedAt)
			if err != nil {
				return err
			}
			asOf = *lease.PreparedAt
		}
		if !valid {
			return ErrConflict
		}
		blocked, err := endpointNativeBlocked(ctx, tx, p, &asOf)
		if err != nil {
			return err
		}
		if blocked {
			return ErrEvidenceRequired
		}
		if lease.State == "rolled_back" && !historical {
			return ErrConflict
		}
		if in.Phase == "prepared" {
			if lease.PlannedStateDigest != "" && lease.PlannedStateDigest != in.StateDigest {
				return ErrConflict
			}
			if lease.PreparedAt != nil && !historical && lease.State != "prepared" {
				return ErrConflict
			}
		}
		if slices.Contains([]string{"configured", "readback", "functional_verified"}, in.Phase) {
			if in.Phase == "configured" && lease.PlannedStateDigest != in.StateDigest {
				return ErrConflict
			}
			if lease.PriorStateDigest == "" || lease.PreparedAt == nil {
				return ErrEvidenceRequired
			}
			if lease.AppliedStateDigest != "" && lease.AppliedStateDigest != in.StateDigest {
				return ErrConflict
			}
			if in.Phase != "configured" && lease.AppliedStateDigest == "" {
				return ErrEvidenceRequired
			}
			if in.Phase == "functional_verified" && !slices.Contains([]string{"readback", "functional_verified"}, lease.State) {
				return ErrEvidenceRequired
			}
			if !historical && in.Phase == "configured" && slices.Contains([]string{"readback", "functional_verified"}, lease.State) {
				return ErrConflict
			}
		}
	} else {
		if in.Phase == "prepared" || in.Phase == "configured" || in.Phase == "readback" || in.Phase == "functional_verified" {
			return ErrInvalid
		}
		if in.Code == "native_not_applied" && (lease.AppliedStateDigest != "") {
			return ErrConflict
		}
		if in.Phase == "rolled_back" && in.Code == "native_rolled_back" && lease.PriorStateDigest == "" {
			return ErrEvidenceRequired
		}
	}
	if historical {
		return nil
	}
	if lease.State == "rolled_back" {
		return ErrConflict
	}
	prior := lease.PriorStateDigest
	if prior == "" && slices.Contains([]string{"prepared", "rolled_back"}, in.Phase) {
		prior = in.PriorStateDigest
	}
	planned := lease.PlannedStateDigest
	preparedAt := lease.PreparedAt
	if in.Phase == "prepared" && preparedAt == nil {
		planned = in.StateDigest
		preparedAt = &in.ObservedAt
	}
	applied := lease.AppliedStateDigest
	if applied == "" && in.Phase == "configured" {
		applied = in.StateDigest
	}
	_, err = tx.Exec(ctx, `UPDATE plume.endpoint_native_leases SET state=$2,prior_state_digest=$3,planned_state_digest=$4,prepared_at=$5,applied_state_digest=$6,latest_observed_at=$7,updated_at=clock_timestamp() WHERE policy_id=$1`, p.ID, in.Phase, prior, planned, preparedAt, applied, in.ObservedAt)
	return err
}
