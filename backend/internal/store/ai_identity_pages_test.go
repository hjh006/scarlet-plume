package store

import (
	"errors"
	"fmt"
	"testing"
)

func TestIdentityOwnerPagesBeyondThousandWithScopedFilters(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	prefix := fmt.Sprintf("page-identity-%d-", f.ws.ID)
	_, err := f.pool.Exec(ctx, `WITH created AS (INSERT INTO plume.users(username,password_hash) SELECT $2||n,'disabled-test-account' FROM generate_series(1,1003)n RETURNING id,username) INSERT INTO plume.ai_identity_users(workspace_id,user_id,external_id,user_name,display_name,active) SELECT $1,id,username,username,'Synthetic account',id%2=0 FROM created`, f.ws.ID, prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = f.pool.Exec(ctx, `DELETE FROM plume.users WHERE position($1 in username)=1`, prefix) }()
	var group int64
	if err = f.pool.QueryRow(ctx, `INSERT INTO plume.ai_identity_groups(workspace_id,external_id,display_name) VALUES($1,'test-security','Security') RETURNING id`, f.ws.ID).Scan(&group); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `INSERT INTO plume.ai_identity_group_members(workspace_id,group_id,identity_id) SELECT $1,$2,id FROM plume.ai_identity_users WHERE workspace_id=$1 AND active ORDER BY id LIMIT 7`, f.ws.ID, group); err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	cursor := ""
	firstCursor := ""
	for {
		page, err := f.repo.IdentityUserPage(ctx, f.ws.ID, f.user.ID, IdentityUserFilter{}, cursor, 100)
		if err != nil {
			t.Fatal(err)
		}
		if page.AccountCount != 1003 || page.MatchingCount != 1003 {
			t.Fatalf("counts: %+v", page)
		}
		for _, user := range page.Items {
			if seen[user.ID] {
				t.Fatalf("duplicate %d", user.ID)
			}
			seen[user.ID] = true
		}
		if firstCursor == "" {
			firstCursor = page.NextCursor
		}
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" {
			t.Fatal("missing continuation")
		}
		cursor = page.NextCursor
	}
	if len(seen) != 1003 {
		t.Fatalf("account cutoff: %d", len(seen))
	}
	filtered, err := f.repo.IdentityUserPage(ctx, f.ws.ID, f.user.ID, IdentityUserFilter{Query: prefix, Active: "true", Group: "test-security"}, "", 50)
	if err != nil || len(filtered.Items) != 7 || filtered.MatchingCount != 7 || filtered.AccountCount != 1003 {
		t.Fatalf("filters: %+v %v", filtered, err)
	}
	for _, user := range filtered.Items {
		if !user.Active || len(user.Groups) != 1 || user.Groups[0] != "test-security" {
			t.Fatalf("wrong group: %+v", user)
		}
	}
	other := assessmentSourceTestStore(t)
	if _, err = f.repo.IdentityUserPage(ctx, f.ws.ID, other.user.ID, IdentityUserFilter{}, "", 50); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nonowner read: %v", err)
	}
	if _, err = f.pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role)VALUES($1,$2,'reviewer')`, f.ws.ID, other.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.IdentityUserPage(ctx, f.ws.ID, other.user.ID, IdentityUserFilter{}, "", 50); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reviewer read: %v", err)
	}
	for _, tc := range []struct {
		ws     int64
		filter IdentityUserFilter
		cursor string
	}{
		{other.ws.ID, IdentityUserFilter{}, firstCursor},
		{f.ws.ID, IdentityUserFilter{Active: "false"}, firstCursor},
		{f.ws.ID, IdentityUserFilter{Active: "sometimes"}, ""},
	} {
		if _, err := f.repo.IdentityUserPage(ctx, tc.ws, f.user.ID, tc.filter, tc.cursor, 50); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid scope accepted: %v", err)
		}
	}
}
