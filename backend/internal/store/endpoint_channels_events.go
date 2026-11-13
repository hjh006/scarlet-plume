package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
	"github.com/jackc/pgx/v5"
)

// Endpoint fields are source evidence. Heartbeat, policy approval and loading
// receipts alone never become a risk clear or a verified operation.
func validateEndpointSecurityEvent(ctx context.Context, tx pgx.Tx, ws, sourceID int64, in SecurityEventInput) error {
	c := in.DataContext
	if c == nil || c.Endpoint == nil {
		return nil
	}
	e := c.Endpoint
	if in.Outcome == "cleared" {
		return ErrEvidenceRequired
	}
	d, err := readEndpointDevice(ctx, tx, ws, sourceID)
	if err != nil {
		return err
	}
	if d.DeviceRef != c.DeviceRef {
		return ErrConflict
	}
	if e.Scope == "observation" {
		cap, _ := endpointCapability(d, c.Channel, "observation")
		if !d.Current || !cap.Supported || cap.Executor != e.Executor {
			return ErrEvidenceRequired
		}
		return nil
	}
	p, err := readEndpointPolicy(ctx, tx, ws, e.PolicyID)
	if err != nil {
		return err
	}
	if p.SourceID != sourceID || p.DeviceRef != c.DeviceRef || p.OS != d.OS || p.Revision != e.PolicyRevision || p.Digest != c.PolicyDigest || p.Channel != c.Channel || p.Scope != e.Scope || p.Executor != e.Executor {
		return ErrConflict
	}
	if p.Mode == "monitor" && c.Enforcement == "blocked" || p.Mode == "block_all" && c.Enforcement == "allowed" {
		return ErrInvalid
	}
	if p.Mode == "block_sensitive" && c.Enforcement == "blocked" && !slices.ContainsFunc(c.Categories, func(category string) bool { return slices.Contains(p.Categories, category) }) {
		return ErrInvalid
	}
	if p.Mode == "block_sensitive" && c.Enforcement == "allowed" {
		if c.Capture != "complete" || !datasafety.SupportsTextPolicy(datasafety.Policy{ProtectedCategories: p.Categories}) || slices.ContainsFunc(c.Categories, func(category string) bool { return slices.Contains(p.Categories, category) }) {
			return ErrInvalid
		}
	}
	// A completion is evidence of an operation already admitted by the source,
	// never permission to start a new operation under an expired/failed policy.
	// Retain its original loading proof even if deployment fails afterwards.
	asOf := in.ObservedAt
	if e.Stage == "completed" || e.Stage == "post_action" {
		var beganAt time.Time
		err = tx.QueryRow(ctx, `SELECT x.observed_at FROM plume.operations_security_events x WHERE x.workspace_id=$1 AND x.source_id=$2 AND x.metadata->'dataContext'->'endpoint'->>'policyId'=$3 AND x.metadata->'dataContext'->'endpoint'->>'operationRef'=$4 AND x.metadata->'dataContext'->>'deviceRef'=$5 AND x.metadata->'dataContext'->>'channel'=$6 AND x.metadata->'dataContext'->>'enforcement'='observed' AND x.metadata->'dataContext'->'endpoint'->>'stage'='observed' AND COALESCE(x.metadata->'dataContext'->>'subjectRef','')=$7 AND COALESCE(x.metadata->'dataContext'->>'fileDigest','')=$8 AND x.metadata->'dataContext'->>'operation'=$9 AND x.observed_at<=$10 AND x.observed_at>=$10::timestamptz-interval '10 minutes' AND x.metadata->'dataContext'->'endpoint'->>'policyRevision'=$11 AND x.metadata->'dataContext'->>'policyDigest'=$12 AND x.metadata->'dataContext'->'endpoint'->>'scope'=$13 AND x.metadata->'dataContext'->'endpoint'->>'executor'=$14 AND (x.metadata->>'count')::bigint>0 ORDER BY x.observed_at DESC,x.id DESC LIMIT 1`, ws, sourceID, fmt.Sprint(p.ID), e.OperationRef, c.DeviceRef, c.Channel, c.SubjectRef, c.FileDigest, c.Operation, in.ObservedAt, fmt.Sprint(p.Revision), p.Digest, p.Scope, p.Executor).Scan(&beganAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		asOf = beganAt
	}
	valid, err := endpointPolicyAt(ctx, tx, ws, p, asOf)
	if err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	var applied bool
	err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT receipt->>'status'='applied' FROM plume.endpoint_channel_receipts WHERE workspace_id=$1 AND source_id=$2 AND policy_id=$3 AND (receipt->>'observedAt')::timestamptz<=$4 ORDER BY (receipt->>'observedAt')::timestamptz DESC,id DESC LIMIT 1),false)`, ws, sourceID, p.ID, asOf).Scan(&applied)
	if err != nil {
		return err
	}
	if !applied {
		return ErrEvidenceRequired
	}
	return nil
}
