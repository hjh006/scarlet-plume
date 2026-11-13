package store

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationstandards"
	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/jackc/pgx/v5"
)

type ClassificationAssetRevision struct {
	AssetID  int64 `json:"assetId"`
	Revision int64 `json:"revision"`
}
type ClassificationBatchReviewInput struct {
	Items             []ClassificationAssetRevision `json:"items"`
	Category          string                        `json:"category"`
	BusinessLevel     string                        `json:"businessLevel"`
	StatutoryCategory string                        `json:"statutoryCategory"`
	Basis             string                        `json:"basis"`
	StandardID        string                        `json:"standardId"`
	LevelCode         string                        `json:"levelCode"`
	RuleID            string                        `json:"ruleId"`
	TaxonomyID        *int64                        `json:"taxonomyId"`
}
type ClassificationCatalogReference struct {
	Kind       string    `json:"kind"`
	Source     string    `json:"source"`
	SourceRef  string    `json:"sourceRef"`
	System     string    `json:"system"`
	Database   string    `json:"database"`
	Schema     string    `json:"schema"`
	Table      string    `json:"table"`
	Field      string    `json:"field"`
	Current    bool      `json:"current"`
	ObservedAt time.Time `json:"observedAt"`
	SourceID   *int64    `json:"sourceId,omitempty"`
	RunID      *int64    `json:"runId,omitempty"`
}
type ClassificationWorkbenchItem struct {
	AssetClassificationItem
	StandardDefinition *classificationstandards.Standard `json:"standardDefinition,omitempty"`
	Catalog            *ClassificationCatalogReference   `json:"catalog,omitempty"`
	PublicationState   string                            `json:"publicationState"`
	NeedsReview        bool                              `json:"needsReview"`
	Facts              json.RawMessage                   `json:"facts,omitempty"`
}
type ClassificationWorkbenchFilter struct {
	Page              int
	PageSize          int
	Q                 string
	Source            string
	System            string
	Kind              string
	Category          string
	BusinessLevel     string
	StatutoryCategory string
	Status            string
	TaxonomyID        int64
}
type ClassificationFacet struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}
type ClassificationWorkbenchFacets struct {
	Category          []ClassificationFacet `json:"category"`
	BusinessLevel     []ClassificationFacet `json:"businessLevel"`
	StatutoryCategory []ClassificationFacet `json:"statutoryCategory"`
	Source            []ClassificationFacet `json:"source"`
	System            []ClassificationFacet `json:"system"`
	Kind              []ClassificationFacet `json:"kind"`
}
type ClassificationWorkbenchCounts struct {
	AssetClassificationCounts
	Changed     int64 `json:"changed"`
	Unpublished int64 `json:"unpublished"`
}
type ClassificationWorkbenchPage struct {
	Items    []ClassificationWorkbenchItem `json:"items"`
	Total    int64                         `json:"total"`
	Page     int                           `json:"page"`
	PageSize int                           `json:"pageSize"`
	Counts   ClassificationWorkbenchCounts `json:"counts"`
	Facets   ClassificationWorkbenchFacets `json:"facets"`
	Taxonomy []ClassificationTaxonomyNode  `json:"taxonomy"`
}

func classificationRevisionItems(items []ClassificationAssetRevision) ([]ClassificationAssetRevision, error) {
	if len(items) < 1 || len(items) > 200 {
		return nil, ErrInvalid
	}
	sorted := append([]ClassificationAssetRevision(nil), items...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].AssetID < sorted[j].AssetID })
	for i, item := range sorted {
		if item.AssetID < 1 || item.Revision < 0 || i > 0 && sorted[i-1].AssetID == item.AssetID {
			return nil, ErrInvalid
		}
	}
	return sorted, nil
}

func (s *Store) BatchReviewAssetClassifications(ctx context.Context, ws, actor int64, input ClassificationBatchReviewInput) ([]AssetClassificationDetail, error) {
	items, e := classificationRevisionItems(input.Items)
	if e != nil {
		return nil, e
	}
	details := make([]AssetClassificationDetail, 0, len(items))
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, ws, actor, true)
		if e != nil {
			return e
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		for _, selected := range items {
			item, e := updateAssetClassificationTx(ctx, tx, ws, actor, selected.AssetID, AssetClassificationInput{Revision: selected.Revision, Category: input.Category, BusinessLevel: input.BusinessLevel, StatutoryCategory: input.StatutoryCategory, Basis: input.Basis, StandardID: input.StandardID, LevelCode: input.LevelCode, RuleID: input.RuleID, TaxonomyID: input.TaxonomyID})
			if e != nil {
				return e
			}
			detail, e := classificationDetail(ctx, tx, ws, item)
			if e != nil {
				return e
			}
			details = append(details, detail)
		}
		return captureDataFlowSnapshot(ctx, tx, ws, actor, "classification")
	})
	if err != nil {
		return nil, storeError(err)
	}
	return details, nil
}

