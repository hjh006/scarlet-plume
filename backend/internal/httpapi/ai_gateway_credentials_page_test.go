package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestGatewayCredentialPageHTTPFiltersScopeAndSecretProjection(t *testing.T) {
	f := newIdentityHTTPFixture(t)
	base := "/api/v1/workspaces/" + f.ws.Slug + "/ai-gateway"
	cfg := gatewayHTTPConfig()
	identityHTTPRequest(t, f.handler, "PUT", base+"/config", gatewayHTTPJSON(t, map[string]any{"expectedRevision": 0, "enabled": cfg.Enabled, "catalogDigest": cfg.CatalogDigest, "applications": cfg.Applications, "providers": cfg.Providers}), f.cookie, "", 200)
	var original struct {
		Credential store.GatewayCredential `json:"credential"`
		Token      string                  `json:"token"`
	}
	if e := json.Unmarshal(identityHTTPRequest(t, f.handler, "POST", base+"/credentials", `{"applicationId":"app","name":"Old active key"}`, f.cookie, "", 201).Body.Bytes(), &original); e != nil {
		t.Fatal(e)
	}
	for i := range 3 {
		if _, e := f.pool.Exec(t.Context(), `INSERT INTO plume.ai_gateway_credentials(workspace_id,application_id,name,prefix,token_hash,enabled,expires_at)VALUES($1,'app','expired history','sgw_expired',$2,false,clock_timestamp()-interval '1 minute')`, f.ws.ID, sessionHash(fmt.Sprintf("%s-expired-%d", f.ws.Slug, i))); e != nil {
			t.Fatal(e)
		}
	}
	path := base + "/credentials"
	identityHTTPRequest(t, f.handler, "GET", path, "", nil, "", 401)
	var page store.GatewayCredentialPage
	w := identityHTTPRequest(t, f.handler, "GET", path+"?limit=1", "", f.viewerCookie, "", 200)
	if e := json.Unmarshal(w.Body.Bytes(), &page); e != nil || len(page.Items) != 1 || !page.HasMore || page.NextCursor == "" || page.Items[0].Status != "expired" {
		t.Fatal("metadata page DTO invalid", e)
	}
	if strings.Contains(w.Body.String(), original.Token) || strings.Contains(w.Body.String(), sessionHash(original.Token)) || strings.Contains(w.Body.String(), `"token"`) {
		t.Fatal("metadata endpoint recovered credentials")
	}
	identityHTTPRequest(t, f.handler, "GET", path+"?before="+page.NextCursor+"&limit=2", "", f.viewerCookie, "", 200)
	identityHTTPRequest(t, f.handler, "GET", path+"?before="+page.NextCursor+"&state=active", "", f.viewerCookie, "", 400)
	for _, q := range []string{"limit=0", "limit=201", "limit=1&limit=2", "state=bogus", "state=active&state=expired", "applicationId=app&applicationId=other", "applicationId=invalid%20app", "before=bad", "before=&before="} {
		identityHTTPRequest(t, f.handler, "GET", path+"?"+q, "", f.cookie, "", 400)
	}
	active := identityHTTPRequest(t, f.handler, "GET", path+"?state=active&applicationId=app&limit=1", "", f.viewerCookie, "", 200)
	if e := json.Unmarshal(active.Body.Bytes(), &page); e != nil || len(page.Items) != 1 || page.Items[0].ID != original.Credential.ID || !page.Items[0].Enabled || page.HasMore {
		t.Fatal("old active key undiscoverable", e)
	}
	identityHTTPRequest(t, f.handler, "GET", "/api/v1/workspaces/unknown/ai-gateway/credentials", "", f.viewerCookie, "", 404)
	identityHTTPRequest(t, f.handler, "DELETE", fmt.Sprintf("%s/%d", path, original.Credential.ID), "", f.viewerCookie, "", 403)
	identityHTTPRequest(t, f.handler, "DELETE", fmt.Sprintf("%s/%d", path, original.Credential.ID), "", f.cookie, "", 204)
	identityHTTPRequest(t, f.handler, "GET", path+"?state=disabled", "", f.viewerCookie, "", 200)
}
