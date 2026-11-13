package store

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
)

func catalogScenarioFinding(t *testing.T, run ScenarioCheckRun) ScenarioCheckFinding {
	t.Helper()
	for _, finding := range run.Findings {
		if finding.ID == "classification_consistency" {
			return finding
		}
	}
	t.Fatal("catalog classification finding missing")
	return ScenarioCheckFinding{}
}

func TestScenarioCatalogClassificationPositiveScopeAndInvalidation(t *testing.T) {
	for _, provider := range []string{"mysql", "mariadb", "mongodb", "openmetadata"} {
		t.Run(provider, func(t *testing.T) {
			f := assessmentSourceTestStore(t)
			ctx := t.Context()
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			token := fmt.Sprintf("%x", sha256.Sum256([]byte(f.ws.Slug)))
			system := provider
			if provider == "openmetadata" {
				system = "mysql"
			}
			source, err := f.repo.SaveDiscoverySource(ctx, f.ws.ID, f.user.ID, 0, DiscoverySourceInput{Source: "catalog-technical-" + provider, Provider: provider, System: system, Database: "inventory", Service: "SyntheticWarehouse", IntervalMinutes: 5, Enabled: true}, token)
			must(err)
			_, err = f.repo.SaveDiscoveryRule(ctx, f.ws.ID, f.user.ID, collector.DiscoveryRule{Key: "customer.label", Version: 1, Name: "Synthetic dictionary candidate", Kind: "dictionary", Target: "content", Category: "national_id", Dictionary: []string{"synthetic"}, Status: "published"})
			must(err)
			table := collector.DiscoveryTable{Database: "inventory", Schema: "inventory", Name: "customers", PermissionsStatus: "unknown", ScannedRows: 1, Fields: []collector.DiscoveryField{
				{Name: "email", Type: "text", Sensitivity: "email", Hits: []collector.DiscoveryHit{{RuleKey: "builtin.email", RuleVersion: 1, Category: "email", Method: "bounded_customer_sample", MatchCount: 1, ScannedCount: 1}}},
				{Name: "department", Type: "text", Hits: []collector.DiscoveryHit{{RuleKey: "customer.label", RuleVersion: 1, Category: "national_id", Method: "bounded_customer_sample", MatchCount: 1, ScannedCount: 1}}},
			}}
			coverage := collector.DiscoveryCoverage{Metadata: "observed", Content: "bounded_sample", Permissions: "unknown", Lineage: "unknown", RowLimit: 200, TableLimit: 100}
			if provider == "mongodb" {
				table.Schema = ""
				table.Warnings = []string{"sample_inferred_schema", "permissions_unknown", "lineage_unknown"}
			}
			if provider == "openmetadata" {
				table.ScannedRows = 0
				coverage.Metadata, coverage.Content = "openmetadata_declared_columns_comments_tags_constraints", "not_collected"
				table.Fields[0].Hits = []collector.DiscoveryHit{{RuleKey: "builtin.metadata.personal", RuleVersion: 1, Category: "personal_information", Method: "metadata", MatchCount: 1, ScannedCount: 1}}
				table.Fields[1].Hits = nil
			}
			counter := 0
			collect := func(status string) {
				t.Helper()
				must(f.repo.QueueDiscoverySource(ctx, f.ws.ID, f.user.ID, source.ID, source.Revision, "collect"))
				counter++
				claim, err := f.repo.ClaimDiscovery(ctx, token, fmt.Sprintf("%032x", counter))
				must(err)
				if claim == nil {
					t.Fatal("missing source lease")
				}
				input := DiscoverySubmission{SourceID: source.ID, Revision: source.Revision, RunRef: claim.RunRef, Status: status, ObservedAt: time.Now().UTC(), Result: collector.DiscoveryResult{Tables: []collector.DiscoveryTable{table}, Coverage: coverage}}
				if status == "error" {
					input.ErrorCode = "collection_failed"
				}
				_, err = f.repo.SubmitDiscovery(ctx, token, input)
				must(err)
			}
			collect("success")
			entries, err := f.repo.ListCatalogEntries(ctx, f.ws.ID, "", "")
			must(err)
			var tableAsset, emailAsset, departmentAsset int64
			for _, entry := range entries {
				if entry.Kind == "table" {
					tableAsset = entry.AssetID
				}
				if entry.Kind == "field" && entry.Field == "email" {
					emailAsset = entry.AssetID
				}
				if entry.Kind == "field" && entry.Field == "department" {
					departmentAsset = entry.AssetID
				}
			}
			classify := func(assetID int64, category string) {
				t.Helper()
				detail, err := f.repo.GetAssetClassification(ctx, f.ws.ID, f.user.ID, assetID)
				must(err)
				_, err = f.repo.UpdateAssetClassification(ctx, f.ws.ID, f.user.ID, assetID, AssetClassificationInput{Revision: detail.Revision, Category: category, BusinessLevel: "internal", StatutoryCategory: "general", Basis: "Synthetic confirmed classification for bounded comparison"})
				must(err)
			}
			check := func(assetID int64, previous *int64) ScenarioCheckRun {
				t.Helper()
				run, err := f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, ScenarioCheckRunInput{Scenario: "personal", Task: "inventory", AssetID: assetID, PreviousRunID: previous})
				must(err)
				return run
			}
			classify(emailAsset, "personal")
			classify(departmentAsset, "business")
			if finding := catalogScenarioFinding(t, check(emailAsset, nil)); finding.Outcome != "needs_verification" {
				t.Fatal("manual edit reused old collection")
			}
			collect("success")
			fieldRun := check(emailAsset, nil)
			want := "compliant"
			if provider == "openmetadata" {
				want = "needs_verification"
			}
			if finding := catalogScenarioFinding(t, fieldRun); finding.Outcome != want || finding.Remediation.Executable {
				t.Fatalf("wrong field scope outcome: %+v", finding)
			}
			// The parent remains automatically classified. Its manual state must not
			// prevent comparison of the independently confirmed field's own signal.
			if finding := catalogScenarioFinding(t, check(departmentAsset, nil)); finding.Outcome != "needs_verification" {
				t.Fatal("custom label or another field's format was promoted")
			}
			for _, evidence := range fieldRun.Evidence {
				if evidence.ID != "catalog_assessment" {
					continue
				}
				var projected struct {
					Fields       []collector.DiscoveryField `json:"fields"`
					Current      bool                       `json:"current"`
					RulesCurrent bool                       `json:"rulesCurrent"`
				}
				must(json.Unmarshal(evidence.Snapshot, &projected))
				if evidence.Source != "discovery" || !projected.Current || !projected.RulesCurrent || len(projected.Fields) != 1 || projected.Fields[0].Name != "email" {
					t.Fatalf("field evidence leaked another field or lost provenance: %+v", projected)
				}
			}
			classify(tableAsset, "personal")
			collect("success")
			tableRun := check(tableAsset, nil)
			finding := catalogScenarioFinding(t, tableRun)
			if finding.Outcome != want || !strings.Contains(finding.Coverage, "有界") || finding.RuleVersion != "catalog-bounded-rules-v1" {
				t.Fatalf("catalog positive boundary not preserved: %+v", finding)
			}
			if finding.Outcome == "compliant" && (!strings.Contains(finding.Remediation.Advice, "维持") || !strings.Contains(finding.Remediation.Reason, "无需发起修复") || finding.Remediation.Executable || finding.Remediation.Target != nil) {
				t.Fatalf("compliant G02 retained repair-required guidance: %+v", finding.Remediation)
			}
			if provider == "openmetadata" {
				return
			}
			same := check(tableAsset, &tableRun.ID)
			if same.RetestState != "same_source_observation" {
				t.Fatal("same catalog batch became a new source retest")
			}
			collect("success")
			newer := check(tableAsset, &tableRun.ID)
			if newer.RetestState != "new_source_observation" {
				t.Fatal("new catalog source batch did not become a source retest")
			}
			_, err = f.pool.Exec(ctx, `UPDATE plume.discovery_runs SET observed_at=now()-interval '2 hours' WHERE workspace_id=$1 AND source_id=$2 AND id=(SELECT latest_run_id FROM plume.discovery_sources WHERE id=$2)`, f.ws.ID, source.ID)
			must(err)
			if finding := catalogScenarioFinding(t, check(tableAsset, nil)); finding.Outcome != "needs_verification" {
				t.Fatal("expired positive catalog batch retained a classification conclusion")
			}
			warnings := slices.Clone(table.Warnings)
			table.Warnings = append(slices.Clone(warnings), "sample_incomplete")
			collect("success")
			if finding := catalogScenarioFinding(t, check(tableAsset, nil)); finding.Outcome != "needs_verification" {
				t.Fatal("incomplete positive sample became a classification conclusion")
			}
			table.Warnings = warnings
			collect("success")
			classify(tableAsset, "business")
			collect("success")
			if finding := catalogScenarioFinding(t, check(tableAsset, nil)); finding.Outcome != "non_compliant" {
				t.Fatalf("fresh positive classification conflict not detected: %+v", finding)
			}
			_, err = f.repo.SaveDiscoveryRule(ctx, f.ws.ID, f.user.ID, collector.DiscoveryRule{Key: "customer.label", Version: 2, Name: "Synthetic revised dictionary", Kind: "dictionary", Target: "content", Category: "national_id", Dictionary: []string{"synthetic"}, Status: "published"})
			must(err)
			if finding := catalogScenarioFinding(t, check(tableAsset, nil)); finding.Outcome != "needs_verification" {
				t.Fatal("changed rule version reused a previously positive batch")
			}
			table.Fields[1].Hits[0].RuleVersion = 2
			collect("success")
			configuration := source.DiscoverySourceInput
			configuration.IntervalMinutes = 10
			source, err = f.repo.SaveDiscoverySource(ctx, f.ws.ID, f.user.ID, source.ID, configuration, "")
			must(err)
			if finding := catalogScenarioFinding(t, check(tableAsset, nil)); finding.Outcome != "needs_verification" {
				t.Fatal("new source configuration reused a positive older revision")
			}
			collect("success")
			collect("error")
			if finding := catalogScenarioFinding(t, check(tableAsset, nil)); finding.Outcome != "needs_verification" {
				t.Fatal("failed latest collection retained a classification conclusion")
			}
			_, err = f.pool.Exec(ctx, `UPDATE plume.discovery_sources SET enabled=false WHERE workspace_id=$1 AND id=$2`, f.ws.ID, source.ID)
			must(err)
			if finding := catalogScenarioFinding(t, check(emailAsset, nil)); finding.Outcome != "needs_verification" {
				t.Fatal("disabled source retained a classification conclusion")
			}
		})
	}
}
