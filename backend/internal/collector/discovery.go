package collector

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// DiscoveryRule is evaluated in the customer collector. No sampled values leave it.
type DiscoveryRule struct {
	compiled   *regexp.Regexp
	exact      map[string]struct{}
	Key        string   `json:"key"`
	Version    int      `json:"version"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Target     string   `json:"target"`
	Category   string   `json:"category"`
	Expression string   `json:"expression"`
	Dictionary []string `json:"dictionary"`
	Status     string   `json:"status,omitempty"`
}
type DiscoveryHit struct {
	RuleKey      string `json:"ruleKey"`
	RuleVersion  int    `json:"ruleVersion"`
	Category     string `json:"category"`
	Method       string `json:"method"`
	MatchCount   int64  `json:"matchCount"`
	ScannedCount int64  `json:"scannedCount"`
}
type DiscoveryField struct {
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	Comment     string         `json:"comment"`
	Nullable    bool           `json:"nullable"`
	Sensitivity string         `json:"sensitivity,omitempty"`
	Hits        []DiscoveryHit `json:"hits"`
}
type DiscoveryTable struct {
	Database          string           `json:"database"`
	Schema            string           `json:"schema"`
	Name              string           `json:"name"`
	Comment           string           `json:"comment"`
	Fields            []DiscoveryField `json:"fields"`
	PermissionsStatus string           `json:"permissionsStatus"`
	Permissions       []string         `json:"permissions"`
	Constraints       []string         `json:"constraints"`
	Lineage           []string         `json:"lineage"`
	Warnings          []string         `json:"warnings"`
	ScannedRows       int64            `json:"scannedRows"`
}
type DiscoveryCoverage struct {
	Metadata    string   `json:"metadata"`
	Content     string   `json:"content"`
	Permissions string   `json:"permissions"`
	Lineage     string   `json:"lineage"`
	RowLimit    int      `json:"rowLimit"`
	TableLimit  int      `json:"tableLimit"`
	Warnings    []string `json:"warnings"`
}
type DiscoveryResult struct {
	Tables   []DiscoveryTable  `json:"tables"`
	Coverage DiscoveryCoverage `json:"coverage"`
}
type DiscoveryConfig struct {
	Provider string          `json:"provider"`
	System   string          `json:"system"`
	Database string          `json:"database"`
	Scopes   []string        `json:"scopes"`
	Service  string          `json:"service"`
	Rules    []DiscoveryRule `json:"rules"`
}

func BuiltinDiscoveryRules() []DiscoveryRule {
	return []DiscoveryRule{
		{Key: "builtin.email", Version: 1, Name: "邮箱格式", Kind: "regexp", Target: "content", Category: "email", Expression: `^[A-Za-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[A-Za-z0-9][A-Za-z0-9.-]*\.[A-Za-z]{2,63}$`},
		{Key: "builtin.phone", Version: 1, Name: "中国大陆手机号格式", Kind: "regexp", Target: "content", Category: "phone", Expression: `^(\+86[- ]?)?1[3-9][0-9]{9}$`},
		{Key: "builtin.metadata.personal", Version: 1, Name: "个人信息字段名候选", Kind: "keyword", Target: "metadata", Category: "personal_information", Expression: "email,phone,mobile,身份证,手机号,邮箱,姓名"},
	}
}
func ValidateDiscoveryRule(rule DiscoveryRule) error {
	if !validText(rule.Key, 80) || !validText(rule.Name, 160) || !validText(rule.Category, 80) || rule.Version < 1 || len(rule.Expression) > 2048 || len(rule.Dictionary) > 1000 {
		return errors.New("识别规则标识、名称、版本或长度无效")
	}
	if rule.Target != "metadata" && rule.Target != "content" {
		return errors.New("识别范围必须是metadata或content")
	}
	switch rule.Kind {
	case "regexp":
		if _, e := regexp.Compile(rule.Expression); e != nil || rule.Expression == "" {
			return errors.New("请输入有效的RE2正则表达式")
		}
	case "keyword":
		if strings.TrimSpace(rule.Expression) == "" {
			return errors.New("关键词不能为空")
		}
	case "dictionary":
		if len(rule.Dictionary) == 0 {
			return errors.New("词典不能为空")
		}
		for _, v := range rule.Dictionary {
			if !validText(v, 256) {
				return errors.New("词典项无效")
			}
		}
	default:
		return errors.New("规则类型无效")
	}
	return nil
}
func discoveryMatch(rule DiscoveryRule, value string) bool {
	switch rule.Kind {
	case "regexp":
		if rule.compiled != nil {
			return rule.compiled.MatchString(value)
		}
		r, e := regexp.Compile(rule.Expression)
		return e == nil && r.MatchString(value)
	case "keyword":
		for _, v := range strings.Split(rule.Expression, ",") {
			if v = strings.TrimSpace(v); v != "" && strings.Contains(strings.ToLower(value), strings.ToLower(v)) {
				return true
			}
		}
	case "dictionary":
		if rule.exact != nil {
			_, ok := rule.exact[value]
			return ok
		}
		for _, v := range rule.Dictionary {
			if value == v {
				return true
			}
		}
	}
	return false
}
func discoveryFieldHits(f *DiscoveryField, rules []DiscoveryRule, values []string) {
	if f.Hits == nil {
		f.Hits = []DiscoveryHit{}
	}
	for _, rule := range rules {
		var count, scanned int64
		method := "metadata"
		if rule.Target == "metadata" {
			scanned = 1
			if discoveryMatch(rule, f.Name+" "+f.Comment+" "+f.Type) {
				count = 1
			}
		} else {
			method = "bounded_customer_sample"
			for _, v := range values {
				if len(v) > 256 {
					continue
				}
				scanned++
				if discoveryMatch(rule, v) {
					count++
				}
			}
		}
		if count > 0 {
			f.Hits = append(f.Hits, DiscoveryHit{RuleKey: rule.Key, RuleVersion: rule.Version, Category: rule.Category, Method: method, MatchCount: count, ScannedCount: scanned})
		}
	}
	if f.Sensitivity == "" {
		f.Sensitivity = PostgreSQLMetadataSensitivity(f.Name, f.Type, f.Comment)
	}
}
func validDiscoveryConfig(c DiscoveryConfig) error {
	if !validText(c.Database, 128) || len(c.Scopes) > 100 {
		return errors.New("请指定数据库与不超过100项范围")
	}
	for _, s := range c.Scopes {
		if !validText(s, 128) {
			return errors.New("采集范围无效")
		}
	}
	for _, r := range c.Rules {
		if e := ValidateDiscoveryRule(r); e != nil {
			return e
		}
	}
	return nil
}
func Discover(ctx context.Context, c DiscoveryConfig, dsn, token string, insecure bool) (DiscoveryResult, error) {
	return DiscoverWithConnection(ctx, c, DiscoveryConnection{DSN: dsn, Token: token, AllowInsecureLoopback: insecure})
}

func DiscoverWithConnection(ctx context.Context, c DiscoveryConfig, connection DiscoveryConnection) (DiscoveryResult, error) {
	if err := ValidateDiscoveryConnection(c, connection); err != nil {
		return DiscoveryResult{}, err
	}
	dsn, token, insecure := connection.DSN, connection.Token, connection.AllowInsecureLoopback
	if e := validDiscoveryConfig(c); e != nil {
		return DiscoveryResult{}, e
	}
	if len(c.Rules) == 0 {
		c.Rules = BuiltinDiscoveryRules()
	}
	for i := range c.Rules {
		if c.Rules[i].Kind == "regexp" {
			c.Rules[i].compiled, _ = regexp.Compile(c.Rules[i].Expression)
		}
		if c.Rules[i].Kind == "dictionary" {
			c.Rules[i].exact = map[string]struct{}{}
			for _, v := range c.Rules[i].Dictionary {
				c.Rules[i].exact[v] = struct{}{}
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	switch c.Provider {
	case "postgresql":
		return discoverPostgres(ctx, c, dsn, insecure, connection.LocalDocker)
	case "mysql", "mariadb":
		return discoverMySQL(ctx, c, dsn, insecure, connection.LocalDocker)
	case "mongodb":
		return discoverMongo(ctx, c, dsn, insecure, connection.LocalDocker)
	case "openmetadata":
		return discoverOpenMetadata(ctx, c, dsn, token, insecure, connection.LocalDocker)
	}
	return DiscoveryResult{}, errors.New("采集提供方无效")
}
func newDiscoveryResult() DiscoveryResult {
	return DiscoveryResult{Tables: []DiscoveryTable{}, Coverage: DiscoveryCoverage{Metadata: "observed", Content: "bounded_sample", Permissions: "unknown", Lineage: "unknown", RowLimit: 200, TableLimit: 100, Warnings: []string{"sample_is_not_full_database", "no_raw_values_uploaded"}}}
}
func blankDiscoveryTable(db, schema, name string) DiscoveryTable {
	return DiscoveryTable{Database: db, Schema: schema, Name: name, Fields: []DiscoveryField{}, PermissionsStatus: "unknown", Permissions: []string{}, Constraints: []string{}, Lineage: []string{}, Warnings: []string{}}
}
func discoverPostgres(ctx context.Context, c DiscoveryConfig, dsn string, insecure, localDocker bool) (DiscoveryResult, error) {
	u, parseErr := url.Parse(dsn)
	if parseErr != nil || strings.TrimPrefix(u.Path, "/") != c.Database {
		return DiscoveryResult{}, errors.New("PostgreSQL连接数据库与任务范围不一致")
	}
	var rows []PostgreSQLTable
	var e error
	if localDocker {
		rows, e = InspectPostgreSQLDockerSample(ctx, dsn, c.Scopes)
	} else {
		rows, e = InspectPostgreSQL(ctx, dsn, c.Scopes, insecure)
	}
	if e != nil {
		return DiscoveryResult{}, e
	}
	r := newDiscoveryResult()
	r.Coverage.Content = "source_aggregate_builtin_only"
	r.Coverage.Permissions = "observed"
	r.Coverage.Lineage = "unknown"
	r.Coverage.Warnings = append(r.Coverage.Warnings, "postgres_custom_rules_metadata_only_use_existing_postgres_content_aggregates")
	for _, t := range rows {
		d := blankDiscoveryTable(c.Database, t.Schema, t.Name)
		d.Comment = t.Comment
		d.PermissionsStatus = "observed"
		for _, g := range t.Grants {
			d.Permissions = append(d.Permissions, g.Grantee+":"+g.Privilege)
		}
		for _, con := range t.Constraints {
			d.Constraints = append(d.Constraints, con.Name+":"+con.Type+":"+strings.Join(con.Columns, ","))
		}
		for _, f := range t.Columns {
			field := DiscoveryField{Name: f.Name, Type: f.Type, Comment: f.Comment, Nullable: f.Nullable, Sensitivity: f.Sensitivity}
			discoveryFieldHits(&field, c.Rules, nil)
			if t.ContentAnalysis != nil {
				d.ScannedRows = t.ContentAnalysis.RowsScanned
				for _, cs := range t.ContentAnalysis.Columns {
					if cs.Column == f.Name {
						for _, hit := range cs.Matches {
							if hit.Count > 0 {
								field.Hits = append(field.Hits, DiscoveryHit{RuleKey: "builtin." + hit.Category, RuleVersion: 1, Category: hit.Category, Method: "source_aggregate", MatchCount: hit.Count, ScannedCount: t.ContentAnalysis.RowsScanned})
							}
						}
					}
				}
			}
			d.Fields = append(d.Fields, field)
		}
		d.Warnings = append(d.Warnings, t.Warnings...)
		if t.ContentAnalysis != nil {
			d.Warnings = append(d.Warnings, t.ContentAnalysis.Warnings...)
		}
		r.Tables = append(r.Tables, d)
	}
	if err := withPostgreSQLCatalog(ctx, dsn, c.Scopes, insecure, localDocker, func(ctx context.Context, tx pgx.Tx, _ []string) error {
		for i := range r.Tables {
			if i >= 10 {
				break
			}
			t := &r.Tables[i]
			n := min(len(t.Fields), 16)
			if n == 0 {
				continue
			}
			cols := make([]string, n)
			for j := range n {
				cols[j] = "pg_catalog.left(" + pgx.Identifier{t.Fields[j].Name}.Sanitize() + "::text,257)"
			}
			values := make([][]string, n)
			err := pgx.BeginFunc(ctx, tx, func(sample pgx.Tx) error {
				rows, e := sample.Query(ctx, "SELECT "+strings.Join(cols, ",")+" FROM "+pgx.Identifier{t.Schema, t.Name}.Sanitize()+" LIMIT 200")
				if e != nil {
					return e
				}
				defer rows.Close()
				for rows.Next() {
					data := make([]*string, n)
					dest := make([]any, n)
					for j := range n {
						dest[j] = &data[j]
					}
					if e = rows.Scan(dest...); e != nil {
						return e
					}
					for j := range n {
						if data[j] != nil {
							values[j] = append(values[j], *data[j])
						}
					}
				}
				return rows.Err()
			})
			if err != nil {
				t.Warnings = append(t.Warnings, "custom_content_sample_unavailable")
				continue
			}
			for j := range n {
				for _, rule := range c.Rules {
					if rule.Target != "content" || strings.HasPrefix(rule.Key, "builtin.") {
						continue
					}
					var count int64
					for _, v := range values[j] {
						if len(v) <= 256 && discoveryMatch(rule, v) {
							count++
						}
					}
					if count > 0 {
						t.Fields[j].Hits = append(t.Fields[j].Hits, DiscoveryHit{RuleKey: rule.Key, RuleVersion: rule.Version, Category: rule.Category, Method: "bounded_customer_sample", MatchCount: count, ScannedCount: int64(len(values[j]))})
					}
				}
			}
		}
		return nil
	}); err != nil {
		r.Coverage.Warnings = append(r.Coverage.Warnings, "custom_content_unavailable")
	}
	r.Coverage.Content = "source_aggregate_and_bounded_customer_custom_rules"
	r.Coverage.Warnings = slices.DeleteFunc(r.Coverage.Warnings, func(s string) bool {
		return s == "postgres_custom_rules_metadata_only_use_existing_postgres_content_aggregates"
	})
	return r, nil
}

// ValidateDiscoveryResult guards the agent boundary, including exact scopes and bounded metadata.
func ValidateDiscoveryResult(c DiscoveryConfig, r DiscoveryResult) error {
	if len(r.Tables) > 100 || len(r.Coverage.Warnings) > 100 {
		return errors.New("采集范围超过上限")
	}
	seen := map[string]bool{}
	count := 0
	for _, t := range r.Tables {
		key := t.Database + "\x00" + t.Schema + "\x00" + t.Name
		if t.Database != c.Database || !validText(t.Name, 128) || len(t.Schema) > 128 || len(t.Comment) > 2048 || seen[key] || t.ScannedRows < 0 || t.ScannedRows > 200 || len(t.Fields) > 1000 || len(t.Permissions) > 500 || len(t.Lineage) > 100 {
			return errors.New("采集表范围或元数据无效")
		}
		seen[key] = true
		scope := t.Schema
		if c.Provider == "mongodb" {
			scope = t.Name
		}
		if len(c.Scopes) > 0 && !slices.Contains(c.Scopes, scope) {
			return errors.New("采集结果超出授权范围")
		}
		seenFields := map[string]bool{}
		for _, f := range t.Fields {
			if seenFields[f.Name] {
				return errors.New("重复的字段路径")
			}
			seenFields[f.Name] = true
			count++
			if !validText(f.Name, 256) || len(f.Type) > 256 || len(f.Comment) > 2048 || !utf8.ValidString(f.Comment) || len(f.Sensitivity) > 160 || len(f.Hits) > 103 {
				return errors.New("字段元数据无效")
			}
			for _, h := range f.Hits {
				if h.MatchCount <= 0 || h.ScannedCount < 0 || h.MatchCount > max(h.ScannedCount, 1) || h.ScannedCount > 200 || !validText(h.Category, 80) || !validText(h.RuleKey, 80) || h.RuleVersion < 1 || (h.Method != "metadata" && h.Method != "bounded_customer_sample" && h.Method != "source_aggregate") {
					return fmt.Errorf("规则统计无效")
				}
			}
		}
	}
	if count > 10000 {
		return errors.New("字段数量超过10000项")
	}
	return nil
}
