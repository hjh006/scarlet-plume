package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/managed"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) managedRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/automation/managed"
	mux.HandleFunc("GET "+base, s.workspace(false, s.managedStatus))
	mux.HandleFunc("POST "+base+"/test", s.workspace(false, s.testManagedConnection))
	mux.HandleFunc("POST "+base+"/jobs", s.workspace(false, s.createManagedJob))
	mux.HandleFunc("PUT "+base+"/jobs/{job}", s.workspace(false, s.updateManagedJob))
	mux.HandleFunc("POST "+base+"/jobs/{job}/run", s.workspace(false, s.runManagedJob))
}

func (s *server) managedStatus(w http.ResponseWriter, _ *http.Request, _ store.User, workspace store.Workspace) {
	hosts := []string{}
	if s.managed != nil && workspace.Role == "owner" {
		hosts = s.managed.AllowedHosts()
	}
	respond(w, http.StatusOK, map[string]any{"enabled": s.managed != nil, "allowedHosts": hosts})
}

func (s *server) canManagePostgres(w http.ResponseWriter, workspace store.Workspace) bool {
	if workspace.Role != "owner" {
		automationFailure(w, store.ErrForbidden)
		return false
	}
	if s.managed == nil {
		fail(w, http.StatusServiceUnavailable, "本实例尚未启用平台托管采集。")
		return false
	}
	return true
}

type managedConnectionInput struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Database string   `json:"database"`
	User     string   `json:"user"`
	Password string   `json:"password,omitempty"`
	Schemas  []string `json:"schemas"`
}

func (input managedConnectionInput) connection() managed.Connection {
	schemas := make([]string, 0, len(input.Schemas))
	for _, schema := range input.Schemas {
		schemas = append(schemas, strings.TrimSpace(schema))
	}
	return managed.Connection{
		Host: strings.ToLower(strings.TrimSpace(input.Host)), Port: input.Port,
		Database: strings.TrimSpace(input.Database), User: strings.TrimSpace(input.User), Schemas: schemas,
	}
}

func sameManagedIdentity(c managed.Connection, cfg *store.ManagedPostgresConfig) bool {
	return c.Host == cfg.Host && c.Port == cfg.Port && c.Database == cfg.Database && c.User == cfg.User
}

func (s *server) testManagedConnection(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	if !s.canManagePostgres(w, workspace) {
		return
	}
	var input struct {
		managedConnectionInput
		JobID *int64 `json:"jobId,omitempty"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	conn := input.connection()
	if err := s.managed.Validate(conn); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}

	var saved *store.ManagedPostgresConfig
	if input.JobID != nil {
		job, err := s.store.ManagedPostgresJob(r.Context(), workspace.ID, user.ID, *input.JobID)
		if err != nil {
			managedJobFailure(w, err)
			return
		}
		saved = job.Managed
	}

	var count int
	var err error
	if input.Password != "" {
		count, err = s.managed.Test(r.Context(), conn, input.Password)
	} else {
		if saved == nil || !sameManagedIdentity(conn, saved) {
			fail(w, http.StatusBadRequest, "请为当前连接填写数据库密码。")
			return
		}
		count, err = s.managed.TestSaved(r.Context(), conn, saved.EncryptedPassword)
	}
	if errors.Is(err, managed.ErrBusy) {
		fail(w, http.StatusTooManyRequests, "连接测试较多，请稍后重试。")
		return
	}
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "无法读取 PostgreSQL 表目录，请核对连接、密码、TLS 和 schema 访问权限。")
		return
	}
	respond(w, http.StatusOK, map[string]int{"tableCount": count})
}

type managedJobInput struct {
	managedConnectionInput
	Source          string `json:"source,omitempty"`
	IntervalMinutes int    `json:"intervalMinutes"`
	Enabled         bool   `json:"enabled"`
	Revision        int64  `json:"revision,omitempty"`
}

func (s *server) prepareManagedJob(w http.ResponseWriter, r *http.Request, input *managedJobInput) (store.ManagedPostgresInput, bool) {
	if !readJSON(w, r, input) {
		return store.ManagedPostgresInput{}, false
	}
	conn := input.connection()
	if err := s.managed.Validate(conn); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return store.ManagedPostgresInput{}, false
	}
	var encrypted []byte
	if input.Password != "" {
		var err error
		encrypted, err = s.managed.EncryptPassword(conn, input.Password)
		if err != nil {
			fail(w, http.StatusBadRequest, "请核对数据库密码长度。")
			return store.ManagedPostgresInput{}, false
		}
	}
	return store.ManagedPostgresInput{
		Host: conn.Host, Port: conn.Port, Database: conn.Database, User: conn.User, Schemas: conn.Schemas,
		EncryptedPassword: encrypted, IntervalMinutes: input.IntervalMinutes, Enabled: input.Enabled,
	}, true
}

func managedJobFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, "来源别名已被使用，或任务状态和配置已变更。请刷新后重试。")
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, "请核对来源别名、密码和采集频率。频率为 1 至 1,440 分钟，连接身份变更时需重新填写密码。")
	default:
		automationFailure(w, err)
	}
}

func (s *server) createManagedJob(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	if !s.canManagePostgres(w, workspace) {
		return
	}
	var body managedJobInput
	input, ok := s.prepareManagedJob(w, r, &body)
	if !ok {
		return
	}
	random, err := sessionToken()
	if err != nil {
		storeFailure(w, err)
		return
	}
	job, err := s.store.CreateManagedPostgresJob(r.Context(), workspace.ID, user.ID,
		strings.TrimSpace(body.Source), sessionHash(random), input)
	if err != nil {
		managedJobFailure(w, err)
		return
	}
	respond(w, http.StatusCreated, job)
}

func (s *server) updateManagedJob(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	if !s.canManagePostgres(w, workspace) {
		return
	}
	id, ok := automationJobID(w, r)
	if !ok {
		return
	}
	var body managedJobInput
	input, ok := s.prepareManagedJob(w, r, &body)
	if !ok {
		return
	}
	if strings.TrimSpace(body.Source) != "" {
		current, err := s.store.ManagedPostgresJob(r.Context(), workspace.ID, user.ID, id)
		if err != nil {
			managedJobFailure(w, err)
			return
		}
		if current.Source != strings.TrimSpace(body.Source) {
			fail(w, http.StatusBadRequest, "来源别名用于资产去重，发布后保持固定。")
			return
		}
	}
	job, err := s.store.UpdateManagedPostgresJob(r.Context(), workspace.ID, user.ID, id, body.Revision, input)
	if err != nil {
		managedJobFailure(w, err)
		return
	}
	respond(w, http.StatusOK, job)
}

func (s *server) runManagedJob(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	if !s.canManagePostgres(w, workspace) {
		return
	}
	id, ok := automationJobID(w, r)
	if !ok {
		return
	}
	if err := s.store.QueueManagedPostgresJob(r.Context(), workspace.ID, user.ID, id); err != nil {
		managedJobFailure(w, err)
		return
	}
	respond(w, http.StatusAccepted, map[string]bool{"queued": true})
}
