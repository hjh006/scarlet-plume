package store

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/valuefingerprint"
	"github.com/jackc/pgx/v5"
)

var runtimeScopeReference = regexp.MustCompile(`^[0-9a-f]{64}$`)

func normalizeRuntimeEvidenceScope(s RuntimeEvidenceScope) (RuntimeEvidenceScope, error) {
	if s.From.IsZero() && s.To.IsZero() {
		s.To = time.Now().UTC()
		s.From = s.To.Add(-time.Hour)
	}
	s.From = s.From.UTC()
	s.To = s.To.UTC()
	if s.SourceID < 0 || s.From.IsZero() || s.To.IsZero() || !s.To.After(s.From) || s.To.Sub(s.From) > 32*24*time.Hour || s.To.After(time.Now().Add(time.Minute)) || len(s.Service) > 80 || len(s.Route) > 200 || strings.IndexFunc(s.Service+s.Route, unicode.IsControl) >= 0 || s.SubjectRef != "" && !runtimeScopeReference.MatchString(s.SubjectRef) {
		return s, ErrInvalid
	}
	if !classificationFilterValue(s.Method, "", "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS", "TRACE") {
		return s, ErrInvalid
	}
	if s.ClientIP != "" {
		ip, e := netip.ParseAddr(s.ClientIP)
		if e != nil || ip.Zone() != "" || ip.IsUnspecified() {
			return s, ErrInvalid
		}
		s.ClientIP = ip.Unmap().String()
	}
	if s.ValueFingerprint != nil && !valuefingerprint.Valid(*s.ValueFingerprint) {
		return s, ErrInvalid
	}
	return s, nil
}

const runtimeEvidenceWhere = ` WHERE src.workspace_id=$1 AND ($2::bigint=0 OR src.id=$2) AND ($3='' OR src.service=$3)
 AND a.observed_at >= $4 AND a.observed_at <= $5 AND ($6='' OR a.method=$6)
 AND ($7='' OR a.route_key=$7) AND ($8='' OR a.observation->>'subjectRef'=$8) AND ($9='' OR a.observation->>'clientIp'=$9)
 AND ($10::jsonb IS NULL OR a.observation->'valueFingerprints' @> $10::jsonb)`

func runtimeScopeArgs(ws int64, s RuntimeEvidenceScope) []any {
	var token any
	if s.ValueFingerprint != nil {
		token = []valuefingerprint.Token{*s.ValueFingerprint}
	}
	return []any{ws, s.SourceID, s.Service, s.From, s.To, s.Method, runtimesecurity.RouteKey(s.Route), s.SubjectRef, s.ClientIP, token}
}
func readRuntimeEvidenceWindow(ctx context.Context, tx pgx.Tx, ws int64, scope RuntimeEvidenceScope, limit int) (RuntimeEvidenceWindow, error) {
	var e error
	scope, e = normalizeRuntimeEvidenceScope(scope)
	if e != nil {
		return RuntimeEvidenceWindow{}, e
	}
	if limit < 1 || limit > 20000 {
		return RuntimeEvidenceWindow{}, ErrInvalid
	}
	out := RuntimeEvidenceWindow{Scope: scope, Items: []RuntimeEvidenceEntry{}, Limit: limit, RetentionDays: RuntimeAuditRetentionDays, RetentionLimitPerSource: RuntimeAuditPerSourceLimit, Warnings: []string{"observed_requests_only_not_complete_business_traffic", "subject_reference_scoped_to_source", "countries_and_recipients_are_explicit_source_labels_not_ip_geolocation"}}
	if scope.SourceID > 0 {
		var id int64
		if e = tx.QueryRow(ctx, `SELECT id FROM plume.runtime_security_sources WHERE workspace_id=$1 AND id=$2`, ws, scope.SourceID).Scan(&id); e != nil {
			return out, e
		}
	}
	args := runtimeScopeArgs(ws, scope)
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM plume.runtime_request_audit a JOIN plume.runtime_security_sources src ON src.id=a.source_id`+runtimeEvidenceWhere, args...).Scan(&out.Total); e != nil {
		return out, e
	}
	args = append(args, limit)
	rows, e := tx.Query(ctx, `SELECT a.id,src.id,src.name,src.service,src.kind,COALESCE(src.revoked_at IS NULL AND src.last_seen>now()-interval '1 day' AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=src.workspace_id AND m.user_id=src.actor_id AND m.role='owner'),false),a.observation,a.method,a.route,a.status,a.observed_at FROM plume.runtime_request_audit a JOIN plume.runtime_security_sources src ON src.id=a.source_id`+runtimeEvidenceWhere+` ORDER BY a.observed_at DESC,a.id DESC LIMIT $11`, args...)
	if e != nil {
		return out, e
	}
	out.Items, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (RuntimeEvidenceEntry, error) {
		var item RuntimeEvidenceEntry
		var method, route string
		var status int
		var at time.Time
		e := row.Scan(&item.ID, &item.SourceID, &item.Source, &item.Service, &item.Kind, &item.SourceCurrent, &item.Observation, &method, &route, &status, &at)
		if item.Observation.Method == "" {
			item.Observation.Method = method
			item.Observation.Route = route
			item.Observation.Status = status
			item.Observation.ObservedAt = at
			item.Observation.CaptureWarnings = []string{"legacy_audit_projection_without_capture_evidence"}
		}
		return item, e
	})
	if e != nil {
		return out, e
	}
	out.Truncated = out.Total > int64(limit)
	if out.Truncated {
		out.Warnings = append(out.Warnings, "matching_rows_truncated_after_scope_filter")
	}
	if scope.From.Before(time.Now().Add(-RuntimeAuditRetentionDays * 24 * time.Hour)) {
		out.Warnings = append(out.Warnings, "requested_window_exceeds_retained_audit")
	}
	return out, nil
}
func (s *Store) RuntimeEvidence(ctx context.Context, ws, actor int64, scope RuntimeEvidenceScope) (RuntimeEvidenceWindow, error) {
	var out RuntimeEvidenceWindow
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var e error
		out, e = readRuntimeEvidenceWindow(ctx, tx, ws, scope, 500)
		return e
	})
	return out, storeError(e)
}
func (s *Store) SearchRuntimeValueFingerprint(ctx context.Context, ws, actor int64, token valuefingerprint.Token, limit int) ([]RuntimeValueMatch, error) {
	if !valuefingerprint.Valid(token) || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	out := []RuntimeValueMatch{}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		now := time.Now()
		scope := RuntimeEvidenceScope{From: now.Add(-7 * 24 * time.Hour), To: now, ValueFingerprint: &token}
		window, e := readRuntimeEvidenceWindow(ctx, tx, ws, scope, limit)
		if e != nil {
			return e
		}
		for _, entry := range window.Items {
			for _, fingerprint := range entry.Observation.ValueFingerprints {
				if fingerprint.KeyID == token.KeyID && fingerprint.Scope == token.Scope && fingerprint.Version == token.Version && fingerprint.Digest == token.Digest {
					out = append(out, RuntimeValueMatch{SourceID: entry.SourceID, Source: entry.Source, Service: entry.Service, ObservedAt: entry.Observation.ObservedAt, Method: entry.Observation.Method, Route: entry.Observation.Route, SubjectRef: entry.Observation.SubjectRef, ClientIP: entry.Observation.ClientIP, Locator: fingerprint.Locator, Category: fingerprint.Category, Record: fmt.Sprintf("runtime-audit:%d", entry.ID)})
				}
			}
		}
		return nil
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, storeError(e)
}
