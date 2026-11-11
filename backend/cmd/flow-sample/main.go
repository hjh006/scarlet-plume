package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

const fixtureUser = "scarlet-demo"
const fixtureQuery = "SELECT contact_value FROM inventory.classification_contacts ORDER BY id LIMIT 20"

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type sampleApp struct {
	database                                queryer
	password, salt, source, endpoint, token string
	databaseName                            string
	client                                  *http.Client
}

func main() {
	password, salt, token, source := os.Getenv("SCARLET_FLOW_PASSWORD"), os.Getenv("SCARLET_FLOW_SUBJECT_SALT"), os.Getenv("SCARLET_FLOW_OTLP_TOKEN"), os.Getenv("SCARLET_FLOW_DATA_SOURCE")
	if len(password) < 16 || len(salt) < 32 || token == "" || source == "" {
		fmt.Fprintln(os.Stderr, "set the flow sample credentials, subject salt, token and database source alias")
		os.Exit(1)
	}
	u := &url.URL{Scheme: "postgresql", Host: "sample-db:5432", Path: "/scarlet_sample", User: url.UserPassword("sample_reader", os.Getenv("SCARLET_SAMPLE_READER_PASSWORD")), RawQuery: "sslmode=disable"}
	pool, err := pgxpool.New(context.Background(), u.String())
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot configure sample database")
		os.Exit(1)
	}
	defer pool.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	app := &sampleApp{database: pool, databaseName: pool.Config().ConnConfig.Database, password: password, salt: salt, source: source, endpoint: "http://app:8080/api/v1/runtime/otlp/v1/traces", token: token, client: &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	server := &http.Server{Addr: "0.0.0.0:8080", Handler: app, ReadHeaderTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "flow sample listener stopped")
		os.Exit(1)
	}
}

func (app *sampleApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	username, password, ok := r.BasicAuth()
	got, want := sha256.Sum256([]byte(password)), sha256.Sum256([]byte(app.password))
	if !ok || username != fixtureUser || !hmac.Equal(got[:], want[:]) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Scarlet local flow sample", charset="UTF-8"`)
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'")
		_, _ = io.WriteString(w, `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>赤翎受控数据追踪样例</title><style>body{font:16px system-ui;max-width:760px;margin:60px auto;padding:24px;color:#15233d;background:#f5f8fe}main{background:white;padding:32px;border-radius:16px}a{color:#0562cd}code{background:#eaf3ff;padding:4px}</style><main><h1>受控数据追踪样例</h1><p>读取合成联系信息，源端生成字段格式计数及数据库查询关系。</p><p>响应字段标签：<code>contacts[].contact_value</code> · 源端邮箱格式检查。</p><p>数据库目标：<code>inventory.classification_contacts</code>。访问主体以 HMAC 去标识引用关联。</p><p><a href="/api/contacts">读取最多 20 条合成联系信息并产生观测</a></p><p>这是显式插桩的本地演示应用。</p></main></html>`)
		return
	}
	if r.URL.Path != "/api/contacts" {
		http.NotFound(w, r)
		return
	}
	started := time.Now().UTC()
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	rows, err := app.database.Query(ctx, fixtureQuery)
	if err != nil {
		http.Error(w, "Sample database unavailable", http.StatusServiceUnavailable)
		return
	}
	contacts := []map[string]string{}
	matched := int64(0)
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			break
		}
		contacts = append(contacts, map[string]string{"contact_value": value})
		for _, category := range runtimesecurity.FormatCategories(value) {
			if category == "email" {
				matched++
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	ended := time.Now().UTC()
	if err != nil {
		http.Error(w, "Sample database unavailable", http.StatusServiceUnavailable)
		return
	}
	body, err := json.Marshal(map[string]any{"contacts": contacts})
	if err != nil {
		http.Error(w, "Sample response unavailable", http.StatusInternalServerError)
		return
	}
	fields, _ := runtimesecurity.InspectJSON(body, "response")
	databaseFields := []runtimesecurity.DataField{}
	if matched > 0 {
		databaseFields = append(databaseFields, runtimesecurity.DataField{Name: "contact_value", Location: "database", Category: "email", Count: matched, Observed: int64(len(contacts)), Method: "format"})
	}
	if err := app.emit(r.Context(), started, ended, fields, databaseFields); err != nil {
		w.Header().Set("X-Scarlet-Telemetry", "failed")
		log.Print(err.Error())
	} else {
		w.Header().Set("X-Scarlet-Telemetry", "delivered")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func textAttribute(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

func (app *sampleApp) emit(ctx context.Context, started, ended time.Time, fields, databaseFields []runtimesecurity.DataField) error {
	trace, serverID, dbID := make([]byte, 16), make([]byte, 8), make([]byte, 8)
	for _, id := range [][]byte{trace, serverID, dbID} {
		if _, err := rand.Read(id); err != nil {
			return errors.New("cannot create observation identity")
		}
	}
	mac := hmac.New(sha256.New, []byte(app.salt))
	_, _ = mac.Write([]byte(fixtureUser))
	subject := hex.EncodeToString(mac.Sum(nil))
	fieldJSON, _ := json.Marshal(fields)
	dbJSON, _ := json.Marshal(databaseFields)
	server := &tracepb.Span{TraceId: trace, SpanId: serverID, Name: "GET /api/contacts", Kind: tracepb.Span_SPAN_KIND_SERVER, StartTimeUnixNano: uint64(started.UnixNano()), EndTimeUnixNano: uint64(ended.UnixNano()), Attributes: []*commonpb.KeyValue{textAttribute("http.request.method", "GET"), textAttribute("http.route", "/api/contacts"), {Key: "http.response.status_code", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 200}}}, textAttribute("scarlet.subject.ref", subject), textAttribute("scarlet.data.fields", string(fieldJSON))}}
	database := &tracepb.Span{TraceId: trace, SpanId: dbID, ParentSpanId: serverID, Name: "sample table read", Kind: tracepb.Span_SPAN_KIND_CLIENT, StartTimeUnixNano: uint64(started.UnixNano()), EndTimeUnixNano: uint64(ended.UnixNano()), Attributes: []*commonpb.KeyValue{textAttribute("db.system.name", "postgresql"), textAttribute("db.namespace", app.databaseName), textAttribute("db.collection.name", "classification_contacts"), textAttribute("scarlet.db.schema", "inventory"), textAttribute("db.operation.name", "SELECT"), textAttribute("scarlet.data.source", app.source), textAttribute("scarlet.data.fields", string(dbJSON))}}
	payload, err := proto.Marshal(&tracepb.TracesData{ResourceSpans: []*tracepb.ResourceSpans{{ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{server, database}}}}}})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, app.endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-protobuf")
	request.Header.Set("Authorization", "Bearer "+app.token)
	response, err := app.client.Do(request)
	if err != nil {
		return errors.New("sample telemetry unavailable")
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("sample telemetry rejected (HTTP %d)", response.StatusCode)
	}
	return nil
}
