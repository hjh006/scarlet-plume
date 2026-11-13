package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func main() {
	origin := flag.String("url", "", "fixed Scarlet Plume HTTPS origin")
	format := flag.String("format", "normalized", "normalized, wazuh, wazuh_context or pgaudit")
	file := flag.String("file", "", "read a single alert file instead of stdin")
	local := flag.Bool("local", false, "allow local Docker app or loopback HTTP")
	flag.Parse()
	if flag.NArg() != 0 {
		stop("use -file for a Wazuh Integrator alert path")
	}
	u, err := url.Parse(*origin)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || u.Scheme != "https" && (u.Scheme != "http" || !*local || !(u.Hostname() == "app" || u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())) {
		stop("provide a fixed HTTPS origin, or a local origin with -local")
	}
	token := os.Getenv("SCARLET_OPERATIONS_TOKEN")
	if token == "" || len(token) > 1024 || strings.ContainsAny(token, " \r\n\t") {
		stop("set SCARLET_OPERATIONS_TOKEN in the integration environment")
	}
	var reader io.Reader = os.Stdin
	var opened *os.File
	if *file != "" {
		opened, err = os.Open(*file)
		if err != nil {
			stop("alert file is not readable")
		}
		defer opened.Close()
		reader = opened
	}
	raw, err := io.ReadAll(io.LimitReader(reader, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		stop("alert exceeds source-side size limit")
	}
	event, err := projectEvent(*format, raw)
	if err != nil {
		stop("unsupported or incomplete alert metadata")
	}
	body, err := json.Marshal(event)
	if err != nil {
		stop("cannot encode event metadata")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	u.Path = "/api/v1/operations/security-events"
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		r, _ := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		response, err := client.Do(r)
		status := 0
		if err == nil {
			status = response.StatusCode
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
		}
		cancel()
		if status >= 200 && status < 300 {
			fmt.Println("event metadata accepted")
			return
		}
		if status >= 400 && status < 500 && status != 429 {
			stop("event source or metadata was rejected")
		}
		if attempt < 2 {
			time.Sleep(time.Duration(200*(1<<attempt)) * time.Millisecond)
		}
	}
	stop("event delivery failed; retain the original source alert for idempotent replay")
}
func stop(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }

func projectEvent(format string, raw []byte) (store.SecurityEventInput, error) {
	if format == "wazuh_context" || format == "pgaudit" {
		return projectDataEvent(format, raw)
	}
	if format == "normalized" {
		var event store.SecurityEventInput
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&event) != nil {
			return event, errors.New("invalid normalized event")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return event, errors.New("multiple alerts")
		}
		return event, nil
	}
	if format != "wazuh" {
		return store.SecurityEventInput{}, errors.New("unsupported event format")
	}
	// Wazuh full_log, data, host/agent identity and free-text descriptions are
	// intentionally not projected into the platform's metadata-only contract.
	var alert struct {
		ID        string `json:"id"`
		Timestamp string `json:"timestamp"`
		Rule      struct {
			ID    json.RawMessage `json:"id"`
			Level int             `json:"level"`
		} `json:"rule"`
	}
	if json.Unmarshal(raw, &alert) != nil || alert.ID == "" || alert.Rule.Level < 0 || alert.Rule.Level > 16 {
		return store.SecurityEventInput{}, errors.New("invalid alert")
	}
	var rule string
	if json.Unmarshal(alert.Rule.ID, &rule) != nil {
		var number json.Number
		if json.Unmarshal(alert.Rule.ID, &number) != nil {
			return store.SecurityEventInput{}, errors.New("invalid rule")
		}
		if _, err := strconv.ParseInt(string(number), 10, 64); err != nil {
			return store.SecurityEventInput{}, err
		}
		rule = string(number)
	}
	var observed time.Time
	var err error
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999-0700"} {
		observed, err = time.Parse(layout, alert.Timestamp)
		if err == nil {
			break
		}
	}
	if err != nil || rule == "" {
		return store.SecurityEventInput{}, errors.New("invalid alert identity or timestamp")
	}
	severity := "low"
	switch {
	case alert.Rule.Level >= 13:
		severity = "critical"
	case alert.Rule.Level >= 10:
		severity = "high"
	case alert.Rule.Level >= 7:
		severity = "medium"
	}
	return store.SecurityEventInput{EventID: alert.ID, ObservedAt: observed.UTC(), Outcome: "observed", Category: "custom", Rule: "wazuh:" + rule, Severity: severity, Count: 1, WindowSeconds: 1}, nil
}
