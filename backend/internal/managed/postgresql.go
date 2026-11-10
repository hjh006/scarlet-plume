package managed

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

const sampleHost = "sample-db"
const samplePort = 5432

var ErrBusy = errors.New("连接测试任务过多，请稍后重试")
var ErrPassword = errors.New("请提供当前数据源的有效 PostgreSQL 密码")
var ErrConnection = errors.New("无法读取 PostgreSQL 目录，请检查连接、账号和 schema 权限")

type Connection struct {
	Host     string
	Port     int
	Database string
	User     string
	Schemas  []string
}

type Manager struct {
	store       *store.Store
	cipher      cipher.AEAD
	allowed     map[string]bool
	localDocker bool
	testSlots   chan struct{}
}

// New configures the managed collector. The key is held outside PostgreSQL.
func New(st *store.Store, keyHex, allowedHosts string, localDocker bool) (*Manager, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return nil, errors.New("SCARLET_MANAGED_PG_KEY 须为 32 字节的十六进制密钥")
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}

	allowed := make(map[string]bool)
	if strings.TrimSpace(allowedHosts) != "" {
		for value := range strings.SplitSeq(allowedHosts, ",") {
			host := canonicalHost(value)
			if !validHost(host) {
				return nil, errors.New("SCARLET_MANAGED_PG_HOSTS 含无效的主机名或地址")
			}
			allowed[host] = true
		}
	}
	return &Manager{store: st, cipher: aead, allowed: allowed, localDocker: localDocker, testSlots: make(chan struct{}, 4)}, nil
}

// AllowedHosts returns the configured host choices for source configuration.
func (m *Manager) AllowedHosts() []string {
	hosts := make([]string, 0, len(m.allowed)+1)
	for host := range m.allowed {
		hosts = append(hosts, host)
	}
	if m.localDocker && !m.allowed[sampleHost] {
		hosts = append(hosts, sampleHost)
	}
	slices.Sort(hosts)
	return hosts
}

func canonicalHost(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func validHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if host == "" || len(host) > 253 {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if ch != '-' && !(ch >= 'a' && ch <= 'z') && !(ch >= '0' && ch <= '9') {
				return false
			}
		}
	}
	return true
}

func validIdentifier(value string) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 63 &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func (m *Manager) dockerSample(c Connection) bool {
	return m.localDocker && canonicalHost(c.Host) == sampleHost && c.Port == samplePort
}

func (m *Manager) Validate(c Connection) error {
	host := canonicalHost(c.Host)
	if !validHost(host) || c.Port < 1 || c.Port > 65535 {
		return errors.New("请提供有效的 PostgreSQL 主机和端口")
	}
	if !m.dockerSample(c) && !m.allowed[host] {
		return errors.New("此 PostgreSQL 主机未加入平台连接允许列表")
	}
	if !validIdentifier(c.Database) || !validIdentifier(c.User) {
		return errors.New("请填写有效的数据库名称和用户")
	}
	if len(c.Schemas) > 64 {
		return errors.New("最多选择 64 个 schema")
	}
	seen := make(map[string]struct{}, len(c.Schemas))
	for _, schema := range c.Schemas {
		if _, duplicate := seen[schema]; duplicate {
			return errors.New("请移除重复的 schema")
		}
		seen[schema] = struct{}{}
	}
	return collector.ValidatePostgreSQLSchemas(c.Schemas)
}

func validPassword(password string) bool {
	return password != "" && len(password) <= 2048 && !strings.ContainsRune(password, 0)
}

func connectionScope(c Connection) []byte {
	scope, _ := json.Marshal([4]string{canonicalHost(c.Host), strconv.Itoa(c.Port), c.Database, c.User})
	return scope
}

func (m *Manager) EncryptPassword(c Connection, password string) ([]byte, error) {
	if err := m.Validate(c); err != nil {
		return nil, err
	}
	if !validPassword(password) {
		return nil, ErrPassword
	}
	return m.cipher.Seal(nil, nil, []byte(password), connectionScope(c)), nil
}

func (m *Manager) decryptPassword(c Connection, ciphertext []byte) (string, error) {
	plaintext, err := m.cipher.Open(nil, nil, ciphertext, connectionScope(c))
	if err != nil {
		return "", ErrPassword
	}
	defer clear(plaintext)
	return string(plaintext), nil
}

func postgresURL(c Connection, password, sslMode string) string {
	u := url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(c.User, password),
		Host:     net.JoinHostPort(canonicalHost(c.Host), strconv.Itoa(c.Port)),
		Path:     "/" + c.Database,
		RawPath:  "/" + url.PathEscape(c.Database),
		RawQuery: "sslmode=" + sslMode,
	}
	return u.String()
}

func (m *Manager) inspect(ctx context.Context, c Connection, password string) ([]collector.PostgreSQLTable, error) {
	if m.dockerSample(c) {
		return collector.InspectPostgreSQLDockerSample(ctx, postgresURL(c, password, "disable"), c.Schemas)
	}
	return collector.InspectPostgreSQL(ctx, postgresURL(c, password, "verify-full"), c.Schemas, false)
}

// Test checks the same read-only catalog operation used by recurring jobs.
func (m *Manager) Test(ctx context.Context, c Connection, password string) (int, error) {
	if err := m.Validate(c); err != nil {
		return 0, err
	}
	return m.test(ctx, c, password)
}

func (m *Manager) test(ctx context.Context, c Connection, password string) (int, error) {
	if !validPassword(password) {
		return 0, ErrPassword
	}
	select {
	case m.testSlots <- struct{}{}:
		defer func() { <-m.testSlots }()
	default:
		return 0, ErrBusy
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	tables, err := m.inspect(ctx, c, password)
	if err != nil {
		return 0, ErrConnection
	}
	return len(tables), nil
}

func (m *Manager) TestSaved(ctx context.Context, c Connection, ciphertext []byte) (int, error) {
	if err := m.Validate(c); err != nil {
		return 0, err
	}
	password, err := m.decryptPassword(c, ciphertext)
	if err != nil {
		return 0, err
	}
	return m.test(ctx, c, password)
}
