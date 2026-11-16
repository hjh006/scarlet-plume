package store

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func identityFixture(t *testing.T) (assessmentSourceFixture, IdentitySettings, string) {
	t.Helper()
	f := assessmentSourceTestStore(t)
	c, e := f.repo.SaveIdentitySettings(t.Context(), f.ws.ID, f.user.ID, IdentitySettings{Enabled: true, Issuer: "https://idp.example.invalid/tenant", ClientID: "test-client", ClientSecretRef: "env:SCARLET_IDP_TEST_SECRET", SCIMEnabled: true, DefaultRole: "viewer", GroupRoles: map[string]string{"operations": "operator"}})
	if e != nil {
		t.Fatal(e)
	}
	h := strings.Repeat("a", 64)
	if e = f.repo.RotateIdentitySCIMToken(t.Context(), f.ws.ID, f.user.ID, h); e != nil {
		t.Fatal(e)
	}
	c, e = f.repo.IdentitySettings(t.Context(), f.ws.ID, f.user.ID)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = f.pool.Exec(ctx, `DELETE FROM plume.users WHERE id IN(SELECT user_id FROM plume.ai_identity_users WHERE workspace_id=$1)`, f.ws.ID)
	})
	return f, c, h
}

func TestIdentityProvisioningIsolationRolesAndImmediateRevocation(t *testing.T) {
	f, c, h := identityFixture(t)
	ctx := t.Context()
	u, e := f.repo.SaveSCIMIdentityUser(ctx, f.ws.Slug, h, 0, IdentityUser{ExternalID: "subject-1", UserName: f.user.Username, DisplayName: "Provisioned account", Active: true}, 0)
	if e != nil {
		t.Fatal(e)
	}
	if u.Role != "viewer" {
		t.Fatal("provisioned identity did not start as viewer")
	}
	local, e := f.repo.FindUser(ctx, f.user.Username)
	if e != nil || local.ID != f.user.ID {
		t.Fatal("SCIM linked to or changed a local account")
	}
	session := strings.Repeat("b", 64)
	user, e := f.repo.CompleteIdentityLogin(ctx, f.ws.ID, c.Revision, c.Issuer, "subject-1", session, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	if user.ID == f.user.ID {
		t.Fatal("federated identity linked by display name")
	}
	if _, e = f.repo.SessionUser(ctx, session); e != nil {
		t.Fatal(e)
	}
	group, e := f.repo.SaveSCIMIdentityGroup(ctx, f.ws.Slug, h, 0, IdentityGroup{ExternalID: "operations", DisplayName: "Operations", Members: []int64{u.ID}}, 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.SessionUser(ctx, session); !errors.Is(e, ErrNotFound) {
		t.Fatal("role change retained old session")
	}
	users, _, e := f.repo.SCIMIdentityUsers(ctx, f.ws.Slug, h, u.ID, "", "", 0, 1)
	if e != nil || len(users) != 1 || users[0].Role != "operator" {
		t.Fatalf("trusted group role not applied: %v", e)
	}
	if _, e = f.repo.CompleteIdentityLogin(ctx, f.ws.ID, c.Revision, "https://other.invalid", "subject-1", session, time.Now().Add(time.Hour)); !errors.Is(e, ErrConflict) {
		t.Fatal("issuer substitution accepted")
	}
	if _, e = f.repo.CompleteIdentityLogin(ctx, f.ws.ID, c.Revision, c.Issuer, "unprovisioned-subject", session, time.Now().Add(time.Hour)); !errors.Is(e, ErrNotFound) {
		t.Fatal("unprovisioned OIDC subject accepted")
	}
	if _, e = f.repo.CompleteIdentityLogin(ctx, f.ws.ID, c.Revision, c.Issuer, "subject-1", session, time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	u.Active = false
	if _, e = f.repo.SaveSCIMIdentityUser(ctx, f.ws.Slug, h, u.ID, u, u.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.SessionUser(ctx, session); !errors.Is(e, ErrNotFound) {
		t.Fatal("disabled account retained session")
	}
	if _, e = f.repo.Membership(ctx, user.ID, f.ws.Slug); !errors.Is(e, ErrNotFound) {
		t.Fatal("disabled account retained membership")
	}
	if _, e = f.repo.CompleteIdentityLogin(ctx, f.ws.ID, c.Revision, c.Issuer, "subject-1", session, time.Now().Add(time.Hour)); !errors.Is(e, ErrNotFound) {
		t.Fatal("disabled account logged in")
	}
	if _, e = f.repo.SaveSCIMIdentityGroup(ctx, f.ws.Slug, h, group.ID, group, group.Revision+1); !errors.Is(e, ErrConflict) {
		t.Fatal("stale group update accepted")
	}
	changed := c
	changed.Issuer = "https://other.invalid"
	if _, e = f.repo.SaveIdentitySettings(ctx, f.ws.ID, f.user.ID, changed); !errors.Is(e, ErrConflict) {
		t.Fatal("issuer changed underneath provisioned subject")
	}
	if e = f.repo.RotateIdentitySCIMToken(ctx, f.ws.ID, f.user.ID, strings.Repeat("c", 64)); e != nil {
		t.Fatal(e)
	}
	if _, _, e = f.repo.SCIMIdentityUsers(ctx, f.ws.Slug, h, 0, "", "", 0, 100); !errors.Is(e, ErrNotFound) {
		t.Fatal("rotated provisioning token still accepted")
	}
}

func TestIdentityCrossWorkspaceAndConfigurationCAS(t *testing.T) {
	f, c, h := identityFixture(t)
	other, _, oh := identityFixture(t)
	ctx := t.Context()
	u, e := f.repo.SaveSCIMIdentityUser(ctx, f.ws.Slug, h, 0, IdentityUser{ExternalID: "subject", UserName: "Employee", Active: true}, 0)
	if e != nil {
		t.Fatal(e)
	}
	foreign, e := other.repo.SaveSCIMIdentityUser(ctx, other.ws.Slug, oh, 0, IdentityUser{ExternalID: "subject", UserName: "Employee", Active: true}, 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.SaveSCIMIdentityGroup(ctx, f.ws.Slug, h, 0, IdentityGroup{ExternalID: "operations", DisplayName: "Operators", Members: []int64{foreign.ID}}, 0); !errors.Is(e, ErrNotFound) {
		t.Fatal("cross-workspace SCIM member accepted")
	}
	if _, e = f.repo.IdentitySettings(ctx, other.ws.ID, f.user.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("foreign owner read identity settings")
	}
	if _, e = f.repo.SaveIdentitySettings(ctx, other.ws.ID, f.user.ID, c); !errors.Is(e, ErrNotFound) {
		t.Fatal("foreign owner changed identity settings")
	}
	users, _, e := f.repo.SCIMIdentityUsers(ctx, f.ws.Slug, h, 0, "", "employee", 0, 100)
	if e != nil || len(users) != 1 {
		t.Fatal("SCIM userName exact filter was case sensitive")
	}
	if _, e = f.repo.SaveSCIMIdentityUser(ctx, f.ws.Slug, h, 0, IdentityUser{ExternalID: "different-subject", UserName: "EMPLOYEE", Active: true}, 0); !errors.Is(e, ErrConflict) {
		t.Fatal("case-equivalent userName duplicate accepted")
	}
	if _, e = f.repo.SaveIdentitySettings(ctx, f.ws.ID, f.user.ID, IdentitySettings{Revision: c.Revision, Enabled: true, Issuer: c.Issuer, ClientID: c.ClientID, ClientSecretRef: c.ClientSecretRef, DefaultRole: "owner"}); !errors.Is(e, ErrInvalid) {
		t.Fatal("SCIM config could grant owner")
	}
	_, e = f.repo.CompleteIdentityLogin(ctx, f.ws.ID, c.Revision, c.Issuer, "subject", strings.Repeat("d", 64), time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	c.Enabled = false
	updated, e := f.repo.SaveIdentitySettings(ctx, f.ws.ID, f.user.ID, c)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.SaveIdentitySettings(ctx, f.ws.ID, f.user.ID, c); !errors.Is(e, ErrConflict) {
		t.Fatal("stale config overwrite accepted")
	}
	if _, e = f.repo.SessionUser(ctx, strings.Repeat("d", 64)); !errors.Is(e, ErrNotFound) {
		t.Fatal("disabled federation retained active session")
	}
	if _, e = f.repo.SaveSCIMIdentityUser(ctx, f.ws.Slug, h, u.ID, IdentityUser{ExternalID: "changed", UserName: u.UserName, Active: true}, u.Revision); !errors.Is(e, ErrInvalid) {
		t.Fatal("subject rebind accepted")
	}
	if _, e = f.repo.CompleteIdentityLogin(ctx, f.ws.ID, updated.Revision, c.Issuer, "subject", strings.Repeat("e", 64), time.Now().Add(time.Hour)); !errors.Is(e, ErrConflict) {
		t.Fatal("disabled federation allowed login")
	}
}

func TestIdentityLoginStateBrowserBindingAndSingleUseAcrossNodes(t *testing.T) {
	f, c, _ := identityFixture(t)
	ctx := t.Context()
	state := IdentityLoginState{WorkspaceID: f.ws.ID, SettingsRevision: c.Revision, StateHash: strings.Repeat("a", 64), BrowserHash: strings.Repeat("b", 64), Nonce: strings.Repeat("n", 43), Verifier: strings.Repeat("v", 43), ExpiresAt: time.Now().Add(time.Minute)}
	if e := f.repo.BeginIdentityLogin(ctx, state); e != nil {
		t.Fatal(e)
	}
	if _, e := f.repo.ConsumeIdentityLogin(ctx, f.ws.ID, state.StateHash, strings.Repeat("c", 64)); !errors.Is(e, ErrNotFound) {
		t.Fatal("login state consumed without browser binding")
	}
	results := make(chan error, 12)
	var successes atomic.Int32
	for range 12 {
		go func() {
			_, e := New(f.pool).ConsumeIdentityLogin(ctx, f.ws.ID, state.StateHash, state.BrowserHash)
			if e == nil {
				successes.Add(1)
			}
			results <- e
		}()
	}
	for range 12 {
		e := <-results
		if e != nil && !errors.Is(e, ErrNotFound) {
			t.Fatal(e)
		}
	}
	if successes.Load() != 1 {
		t.Fatalf("login state consumed %d times", successes.Load())
	}
	state.ExpiresAt = time.Now().Add(-time.Minute)
	if e := f.repo.BeginIdentityLogin(ctx, state); !errors.Is(e, ErrInvalid) {
		t.Fatal("expired login state accepted")
	}
}
