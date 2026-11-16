// ai-security-receipts forwards content-free durable customer runtime audit
// records. It never invokes models or tools; replaying an audit file is safe.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

const receiptPath = "/api/v1/ai-security/runtime-events"

func receiptURL(value string, local bool) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("platform URL must be an origin without credentials, path, query or fragment")
	}
	if u.Scheme != "https" && !(local && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
		return "", errors.New("HTTPS is required; -local permits loopback HTTP only")
	}
	u.Path = receiptPath
	return u.String(), nil
}

func forwardReceipts(ctx context.Context, input io.Reader, client *http.Client, target, token string) (int, error) {
	if !strings.HasPrefix(token, "spai_") || len(token) != 48 || strings.ContainsAny(token, " \t\r\n") {
		return 0, errors.New("invalid source token")
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	count := 0
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		var record aisecurity.AuditRecord
		if err := decoder.Decode(&record); err != nil {
			return count, fmt.Errorf("audit line %d is not a supported content-free record", count+1)
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return count, fmt.Errorf("audit line %d contains trailing JSON", count+1)
		}
		body, err := json.Marshal(record)
		if err != nil {
			return count, errors.New("could not encode audit record")
		}
		requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		r, err := http.NewRequestWithContext(requestCtx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			cancel()
			return count, errors.New("could not build receipt request")
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		response, err := client.Do(r)
		if err != nil {
			cancel()
			return count, errors.New("receipt delivery failed; retain the audit file and retry")
		}
		result, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
		response.Body.Close()
		cancel()
		if response.StatusCode != http.StatusOK || readErr != nil || len(result) > 4096 {
			return count, fmt.Errorf("receipt delivery returned HTTP %d; retain the audit file", response.StatusCode)
		}
		var receipt struct {
			ID       int64 `json:"id"`
			Replayed bool  `json:"replayed"`
		}
		if json.Unmarshal(result, &receipt) != nil || receipt.ID < 1 {
			return count, errors.New("invalid receipt acknowledgement; retain the audit file")
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return count, errors.New("audit file read failed or a record exceeds 64 KiB")
	}
	return count, nil
}

func run() error {
	platform := flag.String("platform", "", "platform HTTPS origin")
	tokenFile := flag.String("token-file", "", "0600 file containing the independent runtime source token")
	auditFile := flag.String("audit-file", "", "durable audit JSONL; empty reads stdin")
	caFile := flag.String("ca-file", "", "optional private CA PEM bundle")
	local := flag.Bool("local", false, "allow synthetic loopback HTTP")
	flag.Parse()
	target, err := receiptURL(*platform, *local)
	if err != nil {
		return err
	}
	info, err := os.Stat(*tokenFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("token-file must be a regular owner-only file")
	}
	if info.Size() > 256 {
		return errors.New("source token file is too large")
	}
	secret, err := os.ReadFile(*tokenFile)
	if err != nil {
		return errors.New("could not read source token file")
	}
	input := io.Reader(os.Stdin)
	if *auditFile != "" {
		file, err := os.Open(*auditFile)
		if err != nil {
			return errors.New("could not open audit file")
		}
		defer file.Close()
		input = file
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if *caFile != "" {
		pem, err := os.ReadFile(*caFile)
		if err != nil {
			return errors.New("could not read private CA")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return errors.New("private CA file has no valid certificate")
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	count, err := forwardReceipts(context.Background(), input, client, target, strings.TrimSpace(string(secret)))
	if err != nil {
		return err
	}
	fmt.Printf("acknowledged %d runtime audit records (including idempotent replays)\n", count)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
