package aisecurity

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestScannerPrivateCAIsScopedAndVerifiesHostname(t *testing.T) {
	handler, err := NewMediaInspectionHandler(func(ctx context.Context, a MediaAsset) (MediaReport, error) {
		return MediaReport{Digest: a.Digest, MIMEType: a.MIMEType, InspectionVersion: MediaInspectionVersion, Complete: true}, nil
	}, sourceTestToken)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	before, _ := x509.SystemCertPool()
	client, err := NewHTTPMediaProcessorWithCA(server.URL+"/v1/inspect", sourceTestToken, false, ca)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := canonicalMedia("image/png", testPNG(), testMediaPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client(t.Context(), asset); err != nil {
		t.Fatal("approved private scanner CA rejected", err)
	}
	untrusted, _ := NewHTTPMediaProcessor(server.URL+"/v1/inspect", sourceTestToken, false)
	if _, err = untrusted(t.Context(), asset); err == nil {
		t.Fatal("private CA leaked to default scanner trust")
	}
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	wrongHost, _ := NewHTTPMediaProcessorWithCA("https://localhost:"+port+"/v1/inspect", sourceTestToken, false, ca)
	if _, err = wrongHost(t.Context(), asset); err == nil {
		t.Fatal("scanner hostname verification disabled")
	}
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherTemplate := &x509.Certificate{SerialNumber: big.NewInt(7001), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	otherDER, err := x509.CreateCertificate(rand.Reader, otherTemplate, otherTemplate, &otherKey.PublicKey, otherKey)
	if err != nil {
		t.Fatal(err)
	}
	badCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: otherDER})
	wrong, _ := NewHTTPMediaProcessorWithCA(server.URL+"/v1/inspect", sourceTestToken, false, badCA)
	if _, err = wrong(t.Context(), asset); err == nil {
		t.Fatal("wrong private CA accepted")
	}
	after, _ := x509.SystemCertPool()
	if !before.Equal(after) {
		t.Fatal("scanner modified system roots")
	}
	ordinary := &http.Client{Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}}
	if r, e := ordinary.Get(server.URL); e == nil {
		r.Body.Close()
		t.Fatal("scanner CA changed unrelated HTTPS trust")
	}
	if _, err = NewHTTPMediaProcessorWithCA(server.URL, sourceTestToken, false, []byte("not a certificate")); err == nil {
		t.Fatal("invalid CA accepted")
	}
}
