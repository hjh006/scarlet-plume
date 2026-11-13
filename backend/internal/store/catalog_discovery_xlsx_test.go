package store

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/valuefingerprint"
)

func TestXLSXRealDiscoveryStoreQueueClaimSubmissionAndHistoricalFingerprints(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	root := t.TempDir()
	fixture, e := os.ReadFile("../collector/testdata/local-files/sensitive-cells.xlsx")
	must(e)
	filename := filepath.Join(root, "private-workbook-name.xlsx")
	must(os.WriteFile(filename, fixture, 0600))
	must(os.WriteFile(filepath.Join(root, "unsupported.xls"), fixture, 0600))
	rawToken := strings.Repeat("x", 43)
	tokenHash := fmt.Sprintf("%x", sha256.Sum256([]byte(rawToken)))
	key := strings.Repeat("41", 32)
	source, e := f.repo.SaveDiscoverySource(ctx, f.ws.ID, f.user.ID, 0, DiscoverySourceInput{Source: "Actual XLSX file fixture", Provider: "local_files", System: "local_files", Database: "Synthetic worksheet collection", Scopes: []string{"."}, IntervalMinutes: 5, Enabled: true}, tokenHash)
	must(e)
	collect := func() DiscoveryRun {
		t.Helper()
		must(f.repo.QueueDiscoverySource(ctx, f.ws.ID, f.user.ID, source.ID, source.Revision, "collect"))
		claim, e := f.repo.ClaimDiscovery(ctx, tokenHash, fmt.Sprintf("%032x", time.Now().UnixNano()))
		must(e)
		if claim == nil {
			t.Fatal("XLSX source queue was not claimed")
		}
		result, e := collector.DiscoverLocalFiles(ctx, claim.Config, root, collector.FileScanOptions{DigestKey: []byte(rawToken), FingerprintKey: key, FingerprintKeyID: "xlsx-key", FingerprintScope: f.ws.Slug})
		must(e)
		if len(result.Files) != 1 {
			t.Fatal("real XLSX skipped or unsupported XLS parser enabled")
		}
		run, e := f.repo.SubmitDiscovery(ctx, tokenHash, DiscoverySubmission{SourceID: source.ID, Revision: source.Revision, RunRef: claim.RunRef, ObservedAt: time.Now().UTC(), Status: "success", Result: result})
		must(e)
		return run
	}
	run := collect()
	entries, e := f.repo.ListCatalogEntries(ctx, f.ws.ID, "file", "")
	must(e)
	if len(entries) != 1 || !entries[0].Current {
		t.Fatal("XLSX did not persist as a current file asset")
	}
	assetID := entries[0].AssetID
	page, e := f.repo.ClassificationWorkbench(ctx, f.ws.ID, f.user.ID, ClassificationWorkbenchFilter{Kind: "file"})
	must(e)
	if len(page.Items) != 1 || page.Items[0].CandidateCount < 2 {
		t.Fatal("XLSX email/phone candidates not available to classification")
	}
	query, e := valuefingerprint.Create(key, "xlsx-key", f.ws.Slug, "shared.person@example.invalid")
	must(e)
	search, e := f.repo.SearchValueFingerprints(ctx, f.ws.ID, f.user.ID, ValueSearchInput{Token: query})
	must(e)
	if len(search.Items) != 1 || search.Items[0].Locator != "sheet:1:cell:A1" || search.Items[0].AssetID != assetID || search.Items[0].RunID != run.ID || !search.Items[0].Current {
		t.Fatal("XLSX cell fingerprint lost source/run/asset/coordinate binding")
	}
	var evidence []byte
	must(f.pool.QueryRow(ctx, `SELECT evidence FROM plume.file_content_evidence WHERE workspace_id=$1 AND asset_id=$2 AND run_id=$3`, f.ws.ID, assetID, run.ID).Scan(&evidence))
	var file collector.FileEvidence
	must(json.Unmarshal(evidence, &file))
	if file.MediaType != "xlsx" || file.Coverage.UnitsScanned != 4 || len(file.Fingerprints) != 4 || !slices.Contains(file.Coverage.Warnings, "xlsx_formula_not_evaluated") {
		t.Fatal("XLSX recognition/evidence snapshot missing")
	}
	serialized, _ := json.Marshal(entries)
	for _, forbidden := range []string{"person@example.invalid", "13800138000", "private-workbook-name", "Private business label"} {
		if bytes.Contains(serialized, []byte(forbidden)) || bytes.Contains(evidence, []byte(forbidden)) {
			t.Fatal("XLSX raw source values or labels persisted")
		}
	}
	formula, e := valuefingerprint.Create(key, "xlsx-key", f.ws.Slug, "formula.cache@example.invalid")
	must(e)
	empty, e := f.repo.SearchValueFingerprints(ctx, f.ws.ID, f.user.ID, ValueSearchInput{Token: formula})
	must(e)
	if len(empty.Items) != 0 {
		t.Fatal("formula cache became observed data")
	}
	// A later unreadable workbook must not keep the previous positive match current.
	var bomb bytes.Buffer
	z := zip.NewWriter(&bomb)
	w, e := z.Create("xl/large.xml")
	must(e)
	_, e = w.Write(bytes.Repeat([]byte("z"), 9<<20))
	must(e)
	must(z.Close())
	must(os.WriteFile(filename, bomb.Bytes(), 0600))
	later := collect()
	var latest []byte
	must(f.pool.QueryRow(ctx, `SELECT evidence FROM plume.file_content_evidence WHERE run_id=$1`, later.ID).Scan(&latest))
	file = collector.FileEvidence{}
	must(json.Unmarshal(latest, &file))
	if file.Coverage.Status != "unknown" || len(file.Fields) != 0 || len(file.Fingerprints) != 0 || !slices.Contains(file.Coverage.Warnings, "archive_limit") {
		t.Fatal("malicious XLSX was not preserved as unknown")
	}
	page, e = f.repo.ClassificationWorkbench(ctx, f.ws.ID, f.user.ID, ClassificationWorkbenchFilter{Kind: "file"})
	must(e)
	if page.Items[0].CandidateCount != 0 {
		t.Fatal("old XLSX rule hits borrowed by new unknown workbook")
	}
	history, e := f.repo.SearchValueFingerprints(ctx, f.ws.ID, f.user.ID, ValueSearchInput{Token: query})
	must(e)
	if len(history.Items) != 1 || history.Items[0].Current {
		t.Fatal("previous XLSX token retained a current evidence claim")
	}
}
