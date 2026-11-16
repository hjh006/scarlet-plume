package store

import (
	"errors"
	"fmt"
	"testing"
)

func TestSecurityPosturePagesTraverseBeyondOverviewAndFreezeAssetRange(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	_, err := f.pool.Exec(ctx, `INSERT INTO plume.assets(workspace_id,name,system,owner_team) SELECT $1,'Scope asset '||n,'Synthetic posture','Security' FROM generate_series(1,263) n`, f.ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	filter := SecurityPostureFilter{Query: "Scope asset", Code: "classification_review"}
	page, err := f.repo.SecurityPosturePage(ctx, f.ws.ID, f.user.ID, filter, "", 100)
	if err != nil || len(page.Assets) != 100 || !page.HasMore || page.MatchingAssets != 263 {
		t.Fatalf("first page: %+v %v", page, err)
	}
	firstCursor := page.NextCursor
	late, err := f.repo.CreateAsset(ctx, f.ws.ID, "Scope asset late", "Synthetic posture", "Security")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for {
		if page.MatchingAssets != 263 {
			t.Fatalf("new asset entered fixed upper bound: %d", page.MatchingAssets)
		}
		for _, asset := range page.Assets {
			if seen[asset.ID] || asset.ID == late.ID {
				t.Fatalf("duplicate or new asset %d", asset.ID)
			}
			seen[asset.ID] = true
		}
		if len(page.Items) != len(page.Assets) {
			t.Fatalf("classification gaps omitted: %d / %d", len(page.Items), len(page.Assets))
		}
		if !page.HasMore {
			break
		}
		page, err = f.repo.SecurityPosturePage(ctx, f.ws.ID, f.user.ID, filter, page.NextCursor, 100)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 263 {
		t.Fatalf("older scope inaccessible: %d", len(seen))
	}
	other := assessmentSourceTestStore(t)
	for _, tc := range []struct {
		ws     int64
		filter SecurityPostureFilter
		cursor string
	}{
		{other.ws.ID, filter, firstCursor},
		{f.ws.ID, SecurityPostureFilter{Query: "changed"}, firstCursor},
		{f.ws.ID, filter, "malformed"},
	} {
		if _, err := f.repo.SecurityPosturePage(ctx, tc.ws, f.user.ID, tc.filter, tc.cursor, 100); !errors.Is(err, ErrInvalid) {
			t.Fatalf("scope/cursor accepted: %v", err)
		}
	}
	if _, err := f.repo.SecurityPosturePage(ctx, f.ws.ID, other.user.ID, filter, "", 100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nonmember read: %v", err)
	}
	// A filtered page may have no findings while more admitted assets remain.
	empty, err := f.repo.SecurityPosturePage(ctx, f.ws.ID, f.user.ID, SecurityPostureFilter{Severity: "high"}, "", 1)
	if err != nil || len(empty.Items) != 0 || len(empty.Assets) != 1 || !empty.HasMore {
		t.Fatalf("empty filtered page hid continuation: %+v %v", empty, err)
	}
	for _, limit := range []int{0, 101} {
		if _, err := f.repo.SecurityPosturePage(ctx, f.ws.ID, f.user.ID, filter, "", limit); !errors.Is(err, ErrInvalid) {
			t.Fatal(fmt.Sprintf("invalid limit %d: %v", limit, err))
		}
	}
}
