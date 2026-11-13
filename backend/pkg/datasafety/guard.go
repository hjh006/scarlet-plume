package datasafety

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hjh006/scarlet-plume/backend/internal/dataformat"
)

type Policy struct {
	Version             string   `json:"version"`
	ProtectedCategories []string `json:"protectedCategories"`
	AllowedChannels     []string `json:"allowedChannels"`
	ApprovedAIDomains   []string `json:"approvedAiDomains"`
	BlockUnapprovedAI   bool     `json:"blockUnapprovedAi"`
}
type Decision struct {
	Outcome      string   `json:"outcome"`
	Rules        []string `json:"rules"`
	PolicyDigest string   `json:"policyDigest"`
}

func (p Policy) Validate() error {
	if len(p.Version) < 1 || len(p.Version) > 64 || len(p.ProtectedCategories) > 9 || len(p.AllowedChannels) > 10 || len(p.ApprovedAIDomains) > 100 {
		return ErrContext
	}
	for _, s := range p.ApprovedAIDomains {
		if !Domain(s) {
			return ErrContext
		}
	}
	probe := Context{Kind: "endpoint", Channel: "file_copy", Operation: "copy", Enforcement: "unknown", Capture: "unknown", Categories: p.ProtectedCategories}
	if probe.Validate() != nil {
		return ErrContext
	}
	for _, v := range p.AllowedChannels {
		if !validChannel(v) {
			return ErrContext
		}
	}
	return nil
}
func Evaluate(p Policy, c Context) (Decision, error) {
	if p.Validate() != nil || c.Validate() != nil {
		return Decision{}, ErrContext
	}
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	out := Decision{Outcome: "allowed", Rules: []string{}, PolicyDigest: hex.EncodeToString(sum[:])}
	if c.Capture != "complete" {
		out.Outcome = "unknown"
		out.Rules = append(out.Rules, "capture_incomplete")
		return out, nil
	}
	if c.Kind == "ai" && p.BlockUnapprovedAI && !slices.Contains(p.ApprovedAIDomains, c.DestinationDomain) {
		out.Outcome = "blocked"
		out.Rules = append(out.Rules, "unapproved_ai_destination")
	}
	protected := false
	for _, v := range c.Categories {
		protected = protected || slices.Contains(p.ProtectedCategories, v)
	}
	if protected && !slices.Contains(p.AllowedChannels, c.Channel) {
		out.Outcome = "blocked"
		out.Rules = append(out.Rules, "protected_data_channel")
	}
	return out, nil
}

const (
	textByteLimit   = 1 << 20
	textTokenLimit  = 16384
	textDepthLimit  = 16
	textScalarLimit = 64 << 10
)

// InspectText inspects bounded plain text. Document/container signatures remain unknown.
// The returned bytes are local copy input and must never be included in an event projection.
func InspectText(r io.Reader) (Context, []byte, error) {
	return inspectTextFormat(r, "txt")
}

// InspectJSON decodes a bounded JSON document before inspecting its scalar values.
// Returned bytes remain local; the Context is the only event-safe projection.
func InspectJSON(r io.Reader) (Context, []byte, error) {
	return inspectTextFormat(r, "json")
}

// InspectFileText applies the existing strict text/CSV/JSON/JSONL parser to a
// source-declared file format. Other formats remain unknown.
func InspectFileText(r io.Reader, format string) (Context, []byte, error) {
	if !slices.Contains([]string{"txt", "csv", "json", "jsonl"}, format) {
		format = "other"
	}
	return inspectTextFormat(r, format)
}

// SupportsTextPolicy reports whether local scalar inspection covers every protected category.
// Other authorized sources may provide the additional categories accepted by Evaluate.
func SupportsTextPolicy(policy Policy) bool {
	for _, category := range policy.ProtectedCategories {
		if !slices.Contains([]string{"email", "phone", "national_id", "financial_account"}, category) {
			return false
		}
	}
	return true
}

func inspectTextFormat(r io.Reader, format string) (Context, []byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, textByteLimit+1))
	if err != nil {
		return Context{}, nil, err
	}
	c := Context{Kind: "endpoint", Channel: "file_copy", FileType: format, Operation: "copy", Enforcement: "unknown", Capture: "unknown", Categories: []string{}}
	if len(raw) > textByteLimit || !utf8.Valid(raw) || bytes.IndexFunc(raw, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' }) >= 0 {
		return c, nil, nil
	}
	sum := sha256.Sum256(raw)
	c.FileDigest = hex.EncodeToString(sum[:])
	if containerSignature(raw) {
		return c, nil, nil
	}
	inspection := textInspection{context: &c}
	complete := false
	switch format {
	case "txt":
		complete = inspection.scalar(string(raw), false)
	case "csv":
		complete = inspection.csv(raw)
	case "json":
		complete = inspection.json(raw)
	case "jsonl":
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(nil, textByteLimit+1)
		complete = true
		lines := 0
		for scanner.Scan() {
			lines++
			if !inspection.json(scanner.Bytes()) {
				complete = false
				break
			}
		}
		complete = complete && lines > 0 && scanner.Err() == nil
	}
	if !complete {
		c.Categories = []string{}
		return c, nil, nil
	}
	c.Capture = "complete"
	return c, raw, nil
}

