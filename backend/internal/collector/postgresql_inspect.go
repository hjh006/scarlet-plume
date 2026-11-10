package collector

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	maxPostgreSQLColumns = 10000
	maxPostgreSQLGrants  = 20000
)

type PostgreSQLTable struct {
	Asset
	Schema              string             `json:"schema"`
	Owner               string             `json:"owner"`
	Columns             []PostgreSQLColumn `json:"columns"`
	Grants              []PostgreSQLGrant  `json:"grants"`
	PublicSchemaUsage   bool               `json:"publicSchemaUsage"`
	PublicSelect        bool               `json:"publicSelect"`
	PublicSelectColumns []string           `json:"publicSelectColumns"`
}

type PostgreSQLColumn struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Ordinal     int    `json:"ordinal"`
	Nullable    bool   `json:"nullable"`
	Sensitivity string `json:"sensitivity,omitempty"`
}

type PostgreSQLGrant struct {
	Grantee     string `json:"grantee"`
	Grantor     string `json:"grantor"`
	Public      bool   `json:"public"`
	Privilege   string `json:"privilege"`
	Grantable   bool   `json:"grantable"`
	Column      string `json:"column,omitempty"`
	SchemaUsage bool   `json:"schemaUsage"`
}

const postgresInspectTableQuery = `
SELECT c.oid, pg_catalog.quote_ident(n.nspname) || '.' || pg_catalog.quote_ident(c.relname),
       c.relname, n.nspname, pg_catalog.pg_get_userbyid(c.relowner),
       EXISTS (
           SELECT FROM pg_catalog.aclexplode(COALESCE(n.nspacl, pg_catalog.acldefault('n', n.nspowner))) AS a
           WHERE a.grantee = 0 AND a.privilege_type = 'USAGE'
       )
FROM pg_catalog.pg_class AS c
JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
WHERE n.nspname::text = ANY ($1::text[])
  AND pg_catalog.has_schema_privilege(n.oid, 'USAGE')
  AND c.relkind IN ('r', 'p')
ORDER BY n.nspname, c.relname
LIMIT $2`

const postgresInspectColumnQuery = `
SELECT a.attrelid, a.attname, pg_catalog.format_type(a.atttypid, CASE WHEN t.typnamespace = 'pg_catalog'::pg_catalog.regnamespace
                                              THEN a.atttypmod ELSE -1 END), a.attnum::integer,
       NOT (a.attnotnull OR t.typnotnull)
FROM pg_catalog.pg_attribute AS a
JOIN pg_catalog.pg_type AS t ON t.oid = a.atttypid
WHERE a.attrelid = ANY ($1::oid[]) AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attrelid, a.attnum
LIMIT $2`

// PostgreSQL stores PUBLIC as OID 0. Its schema usage comes from the schema ACL,
// while schema usage for a named grantee comes from PostgreSQL's privilege check.
const postgresInspectGrantQuery = `
WITH target AS (
    SELECT c.oid, c.relacl, c.relowner, n.oid AS schema_oid,
           EXISTS (
               SELECT FROM pg_catalog.aclexplode(COALESCE(n.nspacl, pg_catalog.acldefault('n', n.nspowner))) AS a
               WHERE a.grantee = 0 AND a.privilege_type = 'USAGE'
           ) AS public_schema_usage
    FROM pg_catalog.pg_class AS c
    JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
    WHERE c.oid = ANY ($1::oid[])
), grants AS (
    SELECT t.oid, ''::text AS column_name, 0 AS ordinal,
           a.grantee, a.grantor, a.privilege_type, a.is_grantable
    FROM target AS t
    CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(t.relacl, pg_catalog.acldefault('r', t.relowner))) AS a
    UNION ALL
    SELECT t.oid, c.attname::text, c.attnum::integer,
           a.grantee, a.grantor, a.privilege_type, a.is_grantable
    FROM target AS t
    JOIN pg_catalog.pg_attribute AS c ON c.attrelid = t.oid AND c.attnum > 0 AND NOT c.attisdropped
    CROSS JOIN LATERAL pg_catalog.aclexplode(c.attacl) AS a
)
SELECT g.oid, g.column_name, g.grantee = 0,
       CASE WHEN g.grantee = 0 THEN 'PUBLIC' ELSE pg_catalog.pg_get_userbyid(g.grantee)::text END,
       pg_catalog.pg_get_userbyid(g.grantor), g.privilege_type, g.is_grantable,
       CASE WHEN g.grantee = 0 THEN t.public_schema_usage
            ELSE pg_catalog.has_schema_privilege(g.grantee, t.schema_oid, 'USAGE') END
FROM grants AS g
JOIN target AS t USING (oid)
ORDER BY g.oid, g.ordinal, g.grantee, g.privilege_type, g.grantor
LIMIT $2`

