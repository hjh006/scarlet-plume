package collector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var uuidPattern = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

type metadataReference struct {
	Name string `json:"name"`
	FQN  string `json:"fullyQualifiedName"`
}

type metadataEntity struct {
	metadataReference
	ID       string             `json:"id"`
	Service  *metadataReference `json:"service"`
	Database *metadataReference `json:"database"`
}

type metadataPage struct {
	Data   []metadataEntity `json:"data"`
	Paging *struct {
		After string `json:"after"`
		Total *int   `json:"total"`
	} `json:"paging"`
}

type metadataClient struct {
	apiURL url.URL
	token  string
	http   *http.Client
}

func OpenMetadata(ctx context.Context, baseURL, token, service string, allowInsecureLoopback bool) ([]Asset, error) {
	if !validText(service, 256) {
		return nil, errors.New("请指定一个有效的 OpenMetadata 数据库服务")
	}
	if len(token) == 0 || len(token) > 8192 || strings.IndexFunc(token, func(r rune) bool {
		return r < 33 || r > 126
	}) != -1 {
		return nil, errors.New("请提供有效的 OpenMetadata 令牌")
	}
	apiURL, err := metadataURL(baseURL, allowInsecureLoopback)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := metadataClient{apiURL: apiURL, token: token, http: &http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	serviceFQN := fqnSegment(service)
	databaseRows, err := client.list(ctx, "databases", "service", serviceFQN, maxAssets)
	if err != nil {
		return nil, err
	}
	databases := make([]string, 0, len(databaseRows))
	seenDatabases := make(map[string]bool)
	for _, row := range databaseRows {
		if !serviceMatches(row.Service, serviceFQN) || !validText(row.FQN, 500) ||
			!strings.HasPrefix(row.FQN, serviceFQN+".") {
			return nil, errors.New("OpenMetadata 返回了指定服务范围外的数据库")
		}
		if seenDatabases[row.FQN] {
			return nil, errors.New("OpenMetadata 返回了重复的数据库")
		}
		seenDatabases[row.FQN] = true
		databases = append(databases, row.FQN)
	}
	slices.Sort(databases)

	assets := make([]Asset, 0)
	seenTables := make(map[string]bool)
	for _, database := range databases {
		tables, err := client.list(ctx, "tables", "database", database, maxAssets-len(assets))
		if err != nil {
			return nil, err
		}
		for _, row := range tables {
			if !serviceMatches(row.Service, serviceFQN) || !tableInDatabase(row, database) {
				return nil, errors.New("OpenMetadata 返回了指定数据库范围外的表")
			}
			name := row.FQN
			if name == "" {
				name = row.Name
			}
			if !uuidPattern.MatchString(row.ID) || !validText(name, 300) {
				return nil, errors.New("OpenMetadata 返回的表标识或名称无效")
			}
			id := strings.ToLower(row.ID)
			if seenTables[id] {
				return nil, errors.New("OpenMetadata 返回了重复的表标识")
			}
			seenTables[id] = true
			assets = append(assets, Asset{SourceRef: id, Name: name})
		}
	}
	return assets, nil
}

func metadataURL(baseURL string, allowInsecureLoopback bool) (url.URL, error) {
	invalidURL := errors.New("请提供不含凭据、查询参数或片段的 OpenMetadata 地址")
	u, err := url.Parse(baseURL)
	if err != nil || !validText(baseURL, 2048) || strings.IndexFunc(baseURL, unicode.IsSpace) != -1 ||
		strings.Contains(baseURL, "\\") || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || strings.Contains(baseURL, "#") || u.RawPath != "" {
		return url.URL{}, invalidURL
	}
	if u.Scheme != "https" {
		ip := loopbackIP(u.Hostname())
		if u.Scheme != "http" || !allowInsecureLoopback || ip == nil {
			return url.URL{}, errors.New("OpenMetadata 连接要求 HTTPS")
		}
		port := u.Port()
		if port == "" {
			port = "80"
		}
		u.Host = net.JoinHostPort(ip.String(), port)
	}
	prefix := strings.TrimRight(u.Path, "/")
	if prefix != "" && path.Clean(prefix) != prefix {
		return url.URL{}, invalidURL
	}
	if !strings.HasSuffix(prefix, "/api") {
		prefix += "/api"
	}
	u.Path = prefix
	return *u, nil
}

func (c metadataClient) list(ctx context.Context, resource, key, scope string, cap int) ([]metadataEntity, error) {
	endpoint := c.apiURL
	endpoint.Path += "/v1/" + resource
	params := url.Values{key: {scope}, "include": {"non-deleted"}}
	seenCursors := make(map[string]bool)
	var items []metadataEntity
	for {
		limit := min(25, cap-len(items)+1)
		params.Set("limit", strconv.Itoa(limit))
		endpoint.RawQuery = params.Encode()
		page, err := c.getPage(ctx, endpoint.String())
		if err != nil {
			return nil, err
		}
		if len(page.Data) > limit {
			return nil, errors.New("OpenMetadata 返回的目录数量无效")
		}
		items = append(items, page.Data...)
		total := page.Paging.Total
		cursor := page.Paging.After
		if len(items) > cap || total != nil && *total > cap || len(items) == cap && cursor != "" {
			return nil, errors.New("OpenMetadata 目录超过单批 1000 项上限，请缩小采集范围")
		}
		if total != nil && *total < len(items) {
			return nil, errors.New("OpenMetadata 返回的目录数量无效")
		}
		if cursor == "" {
			if total != nil && *total != len(items) {
				return nil, errors.New("OpenMetadata 分页结果不完整")
			}
			return items, nil
		}
		if len(page.Data) == 0 || !validText(cursor, 4096) || seenCursors[cursor] {
			return nil, errors.New("OpenMetadata 返回的分页游标无效")
		}
		seenCursors[cursor] = true
		params.Set("after", cursor)
	}
}

func (c metadataClient) getPage(ctx context.Context, endpoint string) (metadataPage, error) {
	var page metadataPage
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return page, errors.New("无法创建 OpenMetadata 请求")
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return page, errors.New("无法读取 OpenMetadata 目录，请检查连接和 TLS")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return page, errors.New("OpenMetadata 拒绝了目录请求，请检查地址、服务权限及令牌")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return page, errors.New("无法读取 OpenMetadata 响应")
	}
	if len(body) > maxBytes {
		return page, errors.New("OpenMetadata 响应超过 512 KiB")
	}
	if !utf8.Valid(body) || json.Unmarshal(body, &page) != nil || page.Data == nil || page.Paging == nil {
		return page, errors.New("OpenMetadata 返回的目录格式无效")
	}
	return page, nil
}

func fqnSegment(name string) string {
	if strings.ContainsAny(name, ".\"") {
		return "\"" + strings.ReplaceAll(name, "\"", "\"\"") + "\""
	}
	return name
}

func serviceMatches(ref *metadataReference, fqn string) bool {
	return ref == nil || (ref.Name == "" || fqnSegment(ref.Name) == fqn) && (ref.FQN == "" || ref.FQN == fqn)
}

func tableInDatabase(table metadataEntity, database string) bool {
	parentFQN := ""
	if table.Database != nil {
		parentFQN = table.Database.FQN
	}
	return (parentFQN != "" || table.FQN != "") && (parentFQN == "" || parentFQN == database) &&
		(table.FQN == "" || strings.HasPrefix(table.FQN, database+"."))
}
