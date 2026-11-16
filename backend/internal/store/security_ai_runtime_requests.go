package store

import (
	"slices"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

// AISecurityRuntimeRequest joins only the bounded receipt window, never request content.
type AISecurityRuntimeRequest struct {
	TaskID                 string    `json:"taskId,omitempty"`
	TaskPurpose            string    `json:"taskPurpose,omitempty"`
	TaskScopeDigest        string    `json:"taskScopeDigest,omitempty"`
	SourceDigests          []string  `json:"sourceDigests,omitempty"`
	SourceID               int64     `json:"sourceId"`
	RequestID              string    `json:"requestId"`
	AttemptID              string    `json:"attemptId,omitempty"`
	RequestDigest          string    `json:"requestDigest"`
	CatalogDigest          string    `json:"catalogDigest"`
	ApplicationID          string    `json:"applicationId"`
	PrincipalID            string    `json:"principalId"`
	Kind                   string    `json:"kind"`
	Status                 string    `json:"status"`
	Current                bool      `json:"current"`
	RuntimeCompatibility   string    `json:"runtimeCompatibility"`
	RuntimeProtocolVersion string    `json:"runtimeProtocolVersion,omitempty"`
	InspectionVersion      string    `json:"inspectionVersion,omitempty"`
	MCPProtocolVersion     string    `json:"mcpProtocolVersion,omitempty"`
	FirstObservedAt        time.Time `json:"firstObservedAt"`
	LastObservedAt         time.Time `json:"lastObservedAt"`
	EventIDs               []int64   `json:"eventIds"`
	MissingStages          []string  `json:"missingStages"`
	Issues                 []string  `json:"issues"`
}

func validAIRuntimeVersions(r aisecurity.AuditRecord) bool {
	if r.RuntimeProtocolVersion == "" && r.InspectionVersion == "" && r.MCPProtocolVersion == "" {
		return true // Legacy reports remain historical evidence with an explicit version gap.
	}
	return securityText(r.RuntimeProtocolVersion, 100, true) && securityText(r.InspectionVersion, 100, true) && securityText(r.MCPProtocolVersion, 100, false)
}

func aiRuntimeCompatibility(r aisecurity.AuditRecord) string {
	if r.Decision.MediaInspectionVersion != "" && r.Decision.MediaInspectionVersion != aisecurity.MediaInspectionVersion {
		return "unsupported"
	}
	if r.RuntimeProtocolVersion == "" || r.InspectionVersion == "" {
		return "unversioned"
	}
	if r.RuntimeProtocolVersion != aisecurity.RuntimeProtocolVersion || r.InspectionVersion != aisecurity.InspectionVersion || (r.MCPProtocolVersion != "" && r.MCPProtocolVersion != aisecurity.MCPVersion) {
		return "unsupported"
	}
	return "supported_profile"
}

func validAIRuntimePhase(r aisecurity.AuditRecord) bool {
	x := r.Execution
	if r.Stage == "decision" || r.Stage == "prepared" {
		return !x.Attempted && x.Outcome == "not_attempted" && x.HTTPStatus == 0 && (r.Stage != "prepared" || r.Decision.Outcome == "allow")
	}
	if r.Stage != "completed" {
		return false
	}
	switch x.Outcome {
	case "not_attempted":
		return !x.Attempted && x.HTTPStatus == 0 && (r.Decision.Outcome == "deny" || x.CacheHit && r.Decision.Outcome == "allow" && r.Decision.Kind == "model" && r.CacheEvidence != nil && r.CacheEvidence.CacheHit)
	case "succeeded":
		return x.Attempted && r.Decision.Outcome == "allow" && ((r.Decision.Kind == "rag" && x.HTTPStatus == 0) || (x.HTTPStatus >= 200 && x.HTTPStatus < 300))
	case "blocked_output", "failed", "unknown":
		return x.Attempted && r.Decision.Outcome == "deny"
	}
	return false
}

func aiRuntimeRequests(events []AISecurityRuntimeEvent) []AISecurityRuntimeRequest {
	type key struct {
		source                   int64
		request, digest, catalog string
		attempt                  string
	}
	type group struct {
		out    AISecurityRuntimeRequest
		phases map[string]AISecurityRuntimeEvent
	}
	groups := map[key]*group{}
	order := []key{}
	for _, e := range events {
		r := e.Record
		k := key{e.SourceID, r.RequestID, r.Decision.RequestDigest, r.Decision.CatalogDigest, r.AttemptID}
		g := groups[k]
		if g == nil {
			g = &group{out: AISecurityRuntimeRequest{SourceID: e.SourceID, RequestID: r.RequestID, AttemptID: r.AttemptID, RequestDigest: r.Decision.RequestDigest, CatalogDigest: r.Decision.CatalogDigest, ApplicationID: r.Decision.ApplicationID, PrincipalID: r.Decision.PrincipalID, Kind: r.Decision.Kind, Current: e.Current, RuntimeCompatibility: aiRuntimeCompatibility(r), RuntimeProtocolVersion: r.RuntimeProtocolVersion, InspectionVersion: r.InspectionVersion, MCPProtocolVersion: r.MCPProtocolVersion, FirstObservedAt: r.ObservedAt, LastObservedAt: r.ObservedAt, EventIDs: []int64{}, MissingStages: []string{}, Issues: []string{}}, phases: map[string]AISecurityRuntimeEvent{}}
			groups[k] = g
			order = append(order, k)
			g.out.TaskID = r.Decision.TaskID
			g.out.TaskPurpose = r.Decision.TaskPurpose
			g.out.TaskScopeDigest = r.Decision.TaskScopeDigest
		}
		if r.Decision.TaskID != g.out.TaskID || r.Decision.TaskPurpose != g.out.TaskPurpose || r.Decision.TaskScopeDigest != g.out.TaskScopeDigest {
			g.out.Issues = append(g.out.Issues, "task_context_changed_between_phases")
		}
		g.out.SourceDigests = gatewayTaskUnion(g.out.SourceDigests, r.Decision.SourceDigests)
		g.out.EventIDs = append(g.out.EventIDs, e.ID)
		g.out.Current = g.out.Current && e.Current
		if r.ObservedAt.Before(g.out.FirstObservedAt) {
			g.out.FirstObservedAt = r.ObservedAt
		}
		if r.ObservedAt.After(g.out.LastObservedAt) {
			g.out.LastObservedAt = r.ObservedAt
		}
		if r.Decision.ApplicationID != g.out.ApplicationID || r.Decision.PrincipalID != g.out.PrincipalID || r.Decision.Kind != g.out.Kind {
			g.out.Issues = append(g.out.Issues, "identity_or_kind_conflict")
		}
		if r.RuntimeProtocolVersion != g.out.RuntimeProtocolVersion || r.InspectionVersion != g.out.InspectionVersion || r.MCPProtocolVersion != g.out.MCPProtocolVersion {
			g.out.Issues = append(g.out.Issues, "runtime_profile_conflict")
		}
		if previous, exists := g.phases[r.Stage]; exists {
			if previous.Record.Decision.Outcome != r.Decision.Outcome || previous.Record.Decision.Code != r.Decision.Code || previous.Record.Execution != r.Execution {
				g.out.Issues = append(g.out.Issues, "phase_result_conflict")
			} else if r.ObservedAt.Before(previous.Record.ObservedAt) {
				g.phases[r.Stage] = e
			}
		} else {
			g.phases[r.Stage] = e
		}
		if !validAIRuntimePhase(r) {
			g.out.Issues = append(g.out.Issues, "invalid_phase_claim")
		}
	}
	out := make([]AISecurityRuntimeRequest, 0, len(order))
	for _, k := range order {
		g := groups[k]
		d, decision := g.phases["decision"]
		p, prepared := g.phases["prepared"]
		c, completed := g.phases["completed"]
		for _, stage := range []string{"decision", "prepared", "completed"} {
			if _, ok := g.phases[stage]; !ok {
				g.out.MissingStages = append(g.out.MissingStages, stage)
			}
		}
		if decision && d.Record.Decision.Outcome != "allow" && (prepared || completed) {
			g.out.Issues = append(g.out.Issues, "execution_after_denial")
		}
		if (decision && prepared && d.Record.ObservedAt.After(p.Record.ObservedAt)) || (prepared && completed && p.Record.ObservedAt.After(c.Record.ObservedAt)) {
			g.out.Issues = append(g.out.Issues, "source_timestamps_out_of_order")
		}
		slices.Sort(g.out.Issues)
		g.out.Issues = slices.Compact(g.out.Issues)
		switch {
		case g.out.AttemptID == "" && slices.Contains(g.out.Issues, "phase_result_conflict"):
			g.out.Status = "attempts_ambiguous"
		case len(g.out.Issues) > 0:
			g.out.Status = "inconsistent"
		case decision && !prepared && !completed && d.Record.Decision.Outcome != "allow":
			g.out.Status = "policy_" + d.Record.Decision.Outcome
			g.out.MissingStages = []string{}
		case len(g.out.MissingStages) > 0:
			g.out.Status = "incomplete"
		default:
			g.out.Status = "reported_" + c.Record.Execution.Outcome
		}
		out = append(out, g.out)
	}
	return out
}