// The facts deliberately omit poll IDs and timestamps. Repeating an identical
// observation should not invalidate a reviewed publication.
const classificationCatalogCurrent = `(` + catalogCurrent + ` AND EXISTS(SELECT 1 FROM plume.memberships source_member WHERE source_member.workspace_id=ds.workspace_id AND source_member.user_id=ds.created_by AND source_member.role='owner'))`

func classificationBuiltinRulesSQL() string {
	encoded, _ := json.Marshal(collector.BuiltinDiscoveryRules())
	return "$classification_builtin_rules$" + string(encoded) + "$classification_builtin_rules$::jsonb"
}

var classificationWorkbenchCTE = `WITH RECURSIVE taxonomy_tree AS (
 SELECT id,parent_id,name,revision,enabled,name::text AS path,(id::text||':'||revision::text)::text AS signature,ARRAY[id] AS ids
 FROM plume.classification_taxonomy WHERE workspace_id=$1 AND parent_id IS NULL
 UNION ALL
 SELECT n.id,n.parent_id,n.name,n.revision,t.enabled AND n.enabled,t.path||' / '||n.name,t.signature||'/'||n.id::text||':'||n.revision::text,t.ids||n.id
 FROM plume.classification_taxonomy n JOIN taxonomy_tree t ON n.parent_id=t.id WHERE n.workspace_id=$1
), rule_entries AS (
 SELECT value AS rule FROM jsonb_array_elements(` + classificationBuiltinRulesSQL() + `)
 UNION ALL
 SELECT jsonb_build_object('key',rule_key,'version',version,'name',name,'kind',kind,'target',target,'category',category,'expression',expression,'dictionary',dictionary,'status',status)
 FROM plume.discovery_rule_versions WHERE workspace_id=$1 AND status='published'
), published_rules AS (
 SELECT COALESCE(jsonb_agg(rule ORDER BY rule->>'key'),'[]'::jsonb) AS versions FROM rule_entries
), raw_rows AS (
 SELECT a.*,
 CASE WHEN ce.id IS NULL THEN NULL ELSE jsonb_build_object('kind',ce.kind,'source',ce.source,'sourceRef',ce.source_ref,'system',ce.system,'database',ce.database_name,'schema',ce.schema_name,'table',ce.table_name,'field',ce.field_name,'current',COALESCE(` + classificationCatalogCurrent + `,ce.source_id IS NULL),'observedAt',ce.observed_at,'sourceId',ce.source_id,'runId',ce.run_id) END AS catalog,
 COALESCE(ce.kind,'asset') AS catalog_kind,
 ce.source_id IS NOT NULL AND NOT COALESCE(` + classificationCatalogCurrent + `,false) AS catalog_unsafe,
 a.classification_taxonomy IS NOT NULL AND (tt.id IS NULL OR NOT tt.enabled OR a.classification_taxonomy IS DISTINCT FROM jsonb_build_object('id',tt.id,'name',tt.name,'path',tt.path,'revision',tt.revision,'signature',tt.signature)) AS taxonomy_stale,
 COALESCE((pi.classification IS NOT NULL AND (
   (pi.classification->>'classificationRevision')::bigint<>a.classification_revision OR pi.classification->>'comparison'='conflict'
   OR pi.classification#>>'{evaluation,standard,id}'<>COALESCE(a.classification_standard->>'id',w.classification_standard_id,'gbt-43697-2024')
   OR aj.revoked_at IS NOT NULL OR NOT EXISTS(SELECT 1 FROM plume.memberships pg_member WHERE pg_member.workspace_id=a.workspace_id AND pg_member.user_id=aj.actor_id AND pg_member.role IN ('owner','operator')) OR (pi.source_host,pi.source_port,pi.source_database) IS DISTINCT FROM (mp.host,mp.port,mp.database_name)
   OR pi.automation_run_id IS DISTINCT FROM (SELECT max(ar.id) FROM plume.automation_runs ar WHERE ar.job_id=aj.id)
 )),false)
 OR COALESCE(ca.classification_revision<>a.classification_revision,false)
 OR COALESCE(ce.kind IN ('table','field','file') AND published_rules.versions IS DISTINCT FROM observed_rules.versions,false)
 OR COALESCE(ce.kind='field' AND ce.metadata ? 'classification' AND (ce.metadata->>'classificationRevision')::bigint IS DISTINCT FROM a.classification_revision,false)
 OR COALESCE(CASE COALESCE(ca.assessment#>>'{evaluation,category}',ce.metadata#>>'{classification,category}') WHEN 'sensitive_personal' THEN 2 WHEN 'personal' THEN 1 ELSE 0 END > CASE a.category WHEN 'sensitive_personal' THEN 2 WHEN 'personal' THEN 1 ELSE 0 END,false)
 OR COALESCE(CASE COALESCE(ca.assessment#>>'{evaluation,businessLevel}',ce.metadata#>>'{classification,businessLevel}') WHEN 'confidential' THEN 2 WHEN 'internal' THEN 1 ELSE 0 END > CASE a.business_level WHEN 'confidential' THEN 2 WHEN 'internal' THEN 1 ELSE 0 END,false) AS automatic_unsafe,
 jsonb_build_object('catalog',CASE WHEN ce.id IS NULL THEN NULL ELSE jsonb_build_object('kind',ce.kind,'sourceId',ce.source_id,'sourceRevision',ce.source_revision,'source',ce.source,'sourceRef',ce.source_ref,'system',ce.system,'database',ce.database_name,'schema',ce.schema_name,'table',ce.table_name,'field',ce.field_name,'metadata',ce.metadata,'confirmedMetadata',ce.confirmed_metadata,'conflicts',ce.conflicts) END,
 'automatic',pi.classification-'jobId'-'runId'-'observedAt'-'stale',
 'catalogEvaluation',ca.assessment->'evaluation','publishedDiscoveryRules',CASE WHEN ce.kind IN ('table','field','file') THEN published_rules.versions ELSE NULL END) AS facts,
 pub.revision AS published_revision,pub.facts AS published_facts,
 tt.ids AS taxonomy_ids
 FROM plume.assets a
 CROSS JOIN published_rules
 JOIN plume.workspaces w ON w.id=a.workspace_id
 LEFT JOIN plume.catalog_entries ce ON ce.asset_id=a.id AND ce.workspace_id=a.workspace_id
 LEFT JOIN plume.discovery_sources ds ON ds.id=ce.source_id AND ds.workspace_id=a.workspace_id
 LEFT JOIN plume.discovery_runs dr ON dr.id=ce.run_id AND dr.workspace_id=a.workspace_id
 LEFT JOIN taxonomy_tree tt ON tt.id=(a.classification_taxonomy->>'id')::bigint
 LEFT JOIN plume.postgres_asset_inspections pi ON pi.asset_id=a.id
 LEFT JOIN plume.automation_jobs aj ON aj.id=pi.job_id AND aj.workspace_id=a.workspace_id
 LEFT JOIN plume.managed_postgres_jobs mp ON mp.job_id=aj.id
 LEFT JOIN LATERAL(SELECT classification_revision,assessment FROM plume.catalog_classification_assessments ca WHERE ca.workspace_id=a.workspace_id AND ca.asset_id=a.id ORDER BY ca.id DESC LIMIT 1) ca ON true
 LEFT JOIN LATERAL(SELECT parent_assessment.assessment FROM plume.catalog_entries parent JOIN plume.catalog_classification_assessments parent_assessment ON parent_assessment.workspace_id=parent.workspace_id AND parent_assessment.asset_id=parent.asset_id AND parent_assessment.run_id=ce.run_id WHERE ce.kind='field' AND parent.workspace_id=a.workspace_id AND parent.source_id=ce.source_id AND parent.source_ref=ce.parent_ref ORDER BY parent_assessment.id DESC LIMIT 1) parent_ca ON true
 LEFT JOIN LATERAL(SELECT COALESCE(jsonb_agg(r.value ORDER BY r.value->>'key'),'[]'::jsonb) AS versions FROM jsonb_array_elements(COALESCE(ca.assessment->'appliedRules',parent_ca.assessment->'appliedRules',ce.metadata->'appliedRules','[]'::jsonb)) r) observed_rules ON true
 LEFT JOIN LATERAL(SELECT i.revision,i.facts FROM plume.classification_publication_items i JOIN plume.classification_publications p ON p.id=i.publication_id AND p.workspace_id=i.workspace_id WHERE i.workspace_id=a.workspace_id AND i.asset_id=a.id AND p.status='published' ORDER BY p.reviewed_at DESC,p.id DESC LIMIT 1) pub ON true
 WHERE a.workspace_id=$1
), workbench AS (
 SELECT *, (catalog_unsafe OR automatic_unsafe OR taxonomy_stale) AS needs_review, CASE WHEN published_revision IS NULL THEN 'unpublished' WHEN published_revision<>classification_revision OR catalog_unsafe OR taxonomy_stale OR automatic_unsafe OR facts IS DISTINCT FROM published_facts THEN 'changed' ELSE 'published' END AS publication_state
 FROM raw_rows
)`

