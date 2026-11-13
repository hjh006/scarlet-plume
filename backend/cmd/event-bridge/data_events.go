package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
)

func projectDataEvent(format string, raw []byte) (store.SecurityEventInput, error) {
	key, err := hex.DecodeString(os.Getenv("SCARLET_EVENT_CONTEXT_KEY_HEX"))
	scope := os.Getenv("SCARLET_EVENT_CONTEXT_SCOPE")
	if err != nil || len(key) != 32 || len(scope) < 1 || len(scope) > 80 {
		return store.SecurityEventInput{}, errors.New("configure a source-side 32-byte context HMAC key and explicit scope")
	}
	ref := func(kind, value string) string {
		if value == "" {
			return ""
		}
		m := hmac.New(sha256.New, key)
		_, _ = m.Write([]byte(scope + "\x00" + kind + "\x00" + value))
		return hex.EncodeToString(m.Sum(nil))
	}
	if format == "wazuh_context" {
		event, err := projectEvent("wazuh", raw)
		if err != nil {
			return event, err
		}
		var a struct {
			Agent struct {
				ID string `json:"id"`
			} `json:"agent"`
			Data struct {
				SrcUser string `json:"srcuser"`
			} `json:"data"`
			Syscheck struct {
				Path   string `json:"path"`
				SHA256 string `json:"sha256_after"`
			} `json:"syscheck"`
		}
		if json.Unmarshal(raw, &a) != nil || a.Agent.ID == "" || a.Syscheck.Path == "" {
			return event, errors.New("FIM agent and object required")
		}
		c := &datasafety.Context{Kind: "endpoint", Channel: "file_change", Operation: "change", Enforcement: "observed", Capture: "partial", Categories: []string{}, SubjectRef: ref("subject", a.Data.SrcUser), DeviceRef: ref("device", a.Agent.ID), ObjectRef: ref("file", a.Syscheck.Path)}
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(a.Syscheck.Path)), ".")
		if slices.Contains([]string{"txt", "csv", "json", "jsonl", "docx", "pptx", "xlsx", "pdf", "png", "jpg"}, ext) {
			c.FileType = ext
		} else {
			c.FileType = "other"
		}
		if a.Syscheck.SHA256 != "" {
			c.FileDigest = strings.ToLower(a.Syscheck.SHA256)
		}
		if c.Validate() != nil {
			return event, errors.New("invalid FIM projection")
		}
		event.DataContext = c
		if event.Severity == "low" || event.Severity == "medium" {
			event.Outcome = "activity"
		}
		return event, nil
	}
	var a struct {
		Timestamp string `json:"timestamp"`
		User      string `json:"user"`
		Database  string `json:"dbname"`
		Session   string `json:"session_id"`
		Line      int64  `json:"line_num"`
		Message   string `json:"message"`
	}
	if json.Unmarshal(raw, &a) != nil || a.User == "" || a.Database == "" || a.Session == "" || a.Line < 1 || !strings.HasPrefix(a.Message, "AUDIT: ") {
		return store.SecurityEventInput{}, errors.New("PostgreSQL JSON audit envelope required")
	}
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(a.Message, "AUDIT: ")))
	reader.FieldsPerRecord = -1
	fields, err := reader.Read()
	if err != nil || len(fields) < 9 || len(fields) > 10 {
		return store.SecurityEventInput{}, errors.New("pgAudit CSV columns required")
	}
	if _, err = reader.Read(); err != io.EOF {
		return store.SecurityEventInput{}, errors.New("single audit row required")
	}
	if !slices.Contains([]string{"SESSION", "OBJECT"}, fields[0]) || !slices.Contains([]string{"READ", "WRITE", "FUNCTION", "ROLE", "DDL", "MISC", "MISC_SET"}, fields[3]) {
		return store.SecurityEventInput{}, errors.New("unsupported audit class")
	}
	if _, err = strconv.ParseInt(fields[1], 10, 64); err != nil {
		return store.SecurityEventInput{}, errors.New("statement ID required")
	}
	observed, err := time.Parse(time.RFC3339Nano, a.Timestamp)
	if err != nil {
		for _, layout := range []string{"2006-01-02 15:04:05.999 MST", "2006-01-02 15:04:05.999 -0700"} {
			observed, err = time.Parse(layout, a.Timestamp)
			if err == nil {
				break
			}
		}
	}
	if err != nil {
		return store.SecurityEventInput{}, errors.New("audit timestamp required")
	}
	op := "execute"
	if fields[3] == "READ" {
		op = "read"
	} else if fields[3] == "WRITE" {
		op = "write"
	}
	c := &datasafety.Context{Kind: "database", Channel: "database", Operation: op, Enforcement: "observed", Capture: "partial", Categories: []string{}, SubjectRef: ref("subject", a.User), DatabaseRef: ref("database", a.Database), DeviceRef: ref("database-session", a.Database+"\x00"+a.Session), ObjectRef: ref("database-object", a.Database+"\x00"+fields[6]), StatementRef: ref("statement", a.Session+"\x00"+fields[1]+"\x00"+fields[2])}
	if fields[6] == "" {
		c.ObjectRef = ""
	}
	// STATEMENT and PARAMETER columns intentionally never leave the source process.
	if c.Validate() != nil {
		return store.SecurityEventInput{}, errors.New("invalid audit projection")
	}
	return store.SecurityEventInput{DataContext: c, EventID: "pgaudit:" + ref("event", a.Session+"\x00"+strconv.FormatInt(a.Line, 10)), ObservedAt: observed.UTC(), Outcome: "activity", Category: "access", Rule: "pgaudit:" + strings.ToLower(fields[3]), Severity: "low", Count: 1, WindowSeconds: 1}, nil
}
