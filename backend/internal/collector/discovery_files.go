package collector

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/hjh006/scarlet-plume/backend/internal/valuefingerprint"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxFileBytes = 8 << 20
const MaxExtractedBytes = 256 << 10
const MaxFileUnits = 200

type FileScanOptions struct {
	FingerprintKey   string `json:"-"`
	FingerprintKeyID string
	FingerprintScope string
	DigestKey        []byte `json:"-"`
	PDF              bool
	OCR              bool
	OCRLanguages     string
}
type FileCoverage struct {
	Status       string   `json:"status"`
	BytesScanned int64    `json:"bytesScanned"`
	UnitsScanned int      `json:"unitsScanned"`
	Warnings     []string `json:"warnings"`
}
type FileLocation struct {
	Locator     string `json:"locator,omitempty"`
	Unit        string `json:"unit"`
	Index       int    `json:"index"`
	RuleKey     string `json:"ruleKey"`
	RuleVersion int    `json:"ruleVersion"`
	Category    string `json:"category"`
	MatchCount  int    `json:"matchCount"`
}
type FileEvidence struct {
	Scope         string                      `json:"scope"`
	Fingerprints  []valuefingerprint.Evidence `json:"fingerprints,omitempty"`
	Ref           string                      `json:"ref"`
	Name          string                      `json:"name"`
	MediaType     string                      `json:"mediaType"`
	SizeBytes     int64                       `json:"sizeBytes"`
	ModifiedAt    time.Time                   `json:"modifiedAt"`
	ContentDigest string                      `json:"contentDigest"`
	Fields        []DiscoveryField            `json:"fields"`
	Locations     []FileLocation              `json:"locations"`
	Coverage      FileCoverage                `json:"coverage"`
}
type fileUnit struct {
	name, kind, value string
	locator           string
	index             int
}

func ValidFileScopes(scopes []string) bool {
	if len(scopes) == 0 || len(scopes) > 100 {
		return false
	}
	for _, scope := range scopes {
		if !fs.ValidPath(scope) || len(scope) > 128 {
			return false
		}
	}
	return true
}

