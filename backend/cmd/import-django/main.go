package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/httpapi"
	"github.com/hjh006/scarlet-plume/backend/internal/legacy"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	apply := flag.Bool("apply", false, "commit a one-time copy from public Django tables")
	activate := flag.String("activate-user", "", "set an initial Go password for an imported active member")
	allowLoopback := flag.Bool("allow-insecure-loopback", false, "allow plaintext PostgreSQL on literal loopback only")
	flag.Parse()
	if flag.NArg() != 0 || (*apply && *activate != "") {
		return errors.New("choose either -apply or -activate-user, or neither to run a read-only preflight")
	}
	config, err := store.PostgresConfig(os.Getenv("SCARLET_DB_DSN"), *allowLoopback)
	if err != nil {
		return err
	}
	config.ConnConfig.RuntimeParams["application_name"] = "scarlet-plume-django-import"
	config.MaxConns = 1
	shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(shutdown, 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return errors.New("could not open the migration database pool")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return errors.New("could not connect to PostgreSQL; check database access and TLS")
	}

	if *activate != "" {
		username := strings.TrimSpace(*activate)
		if username == "" || utf8.RuneCountInString(username) > 150 || strings.IndexFunc(username, unicode.IsControl) >= 0 {
			return errors.New("provide a valid Django username")
		}
		hash, err := httpapi.HashPassword(os.Getenv("SCARLET_IMPORT_USER_PASSWORD"))
		if err != nil {
			return err
		}
		if err := legacy.ActivateUser(ctx, pool, username, hash); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Imported member activated.")
		return nil
	}

	report, err := legacy.Import(ctx, pool, *apply)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}
