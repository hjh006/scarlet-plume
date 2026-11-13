// Package apivalidator implements bounded, explicitly authorized source-side probes.
package apivalidator

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

type Account struct {
	UsernameEnv    string `json:"usernameEnv"`
	PasswordEnv    string `json:"passwordEnv"`
	ObjectPath     string `json:"objectPath"`
	ExpectedObject string `json:"expectedObject"`
	ExpectedOwner  string `json:"expectedOwner"`
}
type Binding struct {
	ProbePolicy  store.APIValidationProbePolicy `json:"probePolicy,omitempty"`
	Ref          string                         `json:"ref"`
	TargetOrigin string                         `json:"targetOrigin"`
	Route        string                         `json:"route"`
	Rules        []string                       `json:"rules"`
	AccountA     Account                        `json:"accountA"`
	AccountB     Account                        `json:"accountB"`
	ObjectField  string                         `json:"objectField"`
	OwnerField   string                         `json:"ownerField"`
}
type Manifest struct {
	SourceID int64     `json:"sourceId"`
	Bindings []Binding `json:"bindings"`
}

func LoadManifest(name string) (Manifest, error) {
	var m Manifest
	file, err := os.Open(name)
	if err != nil {
		return m, errors.New("local manifest unavailable")
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 65537))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil || m.SourceID < 1 || len(m.Bindings) < 1 || len(m.Bindings) > 100 {
		return Manifest{}, errors.New("invalid local manifest")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Manifest{}, errors.New("invalid trailing manifest data")
	}
	return m, nil
}
func PrivateClient(caFile string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxConnsPerHost = 2
	transport.MaxResponseHeaderBytes = 16 << 10
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, errors.New("target CA unavailable")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("invalid target CA")
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("invalid private target")
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(addresses) == 0 {
			return nil, errors.New("target_not_private")
		}
		for _, addr := range addresses {
			if !addr.IP.IsPrivate() && !addr.IP.IsLoopback() {
				return nil, errors.New("target_not_private")
			}
		}
		dialer := net.Dialer{Timeout: 2 * time.Second}
		var last error
		for _, addr := range addresses {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}
	return &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func MatchesRoute(route, rawPath string) bool {
	u, err := url.ParseRequestURI(rawPath)
	if err != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || u.Path != path.Clean(u.Path) || strings.Contains(strings.ToLower(rawPath), "%2f") || strings.Contains(strings.ToLower(rawPath), "%5c") {
		return false
	}
	a, b := strings.Split(route, "/"), strings.Split(u.Path, "/")
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.HasPrefix(a[i], "{") && strings.HasSuffix(a[i], "}") {
			if b[i] == "" || b[i] == "." || b[i] == ".." {
				return false
			}
			continue
		}
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func bindingFor(m Manifest, claim store.APIValidationClaim) (Binding, bool) {
	if m.SourceID != claim.Plan.SourceID || claim.Plan.Method != "GET" {
		return Binding{}, false
	}
	for _, b := range m.Bindings {
		if b.Ref != claim.Plan.ManifestRef || b.TargetOrigin != claim.Plan.TargetOrigin || b.Route != claim.Plan.Route {
			continue
		}
		allowed := true
		for _, rule := range claim.Plan.Rules {
			allowed = allowed && slices.Contains(b.Rules, rule)
		}
		if !allowed || b.ObjectField == "" || b.OwnerField == "" || b.AccountA.ExpectedObject == "" || b.AccountA.ExpectedOwner == "" || !MatchesRoute(b.Route, b.AccountA.ObjectPath) || slices.Contains(claim.Plan.Rules, "bola") && (b.AccountB.ExpectedObject == "" || b.AccountB.ExpectedOwner == "" || b.AccountA.ExpectedObject == b.AccountB.ExpectedObject || b.AccountA.ExpectedOwner == b.AccountB.ExpectedOwner || !MatchesRoute(b.Route, b.AccountB.ObjectPath)) || !store.NormalizeAPIProbePolicy(&b.ProbePolicy, claim.Plan.Rules, b.TargetOrigin) || policyDigest(b.ProbePolicy) != policyDigest(claim.Plan.ProbePolicy) {
			continue
		}
		return b, true
	}
	return Binding{}, false
}

type response struct {
	status    int
	id, owner string
	gap       string
	body      map[string]any
	headers   map[string]string
}

func field(value map[string]any, key string) string {
	parts := strings.Split(key, ".")
	if len(parts) > 8 {
		return ""
	}
	var current any = value
	for _, part := range parts {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[part]
	}
	text, _ := current.(string)
	return text
}
func request(ctx context.Context, client *http.Client, b Binding, account Account, objectPath string, anonymous ...bool) response {
	username, password := os.Getenv(account.UsernameEnv), os.Getenv(account.PasswordEnv)
	unauthenticated := len(anonymous) > 0 && anonymous[0]
	if !unauthenticated && (username == "" || password == "") {
		return response{gap: "credential_missing"}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", b.TargetOrigin+objectPath, nil)
	if err != nil {
		return response{gap: "manifest_mismatch"}
	}
	if !unauthenticated {
		req.SetBasicAuth(username, password)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "target_not_private") {
			return response{gap: "target_not_private"}
		}
		return response{gap: "network_error"}
	}
	defer resp.Body.Close()
	r := response{status: resp.StatusCode, headers: map[string]string{}}
	for _, h := range b.ProbePolicy.ResponseHeaders {
		r.headers[h.Name] = strings.Join(resp.Header.Values(h.Name), ", ")
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return r
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil {
		return response{status: r.status, gap: "network_error"}
	}
	if len(raw) > 65536 {
		return response{status: r.status, gap: "response_too_large"}
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return response{status: r.status, gap: "invalid_json"}
	}
	r.body = body
	r.id, r.owner = field(body, b.ObjectField), field(body, b.OwnerField)
	return r
}
func confirmed(r response, a Account) bool {
	return r.status >= 200 && r.status < 300 && r.gap == "" && r.id == a.ExpectedObject && r.owner == a.ExpectedOwner
}
func weakProvidedPassword(password string) bool {
	if utf8.RuneCountInString(password) < 12 {
		return true
	}
	return slices.Contains([]string{"password123456", "123456789012", "qwerty12345678", "admin12345678"}, strings.ToLower(password))
}
func Execute(ctx context.Context, client *http.Client, m Manifest, claim store.APIValidationClaim) store.APIValidationResult {
	result := store.APIValidationResult{PlanID: claim.Plan.ID, Revision: claim.Plan.Revision, RunRef: claim.RunRef, Mode: claim.Mode, Results: []store.APIValidationRuleResult{}}
	b, allowed := bindingFor(m, claim)
	if allowed && slices.Contains(claim.Plan.Rules, "bola") && os.Getenv(b.AccountA.UsernameEnv) == os.Getenv(b.AccountB.UsernameEnv) {
		allowed = false
	}
	if !allowed {
		for _, rule := range claim.Plan.Rules {
			result.Results = append(result.Results, store.APIValidationRuleResult{Rule: rule, Outcome: "inconclusive", Gaps: []string{"manifest_mismatch"}})
		}
		result.ObservedAt = time.Now().UTC()
		return result
	}
	for _, rule := range claim.Plan.Rules {
		r := store.APIValidationRuleResult{Rule: rule, Outcome: "inconclusive", Gaps: []string{}}
		owner := request(ctx, client, b, b.AccountA, b.AccountA.ObjectPath)
		r.Requests = 1
		r.OwnerStatus = owner.status
		r.OwnerConfirmed = confirmed(owner, b.AccountA)
		if extended, handled := extendedProbe(ctx, client, b, claim, rule, owner); handled {
			result.Results = append(result.Results, extended)
			continue
		}
		if !r.OwnerConfirmed {
			gap := owner.gap
			if gap == "" {
				gap = "owner_unconfirmed"
			}
			r.Gaps = []string{gap}
			result.Results = append(result.Results, r)
			continue
		}
		if rule == "weak_credential" {
			r.CredentialAccepted = true
			r.WeakRuleMatched = weakProvidedPassword(os.Getenv(b.AccountA.PasswordEnv))
			r.ComparisonComplete = claim.Mode == "validation"
			if claim.Mode != "validation" {
				r.Gaps = []string{"connection_only"}
			} else if r.WeakRuleMatched {
				r.Outcome = "vulnerable"
			} else {
				r.Outcome = "not_observed"
			}
			result.Results = append(result.Results, r)
			continue
		}
		other := request(ctx, client, b, b.AccountB, b.AccountB.ObjectPath)
		r.Requests = 2
		r.OtherOwnedStatus = other.status
		r.OtherAccountConfirmed = confirmed(other, b.AccountB)
		if !r.OtherAccountConfirmed {
			gap := other.gap
			if gap == "" {
				gap = "other_account_unconfirmed"
			}
			r.Gaps = []string{gap}
			result.Results = append(result.Results, r)
			continue
		}
		if claim.Mode != "validation" {
			r.Gaps = []string{"connection_only"}
			result.Results = append(result.Results, r)
			continue
		}
		cross := request(ctx, client, b, b.AccountB, b.AccountA.ObjectPath)
		r.Requests = 3
		r.OtherStatus = cross.status
		r.SameObject = confirmed(cross, b.AccountA)
		if cross.gap != "" {
			r.Gaps = []string{cross.gap}
		} else if r.SameObject {
			r.ComparisonComplete = true
			r.Outcome = "vulnerable"
		} else if cross.status == 403 || cross.status == 404 {
			r.ComparisonComplete = true
			r.Outcome = "not_observed"
		} else {
			r.Gaps = []string{"cross_response_unexpected"}
		}
		result.Results = append(result.Results, r)
	}
	result.ObservedAt = time.Now().UTC()
	return result
}
