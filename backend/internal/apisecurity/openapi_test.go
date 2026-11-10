package apisecurity

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAnalyzeContractProjection(t *testing.T) {
	var requests atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer external.Close()
	scan, err := Analyze(json.RawMessage(fmt.Sprintf(`{
	  "openapi":"3.1.1",
	  "security":[{"bearer":[]}],
	  "servers":[{"url":%q}],
	  "components":{
	    "securitySchemes":{"bearer":{"type":"http","scheme":"bearer"}},
	    "schemas":{"Customer":{"properties":{"contact":{"format":"email","example":"never-persist@example.invalid"}}}}
	  },
	  "paths":{
	    "/customers/{id}":{"get":{"security":[{},{"bearer":[]}],"responses":{"200":{"content":{"application/json":{"schema":{
	      "allOf":[{"type":"array","items":{"$ref":"#/components/schemas/Customer"}},{"$ref":%q}]
	    }}}}}}},
	    "/private":{"get":{}},
	    "/invalid":{"get":{"security":[{"unknown":[]}]}}
	  }
	}`, external.URL, external.URL+"/schema")))
	if err != nil {
		t.Fatal(err)
	}
	if scan.UnresolvedReferences != 1 || requests.Load() != 0 || len(scan.Operations) != 3 {
		t.Fatalf("unexpected coverage: %+v (network calls: %d)", scan, requests.Load())
	}
	customer := scan.Operations[0]
	if customer.Auth != "anonymous" || !slices.Contains(customer.SensitiveFields, SensitiveField{"response", "contact", "email"}) {
		t.Fatalf("customer contract did not expose the expected risk metadata: %+v", customer)
	}
	for _, code := range []string{"sensitive_response_auth", "object_authorization_review"} {
		if !slices.ContainsFunc(customer.Findings, func(f Finding) bool { return f.Code == code }) {
			t.Errorf("missing finding %s", code)
		}
	}
	if scan.Operations[1].Auth != "invalid" || scan.Operations[2].Auth != "required" || len(scan.Operations[2].Findings) != 0 {
		t.Fatalf("invalid or inherited security classified incorrectly: %+v", scan.Operations)
	}
	projection, err := json.Marshal(scan)
	if err != nil || strings.Contains(string(projection), "never-persist") || strings.Contains(string(projection), external.URL) {
		t.Fatal("projected facts retained raw schema content")
	}
}

func TestAnalyzeOperationLimit(t *testing.T) {
	paths := make(map[string]any)
	for index := range MaxOperations + 1 {
		paths[fmt.Sprintf("/route/%d", index)] = object{"get": object{}}
	}
	spec, err := json.Marshal(object{"openapi": "3.0.3", "paths": paths})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Analyze(spec); err == nil {
		t.Fatal("oversized contract was accepted")
	}
}
