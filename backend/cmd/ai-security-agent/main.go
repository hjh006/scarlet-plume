// ai-security-agent is an explicit customer-side enforcement service. It is not
// transparent interception of arbitrary applications, models or MCP servers.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

type bindingFile struct {
	Credentials []aisecurity.Credential  `json:"credentials"`
	Targets     map[string]targetBinding `json:"targets"`
}
type targetBinding struct {
	URL              string `json:"url"`
	AuthorizationEnv string `json:"authorizationEnv,omitempty"`
}

func decodePrivate(path string, max int64, value any) error {
	f, e := os.Open(path)
	if e != nil {
		return errors.New("required configuration file unavailable")
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > max {
		return errors.New("configuration must be a private bounded regular file")
	}
	dec := json.NewDecoder(io.LimitReader(f, max+1))
	dec.DisallowUnknownFields()
	if dec.Decode(value) != nil {
		return errors.New("configuration JSON invalid")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errors.New("configuration contains trailing JSON")
	}
	return nil
}
func run() error {
	listen := flag.String("listen", "127.0.0.1:8900", "explicit listen address")
	config := flag.String("config", "", "trusted catalog JSON, reloaded for each authorization check")
	bindings := flag.String("bindings", "", "private credential-hash and static endpoint bindings JSON")
	documents := flag.String("documents", "", "private local document-store JSON (optional when RAG is unused)")
	audit := flag.String("audit", "", "private append-only content-free audit JSONL")
	journal := flag.String("journal", "", "durable request nonce journal, mandatory")
	cert := flag.String("tls-cert", "", "HTTPS certificate file")
	key := flag.String("tls-key", "", "HTTPS private key file")
	local := flag.Bool("local", false, "allow literal loopback-only HTTP for synthetic tests")
	concurrency := flag.Int("concurrency", 8, "bounded simultaneous operations, 1..32")
	definition := flag.String("tool-definition", "", "offline: print the canonical digest of one reviewed MCP Tool JSON file, then exit")
	flag.Parse()
	if *definition != "" {
		var raw json.RawMessage
		if e := decodePrivate(*definition, 65536, &raw); e != nil {
			return e
		}
		digest, e := aisecurity.ToolDefinitionDigest(raw)
		if e != nil {
			return errors.New("MCP tool definition is unsupported, ambiguous or exceeds its budget")
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			Digest          string `json:"digest"`
			ProtocolVersion string `json:"protocolVersion"`
		}{digest, aisecurity.MCPVersion})
	}
	if *config == "" || *bindings == "" || *audit == "" || *journal == "" {
		return errors.New("config, bindings, audit and journal are required")
	}
	host, _, e := net.SplitHostPort(*listen)
	if e != nil {
		return errors.New("invalid listen address")
	}
	if *local {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("local HTTP must bind a literal loopback IP")
		}
	} else if *cert == "" || *key == "" {
		return errors.New("production requires HTTPS certificate and key")
	}
	var binding bindingFile
	if e = decodePrivate(*bindings, 1<<20, &binding); e != nil {
		return e
	}
	targets := map[string]aisecurity.Target{}
	envName := regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,79}$`)
	for ref, target := range binding.Targets {
		authorization := ""
		if target.AuthorizationEnv != "" {
			if !envName.MatchString(target.AuthorizationEnv) {
				return errors.New("invalid upstream credential environment reference")
			}
			token := os.Getenv(target.AuthorizationEnv)
			if len(token) < 16 || len(token) > 4096 {
				return errors.New("upstream credential environment unavailable")
			}
			authorization = "Bearer " + token
		}
		targets[ref] = aisecurity.Target{URL: target.URL, Authorization: authorization}
	}
	replay, e := aisecurity.OpenReplayJournal(*journal)
	if e != nil {
		return errors.New("cannot open exclusive durable replay journal")
	}
	defer replay.Close()
	appendAudit, closeAudit, e := aisecurity.AppendAudit(*audit)
	if e != nil {
		return errors.New("cannot open private durable audit")
	}
	defer closeAudit()
	catalog := func(context.Context) (aisecurity.Config, error) {
		var c aisecurity.Config
		e := decodePrivate(*config, 4<<20, &c)
		return c, e
	}
	var docs func(context.Context) (map[string]aisecurity.LocalDocument, error)
	if *documents != "" {
		docs = func(context.Context) (map[string]aisecurity.LocalDocument, error) {
			var rows []aisecurity.LocalDocument
			if e := decodePrivate(*documents, 8<<20, &rows); e != nil {
				return nil, e
			}
			out := map[string]aisecurity.LocalDocument{}
			for _, v := range rows {
				if _, exists := out[v.ID]; exists {
					return nil, errors.New("duplicate local document")
				}
				out[v.ID] = v
			}
			return out, nil
		}
	}
	handler, e := aisecurity.NewRuntime(aisecurity.RuntimeOptions{Catalog: catalog, Credentials: binding.Credentials, Targets: targets, Documents: docs, Audit: func(_ context.Context, a aisecurity.AuditRecord) error { return appendAudit(a) }, Journal: replay, AllowLoopback: *local, Concurrency: *concurrency})
	if e != nil {
		return e
	}
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		bounded, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = server.Shutdown(bounded)
	}()
	log.Print("AI security runtime ready; only catalog-backed /v1/execute operations are accepted")
	if *local {
		e = server.ListenAndServe()
	} else {
		e = server.ListenAndServeTLS(*cert, *key)
	}
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "ai-security-agent:", e)
		os.Exit(1)
	}
}
