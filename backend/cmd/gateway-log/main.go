package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

const maxLineBytes = 4 << 10

func main() {
	origin := flag.String("url", "", "赤翎平台地址，默认读取 SCARLET_RUNTIME_URL")
	local := flag.Bool("local", false, "允许本地样例使用明文 HTTP")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "网关日志请通过 stdin 提供")
		os.Exit(1)
	}
	address := *origin
	if address == "" {
		address = os.Getenv("SCARLET_RUNTIME_URL")
	}
	endpoint, err := gatewayEndpoint(address, *local)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	token := os.Getenv("SCARLET_RUNTIME_GATEWAY_TOKEN")
	if len(token) == 0 || len(token) > 1024 || strings.IndexFunc(token, func(r rune) bool {
		return r < 33 || r > 126
	}) >= 0 {
		fmt.Fprintln(os.Stderr, "请通过 SCARLET_RUNTIME_GATEWAY_TOKEN 设置网关来源令牌")
		os.Exit(1)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopRead := context.AfterFunc(ctx, func() { _ = os.Stdin.Close() })
	defer stopRead()
	rejected, err := forward(ctx, client, endpoint, token, os.Stdin)
	if rejected > 0 {
		fmt.Fprintf(os.Stderr, "已跳过 %d 条不符合采集格式的网关日志\n", rejected)
	}
	if err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func gatewayEndpoint(origin string, local bool) (string, error) {
	invalid := errors.New("请通过 -url 或 SCARLET_RUNTIME_URL 设置不含路径、参数或凭据的 HTTPS 地址")
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" ||
		u.ForceQuery || strings.Contains(origin, "#") || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return "", invalid
	}
	if u.Scheme != "https" {
		host := u.Hostname()
		if u.Scheme != "http" || !local || (host != "app" && host != "localhost" && !net.ParseIP(host).IsLoopback()) {
			return "", invalid
		}
	}
	u.Path = "/api/v1/runtime/gateway"
	return u.String(), nil
}

func forward(ctx context.Context, client *http.Client, endpoint, token string, input io.Reader) (int, error) {
	reader := bufio.NewReaderSize(input, maxLineBytes+2)
	rejected := 0
	for ctx.Err() == nil {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			rejected++
			for errors.Is(err, bufio.ErrBufferFull) && ctx.Err() == nil {
				_, err = reader.ReadSlice('\n')
			}
		} else if err == nil || errors.Is(err, io.EOF) {
			raw := bytes.TrimRight(line, "\r\n")
			line = bytes.TrimSpace(raw)
			switch {
			case len(raw) > maxLineBytes:
				rejected++
			case len(line) > 0:
				body := make([]byte, 0, len(line)+2)
				body = append(body, '[')
				body = append(body, line...)
				body = append(body, ']')
				if observations, parseErr := runtimesecurity.ParseGateway(body); parseErr != nil || len(observations) != 1 {
					rejected++
				} else if err := postGateway(ctx, client, endpoint, token, body); err != nil {
					return rejected, err
				}
			}
		}
		switch {
		case errors.Is(err, io.EOF):
			return rejected, nil
		case err != nil:
			return rejected, errors.New("无法读取网关日志输入")
		}
	}
	return rejected, ctx.Err()
}

func postGateway(ctx context.Context, client *http.Client, endpoint, token string, body []byte) error {
	var failure error
	for attempt := range 3 {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return errors.New("无法构造网关日志提交请求")
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failure = errors.New("无法送达网关日志，请检查平台网络与 TLS")
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		status := response.StatusCode
		switch {
		case status >= 200 && status < 300:
			return nil
		case status == 408 || status == 425 || status == 429 || status >= 500 && status < 600:
			failure = fmt.Errorf("平台暂时未接收网关日志（HTTP %d）", status)
		default:
			return fmt.Errorf("平台拒绝网关日志（HTTP %d），请核对格式和来源令牌", status)
		}
	}
	return failure
}
