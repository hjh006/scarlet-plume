package collector

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

func discoverMongo(ctx context.Context, c DiscoveryConfig, dsn string, insecure, localDocker bool) (DiscoveryResult, error) {
	u, e := url.Parse(dsn)
	if e != nil || u.Scheme != "mongodb" || u.Hostname() == "" || strings.ContainsAny(u.Host, ",/@") || strings.TrimPrefix(u.Path, "/") != c.Database {
		return DiscoveryResult{}, errors.New("MongoDB连接要求单主机mongodb URL与指定数据库")
	}
	for key := range u.Query() {
		if key != "authSource" && key != "authMechanism" {
			return DiscoveryResult{}, errors.New("MongoDB URL仅允许authSource/authMechanism选项，TLS由采集器强制")
		}
	}
	opts := options.Client().ApplyURI(dsn).SetServerSelectionTimeout(5 * time.Second).SetConnectTimeout(5 * time.Second).SetMaxPoolSize(1).SetRetryWrites(false)
	if localDocker {
		opts.SetTLSConfig(nil)
	} else if insecure && loopbackIP(u.Hostname()) != nil {
		port := u.Port()
		if port == "" {
			port = "27017"
		}
		u.Host = net.JoinHostPort(loopbackIP(u.Hostname()).String(), port)
		opts.ApplyURI(u.String())
		opts.SetTLSConfig(nil)
	} else {
		opts.SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()})
	}
	client, e := mongo.Connect(opts)
	if e != nil {
		return DiscoveryResult{}, errors.New("MongoDB连接配置无效")
	}
	defer client.Disconnect(context.Background())
	if e = client.Ping(ctx, readpref.Primary()); e != nil {
		return DiscoveryResult{}, errors.New("无法连接MongoDB，请核对TLS、账号及数据库权限")
	}
	db := client.Database(c.Database)
	cursor, e := db.ListCollections(ctx, bson.D{})
	if e != nil {
		return DiscoveryResult{}, errors.New("无法列举MongoDB集合")
	}
	defer cursor.Close(ctx)
	r := newDiscoveryResult()
	r.Coverage.Metadata = "declared_validator_and_sample_inference"
	r.Coverage.Permissions = "unknown"
	r.Coverage.Warnings = append(r.Coverage.Warnings, "mongo_schema_inferred_from_200_documents_not_exhaustive", "mongo_acl_and_lineage_unknown")
	for cursor.Next(ctx) {
		if len(r.Tables) == 100 {
			return r, errors.New("MongoDB超过100集合，请缩小范围")
		}
		var info struct {
			Name    string `bson:"name"`
			Type    string `bson:"type"`
			Options struct {
				Validator bson.M `bson:"validator"`
			} `bson:"options"`
		}
		if cursor.Decode(&info) != nil {
			return r, errors.New("MongoDB集合目录无效")
		}
		if info.Type != "collection" {
			continue
		}
		if len(c.Scopes) > 0 && !slices.Contains(c.Scopes, info.Name) {
			continue
		}
		t := blankDiscoveryTable(c.Database, "", info.Name)
		t.Warnings = append(t.Warnings, "sample_inferred_schema", "permissions_unknown", "lineage_unknown")
		fields := map[string]*DiscoveryField{}
		values := map[string][]string{}
		// JSON schema validator is authoritative where it exists; samples add observed paths.
		if schema := mongoDocumentMap(info.Options.Validator["$jsonSchema"]); schema != nil {
			if props := mongoDocumentMap(schema["properties"]); props != nil {
				for k, v := range props {
					f := &DiscoveryField{Name: k, Type: "declared", Nullable: true}
					if m := mongoDocumentMap(v); m != nil {
						if typ, ok := m["bsonType"].(string); ok {
							f.Type = typ
						}
						if desc, ok := m["description"].(string); ok {
							f.Comment = desc
						}
					}
					fields[k] = f
				}
			}
		}
		if len(r.Tables) < 10 {
			docs, de := db.Collection(info.Name).Find(ctx, bson.D{}, options.Find().SetLimit(200).SetBatchSize(50))
			if de == nil {
				for docs.Next(ctx) {
					var document bson.M
					if docs.Decode(&document) != nil {
						continue
					}
					t.ScannedRows++
					mongoDiscoveryFields("", document, fields, values, 0)
				}
				if docs.Err() != nil {
					t.Warnings = append(t.Warnings, "sample_incomplete")
				}
				docs.Close(ctx)
			} else {
				t.Warnings = append(t.Warnings, "sample_permission_or_query_unavailable")
			}
		} else {
			t.Warnings = append(t.Warnings, "content_budget_exceeded")
		}
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if len(keys) > 1000 {
			return r, errors.New("MongoDB集合字段超过1000路径")
		}
		for _, k := range keys {
			f := fields[k]
			discoveryFieldHits(f, c.Rules, values[k])
			t.Fields = append(t.Fields, *f)
		}
		r.Tables = append(r.Tables, t)
	}
	if cursor.Err() != nil {
		return r, errors.New("MongoDB集合列表不完整")
	}
	return r, nil
}
func mongoDiscoveryFields(prefix string, m bson.M, fields map[string]*DiscoveryField, values map[string][]string, depth int) {
	if depth > 8 {
		return
	}
	for key, value := range m {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if len(path) > 256 || len(fields) >= 1001 {
			continue
		}
		typ := "unknown"
		switch v := value.(type) {
		case bson.D:
			typ = "object"
			mongoDiscoveryFields(path, mongoDocumentMap(v), fields, values, depth+1)
		case bson.M:
			typ = "object"
			mongoDiscoveryFields(path, v, fields, values, depth+1)
		case bson.A:
			typ = "array"
		case string:
			typ = "string"
			if len(v) <= 256 {
				values[path] = append(values[path], v)
			}
		case int32, int64, float64:
			typ = "number"
			values[path] = append(values[path], fmt.Sprint(value))
		case bool:
			typ = "boolean"
		case nil:
			typ = "null"
		case bson.ObjectID:
			typ = "objectId"
		case bson.DateTime:
			typ = "date"
		}
		if fields[path] == nil {
			fields[path] = &DiscoveryField{Name: path, Type: typ, Nullable: true}
		} else if fields[path].Type != typ {
			fields[path].Type = "mixed"
		}
	}
}

func mongoDocumentMap(value any) bson.M {
	switch v := value.(type) {
	case bson.M:
		return v
	case bson.D:
		m := bson.M{}
		for _, e := range v {
			m[e.Key] = e.Value
		}
		return m
	}
	return nil
}
