// application-agent pulls a customer-approved metadata API in the customer's
// environment. Source credentials and unprojected configuration never leave it.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxSnapshotBytes = 512 << 10
	observationPath  = "/api/v1/application-agent/observations"
)

var (
	errSource    = errors.New("application API snapshot unavailable or invalid")
	errDelivery  = errors.New("observation delivery failed")
	allowedFacts = map[string]bool{
		"application_name": true, "application_vendor": true, "application_model": true, "application_region": true,
		"application_retention": true, "application_training_use": true, "application_service_audience": true,
		"application_knowledge_bases": true, "application_tools": true, "application_data_categories": true,
	}
	credentialValue    = regexp.MustCompile(`(?i)(?:\bbearer\s+\S{8,}|(?:api[_ -]?key|access[_ -]?token|refresh[_ -]?token|client[_ -]?secret|password|passwd|authorization|credential)["']?\s*[:=]\s*\S+|-----BEGIN[^\r\n]*PRIVATE KEY-----|\b(?:sk-(?:proj-|ant-)?|gh[opusr]_|github_pat_|xox[baprs]-|AIza)[A-Za-z0-9_-]{12,}|\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})`)
	cloudAccessKey     = regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)
	basicAuthorization = regexp.MustCompile(`(?i)\bbasic\s+([A-Za-z0-9+/]+={0,2})`)
	metadataURL        = regexp.MustCompile(`https?://[^\s<>"']+`)
)

type config struct {
	platform, sourceAPI     *url.URL
	platformToken, apiToken string
}

type applicationItem struct {
	AssetID int64             `json:"assetId"`
	Facts   map[string]string `json:"facts"`
}

type snapshot struct {
	Items []applicationItem `json:"items"`
}

type observation struct {
	BatchID    string            `json:"batchId"`
	ObservedAt time.Time         `json:"observedAt"`
	Status     string            `json:"status"`
	Items      []applicationItem `json:"items"`
}

func main() {
	interval := flag.Duration("interval", time.Minute, "collection interval, at least 30s")
	once := flag.Bool("once", false, "collect and deliver one observation")
	loopback := flag.Bool("allow-insecure-loopback", false, "allow explicit literal loopback HTTP for synthetic development fixtures")
	flag.Parse()
	if flag.NArg() != 0 || *interval < 30*time.Second {
		fmt.Fprintln(os.Stderr, "use no positional arguments and an interval of at least 30s")
		os.Exit(1)
	}
	c, err := readConfig(os.Getenv, *loopback)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	client := newHTTPClient()
	defer client.CloseIdleConnections()
	if err := run(ctx, c, client, *once, *interval, os.Stderr); err != nil && !errors.Is(err, context.Canceled) {
		os.Exit(1)
	}
}

func readConfig(getenv func(string) string, loopback bool) (config, error) {
	var c config
	var err error
	if c.platform, err = approvedURL(getenv("SCARLET_URL"), true, loopback); err != nil {
		return c, errors.New("SCARLET_URL requires a fixed HTTPS origin; only explicit literal loopback HTTP is permitted for development")
	}
	if c.sourceAPI, err = approvedURL(getenv("SCARLET_APPLICATION_API_URL"), false, loopback); err != nil {
		return c, errors.New("SCARLET_APPLICATION_API_URL requires a fixed HTTPS URL without credentials, query or fragment")
	}
	c.platformToken, c.apiToken = getenv("SCARLET_APPLICATION_TOKEN"), getenv("SCARLET_APPLICATION_API_TOKEN")
	if !validToken(c.platformToken, false) || !validToken(c.apiToken, true) {
		return c, errors.New("configure valid application source credentials in the runtime environment")
	}
	return c, nil
}

func approvedURL(raw string, originOnly, loopback bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || originOnly && u.Path != "" && u.Path != "/" {
		return nil, errors.New("invalid endpoint")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !loopback || ip == nil || !ip.IsLoopback() {
			return nil, errors.New("verified HTTPS is required")
		}
	}
	return u, nil
}

func validToken(token string, optional bool) bool {
	if token == "" {
		return optional
	}
	return len(token) <= 8192 && strings.IndexFunc(token, func(r rune) bool { return r < 33 || r > 126 }) < 0
}

func newHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func collect(ctx context.Context, c config, client *http.Client) ([]applicationItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.sourceAPI.String(), nil)
	if err != nil {
		return nil, errSource
	}
	req.Header.Set("Accept", "application/json")
	if c.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiToken)
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errSource
	}
	defer response.Body.Close()
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || err != nil || contentType != "application/json" || response.ContentLength > maxSnapshotBytes {
		return nil, errSource
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxSnapshotBytes+1))
	if err != nil || len(raw) > maxSnapshotBytes {
		return nil, errSource
	}
	return decodeSnapshot(raw)
}