const classificationWorkbenchWhere = ` WHERE ($2='' OR strpos(lower(name||' '||system||' '||COALESCE(source,'')||' '||COALESCE(source_ref,'')||' '||COALESCE(catalog->>'database','')||' '||COALESCE(catalog->>'table','')||' '||COALESCE(catalog->>'field','')),lower($2))>0)
 AND ($3='' OR source=$3) AND ($4='' OR system=$4) AND ($5='' OR catalog_kind=$5)
 AND ($6='' OR category=$6) AND ($7='' OR business_level=$7) AND ($8='' OR statutory_category=$8)
 AND ($9::bigint=0 OR $9=ANY(taxonomy_ids))
 AND CASE $10::text WHEN 'pending' THEN classification_revision=0 WHEN 'reviewed' THEN classification_revision>0 AND classification_origin='manual' WHEN 'automatic' THEN classification_revision>0 AND classification_origin='automatic' WHEN 'changed' THEN publication_state='changed' WHEN 'unpublished' THEN publication_state='unpublished' ELSE true END`

func scanClassificationWorkbenchItem(row pgx.Row) (ClassificationWorkbenchItem, error) {
	var item ClassificationWorkbenchItem
	var e error
	item.Asset, e = scanAsset(row, &item.Revision, &item.Standard, &item.Origin, &item.Taxonomy, &item.TaxonomyStale, &item.Catalog, &item.PublicationState, &item.Facts, &item.NeedsReview)
	item.Candidates = []ClassificationCandidate{}
	return item, e
}

