package main

import (
	"context"
	"errors"
	"flag"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadinessMonitorCancelsInFlightProbe(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	entered := make(chan struct{})
	done := make(chan struct{})
	s := &dependencyReadiness{}
	go func() {
		defer close(done)
		monitorDependencies(ctx, s, func(probe context.Context) error { close(entered); <-probe.Done(); return probe.Err() }, time.Millisecond)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe not entered")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled monitor kept probe alive")
	}
	if s.healthy() {
		t.Fatal("cancelled probe became ready")
	}
}

func TestReadinessSubcommandDoesNotOpenExecutionState(t *testing.T) {
	if os.Getenv("SCARLET_READY_CHECK_HELPER") == "1" {
		flag.CommandLine = flag.NewFlagSet("ai-gateway", flag.ContinueOnError)
		os.Args = []string{"ai-gateway", "-journal", os.Getenv("SCARLET_READY_CHECK_STATE"), "-bindings", "/unavailable/bindings.json", "-health-listen", os.Getenv("SCARLET_READY_CHECK_ADDRESS"), "-check-ready"}
		if run() != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	s := httptest.NewServer(readinessHandler(&dependencyReadiness{ready: true, checked: time.Now()}))
	defer s.Close()
	state := filepath.Join(t.TempDir(), "must-not-create-journal")
	p := exec.Command(os.Args[0], "-test.run=^TestReadinessSubcommandDoesNotOpenExecutionState$")
	p.Env = append(os.Environ(), "SCARLET_READY_CHECK_HELPER=1", "SCARLET_READY_CHECK_STATE="+state, "SCARLET_READY_CHECK_ADDRESS="+strings.TrimPrefix(s.URL, "http://"))
	if out, err := p.CombinedOutput(); err != nil {
		t.Fatal("readiness subcommand read serving configuration", err, string(out))
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("healthcheck changed execution journal")
	}
}

func TestPrivateGatewayReadinessIsNotLiveness(t *testing.T) {
	s := &dependencyReadiness{}
	h := readinessHandler(s)
	call := func(path string) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w.Code
	}
	if call("/healthz") != 204 || call("/readyz") != 503 {
		t.Fatal("unprobed dependency became ready")
	}
	s.record(nil)
	if call("/readyz") != 204 {
		t.Fatal("successful dependency not ready")
	}
	s.record(errors.New("old scanner"))
	if call("/healthz") != 204 || call("/readyz") != 503 {
		t.Fatal("dependency failure became healthy")
	}
	s.ready = true
	s.checked = time.Now().Add(-2 * time.Minute)
	if call("/readyz") != 503 {
		t.Fatal("stale readiness accepted")
	}
	for _, addr := range []string{"0.0.0.0:8906", "localhost:8906", "127.0.0.1:0", "127.0.0.1:99999", "127.0.0.1:80/elsewhere"} {
		if readinessAddress(addr) == nil {
			t.Fatal("unsafe readiness target", addr)
		}
	}
	server := httptest.NewServer(readinessHandler(&dependencyReadiness{ready: true, checked: time.Now()}))
	defer server.Close()
	if checkReadiness(strings.TrimPrefix(server.URL, "http://")) != nil {
		t.Fatal("private probe failed")
	}
}
