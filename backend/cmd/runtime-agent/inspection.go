package main

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

type bodyCapture struct {
	mu       sync.Mutex
	upstream io.ReadCloser
	buffer   []byte
	expected int64
	read     int64
	complete bool
	failed   bool
	limited  bool
}

func (c *bodyCapture) Read(p []byte) (int, error) {
	n, err := c.upstream.Read(p)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.read += int64(n)
	remaining := runtimesecurity.MaxJSONCaptureBytes - len(c.buffer)
	if n > remaining {
		c.limited = true
		c.buffer = append(c.buffer, p[:remaining]...)
	} else {
		c.buffer = append(c.buffer, p[:n]...)
	}
	if err == io.EOF || c.expected >= 0 && c.read == c.expected {
		c.complete = true
	}
	if err != nil && err != io.EOF {
		c.failed = true
	}
	return n, err
}

func (c *bodyCapture) Close() error { return c.upstream.Close() }

func wrapCaptureBody(body io.ReadCloser, header http.Header, length int64, warnings *[]string) (io.ReadCloser, *bodyCapture) {
	media, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	if body == nil || err != nil || media != "application/json" && !strings.HasSuffix(media, "+json") {
		return body, nil
	}
	if encoding := header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		*warnings = append(*warnings, "json_encoding_unsupported")
		return body, nil
	}
	if length > runtimesecurity.MaxJSONCaptureBytes {
		*warnings = append(*warnings, "json_size_limit")
		return body, nil
	}
	capture := &bodyCapture{upstream: body, expected: length, complete: length == 0}
	return capture, capture
}

func captureBodyStatistics(observation *requestObservation) ([]runtimesecurity.DataField, []string) {
	fields := slices.Clone(observation.queryFields)
	warnings := slices.Clone(observation.warnings)
	for _, body := range []struct {
		capture  *bodyCapture
		location string
	}{{observation.requestCapture, "request"}, {observation.responseCapture, "response"}} {
		if body.capture == nil {
			continue
		}
		capture := body.capture
		capture.mu.Lock()
		limited, failed, complete := capture.limited, capture.failed, capture.complete
		buffer := slices.Clone(capture.buffer)
		capture.mu.Unlock()
		switch {
		case limited:
			warnings = append(warnings, "json_size_limit")
		case failed || !complete:
			warnings = append(warnings, "json_read_error")
		default:
			found, gaps := runtimesecurity.InspectJSON(buffer, body.location)
			observation.valueFingerprints = append(observation.valueFingerprints, runtimesecurity.CaptureValueFingerprints(buffer, body.location, os.Getenv("SCARLET_VALUE_KEY_HEX"), os.Getenv("SCARLET_VALUE_KEY_ID"), os.Getenv("SCARLET_VALUE_SCOPE"))...)
			if len(gaps) == 0 && (body.location != "request" || observation.queryInspected) {
				observation.inspectedLocations = append(observation.inspectedLocations, body.location)
			}
			if body.location == "request" {
				observation.securitySignals = mergeSecuritySignals(observation.securitySignals, runtimesecurity.InspectSecurity(buffer))
			}
			fields = append(fields, found...)
			warnings = append(warnings, gaps...)
		}
	}
	slices.SortFunc(fields, func(a, b runtimesecurity.DataField) int {
		return strings.Compare(a.Location+"\x00"+a.Name+"\x00"+a.Category+"\x00"+a.Method, b.Location+"\x00"+b.Name+"\x00"+b.Category+"\x00"+b.Method)
	})
	if len(fields) > runtimesecurity.MaxCaptureItems {
		fields = fields[:runtimesecurity.MaxCaptureItems]
		warnings = append(warnings, "json_field_limit")
		observation.inspectedLocations = nil
	}
	kept := []runtimesecurity.ValueFingerprint{}
	for _, fingerprint := range observation.valueFingerprints {
		if len(kept) >= 32 {
			break
		}
		for _, field := range fields {
			if field.Name == fingerprint.Locator && field.Category == fingerprint.Category {
				kept = append(kept, fingerprint)
				break
			}
		}
	}
	observation.valueFingerprints = kept
	slices.Sort(warnings)
	warnings = slices.Compact(warnings)
	return fields, warnings
}

func mergeSecuritySignals(a, b []runtimesecurity.SecuritySignal) []runtimesecurity.SecuritySignal {
	for _, signal := range b {
		index := slices.IndexFunc(a, func(x runtimesecurity.SecuritySignal) bool { return x.Kind == signal.Kind })
		if index < 0 {
			a = append(a, signal)
		} else {
			a[index].Count += signal.Count
			a[index].Observed += signal.Observed
		}
	}
	return a
}

func captureQueryStatistics(o *requestObservation, raw string) {
	values, e := url.ParseQuery(raw)
	if e != nil {
		o.warnings = append(o.warnings, "json_invalid")
		return
	}
	data, _ := json.Marshal(values)
	fields, gaps := runtimesecurity.InspectJSON(data, "request")
	for i := range fields {
		fields[i].Name = "query." + fields[i].Name
	}
	o.queryFields = fields
	o.warnings = append(o.warnings, gaps...)
	o.queryInspected = !slices.ContainsFunc(gaps, func(gap string) bool { return gap != "json_dynamic_keys" })
	tokens := runtimesecurity.CaptureValueFingerprints(data, "request", os.Getenv("SCARLET_VALUE_KEY_HEX"), os.Getenv("SCARLET_VALUE_KEY_ID"), os.Getenv("SCARLET_VALUE_SCOPE"))
	for i := range tokens {
		tokens[i].Locator = "query." + tokens[i].Locator
	}
	o.valueFingerprints = append(o.valueFingerprints, tokens...)
}
