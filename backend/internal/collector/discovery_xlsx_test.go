package collector

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hjh006/scarlet-plume/backend/internal/valuefingerprint"
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
)

func xlsxFixtureBytes(t *testing.T) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/local-files/sensitive-cells.xlsx")
	if e != nil {
		t.Fatal(e)
	}
	return b
}

type officeTestPart struct {
	name string
	body []byte
}

func rewriteOfficeFixture(t *testing.T, raw []byte, replace map[string]string, appendParts ...officeTestPart) []byte {
	t.Helper()
	reader, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	for _, part := range reader.File {
		r, e := part.Open()
		if e != nil {
			t.Fatal(e)
		}
		body, e := io.ReadAll(r)
		r.Close()
		if e != nil {
			t.Fatal(e)
		}
		if changed, ok := replace[part.Name]; ok {
			body = []byte(changed)
		}
		w, e := writer.Create(part.Name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write(body); e != nil {
			t.Fatal(e)
		}
	}
	for _, part := range appendParts {
		w, e := writer.Create(part.name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write(part.body); e != nil {
			t.Fatal(e)
		}
	}
	if e = writer.Close(); e != nil {
		t.Fatal(e)
	}
	return out.Bytes()
}
func TestXLSXActualCellsPrivateLocationsAndFingerprints(t *testing.T) {
	root := t.TempDir()
	raw := xlsxFixtureBytes(t)
	if e := os.WriteFile(filepath.Join(root, "private-source-name.xlsx"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	cfg := DiscoveryConfig{Provider: "local_files", System: "local_files", Database: "Synthetic workbook", Scopes: []string{"."}, Rules: BuiltinDiscoveryRules()}
	options := FileScanOptions{DigestKey: bytes.Repeat([]byte{7}, 32), FingerprintKey: strings.Repeat("42", 32), FingerprintKeyID: "xlsx-test", FingerprintScope: "workspace-data"}
	result, e := DiscoverLocalFiles(t.Context(), cfg, root, options)
	if e != nil {
		t.Fatal(e)
	}
	if e = ValidateDiscoveryResult(cfg, result); e != nil {
		t.Fatal(e)
	}
	if len(result.Files) != 1 {
		t.Fatal("XLSX was silently skipped")
	}
	file := result.Files[0]
	if file.MediaType != "xlsx" || file.Coverage.Status != "partial" || file.Coverage.UnitsScanned != 4 {
		t.Fatal("bounded XLSX cell coverage missing")
	}
	if !slices.Contains(file.Coverage.Warnings, "xlsx_formula_not_evaluated") {
		t.Fatal("formula scope not disclosed")
	}
	for value, locator := range map[string]string{"shared.person@example.invalid": "sheet:1:cell:A1", "inline.person@example.invalid": "sheet:1:cell:B1", "13800138000": "sheet:1:cell:C1", "second.sheet@example.invalid": "sheet:2:cell:A2"} {
		token, e := valuefingerprint.Create(options.FingerprintKey, options.FingerprintKeyID, options.FingerprintScope, value)
		if e != nil {
			t.Fatal(e)
		}
		if !slices.ContainsFunc(file.Fingerprints, func(fp valuefingerprint.Evidence) bool { return fp.Digest == token.Digest && fp.Locator == locator }) {
			t.Fatalf("missing real cell fingerprint at %s", locator)
		}
	}
	if len(file.Fingerprints) != 4 {
		t.Fatal("formula cache, unused shared string or phonetic text was inspected")
	}
	serialized, _ := json.Marshal(result)
	for _, forbidden := range []string{"person@example.invalid", "13800138000", "Private business label", "Second private label", "private-source-name", root, "HYPERLINK"} {
		if bytes.Contains(serialized, []byte(forbidden)) {
			t.Fatal("raw cell, sheet label, formula or path escaped local extraction")
		}
	}
}

func TestXLSXArchiveAndCellBudgetsUnknownPartsAndExternalLinks(t *testing.T) {
	base := xlsxFixtureBytes(t)
	for _, tc := range []struct {
		name    string
		parts   []officeTestPart
		warning string
	}{
		{"zip-bomb", []officeTestPart{{"xl/large.xml", bytes.Repeat([]byte("z"), 9<<20)}}, "archive_limit"},
		{"parent-path", []officeTestPart{{"../outside.xml", []byte("x")}}, "archive_path_invalid"},
		{"absolute-path", []officeTestPart{{"/outside.xml", []byte("x")}}, "archive_path_invalid"},
		{"backslash-path", []officeTestPart{{`xl\outside.xml`, []byte("x")}}, "archive_path_invalid"},
		{"duplicate-part", []officeTestPart{{"xl/sharedStrings.xml", []byte("x")}}, "archive_duplicate_entry"},
		{"case-alias-part", []officeTestPart{{"XL/SHAREDSTRINGS.XML", []byte("x")}}, "archive_duplicate_entry"},
		{"macro", []officeTestPart{{"xl/vbaProject.bin", []byte("do-not-execute")}}, "xlsx_unsupported_parts"},
		{"embedding", []officeTestPart{{"xl/embeddings/object.bin", []byte("do-not-open")}}, "xlsx_unsupported_parts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			units, warnings := extractXLSX(t.Context(), rewriteOfficeFixture(t, base, nil, tc.parts...))
			if len(units) != 0 || !slices.Contains(warnings, tc.warning) {
				t.Fatalf("unsupported archive was not explicit: %v", warnings)
			}
		})
	}
	var accesses atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { accesses.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	rels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="` + server.URL + `/sheet.xml" TargetMode="External"/></Relationships>`
	units, warnings := extractXLSX(t.Context(), rewriteOfficeFixture(t, base, map[string]string{"xl/_rels/workbook.xml.rels": rels}))
	if len(units) != 0 || !slices.Contains(warnings, "xlsx_external_links_not_followed") || accesses.Load() != 0 {
		t.Fatal("external workbook link was followed or not disclosed")
	}
	var cells strings.Builder
	for n := 1; n <= 201; n++ {
		cells.WriteString(`<row r="` + strconv.Itoa(n) + `"><c r="A` + strconv.Itoa(n) + `" t="inlineStr"><is><t>synthetic@example.invalid</t></is></c></row>`)
	}
	sheet := `<worksheet xmlns="` + xlsxNS + `"><sheetData>` + cells.String() + `</sheetData></worksheet>`
	units, warnings = extractXLSX(t.Context(), rewriteOfficeFixture(t, base, map[string]string{"xl/worksheets/sheet1.xml": sheet}))
	if len(units) != 200 || !slices.Contains(warnings, "unit_count_limit") {
		t.Fatal("XLSX cell budget not enforced")
	}
}

func TestOfficeGuardAndLegacyUnsupportedFilesRemainConservative(t *testing.T) {
	root := t.TempDir()
	for _, ext := range []string{".xls", ".xlsm", ".xlsb", ".zip"} {
		if e := os.WriteFile(filepath.Join(root, "legacy"+ext), xlsxFixtureBytes(t), 0600); e != nil {
			t.Fatal(e)
		}
	}
	result, e := DiscoverLocalFiles(t.Context(), DiscoveryConfig{Provider: "local_files", System: "local_files", Database: "Legacy", Scopes: []string{"."}}, root, FileScanOptions{DigestKey: bytes.Repeat([]byte{1}, 32)})
	if e != nil {
		t.Fatal(e)
	}
	if len(result.Files) != 0 {
		t.Fatal("unknown legacy parser was enabled")
	}
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	w, _ := z.Create("word/document.xml")
	_, _ = w.Write([]byte(`<document><p><t>synthetic@example.invalid</t></p></document>`))
	_ = z.Close()
	for _, part := range []officeTestPart{{"../outside.xml", []byte("x")}, {"word/document.xml", []byte("duplicate")}} {
		units, warnings := extractZipXML(rewriteOfficeFixture(t, out.Bytes(), nil, part), ".docx")
		if len(units) != 0 || len(warnings) == 0 {
			t.Fatal("existing Office parser bypassed common archive guard")
		}
	}
}

func TestXLSXAdditionalXMLAndCoordinateBoundaries(t *testing.T) {
	base := xlsxFixtureBytes(t)
	t.Run("actual-XML-byte-budget", func(t *testing.T) {
		large := `<sst xmlns="` + xlsxNS + `"><si><t>` + strings.Repeat("a", MaxExtractedBytes+1000) + `</t></si></sst>`
		units, warnings := extractXLSX(t.Context(), rewriteOfficeFixture(t, base, map[string]string{"xl/sharedStrings.xml": large}))
		if len(units) != 0 || !slices.Contains(warnings, "extracted_size_limit") {
			t.Fatal("actual XML expansion bypassed budget")
		}
	})
	t.Run("entry-count", func(t *testing.T) {
		parts := make([]officeTestPart, 2001)
		for n := range parts {
			parts[n] = officeTestPart{name: "extra/part" + strconv.Itoa(n) + ".xml", body: []byte("x")}
		}
		units, warnings := extractXLSX(t.Context(), rewriteOfficeFixture(t, base, nil, parts...))
		if len(units) != 0 || !slices.Contains(warnings, "archive_limit") {
			t.Fatal("ZIP entry count not bounded")
		}
	})
	for _, address := range []string{"A+1", "A01", "XFE1", "A1048577"} {
		t.Run("invalid-coordinate-"+address, func(t *testing.T) {
			sheet := `<worksheet xmlns="` + xlsxNS + `"><sheetData><row r="1"><c r="` + address + `" t="inlineStr"><is><t>synthetic@example.invalid</t></is></c></row></sheetData></worksheet>`
			units, warnings := extractXLSX(t.Context(), rewriteOfficeFixture(t, base, map[string]string{"xl/worksheets/sheet1.xml": sheet}))
			if len(units) != 0 || !slices.Contains(warnings, "invalid_document") {
				t.Fatal("non-canonical cell coordinate adopted")
			}
		})
	}
	t.Run("relationship-parent-path", func(t *testing.T) {
		rels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="../outside.xml"/></Relationships>`
		units, warnings := extractXLSX(t.Context(), rewriteOfficeFixture(t, base, map[string]string{"xl/_rels/workbook.xml.rels": rels}))
		if len(units) != 0 || !slices.Contains(warnings, "invalid_document") {
			t.Fatal("relationship escaped canonical package scope")
		}
	})
	t.Run("XML-entity-not-resolved", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
		defer server.Close()
		shared := `<!DOCTYPE sst [<!ENTITY fetch SYSTEM "` + server.URL + `/never-fetch">]><sst xmlns="` + xlsxNS + `"><si><t>&fetch;</t></si></sst>`
		units, warnings := extractXLSX(t.Context(), rewriteOfficeFixture(t, base, map[string]string{"xl/sharedStrings.xml": shared}))
		if len(units) != 0 || !slices.Contains(warnings, "invalid_document") || requests.Load() != 0 {
			t.Fatal("XML directive not kept unknown")
		}
	})
	t.Run("inline-only-without-shared-table", func(t *testing.T) {
		workbook := `<workbook xmlns="` + xlsxNS + `" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="private label" sheetId="1" r:id="rId1"/></sheets></workbook>`
		rels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`
		sheet := `<worksheet xmlns="` + xlsxNS + `"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>inline@example.invalid</t></is></c><c r="B1"><v>13800138000</v></c></row></sheetData></worksheet>`
		units, warnings := extractXLSX(t.Context(), rewriteOfficeFixture(t, base, map[string]string{"xl/workbook.xml": workbook, "xl/_rels/workbook.xml.rels": rels, "xl/worksheets/sheet1.xml": sheet}))
		if len(units) != 2 || slices.Contains(warnings, "xlsx_shared_string_unavailable") {
			t.Fatal("valid inline-only workbook required shared strings")
		}
	})
}

func TestXLSXDiscoveryDoesNotAuthorizeTheTextOnlyOutboundGuard(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	if e := os.WriteFile(filepath.Join(from, "sensitive.xlsx"), xlsxFixtureBytes(t), 0600); e != nil {
		t.Fatal(e)
	}
	policy := datasafety.Policy{Version: "unchanged-text-guard", AllowedChannels: []string{"file_copy"}, ProtectedCategories: []string{"email", "phone"}}
	context, decision, e := datasafety.CopyText(policy, from, to, "sensitive.xlsx", "copied.xlsx")
	if !errors.Is(e, datasafety.ErrUnknown) || context.Capture != "unknown" || decision.Outcome != "unknown" {
		t.Fatal("XLSX discovery support expanded the separate outbound guard")
	}
	if _, e = os.Stat(filepath.Join(to, "copied.xlsx")); !os.IsNotExist(e) {
		t.Fatal("text-only guard created an XLSX destination")
	}
}
