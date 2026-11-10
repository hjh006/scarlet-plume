package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestFeishuTenantTokenRefreshesAndLimitsScope(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		user, secret, ok := req.BasicAuth()
		if !ok || user != "cli_test" || secret != "secret" ||
			req.FormValue("grant_type") != "client_credentials" ||
			req.FormValue("scope") != "docs:permission.member:retrieve" {
			t.Error("unexpected token request")
		}
		fmt.Fprintf(w, `{"access_token":"test-%d","token_type":"Bearer","expires_in":3600}`, requests.Add(1))
	}))
	defer server.Close()
	app := &feishuAppToken{client: directHTTPClient(time.Second), id: "cli_test", secret: "secret"}
	for _, want := range []string{"test-1", "test-1", "test-2"} {
		if want == "test-2" {
			app.refreshAt = time.Now().Add(-time.Second)
		}
		if token, err := app.accessToken(t.Context(), server.URL); err != nil || token != want {
			t.Fatal("token cache did not return the expected value")
		}
	}
}
