// service-sample is an explicitly instrumented, synthetic two-application fixture.
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
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

type rowQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type sample struct {
	databaseName                                              string
	db                                                        rowQueryer
	role, password, token, source, endpoint, peer, downstream string
	client                                                    *http.Client
}

func main() {
	role := os.Getenv("SCARLET_SERVICE_ROLE")
	password := os.Getenv("SCARLET_SERVICE_PASSWORD")
	token := os.Getenv("SCARLET_SERVICE_OTLP_TOKEN")
	source := os.Getenv("SCARLET_SERVICE_DATA_SOURCE")
	if (role != "a" && role != "b") || len(password) < 16 || token == "" || source == "" {
		fmt.Fprintln(os.Stderr, "configure the synthetic service role, credentials and source alias")
		os.Exit(1)
	}
	u := &url.URL{Scheme: "postgresql", Host: "sample-db:5432", Path: "/scarlet_sample", User: url.UserPassword("sample_reader", os.Getenv("SCARLET_SAMPLE_READER_PASSWORD")), RawQuery: "sslmode=disable"}
	db, err := pgxpool.New(context.Background(), u.String())
	if err != nil {
		fmt.Fprintln(os.Stderr, "sample database configuration failed")
		os.Exit(1)
	}
	defer db.Close()
	endpoint := os.Getenv("SCARLET_SERVICE_OTLP_URL")
	if endpoint == "" {
		endpoint = "http://app:8080/api/v1/runtime/otlp/v1/traces"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	app := &sample{db: db, databaseName: db.Config().ConnConfig.Database, role: role, password: password, token: token, source: source, endpoint: endpoint, peer: os.Getenv("SCARLET_SERVICE_PEER"), downstream: "http://service-sample-b:8080/api/orders", client: &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if role == "a" && app.peer == "" {
		fmt.Fprintln(os.Stderr, "configure the registered downstream service alias")
		os.Exit(1)
	}
	server := &http.Server{Addr: "0.0.0.0:8080", Handler: app, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 8 * time.Second, IdleTimeout: 30 * time.Second}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "sample listener stopped")
		os.Exit(1)
	}
}
func randomID(size int) []byte {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic("random identity unavailable")
	}
	return value
}
func nonzeroHex(value string, size int) ([]byte, bool) {
	if len(value) != 2*size {
		return nil, false
	}
	decoded, err := hex.DecodeString(value)
	return decoded, err == nil && !bytes.Equal(decoded, make([]byte, size))
}
func traceContext(raw string) ([]byte, []byte) {
	parts := strings.Split(raw, "-")
	if len(parts) == 4 && parts[0] == "00" && (parts[3] == "00" || parts[3] == "01") {
		trace, ok := nonzeroHex(parts[1], 16)
		parent, valid := nonzeroHex(parts[2], 8)
		if ok && valid {
			return trace, parent
		}
	}
	return randomID(16), nil
}
func str(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}
func number(key string, value int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: value}}}
}
func span(trace, id, parent []byte, kind tracepb.Span_SpanKind, start, end time.Time, attrs ...*commonpb.KeyValue) *tracepb.Span {
	return &tracepb.Span{TraceId: trace, SpanId: id, ParentSpanId: parent, Kind: kind, StartTimeUnixNano: uint64(start.UnixNano()), EndTimeUnixNano: uint64(end.UnixNano()), Attributes: attrs}
}
func (app *sample) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		w.WriteHeader(204)
		return
	}
	user, password, ok := r.BasicAuth()
	got, want := sha256.Sum256([]byte(password)), sha256.Sum256([]byte(app.password))
	if !ok || user != "scarlet-demo" || !hmac.Equal(got[:], want[:]) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Scarlet synthetic service"`)
		http.Error(w, "Authentication required", 401)
		return
	}
	route, table, query := "/api/chain", "classification_contacts", "SELECT count(*) FROM inventory.classification_contacts"
	if app.role == "b" {
		route, table, query = "/api/orders", "orders", "SELECT count(*) FROM inventory.orders"
	}
	if r.Method != http.MethodGet || r.URL.Path != route {
		http.NotFound(w, r)
		return
	}
	started := time.Now()
	trace, parent := traceContext(r.Header.Get("traceparent"))
	serverID, dbID := randomID(8), randomID(8)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	queryStart := time.Now()
	var rows int64
	if err := app.db.QueryRow(ctx, query).Scan(&rows); err != nil {
		http.Error(w, "Synthetic database unavailable", 503)
		return
	}
	queryEnd := time.Now()
	spans := []*tracepb.Span{span(trace, dbID, serverID, tracepb.Span_SPAN_KIND_CLIENT, queryStart, queryEnd, str("db.system.name", "postgresql"), str("db.namespace", app.databaseName), str("db.collection.name", table), str("scarlet.db.schema", "inventory"), str("scarlet.data.source", app.source), str("db.operation.name", "SELECT"))}
	var downstreamRows int64
	if app.role == "a" {
		clientID := randomID(8)
		callStart := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, app.downstream, nil)
		if err != nil {
			http.Error(w, "Downstream unavailable", 503)
			return
		}
		req.SetBasicAuth("scarlet-demo", app.password)
		req.Header.Set("traceparent", "00-"+hex.EncodeToString(trace)+"-"+hex.EncodeToString(clientID)+"-01")
		resp, err := app.client.Do(req)
		if err != nil {
			http.Error(w, "Downstream unavailable", 503)
			return
		}
		var result struct {
			Rows int64 `json:"rowsRead"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result)
		_ = resp.Body.Close()
		callEnd := time.Now()
		if resp.StatusCode != 200 || decodeErr != nil {
			http.Error(w, "Downstream unavailable", 503)
			return
		}
		downstreamRows = result.Rows
		spans = append(spans, span(trace, clientID, serverID, tracepb.Span_SPAN_KIND_CLIENT, callStart, callEnd, str("peer.service", app.peer), str("http.request.method", "GET"), number("http.response.status_code", int64(resp.StatusCode))))
	}
	ended := time.Now()
	spans = append(spans, span(trace, serverID, parent, tracepb.Span_SPAN_KIND_SERVER, started, ended, str("http.route", route), str("http.request.method", "GET"), number("http.response.status_code", 200)))
	if app.emit(ctx, spans) == nil {
		w.Header().Set("X-Scarlet-Telemetry", "delivered")
	} else {
		w.Header().Set("X-Scarlet-Telemetry", "failed")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"rowsRead": rows, "downstreamRows": downstreamRows, "traceRef": hex.EncodeToString(trace), "serviceRole": app.role})
}
func (app *sample) emit(ctx context.Context, spans []*tracepb.Span) error {
	payload, err := proto.Marshal(&tracepb.TracesData{ResourceSpans: []*tracepb.ResourceSpans{{ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}}}}})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, app.endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Authorization", "Bearer "+app.token)
	resp, err := app.client.Do(req)
	if err != nil {
		return errors.New("telemetry unavailable")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != 200 {
		return errors.New("telemetry rejected")
	}
	return nil
}
