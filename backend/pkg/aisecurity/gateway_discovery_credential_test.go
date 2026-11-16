package aisecurity

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

func TestMCPTaskDiscoveryCarriesAuthenticatedCredential(t *testing.T) {
	c := fixture()
	c.Applications[0].RequireTask = true
	c.Tools[0].HighImpact = false
	var provider, taskChecks, discoveryChecks atomic.Int32
	g := mcpFacadeFixture(t, &c, &provider).(*gatewayHandler)
	tasks := newTestTaskController()
	g.runtime.options.TaskControl = func(ctx context.Context, op TaskOperation) (gatewaygovernance.TaskGrant, error) {
		got, _ := ctx.Value(gatewayCredentialHashKey{}).(string)
		if got != tokenHash(aliceToken) {
			return gatewaygovernance.TaskGrant{}, errors.New("authenticated credential missing from task context")
		}
		taskChecks.Add(1)
		return tasks.call(ctx, op)
	}
	g.discoverTools = func(ctx context.Context, identity Identity, credential, _ string) ([]string, error) {
		got, _ := ctx.Value(gatewayCredentialHashKey{}).(string)
		if credential != tokenHash(aliceToken) || got != credential || identity != (Identity{ApplicationID: "app", PrincipalID: "alice"}) {
			return nil, errors.New("discovery identity and frozen credential differ")
		}
		discoveryChecks.Add(1)
		return []string{"ticket"}, nil
	}
	for i, method := range []string{"server/discover", "tools/list", "tools/call"} {
		var params map[string]any
		if method == "tools/call" {
			params = map[string]any{"name": "close-ticket", "arguments": map[string]string{"ticketId": "ticket-17", "resolution": "fixed"}}
		}
		out := mcpFacadeRequest(g, aliceToken, method, i+1, params, func(r *http.Request) {
			r.Header.Set("X-Scarlet-Task-Id", "task-one")
			r.Header.Set("X-Scarlet-Credential-Hash", strings.Repeat("f", 64))
		})
		if out.Code != 200 {
			t.Fatalf("valid task-bound %s rejected: status=%d body=%s", method, out.Code, out.Body.String())
		}
		if method != "tools/call" && provider.Load() != 0 {
			t.Fatal("discovery executed a tool")
		}
	}
	if provider.Load() != 1 || taskChecks.Load() < 3 || discoveryChecks.Load() < 3 {
		t.Fatalf("missing live discovery/execution checks: task=%d discovery=%d provider=%d", taskChecks.Load(), discoveryChecks.Load(), provider.Load())
	}
}

func TestMCPMissingRequiredTaskPrecedesRemoteDiscovery(t *testing.T) {
	c := fixture()
	c.Applications[0].RequireTask = true
	var provider, remote atomic.Int32
	g := mcpFacadeFixture(t, &c, &provider).(*gatewayHandler)
	g.discoverTools = func(context.Context, Identity, string, string) ([]string, error) {
		remote.Add(1)
		return nil, errors.New("synthetic unavailable discovery")
	}
	out := mcpFacadeRequest(g, aliceToken, "tools/list", 1, nil, nil)
	if out.Code != 403 || !strings.Contains(out.Body.String(), "Task required") || remote.Load() != 0 || provider.Load() != 0 {
		t.Fatalf("missing task was treated as a remote service failure: status=%d remote=%d provider=%d", out.Code, remote.Load(), provider.Load())
	}
	out = mcpFacadeRequest(g, aliceToken, "tools/list", 2, nil, func(r *http.Request) { r.Header.Set("X-Scarlet-Task-Id", "task-one") })
	if out.Code != 503 || remote.Load() != 1 || provider.Load() != 0 {
		t.Fatalf("actual remote failure lost its status: status=%d remote=%d provider=%d", out.Code, remote.Load(), provider.Load())
	}
}
