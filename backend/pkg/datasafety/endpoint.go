package datasafety

import "slices"

// EndpointEvidence describes the actual interception point. It is a source
// receipt, never proof that every application on a device was intercepted.
type EndpointEvidence struct {
	Version        int    `json:"version"`
	PolicyID       int64  `json:"policyId,omitempty"`
	PolicyRevision int64  `json:"policyRevision,omitempty"`
	Scope          string `json:"scope"`
	Stage          string `json:"stage"`
	Executor       string `json:"executor"`
	OperationRef   string `json:"operationRef"`
}

func (e EndpointEvidence) valid(c Context) bool {
	if e.Version != 1 || c.Kind != "endpoint" || c.DeviceRef == "" || !digestPattern.MatchString(e.OperationRef) || !slices.Contains([]string{"usb", "print", "clipboard"}, c.Channel) {
		return false
	}
	executors := map[string][]string{
		"usb":       {"usb_copy_guard", "windows_usb_volume"},
		"print":     {"windows_print_gdi", "windows_print_spool"},
		"clipboard": {"windows_clipboard_write", "windows_clipboard_listener"},
	}
	if !slices.Contains(executors[c.Channel], e.Executor) {
		return false
	}
	if e.Scope == "observation" {
		return e.PolicyID == 0 && e.PolicyRevision == 0 && c.PolicyDigest == "" && e.Stage == "observed" && (c.Enforcement == "observed" || c.Enforcement == "unknown")
	}
	if e.Scope != "controlled_operations" || e.PolicyID < 1 || e.PolicyRevision < 1 || !digestPattern.MatchString(c.PolicyDigest) {
		return false
	}
	switch e.Stage {
	case "preflight":
		return c.Enforcement == "blocked" || c.Enforcement == "unknown"
	case "completed":
		return c.Enforcement == "allowed"
	case "observed":
		return c.Enforcement == "observed"
	case "post_action":
		return c.Enforcement == "failed"
	}
	return false
}
