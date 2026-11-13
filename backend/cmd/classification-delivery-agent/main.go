// classification-delivery-agent runs beside a customer-approved webhook target.
// The platform never chooses an arbitrary outbound destination for this process.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

type configuration struct {
	Platform, Token, Endpoint, ReceiverToken, CAFile string
	Local                                            bool
}

func validURL(raw string, local bool) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, "\r\n\x00") {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return local && u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())
}
func newClient(cfg configuration) (*http.Client, error) {
	if !validURL(cfg.Platform, cfg.Local) || !validURL(cfg.Endpoint, cfg.Local) || cfg.Token == "" || strings.ContainsAny(cfg.Token+cfg.ReceiverToken, "\r\n") {
		return nil, errors.New("invalid approved endpoint or credentials")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CAFile != "" {
		raw, e := os.ReadFile(cfg.CAFile)
		if e != nil {
			return nil, errors.New("cannot load approved CA")
		}
		roots, e := x509.SystemCertPool()
		if e != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(raw) {
			return nil, errors.New("invalid approved CA")
		}
		tlsConfig.RootCAs = roots
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.TLSClientConfig = tlsConfig
	return &http.Client{Transport: tr, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func post(ctx context.Context, client *http.Client, url, token string, body []byte, key string) (int, []byte, error) {
	r, e := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if e != nil {
		return 0, nil, e
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	resp, e := client.Do(r)
	if e != nil {
		return 0, nil, errors.New("transport")
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 2<<20+1))
	if e != nil || len(raw) > 2<<20 {
		return resp.StatusCode, nil, errors.New("response")
	}
	return resp.StatusCode, raw, nil
}
func runOnce(ctx context.Context, cfg configuration, client *http.Client) (bool, error) {
	nonce := make([]byte, 24)
	if _, e := rand.Read(nonce); e != nil {
		return false, e
	}
	lease := base64.RawURLEncoding.EncodeToString(nonce)
	body, _ := json.Marshal(map[string]string{"leaseId": lease})
	status, raw, e := post(ctx, client, strings.TrimRight(cfg.Platform, "/")+"/api/v1/classification-delivery-agent/claim", cfg.Token, body, "")
	if e != nil {
		return false, e
	}
	if status == 204 {
		return false, nil
	}
	if status != 200 {
		return false, fmt.Errorf("claim rejected (%d)", status)
	}
	var claim store.ClassificationDeliveryClaim
	if json.Unmarshal(raw, &claim) != nil || claim.ID < 1 || claim.LeaseID != lease || claim.LeaseUntil.Before(time.Now()) {
		return false, errors.New("invalid or expired claim")
	}
	receipt := store.ClassificationDeliveryReceipt{DeliveryID: claim.ID, LeaseID: lease, PayloadDigest: claim.PayloadDigest, Status: "failed", ErrorCode: "configuration"}
	canonical, hash, e := store.CanonicalClassificationDelivery(claim.Payload)
	if e == nil && hash == claim.PayloadDigest && claim.Endpoint == cfg.Endpoint {
		status, ack, e := post(ctx, client, cfg.Endpoint, cfg.ReceiverToken, canonical, fmt.Sprintf("scarlet-delivery:%d", claim.ID))
		receipt.ErrorCode = "transport"
		if e == nil {
			receipt.ErrorCode = "response"
			if status >= 200 && status < 300 {
				var result struct {
					DeliveryID    int64  `json:"deliveryId"`
					PayloadDigest string `json:"payloadDigest"`
					Status        string `json:"status"`
					RemoteRef     string `json:"remoteRef"`
				}
				decoder := json.NewDecoder(bytes.NewReader(ack))
				decoder.DisallowUnknownFields()
				receipt.ErrorCode = "acknowledgement"
				if decoder.Decode(&result) == nil && decoder.Decode(new(any)) == io.EOF && result.DeliveryID == claim.ID && result.PayloadDigest == hash && result.Status == "accepted" && len(result.RemoteRef) <= 200 && !strings.ContainsAny(result.RemoteRef, "\r\n\x00") {
					receipt.Status = "accepted"
					receipt.ErrorCode = ""
					receipt.RemoteRef = result.RemoteRef
					if strings.Contains(receipt.RemoteRef, cfg.Token) || cfg.ReceiverToken != "" && strings.Contains(receipt.RemoteRef, cfg.ReceiverToken) {
						receipt.RemoteRef = "[redacted]"
					}
				}
			}
		}
	}
	report, _ := json.Marshal(receipt)
	status, _, e = post(ctx, client, strings.TrimRight(cfg.Platform, "/")+"/api/v1/classification-delivery-agent/receipts", cfg.Token, report, "")
	if e != nil {
		return true, e
	}
	if status != 200 {
		return true, fmt.Errorf("receipt rejected (%d); redelivery uses the same idempotency key", status)
	}
	return true, nil
}
func main() {
	platform := flag.String("url", os.Getenv("SCARLET_URL"), "Approved Scarlet platform base URL")
	endpoint := flag.String("endpoint", os.Getenv("SCARLET_DELIVERY_ENDPOINT"), "Exact approved receiver URL")
	once := flag.Bool("once", false, "Process one eligible delivery")
	local := flag.Bool("allow-insecure-loopback", false, "Allow HTTP only for explicit loopback fixtures")
	ca := flag.String("ca", os.Getenv("SCARLET_DELIVERY_CA_FILE"), "Optional verified private CA PEM")
	interval := flag.Duration("interval", 10*time.Second, "Poll interval, at least 1s")
	flag.Parse()
	cfg := configuration{Platform: *platform, Endpoint: *endpoint, Token: os.Getenv("SCARLET_CLASSIFICATION_DELIVERY_TOKEN"), ReceiverToken: os.Getenv("SCARLET_DELIVERY_RECEIVER_TOKEN"), CAFile: *ca, Local: *local}
	client, e := newClient(cfg)
	if e != nil {
		log.Fatal(e)
	}
	if *interval < time.Second {
		log.Fatal("interval must be at least 1s")
	}
	for {
		worked, e := runOnce(context.Background(), cfg, client)
		if e != nil {
			log.Print(e)
		} else if worked {
			log.Print("delivery receipt saved")
		}
		if *once {
			if e != nil {
				os.Exit(1)
			}
			return
		}
		time.Sleep(*interval)
	}
}
