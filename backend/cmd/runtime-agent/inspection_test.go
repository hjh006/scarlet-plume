package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

func TestOptInJSONInspectionPreservesProxyTraffic(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(map[bool]string{false: "bounded", true: "large"}[large], func(t *testing.T) {
			requestBody := `{"phone":"13900000000","private-user-key@example.test":"private-value"}`
			responseBody := `{"contacts":[{"contact_value":"one@example.test"},{"contact_value":"two@example.test"}]}`
			if large {
				responseBody = `{"value":"` + strings.Repeat("x", runtimesecurity.MaxJSONCaptureBytes) + `"}`
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != requestBody || r.Header.Get("Authorization") != "Bearer private-original-token" {
					t.Error("inspection changed the upstream business request")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(207)
				if n, err := io.WriteString(w, responseBody[:5]); err != nil || n != 5 {
					t.Errorf("upstream prefix write failed: error=%v written=%d expected=5", err, n)
					return
				}
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Errorf("upstream prefix flush failed: %v", err)
					return
				}
				if n, err := io.WriteString(w, responseBody[5:]); err != nil || n != len(responseBody)-5 {
					t.Errorf("upstream remainder write failed: error=%v written=%d expected=%d", err, n, len(responseBody)-5)
				}
			}))
			defer upstream.Close()
			target, _ := url.Parse(upstream.URL)
			telemetry := newObserver("http://127.0.0.1/unused", "fixture-token")
			defer telemetry.client.CloseIdleConnections()
			transport := directTransport()
			defer transport.CloseIdleConnections()
			proxy := httptest.NewServer(proxyHandlerWithInspection(target, transport, testRoutes(t, "/contacts"), telemetry, true))
			defer proxy.Close()
			req, _ := http.NewRequest(http.MethodPost, proxy.URL+"/contacts?private_query=value", strings.NewReader(requestBody))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer private-original-token")
			response, err := proxy.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || response.StatusCode != 207 || string(body) != responseBody {
				t.Fatalf("inspection changed the response stream or status: read error=%v status=%d received=%d expected=%d", err, response.StatusCode, len(body), len(responseBody))
			}
			event := <-telemetry.queue
			if large {
				if !slices.Contains(event.CaptureWarnings, "json_size_limit") {
					t.Fatal("large chunked response did not report its bound")
				}
			} else if !slices.ContainsFunc(event.DataFields, func(field runtimesecurity.DataField) bool {
				return field.Location == "response" && field.Category == "email" && field.Method == "format" && field.Count == 2 && field.Observed == 2
			}) {
				t.Fatal("successful response format statistics missing")
			}
			event.EventID = strings.Repeat("a", 32)
			encoded, _ := json.Marshal([]observationEvent{event})
			if _, err := runtimesecurity.ParseGateway(encoded); err != nil {
				t.Fatalf("agent emitted an invalid capture projection: %v", err)
			}
			for _, secret := range []string{"13900000000", "one@example.test", "two@example.test", "private-user", "private-value", "private-original-token", "private_query"} {
				if bytes.Contains(encoded, []byte(secret)) {
					t.Errorf("raw business data escaped: %s", secret)
				}
			}
		})
	}
}

type errorTransport struct{}

func (errorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("private-upstream-error")
}

func TestInspectionKeepsUpstreamErrors(t *testing.T) {
	target, _ := url.Parse("http://upstream.invalid")
	telemetry := newObserver("http://127.0.0.1/unused", "fixture-token")
	defer telemetry.client.CloseIdleConnections()
	request := httptest.NewRequest(http.MethodPost, "http://proxy/contacts", strings.NewReader(`{"email":"one@example.test"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	proxyHandlerWithInspection(target, errorTransport{}, testRoutes(t, "/contacts"), telemetry, true).ServeHTTP(response, request)
	if response.Code != 502 || !strings.Contains(response.Body.String(), "Bad Gateway") {
		t.Fatal("inspection changed the upstream failure behavior")
	}
	event := <-telemetry.queue
	if event.Status != 502 || !slices.Contains(event.CaptureWarnings, "json_read_error") {
		t.Fatal("an unread request was represented as complete inspection")
	}
}

// The real HTTP fixture above leaves Read boundaries to the transport. These
// responses deterministically cross the capture ceiling, including a final
// Read that returns data and EOF together.
func TestJSONInspectionPreservesResponseReadBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name        string
		size        int
		cuts        []int
		eofWithData bool
	}{
		{"exact-limit", runtimesecurity.MaxJSONCaptureBytes, []int{5}, false},
		{"one-byte-over", runtimesecurity.MaxJSONCaptureBytes + 1, []int{runtimesecurity.MaxJSONCaptureBytes - 1, runtimesecurity.MaxJSONCaptureBytes}, false},
		{"crossing-read-with-eof", runtimesecurity.MaxJSONCaptureBytes + 11, []int{runtimesecurity.MaxJSONCaptureBytes - 2, runtimesecurity.MaxJSONCaptureBytes + 2}, true},
		{"flushed-prefix-with-eof", runtimesecurity.MaxJSONCaptureBytes + 11, []int{5}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const prefix = `{"value":"`
			const suffix = `"}`
			body := prefix + strings.Repeat("x", tc.size-len(prefix)-len(suffix)) + suffix
			chunks := []string{}
			start := 0
			for _, cut := range tc.cuts {
				chunks = append(chunks, body[start:cut])
				start = cut
			}
			chunks = append(chunks, body[start:])
			transport := captureResponseTransport{body: &captureChunkReader{chunks: chunks, eofWithData: tc.eofWithData}}
			target, _ := url.Parse("http://upstream.invalid")
			telemetry := newObserver("http://127.0.0.1/unused", "fixture-token")
			defer telemetry.client.CloseIdleConnections()
			response := httptest.NewRecorder()
			proxyHandlerWithInspection(target, transport, testRoutes(t, "/contacts"), telemetry, true).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://proxy/contacts", nil))
			result := response.Result()
			got, err := io.ReadAll(result.Body)
			_ = result.Body.Close()
			if err != nil || result.StatusCode != 207 || string(got) != body {
				t.Fatalf("response read boundaries changed traffic: read error=%v status=%d received=%d expected=%d", err, result.StatusCode, len(got), len(body))
			}
			event := <-telemetry.queue
			limited := slices.Contains(event.CaptureWarnings, "json_size_limit")
			if limited != (tc.size > runtimesecurity.MaxJSONCaptureBytes) {
				t.Fatalf("wrong size-limit evidence: warnings=%v bytes=%d", event.CaptureWarnings, tc.size)
			}
			if limited && slices.Contains(event.InspectedLocations, "response") {
				t.Fatal("truncated capture declared complete response inspection")
			}
		})
	}
}

type captureResponseTransport struct{ body io.ReadCloser }

func (t captureResponseTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 207, Header: http.Header{"Content-Type": []string{"application/json"}}, ContentLength: -1, Body: t.body, Request: r}, nil
}

type captureChunkReader struct {
	chunks      []string
	eofWithData bool
}

func (r *captureChunkReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks[0] = r.chunks[0][n:]
	if len(r.chunks[0]) == 0 {
		r.chunks = r.chunks[1:]
	}
	if len(r.chunks) == 0 && r.eofWithData {
		return n, io.EOF
	}
	return n, nil
}
func (r *captureChunkReader) Close() error { return nil }
