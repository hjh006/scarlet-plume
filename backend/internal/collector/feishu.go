package collector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

const (
	feishuAPI      = "https://open.feishu.cn"
	maxFeishuBytes = 1024 * 1024
)

var (
	feishuDocTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	feishuOpenIDPattern   = regexp.MustCompile(`^ou_[A-Za-z0-9_-]{1,128}$`)
	feishuBearerPattern   = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)
)

// FeishuACLObservation reports one exact openid entry in the document's
// collaborator list. It does not establish the user's effective access.
type FeishuACLObservation struct {
	Version      int       `json:"version"`
	TargetDigest string    `json:"targetDigest"`
	ObservedAt   time.Time `json:"observedAt"`
	Present      bool      `json:"present"`
	Permission   string    `json:"permission,omitempty"`
}

type feishuMember struct {
	MemberType string `json:"member_type"`
	MemberID   string `json:"member_id"`
	Permission string `json:"perm"`
}

type feishuMemberResponse struct {
	Code *int `json:"code"`
	Data *struct {
		Items         []*feishuMember `json:"items"`
		HasMore       bool            `json:"has_more"`
		PageToken     string          `json:"page_token"`
		NextPageToken string          `json:"next_page_token"`
		Truncated     bool            `json:"truncated"`
		Total         *int            `json:"total"`
	} `json:"data"`
}

func FeishuTargetDigest(docToken, memberOpenID string) (string, error) {
	if !feishuDocTokenPattern.MatchString(docToken) || !feishuOpenIDPattern.MatchString(memberOpenID) {
		return "", errors.New("请提供新版飞书文档 token 和同一飞书应用下的协作者 open_id")
	}
	sum := sha256.Sum256([]byte("scarlet-plume:feishu-acl:v1\x00docx\x00" + docToken + "\x00" + memberOpenID))
	return hex.EncodeToString(sum[:]), nil
}

// FeishuACL uses a caller's short-lived Feishu token to read the collaborator
// list from the fixed China-region Open Platform endpoint.
func FeishuACL(ctx context.Context, accessToken, docToken, memberOpenID string) (FeishuACLObservation, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return observeFeishuACL(ctx, client, feishuAPI, accessToken, docToken, memberOpenID)
}

func observeFeishuACL(ctx context.Context, client *http.Client, origin, accessToken, docToken, memberOpenID string) (FeishuACLObservation, error) {
	var observation FeishuACLObservation
	digest, err := FeishuTargetDigest(docToken, memberOpenID)
	if err != nil {
		return observation, err
	}
	if len(accessToken) > 8192 || !feishuBearerPattern.MatchString(accessToken) {
		return observation, errors.New("请通过 SCARLET_FEISHU_ACCESS_TOKEN 提供短期飞书访问令牌")
	}
	endpoint := origin + "/open-apis/drive/v1/permissions/" + docToken + "/members?type=docx"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return observation, errors.New("无法构造飞书文档权限请求")
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return observation, errors.New("无法连接飞书权限接口，请检查网络和 TLS")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return observation, fmt.Errorf("飞书权限接口返回 HTTP %d，请检查应用和文档权限", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxFeishuBytes+1))
	if err != nil || len(body) > maxFeishuBytes {
		return observation, errors.New("飞书权限响应读取失败或超出大小限制")
	}
	var result feishuMemberResponse
	if err := json.Unmarshal(body, &result); err != nil || result.Code == nil {
		return observation, errors.New("飞书权限响应格式无效")
	}
	if *result.Code != 0 {
		return observation, fmt.Errorf("飞书权限接口返回错误码 %d，请检查令牌、应用和文档权限", *result.Code)
	}
	// The documented endpoint is unpaginated. Never infer absence from a
	// missing array or any explicit truncation signal.
	data := result.Data
	if data == nil || data.Items == nil || data.HasMore || data.PageToken != "" ||
		data.NextPageToken != "" || data.Truncated || (data.Total != nil && *data.Total != len(data.Items)) {
		return observation, errors.New("飞书权限响应缺少完整的协作者列表")
	}

	var permission string
	for _, member := range data.Items {
		if member == nil || member.MemberType == "" || member.MemberID == "" {
			return observation, errors.New("飞书协作者响应缺少成员类型或标识")
		}
		if member.MemberType != "openid" || member.MemberID != memberOpenID {
			continue
		}
		if permission != "" {
			return observation, errors.New("飞书权限响应包含重复的目标协作者记录")
		}
		switch member.Permission {
		case "view", "edit", "full_access":
			permission = member.Permission
		default:
			return observation, errors.New("飞书权限响应包含未知的目标协作者角色")
		}
	}
	return FeishuACLObservation{
		Version: 1, TargetDigest: digest, ObservedAt: time.Now().UTC(),
		Present: permission != "", Permission: permission,
	}, nil
}
