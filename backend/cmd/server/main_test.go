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

	if secure, err := validateOrigin(localOrigin, containerAddress, true); err != nil || secure {
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
		if _, err := validateOrigin(tc.origin, tc.addr, tc.docker); err == nil {
			t.Errorf("unexpectedly allowed origin %s with address %s and Docker=%v", tc.origin, tc.addr, tc.docker)
		}
	}
}
