package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/httpapi"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,59}$`)

func main() {
	if err := run(); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func envDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func run() error {
	mode := flag.String("mode", "serve", "serve, migrate, bootstrap, or add-reviewer")
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	web := flag.String("web", "", "path to built Vue dist (optional)")
	allowLoopback := flag.Bool("allow-insecure-loopback", false, "allow plaintext PostgreSQL on literal loopback only")
	flag.Parse()
	switch *mode {
	case "serve", "migrate", "bootstrap", "add-reviewer":
	default:
		return errors.New("mode must be serve, migrate, bootstrap, or add-reviewer")
	}

	poolConfig, err := store.PostgresConfig(os.Getenv("SCARLET_DB_DSN"), *allowLoopback)
	if err != nil {
		return err
	}
	poolConfig.ConnConfig.RuntimeParams["application_name"] = "scarlet-plume-go"
	poolConfig.MaxConns = 10

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("could not open database connection pool")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return errors.New("could not connect to PostgreSQL; check database access and TLS")
	}

	st := store.New(pool)
	switch *mode {
	case "migrate":
		if err := st.Migrate(ctx); err != nil {
			return errors.New("could not apply Scarlet Plume database migrations")
		}
		slog.Info("Scarlet Plume database migrated")
		return nil
	case "bootstrap":
		return bootstrap(ctx, st)
	case "add-reviewer":
		return addReviewer(ctx, st)
	default:
		return serve(st, *addr, *web)
	}
}

func bootstrap(ctx context.Context, st *store.Store) error {
	username := strings.TrimSpace(os.Getenv("SCARLET_BOOTSTRAP_USER"))
	slug := os.Getenv("SCARLET_BOOTSTRAP_WORKSPACE")
	name := strings.TrimSpace(os.Getenv("SCARLET_BOOTSTRAP_WORKSPACE_NAME"))
	if username == "" || utf8.RuneCountInString(username) > 80 || strings.IndexFunc(username, unicode.IsControl) >= 0 ||
		!slugPattern.MatchString(slug) || name == "" || utf8.RuneCountInString(name) > 100 ||
		strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return errors.New("provide valid SCARLET_BOOTSTRAP_USER, SCARLET_BOOTSTRAP_WORKSPACE, and SCARLET_BOOTSTRAP_WORKSPACE_NAME")
	}
	hash, err := httpapi.HashPassword(os.Getenv("SCARLET_BOOTSTRAP_PASSWORD"))
	if err != nil {
		return err
	}
	if err := st.Bootstrap(ctx, username, hash, slug, name); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return errors.New("workspace already has a different owner")
		}
		return errors.New("could not initialize the workspace; apply migrations first")
	}
	slog.Info("workspace owner initialized", "workspace", slug)
	return nil
}

func addReviewer(ctx context.Context, st *store.Store) error {
	username := strings.TrimSpace(os.Getenv("SCARLET_REVIEWER_USER"))
	slug := os.Getenv("SCARLET_BOOTSTRAP_WORKSPACE")
	if username == "" || utf8.RuneCountInString(username) > 80 || strings.IndexFunc(username, unicode.IsControl) >= 0 ||
		!slugPattern.MatchString(slug) {
		return errors.New("provide a valid SCARLET_REVIEWER_USER and SCARLET_BOOTSTRAP_WORKSPACE")
	}
	hash, err := httpapi.HashPassword(os.Getenv("SCARLET_REVIEWER_PASSWORD"))
	if err != nil {
		return err
	}
	if err := st.ProvisionReviewer(ctx, slug, username, hash); err != nil {
		switch {
		case errors.Is(err, store.ErrConflict):
			return errors.New("choose a separate username for the reviewer")
		case errors.Is(err, store.ErrNotFound):
			return errors.New("workspace not found; bootstrap it first")
		default:
			return errors.New("could not provision reviewer")
		}
	}
	slog.Info("workspace reviewer provisioned", "workspace", slug)
	return nil
}

func serve(st *store.Store, addr, web string) error {
	origin := envDefault("SCARLET_ORIGIN", "http://127.0.0.1:5173")
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" ||
		u.ForceQuery || u.Fragment != "" || (u.Scheme != "https" && (u.Scheme != "http" || !loopback(u.Hostname()))) {
		return errors.New("SCARLET_ORIGIN must be a single HTTPS origin or a literal loopback development origin")
	}
	if u.Scheme == "http" {
		host, _, err := net.SplitHostPort(addr)
		if err != nil || !loopback(host) {
			return errors.New("development HTTP may listen only on loopback")
		}
	}

	handler, err := newHandler(httpapi.New(st, origin, u.Scheme == "https"), web)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second,
		WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-shutdown.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	slog.Info("Scarlet Plume API listening", "addr", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func newHandler(api http.Handler, web string) (http.Handler, error) {
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if web != "" {
		info, err := os.Stat(web + "/index.html")
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("build web/dist before serving the Vue application")
		}
		files := http.FileServer(http.Dir(web))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; img-src 'self' data:")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			files.ServeHTTP(w, r)
		})
	}
	return mux, nil
}
