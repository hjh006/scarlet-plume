package store

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

type IdentityUserFilter struct {
	Query  string `json:"q"`
	Active string `json:"active"`
	Group  string `json:"group"`
}

type IdentityUserPage struct {
	Items         []IdentityUser `json:"items"`
	Limit         int            `json:"limit"`
	Truncated     bool           `json:"truncated"`
	HasMore       bool           `json:"hasMore"`
	NextCursor    string         `json:"nextCursor,omitempty"`
	AccountCount  int64          `json:"accountCount"`
	MatchingCount int64          `json:"matchingCount"`
}

func (s *Store) IdentityUserPage(ctx context.Context, ws, actor int64, filter IdentityUserFilter, cursor string, limit int) (IdentityUserPage, error) {
	out := IdentityUserPage{Items: []IdentityUser{}, Limit: limit}
	filter.Query = strings.TrimSpace(filter.Query)
	filter.Group = strings.TrimSpace(filter.Group)
	if !securityText(filter.Query, 120, false) || !securityText(filter.Group, 255, false) || (filter.Active != "" && filter.Active != "true" && filter.Active != "false") || limit < 1 || limit > 1000 {
		return out, ErrInvalid
	}
	scope := aiOperationsScope("identity-users-v1", ws, filter)
	before, err := aiOperationsBefore(cursor, scope)
	if err != nil {
		return out, err
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if err := identityOwner(ctx, tx, ws, actor); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM plume.ai_identity_users WHERE workspace_id=$1`, ws).Scan(&out.AccountCount); err != nil {
			return err
		}
		const where = `i.workspace_id=$1 AND ($2='' OR position(lower($2) in lower(i.external_id||' '||i.user_name||' '||i.display_name))>0) AND ($3='' OR i.active=($3='true')) AND ($4='' OR EXISTS(SELECT 1 FROM plume.ai_identity_group_members gm JOIN plume.ai_identity_groups g ON g.id=gm.group_id AND g.workspace_id=gm.workspace_id WHERE gm.workspace_id=i.workspace_id AND gm.identity_id=i.id AND g.external_id=$4))`
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM plume.ai_identity_users i WHERE `+where, ws, filter.Query, filter.Active, filter.Group).Scan(&out.MatchingCount); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT i.id,i.external_id,i.user_name,i.display_name,i.active,coalesce(m.role,''),i.revision,i.updated_at,
		coalesce((SELECT array_agg(g.external_id ORDER BY g.external_id) FROM plume.ai_identity_group_members gm JOIN plume.ai_identity_groups g ON g.id=gm.group_id AND g.workspace_id=gm.workspace_id WHERE gm.workspace_id=i.workspace_id AND gm.identity_id=i.id),ARRAY[]::text[])
		FROM plume.ai_identity_users i LEFT JOIN plume.memberships m ON m.workspace_id=i.workspace_id AND m.user_id=i.user_id
		WHERE `+where+` AND ($5::bigint=0 OR i.id<$5) ORDER BY i.id DESC LIMIT $6`, ws, filter.Query, filter.Active, filter.Group, before, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var u IdentityUser
			if err := rows.Scan(&u.ID, &u.ExternalID, &u.UserName, &u.DisplayName, &u.Active, &u.Role, &u.Revision, &u.UpdatedAt, &u.Groups); err != nil {
				return err
			}
			out.Items = append(out.Items, u)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out.HasMore = len(out.Items) > limit
		out.Truncated = out.HasMore
		if out.HasMore {
			out.Items = out.Items[:limit]
			out.NextCursor = aiOperationsNext(out.Items[len(out.Items)-1].ID, scope)
		}
		return nil
	})
	return out, storeError(err)
}
