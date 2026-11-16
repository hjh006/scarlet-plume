package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrivateBifrostHTTPReadinessAndRedirectRefusal(t *testing.T) {
	var healthy, redirect atomic.Bool
	var escaped atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/escape" {
			escaped.Add(1)
			return
		}
		if redirect.Load() {
			http.Redirect(w, r, "/escape", 302)
			return
		}
		if !healthy.Load() {
			http.Error(w, "not initialized", 503)
			return
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	_, raw, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(raw)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := waitBifrost(ctx, port, make(chan error)); err == nil {
		t.Fatal("listener accepted before HTTP dependency ready")
	}
	healthy.Store(true)
	if err := waitBifrost(context.Background(), port, make(chan error)); err != nil {
		t.Fatal(err)
	}
	redirect.Store(true)
	if bifrostHealthy(context.Background(), port) || escaped.Load() != 0 {
		t.Fatal("health redirect followed or considered ready")
	}
}