const classificationWorkbenchColumns = assetColumns + `,classification_revision,classification_standard,classification_origin,classification_taxonomy,taxonomy_stale,catalog,publication_state,facts,needs_review`

func readClassificationWorkbenchItems(ctx context.Context, tx pgx.Tx, ws int64, ids []int64) ([]ClassificationWorkbenchItem, error) {
	rows, e := tx.Query(ctx, classificationWorkbenchCTE+` SELECT `+classificationWorkbenchColumns+` FROM workbench WHERE id=ANY($2) ORDER BY id`, ws, ids)
	if e != nil {
		return nil, e
	}
	items, e := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClassificationWorkbenchItem, error) {
		return scanClassificationWorkbenchItem(row)
	})
	if e != nil {
		return nil, e
	}
	return items, loadClassificationWorkbenchCandidates(ctx, tx, ws, items)
}

func loadClassificationWorkbenchCandidates(ctx context.Context, tx pgx.Tx, ws int64, items []ClassificationWorkbenchItem) error {
	base := make([]AssetClassificationItem, len(items))
	for i := range items {
		base[i] = items[i].AssetClassificationItem
	}
	if e := loadClassificationCandidates(ctx, tx, ws, base); e != nil {
		return e
	}
	for i := range items {
		items[i].AssetClassificationItem = base[i]
	}
	return nil
}

