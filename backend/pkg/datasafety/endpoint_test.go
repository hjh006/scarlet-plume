package datasafety

import (
	"strings"
	"testing"
)

func TestEndpointEvidenceDoesNotUpgradeObservation(t *testing.T) {
	c := Context{Kind: "endpoint", Channel: "clipboard", Operation: "change", Enforcement: "observed", Capture: "partial", DeviceRef: strings.Repeat("a", 64), Endpoint: &EndpointEvidence{Version: 1, Scope: "observation", Stage: "observed", Executor: "windows_clipboard_listener", OperationRef: strings.Repeat("b", 64)}}
	if c.Validate() != nil {
		t.Fatal("valid observation rejected")
	}
	for _, mutate := range []func(*Context){func(c *Context) { c.Enforcement = "blocked" }, func(c *Context) { c.Endpoint.Scope = "native_control" }, func(c *Context) { c.Channel = "usb" }, func(c *Context) { c.Endpoint.PolicyID = 1 }, func(c *Context) { c.PolicyDigest = strings.Repeat("c", 64) }} {
		probe := c
		e := *c.Endpoint
		probe.Endpoint = &e
		mutate(&probe)
		if probe.Validate() == nil {
			t.Fatal("observation upgraded or scope disguised")
		}
	}
}
