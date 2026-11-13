package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const validSnapshot = `{"items":[{"assetId":41,"facts":{"application_name":"Support assistant","application_vendor":"fixture-vendor","application_model":"fixture-model","application_region":"CN","application_retention":"30 days configured","application_training_use":"disabled","application_service_audience":"internal","application_knowledge_bases":"approved support documents","application_tools":"read-only ticket lookup","application_data_categories":"customer support metadata"}}]}`

type receivedObservation struct {
	observation
	body []byte
}

func fixtureAgent(t *testing.T, source http.HandlerFunc) (config, *http.Client, <-chan receivedObservation) {
	t.Helper()
	received := make(chan receivedObservation, 10)
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != observationPath || r.Header.Get("Authorization") != "Bearer platform-fixture-token" {
			t.Error("wrong platform method, path or source credential")
			w.WriteHeader(401)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, maxSnapshotBytes+1))
		var value observation
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&value) != nil || len(body) > maxSnapshotBytes {
			t.Error("invalid observation delivered")
			w.WriteHeader(400)
			return
		}
		received <- receivedObservation{value, body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"accepted":true}`)
	}))
	sourceServer := httptest.NewServer(source)
	c := config{platform: mustURL(t, platform.URL), sourceAPI: mustURL(t, sourceServer.URL+"/configuration"), platformToken: "platform-fixture-token", apiToken: "source-fixture-token"}
	client := newHTTPClient()
	t.Cleanup(func() { client.CloseIdleConnections(); sourceServer.Close(); platform.Close() })
	return c, client, received
}
func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
func nextObservation(t *testing.T, received <-chan receivedObservation) receivedObservation {
	t.Helper()
	select {
	case observed := <-received:
		return observed
	case <-time.After(2 * time.Second):
		t.Fatal("no observation received")
		return receivedObservation{}
	}
}

func TestApplicationAgentApprovedPullSeparatesCredentials(t *testing.T) {
	c, client, received := fixtureAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/configuration" || r.Header.Get("Authorization") != "Bearer source-fixture-token" || r.Header.Get("Accept") != "application/json" {
			t.Error("source request was not an authenticated GET")
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, validSnapshot)
	})
	var output bytes.Buffer
	before := time.Now().UTC()
	if err := run(context.Background(), c, client, true, 30*time.Second, &output); err != nil {
		t.Fatal(err)
	}
	observed := nextObservation(t, received)
	if observed.Status != "success" || len(observed.Items) != 1 || observed.Items[0].AssetID != 41 || len(observed.Items[0].Facts) != 10 || len(observed.BatchID) != 32 || observed.ObservedAt.Before(before) || observed.ObservedAt.After(time.Now()) {
		t.Fatal("wrong scoped observation")
	}
	for _, secret := range []string{c.platformToken, c.apiToken, c.platform.String(), c.sourceAPI.String()} {
		if strings.Contains(output.String(), secret) || bytes.Contains(observed.body, []byte(secret)) {
			t.Fatal("runtime credential or endpoint leaked into payload/logs")
		}
	}
}

func TestApplicationSnapshotRejectsUnknownAmbiguousOrUnboundedInput(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown root":          `{"items":[],"apiKey":"synthetic-raw-secret"}`,
		"unknown item":          `{"items":[{"assetId":1,"facts":{"application_name":"fixture"},"prompt":"synthetic raw prompt"}]}`,
		"unknown fact":          `{"items":[{"assetId":1,"facts":{"api_key":"synthetic-raw-secret"}}]}`,
		"duplicate root":        `{"items":[],"items":[]}`,
		"duplicate asset key":   `{"items":[{"assetId":1,"assetId":2,"facts":{"application_name":"fixture"}}]}`,
		"duplicate fact":        `{"items":[{"assetId":1,"facts":{"application_region":"CN","application_region":"US"}}]}`,
		"duplicate asset":       `{"items":[{"assetId":1,"facts":{"application_name":"fixture"}},{"assetId":1,"facts":{"application_name":"fixture"}}]}`,
		"case-variant root":     `{"Items":[]}`,
		"case-variant identity": `{"items":[{"assetId":1,"AssetID":2,"facts":{"application_name":"fixture"}}]}`,
		"empty facts":           `{"items":[{"assetId":1,"facts":{}}]}`,
		"missing items":         `{}`, "null items": `{"items":null}`,
		"null facts":         `{"items":[{"assetId":1,"facts":null}]}`,
		"invalid id":         `{"items":[{"assetId":0,"facts":{"application_name":"fixture"}}]}`,
		"nonstring fact":     `{"items":[{"assetId":1,"facts":{"application_training_use":false}}]}`,
		"multiple documents": `{"items":[]} {"items":[]}`,
		"blank fact":         `{"items":[{"assetId":1,"facts":{"application_vendor":" "}}]}`,
		"control characters": `{"items":[{"assetId":1,"facts":{"application_vendor":"name\nsecret"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSnapshot([]byte(raw)); !errors.Is(err, errSource) {
				t.Fatal("unsafe snapshot accepted")
			}
		})
	}
	var large snapshot
	for id := int64(1); id <= 51; id++ {
		large.Items = append(large.Items, applicationItem{AssetID: id, Facts: map[string]string{"application_vendor": "fixture"}})
	}
	raw, _ := json.Marshal(large)
	if _, err := decodeSnapshot(raw); err == nil {
		t.Fatal("more than50 applications accepted")
	}
	if _, err := decodeSnapshot(append([]byte(`{"items":[]}`), bytes.Repeat([]byte(" "), maxSnapshotBytes)...)); err == nil {
		t.Fatal("oversized JSON accepted")
	}
	if _, err := decodeSnapshot([]byte("{\"items\":[{\"assetId\":1,\"facts\":{\"vendor\":\"\xff\"}}]}")); err == nil {
		t.Fatal("invalid UTF8 accepted")
	}
	if _, err := decodeSnapshot([]byte(`{"items":[]}`)); err != nil {
		t.Fatal("valid empty snapshot rejected")
	}
	if !safeFact(strings.Repeat("羽", 1000)) || safeFact(strings.Repeat("羽", 1001)) {
		t.Fatal("fact limit is not Unicode characters")
	}
}
func TestApplicationFactsRejectCredentials(t *testing.T) {
	for _, value := range []string{"Bearer synthetic-source-secret", "Basic dXNlcjpwYXNzd29yZA==", "api_key=synthetic-raw-secret", `"client_secret":"synthetic-value"`, "-----BEGIN PRIVATE KEY-----", "sk-proj-" + strings.Repeat("s", 24), "AKIA1234567890ABCDEF", "ghp_" + strings.Repeat("s", 24), "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJmaXh0dXJlIn0.signaturefixturevalue", "https://user:synthetic-password@example.invalid/api", "https://example.invalid/api?token=synthetic-secret"} {
		if safeFact(value) {
			t.Errorf("credential-shaped fact accepted(length%d)", len(value))
		}
	}
	for _, value := range []string{"fixture-vendor", "CN", "30 days configured", "disabled", "internal", "read-only document lookup", "Basic search", "asia-pacific-region", "https://tools.example.invalid/search"} {
		if !safeFact(value) {
			t.Errorf("safe metadata rejected:%s", value)
		}
	}
}
func TestApplicationFailuresSendEmptyErrorWithoutLeakingBodies(t *testing.T) {
	for _, mode := range []string{"unauthorized", "wrong-content-type", "unknown-fields", "secret-value", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			const sentinel = "SYNTHETIC_RESPONSE_SECRET_DO_NOT_FORWARD"
			c, client, received := fixtureAgent(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch mode {
				case "unauthorized":
					w.WriteHeader(401)
					_, _ = io.WriteString(w, sentinel)
				case "wrong-content-type":
					w.Header().Set("Content-Type", "text/plain")
					_, _ = io.WriteString(w, validSnapshot)
				case "unknown-fields":
					_, _ = fmt.Fprintf(w, `{"items":[],"password":"%s"}`, sentinel)
				case "secret-value":
					_, _ = fmt.Fprintf(w, `{"items":[{"assetId":41,"facts":{"application_vendor":"api_key=%s"}}]}`, sentinel)
				case "oversized":
					_, _ = io.WriteString(w, strings.Repeat(sentinel, maxSnapshotBytes/len(sentinel)+1))
				}
			})
			var output bytes.Buffer
			if err := runOnce(context.Background(), c, client, &output); !errors.Is(err, errSource) {
				t.Fatal("source failure was not surfaced")
			}
			observed := nextObservation(t, received)
			if observed.Status != "error" || observed.Items == nil || len(observed.Items) != 0 {
				t.Fatal("failure did not send an empty error observation")
			}
			if strings.Contains(output.String(), sentinel) || bytes.Contains(observed.body, []byte(sentinel)) || strings.Contains(output.String(), c.sourceAPI.String()) {
				t.Fatal("source response or URL leaked")
			}
		})
	}
}
func TestApplicationRedirectsNeverForwardCredentials(t *testing.T) {
	var followed atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Add(1) }))
	defer trap.Close()
	c, client, received := fixtureAgent(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, trap.URL+"/redirected", 307) })
	var output bytes.Buffer
	if err := runOnce(context.Background(), c, client, &output); !errors.Is(err, errSource) {
		t.Fatal("redirect source accepted")
	}
	if followed.Load() != 0 || nextObservation(t, received).Status != "error" {
		t.Fatal("source redirect followed or failure not reported")
	}
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, trap.URL+"/platform-redirect", 307) }))
	defer platform.Close()
	c.platform = mustURL(t, platform.URL)
	if err := deliver(context.Background(), c, client, []byte(`{"status":"error"}`)); !errors.Is(err, errDelivery) || followed.Load() != 0 {
		t.Fatal("platform redirect followed")
	}
}
func TestApplicationVerifiedTLSAndTimeout(t *testing.T) {
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, validSnapshot)
	}))
	defer source.Close()
	source.Config.ErrorLog = log.New(io.Discard, "", 0)
	c := config{sourceAPI: mustURL(t, source.URL)}
	untrusted := newHTTPClient()
	defer untrusted.CloseIdleConnections()
	if _, err := collect(context.Background(), c, untrusted); !errors.Is(err, errSource) {
		t.Fatal("untrusted TLS accepted")
	}
	trusted := newHTTPClient()
	defer trusted.CloseIdleConnections()
	trusted.Transport.(*http.Transport).TLSClientConfig.RootCAs = source.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	if _, err := collect(context.Background(), c, trusted); err != nil {
		t.Fatal("verified TLS source rejected")
	}
	trusted.CloseIdleConnections()
	trusted.Transport.(*http.Transport).TLSClientConfig.ServerName = "wrong-host.example.invalid"
	if _, err := collect(context.Background(), c, trusted); !errors.Is(err, errSource) {
		t.Fatal("wrong TLS hostname accepted")
	}
	c, client, received := fixtureAgent(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	client.Timeout = 20 * time.Millisecond
	var output bytes.Buffer
	before := time.Now()
	if err := runOnce(context.Background(), c, client, &output); !errors.Is(err, errSource) {
		t.Fatal("source timeout not reported")
	}
	if time.Since(before) > time.Second || nextObservation(t, received).Status != "error" {
		t.Fatal("timeout did not bound collection")
	}
}
func TestApplicationDeliveryRetriesSameBatchAndStopsOnPermanentRejection(t *testing.T) {
	var requests atomic.Int32
	bodies := make(chan []byte, 3)
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies <- raw
		if requests.Add(1) == 1 {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(200)
		}
	}))
	defer platform.Close()
	c := config{platform: mustURL(t, platform.URL), platformToken: "platform-fixture-token"}
	client := newHTTPClient()
	defer client.CloseIdleConnections()
	body := []byte(`{"batchId":"same-fixture-batch","observedAt":"2026-11-12T00:00:00Z","status":"success","items":[]}`)
	if err := deliver(context.Background(), c, client, body); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || !bytes.Equal(<-bodies, <-bodies) {
		t.Fatal("retry changed idempotency payload")
	}
	requests.Store(0)
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(403)
		_, _ = io.WriteString(w, "SYNTHETIC_PLATFORM_SECRET")
	}))
	defer denied.Close()
	c.platform = mustURL(t, denied.URL)
	if err := deliver(context.Background(), c, client, body); !errors.Is(err, errDelivery) || requests.Load() != 1 {
		t.Fatal("permanent rejection retried")
	}
}
func TestApplicationConfigurationLimitsEndpointsAndCredentials(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8080", "http://localhost:8080", "http://10.0.0.1:8080", "https://user:secret@example.invalid/api", "https://example.invalid/api?token=secret", "https://example.invalid/api#fragment", "file:///etc/passwd"} {
		if _, err := approvedURL(raw, false, false); err == nil {
			t.Error("unsafe endpoint accepted")
		}
	}
	if _, err := approvedURL("http://127.0.0.1:8080/configuration", false, true); err != nil {
		t.Fatal("explicit loopback fixture rejected")
	}
	for _, raw := range []string{"http://localhost:8080", "http://10.0.0.1:8080", "http://app:8080", "http://127.0.0.1.evil.example:8080"} {
		if _, err := approvedURL(raw, false, true); err == nil {
			t.Fatal("loopback exception expanded")
		}
	}
	if _, err := approvedURL("https://platform.example.invalid/path", true, false); err == nil {
		t.Fatal("platform path not fixed")
	}
	values := map[string]string{"SCARLET_URL": "https://platform.example.invalid", "SCARLET_APPLICATION_API_URL": "https://app.example.invalid/configuration", "SCARLET_APPLICATION_TOKEN": "fixture-source-token"}
	if _, err := readConfig(func(k string) string { return values[k] }, false); err != nil {
		t.Fatal("optional API token required")
	}
	values["SCARLET_APPLICATION_API_TOKEN"] = "secret\r\ninjected-header"
	if _, err := readConfig(func(k string) string { return values[k] }, false); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("invalid credential accepted or echoed")
	}
	client := newHTTPClient()
	defer client.CloseIdleConnections()
	if client.Timeout != 10*time.Second || client.Transport.(*http.Transport).Proxy != nil || client.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify {
		t.Fatal("transport bounds or TLS verification disabled")
	}
}
