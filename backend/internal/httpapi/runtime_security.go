package httpapi

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

const (
	gatewayRuntimePath = "/api/v1/runtime/gateway"
	otlpRuntimePath    = "/api/v1/runtime/otlp/v1/traces"
	runtimeTokenPrefix = "spr_"
)

func (s *server) runtimeSecurityRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/api-security/runtime"
	mux.HandleFunc("GET "+base+"/sources", s.workspace(false, s.runtimeSources))
	mux.HandleFunc("POST "+base+"/sources", s.workspace(true, s.createRuntimeSource))
	mux.HandleFunc("DELETE "+base+"/sources/{source}", s.workspace(true, s.revokeRuntimeSource))
	mux.HandleFunc("GET "+base+"/routes", s.workspace(false, s.runtimeRoutes))
	mux.HandleFunc("GET "+base+"/findings", s.workspace(false, s.runtimeFindings))
	mux.HandleFunc("POST "+gatewayRuntimePath, s.receiveRuntime("gateway", "application/json", 512<<10, runtimesecurity.ParseGateway))
	mux.HandleFunc("POST "+otlpRuntimePath, s.receiveRuntime("otlp", "application/x-protobuf", runtimesecurity.MaxOTLPBytes, runtimesecurity.ParseOTLP))
	s.runtimeMonitorRoutes(mux)
}

func (s *server) runtimeSources(w http.ResponseWriter, r *http.Request, _ store.User, ws store.Workspace) {
	items, err := s.store.ListRuntimeSources(r.Context(), ws.ID)
	if err != nil {
		storeFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items})
}

func (s *server) runtimeRoutes(w http.ResponseWriter, r *http.Request, _ store.User, ws store.Workspace) {
	items, err := s.store.RuntimePosture(r.Context(), ws.ID)
	if err != nil {
		storeFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items})
}

func (s *server) runtimeFindings(w http.ResponseWriter, r *http.Request, _ store.User, ws store.Workspace) {
	items, err := s.store.ListRuntimeFindings(r.Context(), ws.ID)
	if err != nil {
		storeFailure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"items": items})
}

func runtimeSourceFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrForbidden):
		fail(w, 403, "只有工作区负责人可以管理运行时来源。")
	case errors.Is(err, store.ErrInvalid):
		fail(w, 400, "请核对来源名称、契约服务别名和来源类型。")
	case errors.Is(err, store.ErrRuntimeLimit):
		fail(w, 409, "工作区运行时来源数量已达上限。")
	default:
		storeFailure(w, err)
	}
}

