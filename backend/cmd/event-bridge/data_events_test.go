package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDataEventAdaptersProjectHMACWithoutSourceContent(t *testing.T) {
	t.Setenv("SCARLET_EVENT_CONTEXT_KEY_HEX", strings.Repeat("ab", 32))
	t.Setenv("SCARLET_EVENT_CONTEXT_SCOPE", "fixture-source")
	rows := map[string]string{
		"wazuh_context": `{"id":"fim.42","timestamp":"2026-11-12T10:00:00Z","agent":{"id":"private-device"},"rule":{"id":"550","level":10},"data":{"srcuser":"private-user"},"syscheck":{"path":"/private/finance.csv","sha256_after":"` + strings.Repeat("a", 64) + `"},"full_log":"do not export"}`,
		"pgaudit":       `{"timestamp":"2026-11-12 10:00:00.123 UTC","user":"private-user","dbname":"private-db","session_id":"private-session","line_num":3,"message":"AUDIT: SESSION,1,1,READ,SELECT,TABLE,private.secret,SELECT private_value FROM private.secret,<not logged>"}`,
	}
	for format, raw := range rows {
		e, err := projectEvent(format, []byte(raw))
		if err != nil {
			t.Fatal(format, err)
		}
		if e.DataContext == nil || e.DataContext.Validate() != nil || len(e.DataContext.SubjectRef) != 64 {
			t.Fatal("missing safe projection")
		}
		body, _ := json.Marshal(e)
		for _, private := range []string{"private-user", "private-device", "/private/", "private-db", "private-session", "private.secret", "SELECT ", "private_value", "full_log"} {
			if strings.Contains(string(body), private) {
				t.Fatal("source content escaped", format, private)
			}
		}
		again, err := projectEvent(format, []byte(raw))
		if err != nil || again.EventID != e.EventID || again.DataContext.SubjectRef != e.DataContext.SubjectRef {
			t.Fatal("projection not deterministic")
		}
	}
	t.Setenv("SCARLET_EVENT_CONTEXT_KEY_HEX", "")
	if _, err := projectEvent("pgaudit", []byte(rows["pgaudit"])); err == nil {
		t.Fatal("identity projected without source key")
	}
}
