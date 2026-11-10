package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
)

type runResult struct {
	Provider   string                          `json:"provider"`
	ObservedAt time.Time                       `json:"observedAt"`
	RunID      string                          `json:"runId"`
	ErrorCode  string                          `json:"errorCode,omitempty"`
	Assets     json.RawMessage                 `json:"assets,omitempty"`
	Feishu     *collector.FeishuACLObservation `json:"feishu,omitempty"`
}

type deliveryClient struct {
	http     *http.Client
	endpoint string
	token    string
}

type deliveryRejected struct{ status int }

func (err *deliveryRejected) Error() string {
	return fmt.Sprintf("平台拒绝采集提交（HTTP %d），请核对任务范围和采集凭据", err.status)
}

func directHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func newDelivery(client *http.Client, origin, token string, allowLoopback bool) (deliveryClient, error) {
	invalidURL := errors.New("SCARLET_AGENT_URL 须为不含路径、参数或凭据的 HTTPS 地址")
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" ||
		u.ForceQuery || strings.Contains(origin, "#") || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return deliveryClient{}, invalidURL
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !allowLoopback || ip == nil || !ip.IsLoopback() {
			return deliveryClient{}, invalidURL
		}
	}
	if len(token) == 0 || len(token) > 1024 || strings.IndexFunc(token, func(r rune) bool {
		return r < 33 || r > 126
	}) >= 0 {
		return deliveryClient{}, errors.New("请通过 SCARLET_AGENT_TOKEN 设置任务令牌")
	}
	u.Path = "/api/v1/automation/runs"
	return deliveryClient{http: client, endpoint: u.String(), token: token}, nil
}

func (c deliveryClient) submit(ctx context.Context, result runResult) error {
	body, err := json.Marshal(result)
	if err != nil {
		return errors.New("无法生成采集结果")
	}
	var failure error
	for attempt := range 3 {
		if attempt > 0 && !wait(ctx, time.Duration(attempt)*time.Second) {
			return ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return errors.New("无法构造采集提交请求")
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")
		response, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failure = errors.New("无法送达采集结果，请检查平台网络与 TLS")
			continue
		}
		response.Body.Close()
		status := response.StatusCode
		switch {
		case status >= 200 && status < 300:
			return nil
		case status == 408 || status == 425 || status == 429 || status >= 500:
			failure = fmt.Errorf("平台暂时未接收采集结果（HTTP %d）", status)
		default:
			return &deliveryRejected{status: status}
		}
	}
	return failure
}
