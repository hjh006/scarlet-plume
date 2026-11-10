package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

const automationRunPath = "/api/v1/automation/runs"
const automationTokenPrefix = "spl_"

func (s *server) automationRoutes(mux *http.ServeMux) {
	const jobs = "/api/v1/workspaces/{slug}/automation/jobs"
	mux.HandleFunc("GET "+jobs, s.workspace(false, s.listAutomationJobs))
	mux.HandleFunc("POST "+jobs, s.workspace(false, s.createAutomationJob))
	mux.HandleFunc("DELETE "+jobs+"/{job}", s.workspace(false, s.revokeAutomationJob))
	mux.HandleFunc("GET "+jobs+"/{job}/runs", s.workspace(false, s.listAutomationRuns))
	mux.HandleFunc("POST "+automationRunPath, s.deliverAutomationRun)
}

func automationFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrAutomationUnauthorized):
		w.Header().Set("WWW-Authenticate", "Bearer")
		fail(w, http.StatusUnauthorized, "采集令牌无效或已撤销。")
	case errors.Is(err, store.ErrForbidden):
		fail(w, http.StatusForbidden, "只有工作区负责人可以管理采集任务。")
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, "请核对采集类型、数据源、检查目标和采集时间。")
	default:
		storeFailure(w, err)
	}
}

func (s *server) listAutomationJobs(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	jobs, err := s.store.ListAutomationJobs(r.Context(), workspace.ID, user.ID)
	if err != nil {
		automationFailure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"items": jobs})
}

func (s *server) createAutomationJob(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	if workspace.Role != "owner" {
		automationFailure(w, store.ErrForbidden)
		return
	}
	var input store.AutomationJobInput
	if !readJSON(w, r, &input) {
		return
	}
	input.Source = strings.TrimSpace(input.Source)
	random, err := sessionToken()
	if err != nil {
		storeFailure(w, err)
		return
	}
	token := automationTokenPrefix + random
	job, err := s.store.CreateAutomationJob(r.Context(), workspace.ID, user.ID, input, sessionHash(token))
	if err != nil {
		automationFailure(w, err)
		return
	}
	respond(w, http.StatusCreated, map[string]any{"job": job, "token": token})
}

func automationJobID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("job"), 10, 64)
	if err != nil || id < 1 {
		fail(w, http.StatusNotFound, "找不到此采集任务。")
		return 0, false
	}
	return id, true
}

func (s *server) revokeAutomationJob(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	id, ok := automationJobID(w, r)
	if !ok {
		return
	}
	if err := s.store.RevokeAutomationJob(r.Context(), workspace.ID, user.ID, id); err != nil {
		automationFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listAutomationRuns(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	id, ok := automationJobID(w, r)
	if !ok {
		return
	}
	runs, err := s.store.ListAutomationRuns(r.Context(), workspace.ID, user.ID, id)
	if err != nil {
		automationFailure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"items": runs})
}

type automationManifest struct {
	RunID      string          `json:"runId"`
	Provider   string          `json:"provider"`
	ObservedAt time.Time       `json:"observedAt"`
	ErrorCode  string          `json:"errorCode,omitempty"`
	Assets     *manifest       `json:"assets,omitempty"`
	Feishu     *feishuManifest `json:"feishu,omitempty"`
}

func (m automationManifest) delivery(now time.Time) (store.AutomationDelivery, error) {
	if !batchIDPattern.MatchString(m.RunID) || m.ObservedAt.IsZero() || m.ObservedAt.After(now.Add(5*time.Minute)) {
		return store.AutomationDelivery{}, store.ErrInvalid
	}
	m.ObservedAt = m.ObservedAt.UTC()
	input := store.AutomationDelivery{RunID: m.RunID, Provider: m.Provider, FailureCode: m.ErrorCode, ObservedAt: m.ObservedAt}
	switch {
	case m.ErrorCode != "":
		if m.Assets != nil || m.Feishu != nil {
			return store.AutomationDelivery{}, store.ErrInvalid
		}
	case availableProvider(m.Provider):
		if m.Assets == nil || m.Feishu != nil || m.Assets.Provider != m.Provider {
			return store.AutomationDelivery{}, store.ErrInvalid
		}
		if _, valid := m.Assets.digest(); !valid {
			return store.AutomationDelivery{}, store.ErrInvalid
		}
		input.Assets = m.Assets.Assets
		input.Source = m.Assets.Source
	case m.Provider == "feishu":
		if m.Assets != nil || m.Feishu == nil || !m.Feishu.ObservedAt.Equal(m.ObservedAt) {
			return store.AutomationDelivery{}, store.ErrInvalid
		}
		m.Feishu.ObservedAt = m.ObservedAt
		evidence, err := m.Feishu.evidence(now)
		if err != nil {
			return store.AutomationDelivery{}, err
		}
		evidence.SourceSystem = "飞书文档权限（采集任务）"
		evidence.Summary = strings.Replace(evidence.Summary, "上传清单", "采集任务", 1)
		input.Evidence = &evidence
	default:
		return store.AutomationDelivery{}, store.ErrInvalid
	}
	data, err := json.Marshal(m)
	if err != nil {
		return store.AutomationDelivery{}, store.ErrInvalid
	}
	sum := sha256.Sum256(data)
	input.Digest = hex.EncodeToString(sum[:])
	return input, nil
}

func (s *server) deliverAutomationRun(w http.ResponseWriter, r *http.Request) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || !strings.HasPrefix(token, automationTokenPrefix) ||
		len(token) != len(automationTokenPrefix)+43 {
		automationFailure(w, store.ErrAutomationUnauthorized)
		return
	}
	if _, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(token, automationTokenPrefix)); err != nil {
		automationFailure(w, store.ErrAutomationUnauthorized)
		return
	}
	var manifest automationManifest
	if !readJSONLimit(w, r, &manifest, maxRequestBytes+1024) {
		return
	}
	input, err := manifest.delivery(time.Now())
	if err != nil {
		automationFailure(w, err)
		return
	}
	run, err := s.store.DeliverAutomationRun(r.Context(), sessionHash(token), input)
	if err != nil {
		automationFailure(w, err)
		return
	}
	respond(w, http.StatusOK, run)
}
