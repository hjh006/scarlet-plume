package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

const (
	maxRequestBytes = 512 * 1024
	sessionDuration = 8 * time.Hour
)

type server struct {
	store      *store.Store
	origin     string
	secure     bool
	loginSlots chan struct{}
}

func New(st *store.Store, origin string, secure bool) http.Handler {
	s := &server{store: st, origin: origin, secure: secure, loginSlots: make(chan struct{}, 4)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/session", s.getSession)
	mux.HandleFunc("POST /api/v1/session", s.signIn)
	mux.HandleFunc("DELETE /api/v1/session", s.signOut)
	mux.HandleFunc("GET /api/v1/workspaces/{slug}/overview", s.workspace(false, s.overview))
	mux.HandleFunc("GET /api/v1/workspaces/{slug}/assets", s.workspace(false, s.listAssets))
	mux.HandleFunc("POST /api/v1/workspaces/{slug}/assets", s.workspace(true, s.createAsset))
	mux.HandleFunc("GET /api/v1/workspaces/{slug}/integrations", s.workspace(false, s.listIntegrations))
	mux.HandleFunc("POST /api/v1/workspaces/{slug}/imports", s.workspace(true, s.importAssets))
	s.checkRoutes(mux)
	s.activityRoutes(mux)
	s.templateRoutes(mux)
	s.reportRoutes(mux)
	s.automationRoutes(mux)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, "找不到此接口。")
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet && r.Method != http.MethodHead &&
			r.Header.Get("Origin") != s.origin && !(r.Method == http.MethodPost && r.URL.Path == automationRunPath) {
			fail(w, http.StatusForbidden, "请求来源不受信任。")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}

func storeFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, "找不到此工作区或记录。")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, "记录或批次已存在，且内容发生冲突。")
	default:
		slog.Error("database operation failed", "timeout", errors.Is(err, context.DeadlineExceeded))
		fail(w, http.StatusServiceUnavailable, "暂时无法读取或保存，请稍后重试。")
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	return readJSONLimit(w, r, target, maxRequestBytes)
}

func readJSONLimit(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "请提交 JSON。")
		return false
	}
	if r.ContentLength > limit {
		fail(w, http.StatusRequestEntityTooLarge, "提交内容超过大小限制。")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(target)
	if err == nil {
		var extra json.RawMessage
		err = decoder.Decode(&extra)
		if err == io.EOF {
			return true
		}
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		fail(w, http.StatusRequestEntityTooLarge, "提交内容超过大小限制。")
	} else {
		fail(w, http.StatusBadRequest, "JSON 格式或字段无效。")
	}
	return false
}

func validText(value string, max int) bool {
	return strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= max &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func (s *server) cookieName() string {
	if s.secure {
		return "__Host-scarlet-session"
	}
	return "scarlet-session"
}

func (s *server) setSession(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(), Value: token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (s *server) currentUser(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	cookie, err := r.Cookie(s.cookieName())
	if err != nil || len(cookie.Value) != 43 {
		fail(w, http.StatusUnauthorized, "请先登录。")
		return store.User{}, false
	}
	user, err := s.store.SessionUser(r.Context(), sessionHash(cookie.Value))
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusUnauthorized, "请先登录。")
		return store.User{}, false
	}
	if err != nil {
		storeFailure(w, err)
		return store.User{}, false
	}
	return user, true
}

func (s *server) getSession(w http.ResponseWriter, r *http.Request) {
	user, ok := s.currentUser(w, r)
	if !ok {
		return
	}
	workspaces, err := s.store.ListWorkspaces(r.Context(), user.ID)
	if err != nil {
		storeFailure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"username": user.Username, "workspaces": workspaces})
}

func (s *server) signIn(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if !validText(input.Username, 150) || len(input.Password) == 0 || len(input.Password) > 128 {
		fail(w, http.StatusUnauthorized, "账号或密码不正确。")
		return
	}
	select {
	case s.loginSlots <- struct{}{}:
		defer func() { <-s.loginSlots }()
	default:
		fail(w, http.StatusTooManyRequests, "登录请求过多，请稍后重试。")
		return
	}
	user, err := s.store.FindUser(r.Context(), input.Username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		storeFailure(w, err)
		return
	}
	if !verifyPassword(input.Password, user.PasswordHash) || err != nil {
		fail(w, http.StatusUnauthorized, "账号或密码不正确。")
		return
	}
	token, err := sessionToken()
	if err != nil {
		storeFailure(w, err)
		return
	}
	if old, err := r.Cookie(s.cookieName()); err == nil && len(old.Value) == 43 {
		if err = s.store.DeleteSession(r.Context(), sessionHash(old.Value)); err != nil {
			storeFailure(w, err)
			return
		}
	}
	if err := s.store.CreateSession(r.Context(), user.ID, sessionHash(token), time.Now().Add(sessionDuration)); err != nil {
		storeFailure(w, err)
		return
	}
	s.setSession(w, token, int(sessionDuration.Seconds()))
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) signOut(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(s.cookieName()); err == nil && len(cookie.Value) == 43 {
		if err := s.store.DeleteSession(r.Context(), sessionHash(cookie.Value)); err != nil {
			storeFailure(w, err)
			return
		}
	}
	s.setSession(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

type workspaceAction func(http.ResponseWriter, *http.Request, store.User, store.Workspace)

func (s *server) workspace(write bool, action workspaceAction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := s.currentUser(w, r)
		if !ok {
			return
		}
		workspace, err := s.store.Membership(r.Context(), user.ID, r.PathValue("slug"))
		if err != nil {
			storeFailure(w, err)
			return
		}
		if write && workspace.Role != "owner" && workspace.Role != "operator" {
			fail(w, http.StatusForbidden, "当前账号无权执行此操作。")
			return
		}
		action(w, r, user, workspace)
	}
}
