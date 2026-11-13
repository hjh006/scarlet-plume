package httpapi

import (
	"github.com/hjh006/scarlet-plume/backend/internal/classificationai"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"net/http"
)

func (s *server) classificationIntelligenceRoutes(mux *http.ServeMux) {
	s.classificationDeliveryRoutes(mux)
	const base = "/api/v1/workspaces/{slug}/classification"
	mux.HandleFunc("GET "+base+"/knowledge", s.workspace(false, s.classificationKnowledge))
	mux.HandleFunc("POST "+base+"/knowledge", s.workspace(false, s.saveClassificationKnowledge))
	mux.HandleFunc("PUT "+base+"/knowledge/{id}", s.workspace(false, s.saveClassificationKnowledge))
	mux.HandleFunc("POST "+base+"/knowledge/search", s.workspace(false, s.searchClassificationKnowledge))
	mux.HandleFunc("POST "+base+"/knowledge/suggest", s.workspace(false, s.suggestClassificationKnowledge))
	mux.HandleFunc("GET "+base+"/training/models", s.workspace(false, s.classificationTrainingModels))
	mux.HandleFunc("POST "+base+"/training/models", s.workspace(false, s.trainClassificationModel))
	mux.HandleFunc("GET "+base+"/training/models/{id}", s.workspace(false, s.classificationTrainingModel))
	mux.HandleFunc("POST "+base+"/training/models/{id}/predict", s.workspace(false, s.predictClassificationModel))
}
func (s *server) classificationKnowledge(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	items, e := s.store.ClassificationKnowledge(r.Context(), ws.ID, u.ID)
	if e != nil {
		classificationAIFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"items": items})
}
func (s *server) saveClassificationKnowledge(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var input store.ClassificationKnowledgeInput
	if !readJSONLimit(w, r, &input, 64<<10) {
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
	out, e := s.store.SaveClassificationKnowledge(r.Context(), ws.ID, u.ID, id, input)
	if e != nil {
		classificationAIFailure(w, e)
		return
	}
	status := 200
	if id == 0 {
		status = 201
	}
	respond(w, status, out)
}

type classificationIntelligenceScope struct {
	AssetID  int64 `json:"assetId"`
	Revision int64 `json:"revision"`
}

func (s *server) searchClassificationKnowledge(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var input classificationIntelligenceScope
	if !readJSONLimit(w, r, &input, 4<<10) {
		return
	}
	refs, e := s.store.SearchClassificationKnowledge(r.Context(), ws.ID, u.ID, input.AssetID, input.Revision)
	if e != nil {
		classificationAIFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"assetId": input.AssetID, "revision": input.Revision, "references": refs, "engine": "local-lexical-v1"})
}
func (s *server) suggestClassificationKnowledge(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	if !checkIntelligenceEditor(ws) {
		classificationAIFailure(w, store.ErrForbidden)
		return
	}
	var input struct {
		classificationIntelligenceScope
		ProfileID      int64                                    `json:"profileId"`
		AllowKnowledge bool                                     `json:"allowKnowledge"`
		References     []store.ClassificationKnowledgeSelection `json:"references"`
	}
	if !readJSONLimit(w, r, &input, 8<<10) {
		return
	}
	if !input.AllowKnowledge {
		fail(w, 403, "请先预览知识片段并明确同意本次发送。")
		return
	}
	settings, e := s.store.GetClassificationEngineSettings(r.Context(), ws.ID, u.ID)
	if e != nil {
		classificationAIFailure(w, e)
		return
	}
	if settings.Mode != "hybrid" {
		fail(w, 409, "当前使用本地识别引擎，请由负责人启用模型辅助后再生成知识增强建议。")
		return
	}
	if input.ProfileID == 0 && settings.DefaultProfileID != nil {
		input.ProfileID = *settings.DefaultProfileID
	}
	stored, profile, ok := s.classificationRuntimeProfile(w, r, u, ws, input.ProfileID)
	if !ok {
		return
	}
	if !stored.Enabled || !stored.AllowMetadata {
		classificationAIFailure(w, store.ErrForbidden)
		return
	}
	metadata, e := s.store.ClassificationAIInput(r.Context(), ws.ID, u.ID, input.AssetID, input.Revision)
	if e != nil {
		classificationAIFailure(w, e)
		return
	}
	references, e := s.store.SelectClassificationKnowledge(r.Context(), ws.ID, u.ID, input.AssetID, input.Revision, input.References)
	if e != nil {
		classificationAIFailure(w, e)
		return
	}
	release, ok := s.classificationAISlot(w)
	if !ok {
		return
	}
	defer release()
	refs := make([]classificationai.Reference, 0, len(references))
	for _, v := range references {
		refs = append(refs, classificationai.Reference{Name: v.Name, SourceURL: v.SourceURL, Revision: v.Revision, Digest: v.Digest, Excerpt: v.Excerpt})
	}
	result, e := s.classificationAI.SuggestWithReferences(r.Context(), profile, classificationAIMetadata(metadata), refs)
	if e != nil {
		fail(w, 502, "模型未返回可用的知识增强建议，请核对所选连接与发送范围。")
		return
	}
	out, e := s.store.SaveClassificationAISuggestion(r.Context(), ws.ID, u.ID, store.ClassificationAISuggestionInput{AssetID: input.AssetID, AssetRevision: input.Revision, ProfileID: stored.ID, ProfileRevision: stored.Revision, Model: stored.Model, Category: result.Category, BusinessLevel: result.BusinessLevel, Rationale: result.Rationale, KnowledgeContext: references})
	if e != nil {
		classificationAIFailure(w, e)
		return
	}
	respond(w, 201, out)
}
func checkIntelligenceEditor(ws store.Workspace) bool {
	return ws.Role == "owner" || ws.Role == "operator"
}
func (s *server) classificationTrainingModels(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	items, e := s.store.ClassificationTrainingModels(r.Context(), ws.ID, u.ID)
	if e != nil {
		classificationAIFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"items": items})
}
func (s *server) trainClassificationModel(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	if !checkIntelligenceEditor(ws) {
		classificationAIFailure(w, store.ErrForbidden)
		return
	}
	var input store.ClassificationTrainingInput
	if !readJSONLimit(w, r, &input, 600<<10) {
		return
	}
	release, ok := s.classificationAISlot(w)
	if !ok {
		return
	}
	defer release()
	out, e := s.store.TrainClassificationModel(r.Context(), ws.ID, u.ID, input)
	if e != nil {
		failTrainingInput(w, e)
		return
	}
	respond(w, 201, out)
}
func failTrainingInput(w http.ResponseWriter, e error) {
	if e == store.ErrInvalid {
		fail(w, 400, "请提供名称、至少6条训练样本（至少两类、每类2条）及2条独立评估样本；文本不得重复，最多500条、每条1000字符，阈值0.5–0.99。")
		return
	}
	classificationAIFailure(w, e)
}
func (s *server) classificationTrainingModel(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := discoveryID(w, r)
	if !ok {
		return
	}
	out, e := s.store.ClassificationTrainingModel(r.Context(), ws.ID, u.ID, id)
	if e != nil {
		classificationAIFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) predictClassificationModel(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := discoveryID(w, r)
	if !ok {
		return
	}
	var input classificationIntelligenceScope
	if !readJSONLimit(w, r, &input, 4<<10) {
		return
	}
	out, e := s.store.PredictClassificationModel(r.Context(), ws.ID, u.ID, id, input.AssetID, input.Revision)
	if e != nil {
		classificationAIFailure(w, e)
		return
	}
	respond(w, 201, out)
}
