// media-inspector is a private authenticated OCR/ASR worker. It never proxies
// model requests, downloads models or logs media, recognition text or tokens.
package main

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

type options struct {
	listen, cert, key, tokenEnv string
	local                       bool
	command                     aisecurity.CommandMediaOptions
}

func parseOptions(args []string) (o options, err error) {
	f := flag.NewFlagSet("media-inspector", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.listen, "listen", "127.0.0.1:8912", "private listen address")
	f.StringVar(&o.cert, "tls-cert", "", "absolute HTTPS certificate path")
	f.StringVar(&o.key, "tls-key", "", "absolute HTTPS key path")
	f.BoolVar(&o.local, "local", false, "explicit literal-loopback HTTP for tests")
	f.StringVar(&o.tokenEnv, "token-env", "SCARLET_MEDIA_INSPECTOR_TOKEN", "authentication environment reference")
	f.StringVar(&o.command.Tesseract, "tesseract", "", "absolute tesseract binary path; empty disables OCR")
	f.StringVar(&o.command.Whisper, "whisper", "", "absolute whisper-cli binary path; empty disables ASR")
	f.StringVar(&o.command.WhisperModel, "whisper-model", "", "absolute read-only speech model path")
	f.StringVar(&o.command.WhisperModelSHA256, "whisper-model-sha256", "", "pinned speech model SHA-256")
	f.StringVar(&o.command.Languages, "languages", "eng", "installed tesseract languages")
	if e := f.Parse(args); e != nil || f.NArg() != 0 {
		return o, errors.New("invalid media inspector options")
	}
	host, port, e := net.SplitHostPort(o.listen)
	if e != nil || port == "" {
		return o, errors.New("invalid private listen address")
	}
	if o.local {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return o, errors.New("test HTTP requires literal loopback")
		}
		if o.cert != "" || o.key != "" {
			return o, errors.New("test HTTP cannot also configure TLS")
		}
	} else if !filepath.IsAbs(o.cert) || !filepath.IsAbs(o.key) {
		return o, errors.New("HTTPS certificate and key are required")
	}
	if !regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,95}$`).MatchString(o.tokenEnv) {
		return o, errors.New("invalid authentication environment reference")
	}
	return o, nil
}

func inspectionHandler(processor aisecurity.MediaProcessor, token string) (http.Handler, error) {
	if len(token) < 32 || len(token) > 512 || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("authentication environment unavailable")
	}
	inspect, e := aisecurity.NewMediaInspectionHandler(processor, token)
	if e != nil {
		return nil, e
	}
	slots := make(chan struct{}, 2)
	bounded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"code":"inspection_busy"}`)
			return
		}
		inspect.ServeHTTP(w, r)
	})
	timed := http.TimeoutHandler(bounded, 50*time.Second, `{"code":"inspection_timeout"}`)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" && r.URL.RawQuery == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/v1/inspect" || r.URL.RawQuery != "" {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"code":"not_found"}`)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"code":"inspection_authentication_required"}`)
			return
		}
		// The worker owns the slot until inspection returns, including when the
		// HTTP timeout has already canceled a slow command's context.
		timed.ServeHTTP(w, r)

	}), nil
}

func run(ctx context.Context, args []string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	o, e := parseOptions(args)
	if e != nil {
		return e
	}
	processor, e := aisecurity.NewCommandMediaProcessor(o.command)
	if e != nil {
		return errors.New("inspection commands or pinned model unavailable")
	}
	handler, e := inspectionHandler(processor, os.Getenv(o.tokenEnv))
	if e != nil {
		return e
	}
	server := &http.Server{Addr: o.listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 55 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 16 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}, BaseContext: func(net.Listener) context.Context { return ctx }, ErrorLog: log.New(io.Discard, "", 0)}
	if !o.local {
		certificate, e := tls.LoadX509KeyPair(o.cert, o.key)
		if e != nil {
			return errors.New("HTTPS certificate unavailable")
		}
		server.TLSConfig.Certificates = []tls.Certificate{certificate}
	}
	listener, e := net.Listen("tcp", o.listen)
	if e != nil {
		return errors.New("private listener unavailable")
	}
	defer listener.Close()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
	}()
	if o.local {
		e = server.Serve(listener)
	} else {
		e = server.ServeTLS(listener, "", "")
	}
	if errors.Is(e, http.ErrServerClosed) {
		<-stopped
		return nil
	}
	return errors.New("private inspection server stopped")
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if e := run(ctx, os.Args[1:]); e != nil {
		log.Print(e)
		os.Exit(1)
	}
}
