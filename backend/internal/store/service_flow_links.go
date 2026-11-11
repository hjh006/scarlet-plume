package store

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/jackc/pgx/v5"
)

const maxServiceFlowPairs = 500
const serviceFlowWindow = 2 * time.Minute

type serviceFlowPair struct {
	caller                                 DataFlowInvestigationEntry
	receiver                               DataFlowInvestigationEntry
	clientSpan, callerBatch, receiverBatch string
}

// addServiceFlowLinks requires a currently authorized OTLP source on both ends.
// A shared trace alone, a peer name alone, or an unobserved downstream service
// never establishes a relationship. Observation retention and the query budget
// bound both work and the period in which a late batch can complete a chain.
func addServiceFlowLinks(ctx context.Context, tx pgx.Tx, workspaceID int64, filter DataFlowFilter, b *flowBuilder) error {
	rows, err := tx.Query(ctx, `SELECT a.id,sa.id,sa.name,sa.service,sa.kind,a.observation,a.batch_id,
 COALESCE(target.id,0),COALESCE(target.source_id,0),COALESCE(target.name,''),COALESCE(target.service,''),COALESCE(target.kind,''),COALESCE(target.observation,'{}'::jsonb),COALESCE(target.batch_id,''),call->>'spanRef'
 FROM plume.data_flow_observations a
 JOIN plume.runtime_security_sources sa ON sa.id=a.source_id AND sa.workspace_id=a.workspace_id AND sa.kind='otlp' AND sa.revoked_at IS NULL
 JOIN plume.memberships ma ON ma.workspace_id=sa.workspace_id AND ma.user_id=sa.actor_id AND ma.role='owner'
 CROSS JOIN LATERAL jsonb_array_elements(COALESCE(a.observation->'serviceCalls','[]'::jsonb)) call
 LEFT JOIN LATERAL (
  SELECT other.id,other.source_id,other.observation,other.batch_id,sb.name,sb.service,sb.kind
  FROM plume.data_flow_observations other
  JOIN plume.runtime_security_sources sb ON sb.id=other.source_id AND sb.workspace_id=other.workspace_id AND sb.kind='otlp' AND sb.revoked_at IS NULL
  JOIN plume.memberships mb ON mb.workspace_id=sb.workspace_id AND mb.user_id=sb.actor_id AND mb.role='owner'
  WHERE other.workspace_id=a.workspace_id AND other.source_id<>a.source_id
   AND sb.service=call->>'targetService'
   AND other.observation->>'traceRef'=a.observation->>'traceRef'
   AND other.observation->>'parentSpanRef'=call->>'spanRef'
   AND other.observed_at>=statement_timestamp()-interval '7 days'
   AND other.observed_at BETWEEN a.observed_at-interval '2 minutes' AND a.observed_at+interval '2 minutes'
 )target ON true
 WHERE a.workspace_id=$1 AND a.observed_at>=statement_timestamp()-interval '7 days'
  AND length(a.observation->>'traceRef')=32
  AND ($2='' OR sa.service=$2 OR call->>'targetService'=$2)
  AND ($3='' OR a.observation->>'subjectRef'=$3 OR target.observation->>'subjectRef'=$3)
  AND ($4='' OR a.observation->>'traceRef'=$4)
  AND ($5='' OR EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(a.observation->'dataFields','[]'::jsonb)||COALESCE(target.observation->'dataFields','[]'::jsonb)) f WHERE f->>'category'=$5 AND ((f->>'count')::bigint>0 OR f->>'method'='metadata')))
 ORDER BY a.id,target.id,call->>'spanRef' LIMIT 501`, workspaceID, filter.Service, filter.SubjectRef, filter.TraceRef, filter.Category)
	if err != nil {
		return err
	}
	pairs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (serviceFlowPair, error) {
		var p serviceFlowPair
		err := row.Scan(&p.caller.ID, &p.caller.SourceID, &p.caller.Source, &p.caller.Service, &p.caller.Kind, &p.caller.Observation, &p.callerBatch, &p.receiver.ID, &p.receiver.SourceID, &p.receiver.Source, &p.receiver.Service, &p.receiver.Kind, &p.receiver.Observation, &p.receiverBatch, &p.clientSpan)
		return p, err
	})
	if err != nil {
		return err
	}
	if len(pairs) > maxServiceFlowPairs {
		return fmt.Errorf("%w: service call pair budget %d", ErrDataFlowLimit, maxServiceFlowPairs)
	}
	seen := map[string]bool{}
	for _, pair := range pairs {
		if pair.receiver.ID == 0 {
			b.graph.Warnings = flowCategories(b.graph.Warnings, []string{"unmatched_service_call"})
			continue
		}
		if !validServiceFlowPair(pair) {
			b.graph.Warnings = flowCategories(b.graph.Warnings, []string{"unmatched_service_call"})
			continue
		}
		key := flowID(fmt.Sprint(pair.caller.SourceID), pair.caller.TraceRef, pair.caller.SpanRef, pair.clientSpan, fmt.Sprint(pair.receiver.SourceID), pair.receiver.SpanRef)
		if seen[key] {
			continue
		}
		seen[key] = true
		from := ensureServiceFlowEvent(b, pair.caller)
		to := ensureServiceFlowEvent(b, pair.receiver)
		if from == to {
			b.graph.Warnings = flowCategories(b.graph.Warnings, []string{"unmatched_service_call"})
			continue
		}
		cats := flowCategories(serviceFlowCategories(pair.caller.Observation), serviceFlowCategories(pair.receiver.Observation))
		proof := map[string]any{"clientSpanRef": pair.clientSpan, "receiverParentSpanRef": pair.receiver.ParentSpanRef, "targetService": pair.receiver.Service, "windowSeconds": int(serviceFlowWindow / time.Second), "callerBatch": pair.callerBatch, "receiverBatch": pair.receiverBatch, "counting": "two_sided_evidence_not_additive"}
		left := serviceFlowEvidence(pair.caller)
		left.Requests = 0
		left.Details = proof
		right := serviceFlowEvidence(pair.receiver)
		right.Requests = 0
		right.Details = proof
		b.edge(from, to, "service_call", cats, left)
		b.edge(from, to, "service_call", cats, right)
		b.graph.Warnings = flowCategories(b.graph.Warnings, []string{"service_links_window_2_minutes"})
		if len(b.nodes) > 1000 || len(b.edges) > 3000 {
			return ErrDataFlowLimit
		}
	}
	return nil
}
func validServiceFlowPair(pair serviceFlowPair) bool {
	a, c := pair.caller, pair.receiver
	if a.SourceID == 0 || c.SourceID == 0 || a.SourceID == c.SourceID || a.TraceRef == "" || a.TraceRef != c.TraceRef || c.ParentSpanRef != pair.clientSpan || a.SpanRef == c.SpanRef {
		return false
	}
	if _, err := runtimesecurity.NormalizeObservation(a.Observation); err != nil {
		return false
	}
	if _, err := runtimesecurity.NormalizeObservation(c.Observation); err != nil {
		return false
	}
	delta := a.ObservedAt.Sub(c.ObservedAt)
	if delta < 0 {
		delta = -delta
	}
	if delta > serviceFlowWindow {
		return false
	}
	return slices.Contains(a.ServiceCalls, runtimesecurity.ServiceCall{TargetService: c.Service, SpanRef: pair.clientSpan})
}
func serviceFlowCategories(o runtimesecurity.Observation) []string {
	cats := []string{}
	for _, field := range o.DataFields {
		if field.Count > 0 || field.Method == "metadata" {
			cats = flowCategories(cats, []string{field.Category})
		}
	}
	return cats
}
func serviceFlowEvidence(event DataFlowInvestigationEntry) DataFlowEvidence {
	return DataFlowEvidence{Kind: event.Kind, Source: event.Source, SourceID: event.SourceID, Record: fmt.Sprint(event.ID), ObservedAt: event.ObservedAt, TraceRef: event.TraceRef, SpanRef: event.SpanRef, Warnings: event.CaptureWarnings}
}
func ensureServiceFlowEvent(b *flowBuilder, event DataFlowInvestigationEntry) string {
	e := serviceFlowEvidence(event)
	cats := serviceFlowCategories(event.Observation)
	id := "api:" + flowID(event.Service, event.Method, runtimesecurity.RouteKey(event.Route))
	if b.nodes[id] == nil {
		b.api(event.Service, event.Method, event.Route, cats, e, "observed")
	} else {
		b.nodes[id].Categories = flowCategories(b.nodes[id].Categories, cats)
	}
	for _, target := range event.DatabaseTargets {
		targetID := b.targets[catalogFlowTargetKey(target.System, target.Source, target.Database, target.Schema, target.Table)]
		if targetID == "" || targetID == "ambiguous" {
			b.graph.Warnings = flowCategories(b.graph.Warnings, []string{"unmatched_database_target"})
			continue
		}
		if b.edges[flowID(id, targetID, "database_target")] == nil {
			b.edge(id, targetID, "database_target", cats, e)
		}
	}
	for _, system := range event.DatabaseSystems {
		targetID := "database_type:" + flowID(fmt.Sprint(event.SourceID), event.Service, system)
		if b.nodes[targetID] == nil {
			b.node(DataFlowNode{ID: targetID, Kind: "database_type", Label: system + "（类型级）", Service: event.Service, Evidence: []DataFlowEvidence{e}})
		}
		if b.edges[flowID(id, targetID, "database_type")] == nil {
			b.edge(id, targetID, "database_type", nil, e)
		}
	}
	return id
}
