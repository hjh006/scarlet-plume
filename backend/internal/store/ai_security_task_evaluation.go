package store

import (
	"slices"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

// aiEngineTaskCandidate supplies synthetic trusted context only to the pure
// configuration regression gate. It does not create, authorize or execute a task.
// Without this fixture a requireTask policy cannot be published before its first
// real task exists. Actual API evaluation and execution never call this helper.
func aiEngineTaskCandidate(cfg aisecurity.Config, r aisecurity.Request, now time.Time) aisecurity.Request {
	if r.TaskID != "" {
		return r
	}
	required := false
	var app aisecurity.Application
	for _, a := range cfg.Applications {
		if a.ID == r.ApplicationID {
			app = a
			required = a.RequireTask
			break
		}
	}
	purposes := []string{}
	for _, doc := range cfg.Documents {
		used := slices.Contains(r.DocumentIDs, doc.ID) || r.Kind == "model" && slices.Contains(app.DocumentIDs, doc.ID) && doc.Enabled && slices.Contains(doc.AllowedPrincipalIDs, r.PrincipalID)
		if used && doc.UsePolicy != nil {
			required = true
			purposes = append(purposes, doc.UsePolicy.AllowedPurposes...)
		}
	}
	if !required {
		return r
	}
	if len(purposes) == 0 {
		purposes = []string{"configuration-evaluation"}
	}
	slices.Sort(purposes)
	purposes = slices.Compact(purposes)
	var first aisecurity.Request
	for _, purpose := range purposes {
		candidate := r
		candidate.TaskID = "configuration-gate-task"
		expires := now.Add(60 * time.Second)
		// This namespaced synthetic epoch is only a pure-engine fixture. No
		// controller lease, credential, admission or runtime proof is created.
		lease := gatewayDigest(struct{ Boundary, Catalog, Purpose string }{"synthetic-configuration-gate-task-lease", aisecurity.ConfigDigest(cfg), purpose})
		candidate, err := aisecurity.ApplyTaskGrant(candidate, gatewaygovernance.TaskGrant{Allowed: true, Required: true, TaskID: candidate.TaskID, Purpose: purpose, ScopeDigest: aisecurity.ConfigDigest(cfg), LeaseChainDigest: lease, LeaseProtocolVersion: gatewaygovernance.TaskLeaseProtocolVersion, ExpiresAt: &expires})
		if err != nil {
			continue
		}
		if first.TaskID == "" {
			first = candidate
		}
		d := aisecurity.Evaluate(cfg, candidate, now)
		if d.Outcome == "allow" || d.RequiresApproval && d.Code == "tool_approval_required_or_mismatched" {
			return candidate
		}
	}
	if first.TaskID != "" {
		return first
	}
	return r
}
