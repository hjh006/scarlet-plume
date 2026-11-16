package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const securityNetworkBindingColumns = `id,revision,configuration,updated_at`
const securityNetworkChangeColumns = `id,binding_id,binding_revision,proposal,status,created_by,created_at,reviewed_by,review_note,reviewed_at`

const SecurityNetworkBindingPageCoverage = "按关联ID倒序读取当前登记及修订，各页采用读取时事实，不构成跨页快照；登记关联不表示线路、设备或实际数据路径已观测。"
const SecurityNetworkChangePageCoverage = "按变更ID倒序读取冻结提案与当前复核状态；复核状态变化可能改变后续筛选结果，接受提案不表示线路或防火墙已经执行变更。"
const SecurityPlatformSnapshotPageCoverage = "按快照ID倒序遍历保存的快照摘要；时间下界包含、上界不包含，原始快照保留采集时的覆盖范围和预览限制，不代表当前控制有效。"

type SecurityNetworkBindingPage struct {
	Items       []SecurityNetworkBinding `json:"items"`
	Limit       int                      `json:"limit"`
	HasMore     bool                     `json:"hasMore"`
	NextCursor  string                   `json:"nextCursor"`
	GeneratedAt time.Time                `json:"generatedAt"`
	Coverage    string                   `json:"coverage"`
}

type SecurityNetworkChangeFilter struct {
	BindingID int64  `json:"bindingId"`
	Status    string `json:"status"`
}

type SecurityNetworkChangeItem struct {
	SecurityNetworkChange
	SubmittedByCurrentUser bool `json:"submittedByCurrentUser"`
}

type SecurityNetworkChangePage struct {
	Items       []SecurityNetworkChangeItem `json:"items"`
	Limit       int                         `json:"limit"`
	HasMore     bool                        `json:"hasMore"`
	NextCursor  string                      `json:"nextCursor"`
	GeneratedAt time.Time                   `json:"generatedAt"`
	Coverage    string                      `json:"coverage"`
}

type SecurityPlatformSnapshotFilter struct {
	CreatedFrom *time.Time `json:"createdFrom"`
	CreatedTo   *time.Time `json:"createdTo"`
}

type SecurityPlatformSnapshotPage struct {
	Items       []SecurityPlatformSnapshot `json:"items"`
	Limit       int                        `json:"limit"`
	HasMore     bool                       `json:"hasMore"`
	NextCursor  string                     `json:"nextCursor"`
	GeneratedAt time.Time                  `json:"generatedAt"`
	Coverage    string                     `json:"coverage"`
}

type SecurityPlatformPreview struct {
	Limit    int    `json:"limit"`
	Returned int    `json:"returned"`
	HasMore  bool   `json:"hasMore"`
	Order    string `json:"order"`
	ListPath string `json:"listPath"`
	Coverage string `json:"coverage"`
}

func scanSecurityNetworkBinding(row pgx.Row) (out SecurityNetworkBinding, err error) {
	var id int64
	var raw json.RawMessage
	err = row.Scan(&id, &out.Revision, &raw, &out.UpdatedAt)
	if err == nil {
		err = json.Unmarshal(raw, &out.SecurityNetworkBindingInput)
		out.ID, out.ExpectedRevision, out.EvidenceType = id, 0, "declared"
	}
	return out, storeError(err)
}

func (s *Store) SecurityNetworkBinding(ctx context.Context, ws, actor, id int64) (out SecurityNetworkBinding, err error) {
	if id < 1 {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if e := securityPlatformMember(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var e error
		out, e = scanSecurityNetworkBinding(tx.QueryRow(ctx, `SELECT `+securityNetworkBindingColumns+` FROM plume.security_network_bindings WHERE workspace_id=$1 AND id=$2`, ws, id))
		return e
	})
	return out, storeError(err)
}

func (s *Store) SecurityNetworkBindings(ctx context.Context, ws, actor int64, query, cursor string, limit int) (out SecurityNetworkBindingPage, err error) {
	out = SecurityNetworkBindingPage{Items: []SecurityNetworkBinding{}, Limit: limit, GeneratedAt: time.Now().UTC(), Coverage: SecurityNetworkBindingPageCoverage}
	query = strings.TrimSpace(query)
	if !utf8.ValidString(query) || !securityText(query, 120, false) || limit < 1 || limit > 200 {
		return out, ErrInvalid
	}
	scope := aiOperationsScope("security-network-bindings-v1", ws, query)
	before, err := aiOperationsBefore(cursor, scope)
	if err != nil {
		return out, err
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if e := securityPlatformMember(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		// position treats '%' and '_' as literal search text, not SQL wildcards.
		rows, e := tx.Query(ctx, `SELECT `+securityNetworkBindingColumns+` FROM plume.security_network_bindings WHERE workspace_id=$1 AND ($2::bigint=0 OR id<$2) AND ($3::text='' OR position(lower($3) in lower(concat_ws(' ',configuration->>'site',configuration->>'circuit',configuration->>'vrf',configuration->>'applicationRef',configuration->>'region',configuration->>'receiver',configuration->>'purpose',configuration->>'evidenceRef')))>0) ORDER BY id DESC LIMIT $4`, ws, before, query, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			if len(out.Items) == limit {
				out.HasMore = true
				break
			}
			item, e := scanSecurityNetworkBinding(rows)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, item)
		}
		return rows.Err()
	})
	if err == nil && out.HasMore {
		out.NextCursor = aiOperationsNext(out.Items[len(out.Items)-1].ID, scope)
	}
	return out, storeError(err)
}

