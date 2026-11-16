package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func TestOptionsRequireTLSOrLiteralLoopback(t *testing.T) {
	for _, args := range [][]string{{}, {"--listen", "0.0.0.0:8912", "--local"}, {"--listen", "localhost:8912", "--local"}, {"--tls-cert", "relative.crt", "--tls-key", "relative.key"}, {"--local", "--token-env", "RAW=secret"}, {"--local", "--tls-cert", "/cert"}} {
		if _, e := parseOptions(args); e == nil {
			t.Fatalf("unsafe options accepted: %v", args)
		}
	}
	if _, e := parseOptions([]string{"--local", "--listen", "[::1]:8912"}); e != nil {
		t.Fatal(e)
	}
	if _, e := parseOptions([]string{"--listen", "0.0.0.0:8912", "--tls-cert", "/certs/cert.pem", "--tls-key", "/certs/key.pem"}); e != nil {
		t.Fatal(e)
	}
}
func mediaTestAsset(t *testing.T) []byte {
	t.Helper()
	var raw bytes.Buffer
	if e := png.Encode(&raw, image.NewNRGBA(image.Rect(0, 0, 2, 2))); e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(raw.Bytes())
	body, e := json.Marshal(aisecurity.MediaAsset{MIMEType: "image/png", Digest: fmt.Sprintf("%x", sum), Data: raw.Bytes()})
	if e != nil {
		t.Fatal(e)
	}
	return body
}
func TestHandlerAuthenticationHealthAndBoundedWorkers(t *testing.T) {
	token := strings.Repeat("private-test-token", 3)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan int, 2)
	processor := func(ctx context.Context, a aisecurity.MediaAsset) (aisecurity.MediaReport, error) {
		entered <- struct{}{}
		<-release
		return aisecurity.MediaReport{Digest: a.Digest, MIMEType: a.MIMEType, Text: "approved", Complete: true, Confidence: 100, InspectionVersion: aisecurity.MediaInspectionVersion}, nil
	}
	h, e := inspectionHandler(processor, token)
	if e != nil {
		t.Fatal(e)
	}
	body := mediaTestAsset(t)
	request := func(auth string) *http.Request {
		r := httptest.NewRequest("POST", "/v1/inspect", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", auth)
		return r
	}
	for range 2 {
		go func() { w := httptest.NewRecorder(); h.ServeHTTP(w, request("Bearer "+token)); done <- w.Code }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("scanner failed to start")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("Bearer "+token))
	if w.Code != 503 || bytes.Contains(w.Body.Bytes(), []byte(token)) {
		t.Fatalf("worker limit=%d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request("Bearer invalid"))
	if w.Code != 401 {
		t.Fatalf("unauthorized=%d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 204 || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("health disclosed details")
	}
	close(release)
	for range 2 {
		if status := <-done; status != 200 {
			t.Fatalf("inspection=%d", status)
		}
	}
	if _, e = inspectionHandler(processor, token+"\n"); e == nil {
		t.Fatal("newline credential accepted")
	}
}
func TestStartupChecksPinnedModelWithoutDisclosingSecrets(t *testing.T) {
	model := filepath.Join(t.TempDir(), "private-model.bin")
	if e := os.WriteFile(model, bytes.Repeat([]byte{1}, 1024), 0600); e != nil {
		t.Fatal(e)
	}
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	e = run(t.Context(), []string{"--local", "--whisper", executable, "--whisper-model", model, "--whisper-model-sha256", strings.Repeat("0", 64)})
	if e == nil || strings.Contains(e.Error(), model) || strings.Contains(e.Error(), strings.Repeat("0", 64)) {
		t.Fatal("unpinned model accepted or sensitive details disclosed")
	}
}
func TestLocalServiceGracefulShutdown(t *testing.T) {
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("SCARLET_MEDIA_INSPECTOR_TOKEN", strings.Repeat("temporary-test-token", 2))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"--local", "--listen", address, "--tesseract", executable}) }()
	client := &http.Client{Timeout: time.Second}
	ready := false
	for range 50 {
		response, e := client.Get("http://" + address + "/healthz")
		if e == nil {
			_ = response.Body.Close()
			ready = response.StatusCode == 204
			if ready {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("private health endpoint unavailable")
	}
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("graceful shutdown exceeded budget")
	}
}
