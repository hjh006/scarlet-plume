package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type validationDB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}
type sample struct {
	db                                           validationDB
	aUser, aPassword, bUser, bPassword, modeFile string
}

func (s *sample) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == "GET" && r.URL.Path == "/healthz" {
		w.WriteHeader(204)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "GET only", 405)
		return
	}
	user, password, ok := r.BasicAuth()
	valid := func(expected string) bool {
		a, b := sha256.Sum256([]byte(password)), sha256.Sum256([]byte(expected))
		return hmac.Equal(a[:], b[:])
	}
	if !ok || !(user == s.aUser && valid(s.aPassword) || user == s.bUser && valid(s.bPassword)) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Scarlet synthetic API validation"`)
		http.Error(w, "Authentication required", 401)
		return
	}
	prefix := "/api/secure/objects/"
	vulnerable := false
	if strings.HasPrefix(r.URL.Path, "/api/vulnerable/objects/") {
		prefix = "/api/vulnerable/objects/"
		mode, _ := os.ReadFile(s.modeFile)
		vulnerable = strings.TrimSpace(string(mode)) == "vulnerable"
	}
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	object := strings.TrimPrefix(r.URL.Path, prefix)
	owner := s.aUser
	id := int64(1)
	if object == "fixture-b" {
		owner, id = s.bUser, 2
	} else if object != "fixture-a" {
		http.NotFound(w, r)
		return
	}
	if !vulnerable && user != owner {
		http.Error(w, "Object access denied", 403)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var found int64
	if err := s.db.QueryRow(ctx, `SELECT id FROM inventory.customers WHERE id=$1`, id).Scan(&found); err != nil {
		http.Error(w, "Synthetic database unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"objectId": object, "owner": owner, "exists": found == id})
}
func main() {
	a, b := os.Getenv("SCARLET_VALIDATION_A_USER"), os.Getenv("SCARLET_VALIDATION_B_USER")
	ap, bp := os.Getenv("SCARLET_VALIDATION_A_PASSWORD"), os.Getenv("SCARLET_VALIDATION_B_PASSWORD")
	if a == "" || b == "" || a == b || ap == "" || bp == "" {
		fmt.Fprintln(os.Stderr, "configure two distinct synthetic test accounts")
		os.Exit(1)
	}
	u := &url.URL{Scheme: "postgresql", Host: "sample-db:5432", Path: "/scarlet_sample", User: url.UserPassword("sample_reader", os.Getenv("SCARLET_SAMPLE_READER_PASSWORD")), RawQuery: "sslmode=disable"}
	db, err := pgxpool.New(context.Background(), u.String())
	if err != nil {
		fmt.Fprintln(os.Stderr, "sample database configuration failed")
		os.Exit(1)
	}
	defer db.Close()
	mode := os.Getenv("SCARLET_VALIDATION_MODE_FILE")
	if mode == "" {
		mode = "/config/mode"
	}
	server := &http.Server{Addr: "0.0.0.0:8080", Handler: &sample{db, a, ap, b, bp, mode}, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "sample listener stopped")
		os.Exit(1)
	}
}
