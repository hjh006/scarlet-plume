package collector

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testFeishuToken  = "u-testCredential"
	testFeishuDoc    = "doxcnTestDoc"
	testFeishuOpenID = "ou_testCollaborator"
)

func TestFeishuACLRetest(t *testing.T) {
	var revoked atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet ||
			r.URL.Path != "/open-apis/drive/v1/permissions/"+testFeishuDoc+"/members" ||
			r.URL.RawQuery != "type=docx" ||
			r.Header.Get("Authorization") != "Bearer "+testFeishuToken {
			t.Error("unexpected Feishu collaborator request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if revoked.Load() {
			// Other members remain. The collector verifies one exact openid.
			fmt.Fprint(w, `{"code":0,"data":{"items":[{"member_type":"openchat","member_id":"oc_group","perm":"view"}]}}`)
			return
		}
		fmt.Fprintf(w, `{"code":0,"data":{"items":[{"member_type":"openid","member_id":%q,"perm":"edit","name":"not exported","external_label":true}]}}`, testFeishuOpenID)
	}))
	defer server.Close()

	baseline, err := observeFeishuACL(t.Context(), server.Client(), server.URL, testFeishuToken, testFeishuDoc, testFeishuOpenID)
	if err != nil || !baseline.Present || baseline.Permission != "edit" || baseline.Version != 1 {
		t.Fatalf("unexpected baseline observation: %+v, %v", baseline, err)
	}

	revoked.Store(true)
	retest, err := observeFeishuACL(t.Context(), server.Client(), server.URL, testFeishuToken, testFeishuDoc, testFeishuOpenID)
	if err != nil || retest.Present || retest.Permission != "" {
		t.Fatalf("unexpected retest observation: %+v, %v", retest, err)
	}
	if len(baseline.TargetDigest) != 64 || baseline.TargetDigest != retest.TargetDigest ||
		retest.ObservedAt.Before(baseline.ObservedAt) || retest.ObservedAt.Location() != time.UTC {
		t.Fatal("the observations have invalid scope or time")
	}
	payload, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{testFeishuToken, testFeishuDoc, testFeishuOpenID, "not exported", "external_label"} {
		if strings.Contains(string(payload), private) {
			t.Fatal("observation exports private source data")
		}
	}
}

func TestFeishuACLRejectsUncertainAbsence(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 403, `{"code":0,"data":{"items":[]}}`},
		{"nonzero code", 200, `{"code":1063002,"msg":"invalid access token","data":{"items":[]}}`},
		{"missing code", 200, `{"data":{"items":[]}}`},
		{"missing members", 200, `{"code":0,"data":{}}`},
		{"null members", 200, `{"code":0,"data":{"items":null}}`},
		{"missing member ID", 200, `{"code":0,"data":{"items":[{"member_type":"openid","perm":"view"}]}}`},
		{"incomplete page", 200, `{"code":0,"data":{"items":[],"has_more":true,"page_token":"next"}}`},
		{"truncated list", 200, `{"code":0,"data":{"items":[],"truncated":true}}`},
		{"unseen entries", 200, `{"code":0,"data":{"items":[],"total":1}}`},
		{"oversized body", 200, strings.Repeat(" ", maxFeishuBytes) + `{"code":0,"data":{"items":[]}}`},
		{"unexpected grant", 200, fmt.Sprintf(`{"code":0,"data":{"items":[{"member_type":"openid","member_id":%q,"perm":"unknown"}]}}`, testFeishuOpenID)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			result, err := observeFeishuACL(t.Context(), server.Client(), server.URL, testFeishuToken, testFeishuDoc, testFeishuOpenID)
			if err == nil || result != (FeishuACLObservation{}) {
				t.Fatalf("failed API read produced an observation: %+v, %v", result, err)
			}
		})
	}
}

func TestFeishuTargetDigestValidatesScope(t *testing.T) {
	first, err := FeishuTargetDigest(testFeishuDoc, testFeishuOpenID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := FeishuTargetDigest(testFeishuDoc+"A", testFeishuOpenID)
	if err != nil || second == first {
		t.Fatal("a different document must have a different target digest")
	}
	third, err := FeishuTargetDigest(testFeishuDoc, testFeishuOpenID+"B")
	if err != nil || third == first {
		t.Fatal("a different collaborator must have a different target digest")
	}
	if _, err := FeishuTargetDigest("https://example.feishu.cn/docx/"+testFeishuDoc, testFeishuOpenID); err == nil {
		t.Fatal("accepted a URL instead of a token")
	}
	if _, err := FeishuTargetDigest(testFeishuDoc, "collaborator@example.invalid"); err == nil {
		t.Fatal("accepted an email instead of an open_id")
	}
}
