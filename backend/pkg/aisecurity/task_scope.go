package aisecurity

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

// TaskContext is supplied by a trusted controller, never decoded from a request.
type TaskContext struct {
	TaskID, Purpose, ScopeDigest                 string
	Categories, SourceDocumentIDs, SourceDigests []string
	Sources                                      []gatewaygovernance.TaskSource
	ExpiresAt                                    time.Time
}

// SourceEvidence binds an actual private source to its reviewed metadata.
type SourceEvidence = gatewaygovernance.TaskSource

// ApplyTaskGrant is for trusted Go callers after a controller lookup. HTTP
// requests can select a task ID but cannot submit this grant or private context.
func ApplyTaskGrant(r Request, g gatewaygovernance.TaskGrant) (Request, error) {
	if !g.Allowed || g.TaskID != r.TaskID {
		return r, errors.New("task scope denied")
	}
	if r.TaskID == "" {
		if g.Required {
			return r, errors.New("task scope required")
		}
		return r, nil
	}
	if !validID(g.TaskID) || !validID(g.Purpose) || !digest.MatchString(g.ScopeDigest) || g.ExpiresAt == nil || !categories(g.Categories) || !list(g.SourceDocumentIDs, 64, validID) || len(g.Sources) > 64 || len(g.Sources) != len(g.SourceDocumentIDs) {
		return r, errors.New("task grant invalid")
	}
	t := &TaskContext{TaskID: g.TaskID, Purpose: g.Purpose, ScopeDigest: g.ScopeDigest, Categories: append([]string(nil), g.Categories...), SourceDocumentIDs: append([]string(nil), g.SourceDocumentIDs...), Sources: append([]gatewaygovernance.TaskSource(nil), g.Sources...), ExpiresAt: *g.ExpiresAt}
	seen := map[string]bool{}
	for _, s := range t.Sources {
		if !validID(s.DocumentID) || !slices.Contains(g.SourceDocumentIDs, s.DocumentID) || seen[s.DocumentID] || !validID(s.Version) || !validID(s.ACLVersion) || !digest.MatchString(s.ContentDigest) {
			return r, errors.New("task source grant invalid")
		}
		seen[s.DocumentID] = true
		t.SourceDigests = append(t.SourceDigests, sourceEvidenceDigest(s))
	}
	r.taskContext = t
	return r, nil
}

func sourceEvidenceDigest(s SourceEvidence) string { return hash(s) }

type gatewayTaskExecutionKey struct{}
type gatewayTaskSelectionKey struct{}
type gatewayTaskExecution struct {
	Binding     gatewaygovernance.TaskContext
	Admitted    bool
	ScopeDigest string
}

func taskBinding(ctx context.Context, r Request) gatewaygovernance.TaskContext {
	if r.TaskID == "" {
		return gatewaygovernance.TaskContext{}
	}
	if current, ok := ctx.Value(gatewayTaskExecutionKey{}).(*gatewayTaskExecution); ok {
		return current.Binding
	}
	return gatewaygovernance.TaskContext{TaskID: r.TaskID, TaskStepID: r.RequestID, TaskStepDigest: RequestDigest(r)}
}

func selectedTask(in *http.Request, wire string) (string, error) {
	values := in.Header.Values("X-Scarlet-Task-Id")
	if len(values) > 1 {
		return "", errors.New("multiple task selectors")
	}
	if len(values) == 1 {
		if wire != "" && wire != values[0] {
			return "", errors.New("task selectors disagree")
		}
		wire = values[0]
	}
	if wire != "" && !validID(wire) {
		return "", errors.New("task selector invalid")
	}
	return wire, nil
}

func requestFromWire(w RuntimeRequest, i Identity) Request {
	return Request{TaskID: w.TaskID, RequestID: w.RequestID, Kind: w.Kind, ApplicationID: i.ApplicationID, PrincipalID: i.PrincipalID, DocumentIDs: w.DocumentIDs, ModelID: w.ModelID, ToolID: w.ToolID, Action: w.Action, Target: w.Target, Arguments: w.Arguments, Input: w.Input, ApprovalID: w.ApprovalID, IssuedAt: w.IssuedAt, ExpiresAt: w.ExpiresAt}
}

// TaskOperation contains only runtime-derived metadata at the enforcement point.
type TaskOperation struct {
	Stage    string
	Request  Request
	Decision Decision
	Audit    *AuditRecord
}

func (h *runtimeHandler) task(ctx context.Context, r Request, d Decision, stage string, audit *AuditRecord) (Request, gatewaygovernance.TaskGrant, error) {
	if r.TaskID == "" && (stage != "check" || h.options.TaskControl == nil) {
		return r, gatewaygovernance.TaskGrant{Allowed: true}, nil
	}
	if h.options.TaskControl == nil {
		return r, gatewaygovernance.TaskGrant{}, errors.New("task_controller_required")
	}
	g, err := h.options.TaskControl(ctx, TaskOperation{Stage: stage, Request: r, Decision: d, Audit: audit})
	if err != nil {
		return r, g, err
	}
	if d.TaskScopeDigest != "" && d.TaskScopeDigest != g.ScopeDigest {
		return r, g, errors.New("task_scope_changed")
	}
	if current, ok := ctx.Value(gatewayTaskExecutionKey{}).(*gatewayTaskExecution); ok {
		if current.ScopeDigest == "" {
			current.ScopeDigest = g.ScopeDigest
		} else if current.ScopeDigest != g.ScopeDigest {
			return r, g, errors.New("task_scope_changed")
		}
	}
	if stage == "admit" {
		current, _ := ctx.Value(gatewayTaskExecutionKey{}).(*gatewayTaskExecution)
		if g.Replayed && (current == nil || !current.Admitted) {
			return r, g, errors.New("task_step_replayed")
		}
		if current != nil {
			current.Admitted = true
		}
	}
	updated, err := ApplyTaskGrant(r, g)
	if err == nil && r.TaskID != "" && (updated.taskContext == nil || !updated.taskContext.ExpiresAt.After(h.options.Now())) {
		err = errors.New("task_expired")
	}
	return updated, g, err
}

func (h *runtimeHandler) watchTask(in *http.Request, r Request, d Decision) (*http.Request, func()) {
	if r.TaskID == "" {
		return in, func() {}
	}
	ctx, cancel := context.WithCancel(in.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if _, _, e := h.task(ctx, r, d, "check", nil); e != nil {
					cancel()
					return
				}
			}
		}
	}()
	return in.WithContext(ctx), func() { cancel(); <-done }
}
