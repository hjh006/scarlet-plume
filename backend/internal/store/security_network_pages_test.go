package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSecurityNetworkPagesReachBeyondOverviewAndReadCurrentBinding(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	if _, err := f.pool.Exec(ctx, `INSERT INTO plume.security_network_bindings(workspace_id,actor_id,configuration) SELECT $1,$2,jsonb_build_object('site','Branch-'||n,'circuit','L-'||n,'vrf','main','applicationRef','approved-app','assetIds','[]'::jsonb,'activityIds','[]'::jsonb,'region','CN','receiver','HQ','purpose','Approved synthetic scope','evidenceRef','synthetic-reference') FROM generate_series(1,255) n`, f.ws.ID, f.user.ID); err != nil {
		t.Fatal(err)
	}
	first, err := f.repo.SecurityNetworkBindings(ctx, f.ws.ID, f.user.ID, "", "", 50)
	if err != nil || len(first.Items) != 50 || !first.HasMore || first.NextCursor == "" || first.GeneratedAt.IsZero() || first.Coverage == "" {
		t.Fatal("binding page", first, err)
	}
	target := first.Items[0]
	change, err := f.repo.CreateSecurityNetworkChange(ctx, f.ws.ID, f.user.ID, SecurityNetworkChangeInput{BindingID: target.ID, ExpectedRevision: 1, ProposedRegion: "SG", ProposedApplicationRef: "approved-app-v2", Reason: "Synthetic regional review"})
	if err != nil {
		t.Fatal(err)
	}
	proposal, _ := json.Marshal(change)
	if _, err = f.pool.Exec(ctx, `INSERT INTO plume.security_network_changes(workspace_id,binding_id,binding_revision,proposal,created_by) SELECT $1,$2,1,$3,$4 FROM generate_series(1,102)`, f.ws.ID, target.ID, proposal, f.user.ID); err != nil {
		t.Fatal(err)
	}
	in := target.SecurityNetworkBindingInput
	in.ExpectedRevision, in.Site, in.Region = 1, "Tail-target%_branch", "US"
	updated, err := f.repo.SaveSecurityNetworkBinding(ctx, f.ws.ID, f.user.ID, in)
	if err != nil || updated.Revision != 2 {
		t.Fatal(updated, err)
	}
	current, err := f.repo.SecurityNetworkBinding(ctx, f.ws.ID, f.user.ID, target.ID)
	if err != nil || current.ID != target.ID || current.Revision != 2 || current.Region != "US" || current.EvidenceType != "declared" || current.ExpectedRevision != 0 {
		t.Fatal("current binding detail", current, err)
	}
	body := []byte(`{"findings":[],"coverage":["Synthetic frozen scope"]}`)
	digest := assessmentDigest(scenarioCanonicalJSON(body))
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if _, err = f.pool.Exec(ctx, `INSERT INTO plume.security_platform_snapshots(workspace_id,actor_id,digest,snapshot,created_at) SELECT $1,$2,$3,$4,$5::timestamptz+n*interval '1 day' FROM generate_series(0,32) n`, f.ws.ID, f.user.ID, digest, body, start); err != nil {
		t.Fatal(err)
	}
	preview, err := f.repo.SecurityPlatform(ctx, f.ws.ID, f.user.ID)
	if err != nil || len(preview.NetworkBindings) != 250 || len(preview.NetworkChanges) != 100 || len(preview.Snapshots) != 30 || !preview.Truncated {
		t.Fatal("overview compatibility", err)
	}
	if slices.ContainsFunc(preview.NetworkBindings, func(b SecurityNetworkBinding) bool { return b.ID == target.ID }) {
		t.Fatal("fixture did not place target beyond overview")
	}
	for _, entry := range []struct {
		key, order string
		limit      int
	}{{"networkBindings", "id_asc", 250}, {"networkChanges", "id_desc", 100}, {"snapshots", "id_desc", 30}} {
		v := preview.PreviewCoverage[entry.key]
		if v.Limit != entry.limit || v.Returned != entry.limit || !v.HasMore || v.Order != entry.order || v.ListPath == "" || v.Coverage == "" {
			t.Fatal("missing collection preview scope", entry.key, v)
		}
	}
	for _, collection := range []string{"bindings", "changes", "snapshots"} {
		cursor := ""
		seen := map[int64]bool{}
		var previous int64
		for {
			ids := []int64{}
			next := ""
			more := false
			switch collection {
			case "bindings":
				p, e := f.repo.SecurityNetworkBindings(ctx, f.ws.ID, f.user.ID, "", cursor, 50)
				err = e
				next = p.NextCursor
				more = p.HasMore
				for _, v := range p.Items {
					ids = append(ids, v.ID)
				}
			case "changes":
				p, e := f.repo.SecurityNetworkChanges(ctx, f.ws.ID, f.user.ID, SecurityNetworkChangeFilter{}, cursor, 50)
				err = e
				next = p.NextCursor
				more = p.HasMore
				for _, v := range p.Items {
					ids = append(ids, v.ID)
					if v.Binding.Region != "CN" || v.BindingRevision != 1 || v.Binding.ID != target.ID || v.Status != "pending_review" {
						t.Fatal("frozen proposal replaced with live binding", v)
					}
				}
			case "snapshots":
				p, e := f.repo.SecurityPlatformSnapshots(ctx, f.ws.ID, f.user.ID, SecurityPlatformSnapshotFilter{}, cursor, 10)
				err = e
				next = p.NextCursor
				more = p.HasMore
				for _, v := range p.Items {
					ids = append(ids, v.ID)
					if v.Digest != digest {
						t.Fatal("snapshot digest changed")
					}
				}
			}
			if err != nil {
				t.Fatal(collection, err)
			}
			for _, id := range ids {
				if seen[id] || previous > 0 && id >= previous {
					t.Fatal("unstable keyset", collection, id)
				}
				seen[id] = true
				previous = id
			}
			if !more {
				if next != "" {
					t.Fatal("terminal cursor")
				}
				break
			}
			cursor = next
		}
		if expected := map[string]int{"bindings": 255, "changes": 103, "snapshots": 33}[collection]; len(seen) != expected {
			t.Fatal("history lost beyond preview", collection, len(seen))
		}
	}
	for _, q := range []string{"%", "_", "tail-TARGET"} {
		p, e := f.repo.SecurityNetworkBindings(ctx, f.ws.ID, f.user.ID, q, "", 50)
		if e != nil || len(p.Items) != 1 || p.Items[0].ID != target.ID {
			t.Fatal("literal case-insensitive binding search", q, p, e)
		}
	}
	changes, err := f.repo.SecurityNetworkChanges(ctx, f.ws.ID, f.user.ID, SecurityNetworkChangeFilter{BindingID: target.ID, Status: "pending_review"}, "", 50)
	if err != nil || len(changes.Items) != 50 || !changes.HasMore {
		t.Fatal("change filters", err)
	}
	accepted, err := f.repo.SecurityNetworkChanges(ctx, f.ws.ID, f.user.ID, SecurityNetworkChangeFilter{BindingID: target.ID, Status: "accepted"}, "", 50)
	if err != nil || len(accepted.Items) != 0 || accepted.HasMore {
		t.Fatal("status filter ignored", err)
	}
	from, to := start.AddDate(0, 0, 10), start.AddDate(0, 0, 20)
	filter := SecurityPlatformSnapshotFilter{CreatedFrom: &from, CreatedTo: &to}
	snapshots, err := f.repo.SecurityPlatformSnapshots(ctx, f.ws.ID, f.user.ID, filter, "", 5)
	if err != nil || len(snapshots.Items) != 5 || !snapshots.HasMore {
		t.Fatal("snapshot range", err)
	}
	for _, v := range snapshots.Items {
		if v.CreatedAt.Before(from) || !v.CreatedAt.Before(to) {
			t.Fatal("incorrect half-open range", v.CreatedAt)
		}
	}
	offsetFrom, offsetTo := from.In(time.FixedZone("offset", 8*3600)), to.In(time.FixedZone("offset", 8*3600))
	last, err := f.repo.SecurityPlatformSnapshots(ctx, f.ws.ID, f.user.ID, SecurityPlatformSnapshotFilter{CreatedFrom: &offsetFrom, CreatedTo: &offsetTo}, snapshots.NextCursor, 10)
	if err != nil || len(last.Items) != 5 || last.HasMore || !last.Items[len(last.Items)-1].CreatedAt.Equal(from) {
		t.Fatal("range normalization/boundary", last, err)
	}
	oldRaw, err := f.repo.SecurityPlatformSnapshot(ctx, f.ws.ID, f.user.ID, last.Items[0].ID)
	if err != nil || assessmentDigest(scenarioCanonicalJSON(oldRaw)) != digest {
		t.Fatal("old frozen snapshot rewritten", err)
	}
	captured, err := f.repo.CaptureSecurityPlatform(ctx, f.ws.ID, f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.repo.SecurityPlatformSnapshot(ctx, f.ws.ID, f.user.ID, captured.ID)
	if err != nil || assessmentDigest(scenarioCanonicalJSON(raw)) != captured.Digest {
		t.Fatal("new snapshot digest", err)
	}
	var frozen SecurityPlatform
	if json.Unmarshal(raw, &frozen) != nil || len(frozen.Snapshots) != 0 || frozen.PreviewCoverage["snapshots"].Order != "excluded" || frozen.PreviewCoverage["snapshots"].Returned != 0 {
		t.Fatal("snapshot implies nested historical coverage")
	}
}

func TestSecurityNetworkPageCursorFiltersMembershipAndTenantIsolation(t *testing.T) {
	f := assessmentSourceTestStore(t)
	other := assessmentSourceTestStore(t)
	ctx := t.Context()
	in := SecurityNetworkBindingInput{Site: "Synthetic branch", Circuit: "Synthetic circuit", ApplicationRef: "app", AssetIDs: []int64{}, ActivityIDs: []int64{}, Region: "CN", Purpose: "Approved synthetic scope", EvidenceRef: "synthetic-source"}
	var binding SecurityNetworkBinding
	for range 3 {
		var err error
		binding, err = f.repo.SaveSecurityNetworkBinding(ctx, f.ws.ID, f.user.ID, in)
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err := f.repo.SecurityNetworkBindings(ctx, f.ws.ID, f.user.ID, "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct {
		ws, actor int64
		q         string
	}{{other.ws.ID, other.user.ID, ""}, {f.ws.ID, f.user.ID, "changed"}} {
		if _, err = f.repo.SecurityNetworkBindings(ctx, scope.ws, scope.actor, scope.q, p.NextCursor, 1); !errors.Is(err, ErrInvalid) {
			t.Fatal("cross-scope binding cursor", err)
		}
	}
	if _, err = f.repo.SecurityNetworkBinding(ctx, other.ws.ID, other.user.ID, binding.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-tenant binding detail", err)
	}
	if _, err = f.repo.SecurityNetworkBindings(ctx, f.ws.ID, other.user.ID, "", "", 1); !errors.Is(err, ErrNotFound) {
		t.Fatal("nonmember read", err)
	}
	if _, err = f.repo.SecurityNetworkChanges(ctx, f.ws.ID, f.user.ID, SecurityNetworkChangeFilter{}, p.NextCursor, 1); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross-collection cursor", err)
	}
	for range 3 {
		if _, err = f.repo.CreateSecurityNetworkChange(ctx, f.ws.ID, f.user.ID, SecurityNetworkChangeInput{BindingID: binding.ID, ExpectedRevision: 1, ProposedRegion: "SG", ProposedApplicationRef: "app-v2", Reason: "Synthetic review"}); err != nil {
			t.Fatal(err)
		}
	}
	changes, err := f.repo.SecurityNetworkChanges(ctx, f.ws.ID, f.user.ID, SecurityNetworkChangeFilter{BindingID: binding.ID, Status: "pending_review"}, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Items) != 1 || !changes.Items[0].SubmittedByCurrentUser {
		t.Fatal("submitter self-review marker missing")
	}
	if _, err = f.pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role) VALUES($1,$2,'reviewer')`, f.ws.ID, other.user.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM plume.memberships WHERE workspace_id=$1 AND user_id=$2`, f.ws.ID, other.user.ID)
	})
	reviewedPage, err := f.repo.SecurityNetworkChanges(ctx, f.ws.ID, other.user.ID, SecurityNetworkChangeFilter{}, "", 1)
	if err != nil || len(reviewedPage.Items) != 1 || reviewedPage.Items[0].SubmittedByCurrentUser {
		t.Fatal("independent reviewer marked as submitter", err)
	}
	var projectedInStorage bool
	if err = f.pool.QueryRow(ctx, `SELECT bool_or(proposal ? 'submittedByCurrentUser') FROM plume.security_network_changes WHERE workspace_id=$1`, f.ws.ID).Scan(&projectedInStorage); err != nil || projectedInStorage {
		t.Fatal("session projection persisted in frozen proposals", err)
	}
	for _, filter := range []SecurityNetworkChangeFilter{{BindingID: binding.ID, Status: "accepted"}, {BindingID: binding.ID + 1, Status: "pending_review"}} {
		if _, err = f.repo.SecurityNetworkChanges(ctx, f.ws.ID, f.user.ID, filter, changes.NextCursor, 1); !errors.Is(err, ErrInvalid) {
			t.Fatal("change filter cursor", err)
		}
	}
	for _, limit := range []int{0, 201} {
		if _, err = f.repo.SecurityNetworkBindings(ctx, f.ws.ID, f.user.ID, "", "", limit); !errors.Is(err, ErrInvalid) {
			t.Fatal(limit, err)
		}
		if _, err = f.repo.SecurityNetworkChanges(ctx, f.ws.ID, f.user.ID, SecurityNetworkChangeFilter{}, "", limit); !errors.Is(err, ErrInvalid) {
			t.Fatal(limit, err)
		}
		if _, err = f.repo.SecurityPlatformSnapshots(ctx, f.ws.ID, f.user.ID, SecurityPlatformSnapshotFilter{}, "", limit); !errors.Is(err, ErrInvalid) {
			t.Fatal(limit, err)
		}
	}
	for _, q := range []string{strings.Repeat("a", 121), "line\nbreak", string([]byte{0xff})} {
		if _, err = f.repo.SecurityNetworkBindings(ctx, f.ws.ID, f.user.ID, q, "", 1); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid query", fmt.Sprintf("%q", q), err)
		}
	}
	if _, err = f.repo.SecurityNetworkChanges(ctx, f.ws.ID, f.user.ID, SecurityNetworkChangeFilter{Status: "approved"}, "", 1); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown status", err)
	}
	at := time.Now().UTC()
	if _, err = f.repo.SecurityPlatformSnapshots(ctx, f.ws.ID, f.user.ID, SecurityPlatformSnapshotFilter{CreatedFrom: &at, CreatedTo: &at}, "", 1); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty time window", err)
	}
	if _, err = f.repo.SecurityNetworkBindings(ctx, f.ws.ID, f.user.ID, "", "malformed", 1); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid cursor", err)
	}
}
