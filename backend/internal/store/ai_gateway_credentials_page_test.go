package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestGatewayCredentialsPageKeepsOldLiveKeyVisibleAfterWorkloadHistory(t *testing.T) {
	f, in := gatewayTestSetup(t, 100, 10, 100)
	viewer := gatewayTestMember(t, f, "viewer")
	original, err := f.repo.ResolveGateway(t.Context(), GatewayResolveInput{WorkspaceSlug: f.ws.Slug, TokenHash: in.TokenHash})
	if err != nil {
		t.Fatal(err)
	}
	disabled, token, err := f.repo.CreateGatewayCredential(t.Context(), f.ws.ID, f.user.ID, "app", "manually disabled fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.repo.RevokeGatewayCredential(t.Context(), f.ws.ID, f.user.ID, disabled.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(t.Context(), `INSERT INTO plume.ai_gateway_credentials(workspace_id,application_id,name,prefix,token_hash,enabled,expires_at) SELECT $1,'app','expired-fixture-'||n,'sgw_expired',encode(sha256(convert_to($2::text||':'||n::text,'UTF8')),'hex'),n%2=0,clock_timestamp()-interval '1 minute' FROM generate_series(1,1005) n`, f.ws.ID, f.ws.Slug)
	if err != nil {
		t.Fatal(err)
	}
	view, err := f.repo.GatewayOverview(t.Context(), f.ws.ID, viewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Credentials) != 1000 || view.Credentials[0].ID != original.CredentialID || !view.CredentialPreview.HasMore || view.CredentialPreview.Returned != 1000 {
		t.Fatal("legacy active key was hidden by short-token history", view.CredentialPreview)
	}
	active, err := f.repo.GatewayCredentials(t.Context(), f.ws.ID, viewer, "", "active", "app", 1)
	if err != nil || len(active.Items) != 1 || active.Items[0].ID != original.CredentialID || active.Items[0].Status != "active" || !active.Items[0].Enabled || active.HasMore {
		t.Fatal("active filter lost old key", active, err)
	}
	manual, err := f.repo.GatewayCredentials(t.Context(), f.ws.ID, viewer, "", "disabled", "", 100)
	if err != nil || len(manual.Items) != 1 || manual.Items[0].ID != disabled.ID || manual.Items[0].Enabled {
		t.Fatal("disabled state conflated with expired", manual, err)
	}
	expired, err := f.repo.GatewayCredentials(t.Context(), f.ws.ID, viewer, "", "expired", "", 200)
	if err != nil || len(expired.Items) != 200 || !expired.HasMore {
		t.Fatal("expired history silently truncated", expired, err)
	}
	for _, v := range expired.Items {
		if v.Status != "expired" || v.Enabled {
			t.Fatal("expired item labelled active")
		}
	}
	first, err := f.repo.GatewayCredentials(t.Context(), f.ws.ID, viewer, "", "", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	page := first
	found := false
	var last int64
	for {
		for _, v := range page.Items {
			if seen[v.ID] || (last != 0 && v.ID >= last) {
				t.Fatal("keyset duplicate or ordering error")
			}
			seen[v.ID] = true
			last = v.ID
			if v.ID == original.CredentialID {
				found = true
			}
		}
		if !page.HasMore {
			break
		}
		page, err = f.repo.GatewayCredentials(t.Context(), f.ws.ID, viewer, page.NextCursor, "", "", 200)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 1007 || !found {
		t.Fatal("could not enumerate and discover all credential metadata", len(seen), found)
	}
	for _, tc := range []struct {
		cursor, state, app string
		limit              int
	}{{first.NextCursor, "active", "", 200}, {first.NextCursor, "", "app", 200}, {"invalid", "", "", 200}, {"", "invalid", "", 200}, {"", "", "invalid app", 200}, {"", "", "", 201}} {
		if _, e := f.repo.GatewayCredentials(t.Context(), f.ws.ID, viewer, tc.cursor, tc.state, tc.app, tc.limit); !errors.Is(e, ErrInvalid) {
			t.Fatal("invalid filter/cursor accepted", e)
		}
	}
	foreign := assessmentSourceTestStore(t)
	if _, e := f.repo.GatewayCredentials(t.Context(), f.ws.ID, foreign.user.ID, "", "", "", 10); !errors.Is(e, ErrNotFound) {
		t.Fatal("non-member read credential metadata", e)
	}
	if _, e := f.repo.GatewayCredentials(t.Context(), foreign.ws.ID, foreign.user.ID, first.NextCursor, "", "", 10); !errors.Is(e, ErrInvalid) {
		t.Fatal("workspace cursor reused", e)
	}
	projection, _ := json.Marshal(struct {
		Overview GatewayOverview
		Page     GatewayCredentialPage
	}{view, first})
	if strings.Contains(string(projection), token) || strings.Contains(string(projection), in.TokenHash) || strings.Contains(string(projection), "token_hash") {
		t.Fatal("credential secret or full hash recovered through metadata")
	}
	if err = f.repo.RevokeGatewayCredential(t.Context(), f.ws.ID, viewer, original.CredentialID); !errors.Is(err, ErrForbidden) {
		t.Fatal("viewer revoked credential", err)
	}
	if err = f.repo.RevokeGatewayCredential(t.Context(), f.ws.ID, f.user.ID, original.CredentialID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.ResolveGateway(t.Context(), GatewayResolveInput{WorkspaceSlug: f.ws.Slug, TokenHash: in.TokenHash}); !errors.Is(err, ErrForbidden) {
		t.Fatal("discovered old credential could not be revoked", err)
	}
	active, err = f.repo.GatewayCredentials(t.Context(), f.ws.ID, viewer, "", "active", "app", 100)
	if err != nil || len(active.Items) != 0 {
		t.Fatal("revoked key still shown active", active, err)
	}
}