// DiscoverLocalFiles never returns extracted text. The root is customer-local;
// platform scopes can only narrow it to relative directories.
func DiscoverLocalFiles(ctx context.Context, c DiscoveryConfig, rootPath string, options FileScanOptions) (DiscoveryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	result := DiscoveryResult{Tables: []DiscoveryTable{}, Files: []FileEvidence{}, Coverage: DiscoveryCoverage{Metadata: "file_metadata", Content: "bounded_local_file_content", Permissions: "unknown", Lineage: "unknown", TableLimit: 100, RowLimit: 200, Warnings: []string{"authorized_directory_only", "no_raw_values_uploaded", "bounded_files_not_full_repository"}}}
	if len(options.DigestKey) < 32 {
		return result, errors.New("本地文件扫描需要来源专用摘要密钥")
	}
	keyedDigest := func(domain string, value []byte) string {
		h := hmac.New(sha256.New, options.DigestKey)
		h.Write([]byte(domain + "\x00"))
		h.Write(value)
		return hex.EncodeToString(h.Sum(nil))
	}
	if options.FingerprintKey != "" || options.FingerprintKeyID != "" || options.FingerprintScope != "" {
		if _, err := valuefingerprint.Create(options.FingerprintKey, options.FingerprintKeyID, options.FingerprintScope, "configuration-check"); err != nil {
			return result, errors.New("本地指纹密钥配置无效")
		}
	} else {
		result.Coverage.Warnings = append(result.Coverage.Warnings, "fingerprints_disabled")
	}
	info, e := os.Lstat(rootPath)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, errors.New("本地授权目录不可读取或是符号链接")
	}
	root, e := os.OpenRoot(rootPath)
	if e != nil {
		return result, errors.New("本地授权目录不可读取")
	}
	defer root.Close()
	if len(c.Scopes) == 0 {
		return result, errors.New("请选择相对授权目录")
	}
	rules := c.Rules
	if len(rules) == 0 {
		rules = BuiltinDiscoveryRules()
	}
	for i := range rules {
		if e = ValidateDiscoveryRule(rules[i]); e != nil {
			return result, e
		}
		rules[i] = prepareDiscoveryRule(rules[i])
	}
	paths := map[string]bool{}
	visited := 0
	stop := errors.New("bounded stop")
	for _, scope := range c.Scopes {
		if !fs.ValidPath(scope) {
			return result, errors.New("目录范围必须是相对目录，不能包含上级路径")
		}
		// Lstat every ancestor, not just the final component.
		p := ""
		for _, part := range strings.Split(scope, "/") {
			p = path.Join(p, part)
			st, err := root.Lstat(p)
			if err != nil || st.Mode()&os.ModeSymlink != 0 {
				return result, errors.New("授权范围不可读取或包含符号链接")
			}
		}
		e = fs.WalkDir(root.FS(), scope, func(name string, d fs.DirEntry, err error) error {
			visited++
			if visited > 10000 {
				return stop
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				result.Coverage.Warnings = append(result.Coverage.Warnings, "file_read_error")
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				result.Coverage.Warnings = append(result.Coverage.Warnings, "symlink_skipped")
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if paths[name] {
				return nil
			}
			paths[name] = true
			ext := strings.ToLower(path.Ext(name))
			if !slices.Contains([]string{".txt", ".csv", ".json", ".jsonl", ".docx", ".pptx", ".xlsx", ".pdf", ".png", ".jpg", ".jpeg"}, ext) {
				return nil
			}
			if len(result.Files) >= 100 {
				return stop
			}
			st, err := root.Lstat(name)
			if err != nil || !st.Mode().IsRegular() {
				result.Coverage.Warnings = append(result.Coverage.Warnings, "non_regular_file_skipped")
				return nil
			}
			ref := keyedDigest("file-path-v1", []byte(name))
			// Do not transmit filenames or relative paths: they can themselves hold values.
			file := FileEvidence{Scope: scope, Ref: ref, Name: "file-" + ref[:12] + ext, MediaType: ext[1:], SizeBytes: st.Size(), ModifiedAt: st.ModTime().UTC(), Fields: []DiscoveryField{}, Locations: []FileLocation{}, Coverage: FileCoverage{Status: "complete", Warnings: []string{}}}
			if st.Size() > MaxFileBytes {
				file.Coverage.Status = "unknown"
				file.Coverage.Warnings = append(file.Coverage.Warnings, "file_size_limit")
			} else {
				f, err := root.Open(name)
				if err != nil {
					file.Coverage.Status = "unknown"
					file.Coverage.Warnings = append(file.Coverage.Warnings, "file_read_error")
				} else {
					raw, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
					f.Close()
					if err != nil || len(raw) > MaxFileBytes {
						file.Coverage.Status = "unknown"
						file.Coverage.Warnings = append(file.Coverage.Warnings, "file_read_error")
					} else {

						file.ContentDigest = keyedDigest("file-content-v1", raw)
						units, warnings := extractFileUnits(ctx, raw, ext, options)
						file.Coverage.Warnings = append(file.Coverage.Warnings, warnings...)
						if len(warnings) > 0 {
							file.Coverage.Status = "partial"
						}
						if len(units) == 0 {
							file.Coverage.Status = "unknown"
						}
						scanFileUnits(&file, units, rules, options)
					}
				}
			}
			result.Files = append(result.Files, file)
			return nil
		})
		if errors.Is(e, stop) {
			result.Coverage.Warnings = append(result.Coverage.Warnings, "file_count_limit")
			break
		}
		if e != nil {
			return result, errors.New("文件扫描未完成")
		}
	}
	slices.Sort(result.Coverage.Warnings)
	result.Coverage.Warnings = slices.Compact(result.Coverage.Warnings)
	return result, nil
}

