// This isolated demonstration helper is not an LLM. It generates deterministic
// synthetic responses, or terminates TLS for the private demonstration controller.
package main

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"
)

func main() {
	mode := flag.String("mode", "provider", "provider, mcp, or controller-proxy")
	models := flag.String("models", "synthetic-media-model", "explicit comma-separated synthetic model IDs")
	providerID := flag.String("provider-id", "legacy", "content-free fixture identifier")
	toolDelay := flag.Duration("tool-delay", 0, "bounded synthetic tool response delay, at most five seconds")
	addr := flag.String("listen", ":9444", "private HTTPS listener")
	cert := flag.String("cert", "/certs/tls.crt", "TLS certificate")
	key := flag.String("key", "/certs/tls.key", "TLS key")
	imagePath := flag.String("fixture-image", "", "optional synthetic output PNG fixture")
	audioPath := flag.String("fixture-audio", "", "optional synthetic output WAV fixture")
	flag.Parse()
	if *toolDelay < 0 || *toolDelay > 5*time.Second {
		log.Fatal("invalid synthetic tool delay")
	}
	var handler http.Handler
	if *mode == "controller-proxy" {
		u, _ := url.Parse("http://controlplane:8080")
		proxy := httputil.NewSingleHostReverseProxy(u)
		proxy.Transport = &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 10, ResponseHeaderTimeout: 30 * time.Second}
		proxy.ErrorLog = log.New(io.Discard, "", 0)
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(502) }
		handler = proxy
	} else if *mode == "mcp" {
		token := os.Getenv("SCARLET_GATEWAY_DEPTH_MCP_TOKEN")
		if len(token) < 32 {
			log.Fatal("synthetic MCP authentication unavailable")
		}
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("MCP-Protocol-Version", "2026-07-28")
			if r.Method != "POST" || r.URL.Path != "/mcp" || r.URL.RawQuery != "" {
				w.WriteHeader(404)
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
				w.WriteHeader(401)
				return
			}
			var rpc struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
				Method  string          `json:"method"`
				Params  struct {
					Name      string            `json:"name"`
					Arguments map[string]string `json:"arguments"`
				} `json:"params"`
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&rpc) != nil || rpc.JSONRPC != "2.0" || rpc.Method != "tools/call" || !slices.Contains([]string{"read", "close"}, rpc.Params.Name) || rpc.Params.Arguments["ticketId"] != "ticket-17" {
				w.WriteHeader(400)
				return
			}
			log.Print("synthetic MCP tool handled")
			if *toolDelay > 0 {
				select {
				case <-time.After(*toolDelay):
				case <-r.Context().Done():
					return
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{"resultType": "complete", "content": []any{map[string]string{"type": "text", "text": "Synthetic ticket action completed"}}}})
		})
	} else if *mode == "provider" {
		image, audio := "", ""
		for path, dst := range map[string]*string{*imagePath: &image, *audioPath: &audio} {
			if path == "" {
				continue
			}
			raw, err := os.ReadFile(path)
			if err != nil || len(raw) > 4<<20 {
				log.Fatal("synthetic media fixture unavailable")
			}
			*dst = base64.StdEncoding.EncodeToString(raw)
		}
		token := os.Getenv("SCARLET_GATEWAY_PROVIDER_KEY")
		if len(token) < 32 {
			log.Fatal("synthetic provider authentication unavailable")
		}
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if r.Method == "GET" && r.URL.Path == "/healthz" {
				w.WriteHeader(204)
				return
			}
			if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.URL.RawQuery != "" {
				w.WriteHeader(404)
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
				w.WriteHeader(401)
				return
			}
			var body struct {
				Model    string            `json:"model"`
				Messages []json.RawMessage `json:"messages"`
				Stream   bool              `json:"stream"`
			}
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
			if d.Decode(&body) != nil || !slices.Contains(strings.Split(*models, ","), body.Model) || len(body.Messages) == 0 {
				w.WriteHeader(400)
				return
			}
			var extra any
			if d.Decode(&extra) != io.EOF {
				w.WriteHeader(400)
				return
			}
			log.Print("synthetic provider request handled")
			joined := string(bytesJoin(body.Messages))
			if strings.Contains(joined, "fixture:failure-503") && *providerID == "primary" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(503)
				io.WriteString(w, `{"error":{"message":"synthetic unavailability"}}`)
				return
			}
			if strings.Contains(joined, "fixture:timeout") {
				select {
				case <-time.After(12 * time.Second):
				case <-r.Context().Done():
					return
				}
			}
			answer := "Synthetic customer fixture response. No external model was called."
			imageOutput, audioOutput := false, false
			for _, message := range body.Messages {
				if strings.Contains(string(message), "fixture:email-output") {
					answer = "Synthetic fixture contact: demo@example.com"
				}
				imageOutput = imageOutput || strings.Contains(string(message), "fixture:media-email-output")
				audioOutput = audioOutput || strings.Contains(string(message), "fixture:audio-output")
			}
			message := map[string]any{"role": "assistant", "content": answer}
			if imageOutput && image != "" {
				message["content"] = []any{map[string]string{"type": "text", "text": answer}, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64," + image}}}
			}
			if audioOutput && audio != "" {
				message["audio"] = map[string]any{"id": "synthetic-audio", "data": audio, "expires_at": 2000000000, "transcript": "Synthetic fixture only; actual ASR is required"}
			}
			usage := map[string]int{"prompt_tokens": 12, "completion_tokens": 10, "total_tokens": 22}
			id := fmt.Sprintf("synthetic-%d", time.Now().UnixNano())
			if body.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				chunk := func(delta any, finish any, final bool) {
					v := map[string]any{"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": body.Model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
					if final {
						v["usage"] = usage
					}
					b, _ := json.Marshal(v)
					fmt.Fprintf(w, "data: %s\n\n", b)
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
				}
				if strings.Contains(joined, "fixture:chunk-email") {
					chunk(map[string]string{"role": "assistant", "content": "person@"}, nil, false)
					chunk(map[string]string{"content": "example.com"}, nil, false)
				} else {
					chunk(map[string]string{"role": "assistant", "content": answer}, nil, false)
				}
				if strings.Contains(joined, "fixture:partial-stream") {
					return
				}
				chunk(map[string]string{}, "stop", true)
				io.WriteString(w, "data: [DONE]\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": body.Model, "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": "stop"}}, "usage": usage})
		})
	} else {
		log.Fatal("invalid synthetic fixture mode")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s := &http.Server{Addr: *addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ErrorLog: log.New(io.Discard, "", 0)}
	go func() {
		<-ctx.Done()
		bounded, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.Shutdown(bounded)
	}()
	if err := s.ListenAndServeTLS(*cert, *key); err != nil && err != http.ErrServerClosed {
		log.Fatal("synthetic fixture listener failed")
	}
}

func bytesJoin(parts []json.RawMessage) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
