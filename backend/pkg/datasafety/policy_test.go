package datasafety

import "testing"

func TestPolicyJSONRejectsSilentProtectionTypos(t *testing.T) {
	for _, raw := range []string{`{"version":"v1","protectedCategoriez":["email"]}`, `{"version":"v1"} {}`, `null`, "{\"version\":\"\xff\"}"} {
		if _, err := ParsePolicy([]byte(raw)); err == nil {
			t.Fatal("unsafe policy accepted")
		}
	}
	if _, err := ParsePolicy([]byte(`{"version":"v1","protectedCategories":["email"],"allowedChannels":[],"approvedAiDomains":[],"blockUnapprovedAi":false}`)); err != nil {
		t.Fatal(err)
	}
}
