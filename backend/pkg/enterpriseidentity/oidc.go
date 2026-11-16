// Package enterpriseidentity implements the relying-party boundary; provisioning
// and authorization remain in the workspace's PostgreSQL control plane.
package enterpriseidentity

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type Config struct {
	Issuer, ClientID, ClientSecret, RedirectURL string
}

type Client struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	http     *http.Client
}

func New(ctx context.Context, cfg Config, client *http.Client) (*Client, error) {
	if !secureURL(cfg.Issuer) || !secureURL(cfg.RedirectURL) || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("invalid enterprise identity configuration")
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
			Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			ResponseHeaderTimeout: 3 * time.Second, MaxResponseHeaderBytes: 32768,
		}}
	}
	// Discovery, token exchange and JWKS may contact different hosts, but every
	// endpoint must use TLS and cannot silently redirect to another destination.
	base := *client
	base.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	base.Transport = tlsOnly{transport}
	ctx = oidc.ClientContext(ctx, &base)
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, errors.New("identity discovery failed")
	}
	endpoint := provider.Endpoint()
	if !secureURL(endpoint.AuthURL) || !secureURL(endpoint.TokenURL) {
		return nil, errors.New("identity endpoints must use HTTPS")
	}
	return &Client{
		oauth:    oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: cfg.RedirectURL, Endpoint: endpoint, Scopes: []string{oidc.ScopeOpenID, oidc.ScopeProfile}},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID, SupportedSigningAlgs: []string{"RS256", "PS256", "ES256"}}), http: &base,
	}, nil
}

func secureURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Fragment == "" && u.Opaque == ""
}

type tlsOnly struct{ http.RoundTripper }

func (t tlsOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if !secureURL(r.URL.String()) {
		return nil, errors.New("identity transport requires HTTPS")
	}
	return t.RoundTripper.RoundTrip(r)
}

func (c *Client) AuthorizationURL(state, nonce, verifier string) string {
	return c.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
}

// Subject returns only a verified issuer-scoped subject. Email and role claims
// are deliberately excluded from the authorization contract.
func (c *Client) Subject(ctx context.Context, code, nonce, verifier string) (string, error) {
	ctx = oidc.ClientContext(ctx, c.http)
	token, e := c.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if e != nil {
		return "", errors.New("identity exchange failed")
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok || len(raw) > 65536 {
		return "", errors.New("identity token missing or too large")
	}
	id, e := c.verifier.Verify(ctx, raw)
	if e != nil {
		return "", errors.New("identity token rejected")
	}
	if nonce == "" || subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(nonce)) != 1 || id.Subject == "" || len(id.Subject) > 255 || id.IssuedAt.IsZero() || id.IssuedAt.After(time.Now().Add(time.Minute)) {
		return "", errors.New("identity nonce or subject rejected")
	}
	var claims struct {
		AuthorizedParty string `json:"azp"`
	}
	if e := id.Claims(&claims); e != nil || (claims.AuthorizedParty != "" && claims.AuthorizedParty != c.oauth.ClientID) || (len(id.Audience) > 1 && claims.AuthorizedParty != c.oauth.ClientID) {
		return "", errors.New("identity authorized party rejected")
	}
	return id.Subject, nil
}
