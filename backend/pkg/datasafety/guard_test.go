package datasafety

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceGuardEnforcesActualCopyAndShadowAI(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(from, "contacts.csv"), []byte("email\nfixture@example.test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := Policy{Version: "fixture-v1", ProtectedCategories: []string{"email"}, AllowedChannels: []string{}, ApprovedAIDomains: []string{"approved.example"}, BlockUnapprovedAI: true}
	c, d, err := CopyText(p, from, to, "contacts.csv", "blocked.csv")
	if !errors.Is(err, ErrBlocked) || d.Outcome != "blocked" || c.FileDigest == "" || len(c.Categories) != 1 {
		t.Fatalf("expected actual block: %v %+v", err, d)
	}
	if _, err = os.Stat(filepath.Join(to, "blocked.csv")); !os.IsNotExist(err) {
		t.Fatal("blocked copy created a destination")
	}
	p.AllowedChannels = []string{"file_copy", "ai_prompt"}
	c, d, err = CopyText(p, from, to, "contacts.csv", "allowed.csv")
	if err != nil || d.Outcome != "allowed" {
		t.Fatal("allowed copy failed", err)
	}
	got, _ := os.ReadFile(filepath.Join(to, "allowed.csv"))
	if string(got) != "email\nfixture@example.test\n" {
		t.Fatal("copy contents differ")
	}
	if _, _, err = CopyText(p, from, to, "contacts.csv", "allowed.csv"); err == nil {
		t.Fatal("overwrote an existing file")
	}
	for _, bad := range []string{"../contacts.csv", "/etc/passwd"} {
		if _, _, err = CopyText(p, from, to, bad, "x"); err == nil {
			t.Fatal("path escaped root")
		}
	}
	c.Kind, c.Channel, c.Operation, c.DestinationDomain = "ai", "ai_prompt", "upload", "unknown.example"
	d, err = Evaluate(p, c)
	if err != nil || d.Outcome != "blocked" || d.Rules[0] != "unapproved_ai_destination" {
		t.Fatal("unapproved AI destination allowed")
	}
	c.DestinationDomain = "approved.example"
	d, err = Evaluate(p, c)
	if err != nil || d.Outcome != "allowed" {
		t.Fatal("approved AI destination failed")
	}
	c.Capture = "unknown"
	d, _ = Evaluate(p, c)
	if d.Outcome != "unknown" {
		t.Fatal("unknown capture became safe")
	}
}
func TestSourceGuardRejectsBinaryOversizeAndSymlinkEscape(t *testing.T) {
	for _, raw := range []string{"\x00binary", strings.Repeat("a", (1<<20)+1), "\xff"} {
		c, body, err := InspectText(strings.NewReader(raw))
		if err != nil || c.Capture != "unknown" || body != nil {
			t.Fatal("incomplete content inspected as complete")
		}
	}
	from, to, outside := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(from, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	p := Policy{Version: "v1", AllowedChannels: []string{"file_copy"}}
	if _, _, err := CopyText(p, from, to, "escape.txt", "x.txt"); err == nil {
		t.Fatal("symlink escaped authorized root")
	}
}

func TestSourceGuardParsesStructuredTextAndCopiesOriginalBytes(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"escaped.json", `{"contact":"\u0066ixture@example.test"}`},
		{"escaped.jsonl", "{\"safe\":true}\n{\"contact\":\"\\u0066ixture@example.test\"}\n"},
		{"quoted.csv", "name,contact\n\"fixture, synthetic\",\"fixture@example.test\"\n"},
		{"multiline.csv", "name,contact\n\"fixture\nsynthetic\",\"fixture@example.test\"\n"},
		{"numeric.json", `{"phone":13800138000}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, to := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(from, tc.name), []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			p := Policy{Version: "structured-v1", ProtectedCategories: []string{"email", "phone"}}
			c, d, err := CopyText(p, from, to, tc.name, "blocked")
			if !errors.Is(err, ErrBlocked) || c.Capture != "complete" || d.Outcome != "blocked" || len(c.Categories) == 0 {
				t.Fatalf("decoded sensitive scalar bypassed policy: %+v %+v %v", c, d, err)
			}
			if _, err = os.Stat(filepath.Join(to, "blocked")); !os.IsNotExist(err) {
				t.Fatal("blocked structured copy created destination")
			}
			p.AllowedChannels = []string{"file_copy"}
			c, d, err = CopyText(p, from, to, tc.name, "allowed")
			if err != nil || d.Outcome != "allowed" {
				t.Fatal("explicitly allowed structured copy failed", err)
			}
			got, err := os.ReadFile(filepath.Join(to, "allowed"))
			sum := sha256.Sum256([]byte(tc.raw))
			if err != nil || string(got) != tc.raw || c.FileDigest != hex.EncodeToString(sum[:]) {
				t.Fatal("parser changed copied bytes or content digest")
			}
		})
	}
}

func TestSourceGuardRejectsUnsupportedRenamedAndIncompleteFormats(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"ascii.pdf", "%PDF-1.4\n<66697874757265406578616d706c652e74657374>\n%%EOF\n"},
		{"renamed-pdf.txt", "%PDF-1.4\n<66697874757265406578616d706c652e74657374>\n%%EOF\n"},
		{"renamed-zip.txt", "PK\x03\x04container"},
		{"renamed-bzip.txt", "BZh9container"},
		{"renamed-tar.txt", strings.Repeat(" ", 257) + "ustar"},
		{"unknown.docx", "synthetic plain ASCII document"},
		{"extensionless", "synthetic plain ASCII"},
		{"truncated.json", `{"contact":"fixture@example.test"`},
		{"trailing.json", `{"safe":true}{"safe":false}`},
		{"duplicate.json", `{"contact":"fixture@example.test","contact":"redacted"}`},
		{"depth.json", strings.Repeat("[", textDepthLimit+2) + "null" + strings.Repeat("]", textDepthLimit+2)},
		{"tokens.json", "[" + strings.Repeat("true,", textTokenLimit) + "true]"},
		{"scalar.json", `{"value":"` + strings.Repeat("a ", textScalarLimit) + `"}`},
		{"invalid.jsonl", "{\"safe\":true}\n{\"truncated\":\n"},
		{"blank.jsonl", "{\"safe\":true}\n\n{\"safe\":false}\n"},
		{"invalid.csv", "name,contact\n\"unclosed,fixture@example.test\n"},
		{"inconsistent.csv", "name,contact\nfixture,redacted,extra\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, to := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(from, tc.name), []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			c, d, err := CopyText(Policy{Version: "unknown-v1", ProtectedCategories: []string{"email"}, AllowedChannels: []string{"file_copy"}}, from, to, tc.name, "destination")
			if !errors.Is(err, ErrUnknown) || c.Capture != "unknown" || d.Outcome != "unknown" {
				t.Fatalf("unsupported/incomplete format became allowed: %+v %+v %v", c, d, err)
			}
			if _, err = os.Stat(filepath.Join(to, "destination")); !os.IsNotExist(err) {
				t.Fatal("unknown copy created a destination")
			}
		})
	}
}

func TestSourceGuardUnknownForUnsupportedProtectedCategories(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(from, "fixture.txt"), []byte("synthetic content"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, category := range []string{"credential", "personal_name", "address", "ip_address", "business"} {
		p := Policy{Version: "unsupported-v1", ProtectedCategories: []string{category}, AllowedChannels: []string{"file_copy"}}
		c, d, err := CopyText(p, from, to, "fixture.txt", category)
		if !errors.Is(err, ErrUnknown) || c.Capture != "unknown" || d.Outcome != "unknown" {
			t.Fatalf("unsupported category %s treated as inspected: %+v %v", category, d, err)
		}
		if _, err = os.Stat(filepath.Join(to, category)); !os.IsNotExist(err) {
			t.Fatal("uninspected category created destination")
		}
		c.Capture, c.Categories = "complete", []string{category}
		p.AllowedChannels = nil
		if d, err = Evaluate(p, c); err != nil || d.Outcome != "blocked" {
			t.Fatal("explicit source-provided category stopped working", category, err)
		}
	}
}

func TestSourceGuardRejectsKindChannelMismatch(t *testing.T) {
	p := Policy{Version: "kind-v1", AllowedChannels: []string{"file_copy", "database", "ai_prompt"}, BlockUnapprovedAI: true}
	if err := p.Validate(); err != nil {
		t.Fatal("valid channel policy rejected", err)
	}
	base := Context{Kind: "endpoint", Channel: "file_copy", Operation: "copy", Enforcement: "observed", Capture: "complete", DestinationDomain: "unapproved.example", DatabaseRef: strings.Repeat("a", 64)}
	for _, pair := range [][2]string{{"endpoint", "ai_prompt"}, {"endpoint", "database"}, {"ai", "file_copy"}, {"database", "file_copy"}, {"ai", "database"}, {"database", "ai_prompt"}} {
		c := base
		c.Kind, c.Channel = pair[0], pair[1]
		if c.Validate() == nil {
			t.Fatal("kind/channel mismatch accepted", pair)
		}
		if _, err := Evaluate(p, c); !errors.Is(err, ErrContext) {
			t.Fatal("kind/channel mismatch bypassed policy", pair)
		}
	}
	base.Kind, base.Channel = "ai", "ai_prompt"
	if d, err := Evaluate(p, base); err != nil || d.Outcome != "blocked" {
		t.Fatal("unapproved AI domain bypassed policy", err)
	}
	p.AllowedChannels = []string{"not_a_channel"}
	if p.Validate() == nil {
		t.Fatal("unrecognized channel accepted")
	}
}
