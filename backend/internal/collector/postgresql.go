package collector

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const postgresCatalogQuery = `
SELECT pg_catalog.quote_ident(n.nspname) || '.' || pg_catalog.quote_ident(c.relname), c.relname
FROM pg_catalog.pg_class AS c
JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
WHERE n.nspname::text = ANY ($1::text[])
  AND pg_catalog.has_schema_privilege(n.oid, 'USAGE')
  AND c.relkind IN ('r', 'p')
ORDER BY n.nspname, c.relname
LIMIT $2`

func PostgreSQL(ctx context.Context, dsn string, schemas []string, allowInsecureLoopback bool) ([]Asset, error) {
	return collectPostgreSQL(ctx, dsn, schemas, allowInsecureLoopback, false)
}

// PostgreSQLDockerSample permits the explicitly scoped local sample service.
func PostgreSQLDockerSample(ctx context.Context, dsn string, schemas []string) ([]Asset, error) {
	return collectPostgreSQL(ctx, dsn, schemas, false, true)
}

func ValidatePostgreSQLSchemas(schemas []string) error {
	if len(schemas) == 0 || len(schemas) > maxAssets {
		return errors.New("请明确指定要采集的非系统 schema")
	}
	for _, schema := range schemas {
		if !validText(schema, 128) || strings.HasPrefix(schema, "pg_") || schema == "information_schema" {
			return errors.New("采集范围包含无效或系统 schema")
		}
	}
	return nil
}

var errReadPostgreSQLCatalog = errors.New("无法读取 PostgreSQL 表目录，请检查连接、TLS 和 schema 权限")

// ErrPostgreSQLSchemaUnavailable covers missing schemas and schemas without USAGE.
var ErrPostgreSQLSchemaUnavailable = errors.New("至少一个指定的 schema 不存在或当前账号缺少 USAGE 权限")

func collectPostgreSQL(ctx context.Context, dsn string, schemas []string, allowInsecureLoopback, dockerSample bool) ([]Asset, error) {
	assets := make([]Asset, 0)
	err := withPostgreSQLCatalog(ctx, dsn, schemas, allowInsecureLoopback, dockerSample, func(ctx context.Context, tx pgx.Tx, schemas []string) error {
		rows, err := tx.Query(ctx, postgresCatalogQuery, schemas, maxAssets+1)
		if err != nil {
			return errReadPostgreSQLCatalog
		}
		defer rows.Close()
		for rows.Next() {
			if len(assets) == maxAssets {
				return errors.New("PostgreSQL 表超过单批 1000 项上限，请缩小 schema 范围")
			}
			var asset Asset
			if err := rows.Scan(&asset.SourceRef, &asset.Name); err != nil {
				return errReadPostgreSQLCatalog
			}
			assets = append(assets, asset)
		}
		if rows.Err() != nil {
			return errReadPostgreSQLCatalog
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return assets, nil
}

func withPostgreSQLCatalog(ctx context.Context, dsn string, schemas []string, allowInsecureLoopback, dockerSample bool, read func(context.Context, pgx.Tx, []string) error) error {
	if err := ValidatePostgreSQLSchemas(schemas); err != nil {
		return err
	}
	schemas = slices.Clone(schemas)
	slices.Sort(schemas)
	schemas = slices.Compact(schemas)
	config, err := parsePostgresConfig(dsn, allowInsecureLoopback, dockerSample)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return errReadPostgreSQLCatalog
	}
	defer conn.Close(ctx)
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return errReadPostgreSQLCatalog
	}
	defer tx.Rollback(ctx)

	var authorized int
	err = tx.QueryRow(ctx, `
SELECT pg_catalog.count(*) FROM pg_catalog.pg_namespace
WHERE nspname::text = ANY ($1::text[])
  AND pg_catalog.has_schema_privilege(oid, 'USAGE')`, schemas).Scan(&authorized)
	if err != nil {
		return errReadPostgreSQLCatalog
	}
	if authorized != len(schemas) {
		return ErrPostgreSQLSchemaUnavailable
	}

	if err := read(ctx, tx, schemas); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return errReadPostgreSQLCatalog
	}
	return nil
}

func postgresConfig(dsn string, allowInsecureLoopback bool) (*pgx.ConnConfig, error) {
	return parsePostgresConfig(dsn, allowInsecureLoopback, false)
}

func parsePostgresConfig(dsn string, allowInsecureLoopback, dockerSample bool) (*pgx.ConnConfig, error) {
	invalidDSN := errors.New("请提供指向单个 TCP 主机的有效 PostgreSQL URL")
	if os.Getenv("PGSERVICE") != "" {
		return nil, errors.New("采集连接由 SCARLET_PG_DSN 指定，请清除 PGSERVICE")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		u.Hostname() == "" || u.Fragment != "" || strings.ContainsAny(u.Hostname(), ",/\\@ \r\n\t") {
		return nil, invalidDSN
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, invalidDSN
	}
	for key, values := range query {
		switch key {
		case "sslmode", "sslrootcert", "sslcert", "sslkey", "sslpassword", "channel_binding":
			if len(values) == 1 && values[0] != "" {
				continue
			}
		}
		return nil, errors.New("PostgreSQL URL 包含不支持或重复的选项")
	}

	host := u.Hostname()
	loopback := loopbackIP(host)
	mode := query.Get("sslmode")
	if dockerSample && (host != "sample-db" || u.Port() != "5432" || mode != "disable") {
		return nil, errors.New("本地 Docker 样例源仅允许连接 sample-db:5432")
	}
	if mode == "" {
		mode = "verify-full"
		if allowInsecureLoopback && loopback != nil && !query.Has("sslrootcert") && !query.Has("sslcert") {
			mode = "disable"
		}
	}
	if mode == "disable" && allowInsecureLoopback && loopback != nil {
		host = loopback.String()
		port := u.Port()
		if port == "" {
			port = "5432"
		}
		u.Host = net.JoinHostPort(host, port)
	} else if mode != "verify-full" && !dockerSample {
		return nil, errors.New("PostgreSQL 连接要求 sslmode=verify-full")
	}
	query.Set("sslmode", mode)
	query.Set("connect_timeout", "5")
	u.RawQuery = query.Encode()
	config, err := pgx.ParseConfig(u.String())
	if err != nil || config.Host != host || len(config.Fallbacks) != 0 {
		return nil, invalidDSN
	}
	if mode == "verify-full" && (config.TLSConfig == nil || config.TLSConfig.InsecureSkipVerify ||
		config.TLSConfig.ServerName != host) {
		return nil, errors.New("PostgreSQL TLS 主机验证配置无效")
	}
	config.RuntimeParams = map[string]string{
		"application_name":      "scarlet-collector",
		"search_path":           "pg_catalog",
		"statement_timeout":     "5000",
		"quote_all_identifiers": "off",
	}
	return config, nil
}
