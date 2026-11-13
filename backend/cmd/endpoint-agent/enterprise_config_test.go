package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestEnterpriseTemplateRequiresExplicitBoundedLocalTargets(t *testing.T) {
	base := `{"version":1,"platformOrigin":"https://example.test","scope":"test","deviceId":"synthetic","printQueues":["Lab Queue"]}`
	if _, err := parseEnterpriseTemplate([]byte(base)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		strings.Replace(base, `["Lab Queue"]`, `[]`, 1),
		strings.Replace(base, `"Lab Queue"`, `"Lab Queue","lab queue"`, 1),
		strings.Replace(base, `"Lab Queue"`, `"\\\\server\\queue"`, 1),
		strings.Replace(base, `"Lab Queue"`, `" Lab Queue"`, 1),
		strings.Replace(base, `"version":1`, `"version":2`, 1),
		strings.Replace(base, `"version":1`, `"sourceToken":"do-not-accept-in-public-template","version":1`, 1),
		base + ` {}`, strings.Repeat(" ", 32<<10) + base,
	} {
		if _, err := parseEnterpriseTemplate([]byte(raw)); err == nil {
			t.Fatal("unsafe or ambiguous template accepted")
		}
	}
}
func TestEnterpriseProjectionUsesStablePrivateTargetReferences(t *testing.T) {
	cfg := enterpriseConfig{enterpriseTemplate: enterpriseTemplate{Version: 1, PlatformOrigin: "https://example.test", Scope: "synthetic", DeviceID: "private-device", USBWriteProtect: true, PrintQueues: []string{"Private Finance Queue"}, RDPClipboardRedirection: true}, SourceToken: "synthetic-token", KeyHex: strings.Repeat("a", 64)}
	c, err := cfg.client()
	if err != nil {
		t.Fatal(err)
	}
	first, err := cfg.targets(c)
	if err != nil || len(first) != 3 {
		t.Fatal("missing explicit targets")
	}
	second, _ := cfg.targets(c)
	for i, target := range first {
		if len(target.TargetRef) != 64 || len(target.ConfigDigest) != 64 || target.TargetRef != second[i].TargetRef || target.ConfigDigest != second[i].ConfigDigest {
			t.Fatal("unstable target binding")
		}
	}
	var caps []channelCapability
	for _, target := range first {
		caps = append(caps, channelCapability{TargetRefs: []string{target.TargetRef}, ConfigDigest: target.ConfigDigest})
	}
	raw, _ := json.Marshal(caps)
	for _, private := range []string{cfg.DeviceID, cfg.SourceToken, cfg.KeyHex, cfg.PrintQueues[0]} {
		if strings.Contains(string(raw), private) {
			t.Fatal("projection exposed local private values")
		}
	}
	cfg.DeviceID = "another-private-device"
	other, _ := cfg.client()
	changed, _ := cfg.targets(other)
	if changed[0].ConfigDigest == first[0].ConfigDigest {
		t.Fatal("configuration digest lost its device binding")
	}
}
func TestEndpointAuthorizationLossDistinguishedFromOfflineAndValidation(t *testing.T) {
	if sourceAuthorizationRejected(errors.New("offline")) || sourceAuthorizationRejected(endpointHTTPError{400}) {
		t.Fatal("ordinary errors treated as revoked authority")
	}
	for _, status := range []int{401, 403} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("private upstream response"))
		}))
		c, err := newEndpointClientValues(server.URL, true, "synthetic", strings.Repeat("a", 64), "test", "device", "")
		if err != nil {
			t.Fatal(err)
		}
		err = c.request(t.Context(), "GET", "/endpoint", nil, nil)
		server.Close()
		if !sourceAuthorizationRejected(err) || strings.Contains(err.Error(), "private upstream") {
			t.Fatal("authorization rejection missing or response leaked")
		}
	}
}
func TestNativeReplayPreservesPhaseOrderWithoutNativeActions(t *testing.T) {
	e, d, cmd, _, _ := nativeFixture(t)
	if err := e.Handle(t.Context(), cmd); err != nil {
		t.Fatal(err)
	}
	r := e.entries[cmd.LeaseRef]
	for key := range r.journal.Acknowledged {
		r.journal.Acknowledged[key] = false
	}
	// Identical observation timestamps still require preparation before readback.
	for key, receipt := range r.journal.Receipts {
		receipt.ObservedAt = time.Unix(1, 0)
		r.journal.Receipts[key] = receipt
	}
	var phases []string
	e.report = func(_ context.Context, r store.EndpointReceiptInput) error {
		phases = append(phases, r.Phase)
		return nil
	}
	if err := e.replayReceipts(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(phases, ",") != "prepared,configured,readback" || d.applied != 1 {
		t.Fatal("receipt replay reordered phases or repeated native writes")
	}
}
