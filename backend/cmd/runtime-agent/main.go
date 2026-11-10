package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const telemetryPath = "/api/v1/runtime/gateway"

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP proxy listen address")
	upstreamOrigin := flag.String("upstream", "", "fixed upstream HTTP or HTTPS origin")
	platformOrigin := flag.String("url", "", "Scarlet Plume origin, defaults to SCARLET_RUNTIME_URL")
	local := flag.Bool("local", false, "allow plaintext telemetry to the local Docker app or loopback")
	certPath := flag.String("tls-cert", "", "PEM certificate chain for the HTTPS listener")
	keyPath := flag.String("tls-key", "", "PEM private key for the HTTPS listener")
	clientCA := flag.String("tls-client-ca", "", "require a client certificate signed by this PEM CA")
	upstreamCA := flag.String("upstream-ca", "", "PEM CA bundle for the HTTPS upstream")
	var templates []routeTemplate
	flag.Func("route", "trusted route template, repeatable", func(pattern string) error {
		return addRoute(&templates, pattern)
	})
	flag.Parse()
	if flag.NArg() != 0 || len(templates) == 0 {
		exit(errors.New("provide at least one -route and no positional arguments"))
	}
	upstream, err := parseOrigin(*upstreamOrigin)
	if err != nil {
		exit(errors.New("-upstream must be a fixed HTTP or HTTPS origin without a path, query or credentials"))
	}
	platform := *platformOrigin
	if platform == "" {
		platform = os.Getenv("SCARLET_RUNTIME_URL")
	}
	endpoint, err := receiverEndpoint(platform, *local)
	if err != nil {
		exit(err)
	}
	token := os.Getenv("SCARLET_RUNTIME_GATEWAY_TOKEN")
	if token == "" || len(token) > 1024 || strings.IndexFunc(token, func(r rune) bool {
		return r < 33 || r > 126
	}) >= 0 {
		exit(errors.New("set a runtime source token in SCARLET_RUNTIME_GATEWAY_TOKEN"))
	}

	telemetry := newObserver(endpoint, token)
	defer telemetry.client.CloseIdleConnections()
	upstreamTransport := directTransport()
	defer upstreamTransport.CloseIdleConnections()
	listenerTLS, err := listenerTLSConfig(*certPath, *keyPath, *clientCA)
	if err != nil {
		exit(err)
	}
	if err := configureUpstreamTLS(upstreamTransport, upstream, *upstreamCA); err != nil {
		exit(err)
	}
	server := &http.Server{
		Addr:              *listen,
		Handler:           proxyHandler(upstream, upstreamTransport, templates, telemetry),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
		TLSConfig:         listenerTLS,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		telemetry.run(ctx)
	}()
	serverErr := make(chan error, 1)
	go func() {
		if listenerTLS != nil {
			serverErr <- server.ListenAndServeTLS("", "")
			return
		}
		serverErr <- server.ListenAndServe()
	}()
	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			exit(err)
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
		<-workerDone
	}
}

func exit(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func parseOrigin(origin string) (*url.URL, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") ||
		u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(origin, "#") ||
		u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid HTTP origin")
	}
	return u, nil
}

func receiverEndpoint(origin string, local bool) (string, error) {
	u, err := parseOrigin(origin)
	if err != nil || u.Scheme == "http" && (!local || !localReceiver(u.Hostname())) {
		return "", errors.New("set -url or SCARLET_RUNTIME_URL to a HTTPS origin, or a local app origin with -local")
	}
	u.Path = telemetryPath
	return u.String(), nil
}

func localReceiver(host string) bool {
	return host == "app" || host == "localhost" || net.ParseIP(host).IsLoopback()
}

func directTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return transport
}

type observationKey struct{}

type requestObservation struct {
	method string
	route  string
	sent   bool
}

func proxyHandler(upstream *url.URL, transport http.RoundTripper, routes []routeTemplate, telemetry *observer) http.Handler {
	record := func(request *http.Request, status int) {
		if request == nil {
			return
		}
		observation, _ := request.Context().Value(observationKey{}).(*requestObservation)
		if observation == nil || observation.sent {
			return
		}
		observation.sent = true
		telemetry.enqueue(observationEvent{
			Method: observation.method, Route: observation.route,
			Status: status, ObservedAt: time.Now().UTC(),
		})
	}
	proxy := &httputil.ReverseProxy{
		Rewrite:   func(r *httputil.ProxyRequest) { r.SetURL(upstream) },
		Transport: transport,
		ErrorLog:  log.New(io.Discard, "", 0),
		ModifyResponse: func(response *http.Response) error {
			record(response.Request, response.StatusCode)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			record(r, http.StatusBadGateway)
			http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodPut, http.MethodPost, http.MethodDelete,
			http.MethodOptions, http.MethodHead, http.MethodPatch, http.MethodTrace:
			if route := matchRoute(routes, r.URL.EscapedPath()); route != "" {
				metadata := &requestObservation{method: r.Method, route: route}
				r = r.WithContext(context.WithValue(r.Context(), observationKey{}, metadata))
			}
		}
		proxy.ServeHTTP(w, r)
	})
}
