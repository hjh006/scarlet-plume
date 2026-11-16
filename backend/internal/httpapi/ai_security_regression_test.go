package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestAIRegressionJSONRejectsAmbiguityAndExtraContent(t *testing.T) {
	valid, _ := json.Marshal(map[string]any{"pack": store.BaselineAISecurityRegressionPack()})
	for _, raw := range [][]byte{[]byte(`{"pack":{},"pack":{}}`), []byte(`{"pack":{},"request":{"prompt":"unregistered"}}`), append([]byte(`{"pack":{}}`), byte(0xff)), []byte(`{"pack":{}} {}`)} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		var target struct {
			Pack store.AISecurityRegressionPack `json:"pack"`
		}
		if readAIRegressionJSON(w, r, &target) || w.Code != 400 {
			t.Fatalf("ambiguous pack accepted: status %d", w.Code)
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", bytes.NewReader(valid))
	r.Header.Set("Content-Type", "application/json")
	var target struct {
		Pack store.AISecurityRegressionPack `json:"pack"`
	}
	if !readAIRegressionJSON(w, r, &target) || len(target.Pack.Cases) != len(store.BaselineAISecurityRegressionPack().Cases) {
		t.Fatal("valid baseline JSON rejected")
	}
}
