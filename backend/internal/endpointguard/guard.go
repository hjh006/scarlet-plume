// Package endpointguard protects explicitly mediated endpoint operations.
// Native observers do not call this package to undo already completed actions.
package endpointguard

import (
	"bytes"
	"errors"
	"slices"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
)

var (
	ErrBlocked      = errors.New("endpoint operation prevented by policy")
	ErrUnknown      = errors.New("endpoint operation prevented: inspection incomplete")
	ErrPolicy       = errors.New("current approved endpoint policy required")
	ErrAudit        = errors.New("endpoint operation not started: audit unavailable")
	ErrAction       = errors.New("endpoint operation failed; partial side effects may exist")
	ErrAuditPending = errors.New("endpoint operation completed; final audit pending, do not repeat")
)

// Policy is the bounded active policy returned by the platform, not a local
// override. The caller must fetch it and acknowledge the exact revision first.
type Policy struct {
	ID, Revision                            int64
	DeviceRef, Channel, Mode, Scope, Digest string
	Categories                              []string
	ExpiresAt                               time.Time
}

type Identity struct{ DeviceRef, SubjectRef, OperationRef string }

// Run audits before any side effect and passes the exact inspected bytes to
// the native operation. Blocked/unknown attempts never call action. A completion
// audit failure is deliberately distinct from an action failure.
func Run(p Policy, who Identity, format string, raw []byte, audit func(datasafety.Context) error, action func([]byte) error) (datasafety.Context, error) {
	executor := map[string]string{"usb": "usb_copy_guard", "print": "windows_print_gdi", "clipboard": "windows_clipboard_write"}[p.Channel]
	if p.ID < 1 || p.Revision < 1 || p.Scope != "controlled_operations" || p.DeviceRef != who.DeviceRef || !p.ExpiresAt.After(time.Now()) || !slices.Contains([]string{"monitor", "block_sensitive", "block_all"}, p.Mode) || executor == "" || audit == nil || action == nil {
		return datasafety.Context{}, ErrPolicy
	}
	c, _, inspectErr := datasafety.InspectFileText(bytes.NewReader(raw), format)
	if inspectErr != nil || len(raw) > 1<<20 {
		c.Capture = "unknown"
		c.Categories = []string{}
	}
	c.Channel = p.Channel
	c.DeviceRef = who.DeviceRef
	c.SubjectRef = who.SubjectRef
	c.PolicyDigest = p.Digest
	c.Endpoint = &datasafety.EndpointEvidence{Version: 1, PolicyID: p.ID, PolicyRevision: p.Revision, Scope: p.Scope, Stage: "observed", Executor: executor, OperationRef: who.OperationRef}
	c.Enforcement = "observed"
	if c.Validate() != nil {
		return c, ErrPolicy
	}
	policy := datasafety.Policy{Version: "endpoint-channel-v1", ProtectedCategories: p.Categories}
	if policy.Validate() != nil {
		return c, ErrPolicy
	}
	prevented := error(nil)
	if len(raw) > 1<<20 {
		c.Enforcement = "unknown"
		prevented = ErrUnknown
	}
	if p.Mode == "block_all" {
		c.Enforcement = "blocked"
		prevented = ErrBlocked
	} else if p.Mode == "block_sensitive" {
		if c.Capture != "complete" || !datasafety.SupportsTextPolicy(policy) {
			c.Enforcement = "unknown"
			prevented = ErrUnknown
		} else {
			for _, category := range c.Categories {
				if slices.Contains(p.Categories, category) {
					c.Enforcement = "blocked"
					prevented = ErrBlocked
					break
				}
			}
		}
	}
	if prevented != nil {
		c.Endpoint.Stage = "preflight"
		// A denied operation stays denied even when the evidence transport fails.
		_ = audit(c)
		return c, prevented
	}
	if audit(c) != nil {
		return c, ErrAudit
	}
	// Recheck expiry after a potentially slow synchronous audit delivery.
	if !p.ExpiresAt.After(time.Now()) {
		return c, ErrPolicy
	}
	if action(raw) != nil {
		c.Enforcement = "failed"
		c.Endpoint.Stage = "post_action"
		_ = audit(c)
		return c, ErrAction
	}
	c.Enforcement = "allowed"
	c.Endpoint.Stage = "completed"
	if audit(c) != nil {
		return c, ErrAuditPending
	}
	return c, nil
}
