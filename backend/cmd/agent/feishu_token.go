package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
)

const feishuOAuthURL = "https://accounts.feishu.cn/oauth/v3/token"

type feishuAppToken struct {
	client    *http.Client
	id        string
	secret    string
	token     string
	refreshAt time.Time
}

func feishuCollector(client *http.Client, once bool) (collectSource, error) {
	docToken := os.Getenv("SCARLET_FEISHU_DOC_TOKEN")
	openID := os.Getenv("SCARLET_FEISHU_MEMBER_OPEN_ID")
	if _, err := collector.FeishuTargetDigest(docToken, openID); err != nil {
		return nil, err
	}
	staticToken := os.Getenv("SCARLET_FEISHU_ACCESS_TOKEN")
	id := os.Getenv("SCARLET_FEISHU_APP_ID")
	secret := os.Getenv("SCARLET_FEISHU_APP_SECRET")
	app := &feishuAppToken{client: client, id: id, secret: secret}
	switch {
	case staticToken != "" && (id != "" || secret != ""):
		return nil, errors.New("飞书任务只能选择应用凭据或短期访问令牌")
	case staticToken != "" && !once:
		return nil, errors.New("飞书定期采集需要应用凭据以自动续期，短期访问令牌仅用于 -once")
	case staticToken == "" && (id == "" || secret == ""):
		return nil, errors.New("飞书任务需要 SCARLET_FEISHU_APP_ID 和 SCARLET_FEISHU_APP_SECRET")
	}
	return func(ctx context.Context, result *runResult) error {
		token := staticToken
		if token == "" {
			var err error
			token, err = app.accessToken(ctx, feishuOAuthURL)
			if err != nil {
				return err
			}
		}
		observation, err := collector.FeishuACL(ctx, token, docToken, openID)
		if err != nil {
			return err
		}
		result.Feishu = &observation
		return nil
	}, nil
}

func (a *feishuAppToken) accessToken(ctx context.Context, endpoint string) (string, error) {
	started := time.Now()
	if a.token != "" && started.Before(a.refreshAt) {
		return a.token, nil
	}
	unavailable := errors.New("无法获取飞书应用访问令牌")
	form := url.Values{
		"grant_type": {"client_credentials"},
		"scope":      {"docs:permission.member:retrieve"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", unavailable
	}
	// OAuth client_secret_basic uses form-encoded credentials inside Basic auth.
	req.SetBasicAuth(url.QueryEscape(a.id), url.QueryEscape(a.secret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return "", unavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", unavailable
	}
	const limit = 16 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(body) > limit {
		return "", unavailable
	}
	var response struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &response); err != nil ||
		response.AccessToken == "" || len(response.AccessToken) > 8192 ||
		!strings.EqualFold(response.TokenType, "Bearer") || response.ExpiresIn <= 0 || response.ExpiresIn > 7200 {
		return "", unavailable
	}

	lifetime := time.Duration(response.ExpiresIn) * time.Second
	a.refreshAt = started.Add(lifetime - min(lifetime/10, time.Minute))
	a.token = response.AccessToken
	return a.token, nil
}
