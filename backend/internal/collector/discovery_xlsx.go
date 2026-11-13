package collector

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var errXLSXBudget = errors.New("xlsx XML budget")
var errXLSXInvalid = errors.New("invalid xlsx structure")
var errXLSXUnsupported = errors.New("unsupported xlsx parts")
var xlsxNumber = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?(?:[Ee][+-]?[0-9]+)?$`)

const xlsxNS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"

func spreadsheetNS(s string) bool {
	return s == xlsxNS || s == "http://purl.oclc.org/ooxml/spreadsheetml/main"
}

type xlsxBudgetReader struct {
	ctx  context.Context
	r    io.Reader
	left *int
}

func (r *xlsxBudgetReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if *r.left <= 0 {
		return 0, errXLSXBudget
	}
	if len(p) > *r.left {
		p = p[:*r.left]
	}
	n, e := r.r.Read(p)
	*r.left -= n
	return n, e
}
func xlsxDecoder(ctx context.Context, part *zip.File, left *int) (*xml.Decoder, io.Closer, error) {
	if part == nil {
		return nil, nil, errXLSXInvalid
	}
	r, e := part.Open()
	if e != nil {
		return nil, nil, e
	}
	return xml.NewDecoder(&xlsxBudgetReader{ctx: ctx, r: r, left: left}), r, nil
}
func xlsxToken(d *xml.Decoder) (xml.Token, error) {
	t, e := d.Token()
	if _, ok := t.(xml.Directive); ok {
		return nil, errXLSXInvalid
	}
	return t, e
}
func xlsxAttr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
func xlsxWarning(e error) string {
	if errors.Is(e, errXLSXUnsupported) {
		return "xlsx_unsupported_parts"
	}
	if errors.Is(e, errXLSXBudget) {
		return "extracted_size_limit"
	}
	return "invalid_document"
}

type xlsxRel struct{ target, kind string }
type xlsxString struct {
	value string
	valid bool
}

func extractXLSX(ctx context.Context, raw []byte) ([]fileUnit, []string) {
	_, parts, warning := openOfficeArchive(raw)
	if warning != "" {
		return nil, []string{warning}
	}
	warnings := []string{"xlsx_cell_values_only"}
	left := MaxExtractedBytes
	for name := range parts {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "vbaproject") || strings.HasPrefix(lower, "xl/embeddings/") || strings.HasPrefix(lower, "xl/activex/") {
			return nil, []string{"xlsx_unsupported_parts"}
		}
		if strings.HasPrefix(lower, "xl/externallinks/") {
			warnings = append(warnings, "xlsx_external_links_not_followed")
		}
	}
	if e := xlsxContentTypes(ctx, parts["[Content_Types].xml"], &left); e != nil {
		return nil, []string{xlsxWarning(e)}
	}
	ids, e := xlsxSheets(ctx, parts["xl/workbook.xml"], &left)
	if e != nil {
		return nil, []string{xlsxWarning(e)}
	}
	rels, external, e := xlsxRelationships(ctx, parts["xl/_rels/workbook.xml.rels"], &left)
	if e != nil {
		return nil, []string{xlsxWarning(e)}
	}
	if external {
		warnings = append(warnings, "xlsx_external_links_not_followed")
	}
	var shared []xlsxString
	for _, rel := range rels {
		if rel.kind == "sharedStrings" {
			if shared != nil {
				return nil, []string{"invalid_document"}
			}
			var w []string
			shared, w, e = xlsxSharedStrings(ctx, parts[rel.target], &left)
			warnings = append(warnings, w...)
			if e != nil {
				return nil, []string{xlsxWarning(e)}
			}
		}
	}
	if len(ids) > 20 {
		ids = ids[:20]
		warnings = append(warnings, "xlsx_sheet_limit")
	}
	units := []fileUnit{}
	visited, textBytes := 0, 0
	for sheet, id := range ids {
		rel, ok := rels[id]
		if !ok || rel.kind != "worksheet" {
			warnings = append(warnings, "xlsx_unsupported_parts")
			continue
		}
		found, w, e := xlsxCells(ctx, parts[rel.target], &left, sheet+1, shared, &visited, &textBytes)
		warnings = append(warnings, w...)
		if e != nil {
			if errors.Is(e, errXLSXBudget) {
				warnings = append(warnings, "extracted_size_limit")
				units = append(units, found...)
				break
			}
			return nil, []string{"invalid_document"}
		}
		units = append(units, found...)
		if visited >= MaxFileUnits {
			warnings = append(warnings, "unit_count_limit")
			break
		}
	}
	slices.Sort(warnings)
	return units, slices.Compact(warnings)
}

func xlsxContentTypes(ctx context.Context, part *zip.File, left *int) error {
	d, r, e := xlsxDecoder(ctx, part, left)
	if e != nil {
		return e
	}
	defer r.Close()
	found, depth := false, 0
	for {
		t, e := xlsxToken(d)
		if e == io.EOF {
			if !found {
				return errXLSXInvalid
			}
			return nil
		}
		if e != nil {
			return e
		}
		switch x := t.(type) {
		case xml.StartElement:
			depth++
			if depth > 32 {
				return errXLSXInvalid
			}
			if depth == 1 && (x.Name.Local != "Types" || x.Name.Space != "http://schemas.openxmlformats.org/package/2006/content-types") {
				return errXLSXInvalid
			}
			if x.Name.Local == "Override" && xlsxAttr(x, "PartName") == "/xl/workbook.xml" {
				if xlsxAttr(x, "ContentType") != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml" {
					return errXLSXUnsupported
				}
				found = true
			}
		case xml.EndElement:
			depth--
		}
	}
}
func xlsxSheets(ctx context.Context, part *zip.File, left *int) ([]string, error) {
	d, r, e := xlsxDecoder(ctx, part, left)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	ids := []string{}
	seen := map[string]bool{}
	depth := 0
	inSheets := false
	for {
		t, e := xlsxToken(d)
		if e == io.EOF {
			if len(ids) == 0 {
				return nil, errXLSXInvalid
			}
			return ids, nil
		}
		if e != nil {
			return nil, e
		}
		switch x := t.(type) {
		case xml.StartElement:
			depth++
			if depth > 32 || depth == 1 && (x.Name.Local != "workbook" || !spreadsheetNS(x.Name.Space)) {
				return nil, errXLSXInvalid
			}
			if depth == 2 && x.Name.Local == "sheets" && spreadsheetNS(x.Name.Space) {
				inSheets = true
			}
			if depth == 3 && inSheets && x.Name.Local == "sheet" && spreadsheetNS(x.Name.Space) {
				id := xlsxAttr(x, "id")
				if id == "" || len(id) > 128 || seen[id] || len(ids) >= 2000 {
					return nil, errXLSXInvalid
				}
				seen[id] = true
				ids = append(ids, id)
			}
		case xml.EndElement:
			if depth == 2 {
				inSheets = false
			}
			depth--
		}
	}
}
func xlsxRelationships(ctx context.Context, part *zip.File, left *int) (map[string]xlsxRel, bool, error) {
	d, r, e := xlsxDecoder(ctx, part, left)
	if e != nil {
		return nil, false, e
	}
	defer r.Close()
	out := map[string]xlsxRel{}
	seen := map[string]bool{}
	depth := 0
	external := false
	for {
		t, e := xlsxToken(d)
		if e == io.EOF {
			return out, external, nil
		}
		if e != nil {
			return nil, false, e
		}
		switch x := t.(type) {
		case xml.StartElement:
			depth++
			if depth > 32 || depth == 1 && (x.Name.Local != "Relationships" || x.Name.Space != "http://schemas.openxmlformats.org/package/2006/relationships") {
				return nil, false, errXLSXInvalid
			}
			if depth != 2 || x.Name.Local != "Relationship" {
				continue
			}
			id, target, typ := xlsxAttr(x, "Id"), xlsxAttr(x, "Target"), xlsxAttr(x, "Type")
			if id == "" || len(id) > 128 || len(seen) >= 2000 || seen[id] {
				return nil, false, errXLSXInvalid
			}
			seen[id] = true
			if xlsxAttr(x, "TargetMode") == "External" || strings.Contains(target, ":") || strings.HasPrefix(target, "//") {
				external = true
				continue
			}
			kind := ""
			for _, k := range []string{"worksheet", "sharedStrings"} {
				if typ == "http://schemas.openxmlformats.org/officeDocument/2006/relationships/"+k || typ == "http://purl.oclc.org/ooxml/officeDocument/relationships/"+k {
					kind = k
				}
			}
			if kind == "" {
				continue
			}
			target = strings.TrimPrefix(target, "/")
			if !fs.ValidPath(target) || strings.ContainsAny(target, "\\%\x00") {
				return nil, false, errXLSXInvalid
			}
			if !strings.HasPrefix(target, "xl/") {
				target = path.Join("xl", target)
			}
			out[id] = xlsxRel{target: target, kind: kind}
		case xml.EndElement:
			depth--
		}
	}
}

// readXLSXText joins ordinary/rich text runs, omitting phonetic annotations.
// It also records formula presence without evaluating it or adopting its cache.
func readXLSXText(d *xml.Decoder, start xml.StartElement) (value, inline string, formula, oversized bool, err error) {
	depth, capture, phonetic := 1, 0, 0
	kind := ""
	var v, text strings.Builder
	for depth > 0 {
		t, e := xlsxToken(d)
		if e != nil {
			return "", "", false, false, e
		}
		switch x := t.(type) {
		case xml.StartElement:
			depth++
			if depth > 32 {
				return "", "", false, false, errXLSXInvalid
			}
			if x.Name.Local == "f" {
				formula = true
			}
			if x.Name.Local == "rPh" {
				phonetic = depth
			}
			if phonetic == 0 && (x.Name.Local == "v" || x.Name.Local == "t") {
				capture = depth
				kind = x.Name.Local
			}
		case xml.CharData:
			if capture > 0 {
				b := &text
				if kind == "v" {
					b = &v
				}
				if b.Len()+len(x) > 4096 {
					oversized = true
				} else {
					b.Write(x)
				}
			}
		case xml.EndElement:
			if depth == capture {
				capture = 0
			}
			if depth == phonetic {
				phonetic = 0
			}
			depth--
		}
	}
	return v.String(), text.String(), formula, oversized, nil
}
func xlsxSharedStrings(ctx context.Context, part *zip.File, left *int) ([]xlsxString, []string, error) {
	d, r, e := xlsxDecoder(ctx, part, left)
	if e != nil {
		return nil, nil, e
	}
	defer r.Close()
	values := []xlsxString{}
	warnings := []string{}
	depth := 0
	for {
		t, e := xlsxToken(d)
		if e == io.EOF {
			return values, warnings, nil
		}
		if e != nil {
			return nil, nil, e
		}
		switch x := t.(type) {
		case xml.StartElement:
			depth++
			if depth > 32 || depth == 1 && (x.Name.Local != "sst" || !spreadsheetNS(x.Name.Space)) {
				return nil, nil, errXLSXInvalid
			}
			if depth == 2 && x.Name.Local == "si" {
				if len(values) >= 2000 {
					return values, append(warnings, "xlsx_shared_string_limit"), nil
				}
				_, text, _, long, e := readXLSXText(d, x)
				if e != nil {
					return nil, nil, e
				}
				values = append(values, xlsxString{value: text, valid: !long})
				if long {
					warnings = append(warnings, "unit_size_limit")
				}
				depth--
			}
		case xml.EndElement:
			depth--
		}
	}
}

func xlsxAddress(address string) (int, bool) {
	i, column := 0, 0
	for i < len(address) && address[i] >= 'A' && address[i] <= 'Z' {
		column = column*26 + int(address[i]-'A'+1)
		i++
		if i > 3 {
			return 0, false
		}
	}
	if i == 0 || i == len(address) || column > 16384 || address[i] == '0' {
		return 0, false
	}
	for _, r := range address[i:] {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	row, e := strconv.Atoi(address[i:])
	return column, e == nil && row > 0 && row <= 1048576
}
func validXLSXLocator(locator string) bool {
	parts := strings.Split(locator, ":")
	if len(parts) != 4 || parts[0] != "sheet" || parts[2] != "cell" {
		return false
	}
	sheet, e := strconv.Atoi(parts[1])
	if e != nil || sheet < 1 || sheet > 20 || strconv.Itoa(sheet) != parts[1] {
		return false
	}
	_, ok := xlsxAddress(parts[3])
	return ok
}

func xlsxCells(ctx context.Context, part *zip.File, left *int, sheet int, shared []xlsxString, visited, textBytes *int) ([]fileUnit, []string, error) {
	d, r, e := xlsxDecoder(ctx, part, left)
	if e != nil {
		return nil, nil, e
	}
	defer r.Close()
	out := []fileUnit{}
	warnings := []string{}
	stack := []string{}
	seen := map[string]bool{}
	for {
		t, e := xlsxToken(d)
		if e == io.EOF {
			return out, warnings, nil
		}
		if e != nil {
			return out, warnings, e
		}
		switch x := t.(type) {
		case xml.StartElement:
			stack = append(stack, x.Name.Local)
			if len(stack) > 32 || len(stack) == 1 && (x.Name.Local != "worksheet" || !spreadsheetNS(x.Name.Space)) {
				return nil, nil, errXLSXInvalid
			}
			if len(stack) != 4 || stack[1] != "sheetData" || stack[2] != "row" || x.Name.Local != "c" {
				continue
			}
			if *visited >= MaxFileUnits {
				return out, append(warnings, "unit_count_limit"), nil
			}
			*visited++
			address := xlsxAttr(x, "r")
			column, valid := xlsxAddress(address)
			if !valid || seen[address] {
				return nil, nil, errXLSXInvalid
			}
			seen[address] = true
			v, text, formula, long, e := readXLSXText(d, x)
			stack = stack[:len(stack)-1]
			if e != nil {
				return out, warnings, e
			}
			if formula {
				warnings = append(warnings, "xlsx_formula_not_evaluated")
				continue
			}
			if long {
				warnings = append(warnings, "unit_size_limit")
				continue
			}
			typ := xlsxAttr(x, "t")
			value := ""
			switch typ {
			case "s":
				index, e := strconv.Atoi(strings.TrimSpace(v))
				if e != nil || index < 0 || index >= len(shared) || !shared[index].valid {
					warnings = append(warnings, "xlsx_shared_string_unavailable")
					continue
				}
				value = shared[index].value
			case "inlineStr":
				value = text
			case "str":
				value = v
			case "", "n":
				value = strings.TrimSpace(v)
				if value != "" && !xlsxNumber.MatchString(value) {
					warnings = append(warnings, "xlsx_unsupported_cell_type")
					continue
				}
				if xlsxAttr(x, "s") != "" || strings.ContainsAny(value, ".Ee") {
					warnings = append(warnings, "xlsx_number_format_not_applied")
				}
			default:
				warnings = append(warnings, "xlsx_unsupported_cell_type")
				continue
			}
			if value == "" {
				continue
			}
			if *textBytes+len(value) > MaxExtractedBytes {
				return out, append(warnings, "extracted_size_limit"), nil
			}
			*textBytes += len(value)
			out = append(out, fileUnit{name: fmt.Sprintf("column_%d", column), kind: "cell", index: *visited, value: value, locator: fmt.Sprintf("sheet:%d:cell:%s", sheet, address)})
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, nil, errXLSXInvalid
			}
			stack = stack[:len(stack)-1]
		}
	}
}
