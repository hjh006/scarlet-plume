package classificationstandards

import (
	"errors"
	"testing"
)

func TestBuiltinCatalogAndVersionSelection(t *testing.T) {
	catalog, err := List()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{DefaultID, "gbt-39725-2020", "education-jyt-0661-2025", "energy-grading-2026", "ydt-4981-2024"} {
		if active, err := IsSelectable(id); err != nil || !active {
			t.Fatalf("missing selectable standard %s: %v", id, err)
		}
	}
	if len(catalog) == 0 {
		t.Fatal("empty built-in classification catalog")
	}
	if _, err := Select("jrt-0158-2018", "", "", "unknown"); !errors.Is(err, ErrSelection) {
		t.Fatal("superseded standard accepted for a new determination")
	}
	reference, err := Select("health-grading-trial-2023", "", "", "unknown")
	if err != nil || reference.LevelCode != "" || reference.RuleID != "" {
		t.Fatalf("issued reference-only document unavailable: %v", err)
	}
	if _, err := Select(DefaultID, "core", "ordinary-grading", "core"); !errors.Is(err, ErrSelection) {
		t.Fatal("mismatched native level and rule was accepted")
	}
}
