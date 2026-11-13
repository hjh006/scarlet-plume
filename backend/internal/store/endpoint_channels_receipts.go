package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

var endpointReceiptCodes = []string{"policy_loaded", "operation_supported", "permission_denied", "executor_unavailable", "policy_expired", "configuration_mismatch", "execution_failed", "unsupported_scope", "unsupported_channel", "unsupported_mode"}

func validEndpointReceipt(in *EndpointReceiptInput) bool {
	in.ObservedAt = in.ObservedAt.UTC().Truncate(time.Microsecond)
	if !digestPattern.MatchString(in.ReceiptRef) || !digestPattern.MatchString(in.DeviceRef) || !digestPattern.MatchString(in.PolicyDigest) || in.PolicyID < 1 || in.PolicyRevision < 1 || !slices.Contains([]string{"applied", "failed", "unsupported"}, in.Status) || in.ObservedAt.Before(time.Now().Add(-7*24*time.Hour)) || in.ObservedAt.After(time.Now().Add(time.Minute)) {
		return false
	}
	if in.Scope == "native_control" {
		return validEndpointNativeReceipt(in)
	}
	if in.Phase != "" || in.LeaseRef != "" || in.OperationRef != "" || in.Action != "" || in.TargetScope != "" || len(in.TargetRefs) > 0 || in.ConfigDigest != "" || in.PriorStateDigest != "" || in.StateDigest != "" || in.VerificationRef != "" || in.VerificationMethod != "" || !slices.Contains(endpointReceiptCodes, in.Code) {
		return false
	}
	if in.Status == "applied" {
		return in.Code == "policy_loaded" || in.Code == "operation_supported"
	}
	return in.Code != "policy_loaded" && in.Code != "operation_supported"
}
func endpointPolicyMatchesReceipt(p EndpointPolicy, in EndpointReceiptInput) bool {
	return p.ID == in.PolicyID && p.Revision == in.PolicyRevision && p.Digest == in.PolicyDigest && p.DeviceRef == in.DeviceRef && p.Channel == in.Channel && p.Scope == in.Scope && p.Executor == in.Executor
}
func endpointPolicyAt(ctx context.Context, tx pgx.Tx, ws int64, p EndpointPolicy, at time.Time) (bool, error) {
	if p.ApprovedAt == nil || at.Before(*p.ApprovedAt) || at.After(p.ExpiresAt) || p.Status == "draft" || p.Status == "rejected" {
		return false, nil
	}
	var ended *time.Time
	if err := tx.QueryRow(ctx, `SELECT ended_at FROM plume.endpoint_channel_policies WHERE workspace_id=$1 AND id=$2`, ws, p.ID).Scan(&ended); err != nil {
		return false, err
	}
	return ended == nil || at.Before(*ended), nil
}
func (s *Store) ReceiveEndpointReceipt(ctx context.Context, digest string, in EndpointReceiptInput) (EndpointReceipt, bool, error) {
	if !validEndpointReceipt(&in) {
		return EndpointReceipt{}, false, ErrInvalid
	}
	var result EndpointReceipt
	var replay bool
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		source, ws, _, err := operationsSourceActor(ctx, tx, "security_events", digest)
		if err != nil {
			return err
		}
		inputHash := assessmentDigest(in)
		var oldHash string
		err = tx.QueryRow(ctx, `SELECT id,receipt,source_id,created_at,snapshot_digest,input_digest FROM plume.endpoint_channel_receipts WHERE source_id=$1 AND receipt_ref=$2`, source.ID, in.ReceiptRef).Scan(&result.ID, &result.EndpointReceiptInput, &result.SourceID, &result.ReceivedAt, &result.SnapshotDigest, &oldHash)
		if err == nil {
			if oldHash != inputHash {
				return ErrConflict
			}
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
		if p.SourceID != source.ID || !endpointPolicyMatchesReceipt(p, in) {
			return ErrConflict
		}
		d, err := readEndpointDevice(ctx, tx, ws, source.ID)
		if err != nil {
			return err
		}
		if d.DeviceRef != in.DeviceRef || d.OS != p.OS {
			return ErrConflict
		}
		if p.Scope == "native_control" {
			if err = recordEndpointNativeReceipt(ctx, tx, ws, p, in); err != nil {
				return err
			}
			if err = refreshEndpointPolicy(ctx, tx, ws, 0, "", &p); err != nil {
				return err
			}
		} else if in.Status == "applied" {
			valid, err := endpointPolicyAt(ctx, tx, ws, p, in.ObservedAt)
			if err != nil {
				return err
			}
			if !valid {
				return ErrConflict
			}
			// A late source outbox may retain historical receipts, but cannot activate
			// a capability that was never declared or approved for this policy.
			if p.Scope != "controlled_operations" || p.Executor == "" {
				return ErrEvidenceRequired
			}
			if p.Status == "approved" && p.ExpiresAt.After(time.Now()) {
				if err = refreshEndpointPolicy(ctx, tx, ws, 0, "", &p); err != nil {
					return err
				}
				if !p.Current {
					return ErrEvidenceRequired
				}
			}
		} else if p.ApprovedAt == nil {
			return ErrConflict
		}
		result.EndpointReceiptInput = in
		result.SourceID = source.ID
		if err = tx.QueryRow(ctx, `SELECT nextval('plume.endpoint_channel_receipts_id_seq'),clock_timestamp()`).Scan(&result.ID, &result.ReceivedAt); err != nil {
			return err
		}
		snapshot := struct {
			Version int             `json:"version"`
			Policy  EndpointPolicy  `json:"policy"`
			Device  EndpointDevice  `json:"device"`
			Receipt EndpointReceipt `json:"receipt"`
		}{1, p, d, result}
		result.SnapshotDigest = assessmentDigest(snapshot)
		_, err = tx.Exec(ctx, `INSERT INTO plume.endpoint_channel_receipts(id,workspace_id,source_id,policy_id,receipt_ref,input_digest,receipt,snapshot,snapshot_digest,created_at) OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, result.ID, ws, source.ID, p.ID, in.ReceiptRef, inputHash, in, snapshot, result.SnapshotDigest, result.ReceivedAt)
		return err
	})
	return result, replay, storeError(err)
}
func readEndpointReceipts(ctx context.Context, tx pgx.Tx, ws, policyID int64) ([]EndpointReceipt, error) {
	rows, err := tx.Query(ctx, `SELECT id,receipt,source_id,created_at,snapshot_digest FROM plume.endpoint_channel_receipts WHERE workspace_id=$1 AND policy_id=$2 ORDER BY id DESC LIMIT 100`, ws, policyID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (EndpointReceipt, error) {
		var r EndpointReceipt
		err := row.Scan(&r.ID, &r.EndpointReceiptInput, &r.SourceID, &r.ReceivedAt, &r.SnapshotDigest)
		return r, err
	})
}
func (s *Store) EndpointPolicyDetail(ctx context.Context, ws, actor, id int64) (EndpointPolicyDetail, error) {
	var result EndpointPolicyDetail
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, ws, actor, false)
		if err != nil {
			return err
		}
		result.EndpointPolicy, err = readEndpointPolicy(ctx, tx, ws, id)
		if err != nil {
			return err
		}
		if err = refreshEndpointPolicy(ctx, tx, ws, actor, role, &result.EndpointPolicy); err != nil {
			return err
		}
		result.Receipts, err = readEndpointReceipts(ctx, tx, ws, id)
		return err
	})
	return result, storeError(err)
}