// InspectPostgreSQL reads column and ACL metadata within a single catalog snapshot.
func InspectPostgreSQL(ctx context.Context, dsn string, schemas []string, allowInsecureLoopback bool) ([]PostgreSQLTable, error) {
	return inspectPostgreSQL(ctx, dsn, schemas, allowInsecureLoopback, false)
}

// InspectPostgreSQLDockerSample uses the local Docker sample transport policy.
func InspectPostgreSQLDockerSample(ctx context.Context, dsn string, schemas []string) ([]PostgreSQLTable, error) {
	return inspectPostgreSQL(ctx, dsn, schemas, false, true)
}

func inspectPostgreSQL(ctx context.Context, dsn string, schemas []string, allowInsecureLoopback, dockerSample bool) ([]PostgreSQLTable, error) {
	tables := make([]PostgreSQLTable, 0)
	err := withPostgreSQLCatalog(ctx, dsn, schemas, allowInsecureLoopback, dockerSample, func(ctx context.Context, tx pgx.Tx, schemas []string) error {
		ids, positions, err := postgresInspectTables(ctx, tx, schemas, &tables)
		if err != nil || len(ids) == 0 {
			return err
		}
		if err := postgresInspectColumns(ctx, tx, ids, positions, tables); err != nil {
			return err
		}
		return postgresInspectGrants(ctx, tx, ids, positions, tables)
	})
	if err != nil {
		return nil, err
	}
	return tables, nil
}

func postgresInspectTables(ctx context.Context, tx pgx.Tx, schemas []string, tables *[]PostgreSQLTable) ([]uint32, map[uint32]int, error) {
	rows, err := tx.Query(ctx, postgresInspectTableQuery, schemas, maxAssets+1)
	if err != nil {
		return nil, nil, errReadPostgreSQLCatalog
	}
	defer rows.Close()
	ids := make([]uint32, 0)
	positions := make(map[uint32]int)
	for rows.Next() {
		if len(ids) == maxAssets {
			return nil, nil, errors.New("PostgreSQL 表超过单批 1000 项上限，请缩小 schema 范围")
		}
		var id uint32
		table := PostgreSQLTable{
			Columns: []PostgreSQLColumn{}, Grants: []PostgreSQLGrant{}, PublicSelectColumns: []string{},
		}
		if err := rows.Scan(&id, &table.SourceRef, &table.Name, &table.Schema, &table.Owner, &table.PublicSchemaUsage); err != nil {
			return nil, nil, errReadPostgreSQLCatalog
		}
		positions[id] = len(*tables)
		ids = append(ids, id)
		*tables = append(*tables, table)
	}
	if rows.Err() != nil {
		return nil, nil, errReadPostgreSQLCatalog
	}
	return ids, positions, nil
}

func postgresInspectColumns(ctx context.Context, tx pgx.Tx, ids []uint32, positions map[uint32]int, tables []PostgreSQLTable) error {
	rows, err := tx.Query(ctx, postgresInspectColumnQuery, ids, maxPostgreSQLColumns+1)
	if err != nil {
		return errReadPostgreSQLCatalog
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		if count == maxPostgreSQLColumns {
			return errors.New("PostgreSQL 字段超过单批 10000 项上限，请缩小 schema 范围")
		}
		var id uint32
		var column PostgreSQLColumn
		if err := rows.Scan(&id, &column.Name, &column.Type, &column.Ordinal, &column.Nullable); err != nil {
			return errReadPostgreSQLCatalog
		}
		column.Sensitivity = MetadataSensitivity(column.Name, column.Type)
		table := &tables[positions[id]]
		table.Columns = append(table.Columns, column)
		count++
	}
	if rows.Err() != nil {
		return errReadPostgreSQLCatalog
	}
	return nil
}

