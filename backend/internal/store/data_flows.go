package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hjh006/scarlet-plume/backend/internal/apisecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/jackc/pgx/v5"
)

var ErrDataFlowLimit = errors.New("data flow graph exceeds node or edge limit")
var errDataFlowBusy = errors.New("data flow snapshot is already being captured")

type DataFlowFilter struct {
	Service    string `json:"service"`
	Category   string `json:"category"`
	SubjectRef string `json:"subjectRef"`
	TraceRef   string `json:"traceRef"`
	Focus      string `json:"focus"`
}
type DataFlowSummary struct {
	Nodes                 int `json:"nodes"`
	Edges                 int `json:"edges"`
	Applications          int `json:"applications"`
	APIs                  int `json:"apis"`
	Tables                int `json:"tables"`
	Subjects              int `json:"subjects"`
	TracedDatabaseTargets int `json:"tracedDatabaseTargets"`
}
type DataFlowEvidence struct {
	Details    map[string]any `json:"details,omitempty"`
	Kind       string         `json:"kind"`
	Source     string         `json:"source"`
	SourceID   int64          `json:"sourceId,omitempty"`
	Record     string         `json:"record"`
	ObservedAt time.Time      `json:"observedAt,omitempty"`
	Requests   int64          `json:"requests,omitempty"`
	TraceRef   string         `json:"traceRef,omitempty"`
	SpanRef    string         `json:"spanRef,omitempty"`
	Warnings   []string       `json:"warnings,omitempty"`
}
type DataFlowTableTarget struct {
	System   string `json:"system"`
	Source   string `json:"source"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
	Table    string `json:"table"`
}
type DataFlowNode struct {
	Target            *DataFlowTableTarget `json:"target,omitempty"`
	ID                string               `json:"id"`
	Kind              string               `json:"kind"`
	Label             string               `json:"label"`
	Service           string               `json:"service,omitempty"`
	AssetID           int64                `json:"assetId,omitempty"`
	Category          string               `json:"category,omitempty"`
	BusinessLevel     string               `json:"businessLevel,omitempty"`
	StatutoryCategory string               `json:"statutoryCategory,omitempty"`
	Categories        []string             `json:"categories"`
	Evidence          []DataFlowEvidence   `json:"evidence"`
}
type DataFlowEdge struct {
	ID         string             `json:"id"`
	From       string             `json:"from"`
	To         string             `json:"to"`
	Relation   string             `json:"relation"`
	Categories []string           `json:"categories"`
	Evidence   []DataFlowEvidence `json:"evidence"`
}
type DataFlowSnapshotStatus struct {
	Status        string    `json:"status"`
	LastAttemptAt time.Time `json:"lastAttemptAt"`
	Error         string    `json:"error,omitempty"`
}
type DataFlowGraph struct {
	Scope          DataFlowFilter          `json:"scope"`
	GeneratedAt    time.Time               `json:"generatedAt"`
	WindowStart    time.Time               `json:"windowStart"`
	RetentionDays  int                     `json:"retentionDays"`
	Nodes          []DataFlowNode          `json:"nodes"`
	Edges          []DataFlowEdge          `json:"edges"`
	Warnings       []string                `json:"warnings"`
	Summary        DataFlowSummary         `json:"summary"`
	SnapshotStatus *DataFlowSnapshotStatus `json:"snapshotStatus,omitempty"`
}
type DataFlowSnapshotSummary struct {
	ID        int64           `json:"id"`
	CreatedAt time.Time       `json:"createdAt"`
	Origin    string          `json:"origin"`
	Scope     DataFlowFilter  `json:"scope"`
	Digest    string          `json:"digest"`
	Summary   DataFlowSummary `json:"summary"`
	Warnings  []string        `json:"warnings"`
}
type DataFlowSnapshot struct {
	DataFlowSnapshotSummary
	Graph DataFlowGraph `json:"graph"`
}
type DataFlowInvestigationEntry struct {
	ID       int64  `json:"id"`
	SourceID int64  `json:"sourceId"`
	Source   string `json:"source"`
	Service  string `json:"service"`
	Kind     string `json:"kind"`
	runtimesecurity.Observation
}
type DataFlowInvestigation struct {
	Items         []DataFlowInvestigationEntry `json:"items"`
	Limit         int                          `json:"limit"`
	Truncated     bool                         `json:"truncated"`
	RetentionDays int                          `json:"retentionDays"`
}
type DataFlowComparison struct {
	From         DataFlowSnapshotSummary `json:"from"`
	To           DataFlowSnapshotSummary `json:"to"`
	AddedNodes   []DataFlowNode          `json:"addedNodes"`
	RemovedNodes []DataFlowNode          `json:"removedNodes"`
	ChangedNodes []DataFlowNode          `json:"changedNodes"`
	AddedEdges   []DataFlowEdge          `json:"addedEdges"`
	RemovedEdges []DataFlowEdge          `json:"removedEdges"`
	ChangedEdges []DataFlowEdge          `json:"changedEdges"`
}

type flowBuilder struct {
	graph   DataFlowGraph
	nodes   map[string]*DataFlowNode
	edges   map[string]*DataFlowEdge
	targets map[string]string
}

func flowID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}
func flowCategories(a, b []string) []string {
	result := append([]string{}, a...)
	for _, s := range b {
		if s != "" && !slices.Contains(result, s) {
			result = append(result, s)
		}
	}
	slices.Sort(result)
	return result
}
func (b *flowBuilder) node(n DataFlowNode) string {
	if old := b.nodes[n.ID]; old != nil {
		old.Categories = flowCategories(old.Categories, n.Categories)
		old.Evidence = mergeFlowEvidence(old.Evidence, n.Evidence)
		return old.ID
	}
	n.Categories = flowCategories(nil, n.Categories)
	if n.Evidence == nil {
		n.Evidence = []DataFlowEvidence{}
	}
	b.nodes[n.ID] = &n
	return n.ID
}
func mergeFlowEvidence(old, in []DataFlowEvidence) []DataFlowEvidence {
	result := append([]DataFlowEvidence{}, old...)
	for _, e := range in {
		found := false
		for i := range result {
			if result[i].Kind == e.Kind && result[i].Source == e.Source && result[i].SourceID == e.SourceID {
				found = true
				count := result[i].Requests + e.Requests
				if !e.ObservedAt.Before(result[i].ObservedAt) {
					result[i] = e
				}
				result[i].Requests = count
				break
			}
		}
		if !found {
			result = append(result, e)
		}
	}
	return result
}
func (b *flowBuilder) edge(from, to, relation string, categories []string, e DataFlowEvidence) {
	id := flowID(from, to, relation)
	if old := b.edges[id]; old != nil {
		old.Categories = flowCategories(old.Categories, categories)
		old.Evidence = mergeFlowEvidence(old.Evidence, []DataFlowEvidence{e})
		return
	}
	b.edges[id] = &DataFlowEdge{ID: id, From: from, To: to, Relation: relation, Categories: flowCategories(nil, categories), Evidence: []DataFlowEvidence{e}}
}
func (b *flowBuilder) application(service string) string {
	return b.node(DataFlowNode{ID: "application:" + flowID(service), Kind: "application", Label: service, Service: service})
}
func (b *flowBuilder) api(service, method, route string, cats []string, e DataFlowEvidence, relation string) string {
	app := b.application(service)
	id := "api:" + flowID(service, method, runtimesecurity.RouteKey(route))
	b.node(DataFlowNode{ID: id, Kind: "api", Label: method + " " + route, Service: service, Categories: cats, Evidence: []DataFlowEvidence{e}})
	b.edge(app, id, relation, cats, e)
	return id
}
func validateDataFlowFilter(f DataFlowFilter) error {
	for _, value := range []string{f.Service, f.Category, f.Focus} {
		if !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return ErrInvalid
		}
	}
	if utf8.RuneCountInString(f.Service) > 80 || len(f.Category) > 40 || len(f.Focus) > 100 {
		return ErrInvalid
	}
	for _, item := range []struct {
		value string
		size  int
	}{{f.SubjectRef, 64}, {f.TraceRef, 32}} {
		if item.value != "" {
			raw, err := hex.DecodeString(item.value)
			if err != nil || len(raw)*2 != item.size || strings.ToLower(item.value) != item.value {
				return ErrInvalid
			}
		}
	}

	return nil
}

func readDataFlowGraph(ctx context.Context, tx pgx.Tx, workspaceID int64, filter DataFlowFilter) (DataFlowGraph, error) {
	now := time.Now().UTC()
	b := flowBuilder{graph: DataFlowGraph{Scope: filter, GeneratedAt: now, WindowStart: now.Add(-7 * 24 * time.Hour), RetentionDays: 7, Nodes: []DataFlowNode{}, Edges: []DataFlowEdge{}, Warnings: []string{"scope_is_connected_sources", "runtime_window_7_days", "database_family_is_not_table_lineage"}}, nodes: map[string]*DataFlowNode{}, edges: map[string]*DataFlowEdge{}, targets: map[string]string{}}
	rows, err := tx.Query(ctx, `SELECT a.id,a.name,a.statutory_category,a.category,a.business_level,a.classification_revision,j.source,i.source_database,i.inspection,r.run_id,r.observed_at
 FROM plume.postgres_asset_inspections i JOIN plume.assets a ON a.id=i.asset_id
 JOIN plume.automation_jobs j ON j.id=i.job_id AND j.workspace_id=a.workspace_id
 JOIN plume.managed_postgres_jobs p ON p.job_id=j.id
 JOIN plume.automation_runs r ON r.id=i.automation_run_id
 WHERE a.workspace_id=$1 AND j.revoked_at IS NULL AND (i.source_host,i.source_port,i.source_database)=(p.host,p.port,p.database_name)
 AND r.status='success' AND r.id=(SELECT latest.id FROM plume.automation_runs latest WHERE latest.job_id=i.job_id ORDER BY latest.id DESC LIMIT 1)
 ORDER BY a.id LIMIT 1001`, workspaceID)
	if err != nil {
		return b.graph, err
	}
	for rows.Next() {
		var assetID, revision int64
		var name, statutory, category, businessLevel, source, database, run string
		var table collector.PostgreSQLTable
		var observed time.Time
		if err := rows.Scan(&assetID, &name, &statutory, &category, &businessLevel, &revision, &source, &database, &table, &run, &observed); err != nil {
			rows.Close()
			return b.graph, err
		}
		categories := []string{}
		for _, field := range postgresSensitiveFields(table) {
			categories = flowCategories(categories, []string{field.Sensitivity})
		}
		if table.ContentAnalysis != nil {
			table.Warnings = flowCategories(table.Warnings, table.ContentAnalysis.Warnings)
		}
		evidence := DataFlowEvidence{Kind: "postgresql", Source: source, Record: run, ObservedAt: observed, Warnings: table.Warnings}
		id := "table:" + fmt.Sprint(assetID)
		if revision == 0 {
			statutory = ""
		}
		b.node(DataFlowNode{ID: id, Kind: "table", Label: database + "." + table.Schema + "." + table.Name, AssetID: assetID, StatutoryCategory: statutory, Category: category, BusinessLevel: businessLevel, Categories: categories, Evidence: []DataFlowEvidence{evidence}, Target: &DataFlowTableTarget{System: "postgresql", Source: source, Database: database, Schema: table.Schema, Table: table.Name}})
		key := catalogFlowTargetKey("postgresql", source, database, table.Schema, table.Name)
		if previous := b.targets[key]; previous != "" && previous != id {
			b.targets[key] = "ambiguous"
		} else if previous == "" {
			b.targets[key] = id
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return b.graph, err
	}
	if err = addCatalogFlowNodes(ctx, tx, workspaceID, &b); err != nil {
		return b.graph, err
	}
	rows, err = tx.Query(ctx, `SELECT DISTINCT ON(source) id,source,created_at,operations FROM plume.api_security_scans WHERE workspace_id=$1 ORDER BY source,id DESC`, workspaceID)
	if err != nil {
		return b.graph, err
	}
	for rows.Next() {
		var id int64
		var source string
		var at time.Time
		var operations []apisecurity.Operation
		if err := rows.Scan(&id, &source, &at, &operations); err != nil {
			rows.Close()
			return b.graph, err
		}
		if filter.SubjectRef != "" || filter.TraceRef != "" || filter.Service != "" && source != filter.Service {
			continue
		}
		for _, op := range operations {
			cats := []string{}
			for _, f := range op.SensitiveFields {
				cats = flowCategories(cats, []string{f.Category})
			}
			b.api(source, op.Method, op.Path, cats, DataFlowEvidence{Kind: "openapi", Source: source, Record: fmt.Sprint(id), ObservedAt: at}, "declared")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return b.graph, err
	}
	rows, err = tx.Query(ctx, `SELECT s.id,s.name,s.service,s.kind,r.method,r.route,r.last_seen,r.database_systems
 FROM plume.runtime_security_routes r JOIN plume.runtime_security_sources s ON s.id=r.source_id
 WHERE s.workspace_id=$1 AND r.last_seen>= $2 AND ($3='' OR s.service=$3)
 ORDER BY s.id,r.method,r.route_key`, workspaceID, b.graph.WindowStart, filter.Service)
	if err != nil {
		return b.graph, err
	}
	for rows.Next() {
		var sourceID int64
		var source, service, kind, method, route string
		var at time.Time
		var systems []string
		if err := rows.Scan(&sourceID, &source, &service, &kind, &method, &route, &at, &systems); err != nil {
			rows.Close()
			return b.graph, err
		}
		if filter.SubjectRef != "" || filter.TraceRef != "" {
			continue
		}
		e := DataFlowEvidence{Kind: kind, Source: source, SourceID: sourceID, Record: "route", ObservedAt: at}
		api := b.api(service, method, route, nil, e, "observed")
		if filter.SubjectRef != "" || filter.TraceRef != "" {
			continue
		}
		for _, system := range systems {
			db := b.node(DataFlowNode{ID: "database_type:" + flowID(fmt.Sprint(sourceID), service, system), Kind: "database_type", Label: system + "（类型级）", Service: service, Evidence: []DataFlowEvidence{e}})
			b.edge(api, db, "database_type", nil, e)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return b.graph, err
	}
	entries, err := readDataFlowEvents(ctx, tx, workspaceID, filter, 5001)
	if err != nil {
		return b.graph, err
	}
	if len(entries) > 5000 {
		return b.graph, fmt.Errorf("%w: observation budget 5000", ErrDataFlowLimit)
	}
	for _, event := range entries {
		e := DataFlowEvidence{Kind: event.Kind, Source: event.Source, SourceID: event.SourceID, Record: fmt.Sprint(event.ID), ObservedAt: event.ObservedAt, Requests: 1, TraceRef: event.TraceRef, SpanRef: event.SpanRef, Warnings: event.CaptureWarnings}
		cats := []string{}
		for _, f := range event.DataFields {
			if f.Count > 0 || f.Method == "metadata" {
				cats = flowCategories(cats, []string{f.Category})
			}
		}
		api := b.api(event.Service, event.Method, event.Route, cats, e, "observed")
		if event.SubjectRef != "" {
			subject := b.node(DataFlowNode{ID: "subject:" + flowID(fmt.Sprint(event.SourceID), event.SubjectRef), Kind: "subject", Label: "去标识主体 " + event.SubjectRef[:12], Service: event.Service, Evidence: []DataFlowEvidence{e}})
			b.edge(subject, api, "subject_access", cats, e)
		}
		for _, target := range event.DatabaseTargets {
			id := b.targets[catalogFlowTargetKey(target.System, target.Source, target.Database, target.Schema, target.Table)]
			if id == "" || id == "ambiguous" {
				b.graph.Warnings = flowCategories(b.graph.Warnings, []string{"unmatched_database_target"})
				continue
			}
			b.edge(api, id, "database_target", cats, e)
		}
	}
	if err = addServiceFlowLinks(ctx, tx, workspaceID, filter, &b); err != nil {
		return b.graph, err
	}
	if filter.SubjectRef != "" || filter.TraceRef != "" {
		used := map[string]bool{}
		for _, edge := range b.edges {
			used[edge.From] = true
			used[edge.To] = true
		}
		for id := range b.nodes {
			if !used[id] {
				delete(b.nodes, id)
			}
		}
	}
	for _, node := range b.nodes {
		b.graph.Nodes = append(b.graph.Nodes, *node)
	}
	for _, edge := range b.edges {
		b.graph.Edges = append(b.graph.Edges, *edge)
	}
	b.graph = filterDataFlowGraph(b.graph, filter)
	if len(b.graph.Nodes) > 1000 || len(b.graph.Edges) > 3000 {
		return b.graph, ErrDataFlowLimit
	}
	slices.SortFunc(b.graph.Nodes, func(a, c DataFlowNode) int { return strings.Compare(a.ID, c.ID) })
	slices.SortFunc(b.graph.Edges, func(a, c DataFlowEdge) int { return strings.Compare(a.ID, c.ID) })
	b.graph.Summary = summarizeDataFlow(b.graph)
	var status DataFlowSnapshotStatus
	err = tx.QueryRow(ctx, `SELECT status,last_attempt_at,error FROM plume.data_flow_snapshot_status WHERE workspace_id=$1`, workspaceID).Scan(&status.Status, &status.LastAttemptAt, &status.Error)
	if err == nil {
		b.graph.SnapshotStatus = &status
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return b.graph, err
	}
	return b.graph, nil
}

func filterDataFlowGraph(graph DataFlowGraph, f DataFlowFilter) DataFlowGraph {
	if f.Service == "" && f.Category == "" && f.Focus == "" && f.SubjectRef == "" && f.TraceRef == "" {
		return graph
	}
	selected := map[string]bool{}
	for _, n := range graph.Nodes {
		if f.Focus != "" {
			selected[n.ID] = n.ID == f.Focus
			continue
		}
		selected[n.ID] = (f.Service == "" || n.Service == f.Service) && (f.Category == "" || slices.Contains(n.Categories, f.Category))
	}
	if f.Focus != "" {
		for _, e := range graph.Edges {
			if e.From == f.Focus || e.To == f.Focus {
				selected[e.From] = true
				selected[e.To] = true
			}
		}
	}
	// A verified inter-service relation can add its actual peer and that peer's
	// database context. Application membership must never pull in unrelated APIs.
	for changed := true; changed; {
		changed = false
		for _, edge := range graph.Edges {
			if edge.Relation == "service_call" && (selected[edge.From] || selected[edge.To]) {
				if !selected[edge.From] || !selected[edge.To] {
					changed = true
				}
				selected[edge.From] = true
				selected[edge.To] = true
			}
		}
	}
	// Preserve the actual target endpoints of matching evidence; never invent an edge.
	seeds := map[string]bool{}
	for id, match := range selected {
		seeds[id] = match
	}
	edges := []DataFlowEdge{}
	for _, e := range graph.Edges {
		match := seeds[e.From] || seeds[e.To]
		if f.Category != "" {
			match = match && slices.Contains(e.Categories, f.Category)
		}
		if match {
			edges = append(edges, e)
			selected[e.From] = true
			selected[e.To] = true
		}
	}
	nodes := []DataFlowNode{}
	for _, n := range graph.Nodes {
		if selected[n.ID] {
			nodes = append(nodes, n)
		}
	}
	graph.Nodes, graph.Edges = nodes, edges
	return graph
}
func summarizeDataFlow(g DataFlowGraph) DataFlowSummary {
	s := DataFlowSummary{Nodes: len(g.Nodes), Edges: len(g.Edges)}
	targets := map[string]bool{}
	for _, n := range g.Nodes {
		switch n.Kind {
		case "application":
			s.Applications++
		case "api":
			s.APIs++
		case "table":
			s.Tables++
		case "subject":
			s.Subjects++
		}
	}
	for _, e := range g.Edges {
		if e.Relation == "database_target" {
			targets[e.To] = true
		}
	}
	s.TracedDatabaseTargets = len(targets)
	return s
}
func readDataFlowEvents(ctx context.Context, tx pgx.Tx, workspaceID int64, f DataFlowFilter, limit int) ([]DataFlowInvestigationEntry, error) {
	query := `SELECT e.id,s.id,s.name,s.service,s.kind,e.observation FROM plume.data_flow_observations e JOIN plume.runtime_security_sources s ON s.id=e.source_id AND s.workspace_id=e.workspace_id
 WHERE e.workspace_id=$1 AND e.observed_at>=statement_timestamp()-interval '7 days' AND ($2='' OR s.service=$2)
 AND ($3='' OR e.observation->>'subjectRef'=$3) AND ($4='' OR e.observation->>'traceRef'=$4)
 AND ($5='' OR EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(e.observation->'dataFields','[]')) f WHERE f->>'category'=$5 AND ((f->>'count')::bigint>0 OR f->>'method'='metadata')))
 ORDER BY e.observed_at DESC,e.id DESC`
	args := []any{workspaceID, f.Service, f.SubjectRef, f.TraceRef, f.Category}
	if limit > 0 {
		query += " LIMIT $6"
		args = append(args, limit)
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	entries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (DataFlowInvestigationEntry, error) {
		var e DataFlowInvestigationEntry
		err := row.Scan(&e.ID, &e.SourceID, &e.Source, &e.Service, &e.Kind, &e.Observation)
		return e, err
	})
	if entries == nil {
		entries = []DataFlowInvestigationEntry{}
	}
	return entries, err
}

func (s *Store) DataFlows(ctx context.Context, workspaceID, actorID int64, f DataFlowFilter) (DataFlowGraph, error) {
	var graph DataFlowGraph
	if err := validateDataFlowFilter(f); err != nil {
		return graph, err
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		var err error
		graph, err = readDataFlowGraph(ctx, tx, workspaceID, f)
		return err
	})
	return graph, storeError(err)
}
func (s *Store) DataFlowSummary(ctx context.Context, workspaceID int64) (DataFlowSummary, error) {
	var result DataFlowSummary
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		graph, err := readDataFlowGraph(ctx, tx, workspaceID, DataFlowFilter{})
		result = graph.Summary
		return err
	})
	return result, err
}
func persistDataFlowObservations(ctx context.Context, tx pgx.Tx, workspaceID, sourceID, actorID int64, service, kind, batchID string, observations []runtimesecurity.Observation) error {
	for i, o := range observations {
		if _, err := tx.Exec(ctx, `INSERT INTO plume.data_flow_observations(workspace_id,source_id,batch_id,ordinal,observed_at,observation)
   SELECT $1,id,$3,$4,$5,$6 FROM plume.runtime_security_sources WHERE id=$2 AND workspace_id=$1 AND service=$7 AND kind=$8
   ON CONFLICT(source_id,batch_id,ordinal) DO NOTHING`, workspaceID, sourceID, batchID, i, o.ObservedAt, o, service, kind); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `DELETE FROM plume.data_flow_observations WHERE source_id=$1 AND
 (observed_at<statement_timestamp()-interval '7 days' OR id IN (SELECT id FROM plume.data_flow_observations WHERE source_id=$1 ORDER BY observed_at DESC,id DESC OFFSET 20000))`, sourceID)
	return err
}
func flowDigest(g DataFlowGraph) string {
	type node struct {
		ID, Kind, Category, BusinessLevel, StatutoryCategory string
		Categories                                           []string
	}
	type edge struct {
		ID, From, To, Relation string
		Categories             []string
	}
	data := struct {
		Scope DataFlowFilter
		Nodes []node
		Edges []edge
	}{Scope: g.Scope, Nodes: []node{}, Edges: []edge{}}
	for _, n := range g.Nodes {
		data.Nodes = append(data.Nodes, node{n.ID, n.Kind, n.Category, n.BusinessLevel, n.StatutoryCategory, n.Categories})
	}
	for _, e := range g.Edges {
		data.Edges = append(data.Edges, edge{e.ID, e.From, e.To, e.Relation, e.Categories})
	}
	raw, _ := json.Marshal(data)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func saveDataFlowSnapshot(ctx context.Context, tx pgx.Tx, workspaceID, actorID int64, origin string, f DataFlowFilter) (int64, error) {
	var oldTimeout string
	if err := tx.QueryRow(ctx, "SHOW statement_timeout").Scan(&oldTimeout); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout='500ms'"); err != nil {
		return 0, err
	}
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('plume:data-flow:'||$1::bigint::text,0))`, workspaceID).Scan(&locked); err != nil {
		return 0, err
	}
	if !locked {
		return 0, errDataFlowBusy
	}
	graph, err := readDataFlowGraph(ctx, tx, workspaceID, f)
	if err != nil {
		return 0, err
	}
	graph.SnapshotStatus = nil
	digest := flowDigest(graph)
	var id int64
	var last string
	err = tx.QueryRow(ctx, `SELECT id,digest FROM plume.data_flow_snapshots WHERE workspace_id=$1 AND scope=$2 AND status='ready' ORDER BY id DESC LIMIT 1`, workspaceID, f).Scan(&id, &last)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	if last != digest {
		if err := tx.QueryRow(ctx, `INSERT INTO plume.data_flow_snapshots(workspace_id,actor_id,origin,scope,digest,payload) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, workspaceID, actorID, origin, f, digest, graph).Scan(&id); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM plume.data_flow_snapshots WHERE workspace_id=$1 AND id IN(SELECT id FROM plume.data_flow_snapshots WHERE workspace_id=$1 ORDER BY id DESC OFFSET 30)`, workspaceID); err != nil {
			return 0, err
		}
	}
	if f == (DataFlowFilter{}) {
		_, err = tx.Exec(ctx, `INSERT INTO plume.data_flow_snapshot_status(workspace_id,status,error) VALUES($1,'ready','') ON CONFLICT(workspace_id) DO UPDATE SET status='ready',error='',last_attempt_at=clock_timestamp()`, workspaceID)
		if err != nil {
			return 0, err
		}
	}
	_, err = tx.Exec(ctx, "SELECT set_config('statement_timeout',$1,true)", oldTimeout)
	return id, err
}
func recordDataFlowSnapshotFailure(ctx context.Context, tx pgx.Tx, workspaceID int64, cause error) error {
	code := "capture_failed"
	if errors.Is(cause, ErrDataFlowLimit) {
		code = "graph_limit"
	}
	if errors.Is(cause, errDataFlowBusy) {
		code = "capture_busy"
	}
	// Status is secondary to the accepted source facts. A contended status row
	// must not hold up telemetry or discard the enclosing collection transaction.
	err := pgx.BeginFunc(ctx, tx, func(statusTx pgx.Tx) error {
		var statement, lock string
		if err := statusTx.QueryRow(ctx, "SELECT current_setting('statement_timeout'),current_setting('lock_timeout')").Scan(&statement, &lock); err != nil {
			return err
		}
		if _, err := statusTx.Exec(ctx, "SET LOCAL lock_timeout='25ms'"); err != nil {
			return err
		}
		if _, err := statusTx.Exec(ctx, "SET LOCAL statement_timeout='100ms'"); err != nil {
			return err
		}
		if _, err := statusTx.Exec(ctx, `INSERT INTO plume.data_flow_snapshot_status(workspace_id,status,error)VALUES($1,'error',$2) ON CONFLICT(workspace_id) DO UPDATE SET status='error',error=excluded.error,last_attempt_at=clock_timestamp()`, workspaceID, code); err != nil {
			return err
		}
		_, err := statusTx.Exec(ctx, "SELECT set_config('statement_timeout',$1,true),set_config('lock_timeout',$2,true)", statement, lock)
		return err
	})
	if err != nil {
		slog.Warn("data flow snapshot status could not be recorded", "workspaceID", workspaceID)
	}
	return nil
}
func captureDataFlowSnapshot(ctx context.Context, tx pgx.Tx, workspaceID, actorID int64, origin string) error {
	err := pgx.BeginFunc(ctx, tx, func(nested pgx.Tx) error {
		_, err := saveDataFlowSnapshot(ctx, nested, workspaceID, actorID, origin, DataFlowFilter{})
		return err
	})
	if err != nil {
		return recordDataFlowSnapshotFailure(ctx, tx, workspaceID, err)
	}
	return nil
}
func (s *Store) CaptureDataFlowSnapshot(ctx context.Context, workspaceID, actorID int64, f DataFlowFilter) (DataFlowSnapshot, error) {
	if err := validateDataFlowFilter(f); err != nil {
		return DataFlowSnapshot{}, err
	}
	var id int64
	var captureErr error
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		captureErr = pgx.BeginFunc(ctx, tx, func(nested pgx.Tx) error {
			id, err = saveDataFlowSnapshot(ctx, nested, workspaceID, actorID, "manual", f)
			return err
		})
		if captureErr != nil {
			if f == (DataFlowFilter{}) {
				return recordDataFlowSnapshotFailure(ctx, tx, workspaceID, captureErr)
			}
			return nil
		}
		return nil
	})
	if err != nil {
		return DataFlowSnapshot{}, storeError(err)
	}
	if captureErr != nil {
		return DataFlowSnapshot{}, captureErr
	}
	return s.DataFlowSnapshot(ctx, workspaceID, actorID, id)
}
func readDataFlowSnapshot(ctx context.Context, tx pgx.Tx, workspaceID, id int64) (DataFlowSnapshot, error) {
	var s DataFlowSnapshot
	err := tx.QueryRow(ctx, `SELECT id,created_at,origin,scope,digest,payload FROM plume.data_flow_snapshots WHERE workspace_id=$1 AND id=$2 AND status='ready'`, workspaceID, id).Scan(&s.ID, &s.CreatedAt, &s.Origin, &s.Scope, &s.Digest, &s.Graph)
	s.Summary, s.Warnings = s.Graph.Summary, s.Graph.Warnings
	return s, storeError(err)
}
func (s *Store) DataFlowSnapshot(ctx context.Context, workspaceID, actorID, id int64) (DataFlowSnapshot, error) {
	var snapshot DataFlowSnapshot
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		var err error
		snapshot, err = readDataFlowSnapshot(ctx, tx, workspaceID, id)
		return err
	})
	return snapshot, storeError(err)
}
func (s *Store) ListDataFlowSnapshots(ctx context.Context, workspaceID, actorID int64) ([]DataFlowSnapshotSummary, error) {
	result := []DataFlowSnapshotSummary{}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,created_at,origin,scope,digest,payload->'summary',payload->'warnings' FROM plume.data_flow_snapshots WHERE workspace_id=$1 AND status='ready' ORDER BY id DESC LIMIT 30`, workspaceID)
		if err != nil {
			return err
		}
		result, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (DataFlowSnapshotSummary, error) {
			var s DataFlowSnapshotSummary
			err := row.Scan(&s.ID, &s.CreatedAt, &s.Origin, &s.Scope, &s.Digest, &s.Summary, &s.Warnings)
			return s, err
		})
		return err
	})
	return result, storeError(err)
}
func dataFlowEventMatchesFocus(event DataFlowInvestigationEntry, focus DataFlowNode) bool {
	switch focus.Kind {
	case "api":
		return focus.ID == "api:"+flowID(event.Service, event.Method, runtimesecurity.RouteKey(event.Route))
	case "application":
		return focus.ID == "application:"+flowID(event.Service)
	case "subject":
		return event.SubjectRef != "" && focus.ID == "subject:"+flowID(fmt.Sprint(event.SourceID), event.SubjectRef)
	case "database_type":
		for _, system := range event.DatabaseSystems {
			if focus.ID == "database_type:"+flowID(fmt.Sprint(event.SourceID), event.Service, system) {
				return true
			}
		}
	case "table":
		if focus.Target != nil {
			for _, target := range event.DatabaseTargets {
				if catalogFlowTargetKey(target.System, target.Source, target.Database, target.Schema, target.Table) == catalogFlowTargetKey(focus.Target.System, focus.Target.Source, focus.Target.Database, focus.Target.Schema, focus.Target.Table) {
					return true
				}
			}
			return false
		}
		// Historical PG snapshots predate structured targets.
		for _, target := range event.DatabaseTargets {
			if canonicalCatalogSystem(target.System) != "postgresql" || target.Database+"."+target.Schema+"."+target.Table != focus.Label {
				continue
			}
			for _, evidence := range focus.Evidence {
				if evidence.Kind == "postgresql" && evidence.Source == target.Source {
					return true
				}
			}
		}

	}
	return false
}

func (s *Store) InvestigateDataFlows(ctx context.Context, workspaceID, actorID int64, f DataFlowFilter) (DataFlowInvestigation, error) {
	result := DataFlowInvestigation{Items: []DataFlowInvestigationEntry{}, Limit: 500, RetentionDays: 7}
	if err := validateDataFlowFilter(f); err != nil {
		return result, err
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		if f.Focus == "" {
			var err error
			result.Items, err = readDataFlowEvents(ctx, tx, workspaceID, f, 501)
			return err
		}
		graph, err := readDataFlowGraph(ctx, tx, workspaceID, f)
		if err != nil {
			return err
		}
		index := slices.IndexFunc(graph.Nodes, func(node DataFlowNode) bool { return node.ID == f.Focus })
		if index < 0 {
			return nil
		}
		entries, err := readDataFlowEvents(ctx, tx, workspaceID, f, 0)
		if err != nil {
			return err
		}
		for _, event := range entries {
			if graph.Nodes[index].Kind == "table" {
				matched := false
				api := "api:" + flowID(event.Service, event.Method, runtimesecurity.RouteKey(event.Route))
				for _, edge := range graph.Edges {
					if edge.From == api && edge.To == f.Focus && edge.Relation == "database_target" {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}
			if dataFlowEventMatchesFocus(event, graph.Nodes[index]) {
				result.Items = append(result.Items, event)
				if len(result.Items) == 501 {
					break
				}
			}
		}
		return nil
	})
	if len(result.Items) > 500 {
		result.Items = result.Items[:500]
		result.Truncated = true
	}
	return result, storeError(err)
}
func (s *Store) CompareDataFlowSnapshots(ctx context.Context, workspaceID, actorID, fromID, toID int64) (DataFlowComparison, error) {
	result := DataFlowComparison{AddedNodes: []DataFlowNode{}, RemovedNodes: []DataFlowNode{}, ChangedNodes: []DataFlowNode{}, AddedEdges: []DataFlowEdge{}, RemovedEdges: []DataFlowEdge{}, ChangedEdges: []DataFlowEdge{}}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		a, err := readDataFlowSnapshot(ctx, tx, workspaceID, fromID)
		if err != nil {
			return err
		}
		b, err := readDataFlowSnapshot(ctx, tx, workspaceID, toID)
		if err != nil {
			return err
		}
		if a.Scope != b.Scope {
			return ErrInvalid
		}
		result.From, result.To = a.DataFlowSnapshotSummary, b.DataFlowSnapshotSummary
		nodes := map[string]DataFlowNode{}
		for _, n := range a.Graph.Nodes {
			nodes[n.ID] = n
		}
		for _, n := range b.Graph.Nodes {
			old, exists := nodes[n.ID]
			if !exists {
				result.AddedNodes = append(result.AddedNodes, n)
			} else if !slices.Equal(old.Categories, n.Categories) || old.StatutoryCategory != n.StatutoryCategory || old.Category != n.Category || old.BusinessLevel != n.BusinessLevel {
				result.ChangedNodes = append(result.ChangedNodes, n)
			}
			delete(nodes, n.ID)
		}
		for _, n := range nodes {
			result.RemovedNodes = append(result.RemovedNodes, n)
		}
		edges := map[string]DataFlowEdge{}
		for _, e := range a.Graph.Edges {
			edges[e.ID] = e
		}
		for _, e := range b.Graph.Edges {
			old, exists := edges[e.ID]
			if !exists {
				result.AddedEdges = append(result.AddedEdges, e)
			} else if !slices.Equal(old.Categories, e.Categories) {
				result.ChangedEdges = append(result.ChangedEdges, e)
			}
			delete(edges, e.ID)
		}
		for _, e := range edges {
			result.RemovedEdges = append(result.RemovedEdges, e)
		}
		for _, items := range [][]DataFlowNode{result.AddedNodes, result.RemovedNodes, result.ChangedNodes} {
			slices.SortFunc(items, func(a, b DataFlowNode) int { return strings.Compare(a.ID, b.ID) })
		}
		for _, items := range [][]DataFlowEdge{result.AddedEdges, result.RemovedEdges, result.ChangedEdges} {
			slices.SortFunc(items, func(a, b DataFlowEdge) int { return strings.Compare(a.ID, b.ID) })
		}
		return nil
	})
	return result, storeError(err)
}
