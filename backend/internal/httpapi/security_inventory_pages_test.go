package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestOwnerAccountPageHTTPAndMemberPostureScope(t *testing.T) {
	f := newIdentityHTTPFixture(t)
	base := "/api/v1/workspaces/" + f.ws.Slug
	for _, query := range []string{"?limit=0", "?limit=201", "?active=unknown", "?cursor=invalid"} {
		identityHTTPRequest(t, f.handler, "GET", base+"/ai-gateway/identity/users"+query, "", f.cookie, "", 400)
	}
	identityHTTPRequest(t, f.handler, "GET", base+"/ai-gateway/identity/users?limit=50", "", f.viewerCookie, "", 403)
	w := identityHTTPRequest(t, f.handler, "GET", base+"/ai-gateway/identity/users?limit=50", "", f.cookie, "", 200)
	var accounts store.IdentityUserPage
	if err := json.Unmarshal(w.Body.Bytes(), &accounts); err != nil || accounts.Items == nil || accounts.Limit != 50 {
		t.Fatalf("accounts contract: %s %v", w.Body.String(), err)
	}
	for _, name := range []string{"Synthetic inventory A", "Synthetic inventory B"} {
		if _, err := f.repo.CreateAsset(t.Context(), f.ws.ID, name, "Inventory fixture", "Synthetic team"); err != nil {
			t.Fatal(err)
		}
	}
	w = identityHTTPRequest(t, f.handler, "GET", base+"/security-platform/posture?q=Synthetic&limit=1", "", f.viewerCookie, "", 200)
	var posture store.SecurityPosturePage
	if err := json.Unmarshal(w.Body.Bytes(), &posture); err != nil || len(posture.Assets) != 1 || posture.MatchingAssets != 2 || !posture.HasMore || posture.NextCursor == "" {
		t.Fatalf("posture contract: %s %v", w.Body.String(), err)
	}
	for _, query := range []string{"?limit=101", "?severity=critical", "?code=unknown", "?cursor=invalid"} {
		identityHTTPRequest(t, f.handler, "GET", base+"/security-platform/posture"+query, "", f.cookie, "", 400)
	}
	other := newIdentityHTTPFixture(t)
	identityHTTPRequest(t, other.handler, "GET", base+"/security-platform/posture", "", other.cookie, "", 404)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("inventory cached")
	}
}
