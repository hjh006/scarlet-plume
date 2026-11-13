// Package cryptoevidence instruments an explicitly authorized customer crypto
// call. It neither decrypts arbitrary data nor exports input/output/key bytes.
package cryptoevidence

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/hjh006/scarlet-plume/backend/internal/valuefingerprint"
	"regexp"
	"strings"
	"time"
)

type Record struct {
	EventID    string                 `json:"eventId"`
	TraceRef   string                 `json:"traceRef"`
	SpanRef    string                 `json:"spanRef"`
	ObservedAt time.Time              `json:"observedAt"`
	Operation  string                 `json:"operation"`
	Algorithm  string                 `json:"algorithm"`
	Input      valuefingerprint.Token `json:"input"`
	Output     valuefingerprint.Token `json:"output"`
}
type Recorder struct {
	KeyHex string
	KeyID  string
	Scope  string
}

var traceID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var spanID = regexp.MustCompile(`^[a-f0-9]{16}$`)
var algorithmID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func Valid(r Record) bool {
	if !traceID.MatchString(r.EventID) || !traceID.MatchString(r.TraceRef) || r.TraceRef == strings.Repeat("0", 32) || !spanID.MatchString(r.SpanRef) || r.SpanRef == strings.Repeat("0", 16) || !algorithmID.MatchString(r.Algorithm) || !valuefingerprint.Valid(r.Input) || !valuefingerprint.Valid(r.Output) || r.Input.KeyID != r.Output.KeyID || r.Input.Scope != r.Output.Scope || r.ObservedAt.IsZero() {
		return false
	}
	return r.Operation == "encrypt" && r.Input.Version == valuefingerprint.Version && r.Output.Version == valuefingerprint.BinaryVersion || r.Operation == "decrypt" && r.Input.Version == valuefingerprint.BinaryVersion && r.Output.Version == valuefingerprint.Version
}

// Transform wraps the application's real encryption/decryption function. The
// application retains the returned bytes; only Record is submitted as evidence.
func (r Recorder) Transform(trace, span, operation, algorithm string, input []byte, apply func([]byte) ([]byte, error)) ([]byte, Record, error) {
	if !traceID.MatchString(trace) || trace == strings.Repeat("0", 32) || !spanID.MatchString(span) || span == strings.Repeat("0", 16) || !algorithmID.MatchString(algorithm) {
		return nil, Record{}, errors.New("invalid trace or transform metadata")
	}
	if apply == nil {
		return nil, Record{}, errors.New("crypto function required")
	}
	var before valuefingerprint.Token
	var e error
	switch operation {
	case "encrypt":
		before, e = valuefingerprint.Create(r.KeyHex, r.KeyID, r.Scope, string(input))
	case "decrypt":
		before, e = valuefingerprint.CreateBytes(r.KeyHex, r.KeyID, r.Scope, input)
	default:
		return nil, Record{}, errors.New("unsupported transform operation")
	}
	if e != nil {
		return nil, Record{}, errors.New("invalid local fingerprint configuration or bounded input")
	}
	output, e := apply(input)
	if e != nil {
		return nil, Record{}, errors.New("customer crypto operation failed")
	}
	var after valuefingerprint.Token
	if operation == "encrypt" {
		after, e = valuefingerprint.CreateBytes(r.KeyHex, r.KeyID, r.Scope, output)
	} else {
		after, e = valuefingerprint.Create(r.KeyHex, r.KeyID, r.Scope, string(output))
	}
	if e != nil {
		return output, Record{}, errors.New("crypto output outside evidence scope")
	}
	id := make([]byte, 16)
	if _, e = rand.Read(id); e != nil {
		return output, Record{}, errors.New("evidence identity unavailable")
	}
	record := Record{EventID: hex.EncodeToString(id), TraceRef: trace, SpanRef: span, ObservedAt: time.Now().UTC(), Operation: operation, Algorithm: algorithm, Input: before, Output: after}
	if !Valid(record) {
		return output, Record{}, errors.New("invalid trace or transform metadata")
	}
	return output, record, nil
}
