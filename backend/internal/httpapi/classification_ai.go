package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationai"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func classificationAIFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrForbidden):
		fail(w, 403, "当前身份不能配置或调用所选模型连接。")
	case errors.Is(err, store.ErrConflict):
		fail(w, 409, "模型设置或所选资产已变化，请刷新后重试。")
	case errors.Is(err, store.ErrInvalid):
		fail(w, 400, "请核对模型连接、识别模式和所选资产版本。")
	default:
		classificationWorkbenchFailure(w, err)
	}
}

func (s *server) classificationAIRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/classification"
	mux.HandleFunc("GET "+base+"/engine", s.workspace(false, s.classificationEngine))
	mux.HandleFunc("PUT "+base+"/engine", s.workspace(false, s.saveClassificationEngine))
	mux.HandleFunc("GET "+base+"/ai/profiles", s.workspace(false, s.classificationAIProfiles))
	mux.HandleFunc("POST "+base+"/ai/profiles", s.workspace(false, s.saveClassificationAIProfile))
	mux.HandleFunc("PUT "+base+"/ai/profiles/{id}", s.workspace(false, s.saveClassificationAIProfile))
	mux.HandleFunc("POST "+base+"/ai/profiles/{id}/test", s.workspace(false, s.testClassificationAIProfile))
	mux.HandleFunc("GET "+base+"/ai/input", s.workspace(false, s.classificationAIInput))
	mux.HandleFunc("GET "+base+"/ai/suggestions", s.workspace(false, s.classificationAISuggestions))
	mux.HandleFunc("POST "+base+"/ai/suggestions", s.workspace(false, s.createClassificationAISuggestion))
}

func (s *server) classificationEngine(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	settings, err := s.store.GetClassificationEngineSettings(r.Context(), ws.ID, u.ID)
	if err != nil {
		classificationAIFailure(w, err)
		return
	}
	profiles, err := s.store.ListClassificationAIProfiles(r.Context(), ws.ID, u.ID)
	if err != nil {
		classificationAIFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"local": map[string]any{"version": "scarlet-local-v2", "ruleKinds": []string{"keyword", "regexp", "dictionary"}, "formats": []string{"email", "phone", "national_id", "financial_account"}, "autoLegalClassification": false}, "settings": settings, "profiles": profiles, "encryptionReady": s.classificationAI != nil && s.classificationAI.ReadyEncryption()})
}
func (s *server) saveClassificationEngine(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var input store.ClassificationEngineSettingsInput
	if !readJSONLimit(w, r, &input, 4<<10) {
		return
	}
	settings, err := s.store.SaveClassificationEngineSettings(r.Context(), ws.ID, u.ID, input)
	if err != nil {
		classificationAIFailure(w, err)
		return
	}
	respond(w, 200, settings)
}
func (s *server) classificationAIProfiles(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	items, err := s.store.ListClassificationAIProfiles(r.Context(), ws.ID, u.ID)
	if err != nil {
		classificationAIFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items})
}
func (s *server) saveClassificationAIProfile(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	if ws.Role != "owner" {
		classificationAIFailure(w, store.ErrForbidden)
		return
	}
	var payload struct {
		Name          string  `json:"name"`
		BaseURL       string  `json:"baseUrl"`
		Protocol      string  `json:"protocol"`
		Model         string  `json:"model"`
		Enabled       bool    `json:"enabled"`
		AllowMetadata bool    `json:"allowMetadata"`
		Revision      int64   `json:"revision"`
		APIKey        *string `json:"apiKey"`
		ClearAPIKey   bool    `json:"clearApiKey"`
	}
	if !readJSONLimit(w, r, &payload, 16<<10) {
		return
	}
	base, err := classificationai.NormalizeBaseURL(payload.BaseURL)
	if err != nil {
		fail(w, 400, "请填写不含账号、查询参数和片段的 HTTPS 模型 API 基础地址。")
		return
	}
	var id int64
	if r.Method == http.MethodPut {
		var ok bool
		id, ok = discoveryID(w, r)
		if !ok {
			return
		}
	}
	input := store.ClassificationAIProfileInput{Name: payload.Name, BaseURL: base, Protocol: payload.Protocol, Model: payload.Model, Enabled: payload.Enabled, AllowMetadata: payload.AllowMetadata, Revision: payload.Revision}
	if payload.APIKey != nil && *payload.APIKey != "" {
		secret := *payload.APIKey
		if payload.ClearAPIKey || len(secret) > 4096 || !utf8.ValidString(secret) || strings.IndexFunc(secret, func(c rune) bool { return c < 33 || c > 126 }) >= 0 {
			fail(w, 400, "请填写 API 密钥本身；不能同时替换与清除密钥。")
			return
		}
		if s.classificationAI == nil || !s.classificationAI.ReadyEncryption() {
			fail(w, 503, "服务尚未配置模型密钥加密能力，请由管理员配置后再保存密钥。")
			return
		}
		input.EncryptedAPIKey, err = s.classificationAI.Encrypt(ws.ID, base, secret)
		if err != nil {
			fail(w, 503, "暂时无法安全保存模型密钥。")
			return
		}
		input.ReplaceAPIKey = true
	} else if payload.ClearAPIKey {
		input.ReplaceAPIKey = true
	}
	profile, err := s.store.SaveClassificationAIProfile(r.Context(), ws.ID, u.ID, id, input)
	if err != nil {
		classificationAIFailure(w, err)
		return
	}
	status := 200
	if r.Method == http.MethodPost {
		status = 201
	}
	respond(w, status, profile)
}

