package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
)

func listenerTLSConfig(certPath, keyPath, clientCA string) (*tls.Config, error) {
	if (certPath == "") != (keyPath == "") || certPath == "" && clientCA != "" {
		return nil, errors.New("-tls-cert and -tls-key are required together and for -tls-client-ca")
	}
	if certPath == "" {
		return nil, nil
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, errors.New("could not load listener TLS certificate and private key")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}
	if clientCA != "" {
		pool, err := certificatePool(clientCA)
		if err != nil {
			return nil, fmt.Errorf("invalid -tls-client-ca: %w", err)
		}
		config.ClientAuth = tls.RequireAndVerifyClientCert
		config.ClientCAs = pool
	}
	return config, nil
}

func configureUpstreamTLS(transport *http.Transport, upstream *url.URL, caPath string) error {
	if caPath == "" {
		return nil
	}
	if upstream.Scheme != "https" {
		return errors.New("-upstream-ca requires an HTTPS upstream")
	}
	pool, err := certificatePool(caPath)
	if err != nil {
		return fmt.Errorf("invalid -upstream-ca: %w", err)
	}
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	return nil
}

func certificatePool(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("could not read CA bundle")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, errors.New("CA bundle contains no valid PEM certificate")
	}
	return pool, nil
}
