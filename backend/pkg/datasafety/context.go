// Package datasafety projects data-security facts without source content or identities.
package datasafety

import (
	"errors"
	"net"
	"regexp"
	"slices"
	"strings"
)

var ErrContext = errors.New("invalid data-security projection")
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Context struct {
	Endpoint          *EndpointEvidence `json:"endpoint,omitempty"`
	Kind              string            `json:"kind"`
	Channel           string            `json:"channel"`
	SubjectRef        string            `json:"subjectRef,omitempty"`
	DeviceRef         string            `json:"deviceRef,omitempty"`
	FileDigest        string            `json:"fileDigest,omitempty"`
	FileType          string            `json:"fileType,omitempty"`
	Categories        []string          `json:"categories"`
	DestinationDomain string            `json:"destinationDomain,omitempty"`
	Operation         string            `json:"operation"`
	Enforcement       string            `json:"enforcement"`
	Capture           string            `json:"capture"`
	DatabaseRef       string            `json:"databaseRef,omitempty"`
	ObjectRef         string            `json:"objectRef,omitempty"`
	StatementRef      string            `json:"statementRef,omitempty"`
	Records           *int64            `json:"records,omitempty"`
	PolicyDigest      string            `json:"policyDigest,omitempty"`
}

func Domain(value string) bool {
	if len(value) < 1 || len(value) > 253 || strings.ToLower(value) != value || net.ParseIP(value) != nil {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return strings.Contains(value, ".")
}

func validChannel(channel string) bool {
	return slices.Contains([]string{"web_upload", "mail", "im", "usb", "print", "clipboard", "file_copy", "database", "ai_prompt", "file_change"}, channel)
}

func (c Context) Validate() error {
	if !slices.Contains([]string{"endpoint", "database", "ai"}, c.Kind) || !validChannel(c.Channel) || !slices.Contains([]string{"read", "write", "delete", "export", "upload", "copy", "execute", "change"}, c.Operation) || !slices.Contains([]string{"observed", "allowed", "blocked", "failed", "unknown"}, c.Enforcement) || !slices.Contains([]string{"complete", "partial", "unknown"}, c.Capture) {
		return ErrContext
	}
	if len(c.Categories) > 9 || c.Records != nil && (*c.Records < 0 || *c.Records > 1000000000000) {
		return ErrContext
	}
	for _, v := range []string{c.SubjectRef, c.DeviceRef, c.FileDigest, c.DatabaseRef, c.ObjectRef, c.StatementRef, c.PolicyDigest} {
		if v != "" && !digestPattern.MatchString(v) {
			return ErrContext
		}
	}
	if c.DestinationDomain != "" && !Domain(c.DestinationDomain) {
		return ErrContext
	}
	if c.FileType != "" && !slices.Contains([]string{"txt", "csv", "json", "jsonl", "docx", "pptx", "xlsx", "pdf", "png", "jpg", "other"}, c.FileType) {
		return ErrContext
	}
	seen := map[string]bool{}
	for _, v := range c.Categories {
		if seen[v] || !slices.Contains([]string{"email", "phone", "national_id", "financial_account", "credential", "personal_name", "address", "ip_address", "business"}, v) {
			return ErrContext
		}
		seen[v] = true
	}
	if (c.Kind == "database") != (c.Channel == "database") || c.Kind == "database" && c.DatabaseRef == "" {
		return ErrContext
	}
	if (c.Kind == "ai") != (c.Channel == "ai_prompt") || c.Kind == "ai" && c.DestinationDomain == "" {
		return ErrContext
	}
	if c.Endpoint != nil && !c.Endpoint.valid(c) {
		return ErrContext
	}
	return nil
}