func postgresInspectGrants(ctx context.Context, tx pgx.Tx, ids []uint32, positions map[uint32]int, tables []PostgreSQLTable) error {
	rows, err := tx.Query(ctx, postgresInspectGrantQuery, ids, maxPostgreSQLGrants+1)
	if err != nil {
		return errReadPostgreSQLCatalog
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		if count == maxPostgreSQLGrants {
			return errors.New("PostgreSQL 授权项超过单批 20000 项上限，请缩小 schema 范围")
		}
		var id uint32
		var grant PostgreSQLGrant
		if err := rows.Scan(&id, &grant.Column, &grant.Public, &grant.Grantee, &grant.Grantor, &grant.Privilege, &grant.Grantable, &grant.SchemaUsage); err != nil {
			return errReadPostgreSQLCatalog
		}
		table := &tables[positions[id]]
		table.Grants = append(table.Grants, grant)
		if grant.Public && grant.Privilege == "SELECT" {
			if grant.Column == "" {
				table.PublicSelect = true
			} else if len(table.PublicSelectColumns) == 0 || table.PublicSelectColumns[len(table.PublicSelectColumns)-1] != grant.Column {
				table.PublicSelectColumns = append(table.PublicSelectColumns, grant.Column)
			}
		}
		count++
	}
	if rows.Err() != nil {
		return errReadPostgreSQLCatalog
	}
	return nil
}

// MetadataSensitivity identifies candidate fields from their declared name and type.
func MetadataSensitivity(name, dataType string) string {
	dataType = strings.TrimSuffix(dataType, "[]")
	if dataType == "inet" || dataType == "cidr" {
		return "ip_address"
	}
	if dataType == "boolean" || dataType == "date" || strings.HasPrefix(dataType, "timestamp") {
		return ""
	}
	words := postgresNameWords(name)
	normalized := strings.Join(words, "")
	switch {
	case sensitivityName(words, normalized, "email", "emailaddress", "邮箱", "电子邮件"):
		return "email"
	case sensitivityName(words, normalized, "phone", "phonenumber", "mobilephone", "mobilenumber", "telephone", "telnumber", "手机号", "手机号码", "电话号码") || normalized == "mobile":
		return "phone"
	case sensitivityName(words, normalized, "idcard", "idcardnumber", "nationalid", "nationalidnumber", "identitynumber", "身份证", "身份证号", "身份证号码", "passportnumber", "护照号码"):
		return "national_id"
	case sensitivityName(words, normalized, "bankaccount", "bankaccountnumber", "bankcardnumber", "creditcardnumber", "debitcardnumber", "paymentcardnumber", "iban", "银行账号", "银行卡号"):
		return "financial_account"
	case sensitivityName(words, normalized, "password", "passwordhash", "passwd", "pwdhash", "apikey", "clientsecret", "privatekey", "accesstoken", "refreshtoken", "sessiontoken", "credential", "密码", "密码哈希", "密钥", "访问令牌") || normalized == "pwd":
		return "credential"
	case sensitivityName(words, normalized, "fullname", "firstname", "lastname", "givenname", "familyname", "realname", "contactname", "customername", "recipientname", "姓名"):
		return "personal_name"
	case sensitivityName(words, normalized, "streetaddress", "billingaddress", "shippingaddress", "postaladdress", "homeaddress", "residentialaddress", "住址", "联系地址", "收货地址") || normalized == "address":
		return "address"
	case sensitivityName(words, normalized, "ipaddress", "ipv4address", "ipv6address", "ip地址"):
		return "ip_address"
	default:
		return ""
	}
}

func postgresNameWords(name string) []string {
	runes := []rune(name)
	words := make([]string, 0, 4)
	var word strings.Builder
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			if word.Len() != 0 {
				words = append(words, word.String())
				word.Reset()
			}
			continue
		}
		if word.Len() != 0 && unicode.IsUpper(r) {
			previous := runes[i-1]
			if unicode.IsLower(previous) || unicode.IsNumber(previous) ||
				(i+1 < len(runes) && unicode.IsLower(runes[i+1])) {
				words = append(words, word.String())
				word.Reset()
			}
		}
		word.WriteRune(unicode.ToLower(r))
	}
	if word.Len() != 0 {
		words = append(words, word.String())
	}
	return words
}

func sensitivityName(words []string, normalized string, keywords ...string) bool {
	var suffix string
	for i := len(words) - 1; i >= 0; i-- {
		suffix = words[i] + suffix
		for _, keyword := range keywords {
			if suffix == keyword {
				return true
			}
		}
	}
	// Han words often appear without separators in Chinese column names.
	for _, keyword := range keywords {
		first, _ := utf8.DecodeRuneInString(keyword)
		if unicode.Is(unicode.Han, first) && strings.HasSuffix(normalized, keyword) {
			return true
		}
	}
	return false
}
