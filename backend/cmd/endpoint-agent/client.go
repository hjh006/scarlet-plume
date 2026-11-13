package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/endpointguard"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
)

type endpointClient struct {
	origin, token, device, subject, scope string
	key                                   []byte
	http                                  *http.Client
	clockMu                               sync.RWMutex
	platformTime, receivedAt              time.Time
	nativeCapabilities                    func() []channelCapability
	nativeService                         bool
}
type endpointHTTPError struct{ status int }

func (endpointHTTPError) Error() string {
	return "platform rejected source, policy revision or metadata"
}
func sourceAuthorizationRejected(err error) bool {
	var rejected endpointHTTPError
	return errors.As(err, &rejected) && (rejected.status == http.StatusUnauthorized || rejected.status == http.StatusForbidden)
}

type channelCapability struct {
	Channel      string   `json:"channel"`
	Scope        string   `json:"scope"`
	Supported    bool     `json:"supported"`
	Modes        []string `json:"modes"`
	Executor     string   `json:"executor"`
	Reason       string   `json:"reason"`
	TargetScope  string   `json:"targetScope,omitempty"`
	TargetRefs   []string `json:"targetRefs,omitempty"`
	ConfigDigest string   `json:"configDigest,omitempty"`
	Privilege    string   `json:"privilege,omitempty"`
}
type activePolicy struct {
	ID         int64      `json:"id"`
	Revision   int64      `json:"revision"`
	DeviceRef  string     `json:"deviceRef"`
	Channel    string     `json:"channel"`
	Mode       string     `json:"mode"`
	Scope      string     `json:"scope"`
	Digest     string     `json:"digest"`
	Categories []string   `json:"categories"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	ApprovedAt *time.Time `json:"approvedAt,omitempty"`
	Executor   string     `json:"executor"`
}
type policyBundle struct {
	DeviceRef      string                        `json:"deviceRef"`
	IssuedAt       time.Time                     `json:"issuedAt"`
	Policies       []activePolicy                `json:"policies"`
	NativeCommands []store.EndpointNativeCommand `json:"nativeCommands"`
}

func newEndpointClient(origin string, local bool) (*endpointClient, error) {
	return newEndpointClientValues(origin, local, os.Getenv("SCARLET_OPERATIONS_TOKEN"), os.Getenv("SCARLET_ENDPOINT_KEY_HEX"), os.Getenv("SCARLET_ENDPOINT_SCOPE"), os.Getenv("SCARLET_ENDPOINT_DEVICE_ID"), os.Getenv("SCARLET_ENDPOINT_SUBJECT_ID"))
}
func newEndpointClientValues(origin string, local bool, token, keyHex, scope, device, subject string) (*endpointClient, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || u.Scheme != "https" && (u.Scheme != "http" || !local || !(u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())) {
		return nil, errors.New("fixed HTTPS platform origin required")
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 || len(token) < 1 || len(token) > 1024 || strings.ContainsAny(token, " \r\n\t") || len(scope) < 1 || len(scope) > 80 || len(device) < 1 || len(device) > 256 || len(subject) > 256 {
		return nil, errors.New("source token, HMAC key, scope and explicit local device ID required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	c := &endpointClient{origin: strings.TrimRight(origin, "/"), token: token, key: key, scope: scope, http: &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	c.device = c.ref("device", device)
	if subject != "" {
		c.subject = c.ref("subject", subject)
	}
	return c, nil
}
func (c *endpointClient) ref(kind, value string) string {
	m := hmac.New(sha256.New, c.key)
	_, _ = m.Write([]byte(c.scope + "\x00" + kind + "\x00" + value))
	return hex.EncodeToString(m.Sum(nil))
}

func (c *endpointClient) observationRef(stream, channel, kind, sequence, target string) string {
	return c.ref("observation", strings.Join([]string{stream, channel, kind, sequence, c.ref("observed-object", target)}, "\x00"))
}

// now advances the authenticated platform time using monotonic elapsed
// local time. Before an authenticated policy bundle is accepted (heartbeat and
// observer-only mode), the host must still keep its wall clock synchronized.
func (c *endpointClient) now() time.Time {
	c.clockMu.RLock()
	base, received := c.platformTime, c.receivedAt
	c.clockMu.RUnlock()
	if received.IsZero() {
		return time.Now().UTC()
	}
	return base.Add(time.Since(received)).UTC()
}
func (c *endpointClient) request(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	var err error
	if in != nil {
		body, err = json.Marshal(in)
		if err != nil {
			return errors.New("metadata encoding failed")
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, c.origin+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("metadata request failed")
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(r)
	if err != nil {
		return errors.New("platform unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return endpointHTTPError{status: response.StatusCode}
	}
	if out != nil {
		raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20+1))
		if err != nil || len(raw) > 2<<20 || json.Unmarshal(raw, out) != nil {
			return errors.New("invalid bounded platform response")
		}
	}
	return nil
}
func capabilities() []channelCapability {
	result := []channelCapability{}
	if runtime.GOOS != "windows" {
		return []channelCapability{{Channel: "usb", Scope: "controlled_operations", Supported: false, Modes: []string{}, Executor: "usb_copy_guard", Reason: "unsupported_os"}}
	}
	for _, channel := range []string{"usb", "print", "clipboard"} {
		obs := map[string]string{"usb": "windows_usb_volume", "print": "windows_print_spool", "clipboard": "windows_clipboard_listener"}[channel]
		executor := map[string]string{"usb": "usb_copy_guard", "print": "windows_print_gdi", "clipboard": "windows_clipboard_write"}[channel]
		supported := runtime.GOOS == "windows"
		result = append(result, channelCapability{Channel: channel, Scope: "observation", Supported: supported, Modes: []string{"monitor"}, Executor: obs, Reason: "windows_observer_available"}, channelCapability{Channel: channel, Scope: "controlled_operations", Supported: supported, Modes: []string{"monitor", "block_sensitive", "block_all"}, Executor: executor, Reason: "explicit_mediated_operation_only"})
	}
	return result
}
func (c *endpointClient) heartbeat(ctx context.Context) error {
	caps := capabilities()
	if c.nativeService {
		caps = []channelCapability{}
	}
	if c.nativeCapabilities != nil {
		caps = append(caps, c.nativeCapabilities()...)
	}
	return c.request(ctx, "POST", "/api/v1/endpoint/heartbeat", map[string]any{"deviceRef": c.device, "os": runtime.GOOS, "agentVersion": "endpoint-agent-v2", "observedAt": time.Now().UTC(), "capabilities": caps}, nil)
}
func (c *endpointClient) policy(ctx context.Context, id int64) (activePolicy, error) {
	var bundle policyBundle
	err := c.request(ctx, "GET", "/api/v1/endpoint/policy?deviceRef="+c.device, nil, &bundle)
	received := time.Now()
	if err != nil {
		return activePolicy{}, err
	}
	// Match the server heartbeat's bounded clock-skew allowance. The platform
	// timestamp is trusted only from this authenticated, device-bound response.
	oldest, latest := received.Add(-5*time.Minute), received.Add(5*time.Minute)
	if bundle.DeviceRef != c.device || bundle.IssuedAt.Before(oldest) || bundle.IssuedAt.After(latest) {
		return activePolicy{}, errors.New("fresh device-bound policy bundle required")
	}
	base := bundle.IssuedAt
	for _, p := range bundle.Policies {
		if p.ApprovedAt != nil && p.ApprovedAt.After(base) {
			base = *p.ApprovedAt
		}
	}
	if base.After(latest) {
		return activePolicy{}, errors.New("fresh device-bound policy bundle required")
	}
	for _, p := range bundle.Policies {
		if p.ID == id && p.DeviceRef == c.device && p.ApprovedAt != nil && !p.ApprovedAt.IsZero() && p.ExpiresAt.After(base) && p.Scope == "controlled_operations" {
			c.clockMu.Lock()
			c.platformTime, c.receivedAt = base.UTC(), received
			c.clockMu.Unlock()
			return p, nil
		}
	}
	return activePolicy{}, errors.New("approved active policy not available for this device")
}
func (c *endpointClient) receipt(ctx context.Context, p activePolicy) error {
	at := c.now()
	return c.request(ctx, "POST", "/api/v1/endpoint/receipts", map[string]any{"receiptRef": c.ref("receipt", p.Digest+at.Format(time.RFC3339Nano)), "deviceRef": c.device, "policyId": p.ID, "policyRevision": p.Revision, "policyDigest": p.Digest, "channel": p.Channel, "scope": p.Scope, "executor": p.Executor, "status": "applied", "code": "policy_loaded", "observedAt": at}, nil)
}
func (c *endpointClient) guardPolicy(p activePolicy) endpointguard.Policy {
	// Add keeps the local monotonic clock component. The guard's before/after
	// audit expiry checks must not compare a server wall-clock expiry directly
	// with a slow host wall clock or extend its TTL after a host clock change.
	deadline := time.Now().Add(p.ExpiresAt.Sub(c.now()))
	return endpointguard.Policy{ID: p.ID, Revision: p.Revision, DeviceRef: p.DeviceRef, Channel: p.Channel, Mode: p.Mode, Scope: p.Scope, Digest: p.Digest, Categories: p.Categories, ExpiresAt: deadline}
}
func (c *endpointClient) event(operationRef string, fact datasafety.Context) store.SecurityEventInput {
	severity, rule, outcome := "low", "endpoint:activity", "activity"
	if fact.Enforcement == "blocked" {
		severity, rule, outcome = "high", "endpoint:blocked", "observed"
	} else if fact.Enforcement == "failed" || fact.Enforcement == "unknown" {
		severity, rule, outcome = "medium", "endpoint:incomplete", "observed"
	}
	stage := fact.Endpoint.Stage
	return store.SecurityEventInput{DataContext: &fact, EventID: "endpoint:" + c.ref("event", operationRef+":"+stage), ObservedAt: c.now(), Outcome: outcome, Category: "data_leak", Rule: rule, Severity: severity, Count: 1, WindowSeconds: 1}
}