func decodeSnapshot(raw []byte) ([]applicationItem, error) {
	if len(raw) > maxSnapshotBytes || !utf8.Valid(raw) || uniqueJSON(raw) != nil {
		return nil, errSource
	}
	var value snapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || value.Items == nil || len(value.Items) > 50 {
		return nil, errSource
	}
	seen := map[int64]bool{}
	for _, item := range value.Items {
		// assetId is an explicit customer-approved platform mapping, never an
		// inference from a display name. The platform also enforces source scope.
		if item.AssetID <= 0 || seen[item.AssetID] || len(item.Facts) == 0 || len(item.Facts) > len(allowedFacts) {
			return nil, errSource
		}
		seen[item.AssetID] = true
		for key, value := range item.Facts {
			if !allowedFacts[key] || !safeFact(value) {
				return nil, errSource
			}
		}
	}
	return value.Items, nil
}

func safeFact(value string) bool {
	if strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > 1000 || strings.IndexFunc(value, unicode.IsControl) >= 0 || credentialValue.MatchString(value) || cloudAccessKey.MatchString(value) {
		return false
	}
	for _, match := range basicAuthorization.FindAllStringSubmatch(value, -1) {
		decoded, err := base64.StdEncoding.DecodeString(match[1])
		if err == nil && bytes.ContainsRune(decoded, ':') {
			return false
		}
	}
	for _, raw := range metadataURL.FindAllString(value, -1) {
		u, err := url.Parse(raw)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return false
		}
	}
	return true
}

// The schema has at most five nested levels. Reject duplicate keys before
// decoding so ambiguous asset mappings or fact values are never accepted.
func uniqueJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value func(int) error
	value = func(depth int) error {
		if depth > 6 {
			return errSource
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, container := token.(json.Delim)
		if !container {
			return nil
		}
		if delim != '{' && delim != '[' {
			return errSource
		}
		seen := map[string]bool{}
		for decoder.More() {
			if delim == '{' {
				key, err := decoder.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] || depth == 0 && name != "items" || depth == 2 && name != "assetId" && name != "facts" || depth == 3 && !allowedFacts[name] {
					return errSource
				}
				seen[name] = true
			}
			if err := value(depth + 1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errSource
	}
	return nil
}

func deliver(ctx context.Context, c config, client *http.Client, body []byte) error {
	u := *c.platform
	u.Path = observationPath
	u.RawPath = ""
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
		if err != nil {
			return errDelivery
		}
		req.Header.Set("Authorization", "Bearer "+c.platformToken)
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		status := 0
		if err == nil {
			status = response.StatusCode
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
		}
		if status >= 200 && status < 300 {
			return nil
		}
		if ctx.Err() != nil || status >= 300 && status < 500 && status != http.StatusTooManyRequests {
			return errDelivery
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return errDelivery
			case <-time.After(time.Duration(attempt+1) * 100 * time.Millisecond):
			}
		}
	}
	return errDelivery
}

func runOnce(ctx context.Context, c config, client *http.Client, out io.Writer) error {
	items, sourceErr := collect(ctx, c, client)
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		fmt.Fprintln(out, "application-agent: unable to create an observation identifier")
		return errDelivery
	}
	// observedAt means when this API response was collected; it is not the
	// supplier's configuration-change time or an independent compliance result.
	observation := observation{BatchID: hex.EncodeToString(nonce), ObservedAt: time.Now().UTC(), Status: "success", Items: items}
	if sourceErr != nil {
		observation.Status, observation.Items = "error", []applicationItem{}
	}
	body, err := json.Marshal(observation)
	if err != nil || len(body) > maxSnapshotBytes {
		sourceErr = errSource
		observation.Status, observation.Items = "error", []applicationItem{}
		body, _ = json.Marshal(observation)
	}
	if err := deliver(ctx, c, client, body); err != nil {
		fmt.Fprintln(out, "application-agent: observation delivery failed; source state is unconfirmed")
		return errDelivery
	}
	if sourceErr != nil {
		fmt.Fprintln(out, "application-agent: source unavailable or invalid; failure observation delivered")
		return errSource
	}
	fmt.Fprintln(out, "application-agent: configuration observation delivered")
	return nil
}

func run(ctx context.Context, c config, client *http.Client, once bool, interval time.Duration, out io.Writer) error {
	for {
		err := runOnce(ctx, c, client, out)
		if once {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
