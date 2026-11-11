package collector

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

func discoverMySQL(ctx context.Context, c DiscoveryConfig, dsn string, insecure, localDocker bool) (DiscoveryResult, error) {
	cfg, e := mysql.ParseDSN(dsn)
	if e != nil || cfg.Net != "tcp" || cfg.DBName != c.Database || cfg.MultiStatements || cfg.AllowAllFiles {
		return DiscoveryResult{}, errors.New("MySQL连接要求单TCP主机、指定数据库且禁止multiStatements/local files")
	}
	host, port, e := net.SplitHostPort(cfg.Addr)
	if e != nil || host == "" || port == "" {
		return DiscoveryResult{}, errors.New("MySQL主机无效")
	}
	if localDocker {
		cfg.TLSConfig = "false"
		cfg.TLS = nil
	} else if insecure && loopbackIP(host) != nil {
		cfg.TLSConfig = "false"
		cfg.TLS = nil
		cfg.Addr = net.JoinHostPort(loopbackIP(host).String(), port)
	} else {
		cfg.TLSConfig = ""
		cfg.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	}
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	cfg.WriteTimeout = 5 * time.Second
	cfg.InterpolateParams = false
	cfg.ParseTime = false
	cfg.Params = nil
	connector, e := mysql.NewConnector(cfg)
	if e != nil {
		return DiscoveryResult{}, errors.New("MySQL连接配置无效")
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(1)
	if e = db.PingContext(ctx); e != nil {
		return DiscoveryResult{}, errors.New("无法连接MySQL，请核对TLS、账号与权限")
	}
	tx, e := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if e != nil {
		return DiscoveryResult{}, errors.New("无法启动MySQL只读采集")
	}
	defer tx.Rollback()
	r := newDiscoveryResult()
	r.Coverage.Permissions = "current_account_visible_grants"
	r.Coverage.Warnings = append(r.Coverage.Warnings, "mysql_grants_only_current_account_not_effective_all_principals")
	rows, e := tx.QueryContext(ctx, `SELECT TABLE_NAME,COALESCE(TABLE_COMMENT,'') FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_TYPE='BASE TABLE' ORDER BY TABLE_NAME LIMIT 101`, c.Database)
	if e != nil {
		return r, errors.New("无法读取MySQL表目录")
	}
	for rows.Next() {
		d := blankDiscoveryTable(c.Database, c.Database, "")
		if e = rows.Scan(&d.Name, &d.Comment); e != nil {
			rows.Close()
			return r, e
		}
		if len(r.Tables) == 100 {
			rows.Close()
			return r, errors.New("MySQL超过100表，请缩小数据库范围")
		}
		r.Tables = append(r.Tables, d)
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return r, e
	}
	rows.Close()
	// SHOW GRANTS describes only this authenticated account; it is never presented as a full ACL inventory.
	var grants []string
	if gs, ge := tx.QueryContext(ctx, "SHOW GRANTS FOR CURRENT_USER()"); ge == nil {
		for gs.Next() {
			var grant string
			if gs.Scan(&grant) == nil && len(grants) < 500 {
				if p := strings.Index(strings.ToUpper(grant), " IDENTIFIED "); p >= 0 {
					grant = grant[:p] + " [authentication details omitted]"
				}
				grants = append(grants, grant)
			}
		}
		gs.Close()
	}
	for i := range r.Tables {
		t := &r.Tables[i]
		if len(grants) > 0 {
			t.PermissionsStatus = "current_account_visible_grants"
			t.Permissions = grants
		}
		t.Warnings = append(t.Warnings, "lineage_unknown", "grants_only_current_account")
		cols, ce := tx.QueryContext(ctx, `SELECT COLUMN_NAME,COLUMN_TYPE,COALESCE(COLUMN_COMMENT,''),IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY ORDINAL_POSITION LIMIT 1001`, c.Database, t.Name)
		if ce != nil {
			return r, errors.New("无法读取MySQL字段目录")
		}
		for cols.Next() {
			var f DiscoveryField
			var nullable string
			if e = cols.Scan(&f.Name, &f.Type, &f.Comment, &nullable); e != nil {
				cols.Close()
				return r, e
			}
			if len(t.Fields) == 1000 {
				cols.Close()
				return r, errors.New("MySQL单表超过1000字段")
			}
			f.Nullable = nullable == "YES"
			t.Fields = append(t.Fields, f)
		}
		e = cols.Err()
		cols.Close()
		if e != nil {
			return r, e
		}
		constraints, ce := tx.QueryContext(ctx, `SELECT CONSTRAINT_NAME,CONSTRAINT_TYPE FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY CONSTRAINT_NAME LIMIT 501`, c.Database, t.Name)
		if ce == nil {
			for constraints.Next() {
				var name, kind string
				if constraints.Scan(&name, &kind) == nil {
					t.Constraints = append(t.Constraints, name+":"+kind)
				}
			}
			constraints.Close()
		}
		values := make([][]string, len(t.Fields))
		if i < 10 && len(t.Fields) > 0 {
			selected := min(len(t.Fields), 16)
			names := make([]string, selected)
			for j := range selected {
				names[j] = "LEFT(CAST(" + mysqlIdentifier(t.Fields[j].Name) + " AS CHAR),257)"
			}
			query := "SELECT " + strings.Join(names, ",") + " FROM " + mysqlIdentifier(c.Database) + "." + mysqlIdentifier(t.Name) + " LIMIT 200"
			content, ce := tx.QueryContext(ctx, query)
			if ce == nil {
				for content.Next() {
					dest := make([]any, selected)
					data := make([]sql.NullString, selected)
					for j := range selected {
						dest[j] = &data[j]
					}
					if content.Scan(dest...) != nil {
						break
					}
					t.ScannedRows++
					for j := range selected {
						if data[j].Valid {
							values[j] = append(values[j], data[j].String)
						}
					}
				}
				if content.Err() != nil {
					t.Warnings = append(t.Warnings, "sample_incomplete")
				}
				content.Close()
				t.Warnings = append(t.Warnings, "sample_limit_200_fields_16")
			} else {
				t.Warnings = append(t.Warnings, "sample_permission_or_query_unavailable")
			}
		} else {
			t.Warnings = append(t.Warnings, "content_budget_exceeded")
		}
		for j := range t.Fields {
			discoveryFieldHits(&t.Fields[j], c.Rules, values[j])
		}
	}
	if e = tx.Commit(); e != nil {
		return r, errors.New("MySQL采集事务未完成")
	}
	return r, nil
}
func mysqlIdentifier(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
