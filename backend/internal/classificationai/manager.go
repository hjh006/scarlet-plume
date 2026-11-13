package classificationai

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const maxResponseBytes = 512 << 10

var (
	ErrConfiguration         = errors.New("AI 模型连接配置无效")
	ErrEncryptionUnavailable = errors.New("AI 凭据加密尚未配置")
	ErrCredential            = errors.New("AI 凭据无法处理")
	ErrRequest               = errors.New("AI 模型请求失败，请核对连接、协议、权限和模型配置")
	ErrResponse              = errors.New("AI 模型响应无效或超出限制")
	ErrMetadata              = errors.New("AI 分类输入元数据无效或超出限制")
)

type Profile struct {
	BaseURL  string
	Protocol string
	Model    string
	APIKey   string
}

type Manager struct {
	cipher  cipher.AEAD
	allowed map[string]bool
	roots   *x509.CertPool
	lookup  func(context.Context, string) ([]net.IPAddr, error)
	dial    func(context.Context, string, string) (net.Conn, error)
}

// New uses system certificate trust. An empty key leaves credential storage off;
// the built-in rule engine does not require an AI connection or encryption key.
func New(keyHex, allowedHosts string) (*Manager, error) {
	return NewWithRootCAs(keyHex, allowedHosts, nil)
}

// NewWithRootCAs supports verified private-CA HTTPS endpoints and local tests.
// Certificate and hostname verification always remain enabled.
func NewWithRootCAs(keyHex, allowedHosts string, roots *x509.CertPool) (*Manager, error) {
	m := &Manager{allowed: map[string]bool{}, lookup: net.DefaultResolver.LookupIPAddr, dial: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}
	if roots != nil {
		m.roots = roots.Clone()
	}
	for _, entry := range strings.FieldsFunc(allowedHosts, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		u, err := normalizeURL("https://" + entry)
		if err != nil || u.Path != "" {
			return nil, ErrConfiguration
		}
		m.allowed[authority(u)] = true
	}
	if keyHex == "" {
		return m, nil
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return nil, ErrConfiguration
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrConfiguration
	}
	m.cipher, err = cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, ErrConfiguration
	}
	return m, nil
}

func (m *Manager) ReadyEncryption() bool { return m != nil && m.cipher != nil }

func NormalizeBaseURL(base string) (string, error) {
	u, err := normalizeURL(base)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func normalizeURL(base string) (*url.URL, error) {
	if len(base) == 0 || len(base) > 2048 || strings.Contains(base, "#") || strings.IndexFunc(base, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == '\\' }) >= 0 {
		return nil, ErrConfiguration
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, ErrConfiguration
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" {
		return nil, ErrConfiguration
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return nil, ErrConfiguration
		}
		host = ip.Unmap().String()
	} else {
		if len(host) > 253 || len(host) == 0 {
			return nil, ErrConfiguration
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return nil, ErrConfiguration
			}
			for _, r := range label {
				if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
					return nil, ErrConfiguration
				}
			}
		}
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, ErrConfiguration
		}
		port = strconv.Itoa(n)
	}
	if port == "443" {
		port = ""
	}
	u.Host = host
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path != "" && path.Clean(u.Path) != u.Path {
		return nil, ErrConfiguration
	}
	return u, nil
}

func authority(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

func credentialAAD(workspaceID int64, baseURL string) ([]byte, error) {
	base, err := NormalizeBaseURL(baseURL)
	if err != nil || workspaceID < 1 {
		return nil, ErrCredential
	}
	return []byte(fmt.Sprintf("classification-ai:%d\x00%s", workspaceID, base)), nil
}

func (m *Manager) Encrypt(workspaceID int64, baseURL, plain string) ([]byte, error) {
	if !m.ReadyEncryption() {
		return nil, ErrEncryptionUnavailable
	}
	aad, err := credentialAAD(workspaceID, baseURL)
	if err != nil || plain == "" || !validAPIKey(plain) {
		return nil, ErrCredential
	}
	return m.cipher.Seal(nil, nil, []byte(plain), aad), nil
}

func (m *Manager) Decrypt(workspaceID int64, baseURL string, encrypted []byte) (string, error) {
	if !m.ReadyEncryption() {
		return "", ErrEncryptionUnavailable
	}
	aad, err := credentialAAD(workspaceID, baseURL)
	if err != nil || len(encrypted) > 8192+m.cipher.Overhead() {
		return "", ErrCredential
	}
	plain, err := m.cipher.Open(nil, nil, encrypted, aad)
	if err != nil {
		return "", ErrCredential
	}
	defer clear(plain)
	return string(plain), nil
}

func validAPIKey(value string) bool {
	return len(value) <= 8192 && strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}

// Public egress excludes private, local and special-use ranges. DNS is resolved
// once and every returned address is validated before any connection is made.
func publicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range specialUseRanges {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

var specialUseRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("2001:2::/48"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
}

func (m *Manager) request(ctx context.Context, profile Profile, method, resource string, payload any) ([]byte, error) {
	if m == nil || (profile.Protocol != "chat_completions" && profile.Protocol != "responses") || !validAPIKey(profile.APIKey) {
		return nil, ErrConfiguration
	}
	u, err := normalizeURL(profile.BaseURL)
	if err != nil {
		return nil, ErrConfiguration
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	allowed := m.allowed[authority(u)]
	host := u.Hostname()
	if !allowed && (host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "localhost.localdomain") {
		return nil, ErrRequest
	}
	addresses, err := m.lookup(ctx, host)
	if err != nil || len(addresses) == 0 || len(addresses) > 32 {
		return nil, ErrRequest
	}
	for _, address := range addresses {
		if address.Zone != "" || address.IP == nil || !allowed && !publicIP(address.IP) {
			return nil, ErrRequest
		}
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	target := net.JoinHostPort(host, port)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host, RootCAs: m.roots}}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != target {
			return nil, ErrRequest
		}
		for _, resolved := range addresses {
			conn, err := m.dial(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
			if err == nil {
				return conn, nil
			}
		}
		return nil, ErrRequest
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	u.Path += "/" + resource
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil || len(encoded) > 64<<10 {
			return nil, ErrMetadata
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, ErrConfiguration
	}
	if profile.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+profile.APIKey)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, ErrRequest
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ErrRequest
	}
	if resp.ContentLength > maxResponseBytes {
		return nil, ErrResponse
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return nil, ErrResponse
	}
	return data, nil
}
