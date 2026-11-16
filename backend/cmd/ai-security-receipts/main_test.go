package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReceiptOriginRestrictsCredentialsAndCleartext(t *testing.T) {
	for _, value := range []string{"http://example.com", "https://user:password@example.com", "https://example.com?token=secret", "https://example.com/path", "https://example.com/#fragment"} {
		if _, err := receiptURL(value, true); err == nil {
			t.Fatalf("accepted invalid origin %q", value)
		}
	}
	if _, err := receiptURL("http://127.0.0.1:9000", false); err == nil {
		t.Fatal("loopback cleartext silently allowed")
	}
	if got, err := receiptURL("http://127.0.0.1:9000", true); err != nil || got != "http://127.0.0.1:9000"+receiptPath {
		t.Fatal("local fixture not supported")
	}
}

func TestReceiptForwardingNeverSendsUnknownContentAndDoesNotFollowRedirect(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer spai_"+strings.Repeat("a", 43) {
			t.Error("missing independent source authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"replayed":false}`))
	}))
	defer upstream.Close()
	token := "spai_" + strings.Repeat("a", 43)
	if _, err := forwardReceipts(context.Background(), strings.NewReader(`{"input":"customer secret"}`), upstream.Client(), upstream.URL, token); err == nil || calls != 0 {
		t.Fatal("unknown raw content reached platform")
	}
	if count, err := forwardReceipts(context.Background(), strings.NewReader(`{"eventId":"test"}`), upstream.Client(), upstream.URL, token); err != nil || count != 1 || calls != 1 {
		t.Fatalf("receipt not acknowledged %d %v", count, err)
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, upstream.URL, 302) }))
	defer redirect.Close()
	client := redirect.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if _, err := forwardReceipts(context.Background(), strings.NewReader(`{"eventId":"test"}`), client, redirect.URL, token); err == nil || calls != 1 {
		t.Fatal("redirect exposed source token or falsely acknowledged receipt")
	}
}