func (s *server) classificationRuntimeProfile(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace, id int64) (store.ClassificationAIProfile, classificationai.Profile, bool) {
	profile, err := s.store.GetClassificationAIProfileSecret(r.Context(), ws.ID, u.ID, id)
	if err != nil {
		classificationAIFailure(w, err)
		return profile, classificationai.Profile{}, false
	}
	if s.classificationAI == nil {
		fail(w, 503, "模型连接尚未在当前服务实例启用。")
		return profile, classificationai.Profile{}, false
	}
	secret := ""
	if len(profile.EncryptedAPIKey) > 0 {
		secret, err = s.classificationAI.Decrypt(ws.ID, profile.BaseURL, profile.EncryptedAPIKey)
		if err != nil {
			fail(w, 503, "模型密钥不可用，请由负责人重新保存连接密钥。")
			return profile, classificationai.Profile{}, false
		}
	}
	return profile, classificationai.Profile{BaseURL: profile.BaseURL, Protocol: profile.Protocol, Model: profile.Model, APIKey: secret}, true
}
func (s *server) classificationAISlot(w http.ResponseWriter) (func(), bool) {
	select {
	case s.runtimeSlots <- struct{}{}:
		return func() { <-s.runtimeSlots }, true
	default:
		fail(w, 429, "模型辅助任务繁忙，请稍后重试。")
		return nil, false
	}
}
func (s *server) testClassificationAIProfile(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	if ws.Role != "owner" {
		classificationAIFailure(w, store.ErrForbidden)
		return
	}
	id, ok := discoveryID(w, r)
	if !ok {
		return
	}
	_, profile, ok := s.classificationRuntimeProfile(w, r, u, ws, id)
	if !ok {
		return
	}
	release, ok := s.classificationAISlot(w)
	if !ok {
		return
	}
	defer release()
	models, err := s.classificationAI.Models(r.Context(), profile)
	if err != nil {
		fail(w, 502, "模型列表接口未通过测试，请核对协议、认证、HTTPS 及管理员允许的主机；仍可手动填写模型名称。")
		return
	}
	respond(w, 200, map[string]any{"models": models})
}

