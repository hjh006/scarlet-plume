package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

func TestSourceSecurityInspectionDoesNotChangeBusinessTraffic(t *testing.T) {
	body := `{"value":"<script>private-business-marker</script>"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil || string(got) != body || r.URL.Query().Get("q") != "private-query-marker UNION SELECT password FROM users" {
			t.Error("security inspection altered upstream request")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"password":"fixture-not-real-secret"}`)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	telemetry := newObserver("http://127.0.0.1/unused", "fixture")
	defer telemetry.client.CloseIdleConnections()
	transport := directTransport()
	defer transport.CloseIdleConnections()
	proxy := httptest.NewServer(proxyHandlerWithInspection(target, transport, testRoutes(t, "/safe"), telemetry, true))
	defer proxy.Close()
	req, _ := http.NewRequest(http.MethodPost, proxy.URL+"/safe?q="+url.QueryEscape("private-query-marker UNION SELECT password FROM users"), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response, err := proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 201 || string(got) != `{"password":"fixture-not-real-secret"}` {
		t.Fatal("security inspection altered response")
	}
	event := <-telemetry.queue
	for _, kind := range []string{"sql_injection", "xss"} {
		if !slices.ContainsFunc(event.SecuritySignals, func(s runtimesecurity.SecuritySignal) bool { return s.Kind == kind && s.Count == 1 && s.Observed == 2 }) {
			t.Fatalf("missing request pattern: %+v", event.SecuritySignals)
		}
	}
	if !slices.Contains(event.InspectedLocations, "request") || !slices.Contains(event.InspectedLocations, "response") {
		t.Fatal("successful complete capture scope missing")
	}
	event.EventID = strings.Repeat("a", 32)
	raw, _ := json.Marshal([]observationEvent{event})
	parsed, err := runtimesecurity.ParseGateway(raw)
	if err != nil || len(parsed) != 1 {
		t.Fatalf("agent security projection rejected: %v", err)
	}
	if parsed[0].AuthState != "" {
		t.Fatal("proxy invented an authentication outcome")
	}
	for _, secret := range []string{"private-business-marker", "private-query-marker", "fixture-not-real-secret", "<script>", "UNION SELECT"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("business value escaped into telemetry: %s", secret)
		}
	}
}

func TestCombinedCaptureLimitCannotDeclareCleanResponse(t *testing.T) {
	request := map[string]any{}
	// Each body is within its own budget. Their combined projection exceeds 32.
	for _, key := range []string{"data", "items", "results", "records", "contacts", "customers", "users", "profile", "account", "contact", "attributes", "list", "response", "request", "payload", "details"} {
		request[key] = map[string]any{"email": "fixture@example.test"}
	}
	raw, _ := json.Marshal(request)
	o := &requestObservation{queryInspected: true, requestCapture: &bodyCapture{complete: true, buffer: raw}, responseCapture: &bodyCapture{complete: true, buffer: []byte(`{"email":"response@example.test"}`)}}
	fields, warnings := captureBodyStatistics(o)
	if len(fields) > runtimesecurity.MaxCaptureItems || !slices.Contains(warnings, "json_field_limit") || len(o.inspectedLocations) != 0 {
		t.Fatalf("combined truncation fabricated complete scope: fields=%d markers=%v warnings=%v", len(fields), o.inspectedLocations, warnings)
	}
}
