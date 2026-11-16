package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestConsoleRoutes(t *testing.T) {
	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("Scarlet Plume"), 0600); err != nil {
		t.Fatal(err)
	}
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	handler, err := newHandler(api, web)
	if err != nil {
		t.Fatal(err)
	}

	for _, route := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/", http.StatusOK},
		{http.MethodHead, "/", http.StatusOK},
		{http.MethodPost, "/", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/v1/session", http.StatusUnauthorized},
		{http.MethodPost, "/api/v1/session", http.StatusUnauthorized},
		{http.MethodGet, "/healthz", http.StatusNoContent},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(route.method, route.path, nil))
		if w.Code != route.status {
			t.Errorf("%s %s: got %d, want %d", route.method, route.path, w.Code, route.status)
		}
	}
}

func TestLocalDockerOrigin(t *testing.T) {
	const localOrigin = "http://127.0.0.1:8890"
	const containerAddress = "0.0.0.0:8080"

	if secure, err := validateOrigin(localOrigin, containerAddress, true, false); err != nil || secure {
		t.Fatalf("local Docker origin rejected: secure=%v err=%v", secure, err)
	}
	for _, tc := range []struct {
		origin string
		addr   string
		docker bool
	}{
		{localOrigin, containerAddress, false},
		{localOrigin, "0.0.0.0:80", true},
		{"https://example.com", containerAddress, true},
		{"http://example.com", containerAddress, true},
	} {
		if _, err := validateOrigin(tc.origin, tc.addr, tc.docker, false); err == nil {
			t.Errorf("unexpectedly allowed origin %s with address %s and Docker=%v", tc.origin, tc.addr, tc.docker)
		}
	}
}

func TestCanonicalLocalhostOriginRequiresExplicitDevelopmentMode(t *testing.T) {
	for _, tc := range []struct {
		origin, addr          string
		docker, allow, secure bool
	}{
		{"http://localhost:18912", "127.0.0.1:8080", false, true, false},
		{"http://localhost", "127.0.0.1:8080", false, true, false},
		{"http://localhost:18912", "0.0.0.0:8080", true, false, false},
		{"http://127.0.0.1:18912", "127.0.0.1:8080", false, false, false},
		{"https://example.com", "0.0.0.0:8080", false, false, true},
		{"https://LOCALHOST", "0.0.0.0:8080", false, false, true},
	} {
		if secure, err := validateOrigin(tc.origin, tc.addr, tc.docker, tc.allow); err != nil || secure != tc.secure {
			t.Errorf("valid origin %q rejected or secure flag changed: %v %v", tc.origin, secure, err)
		}
	}
	for _, origin := range []string{"http://LOCALHOST:18912", "http://LocalHost:18912", "http://localhost.evil:18912", "http://child.localhost:18912", "http://localhost.:18912", "http://[localhost]:18912", "http://example.com:18912", "http://user@localhost:18912", "http://localhost:18912/", "http://localhost:18912?", "http://localhost:18912?a=b", "http://localhost:18912#fragment", "localhost:18912", "//localhost:18912", "http:localhost", "http://localhost:", "http://localhost:0", "http://localhost:65536", "http://localhost:0018912", "http://localhost:abc"} {
		for _, docker := range []bool{false, true} {
			if _, err := validateOrigin(origin, "0.0.0.0:8080", docker, true); err == nil {
				t.Errorf("unsafe or noncanonical development origin allowed: %q Docker=%v", origin, docker)
			}
		}
	}
	for _, tc := range []struct {
		addr          string
		docker, allow bool
	}{{"127.0.0.1:8080", false, false}, {"0.0.0.0:8080", false, true}, {"0.0.0.0:8081", true, false}} {
		if _, err := validateOrigin("http://localhost:18912", tc.addr, tc.docker, tc.allow); err == nil {
			t.Errorf("localhost broadened development mode/listener: %+v", tc)
		}
	}
}
