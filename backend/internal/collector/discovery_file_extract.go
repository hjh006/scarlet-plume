package collector

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// boundedOutput stops child processes and archive expansion at the same budget.
type boundedOutput struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		b.exceeded = true
		return 0, io.ErrShortWrite
	}
	return b.Buffer.Write(p)
}
func localExtractor(ctx context.Context, binary string, args []string, raw []byte) ([]byte, string) {
	executable, e := exec.LookPath(binary)
	if e != nil {
		return nil, "extractor_unavailable"
	}
	child, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, executable, args...)
	cmd.Stdin = bytes.NewReader(raw)
	out := &boundedOutput{limit: MaxExtractedBytes}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		if out.exceeded {
			return out.Bytes(), "extracted_size_limit"
		}
		return nil, "extractor_failed"
	}
	return out.Bytes(), ""
}
func extractFileUnits(ctx context.Context, raw []byte, ext string, o FileScanOptions) ([]fileUnit, []string) {
	switch ext {
	case ".txt":
		return textUnits(raw, "line")
	case ".csv":
		if !utf8.Valid(raw) {
			return nil, []string{"invalid_utf8"}
		}
		truncated := len(raw) > MaxExtractedBytes
		if len(raw) > MaxExtractedBytes {
			raw = raw[:MaxExtractedBytes]
		}
		reader := csv.NewReader(bytes.NewReader(raw))
		reader.FieldsPerRecord = -1
		units := []fileUnit{}
		row := 0
		for {
			record, e := reader.Read()
			if e == io.EOF {
				break
			}
			if e != nil {
				return units, []string{"invalid_document"}
			}
			row++
			for column, value := range record {
				if len(units) >= 200 {
					return units, []string{"unit_count_limit"}
				}
				units = append(units, fileUnit{name: "column_" + strconv.Itoa(column+1), kind: "row", index: row, value: value})
			}
		}
		if truncated {
			return units, []string{"extracted_size_limit"}
		}
		return units, nil
	case ".json", ".jsonl":
		if !utf8.Valid(raw) {
			return nil, []string{"invalid_utf8"}
		}
		if len(raw) > MaxExtractedBytes {
			return nil, []string{"extracted_size_limit"}
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		units := []fileUnit{}
		warnings := []string{}
		var walk func(any, int)
		walk = func(v any, depth int) {
			if depth > 8 {
				warnings = append(warnings, "unit_count_limit")
				return
			}
			if len(units) >= 200 {
				warnings = append(warnings, "unit_count_limit")
				return
			}
			switch x := v.(type) {
			case map[string]any:
				keys := []string{}
				for key := range x {
					keys = append(keys, key)
				}
				slices.Sort(keys)
				for _, key := range keys {
					walk(x[key], depth+1)
				}
			case []any:
				for _, item := range x {
					walk(item, depth+1)
				}
			case string:
				units = append(units, fileUnit{name: "text", kind: "value", index: len(units) + 1, value: x})
			case json.Number:
				units = append(units, fileUnit{name: "text", kind: "value", index: len(units) + 1, value: string(x)})
			}
		}
		for {
			var v any
			e := dec.Decode(&v)
			if e == io.EOF {
				break
			}
			if e != nil {
				return units, []string{"invalid_document"}
			}
			walk(v, 0)
			if ext == ".json" {
				var extra any
				if dec.Decode(&extra) != io.EOF {
					return units, []string{"invalid_document"}
				}
				break
			}
		}
		slices.Sort(warnings)
		return units, slices.Compact(warnings)
	case ".docx", ".pptx":
		return extractZipXML(raw, ext)
	case ".xlsx":
		return extractXLSX(ctx, raw)
	case ".pdf":
		if !o.PDF {
			return nil, []string{"extractor_disabled"}
		}
		text, warning := localExtractor(ctx, "pdftotext", []string{"-q", "-f", "1", "-l", "20", "-", "-"}, raw)
		if warning != "" {
			return nil, []string{warning}
		}
		units, warnings := textUnits(text, "line")
		warnings = append(warnings, "pdf_page_scope")
		if len(units) == 0 {
			warnings = append(warnings, "pdf_text_unavailable")
		}
		return units, warnings
	case ".png", ".jpg", ".jpeg":
		if !o.OCR {
			return nil, []string{"ocr_disabled"}
		}
		config, _, e := image.DecodeConfig(bytes.NewReader(raw))
		if e != nil || config.Width > 10000 || config.Height > 10000 || int64(config.Width)*int64(config.Height) > 25000000 {
			return nil, []string{"image_size_limit"}
		}
		languages := o.OCRLanguages
		if languages == "" {
			languages = "eng"
		}
		for _, r := range languages {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || r == '+') {
				return nil, []string{"ocr_unavailable"}
			}
		}
		text, warning := localExtractor(ctx, "tesseract", []string{"stdin", "stdout", "-l", languages, "quiet"}, raw)
		if warning != "" {
			if warning == "extractor_unavailable" {
				warning = "ocr_unavailable"
			} else {
				warning = "ocr_failed"
			}
			return nil, []string{warning}
		}
		units, warnings := textUnits(text, "image")
		warnings = append(warnings, "ocr_requires_review")
		return units, warnings
	}
	return nil, []string{"invalid_document"}
}

func extractZipXML(raw []byte, ext string) ([]fileUnit, []string) {
	archive, _, warning := openOfficeArchive(raw)
	if warning != "" {
		return nil, []string{warning}
	}
	entries := []*zip.File{}
	for _, f := range archive.File {
		if ext == ".docx" && f.Name == "word/document.xml" || ext == ".pptx" && strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			entries = append(entries, f)
		}
	}
	slices.SortFunc(entries, func(a, b *zip.File) int { return strings.Compare(a.Name, b.Name) })
	out := []fileUnit{}
	extracted := 0
	for _, entry := range entries {
		r, e := entry.Open()
		if e != nil {
			return out, []string{"invalid_document"}
		}
		decoder := xml.NewDecoder(io.LimitReader(r, MaxExtractedBytes+1))
		var paragraph strings.Builder
		inText := false
		for {
			tok, e := decoder.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				r.Close()
				return out, []string{"invalid_document"}
			}
			switch x := tok.(type) {
			case xml.StartElement:
				if x.Name.Local == "t" {
					inText = true
				}
			case xml.CharData:
				if inText {
					extracted += len(x)
					if extracted > MaxExtractedBytes {
						r.Close()
						return out, []string{"extracted_size_limit"}
					}
					paragraph.Write(x)
				}
			case xml.EndElement:
				if x.Name.Local == "t" {
					inText = false
				}
				if x.Name.Local == "p" && paragraph.Len() > 0 {
					if len(out) >= 200 {
						r.Close()
						return out, []string{"unit_count_limit"}
					}
					out = append(out, fileUnit{name: "text", kind: "paragraph", index: len(out) + 1, value: paragraph.String()})
					paragraph.Reset()
				}
			}
		}
		r.Close()
	}
	if len(out) == 0 {
		return nil, []string{"invalid_document"}
	}
	return out, []string{"document_main_body_only"}
}
