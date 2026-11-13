package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
)

const aiEventsPath = "/api/v1/operations/security-events"

type aiEgress struct {
	policy      datasafety.Policy
	destination string
	routes      []routeTemplate
	endpoint    string
	token       string
	client      *http.Client
	slots       chan struct{}
}

func loadDataPolicy(path string) (datasafety.Policy, error) {
	var policy datasafety.Policy
	f, err := os.Open(path)
	if err != nil {
		return policy, errors.New("data policy unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 {
		return policy, errors.New("data policy exceeds its source-side budget")
	}
	return datasafety.ParsePolicy(raw)
}

func newAIEgress(upstream *url.URL, platform, token string, local bool, policy datasafety.Policy, routes []routeTemplate) (*aiEgress, error) {
	if upstream == nil || upstream.Scheme != "https" || !datasafety.Domain(strings.ToLower(upstream.Hostname())) || policy.Validate() != nil || len(routes) == 0 {
		return nil, errors.New("AI egress requires a fixed HTTPS DNS upstream, explicit routes and a valid data policy")
	}
	if token == "" || len(token) > 1024 || strings.IndexFunc(token, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return nil, errors.New("set a security-events source token in SCARLET_OPERATIONS_TOKEN")
	}
	endpoint, err := receiverEndpoint(platform, local)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(endpoint)
	u.Path = aiEventsPath
	return &aiEgress{policy: policy, destination: strings.ToLower(upstream.Hostname()), routes: routes, endpoint: u.String(), token: token, slots: make(chan struct{}, 8), client: &http.Client{Transport: directTransport(), Timeout: 1500 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// A caller must never be given a redirect that could replay its POST body outside this policy.
// ReverseProxy invokes this before copying response headers; its normal error path returns 502.
func rejectAIEgressRedirect(response *http.Response) error {
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		response.Header.Del("Location")
		return errors.New("AI upstream redirects are not permitted")
	}
	return nil
}

func (a *aiEgress) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := matchRoute(a.routes, r.URL.EscapedPath())
		if r.Method != http.MethodPost || route == "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
			http.Error(w, "AI egress permits only the configured POST JSON routes without a query", http.StatusForbidden)
			return
		}
		select {
		case a.slots <- struct{}{}:
			defer func() { <-a.slots }()
		default:
			http.Error(w, "AI inspection budget unavailable", http.StatusTooManyRequests)
			return
		}

		c := datasafety.Context{Kind: "ai", Channel: "ai_prompt", DestinationDomain: a.destination, Operation: "upload", Enforcement: "unknown", Capture: "unknown", Categories: []string{}}
		var raw []byte
		media, params, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
		charset := strings.ToLower(params["charset"])
		if mediaErr == nil && media == "application/json" && len(r.Header.Values("Content-Type")) == 1 && len(r.Header.Values("Content-Encoding")) <= 1 && (encoding == "" || encoding == "identity") && (charset == "" || charset == "utf-8" || charset == "utf8") && r.Body != nil {
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Second))
			inspected, body, err := datasafety.InspectJSON(r.Body)
			_ = r.Body.Close()
			_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
			if err == nil {
				c.Capture, c.Categories, raw = inspected.Capture, inspected.Categories, body
			}
		}
		if !datasafety.SupportsTextPolicy(a.policy) {
			c.Capture, raw = "unknown", nil
		}
		decision, err := datasafety.Evaluate(a.policy, c)
		if err != nil {
			http.Error(w, "AI policy unavailable", http.StatusServiceUnavailable)
			return
		}
		c.PolicyDigest, c.Enforcement = decision.PolicyDigest, decision.Outcome
		status := http.StatusForbidden
		if decision.Outcome == "unknown" {
			status = http.StatusUnprocessableEntity
		}
		if err := a.record(r.Context(), c, route); err != nil {
			if decision.Outcome == "allowed" {
				http.Error(w, "AI audit unavailable; request was not forwarded", http.StatusServiceUnavailable)
				return
			}
		}
		if decision.Outcome != "allowed" {
			http.Error(w, "AI request was not forwarded by the source-side policy", status)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		r.TransferEncoding = nil
		next.ServeHTTP(w, r)
	})
}

func (a *aiEgress) record(ctx context.Context, c datasafety.Context, route string) error {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("cannot create AI audit identity")
	}
	severity := "low"
	outcome := "activity"
	if c.Enforcement == "blocked" {
		severity = "high"
		outcome = "observed"
	} else if c.Enforcement == "unknown" {
		severity = "medium"
		outcome = "observed"
	}
	event := store.SecurityEventInput{DataContext: &c, EventID: "ai-egress:" + hex.EncodeToString(random[:]), ObservedAt: time.Now().UTC(), Category: "data_leak", Outcome: outcome, Rule: "dlp:ai_egress_" + c.Enforcement, Severity: severity, Method: http.MethodPost, Route: route, Count: 1, WindowSeconds: 1}
	body, err := json.Marshal(event)
	if err != nil {
		return errors.New("cannot encode AI audit projection")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("cannot create AI audit request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	response, err := a.client.Do(req)
	if err != nil {
		return errors.New("AI audit receiver unavailable")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("AI audit was not accepted")
	}
	return nil
}