func (s *server) createRuntimeSource(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	if ws.Role != "owner" {
		runtimeSourceFailure(w, store.ErrForbidden)
		return
	}
	var input struct {
		Name    string `json:"name"`
		Service string `json:"service"`
		Kind    string `json:"kind"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	random, err := sessionToken()
	if err != nil {
		storeFailure(w, err)
		return
	}
	token := runtimeTokenPrefix + random
	source, err := s.store.CreateRuntimeSource(r.Context(), ws.ID, user.ID, strings.TrimSpace(input.Name), strings.TrimSpace(input.Service), input.Kind, sessionHash(token))
	if err != nil {
		runtimeSourceFailure(w, err)
		return
	}
	respond(w, 201, map[string]any{"source": source, "token": token})
}

func (s *server) revokeRuntimeSource(w http.ResponseWriter, r *http.Request, user store.User, ws store.Workspace) {
	if ws.Role != "owner" {
		runtimeSourceFailure(w, store.ErrForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("source"), 10, 64)
	if err != nil || id < 1 {
		fail(w, 404, "找不到此运行时来源。")
		return
	}
	if err = s.store.RevokeRuntimeSource(r.Context(), ws.ID, user.ID, id); err != nil {
		runtimeSourceFailure(w, err)
		return
	}
	w.WriteHeader(204)
}

func runtimeDigest(header string) string {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || !strings.HasPrefix(token, runtimeTokenPrefix) {
		return ""
	}
	if len(token) != len(runtimeTokenPrefix)+43 {
		return ""
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(token, runtimeTokenPrefix))
	if err != nil || len(decoded) != 32 {
		return ""
	}
	return sessionHash(token)
}

func runtimeProtocolError(w http.ResponseWriter, otlp bool, status int, message string) {
	if !otlp {
		fail(w, status, message)
		return
	}
	// OTLP HTTP errors use the google.rpc.Status wire fields for code and message.
	codes := map[int]uint64{400: 3, 401: 16, 413: 8, 415: 3, 429: 8, 503: 14}
	body := protowire.AppendTag(nil, 1, protowire.VarintType)
	body = protowire.AppendVarint(body, codes[status])
	body = protowire.AppendTag(body, 2, protowire.BytesType)
	body = protowire.AppendString(body, message)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (s *server) receiveRuntime(kind, mediaType string, limit int64, parse func([]byte) ([]runtimesecurity.Observation, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		otlp := kind == "otlp"
		bad := func(status int, msg string) { runtimeProtocolError(w, otlp, status, msg) }
		token := runtimeDigest(r.Header.Get("Authorization"))
		unauthorized := func() {
			w.Header().Set("WWW-Authenticate", "Bearer")
			bad(401, "运行时来源令牌无效。")
		}
		if token == "" {
			unauthorized()
			return
		}
		select {
		case s.runtimeSlots <- struct{}{}:
			defer func() { <-s.runtimeSlots }()
		default:
			w.Header().Set("Retry-After", "2")
			bad(429, "采集接收繁忙，请稍后重试。")
			return
		}
		if err := s.store.AuthorizeRuntimeSource(r.Context(), kind, token); err != nil {
			if errors.Is(err, store.ErrAutomationUnauthorized) {
				unauthorized()
			} else {
				bad(503, "暂时无法核验采集令牌。")
			}
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != mediaType {
			bad(415, "此来源不支持所提交的数据格式。")
			return
		}
		if r.ContentLength > limit {
			bad(413, "采集批次超过大小限制。")
			return
		}
		body := http.MaxBytesReader(w, r.Body, limit)
		defer body.Close()
		var reader io.Reader = body
		switch r.Header.Get("Content-Encoding") {
		case "", "identity":
		case "gzip":
			gzipReader, err := gzip.NewReader(body)
			if err != nil {
				bad(400, "采集批次压缩格式无效。")
				return
			}
			defer gzipReader.Close()
			reader = gzipReader
		default:
			bad(415, "不支持所提交的压缩格式。")
			return
		}
		raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
		var tooLarge *http.MaxBytesError
		if int64(len(raw)) > limit || errors.As(err, &tooLarge) {
			bad(413, "采集批次超过大小限制。")
			return
		}
		if err != nil {
			bad(400, "无法读取采集批次。")
			return
		}
		observations, err := parse(raw)
		if errors.Is(err, runtimesecurity.ErrOTLPTooLarge) {
			bad(413, "采集批次超过大小限制。")
			return
		}
		if err != nil {
			bad(400, "采集批次格式或字段无效。")
			return
		}
		identity := bytes.TrimSpace(raw)
		if otlp {
			// Idempotence uses the permitted projection, never discarded URL or SQL.
			identity, _ = json.Marshal(observations)
		}
		digest := sha256.Sum256(identity)
		receipt, err := s.store.DeliverRuntimeBatch(r.Context(), kind, token, hex.EncodeToString(digest[:]), observations)
		switch {
		case errors.Is(err, store.ErrAutomationUnauthorized):
			unauthorized()
		case errors.Is(err, store.ErrInvalid):
			bad(400, "请检查观测时间和路由模板。")
		case errors.Is(err, store.ErrRuntimeLimit):
			bad(413, "来源已达到路由或批次容量限制。")
		case err != nil:
			bad(503, "暂时无法写入运行时批次。")
		case otlp:
			w.Header().Set("Content-Type", mediaType)
			w.WriteHeader(200)
		default:
			respond(w, 200, receipt)
		}
	}
}