func (s *Store) SecurityNetworkChanges(ctx context.Context, ws, actor int64, filter SecurityNetworkChangeFilter, cursor string, limit int) (out SecurityNetworkChangePage, err error) {
	out = SecurityNetworkChangePage{Items: []SecurityNetworkChangeItem{}, Limit: limit, GeneratedAt: time.Now().UTC(), Coverage: SecurityNetworkChangePageCoverage}
	if filter.BindingID < 0 || !slices.Contains([]string{"", "pending_review", "accepted", "rejected"}, filter.Status) || limit < 1 || limit > 200 {
		return out, ErrInvalid
	}
	scope := aiOperationsScope("security-network-changes-v1", ws, filter)
	before, err := aiOperationsBefore(cursor, scope)
	if err != nil {
		return out, err
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if e := securityPlatformMember(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT `+securityNetworkChangeColumns+` FROM plume.security_network_changes WHERE workspace_id=$1 AND ($2::bigint=0 OR id<$2) AND ($3::bigint=0 OR binding_id=$3) AND ($4::text='' OR status=$4) ORDER BY id DESC LIMIT $5`, ws, before, filter.BindingID, filter.Status, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			if len(out.Items) == limit {
				out.HasMore = true
				break
			}
			item, e := scanSecurityNetworkChange(rows)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, SecurityNetworkChangeItem{SecurityNetworkChange: item, SubmittedByCurrentUser: item.CreatedBy == actor})
		}
		return rows.Err()
	})
	if err == nil && out.HasMore {
		out.NextCursor = aiOperationsNext(out.Items[len(out.Items)-1].ID, scope)
	}
	return out, storeError(err)
}

func (s *Store) SecurityPlatformSnapshots(ctx context.Context, ws, actor int64, filter SecurityPlatformSnapshotFilter, cursor string, limit int) (out SecurityPlatformSnapshotPage, err error) {
	out = SecurityPlatformSnapshotPage{Items: []SecurityPlatformSnapshot{}, Limit: limit, GeneratedAt: time.Now().UTC(), Coverage: SecurityPlatformSnapshotPageCoverage}
	if limit < 1 || limit > 200 {
		return out, ErrInvalid
	}
	if filter.CreatedFrom != nil {
		v := filter.CreatedFrom.UTC()
		if v.IsZero() {
			return out, ErrInvalid
		}
		filter.CreatedFrom = &v
	}
	if filter.CreatedTo != nil {
		v := filter.CreatedTo.UTC()
		if v.IsZero() {
			return out, ErrInvalid
		}
		filter.CreatedTo = &v
	}
	if filter.CreatedFrom != nil && filter.CreatedTo != nil && !filter.CreatedFrom.Before(*filter.CreatedTo) {
		return out, ErrInvalid
	}
	scope := aiOperationsScope("security-platform-snapshots-v1", ws, filter)
	before, err := aiOperationsBefore(cursor, scope)
	if err != nil {
		return out, err
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if e := securityPlatformMember(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT id,digest,created_at,jsonb_array_length(snapshot->'findings') FROM plume.security_platform_snapshots WHERE workspace_id=$1 AND ($2::bigint=0 OR id<$2) AND ($3::timestamptz IS NULL OR created_at>=$3) AND ($4::timestamptz IS NULL OR created_at<$4) ORDER BY id DESC LIMIT $5`, ws, before, filter.CreatedFrom, filter.CreatedTo, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			if len(out.Items) == limit {
				out.HasMore = true
				break
			}
			var item SecurityPlatformSnapshot
			if e = rows.Scan(&item.ID, &item.Digest, &item.CreatedAt, &item.FindingsCount); e != nil {
				return e
			}
			out.Items = append(out.Items, item)
		}
		return rows.Err()
	})
	if err == nil && out.HasMore {
		out.NextCursor = aiOperationsNext(out.Items[len(out.Items)-1].ID, scope)
	}
	return out, storeError(err)
}
