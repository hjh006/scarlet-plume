// data-transfer-guard protects a single explicitly requested bounded text copy.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
)

func main() {
	policyFile := flag.String("policy", "", "explicit local JSON policy")
	from := flag.String("from-root", "", "authorized source directory")
	to := flag.String("to-root", "", "authorized destination directory")
	source := flag.String("source", "", "relative text file")
	destination := flag.String("destination", "", "relative destination, never overwrite")
	eventID := flag.String("event-id", "", "idempotent source event ID")
	subjectRef := flag.String("subject-ref", "", "optional source-side HMAC subject reference")
	deviceRef := flag.String("device-ref", "", "optional source-side HMAC device reference")
	flag.Parse()
	if flag.NArg() != 0 || *eventID == "" {
		fmt.Fprintln(os.Stderr, "provide explicit roots, relative paths, policy and event-id")
		os.Exit(2)
	}
	identity := datasafety.Context{Kind: "endpoint", Channel: "file_copy", Operation: "copy", Enforcement: "unknown", Capture: "unknown", SubjectRef: *subjectRef, DeviceRef: *deviceRef}
	if identity.Validate() != nil {
		fmt.Fprintln(os.Stderr, "provide valid source-side identity references")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*policyFile)
	if err != nil || len(raw) > 16<<10 {
		fmt.Fprintln(os.Stderr, "policy unavailable")
		os.Exit(2)
	}
	policy, policyErr := datasafety.ParsePolicy(raw)
	if policyErr != nil {
		fmt.Fprintln(os.Stderr, "invalid policy")
		os.Exit(2)
	}
	context, decision, err := datasafety.CopyText(policy, *from, *to, *source, *destination)
	context.SubjectRef, context.DeviceRef = *subjectRef, *deviceRef
	if err != nil && !errors.Is(err, datasafety.ErrBlocked) && !errors.Is(err, datasafety.ErrUnknown) {
		fmt.Fprintln(os.Stderr, "copy not completed")
		os.Exit(2)
	}
	severity := "low"
	rule := "dlp:allowed"
	if decision.Outcome == "blocked" {
		severity = "high"
		rule = "dlp:blocked"
	} else if decision.Outcome == "unknown" {
		severity = "medium"
		rule = "dlp:unknown"
	}
	event := store.SecurityEventInput{DataContext: &context, EventID: *eventID, ObservedAt: time.Now().UTC(), Outcome: "observed", Category: "data_leak", Rule: rule, Severity: severity, Count: 1, WindowSeconds: 1}
	if decision.Outcome == "allowed" {
		event.Outcome = "activity"
	}
	_ = json.NewEncoder(os.Stdout).Encode(event)
	if err != nil {
		os.Exit(1)
	}
}
