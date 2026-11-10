package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
)

func TestAgentRetriesSameObservation(t *testing.T) {
	var requests [][]byte
	var lock sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != "/api/v1/automation/runs" ||
			req.Header.Get("Authorization") != "Bearer sample-collector-token" {
			t.Error("unexpected request")
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Error(err)
		}
		lock.Lock()
		defer lock.Unlock()
		requests = append(requests, body)
		if len(requests) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := newDelivery(directHTTPClient(time.Second), server.URL, "sample-collector-token", true)
	if err != nil {
		t.Fatal(err)
	}
	scans := 0
	collect := assetCollector("postgresql", "示例", func(context.Context) ([]collector.Asset, error) {
		scans++
		return []collector.Asset{{SourceRef: "app.logs", Name: "logs"}}, nil
	})
	if err := collectAndDeliver(t.Context(), "postgresql", collect, client); err != nil {
		t.Fatal(err)
	}
	lock.Lock()
	defer lock.Unlock()
	if scans != 1 || len(requests) != 2 || !bytes.Equal(requests[0], requests[1]) {
		t.Fatalf("expected one scan with an identical retry, got %d scans and %d requests", scans, len(requests))
	}
	var result runResult
	if err := json.Unmarshal(requests[0], &result); err != nil || len(result.RunID) != 32 ||
		result.Provider != "postgresql" || result.ObservedAt.IsZero() || len(result.Assets) == 0 ||
		result.Feishu != nil || result.ErrorCode != "" {
		t.Fatal("invalid run envelope")
	}
}

func TestAgentDoesNotSendCredentialToRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("followed redirect")
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client, err := newDelivery(directHTTPClient(time.Second), redirect.URL, "sample-collector-token", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.submit(t.Context(), runResult{RunID: "run"}); err == nil {
		t.Fatal("accepted redirect")
	}
	for _, origin := range []string{"http://example.com", "http://localhost", "https://user:pass@example.com", "https://example.com/extra"} {
		if _, err := newDelivery(nil, origin, "sample-collector-token", true); err == nil {
			t.Errorf("allowed unsafe origin %s", origin)
		}
	}
}
