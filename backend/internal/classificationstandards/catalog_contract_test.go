package classificationstandards

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestClassificationReferenceMayRetainUnassessedNativeGrade(t *testing.T) {
	reference, err := Select(DefaultID, "", "industry-business-classification", "unknown")
	if err != nil || reference.LevelCode != "" || reference.StatutoryCategory != "" || reference.RuleEvidence == "" {
		t.Fatalf("attribute-only confirmation cannot preserve an unassessed grade with rule evidence: %+v, %v", reference, err)
	}
	for _, input := range []struct{ rule, category string }{
		{"industry-business-classification", "general"},
		{"ordinary-grading", "unknown"},
	} {
		if _, err := Select(DefaultID, "", input.rule, input.category); !errors.Is(err, ErrSelection) {
			t.Errorf("empty grade accepted with rule=%s, category=%s: %v", input.rule, input.category, err)
		}
	}
}

// Exercise the complete shipped rule library through the same selection and
// JSON projection used when saving an asset, without fixing a catalog count.
func TestEveryEffectiveRuleProducesCompleteSnapshot(t *testing.T) {
	catalog, err := List()
	if err != nil {
		t.Fatal(err)
	}
	for _, standard := range catalog {
		if standard.Status != "effective" {
			continue
		}
		for _, rule := range standard.Rules {
			t.Run(standard.ID+"/"+rule.ID, func(t *testing.T) {
				levelCode, category := "", "unknown"
				if len(standard.Levels) > 0 {
					i := slices.IndexFunc(standard.Levels, func(level Level) bool {
						return len(rule.LevelCodes) == 0 || slices.Contains(rule.LevelCodes, level.Code)
					})
					if i < 0 {
						t.Fatal("rule has no selectable native level")
					}
					levelCode = standard.Levels[i].Code
					if mapped := standard.Levels[i].StatutoryCategory; mapped != "" {
						category = mapped
					}
				}
				reference, err := Select(standard.ID, levelCode, rule.ID, category)
				if err != nil {
					t.Fatalf("published rule cannot be selected: %v", err)
				}
				encoded, err := json.Marshal(reference)
				if err != nil {
					t.Fatal(err)
				}
				var snapshot Reference
				if err := json.Unmarshal(encoded, &snapshot); err != nil {
					t.Fatal(err)
				}
				if snapshot.ID != standard.ID || snapshot.Code != standard.Code ||
					snapshot.RuleID != rule.ID || snapshot.RuleClause != rule.Clause ||
					snapshot.RuleCriterion != rule.Criterion || snapshot.RuleEvidence != rule.Evidence {
					t.Fatalf("serialized determination lost its source, rule or evidence requirements: %+v", snapshot)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(encoded, &fields); err != nil || len(fields["ruleEvidence"]) == 0 {
					t.Fatal("client-facing ruleEvidence is missing from the snapshot")
				}
			})
		}
	}
}

func TestInactiveCatalogVersionsCannotBecomeDefaultOrDetermination(t *testing.T) {
	catalog, err := List()
	if err != nil {
		t.Fatal(err)
	}
	for _, standard := range catalog {
		if standard.Status == "effective" {
			continue
		}
		t.Run(standard.ID, func(t *testing.T) {
			selectable, err := IsSelectable(standard.ID)
			if err != nil || selectable {
				t.Fatalf("inactive standard can become the workspace default: %v", err)
			}
			referenceOnly := standard.Status == "published" && standard.EffectiveOn == "" &&
				len(standard.Levels) == 0 && len(standard.Rules) == 0
			_, err = Select(standard.ID, "", "", "unknown")
			if referenceOnly {
				if err != nil {
					t.Fatalf("issued reference document is unavailable: %v", err)
				}
			} else if !errors.Is(err, ErrSelection) {
				t.Fatalf("future or superseded standard accepted for a new determination: %v", err)
			}
		})
	}
}
