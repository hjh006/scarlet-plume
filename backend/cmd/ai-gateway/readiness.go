package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type dependencyReadiness struct {
	mu      sync.RWMutex
	ready   bool
	checked time.Time
}

func (s *dependencyReadiness) record(err error) {
	s.mu.Lock()
	s.ready = err == nil
	s.checked = time.Now()
	s.mu.Unlock()
}
func (s *dependencyReadiness) healthy() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready && !s.checked.IsZero() && time.Since(s.checked) < 90*time.Second
}

func readinessAddress(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	ip := net.ParseIP(host)
	n, parseErr := strconv.Atoi(port)
	if err != nil || ip == nil || !ip.IsLoopback() || parseErr != nil || n < 1 || n > 65535 {
		return errors.New("health listener must be literal loopback")
	}
	return nil
}

func readinessHandler(s *dependencyReadiness) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet || r.URL.RawQuery != "" {
			w.WriteHeader(404)
			return
		}
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(204)
		case "/readyz":
			if s.healthy() {
				w.WriteHeader(204)
			} else {
				w.WriteHeader(503)
			}
		default:
			w.WriteHeader(404)
		}
	})
}

func checkReadiness(addr string) error {
	if readinessAddress(addr) != nil {
		return errors.New("invalid private health listener")
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil, MaxResponseHeaderBytes: 16384}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := client.Get("http://" + addr + "/readyz")
	if err != nil {
		return errors.New("gateway dependencies not ready")
	}
	defer r.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1024))
	if r.StatusCode != 204 {
		return errors.New("gateway dependencies not ready")
	}
	return nil
}

func monitorDependencies(ctx context.Context, s *dependencyReadiness, check func(context.Context) error, interval time.Duration) {
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		bounded, cancel := context.WithTimeout(ctx, 50*time.Second)
		err := check(bounded)
		cancel()
		s.record(err)
	}
}
