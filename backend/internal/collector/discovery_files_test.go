package collector

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalFileDiscoveryPrivateBoundedEvidence(t *testing.T) {
	root := t.TempDir()
	sensitive := "private.person@example.invalid"
	for name, raw := range map[string]string{"notes.txt": "Contact " + sensitive + " for support", "contacts.csv": "email,department\n" + sensitive + ",Synthetic\n", "nested.json": "{\"record\":{\"email\":\"" + sensitive + "\"}}"} {
		if e := os.WriteFile(filepath.Join(root, name), []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, _ := z.Create("word/document.xml")
	_, _ = w.Write([]byte(`<w:document xmlns:w="urn:w"><w:body><w:p><w:r><w:t>Contact ` + sensitive + `</w:t></w:r></w:p></w:body></w:document>`))
	_ = z.Close()
	_ = os.WriteFile(filepath.Join(root, "note.docx"), buf.Bytes(), 0600)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	_ = os.WriteFile(outside, []byte(sensitive), 0600)
	_ = os.Symlink(outside, filepath.Join(root, "link.txt"))
	cfg := DiscoveryConfig{Provider: "local_files", System: "local_files", Database: "fixture", Scopes: []string{"."}, Rules: BuiltinDiscoveryRules()}
	options := FileScanOptions{DigestKey: bytes.Repeat([]byte{7}, 32), FingerprintKey: strings.Repeat("42", 32), FingerprintKeyID: "fixture-key", FingerprintScope: "fixture-workspace"}
	result, e := DiscoverLocalFiles(t.Context(), cfg, root, options)
	if e != nil {
		t.Fatal(e)
	}
	if len(result.Files) != 4 {
		t.Fatalf("wrong bounded local files: %d", len(result.Files))
	}
	if e = ValidateDiscoveryResult(cfg, result); e != nil {
		t.Fatal(e)
	}
	for _, f := range result.Files {
		found := false
		for _, field := range f.Fields {
			for _, hit := range field.Hits {
				if hit.Category == "email" {
					found = true
				}
			}
		}
		if !found || len(f.Fingerprints) == 0 {
			t.Fatalf("missing real text detection: %+v", f)
		}
	}
	raw, _ := json.Marshal(result)
	for _, forbidden := range []string{sensitive, root, "notes.txt", "contacts.csv", "nested.json", "outside"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatal("raw source content or path escaped local extraction")
		}
	}
	cfg.Scopes = []string{"../"}
	if _, e = DiscoverLocalFiles(t.Context(), cfg, root, options); e == nil {
		t.Fatal("parent scope accepted")
	}
	cfg.Scopes = []string{"link.txt"}
	if _, e = DiscoverLocalFiles(t.Context(), cfg, root, options); e == nil {
		t.Fatal("symlink scope accepted")
	}
}
func TestFileExtractionBudgetsAndDisabledNativeTools(t *testing.T) {
	if units, w := extractFileUnits(t.Context(), []byte("not-pdf"), ".pdf", FileScanOptions{}); len(units) != 0 || len(w) != 1 || w[0] != "extractor_disabled" {
		t.Fatal("PDF executed without opt-in")
	}
	if units, w := extractFileUnits(t.Context(), []byte("not-image"), ".png", FileScanOptions{}); len(units) != 0 || len(w) != 1 || w[0] != "ocr_disabled" {
		t.Fatal("OCR executed without opt-in")
	}
	if units, w := textUnits([]byte(strings.Repeat("line\n", 201)), "line"); len(units) != 200 || len(w) == 0 {
		t.Fatal("unit limit ignored")
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, _ := z.Create("word/document.xml")
	_, _ = w.Write(bytes.Repeat([]byte("a"), 9<<20))
	_ = z.Close()
	if _, warnings := extractZipXML(buf.Bytes(), ".docx"); len(warnings) != 1 || warnings[0] != "archive_limit" {
		t.Fatal("archive expansion budget ignored")
	}
}

func TestLocalFileNativeExtractorsFixture(t *testing.T) {
	root := os.Getenv("SCARLET_FILE_NATIVE_FIXTURE_DIR")
	if root == "" {
		t.Skip("requires explicit synthetic native PDF/OCR fixtures")
	}
	cfg := DiscoveryConfig{Provider: "local_files", System: "local_files", Database: "native-fixture", Scopes: []string{"."}, Rules: BuiltinDiscoveryRules()}
	result, e := DiscoverLocalFiles(t.Context(), cfg, root, FileScanOptions{DigestKey: bytes.Repeat([]byte{9}, 32), PDF: true, OCR: true, OCRLanguages: "eng"})
	if e != nil {
		t.Fatal(e)
	}
	if e = ValidateDiscoveryResult(cfg, result); e != nil {
		t.Fatal(e)
	}
	found := map[string]bool{}
	for _, file := range result.Files {
		if file.MediaType != "pdf" && file.MediaType != "png" {
			continue
		}
		for _, field := range file.Fields {
			for _, hit := range field.Hits {
				if file.MediaType == "pdf" && hit.Category == "email" || file.MediaType == "png" && hit.Category == "phone" {
					found[file.MediaType] = true
				}
			}
		}
	}
	if !found["pdf"] || !found["png"] {
		t.Fatalf("actual Poppler email/OCR phone detection missing: %+v", result.Files)
	}
}
