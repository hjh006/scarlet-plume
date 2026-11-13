package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/valuefingerprint"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileDiscoveryRealCollectionAndScopedValueSearch(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	rawToken := strings.Repeat("a", 43)
	sum := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(sum[:])
	root := t.TempDir()
	value := "actual.synthetic@example.invalid"
	must(os.WriteFile(filepath.Join(root, "contains-private-name.txt"), []byte("Contact "+value+" today"), 0600))
	source, e := f.repo.SaveDiscoverySource(ctx, f.ws.ID, f.user.ID, 0, DiscoverySourceInput{Source: "Local test files", Provider: "local_files", System: "local_files", Database: "Evidence folder", Scopes: []string{"."}, IntervalMinutes: 5, Enabled: true}, tokenHash)
	must(e)
	claim, e := f.repo.ClaimDiscovery(ctx, tokenHash, fmt.Sprintf("%032x", time.Now().UnixNano()))
	must(e)
	if claim == nil {
		t.Fatal("file task not claimable")
	}
	key := strings.Repeat("31", 32)
	result, e := collector.DiscoverLocalFiles(ctx, claim.Config, root, collector.FileScanOptions{DigestKey: []byte(rawToken), FingerprintKey: key, FingerprintKeyID: "test-key", FingerprintScope: f.ws.Slug})
	must(e)
	run, e := f.repo.SubmitDiscovery(ctx, tokenHash, DiscoverySubmission{SourceID: source.ID, Revision: source.Revision, RunRef: claim.RunRef, Status: "success", ObservedAt: time.Now().UTC(), Result: result})
	must(e)
	entries, e := f.repo.ListCatalogEntries(ctx, f.ws.ID, "file", "")
	must(e)
	if len(entries) != 1 || entries[0].AssetID < 1 || !entries[0].Current {
		t.Fatalf("missing actual file asset: %+v", entries)
	}
	page, e := f.repo.ClassificationWorkbench(ctx, f.ws.ID, f.user.ID, ClassificationWorkbenchFilter{Kind: "file"})
	must(e)
	if len(page.Items) != 1 || page.Items[0].CandidateCount < 1 {
		t.Fatal("real file candidates missing from workbench")
	}
	token, e := valuefingerprint.Create(key, "test-key", f.ws.Slug, value)
	must(e)
	found, e := f.repo.SearchValueFingerprints(ctx, f.ws.ID, f.user.ID, ValueSearchInput{Token: token})
	must(e)
	if len(found.Items) != 1 || found.Items[0].RunID != run.ID || found.Items[0].AssetID != entries[0].AssetID || found.Items[0].Locator != "line:1" || !found.Items[0].Current {
		t.Fatalf("fingerprint lost scope/provenance: %+v", found)
	}
	serialized, _ := json.Marshal(entries)
	if bytes.Contains(serialized, []byte(value)) || bytes.Contains(serialized, []byte("contains-private-name")) {
		t.Fatal("raw content/path persisted")
	}
	other := assessmentSourceTestStore(t)
	empty, e := other.repo.SearchValueFingerprints(ctx, other.ws.ID, other.user.ID, ValueSearchInput{Token: token})
	must(e)
	if len(empty.Items) != 0 {
		t.Fatal("fingerprint crossed workspace")
	}
	_, e = f.repo.SaveDiscoveryRule(ctx, f.ws.ID, f.user.ID, collector.DiscoveryRule{Key: "changed-file-policy", Version: 1, Name: "Changed policy", Kind: "keyword", Target: "content", Category: "custom-business", Expression: "new policy", Status: "published"})
	must(e)
	changedPage, e := f.repo.ClassificationWorkbench(ctx, f.ws.ID, f.user.ID, ClassificationWorkbenchFilter{Kind: "file"})
	must(e)
	if len(changedPage.Items) != 1 || !changedPage.Items[0].NeedsReview {
		t.Fatal("new published rules did not invalidate the leased file rule snapshot")
	}
	source.Enabled = false
	_, e = f.repo.SaveDiscoverySource(ctx, f.ws.ID, f.user.ID, source.ID, source.DiscoverySourceInput, "")
	must(e)
	stale, e := f.repo.SearchValueFingerprints(ctx, f.ws.ID, f.user.ID, ValueSearchInput{Token: token})
	must(e)
	if len(stale.Items) != 1 || stale.Items[0].Current {
		t.Fatal("historical fingerprint presented as current")
	}
}
