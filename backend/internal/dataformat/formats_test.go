package dataformat

import (
	"slices"
	"strings"
	"testing"
)

func TestFormatChecksumsAndBounds(t *testing.T) {
	for value, category := range map[string]string{"11010519491231002X": "national_id", "11010519491231002x": "national_id", "4111111111111111": "financial_account", "13900000000": "phone", "a@example.test": "email"} {
		if !Matches(category, value) || !slices.Contains(Categories(value), category) {
			t.Errorf("format missing: %s", category)
		}
	}
	for value, category := range map[string]string{"110105194912310021": "national_id", "4111111111111112": "financial_account", "8888888888888888": "financial_account", "1": "financial_account", "": "financial_account"} {
		if Matches(category, value) {
			t.Errorf("invalid %s format accepted", category)
		}
	}
	if Matches("email", strings.Repeat("a", 244)+"@example.test") || Categories(strings.Repeat("a", 257)) != nil || Matches("unknown", "anything") {
		t.Fatal("format bound or category validation missing")
	}
}