func (s *Store) ClassificationWorkbench(ctx context.Context, ws, actor int64, f ClassificationWorkbenchFilter) (ClassificationWorkbenchPage, error) {
	if f.Page == 0 {
		f.Page = 1
	}
	if f.PageSize == 0 {
		f.PageSize = 50
	}
	if f.Page < 1 || f.Page > 1000000 || f.PageSize < 1 || f.PageSize > 100 || f.TaxonomyID < 0 || utf8.RuneCountInString(f.Q) > 100 || len(f.Source) > 240 || len(f.System) > 240 || !classificationFilterValue(f.Status, "", "pending", "reviewed", "automatic", "changed", "unpublished") || !classificationFilterValue(f.Kind, "", "asset", "business", "application", "database", "table", "field", "file") || !classificationFilterValue(f.Category, "", "unknown", "business", "personal", "sensitive_personal") || !classificationFilterValue(f.BusinessLevel, "", "unknown", "internal", "confidential") || f.StatutoryCategory != "" && !validStatutoryCategory(f.StatutoryCategory) {
		return ClassificationWorkbenchPage{}, ErrInvalid
	}
	result := ClassificationWorkbenchPage{Items: []ClassificationWorkbenchItem{}, Page: f.Page, PageSize: f.PageSize}
	args := []any{ws, strings.TrimSpace(f.Q), f.Source, f.System, f.Kind, f.Category, f.BusinessLevel, f.StatutoryCategory, f.TaxonomyID, f.Status}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		e := tx.QueryRow(ctx, classificationWorkbenchCTE+` SELECT count(*),count(*) FILTER(WHERE classification_revision=0),count(*) FILTER(WHERE classification_revision>0 AND classification_origin='manual'),count(*) FILTER(WHERE classification_revision>0 AND classification_origin='automatic'),count(*) FILTER(WHERE statutory_category IN ('important','core')),count(*) FILTER(WHERE publication_state='changed'),count(*) FILTER(WHERE publication_state='unpublished') FROM workbench`, ws).Scan(&result.Counts.Total, &result.Counts.Pending, &result.Counts.Reviewed, &result.Counts.Automatic, &result.Counts.ImportantOrCore, &result.Counts.Changed, &result.Counts.Unpublished)
		if e != nil {
			return e
		}
		if e = tx.QueryRow(ctx, classificationWorkbenchCTE+` SELECT count(*) FROM workbench`+classificationWorkbenchWhere, args...).Scan(&result.Total); e != nil {
			return e
		}
		pageArgs := append(append([]any(nil), args...), f.PageSize, (f.Page-1)*f.PageSize)
		rows, e := tx.Query(ctx, classificationWorkbenchCTE+` SELECT `+classificationWorkbenchColumns+` FROM workbench`+classificationWorkbenchWhere+` ORDER BY id LIMIT $11 OFFSET $12`, pageArgs...)
		if e != nil {
			return e
		}
		result.Items, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClassificationWorkbenchItem, error) {
			return scanClassificationWorkbenchItem(row)
		})
		if e != nil {
			return e
		}
		if e = loadClassificationWorkbenchCandidates(ctx, tx, ws, result.Items); e != nil {
			return e
		}
		facets, e := tx.Query(ctx, classificationWorkbenchCTE+` SELECT facet,key,count(*) FROM workbench CROSS JOIN LATERAL(VALUES('category',category),('businessLevel',business_level),('statutoryCategory',statutory_category),('source',COALESCE(source,'')),('system',system),('kind',catalog_kind)) f(facet,key) GROUP BY facet,key ORDER BY facet,key`, ws)
		if e != nil {
			return e
		}
		result.Facets = ClassificationWorkbenchFacets{Category: []ClassificationFacet{}, BusinessLevel: []ClassificationFacet{}, StatutoryCategory: []ClassificationFacet{}, Source: []ClassificationFacet{}, System: []ClassificationFacet{}, Kind: []ClassificationFacet{}}
		for facets.Next() {
			var name string
			var value ClassificationFacet
			if e = facets.Scan(&name, &value.Key, &value.Count); e != nil {
				facets.Close()
				return e
			}
			switch name {
			case "category":
				result.Facets.Category = append(result.Facets.Category, value)
			case "businessLevel":
				result.Facets.BusinessLevel = append(result.Facets.BusinessLevel, value)
			case "statutoryCategory":
				result.Facets.StatutoryCategory = append(result.Facets.StatutoryCategory, value)
			case "source":
				result.Facets.Source = append(result.Facets.Source, value)
			case "system":
				result.Facets.System = append(result.Facets.System, value)
			case "kind":
				result.Facets.Kind = append(result.Facets.Kind, value)
			}
		}
		if e = facets.Err(); e != nil {
			facets.Close()
			return e
		}
		facets.Close()
		result.Taxonomy, e = readClassificationTaxonomy(ctx, tx, ws)
		return e
	})
	return result, storeError(err)
}

func classificationFilterValue(value string, allowed ...string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	return false
}
