package aisecurity

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func gatewayHeaderFixture(t *testing.T) (http.Handler, *atomic.Int32, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var provider, authenticated, admitted atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		provider.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"safe answer"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(up.Close)
	c := fixture()
	c.Models[0].ResponseFormat = "openai_chat"
	runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	o := runtime.(*runtimeHandler).options
	o.Authenticate = func(context.Context, string) (Identity, error) {
		authenticated.Add(1)
		return Identity{ApplicationID: "app", PrincipalID: "alice"}, nil
	}
	g, err := NewGateway(GatewayOptions{Runtime: o, BeforeExecute: func(context.Context, GatewayAuthorization) error {
		admitted.Add(1)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return g, &provider, &authenticated, &admitted
}

func TestGatewayRejectsAmbiguousEnvelopeHeadersBeforeAuthentication(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/execute", "/mcp", "/v1/mcp/execute"} {
		for _, tc := range []struct {
			name, header string
			values       []string
			status       int
		}{
			{"duplicate credential", "Authorization", []string{"Bearer " + aliceToken, "Bearer " + bobToken}, 401},
			{"duplicate same credential", "Authorization", []string{"Bearer " + aliceToken, "Bearer " + aliceToken}, 401},
			{"duplicate content type", "Content-Type", []string{"application/json", "application/json"}, 415},
			{"conflicting content type", "Content-Type", []string{"application/json", "text/plain"}, 415},
			{"hidden compression", "Content-Encoding", []string{"", "gzip"}, 415},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				g, provider, authenticated, admitted := gatewayHeaderFixture(t)
				in := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
				in.Header.Set("Content-Type", "application/json")
				in.Header.Set("Authorization", "Bearer "+aliceToken)
				in.Header[http.CanonicalHeaderKey(tc.header)] = tc.values
				out := httptest.NewRecorder()
				g.ServeHTTP(out, in)
				if out.Code != tc.status || provider.Load() != 0 || authenticated.Load() != 0 || admitted.Load() != 0 {
					t.Fatalf("ambiguous envelope reached authenticated execution: status=%d auth=%d admission=%d provider=%d", out.Code, authenticated.Load(), admitted.Load(), provider.Load())
				}
			})
		}
	}
}

func TestGatewayChatExplicitControlsNeverBecomeDefaults(t *testing.T) {
	now := time.Now().UTC()
	nonce := strings.Repeat("n", 24)
	for _, tc := range []struct {
		name, header string
		values       []string
	}{
		{"empty nonce", "Idempotency-Key", []string{""}},
		{"duplicate same nonce", "Idempotency-Key", []string{nonce, nonce}},
		{"duplicate different nonce", "Idempotency-Key", []string{nonce, strings.Repeat("p", 24)}},
		{"empty issue time", "X-Scarlet-Issued-At", []string{""}},
		{"empty deadline", "X-Scarlet-Expires-At", []string{""}},
		{"zero issue time", "X-Scarlet-Issued-At", []string{time.Time{}.Format(time.RFC3339Nano)}},
		{"zero deadline", "X-Scarlet-Expires-At", []string{time.Time{}.Format(time.RFC3339Nano)}},
		{"duplicate issue time", "X-Scarlet-Issued-At", []string{now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)}},
		{"hidden expired deadline", "X-Scarlet-Expires-At", []string{"", now.Add(-time.Minute).Format(time.RFC3339Nano)}},
		{"duplicate deadline", "X-Scarlet-Expires-At", []string{now.Add(time.Second).Format(time.RFC3339Nano), now.Add(-time.Minute).Format(time.RFC3339Nano)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, provider, _, admitted := gatewayHeaderFixture(t)
			in := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"enterprise","messages":[{"role":"user","content":"hello"}]}`))
			in.Header.Set("Content-Type", "application/json")
			in.Header.Set("Authorization", "Bearer "+aliceToken)
			in.Header[http.CanonicalHeaderKey(tc.header)] = tc.values
			out := httptest.NewRecorder()
			g.ServeHTTP(out, in)
			if out.Code != 400 || provider.Load() != 0 || admitted.Load() != 0 || out.Header().Get("X-Scarlet-Request-ID") != "" {
				t.Fatalf("explicit invalid control became a new execution: status=%d admission=%d provider=%d nonce=%q", out.Code, admitted.Load(), provider.Load(), out.Header().Get("X-Scarlet-Request-ID"))
			}
		})
	}
}

func TestStandaloneRuntimeRejectsAmbiguousEnvelopeHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		values       []string
		status       int
	}{
		{"duplicate credential", "Authorization", []string{"Bearer " + aliceToken, "Bearer " + bobToken}, 401},
		{"duplicate content type", "Content-Type", []string{"application/json", "application/json"}, 415},
		{"conflicting content type", "Content-Type", []string{"application/json", "text/plain"}, 415},
		{"hidden compression", "Content-Encoding", []string{"", "gzip"}, 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, provider, authenticated, _ := gatewayHeaderFixture(t)
			in := httptest.NewRequest("POST", "/v1/execute", strings.NewReader(`{}`))
			in.Header.Set("Content-Type", "application/json")
			in.Header.Set("Authorization", "Bearer "+aliceToken)
			in.Header[http.CanonicalHeaderKey(tc.header)] = tc.values
			out := httptest.NewRecorder()
			g.(*gatewayHandler).runtime.ServeHTTP(out, in)
			if out.Code != tc.status || provider.Load() != 0 || tc.status == 401 && authenticated.Load() != 0 {
				t.Fatalf("standalone runtime accepted an ambiguous envelope: status=%d auth=%d provider=%d", out.Code, authenticated.Load(), provider.Load())
			}
		})
	}
}

func TestGatewayChatOmittedControlsAndExplicitNonceRemainCompatible(t *testing.T) {
	g, provider, _, admitted := gatewayHeaderFixture(t)
	body := `{"model":"enterprise","messages":[{"role":"user","content":"hello"}]}`
	nonce := strings.Repeat("v", 24)
	for i, id := range []string{"", nonce, nonce} {
		out := gatewayRequest(g, "/v1/chat/completions", aliceToken, body, id)
		want := 200
		if i == 2 {
			want = 409
		}
		if out.Code != want || i < 2 && len(out.Header().Get("X-Scarlet-Request-ID")) < 16 || i == 1 && out.Header().Get("X-Scarlet-Request-ID") != nonce {
			t.Fatalf("compatible request or replay result changed: status=%d nonce=%q", out.Code, out.Header().Get("X-Scarlet-Request-ID"))
		}
	}
	if provider.Load() != 2 || admitted.Load() != 2 {
		t.Fatalf("unexpected execution count: admission=%d provider=%d", admitted.Load(), provider.Load())
	}
	// An explicit, parseable expired window stays expired rather than receiving
	// the compatibility default reserved for omitted headers.
	in := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	in.Header.Set("Content-Type", "application/json")
	in.Header.Set("Authorization", "Bearer "+aliceToken)
	in.Header.Set("X-Scarlet-Issued-At", time.Now().Add(-2*time.Minute).Format(time.RFC3339Nano))
	in.Header.Set("X-Scarlet-Expires-At", time.Now().Add(-time.Minute).Format(time.RFC3339Nano))
	out := httptest.NewRecorder()
	g.ServeHTTP(out, in)
	if out.Code != 403 || provider.Load() != 2 || admitted.Load() != 2 {
		t.Fatalf("expired explicit window was renewed: status=%d admission=%d provider=%d", out.Code, admitted.Load(), provider.Load())
	}
}