// These formats require separate parsers. Renaming them must not imply text coverage.
func containerSignature(raw []byte) bool {
	head := bytes.TrimSpace(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}))
	for _, magic := range [][]byte{[]byte("PK\x03\x04"), []byte("PK\x05\x06"), []byte("PK\x07\x08"), []byte("BZh"), []byte("Rar!"), {0x1f, 0x8b}, {0xfd, '7', 'z', 'X', 'Z', 0}, {'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}, {0xd0, 0xcf, 0x11, 0xe0}, []byte("%!PS"), []byte("{\\rtf"), []byte("<?xml")} {
		if bytes.HasPrefix(head, magic) {
			return true
		}
	}
	return bytes.Contains(raw[:min(1024, len(raw))], []byte("%PDF-")) || len(raw) >= 262 && bytes.Equal(raw[257:262], []byte("ustar"))
}

type textInspection struct {
	context *Context
	tokens  int
	words   int
}

func (i *textInspection) scalar(value string, bounded bool) bool {
	i.tokens++
	if i.tokens > textTokenLimit || bounded && len(value) > textScalarLimit {
		return false
	}
	words := strings.FieldsFunc(value, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("@._+-", r))
	})
	for _, word := range words {
		i.words++
		if i.words > textTokenLimit || len(word) > 256 {
			return false
		}
		for _, category := range dataformat.Categories(word) {
			if !slices.Contains(i.context.Categories, category) {
				i.context.Categories = append(i.context.Categories, category)
			}
		}
	}
	return true
}

func (i *textInspection) csv(raw []byte) bool {
	r := csv.NewReader(bytes.NewReader(raw))
	r.ReuseRecord = true
	rows := 0
	for {
		record, err := r.Read()
		if err == io.EOF {
			return rows > 0
		}
		rows++
		if err != nil || rows > textTokenLimit {
			return false
		}
		for _, value := range record {
			if !i.scalar(value, true) {
				return false
			}
		}
	}
}

func (i *textInspection) json(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if !i.jsonValue(d, 0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func (i *textInspection) jsonValue(d *json.Decoder, depth int) bool {
	i.tokens++
	if depth > textDepthLimit || i.tokens > textTokenLimit {
		return false
	}
	token, err := d.Token()
	if err != nil {
		return false
	}
	switch value := token.(type) {
	case string:
		return i.scalar(value, true)
	case json.Number:
		return i.scalar(string(value), true)
	case json.Delim:
		if value != '{' && value != '[' {
			return false
		}
		seen := map[string]bool{}
		for d.More() {
			if value == '{' {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] || !i.scalar(name, true) {
					return false
				}
				seen[name] = true
			}
			if !i.jsonValue(d, depth+1) {
				return false
			}
		}
		end, err := d.Token()
		return err == nil && (value == '{' && end == json.Delim('}') || value == '[' && end == json.Delim(']'))
	case nil, bool:
		return true
	default:
		return false
	}
}

var ErrBlocked = errors.New("data transfer blocked by source-side policy")
var ErrUnknown = errors.New("data transfer requires complete source-side inspection")

// CopyText enforces only this explicitly invoked file-copy operation, never OS-wide channels.
func CopyText(policy Policy, fromRoot, toRoot, source, destination string) (Context, Decision, error) {
	if filepath.IsAbs(source) || filepath.IsAbs(destination) || !filepath.IsLocal(source) || !filepath.IsLocal(destination) {
		return Context{}, Decision{}, ErrContext
	}
	inRoot, err := os.OpenRoot(fromRoot)
	if err != nil {
		return Context{}, Decision{}, err
	}
	defer inRoot.Close()
	file, err := inRoot.Open(source)
	if err != nil {
		return Context{}, Decision{}, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return Context{}, Decision{}, ErrContext
	}
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(source)), ".")
	if !slices.Contains([]string{"txt", "csv", "json", "jsonl"}, format) {
		format = "other"
	}
	c, raw, err := inspectTextFormat(file, format)
	if err != nil {
		return c, Decision{}, err
	}
	if !SupportsTextPolicy(policy) {
		c.Capture = "unknown"
		raw = nil
	}
	d, err := Evaluate(policy, c)
	if err != nil {
		return c, d, err
	}
	c.PolicyDigest = d.PolicyDigest
	c.Enforcement = d.Outcome
	if d.Outcome == "blocked" {
		return c, d, ErrBlocked
	}
	if d.Outcome != "allowed" {
		return c, d, ErrUnknown
	}
	targetRoot, err := os.OpenRoot(toRoot)
	if err != nil {
		return c, d, err
	}
	defer targetRoot.Close()
	target, err := targetRoot.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return c, d, err
	}
	if _, err = target.Write(raw); err != nil {
		_ = target.Close()
		return c, d, err
	}
	err = target.Close()
	return c, d, err
}
