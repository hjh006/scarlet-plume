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
	"strings"
	"syscall"
	"time"
)

func main() {
	mode := flag.String("mode", "provider", "provider or controller-proxy")
	addr := flag.String("listen", ":9444", "private HTTPS listener")
	cert := flag.String("cert", "/certs/tls.crt", "TLS certificate")
	key := flag.String("key", "/certs/tls.key", "TLS key")
	imagePath := flag.String("fixture-image", "", "optional synthetic output PNG fixture")
	audioPath := flag.String("fixture-audio", "", "optional synthetic output WAV fixture")
	flag.Parse()
	var handler http.Handler
	if *mode == "controller-proxy" {
		u, _ := url.Parse("http://controlplane:8080")
		proxy := httputil.NewSingleHostReverseProxy(u)
		proxy.Transport = &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 10, ResponseHeaderTimeout: 30 * time.Second}
		proxy.ErrorLog = log.New(io.Discard, "", 0)
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(502) }
		handler = proxy
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
			if d.Decode(&body) != nil || body.Model != "synthetic-media-model" || len(body.Messages) == 0 {
				w.WriteHeader(400)
				return
			}
			var extra any
			if d.Decode(&extra) != io.EOF {
				w.WriteHeader(400)
				return
			}
			log.Print("synthetic provider request handled")
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
				chunk(map[string]string{"role": "assistant", "content": answer}, nil, false)
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