func fileRuleMatches(rule DiscoveryRule, value string) bool {
	if discoveryMatch(rule, value) {
		return true
	}
	if strings.HasPrefix(rule.Key, "builtin.") && rule.Target == "content" && rule.Kind == "regexp" {
		expression := strings.TrimSuffix(strings.TrimPrefix(rule.Expression, "^"), "$")
		re, e := regexp.Compile(expression)
		if e != nil {
			return false
		}
		for _, idx := range re.FindAllStringIndex(value, 100) {
			before, after := byte(0), byte(0)
			if idx[0] > 0 {
				before = value[idx[0]-1]
			}
			if idx[1] < len(value) {
				after = value[idx[1]]
			}
			alnum := func(v byte) bool { return v >= '0' && v <= '9' || v >= 'A' && v <= 'Z' || v >= 'a' && v <= 'z' }
			if !alnum(before) && !alnum(after) && discoveryMatch(rule, value[idx[0]:idx[1]]) {
				return true
			}
		}
	}
	return false
}
func scanFileUnits(file *FileEvidence, units []fileUnit, rules []DiscoveryRule, options FileScanOptions) {
	fields := map[string]*DiscoveryField{}
	counts := map[string]map[string]int64{}
	scanned := map[string]int64{}
	for _, unit := range units {
		if len(unit.value) > 4096 || !utf8.ValidString(unit.value) {
			file.Coverage.Status = "partial"
			file.Coverage.Warnings = append(file.Coverage.Warnings, "unit_size_limit")
			continue
		}
		file.Coverage.UnitsScanned++
		file.Coverage.BytesScanned += int64(len(unit.value))
		scanned[unit.name]++
		if fields[unit.name] == nil {
			fields[unit.name] = &DiscoveryField{Name: unit.name, Type: "text", Hits: []DiscoveryHit{}}
			counts[unit.name] = map[string]int64{}
		}
		for _, rule := range rules {
			value := unit.value
			if rule.Target == "metadata" {
				value = unit.name + " text"
			}
			if fileRuleMatches(rule, value) {
				counts[unit.name][rule.Key]++
				if options.FingerprintKey != "" && rule.Target == "content" && len(file.Fingerprints) < 32 {
					values := []string{value}
					if rule.Kind == "regexp" {
						expression := strings.TrimSuffix(strings.TrimPrefix(rule.Expression, "^"), "$")
						if re, err := regexp.Compile(expression); err == nil {
							values = re.FindAllString(value, 32)
						}
					}
					for _, matched := range values {
						if len(file.Fingerprints) >= 32 {
							break
						}
						if !fileRuleMatches(rule, matched) {
							continue
						}
						token, err := valuefingerprint.Create(options.FingerprintKey, options.FingerprintKeyID, options.FingerprintScope, matched)
						if err == nil {
							file.Fingerprints = append(file.Fingerprints, valuefingerprint.Evidence{Token: token, Locator: fileUnitLocator(unit), Category: rule.Category})
						}
					}
				}
				if len(file.Locations) < 200 {
					file.Locations = append(file.Locations, FileLocation{Locator: unit.locator, Unit: unit.kind, Index: unit.index, RuleKey: rule.Key, RuleVersion: rule.Version, Category: rule.Category, MatchCount: 1})
				} else {
					file.Coverage.Status = "partial"
					file.Coverage.Warnings = append(file.Coverage.Warnings, "location_limit")
				}
			}
		}
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		f := fields[name]
		for _, rule := range rules {
			if count := counts[name][rule.Key]; count > 0 {
				f.Hits = append(f.Hits, DiscoveryHit{RuleKey: rule.Key, RuleVersion: rule.Version, Category: rule.Category, Method: "bounded_local_file", MatchCount: count, ScannedCount: scanned[name]})
			}
		}
		file.Fields = append(file.Fields, *f)
	}
	slices.Sort(file.Coverage.Warnings)
	file.Coverage.Warnings = slices.Compact(file.Coverage.Warnings)
}

