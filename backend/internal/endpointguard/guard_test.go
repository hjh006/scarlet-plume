package endpointguard

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
)

func TestEndpointPreventionAndAuditOrdering(t *testing.T) {
	for _, channel := range []string{"usb", "print", "clipboard"} {
		t.Run(channel, func(t *testing.T) {
			p := Policy{ID: 1, Revision: 2, DeviceRef: strings.Repeat("a", 64), Channel: channel, Mode: "block_sensitive", Scope: "controlled_operations", Digest: strings.Repeat("b", 64), Categories: []string{"email"}, ExpiresAt: time.Now().Add(time.Hour)}
			who := Identity{DeviceRef: p.DeviceRef, OperationRef: strings.Repeat("c", 64)}
			calls := 0
			stages := []string{}
			audit := func(c datasafety.Context) error {
				if c.Validate() != nil {
					t.Fatal("invalid generated context")
				}
				stages = append(stages, c.Endpoint.Stage)
				return nil
			}
			action := func(raw []byte) error {
				calls++
				if len(stages) != 1 || stages[0] != "observed" || string(raw) != "public text" {
					t.Fatal("wrong bytes or missing pre-action audit")
				}
				return nil
			}
			c, err := Run(p, who, "txt", []byte("private@example.test"), audit, action)
			if !errors.Is(err, ErrBlocked) || calls != 0 || c.Endpoint.Stage != "preflight" {
				t.Fatal("sensitive operation was not prevented")
			}
			stages = nil
			c, err = Run(p, who, "txt", []byte("public text"), audit, action)
			if err != nil || calls != 1 || c.Enforcement != "allowed" || strings.Join(stages, ",") != "observed,completed" {
				t.Fatal("allowed operation evidence incomplete")
			}
			stages = nil
			_, err = Run(p, who, "txt", []byte("public text"), func(datasafety.Context) error { return errors.New("offline") }, action)
			if !errors.Is(err, ErrAudit) || calls != 1 {
				t.Fatal("audit failure allowed a side effect")
			}
			for _, raw := range [][]byte{[]byte("%PDF-1.7"), {0xff}, []byte(strings.Repeat("x", (1<<20)+1))} {
				_, err = Run(p, who, "txt", raw, audit, action)
				if !errors.Is(err, ErrUnknown) || calls != 1 {
					t.Fatal("unknown content was allowed")
				}
			}
			p.Mode = "block_all"
			_, err = Run(p, who, "txt", nil, audit, action)
			if !errors.Is(err, ErrBlocked) || calls != 1 {
				t.Fatal("block_all failed")
			}
			p.ExpiresAt = time.Now().Add(-time.Second)
			_, err = Run(p, who, "txt", nil, audit, action)
			if !errors.Is(err, ErrPolicy) || calls != 1 {
				t.Fatal("expired policy ran")
			}
		})
	}
}

func TestEndpointFinalAuditFailureDoesNotReexecute(t *testing.T) {
	p := Policy{ID: 1, Revision: 1, DeviceRef: strings.Repeat("a", 64), Channel: "print", Mode: "monitor", Scope: "controlled_operations", Digest: strings.Repeat("b", 64), ExpiresAt: time.Now().Add(time.Hour)}
	count := 0
	c, err := Run(p, Identity{DeviceRef: p.DeviceRef, OperationRef: strings.Repeat("c", 64)}, "txt", []byte("public text"), func(c datasafety.Context) error {
		if c.Endpoint.Stage == "completed" {
			return errors.New("offline")
		}
		return nil
	}, func([]byte) error { count++; return nil })
	if !errors.Is(err, ErrAuditPending) || count != 1 || c.Enforcement != "allowed" {
		t.Fatal("completed operation was reported as failed")
	}
}
