package runtimesecurity

import (
	"bytes"
	"encoding/json"
	"github.com/hjh006/scarlet-plume/backend/internal/valuefingerprint"
	"strings"
	"testing"
	"time"
)

func TestSourceValueFingerprintCaptureAndNormalization(t *testing.T) {
	value := "actual.synthetic@example.invalid"
	raw := []byte(`{"contacts":[{"email":"` + value + `"}],"raw-user-name@example.invalid":"` + value + `"}`)
	key := strings.Repeat("61", 32)
	fingerprints := CaptureValueFingerprints(raw, "response", key, "fixture-key", "fixture-ws")
	if len(fingerprints) != 1 || fingerprints[0].Locator != "contacts[].email" {
		t.Fatalf("field scope lost: %+v", fingerprints)
	}
	expected, e := valuefingerprint.Create(key, "fixture-key", "fixture-ws", value)
	if e != nil || fingerprints[0].Digest != expected.Digest {
		t.Fatal("CLI and capture token differ")
	}
	fields, warnings := InspectJSON(raw, "response")
	_, e = NormalizeObservation(Observation{Method: "GET", Route: "/contacts", Status: 200, ObservedAt: time.Now(), DataFields: fields, CaptureWarnings: warnings, ValueFingerprints: fingerprints})
	if e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(fingerprints)
	if bytes.Contains(encoded, []byte(value)) {
		t.Fatal("raw value escaped")
	}
	if got := CaptureValueFingerprints(raw, "response", "", "fixture-key", "fixture-ws"); len(got) != 0 {
		t.Fatal("unconfigured capture enabled")
	}
	if got := CaptureValueFingerprints(bytes.Repeat([]byte("a"), MaxJSONCaptureBytes+1), "response", key, "fixture-key", "fixture-ws"); len(got) != 0 {
		t.Fatal("capture size budget ignored")
	}
}
