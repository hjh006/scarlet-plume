package aisecurity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSourceSafetyOriginalHTTPProofCannotBeRepinned(t *testing.T) {
	var mode atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			WorkspaceSlug, ApplicationID, PrincipalID string
			DocumentIDs                               []string
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.WorkspaceSlug != "tenant-a" || in.ApplicationID != "app" || in.PrincipalID != "alice" || len(in.DocumentIDs) != 1 || in.DocumentIDs[0] != "doc" || r.Header.Get("Authorization") != "Bearer "+sourceTestToken {
			t.Error("source authorization was not bound to the selected identity")
		}
		content := "original synthetic source"
		if mode.Load() == 1 {
			content = "different synthetic source under the same version"
		}
		out := sourceTestReceipt(content)
		out["workspaceSlug"] = "tenant-a"
		doc := out["documents"].([]map[string]any)[0]
		if mode.Load() == 2 {
			doc["aclVersion"] = "acl2"
		}
		if mode.Load() == 3 {
			doc["authorized"] = false
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer source.Close()
	resolve, err := NewHTTPDocumentSourceForWorkspace(source.URL, sourceTestToken, "tenant-a", true)
	if err != nil {
		t.Fatal(err)
	}
	c := fixture()
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, "http://127.0.0.1:1", nil, nil)
	h.(*runtimeHandler).options.ResolveDocuments = resolve
	r := testRequest("model")
	r.ModelID = "enterprise"
	r.TaskID = "task-one"
	r.DocumentIDs = []string{"doc"}
	r.Input = `{"prompt":"summarize permitted source"}`
	r, err = ApplyTaskGrant(r, newTestTaskController().grant)
	if err != nil {
		t.Fatal(err)
	}
	r, err = h.(*runtimeHandler).prepareSources(t.Context(), r, c)
	if err != nil {
		t.Fatal(err)
	}
	if !h.(*runtimeHandler).sourcesCurrent(t.Context(), r, c) {
		t.Fatal("valid original HTTP source proof rejected")
	}
	proof := r.sourceEvidence[0]
	lease := TaskLeaseDigest(r)
	for _, changed := range []int32{1, 2, 3} {
		mode.Store(changed)
		if h.(*runtimeHandler).sourcesCurrent(t.Context(), r, c) {
			t.Fatal("source change or revocation accepted for the original request", changed)
		}
		if r.sourceEvidence[0] != proof || TaskLeaseDigest(r) != lease || !strings.Contains(r.sourceModelInput, "original synthetic source") || strings.Contains(r.sourceModelInput, "different synthetic") {
			t.Fatal("freshness check repinned original content or lease")
		}
	}
}
