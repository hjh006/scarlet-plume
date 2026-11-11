package collector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type deepMetadataColumn struct {
	Name            string `json:"name"`
	DataType        string `json:"dataType"`
	DataTypeDisplay string `json:"dataTypeDisplay"`
	Description     string `json:"description"`
	Constraint      string `json:"constraint"`
	Tags            []struct {
		TagFQN string `json:"tagFQN"`
	} `json:"tags"`
	Children []deepMetadataColumn `json:"children"`
}
type deepMetadataTable struct {
	ServiceType      string               `json:"serviceType"`
	ID               string               `json:"id"`
	Name             string               `json:"name"`
	Description      string               `json:"description"`
	Service          metadataReference    `json:"service"`
	Database         metadataReference    `json:"database"`
	DatabaseSchema   metadataReference    `json:"databaseSchema"`
	Columns          []deepMetadataColumn `json:"columns"`
	TableConstraints []struct {
		Type    string   `json:"constraintType"`
		Columns []string `json:"columns"`
	} `json:"tableConstraints"`
}

func discoverOpenMetadata(ctx context.Context, c DiscoveryConfig, endpoint, token string, insecure, localDocker bool) (DiscoveryResult, error) {
	base, e := metadataURL(endpoint, insecure)
	if localDocker {
		if err := ValidateDiscoveryConnection(c, DiscoveryConnection{DSN: endpoint, LocalDocker: true}); err != nil {
			return DiscoveryResult{}, err
		}
		base, e = url.URL{Scheme: "http", Host: "openmetadata:8585", Path: "/api"}, nil
	}
	if e != nil {
		return DiscoveryResult{}, e
	}
	if !validText(c.Service, 256) || token == "" || len(token) > 8192 || strings.ContainsAny(token, "\r\n") {
		return DiscoveryResult{}, errors.New("请指定OpenMetadata服务与有效令牌")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string, q url.Values, out any) error {
		u := base
		u.Path += "/v1/" + path
		u.RawQuery = q.Encode()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return errors.New("无法连接OpenMetadata")
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return errors.New("OpenMetadata拒绝请求，请核对服务与权限")
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
		if err != nil || len(b) > 4*1024*1024 || json.Unmarshal(b, out) != nil {
			return errors.New("OpenMetadata深元数据响应无效或超限")
		}
		return nil
	}
	r := newDiscoveryResult()
	r.Coverage.Content = "not_collected"
	r.Coverage.Metadata = "openmetadata_declared_columns_comments_tags_constraints"
	r.Coverage.Lineage = "per_table_api"
	r.Coverage.Warnings = append(r.Coverage.Warnings, "source_database_acl_unknown_openmetadata_ownership_is_not_acl", "metadata_tags_are_candidates_not_confirmed_classification")
	cursor := ""
	seen := map[string]bool{}
	for pageNo := 0; pageNo < 20; pageNo++ {
		var page struct {
			Data   []deepMetadataTable `json:"data"`
			Paging struct {
				After string `json:"after"`
				Total int    `json:"total"`
			} `json:"paging"`
		}
		q := url.Values{"database": {fqnSegment(c.Service) + "." + fqnSegment(c.Database)}, "fields": {"columns,tags,tableConstraints"}, "limit": {"100"}, "include": {"non-deleted"}}
		if cursor != "" {
			q.Set("after", cursor)
		}
		if e = get("tables", q, &page); e != nil {
			return r, e
		}
		if page.Data == nil {
			return r, errors.New("OpenMetadata缺少data字段")
		}
		for _, row := range page.Data {
			system := strings.ToLower(row.ServiceType)
			if system == "postgres" {
				system = "postgresql"
			}
			if system == "azuresql" {
				system = "mssql"
			}
			if system == "" || system != c.System {
				return r, errors.New("OpenMetadata服务类型缺失或与任务声明的数据库类型不一致")
			}
			if row.Service.FQN != c.Service && row.Service.Name != c.Service {
				return r, errors.New("OpenMetadata返回跨服务表")
			}
			if row.Database.Name != c.Database && row.Database.FQN != fqnSegment(c.Service)+"."+fqnSegment(c.Database) {
				return r, errors.New("OpenMetadata返回跨数据库表")
			}
			schema := row.DatabaseSchema.Name
			if !uuidPattern.MatchString(row.ID) || !validText(schema, 128) {
				return r, errors.New("OpenMetadata表身份或schema无效")
			}
			if len(c.Scopes) > 0 {
				allowed := false
				for _, s := range c.Scopes {
					if schema == s {
						allowed = true
					}
				}
				if !allowed {
					continue
				}
			}
			if len(r.Tables) == 100 {
				return r, errors.New("OpenMetadata超过100表，请缩小schema范围")
			}
			t := blankDiscoveryTable(c.Database, schema, row.Name)
			t.Comment = row.Description
			if len(t.Comment) > 2048 {
				t.Comment = t.Comment[:2048]
				t.Warnings = append(t.Warnings, "comment_truncated")
			}
			metadataFlattenColumns("", row.Columns, &t, c.Rules)
			for _, constraint := range row.TableConstraints {
				t.Constraints = append(t.Constraints, constraint.Type+":"+strings.Join(constraint.Columns, ","))
			}
			var lineage struct {
				Upstream []struct {
					From string `json:"fromEntity"`
					To   string `json:"toEntity"`
				} `json:"upstreamEdges"`
				Downstream []struct {
					From string `json:"fromEntity"`
					To   string `json:"toEntity"`
				} `json:"downstreamEdges"`
			}
			if le := get("lineage/table/"+row.ID, url.Values{"upstreamDepth": {"1"}, "downstreamDepth": {"1"}}, &lineage); le == nil {
				for _, edge := range lineage.Upstream {
					t.Lineage = append(t.Lineage, edge.From+"->"+edge.To)
				}
				for _, edge := range lineage.Downstream {
					t.Lineage = append(t.Lineage, edge.From+"->"+edge.To)
				}
				if len(t.Lineage) > 100 {
					t.Lineage = t.Lineage[:100]
					t.Warnings = append(t.Warnings, "lineage_truncated")
				}
			} else {
				t.Warnings = append(t.Warnings, "lineage_unknown")
			}
			t.Warnings = append(t.Warnings, "source_acl_unknown", "content_not_collected")
			r.Tables = append(r.Tables, t)
		}
		if page.Paging.After == "" {
			return r, nil
		}
		if seen[page.Paging.After] || len(page.Data) == 0 {
			return r, errors.New("OpenMetadata分页游标重复或不完整")
		}
		cursor = page.Paging.After
		seen[cursor] = true
	}
	return r, errors.New("OpenMetadata分页超过20页预算")
}
func metadataFlattenColumns(prefix string, columns []deepMetadataColumn, t *DiscoveryTable, rules []DiscoveryRule) {
	for _, col := range columns {
		if len(t.Fields) > 1000 {
			return
		}
		name := col.Name
		if prefix != "" {
			name = prefix + "." + name
		}
		typ := col.DataTypeDisplay
		if typ == "" {
			typ = col.DataType
		}
		f := DiscoveryField{Name: name, Type: typ, Comment: col.Description, Nullable: col.Constraint != "NOT_NULL" && col.Constraint != "PRIMARY_KEY"}
		if len(f.Comment) > 2048 {
			f.Comment = f.Comment[:2048]
		}
		for _, tag := range col.Tags {
			if f.Sensitivity == "" {
				f.Sensitivity = tag.TagFQN
			}
		}
		discoveryFieldHits(&f, rules, nil)
		t.Fields = append(t.Fields, f)
		if len(col.Children) > 0 && strings.Count(name, ".") < 8 {
			metadataFlattenColumns(name, col.Children, t, rules)
		}
	}
}
