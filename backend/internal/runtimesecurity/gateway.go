package runtimesecurity

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var requestID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type gatewayEntry struct {
	TransportHop       string             `json:"transportHop,omitempty"`
	ClientIP           string             `json:"clientIp,omitempty"`
	Transport          string             `json:"transport,omitempty"`
	Context            *RuntimeContext    `json:"context,omitempty"`
	ValueFingerprints  []ValueFingerprint `json:"valueFingerprints,omitempty"`
	Method             string             `json:"method"`
	Route              string             `json:"route"`
	Status             json.RawMessage    `json:"status"`
	ObservedAt         time.Time          `json:"observedAt"`
	EventID            string             `json:"eventId"`
	TraceRef           string             `json:"traceRef,omitempty"`
	SpanRef            string             `json:"spanRef,omitempty"`
	SubjectRef         string             `json:"subjectRef,omitempty"`
	DatabaseTargets    []DatabaseTarget   `json:"databaseTargets,omitempty"`
	DataFields         []DataField        `json:"dataFields,omitempty"`
	CaptureWarnings    []string           `json:"captureWarnings,omitempty"`
	AuthState          string             `json:"authState,omitempty"`
	AuthRequired       *bool              `json:"authRequired,omitempty"`
	SecuritySignals    []SecuritySignal   `json:"securitySignals,omitempty"`
	InspectedLocations []string           `json:"inspectedLocations,omitempty"`
}

// ParseGateway accepts a batch of sanitized, vendor-neutral edge observations.
func ParseGateway(raw []byte) ([]Observation, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var entries []gatewayEntry
	if err := dec.Decode(&entries); err != nil || len(entries) == 0 || len(entries) > MaxObservations {
		return nil, ErrInvalidObservation
	}
	var extra json.RawMessage
	if dec.Decode(&extra) != io.EOF {
		return nil, ErrInvalidObservation
	}
	seen := make(map[string]bool, len(entries))
	out := make([]Observation, 0, len(entries))
	for _, e := range entries {
		status, err := strconv.Atoi(strings.Trim(string(e.Status), `"`))
		if err != nil || !requestID.MatchString(e.EventID) || seen[e.EventID] {
			return nil, ErrInvalidObservation
		}
		seen[e.EventID] = true
		obs, err := NormalizeObservation(Observation{ClientIP: e.ClientIP, Transport: e.Transport, TransportHop: e.TransportHop, Context: e.Context, ValueFingerprints: e.ValueFingerprints, Method: e.Method, Route: e.Route, Status: status, ObservedAt: e.ObservedAt, TraceRef: e.TraceRef, SpanRef: e.SpanRef, SubjectRef: e.SubjectRef, DatabaseTargets: e.DatabaseTargets, DataFields: e.DataFields, CaptureWarnings: e.CaptureWarnings, AuthState: e.AuthState, AuthRequired: e.AuthRequired, SecuritySignals: e.SecuritySignals, InspectedLocations: e.InspectedLocations})
		if err != nil {
			return nil, err
		}
		out = append(out, obs)
	}
	return out, nil
}
