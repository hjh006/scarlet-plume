package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type ClassificationDeliveryTargetInput struct {
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"`
	Enabled     bool   `json:"enabled"`
	AutoEvents  bool   `json:"autoEvents"`
	MinSeverity string `json:"minSeverity"`
	Revision    int64  `json:"revision"`
}
type ClassificationDeliveryTarget struct {
	ClassificationDeliveryTargetInput
	ID        int64     `json:"id"`
	HasToken  bool      `json:"hasToken"`
	CreatedAt time.Time `json:"createdAt"`
}
type ClassificationDelivery struct {
	ID             int64      `json:"id"`
	PublicationID  *int64     `json:"publicationId,omitempty"`
	TargetID       int64      `json:"targetId"`
	TargetRevision int64      `json:"targetRevision"`
	TargetName     string     `json:"targetName"`
	TargetEndpoint string     `json:"targetEndpoint"`
	ResourceKind   string     `json:"resourceKind"`
	ResourceID     int64      `json:"resourceId"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	PayloadDigest  string     `json:"payloadDigest"`
	RemoteRef      string     `json:"remoteRef"`
	ErrorCode      string     `json:"errorCode"`
	CreatedAt      time.Time  `json:"createdAt"`
	DeliveredAt    *time.Time `json:"deliveredAt,omitempty"`
}
type ClassificationDeliveryClaim struct {
	ClassificationDelivery
	Endpoint   string          `json:"endpoint"`
	LeaseID    string          `json:"leaseId"`
	LeaseUntil time.Time       `json:"leaseUntil"`
	Payload    json.RawMessage `json:"payload"`
}
type ClassificationDeliveryReceipt struct {
	DeliveryID    int64  `json:"deliveryId"`
	LeaseID       string `json:"leaseId"`
	Status        string `json:"status"`
	PayloadDigest string `json:"payloadDigest"`
	RemoteRef     string `json:"remoteRef"`
	ErrorCode     string `json:"errorCode"`
}

const deliveryTargetColumns = "id,name,endpoint,enabled,auto_events,min_severity,revision,created_at"
const deliveryColumns = "d.id,d.target_id,d.target_revision,d.target_name,d.target_endpoint,d.resource_kind,d.resource_id,d.status,d.attempts,d.payload_digest,d.remote_ref,d.error_code,d.created_at,d.delivered_at"

func scanDeliveryTarget(row pgx.Row) (ClassificationDeliveryTarget, error) {
	var d ClassificationDeliveryTarget
	e := row.Scan(&d.ID, &d.Name, &d.Endpoint, &d.Enabled, &d.AutoEvents, &d.MinSeverity, &d.Revision, &d.CreatedAt)
	d.HasToken = true
	return d, e
}
func scanClassificationDelivery(row pgx.Row) (ClassificationDelivery, error) {
	var d ClassificationDelivery
	e := row.Scan(&d.ID, &d.TargetID, &d.TargetRevision, &d.TargetName, &d.TargetEndpoint, &d.ResourceKind, &d.ResourceID, &d.Status, &d.Attempts, &d.PayloadDigest, &d.RemoteRef, &d.ErrorCode, &d.CreatedAt, &d.DeliveredAt)
	if d.ResourceKind == "classification_publication" {
		id := d.ResourceID
		d.PublicationID = &id
	}
	return d, e
}
func validDeliveryEndpoint(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\x00") {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && (strings.EqualFold(u.Hostname(), "localhost") || ip != nil && ip.IsLoopback())
}
func (s *Store) ClassificationDeliveryTargets(ctx context.Context, ws, actor int64) ([]ClassificationDeliveryTarget, error) {
	out := []ClassificationDeliveryTarget{}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, "SELECT "+deliveryTargetColumns+" FROM plume.classification_delivery_targets WHERE workspace_id=$1 ORDER BY id LIMIT 50", ws)
		if e != nil {
			return e
		}
		out, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClassificationDeliveryTarget, error) { return scanDeliveryTarget(row) })
		return e
	})
	return out, storeError(e)
}
func (s *Store) SaveClassificationDeliveryTarget(ctx context.Context, ws, actor, id int64, input ClassificationDeliveryTargetInput, tokenDigest string, rotate bool) (ClassificationDeliveryTarget, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Endpoint = strings.TrimSpace(input.Endpoint)
	if input.MinSeverity == "" {
		input.MinSeverity = "high"
	}
	if id < 0 || (!rotate && (!validCheckText(input.Name, 120) || !validDeliveryEndpoint(input.Endpoint) || !slices.Contains([]string{"low", "medium", "high", "critical"}, input.MinSeverity))) || ((id == 0 || rotate) && !digestPattern.MatchString(tokenDigest)) {
		return ClassificationDeliveryTarget{}, ErrInvalid
	}
	var out ClassificationDeliveryTarget
	e := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if e := classificationAIEditor(ctx, tx, ws, actor, true); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, "SELECT id FROM plume.workspaces WHERE id=$1 FOR UPDATE", ws); e != nil {
			return e
		}
		if id == 0 {
			if input.Revision != 0 || rotate {
				return ErrInvalid
			}
			var count int
			if e := tx.QueryRow(ctx, "SELECT count(*) FROM plume.classification_delivery_targets WHERE workspace_id=$1", ws).Scan(&count); e != nil {
				return e
			}
			if count >= 50 {
				return ErrInvalid
			}
			var e error
			out, e = scanDeliveryTarget(tx.QueryRow(ctx, "INSERT INTO plume.classification_delivery_targets(workspace_id,name,endpoint,enabled,auto_events,min_severity,token_digest,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING "+deliveryTargetColumns, ws, input.Name, input.Endpoint, input.Enabled, input.AutoEvents, input.MinSeverity, tokenDigest, actor))
			return e
		}
		old, e := scanDeliveryTarget(tx.QueryRow(ctx, "SELECT "+deliveryTargetColumns+" FROM plume.classification_delivery_targets WHERE workspace_id=$1 AND id=$2 FOR UPDATE", ws, id))
		if e != nil {
			return e
		}
		if old.Revision != input.Revision {
			return ErrConflict
		}
		if rotate {
			out, e = scanDeliveryTarget(tx.QueryRow(ctx, "UPDATE plume.classification_delivery_targets SET token_digest=$3,revision=revision+1,created_by=$4 WHERE workspace_id=$1 AND id=$2 RETURNING "+deliveryTargetColumns, ws, id, tokenDigest, actor))
		} else {
			out, e = scanDeliveryTarget(tx.QueryRow(ctx, "UPDATE plume.classification_delivery_targets SET name=$3,endpoint=$4,enabled=$5,auto_events=$6,min_severity=$7,revision=revision+1 WHERE workspace_id=$1 AND id=$2 RETURNING "+deliveryTargetColumns, ws, id, input.Name, input.Endpoint, input.Enabled, input.AutoEvents, input.MinSeverity))
		}
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, "UPDATE plume.classification_deliveries SET status='cancelled',error_code='target_changed',lease_id='',lease_until=NULL WHERE workspace_id=$1 AND target_id=$2 AND status IN('queued','delivering')", ws, id)
		return e
	})
	return out, storeError(e)
}
func CanonicalClassificationDelivery(raw []byte) ([]byte, string, error) {
	if len(raw) > 2<<20 {
		return nil, "", ErrInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil || dec.Decode(new(any)) != io.EOF {
		return nil, "", ErrInvalid
	}
	canonical, e := json.Marshal(v)
	if e != nil {
		return nil, "", ErrInvalid
	}
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:]), nil
}
func enqueueClassificationDelivery(ctx context.Context, tx pgx.Tx, ws, actor, targetID int64, kind string, resource int64, snapshot any, digest string) (ClassificationDelivery, error) {
	target, e := scanDeliveryTarget(tx.QueryRow(ctx, "SELECT "+deliveryTargetColumns+" FROM plume.classification_delivery_targets WHERE workspace_id=$1 AND id=$2 FOR UPDATE", ws, targetID))
	if e != nil {
		return ClassificationDelivery{}, e
	}
	if !target.Enabled {
		return ClassificationDelivery{}, ErrConflict
	}
	old, e := scanClassificationDelivery(tx.QueryRow(ctx, "SELECT "+deliveryColumns+" FROM plume.classification_deliveries d JOIN plume.classification_delivery_targets t ON t.workspace_id=d.workspace_id AND t.id=d.target_id WHERE d.workspace_id=$1 AND d.target_id=$2 AND d.target_revision=$3 AND d.resource_kind=$4 AND d.resource_id=$5 FOR UPDATE OF d", ws, targetID, target.Revision, kind, resource))
	if e == nil {
		if old.Status == "failed" {
			if _, e = tx.Exec(ctx, "UPDATE plume.classification_deliveries SET status='queued',error_code='',lease_id='',lease_until=NULL WHERE id=$1", old.ID); e != nil {
				return old, e
			}
			old.Status = "queued"
			old.ErrorCode = ""
		}
		return old, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return ClassificationDelivery{}, e
	}
	var id int64
	if e = tx.QueryRow(ctx, "INSERT INTO plume.classification_deliveries(workspace_id,target_id,target_revision,resource_kind,resource_id,payload_digest,created_by,target_name,target_endpoint) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id", ws, targetID, target.Revision, kind, resource, strings.Repeat("0", 64), actor, target.Name, target.Endpoint).Scan(&id); e != nil {
		return ClassificationDelivery{}, e
	}
	raw, e := json.Marshal(map[string]any{"schemaVersion": "scarlet-evidence-delivery-v1", "deliveryId": id, "resourceKind": kind, "resourceId": resource, "snapshotDigest": digest, "snapshot": snapshot})
	if e != nil {
		return ClassificationDelivery{}, e
	}
	payload, hash, e := CanonicalClassificationDelivery(raw)
	if e != nil {
		return ClassificationDelivery{}, e
	}
	if _, e = tx.Exec(ctx, "UPDATE plume.classification_deliveries SET payload=$2,payload_digest=$3 WHERE id=$1", id, payload, hash); e != nil {
		return ClassificationDelivery{}, e
	}
	return scanClassificationDelivery(tx.QueryRow(ctx, "SELECT "+deliveryColumns+" FROM plume.classification_deliveries d JOIN plume.classification_delivery_targets t ON t.id=d.target_id WHERE d.id=$1", id))
}
func (s *Store) CreateClassificationDelivery(ctx context.Context, ws, actor, publication, target int64) (ClassificationDelivery, error) {
	var out ClassificationDelivery
	e := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if e := classificationAIEditor(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		p, e := readClassificationPublication(ctx, tx, ws, publication)
		if e != nil {
			return e
		}
		if p.Status != "published" {
			return ErrConflict
		}
		out, e = enqueueClassificationDelivery(ctx, tx, ws, actor, target, "classification_publication", p.ID, p, p.SnapshotDigest)
		return e
	})
	return out, storeError(e)
}
func (s *Store) CreateSecurityEventDelivery(ctx context.Context, ws, actor, event, target int64) (ClassificationDelivery, error) {
	var out ClassificationDelivery
	e := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if e := classificationAIEditor(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var snapshot json.RawMessage
		var digest string
		if e := tx.QueryRow(ctx, "SELECT metadata,digest FROM plume.operations_security_events WHERE workspace_id=$1 AND id=$2", ws, event).Scan(&snapshot, &digest); e != nil {
			return e
		}
		var e error
		out, e = enqueueClassificationDelivery(ctx, tx, ws, actor, target, "security_event", event, snapshot, digest)
		return e
	})
	return out, storeError(e)
}
func enqueueSecurityEventDeliveries(ctx context.Context, tx pgx.Tx, ws, actor int64, event OperationsSecurityEvent) error {
	if event.Outcome != "observed" {
		return nil
	}
	rows, e := tx.Query(ctx, "SELECT t.id,t.min_severity FROM plume.classification_delivery_targets t JOIN plume.memberships m ON m.workspace_id=t.workspace_id AND m.user_id=t.created_by WHERE t.workspace_id=$1 AND t.enabled AND t.auto_events AND m.role='owner' ORDER BY t.id", ws)
	if e != nil {
		return e
	}
	type target struct {
		id       int64
		severity string
	}
	targets, e := pgx.CollectRows(rows, func(row pgx.CollectableRow) (target, error) {
		var t target
		e := row.Scan(&t.id, &t.severity)
		return t, e
	})
	if e != nil {
		return e
	}
	ranks := map[string]int{"low": 0, "medium": 1, "high": 2, "critical": 3}
	raw, _ := json.Marshal(event.SecurityEventInput)
	_, digest, e := CanonicalClassificationDelivery(raw)
	if e != nil {
		return e
	}
	for _, t := range targets {
		if ranks[event.Severity] >= ranks[t.severity] {
			if _, e := enqueueClassificationDelivery(ctx, tx, ws, actor, t.id, "security_event", event.ID, event.SecurityEventInput, digest); e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *Store) ClassificationDeliveries(ctx context.Context, ws, actor, publication, target int64) ([]ClassificationDelivery, error) {
	if publication < 0 || target < 0 {
		return nil, ErrInvalid
	}
	out := []ClassificationDelivery{}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, "SELECT "+deliveryColumns+" FROM plume.classification_deliveries d JOIN plume.classification_delivery_targets t ON t.workspace_id=d.workspace_id AND t.id=d.target_id WHERE d.workspace_id=$1 AND ($2::bigint=0 OR d.resource_kind='classification_publication' AND d.resource_id=$2) AND ($3::bigint=0 OR d.target_id=$3) ORDER BY d.id DESC LIMIT 200", ws, publication, target)
		if e != nil {
			return e
		}
		out, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClassificationDelivery, error) { return scanClassificationDelivery(row) })
		return e
	})
	return out, storeError(e)
}
func classificationDeliveryActor(ctx context.Context, tx pgx.Tx, digest string) (ClassificationDeliveryTarget, int64, error) {
	var ws int64
	var id int64
	e := tx.QueryRow(ctx, "SELECT t.id,t.workspace_id FROM plume.classification_delivery_targets t JOIN plume.memberships m ON m.workspace_id=t.workspace_id AND m.user_id=t.created_by WHERE t.token_digest=$1 AND t.enabled AND m.role='owner' FOR SHARE OF t,m", digest).Scan(&id, &ws)
	if errors.Is(e, pgx.ErrNoRows) {
		return ClassificationDeliveryTarget{}, 0, ErrAutomationUnauthorized
	}
	if e != nil {
		return ClassificationDeliveryTarget{}, 0, e
	}
	target, e := scanDeliveryTarget(tx.QueryRow(ctx, "SELECT "+deliveryTargetColumns+" FROM plume.classification_delivery_targets WHERE id=$1", id))
	return target, ws, e
}
func (s *Store) ClaimClassificationDelivery(ctx context.Context, digest, leaseID string) (*ClassificationDeliveryClaim, error) {
	if !digestPattern.MatchString(digest) || !operationsKey.MatchString(leaseID) || len(leaseID) < 16 {
		return nil, ErrInvalid
	}
	var out *ClassificationDeliveryClaim
	e := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		target, ws, e := classificationDeliveryActor(ctx, tx, digest)
		if e != nil {
			return e
		}
		var id int64
		e = tx.QueryRow(ctx, "SELECT d.id FROM plume.classification_deliveries d JOIN plume.memberships m ON m.workspace_id=d.workspace_id AND m.user_id=d.created_by WHERE d.workspace_id=$1 AND d.target_id=$2 AND d.target_revision=$3 AND m.role IN('owner','operator') AND (d.status='queued' OR d.status='delivering' AND d.lease_until<=clock_timestamp()) ORDER BY d.id FOR UPDATE OF d SKIP LOCKED LIMIT 1", ws, target.ID, target.Revision).Scan(&id)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		var until time.Time
		if e = tx.QueryRow(ctx, "UPDATE plume.classification_deliveries SET status='delivering',attempts=attempts+1,lease_id=$2,lease_until=clock_timestamp()+interval '2 minutes' WHERE id=$1 RETURNING lease_until", id, leaseID).Scan(&until); e != nil {
			return e
		}
		d, e := scanClassificationDelivery(tx.QueryRow(ctx, "SELECT "+deliveryColumns+" FROM plume.classification_deliveries d JOIN plume.classification_delivery_targets t ON t.id=d.target_id WHERE d.id=$1", id))
		if e != nil {
			return e
		}
		var raw json.RawMessage
		if e = tx.QueryRow(ctx, "SELECT payload FROM plume.classification_deliveries WHERE id=$1", id).Scan(&raw); e != nil {
			return e
		}
		canonical, hash, e := CanonicalClassificationDelivery(raw)
		if e != nil || hash != d.PayloadDigest {
			return ErrConflict
		}
		if d.TargetEndpoint != target.Endpoint {
			return ErrConflict
		}
		out = &ClassificationDeliveryClaim{ClassificationDelivery: d, Endpoint: d.TargetEndpoint, LeaseID: leaseID, LeaseUntil: until, Payload: canonical}
		return nil
	})
	return out, storeError(e)
}
func (s *Store) ReceiveClassificationDelivery(ctx context.Context, digest string, input ClassificationDeliveryReceipt) (ClassificationDelivery, error) {
	if !digestPattern.MatchString(digest) || input.DeliveryID < 1 || !operationsKey.MatchString(input.LeaseID) || !digestPattern.MatchString(input.PayloadDigest) || !slices.Contains([]string{"accepted", "failed"}, input.Status) || len(input.RemoteRef) > 200 || strings.ContainsAny(input.RemoteRef, "\r\n\x00") || !slices.Contains([]string{"", "transport", "response", "acknowledgement", "configuration"}, input.ErrorCode) || input.Status == "accepted" && input.ErrorCode != "" {
		return ClassificationDelivery{}, ErrInvalid
	}
	var out ClassificationDelivery
	e := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		target, ws, e := classificationDeliveryActor(ctx, tx, digest)
		if e != nil {
			return e
		}
		var lease string
		var current string
		var until *time.Time
		var hash, remote, code string
		var rev int64
		e = tx.QueryRow(ctx, "SELECT status,lease_id,lease_until,payload_digest,remote_ref,error_code,target_revision FROM plume.classification_deliveries WHERE workspace_id=$1 AND target_id=$2 AND id=$3 FOR UPDATE", ws, target.ID, input.DeliveryID).Scan(&current, &lease, &until, &hash, &remote, &code, &rev)
		if e != nil {
			return e
		}
		if hash != input.PayloadDigest || lease != input.LeaseID || rev != target.Revision {
			return ErrConflict
		}
		if current == "accepted" || current == "failed" {
			if current != input.Status || remote != input.RemoteRef || code != input.ErrorCode {
				return ErrConflict
			}
		} else {
			if current != "delivering" || until == nil {
				return ErrConflict
			}
			var fresh bool
			if e = tx.QueryRow(ctx, "SELECT $1::timestamptz>clock_timestamp()", until).Scan(&fresh); e != nil {
				return e
			}
			if !fresh {
				return ErrConflict
			}
			if _, e = tx.Exec(ctx, "UPDATE plume.classification_deliveries SET status=$2,remote_ref=$3,error_code=$4,delivered_at=CASE WHEN $2='accepted' THEN clock_timestamp() END WHERE id=$1", input.DeliveryID, input.Status, input.RemoteRef, input.ErrorCode); e != nil {
				return e
			}
		}
		out, e = scanClassificationDelivery(tx.QueryRow(ctx, "SELECT "+deliveryColumns+" FROM plume.classification_deliveries d JOIN plume.classification_delivery_targets t ON t.id=d.target_id WHERE d.id=$1", input.DeliveryID))
		return e
	})
	return out, storeError(e)
}
