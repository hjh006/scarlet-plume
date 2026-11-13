package store

import (
	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/jackc/pgx/v5"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPostgreSQLTransportRealSourceSnapshotFreshness(t *testing.T) {
	secure, plain := os.Getenv("SCARLET_TRANSPORT_TLS_TEST_DSN"), os.Getenv("SCARLET_TRANSPORT_PLAIN_TEST_DSN")
	if secure == "" || plain == "" {
		t.Skip("requires actual isolated TLS/plaintext source fixtures")
	}
	for _, tc := range []struct {
		name, dsn             string
		tls, insecure, legacy bool
	}{{"verified-tls", secure, true, false, false}, {"plaintext", plain, false, true, false}, {"legacy-unknown", plain, false, true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := assessmentSourceTestStore(t)
			ctx := t.Context()
			must := func(e error) {
				t.Helper()
				if e != nil {
					t.Fatal(e)
				}
			}
			cfg := ManagedPostgresInput{Host: "127.0.0.1", Port: 54834, Database: "transport_fixture", User: "postgres", Schemas: []string{"transport_fixture"}, IntervalMinutes: 5, Enabled: true, EncryptedPassword: []byte("isolated-local-trust-fixture")}
			job, e := f.repo.CreateManagedPostgresJob(ctx, f.ws.ID, f.user.ID, "Actual transport "+tc.name, discoveryDigest(f.ws.Slug), cfg)
			must(e)
			must(f.repo.QueueManagedPostgresJob(ctx, f.ws.ID, f.user.ID, job.ID))
			claim, e := f.repo.ClaimManagedPostgresJob(ctx, "transport-"+tc.name+f.ws.Slug, time.Minute)
			must(e)
			if claim == nil {
				t.Fatal("source lease missing")
			}
			observed := time.Now().UTC()
			tables, e := collector.InspectPostgreSQL(ctx, tc.dsn, cfg.Schemas, tc.insecure)
			must(e)
			if len(tables) != 1 {
				t.Fatal("unexpected real source scope")
			}
			if tc.legacy {
				tables[0].Transport = nil
			}
			_, e = f.repo.FinishManagedPostgresJob(ctx, *claim, AutomationDelivery{RunID: claim.RunID, Digest: discoveryDigest(tables), ObservedAt: observed, Assets: []CollectedAsset{{SourceRef: tables[0].SourceRef, Name: tables[0].Name}}, PostgresTables: tables})
			must(e)
			assets, e := f.repo.ListAssets(ctx, f.ws.ID, 0, 10)
			must(e)
			if len(assets) != 1 {
				t.Fatal("actual source asset missing")
			}
			read := func() *scenarioPostgresSource {
				var source *scenarioPostgresSource
				must(pgx.BeginTxFunc(ctx, f.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
					var err error
					source, err = readScenarioPostgresSource(ctx, tx, f.ws.ID, assets[0].ID)
					return err
				}))
				return source
			}
			source := read()
			if source == nil || !source.Current {
				t.Fatal("actual source observation not current")
			}
			transport := source.Inspection.Table.Transport
			if tc.legacy {
				if transport != nil {
					t.Fatal("legacy snapshot invented transport")
				}
			} else if transport == nil || transport.TLS != tc.tls {
				t.Fatalf("real transport lost on store snapshot: %+v", transport)
			}
			protection := func(run ScenarioCheckRun) ScenarioCheckFinding {
				t.Helper()
				if len(run.Findings) != 3 {
					t.Fatalf("transport changed default finding count: %d", len(run.Findings))
				}
				for _, finding := range run.Findings {
					if finding.ID == "protection_coverage" {
						return finding
					}
				}
				t.Fatal("protection finding missing")
				return ScenarioCheckFinding{}
			}
			input := ScenarioCheckRunInput{Scenario: "personal", Task: "pia", AssetID: assets[0].ID}
			run, e := f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, input)
			must(e)
			finding := protection(run)
			if finding.Outcome != "needs_verification" || finding.Remediation.Executable {
				t.Fatal("single-hop TLS promoted full protection to compliant or executable")
			}
			missing := strings.Join(finding.MissingConditions, " ")
			for _, required := range []string{"静态加密", "完整有效访问", "备份恢复"} {
				if !strings.Contains(missing, required) {
					t.Fatalf("protection gap lost: %s", required)
				}
			}
			switch {
			case tc.legacy:
				if strings.Contains(finding.Observed, "本次采集器→") || strings.Contains(finding.Observed, "明文") {
					t.Fatal("legacy unknown emitted a current connection fact")
				}
			case tc.tls:
				if !strings.Contains(finding.Observed, transport.Protocol) || !strings.Contains(finding.Observed, transport.Cipher) {
					t.Fatal("actual TLS protocol/cipher not projected")
				}
			default:
				if !strings.Contains(finding.Observed, "明文") {
					t.Fatal("actual non-TLS hop not described")
				}
			}
			cfg.Enabled = false
			_, e = f.repo.UpdateManagedPostgresJob(ctx, f.ws.ID, f.user.ID, job.ID, job.Managed.Revision, cfg)
			must(e)
			if source = read(); source == nil || source.Current {
				t.Fatal("paused/revised source transport remained current")
			}
			staleRun, e := f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, input)
			must(e)
			stale := protection(staleRun)
			if stale.Outcome != "needs_verification" || strings.Contains(stale.Observed, "本次采集器→") || strings.Contains(stale.Observed, "明文") {
				t.Fatal("stale snapshot emitted a current transport fact")
			}
		})
	}
}