func validateFileDiscoveryResult(c DiscoveryConfig, r DiscoveryResult) error {
	if len(r.Tables) > 0 || len(r.Files) > 100 || len(r.Coverage.Warnings) > 100 {
		return errors.New("文件扫描范围无效")
	}
	for _, warning := range r.Coverage.Warnings {
		if !slices.Contains([]string{"authorized_directory_only", "no_raw_values_uploaded", "bounded_files_not_full_repository", "file_read_error", "symlink_skipped", "non_regular_file_skipped", "file_count_limit", "fingerprints_disabled"}, warning) {
			return errors.New("文件总覆盖提示无效")
		}
	}
	seen := map[string]bool{}
	allowed := map[string]DiscoveryRule{}
	for _, rule := range c.Rules {
		allowed[rule.Key] = rule
	}
	if len(allowed) == 0 {
		for _, rule := range BuiltinDiscoveryRules() {
			allowed[rule.Key] = rule
		}
	}
	hash := regexp.MustCompile(`^[a-f0-9]{64}$`)
	for _, f := range r.Files {
		if !slices.Contains(c.Scopes, f.Scope) || !hash.MatchString(f.Ref) || (f.ContentDigest != "" && !hash.MatchString(f.ContentDigest)) || seen[f.Ref] || (!slices.Contains([]string{"txt", "csv", "json", "jsonl", "docx", "pptx", "xlsx", "pdf", "png", "jpg", "jpeg"}, f.MediaType) || f.Name != "file-"+f.Ref[:12]+"."+f.MediaType) || len(f.Name) > 40 || f.SizeBytes < 0 || len(f.Fields) > 200 || len(f.Locations) > 200 || f.Coverage.BytesScanned > MaxFileBytes || f.Coverage.BytesScanned < 0 || f.Coverage.UnitsScanned < 0 || f.Coverage.UnitsScanned > 200 || !slices.Contains([]string{"complete", "partial", "unknown"}, f.Coverage.Status) {
			return errors.New("文件证据无效")
		}
		seen[f.Ref] = true
		for _, field := range f.Fields {
			if field.Type != "text" || field.Comment != "" || field.Sensitivity != "" || !regexp.MustCompile(`^(text|column_[0-9]+|field_[0-9]+)$`).MatchString(field.Name) || len(field.Hits) > 105 {
				return errors.New("文件字段无效")
			}
			for _, h := range field.Hits {
				rule, ok := allowed[h.RuleKey]
				if !ok || rule.Version != h.RuleVersion || rule.Category != h.Category || h.Method != "bounded_local_file" || h.MatchCount < 1 || h.MatchCount > h.ScannedCount || h.ScannedCount > 200 {
					return errors.New("文件规则统计无效")
				}
			}
		}
		for _, loc := range f.Locations {
			rule, ok := allowed[loc.RuleKey]
			if loc.Unit == "cell" && (f.MediaType != "xlsx" || loc.Index > MaxFileUnits || !validXLSXLocator(loc.Locator)) || loc.Unit != "cell" && loc.Locator != "" {
				return errors.New("文件坐标无效")
			}
			if !ok || rule.Version != loc.RuleVersion || rule.Category != loc.Category || loc.Index < 1 || loc.Index > 100000 || loc.MatchCount != 1 || !slices.Contains([]string{"line", "row", "value", "paragraph", "page", "image", "cell"}, loc.Unit) {
				return errors.New("文件定位证据无效")
			}
		}
		if len(f.Fingerprints) > 32 {
			return errors.New("文件指纹数量无效")
		}
		for _, fp := range f.Fingerprints {
			if !valuefingerprint.Valid(fp.Token) || fp.Version != valuefingerprint.Version || (!regexp.MustCompile(`^(line|row|value|paragraph|image):[1-9][0-9]{0,5}$`).MatchString(fp.Locator) && !validXLSXLocator(fp.Locator)) || !validText(fp.Category, 80) {
				return errors.New("文件指纹无效")
			}
			found := false
			for _, loc := range f.Locations {
				if fp.Locator == fileLocationLocator(loc) && fp.Category == loc.Category {
					found = true
				}
			}
			if !found {
				return errors.New("指纹不在已观察命中范围")
			}
		}
		if !validFileWarnings(f.Coverage.Warnings) {
			return errors.New("文件覆盖提示无效")
		}
	}
	return nil
}
func validFileWarnings(warnings []string) bool {
	if len(warnings) > 32 {
		return false
	}
	for _, w := range warnings {
		if !slices.Contains([]string{"file_size_limit", "file_read_error", "unit_size_limit", "location_limit", "unit_count_limit", "extracted_size_limit", "invalid_utf8", "invalid_document", "archive_limit", "extractor_disabled", "extractor_unavailable", "extractor_failed", "ocr_disabled", "ocr_unavailable", "ocr_failed", "image_size_limit", "pdf_text_unavailable", "unsupported_encoding", "pdf_page_scope", "document_main_body_only", "ocr_requires_review", "archive_path_invalid", "archive_duplicate_entry", "xlsx_cell_values_only", "xlsx_formula_not_evaluated", "xlsx_external_links_not_followed", "xlsx_unsupported_parts", "xlsx_shared_string_unavailable", "xlsx_shared_string_limit", "xlsx_sheet_limit", "xlsx_number_format_not_applied", "xlsx_unsupported_cell_type"}, w) {
			return false
		}
	}
	return true
}

func textUnits(raw []byte, kind string) ([]fileUnit, []string) {
	warnings := []string{}
	if len(raw) > MaxExtractedBytes {
		raw = raw[:MaxExtractedBytes]
		warnings = append(warnings, "extracted_size_limit")
	}
	if !utf8.Valid(raw) {
		return nil, []string{"invalid_utf8"}
	}
	out := []fileUnit{}
	for index, line := range bytes.Split(raw, []byte{'\n'}) {
		if index >= 100000 {
			warnings = append(warnings, "unit_count_limit")
			break
		}
		if len(out) >= MaxFileUnits {
			warnings = append(warnings, "unit_count_limit")
			break
		}
		if len(bytes.TrimSpace(line)) > 0 {
			out = append(out, fileUnit{name: "text", kind: kind, index: index + 1, value: string(line)})
		}
	}
	return out, warnings
}

func fileUnitLocator(unit fileUnit) string {
	if unit.locator != "" {
		return unit.locator
	}
	return unit.kind + ":" + fmt.Sprint(unit.index)
}
func fileLocationLocator(loc FileLocation) string {
	if loc.Locator != "" {
		return loc.Locator
	}
	return loc.Unit + ":" + fmt.Sprint(loc.Index)
}
