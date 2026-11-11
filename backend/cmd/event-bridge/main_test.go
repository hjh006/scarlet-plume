package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWazuhProjectionContainsOnlyControlledMetadata(t *testing.T) {
	for _, rule := range []string{`"100101"`, `100101`} {
		raw := []byte(`{"id":"fixture.42","timestamp":"2026-11-11T09:00:00.123+0000","rule":{"id":` + rule + `,"level":12,"description":"do not upload"},"full_log":"private@example.test original payload","agent":{"name":"private-machine"},"data":{"password":"private-value"}}`)
		event, err := projectEvent("wazuh", raw)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(event)
		if event.Rule != "wazuh:100101" || event.Severity != "high" || event.Count != 1 || event.WindowSeconds != 1 || event.EventID != "fixture.42" {
			t.Fatalf("invalid projection: %+v", event)
		}
		for _, private := range []string{"private@", "private-machine", "private-value", "full_log", "do not upload"} {
			if strings.Contains(string(body), private) {
				t.Fatal("original alert content escaped the source projection")
			}
		}
	}
}
func TestNormalizedProjectionRejectsUncontrolledFields(t *testing.T) {
	for _, raw := range []string{`{"eventId":"fixture","full_log":"original"}`, `{} {}`, `{"observedAt":"invalid"}`} {
		if _, err := projectEvent("normalized", []byte(raw)); err == nil {
			t.Fatal("invalid metadata accepted")
		}
	}
}
