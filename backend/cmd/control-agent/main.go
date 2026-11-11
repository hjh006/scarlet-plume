package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5"
)

type journalEntry struct {
	Spec   store.ControlSpec   `json:"spec"`
	Phase  string              `json:"phase"`
	State  string              `json:"state"`
	Result store.ControlResult `json:"result"`
}
type controlAgent struct {
	origin, token, path string
	client              *http.Client
	dbConfig            *pgx.ConnConfig
	journal             map[string]journalEntry
}

func main() {
	origin := flag.String("url", "", "platform HTTPS origin")
	local := flag.Bool("local", false, "allow fixed local Docker or loopback endpoints")
	once := flag.Bool("once", false, "claim and execute one command, then exit")
	path := flag.String("journal", "/data/control-journal.json", "durable execution journal (contains no database credentials)")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal("unexpected arguments")
	}
	u, err := url.Parse(*origin)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || u.Scheme != "https" && (u.Scheme != "http" || !*local || !localHost(u.Hostname())) {
		fatal("provide a fixed HTTPS platform origin; local HTTP needs -local")
	}
	token := os.Getenv("SCARLET_OPERATIONS_TOKEN")
	if token == "" || len(token) > 1024 || strings.ContainsAny(token, " \t\r\n") {
		fatal("set SCARLET_OPERATIONS_TOKEN")
	}
	config, err := pgx.ParseConfig(os.Getenv("SCARLET_CONTROL_PG_DSN"))
	if err != nil || len(config.Fallbacks) > 0 || config.Host == "" || strings.ContainsAny(config.Host, "/\\,\r\n") {
		fatal("set a single fixed PostgreSQL connection")
	}
	secure := config.TLSConfig != nil && !config.TLSConfig.InsecureSkipVerify && config.TLSConfig.ServerName == config.Host
	if !secure && (!*local || !(config.Host == "sample-db" || net.ParseIP(config.Host).IsLoopback())) {
		fatal("PostgreSQL must use verified TLS; local sample needs -local")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	agent := controlAgent{origin: strings.TrimRight(u.String(), "/"), token: token, path: *path, dbConfig: config, client: &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, journal: map[string]journalEntry{}}
	if data, err := os.ReadFile(*path); err == nil {
		if len(data) > 4<<20 || json.Unmarshal(data, &agent.journal) != nil {
			fatal("execution journal is invalid")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		fatal("execution journal is not readable")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	defer transport.CloseIdleConnections()
	for ctx.Err() == nil {
		if err := agent.runOne(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "control task could not complete; retained for retry")
			if *once {
				os.Exit(1)
			}
		}
		if *once {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
func fatal(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
func localHost(host string) bool {
	return host == "app" || host == "localhost" || net.ParseIP(host).IsLoopback()
}

func (a *controlAgent) post(ctx context.Context, path string, input, output any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, a.origin+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+a.token)
	response, err := a.client.Do(r)
	if err != nil {
		return errors.New("platform unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("platform rejected command or receipt")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<10+1))
	if err != nil || len(data) > 64<<10 {
		return errors.New("invalid platform response")
	}
	if output != nil && json.Unmarshal(data, output) != nil {
		return errors.New("invalid command envelope")
	}
	return nil
}
func (a *controlAgent) persist() error {
	data, err := json.Marshal(a.journal)
	if err != nil || len(data) > 4<<20 {
		return errors.New("execution journal capacity reached")
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(a.path), ".control-journal-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, a.path)
}
func controlJournalKey(command store.ControlCommand) string {
	raw, _ := json.Marshal(struct {
		ID    int64
		Spec  store.ControlSpec
		Phase string
	}{command.ID, command.Spec, command.Phase})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (a *controlAgent) runOne(ctx context.Context) error {
	var response struct {
		Command *store.ControlCommand `json:"command"`
	}
	if err := a.post(ctx, "/api/v1/operations/control/claim", struct{}{}, &response); err != nil {
		return err
	}
	if response.Command == nil {
		return nil
	}
	command := *response.Command
	key := controlJournalKey(command)
	entry, ok := a.journal[key]
	if !ok || entry.State != "completed" {
		result := a.execute(ctx, command, key)
		entry = journalEntry{Spec: command.Spec, Phase: command.Phase, State: "completed", Result: result}
		a.journal[key] = entry
		if err := a.persist(); err != nil {
			return err
		}
	}
	// The phase/result are frozen before delivery. Lease retries never re-run a
	// completed permission change or rewrite the captured original permission.
	return a.post(ctx, "/api/v1/operations/control/receipts", store.ControlReceipt{ActionID: command.ID, Revision: command.Revision, Lease: command.Lease, Result: entry.Result}, nil)
}

const directSelectQuery = `SELECT COALESCE(bool_or(a.privilege_type='SELECT'),false),COALESCE(bool_or(a.privilege_type='SELECT' AND a.is_grantable),false)
 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(c.relacl,pg_catalog.acldefault('r',c.relowner)))a
 WHERE n.nspname=$1 AND c.relname=$2 AND c.relkind IN('r','p') AND a.grantee=CASE WHEN $3::text='PUBLIC' THEN 0::oid ELSE (SELECT oid FROM pg_catalog.pg_roles WHERE rolname=$3) END`

func (a *controlAgent) execute(parent context.Context, command store.ControlCommand, key string) store.ControlResult {
	result := store.ControlResult{Phase: command.Phase, Code: "execution_failed", ObservedAt: time.Now().UTC().Truncate(time.Microsecond)}
	spec := command.Spec
	if command.ID < 1 || command.Lease == "" || command.Phase != "apply" && command.Phase != "rollback" || spec.Schema == "" || spec.Table == "" || spec.Role == "" || spec.Host != a.dbConfig.Host || spec.Port != int(a.dbConfig.Port) || spec.Database != a.dbConfig.Database {
		result.Code = "configuration_mismatch"
		return result
	}
	if command.Phase == "apply" && !time.Now().Before(command.ExpiresAt) {
		result.Code = "expired"
		return result
	}
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, a.dbConfig)
	if err != nil {
		result.Code = "database_unavailable"
		return result
	}
	defer conn.Close(context.Background())
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout='300ms'"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout='2s'"); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND c.relkind IN('r','p'))`, spec.Schema, spec.Table).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			result.Code = "scope_changed"
			return errors.New("target no longer exists")
		}
		var before, grantable bool
		if err := tx.QueryRow(ctx, directSelectQuery, spec.Schema, spec.Table, spec.Role).Scan(&before, &grantable); err != nil {
			return err
		}
		result.HadSelect = &before
		intent, recovering := a.journal[key]
		if command.Phase == "apply" {
			if grantable || before != spec.ExpectedSelect && !(recovering && intent.State == "intent" && intent.Result.HadSelect != nil && *intent.Result.HadSelect) {
				result.Code = "scope_changed"
				return errors.New("permission scope changed")
			}
			if recovering && intent.State == "intent" && intent.Result.HadSelect != nil {
				result.HadSelect = intent.Result.HadSelect
			} else {
				a.journal[key] = journalEntry{Spec: spec, Phase: command.Phase, State: "intent", Result: result}
				if err := a.persist(); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, "REVOKE SELECT ON TABLE "+pgx.Identifier{spec.Schema, spec.Table}.Sanitize()+" FROM "+controlRole(spec.Role)+" RESTRICT"); err != nil {
				result.Code = "permission_denied"
				return err
			}
		} else {
			if command.Original == nil || !command.Original.Success || command.Original.HadSelect == nil || !*command.Original.HadSelect {
				result.Code = "scope_changed"
				return errors.New("no confirmed original permission")
			}
			if grantable || before && !recovering {
				result.Code = "scope_changed"
				return errors.New("permission changed since revocation")
			}
			if !recovering {
				a.journal[key] = journalEntry{Spec: spec, Phase: command.Phase, State: "intent", Result: result}
				if err := a.persist(); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, "GRANT SELECT ON TABLE "+pgx.Identifier{spec.Schema, spec.Table}.Sanitize()+" TO "+controlRole(spec.Role)); err != nil {
				result.Code = "permission_denied"
				return err
			}
		}
		var after, afterGrantable bool
		if err := tx.QueryRow(ctx, directSelectQuery, spec.Schema, spec.Table, spec.Role).Scan(&after, &afterGrantable); err != nil {
			return err
		}
		result.HasSelect = &after
		if command.Phase == "apply" && after || command.Phase == "rollback" && after != spec.ExpectedSelect || afterGrantable {
			return errors.New("permission postcondition failed")
		}
		return nil
	})
	result.ObservedAt = time.Now().UTC().Truncate(time.Microsecond)
	if err != nil {
		return result
	}
	result.Success = true
	result.Code = "select_revoked"
	if command.Phase == "rollback" {
		result.Code = "select_restored"
	}
	return result
}
func controlRole(role string) string {
	if role == "PUBLIC" {
		return "PUBLIC"
	}
	return pgx.Identifier{role}.Sanitize()
}