func classificationAIMetadata(input store.ClassificationAIMetadata) classificationai.Metadata {
	fields := make([]classificationai.MetadataField, 0, len(input.Fields))
	for _, field := range input.Fields {
		fields = append(fields, classificationai.MetadataField{Name: field.Name, Type: field.Type})
	}
	return classificationai.Metadata{AssetName: input.Name, System: input.System, Kind: input.Kind, Fields: fields}
}
func (s *server) classificationAIInput(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	asset, ok := optionalQueryID(w, r, "assetId")
	if !ok {
		return
	}
	revision, parseErr := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	if asset < 1 || parseErr != nil || revision < 0 {
		fail(w, 400, "请选择实际资产及其当前分类修订。")
		return
	}
	input, err := s.store.ClassificationAIInput(r.Context(), ws.ID, u.ID, asset, revision)
	if err != nil {
		classificationAIFailure(w, err)
		return
	}
	metadata := classificationAIMetadata(input)
	if classificationai.ValidateMetadata(metadata) != nil {
		fail(w, 400, "所选元数据超出模型辅助的允许范围，请缩小范围。")
		return
	}
	respond(w, 200, map[string]any{"assetId": input.AssetID, "revision": input.Revision, "metadata": metadata})
}
func (s *server) classificationAISuggestions(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	asset, ok := optionalQueryID(w, r, "assetId")
	if !ok {
		return
	}
	if asset < 1 {
		fail(w, 400, "请选择实际资产。")
		return
	}
	items, err := s.store.ListClassificationAISuggestions(r.Context(), ws.ID, u.ID, asset)
	if err != nil {
		classificationAIFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items})
}
func (s *server) createClassificationAISuggestion(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	if ws.Role != "owner" && ws.Role != "operator" {
		classificationAIFailure(w, store.ErrForbidden)
		return
	}
	var payload struct {
		AssetID   int64  `json:"assetId"`
		Revision  *int64 `json:"revision"`
		ProfileID int64  `json:"profileId"`
	}
	if !readJSONLimit(w, r, &payload, 4<<10) {
		return
	}
	if payload.AssetID < 1 || payload.Revision == nil || *payload.Revision < 0 || payload.ProfileID < 0 {
		classificationAIFailure(w, store.ErrInvalid)
		return
	}
	// Local mode is checked before loading credentials or making any request.
	settings, err := s.store.GetClassificationEngineSettings(r.Context(), ws.ID, u.ID)
	if err != nil {
		classificationAIFailure(w, err)
		return
	}
	if settings.Mode != "hybrid" {
		fail(w, 409, "当前使用本地识别引擎；需由负责人明确启用 AI 辅助。")
		return
	}
	if payload.ProfileID == 0 && settings.DefaultProfileID != nil {
		payload.ProfileID = *settings.DefaultProfileID
	}
	stored, profile, ok := s.classificationRuntimeProfile(w, r, u, ws, payload.ProfileID)
	if !ok {
		return
	}
	if !stored.Enabled || !stored.AllowMetadata {
		fail(w, 403, "该连接未启用或尚未授权发送所选元数据。")
		return
	}
	input, err := s.store.ClassificationAIInput(r.Context(), ws.ID, u.ID, payload.AssetID, *payload.Revision)
	if err != nil {
		classificationAIFailure(w, err)
		return
	}
	release, ok := s.classificationAISlot(w)
	if !ok {
		return
	}
	defer release()
	candidate, err := s.classificationAI.Suggest(r.Context(), profile, classificationAIMetadata(input))
	if err != nil {
		fail(w, 502, "模型未返回可用的分类参考结果，请核对模型、协议与访问范围。本地分类结果保持原状。")
		return
	}
	result, err := s.store.SaveClassificationAISuggestion(r.Context(), ws.ID, u.ID, store.ClassificationAISuggestionInput{AssetID: input.AssetID, AssetRevision: input.Revision, ProfileID: stored.ID, ProfileRevision: stored.Revision, Model: stored.Model, Category: candidate.Category, BusinessLevel: candidate.BusinessLevel, Rationale: candidate.Rationale})
	if err != nil {
		classificationAIFailure(w, err)
		return
	}
	respond(w, 201, result)
}
