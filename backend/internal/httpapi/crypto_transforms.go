package httpapi

import (
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"net/http"
)

const CryptoTransformPath = "/api/v1/runtime/crypto-transforms"

func (s *server) cryptoTransformRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST "+CryptoTransformPath, s.receiveCryptoTransforms)
}
func (s *server) receiveCryptoTransforms(w http.ResponseWriter, r *http.Request) {
	token := runtimeDigest(r.Header.Get("Authorization"))
	if token == "" {
		fail(w, 401, "运行时来源令牌无效。")
		return
	}
	var input store.CryptoTransformInput
	if !readJSONLimit(w, r, &input, 128<<10) {
		return
	}
	count, e := s.store.RecordCryptoTransforms(r.Context(), token, input)
	if e != nil {
		runtimeSourceFailure(w, e)
		return
	}
	respond(w, 200, map[string]int{"accepted": count})
}
