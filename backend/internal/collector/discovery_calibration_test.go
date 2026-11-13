package collector

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestRuleCalibrationUsesCollectionMatcher(t *testing.T) {
	positive, negative := true, false
	rule := DiscoveryRule{Key: "customer.department", Version: 2, Name: "部门合成词典", Kind: "dictionary", Target: "content", Category: "business", Dictionary: []string{"synthetic-operations", "synthetic-sales"}}
	samples := []RuleCalibrationSample{
		{Value: "synthetic-operations", Expected: &positive},
		{Value: "synthetic-sales", Expected: &negative},
		{Value: "synthetic-missing", Expected: &positive},
		{Value: "SYNTHETIC-OPERATIONS", Expected: &negative},
		{Value: "synthetic-operations"},
	}
	result, err := CalibrateDiscoveryRule(RuleCalibrationInput{Rule: rule, Samples: samples})
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 5 || result.Matched != 3 || result.TruePositive != 1 || result.FalsePositive != 1 || result.TrueNegative != 1 || result.FalseNegative != 1 {
		t.Fatalf("wrong calibration counts: %+v", result)
	}
	if result.Precision == nil || *result.Precision != 0.5 || result.Recall == nil || *result.Recall != 0.5 || result.Accuracy == nil || *result.Accuracy != 0.5 {
		t.Fatalf("wrong labeled-only metrics: %+v", result)
	}
	values := []string{}
	for index, sample := range samples {
		values = append(values, sample.Value)
		if result.Outcomes[index].Index != index || result.Outcomes[index].Matched != discoveryMatch(rule, sample.Value) {
			t.Fatal("calibration differs from collection matcher")
		}
	}
	field := DiscoveryField{Name: "department"}
	discoveryFieldHits(&field, []DiscoveryRule{prepareDiscoveryRule(rule)}, values)
	if len(field.Hits) != 1 || field.Hits[0].MatchCount != int64(result.Matched) || field.Hits[0].ScannedCount != int64(result.Scanned) {
		t.Fatalf("collection and calibration statistics differ: %+v", field.Hits)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "synthetic-operations") || strings.Contains(string(encoded), "customer.department") || !slices.Contains(result.Warnings, "synthetic_examples_only_not_production_accuracy") {
		t.Fatal("calibration exposed sample contents or omitted scope warning")
	}
}

func TestRuleCalibrationMetadataAndContentBounds(t *testing.T) {
	positive := true
	rule := DiscoveryRule{Key: "customer.contact", Version: 1, Name: "联系方式", Kind: "keyword", Target: "metadata", Category: "personal_information", Expression: "email,邮箱"}
	result, err := CalibrateDiscoveryRule(RuleCalibrationInput{Rule: rule, Samples: []RuleCalibrationSample{{Value: "customer_email 联系邮箱 varchar", Expected: &positive}, {Value: "customer_department 业务部门 varchar"}}})
	if err != nil || result.Scanned != 2 || result.Matched != 1 || result.TruePositive != 1 {
		t.Fatalf("metadata calibration failed: %+v %v", result, err)
	}
	rule.Kind, rule.Target, rule.Expression = "regexp", "content", "^a+$"
	result, err = CalibrateDiscoveryRule(RuleCalibrationInput{Rule: rule, Samples: []RuleCalibrationSample{{Value: strings.Repeat("a", 256)}, {Value: strings.Repeat("a", 257), Expected: &positive}, {Value: strings.Repeat("字", 100), Expected: &positive}}})
	if err != nil || result.Scanned != 1 || result.Matched != 1 || !result.Outcomes[1].Skipped || !result.Outcomes[2].Skipped || result.Recall != nil || result.Precision != nil || result.Accuracy != nil {
		t.Fatalf("content bounds or unlabeled metrics differ: %+v %v", result, err)
	}
	if !slices.Contains(result.Warnings, "content_values_over_256_bytes_not_scanned") {
		t.Fatal("skipped source sample limit missing")
	}
	for name, input := range map[string]RuleCalibrationInput{
		"empty":      {Rule: rule},
		"count":      {Rule: rule, Samples: make([]RuleCalibrationSample, 101)},
		"characters": {Rule: rule, Samples: []RuleCalibrationSample{{Value: strings.Repeat("字", 1001)}}},
		"utf8":       {Rule: rule, Samples: []RuleCalibrationSample{{Value: string([]byte{0xff})}}},
		"total":      {Rule: rule, Samples: make([]RuleCalibrationSample, 100)},
	} {
		if name == "total" {
			for index := range input.Samples {
				input.Samples[index].Value = strings.Repeat("字", 1000)
			}
		}
		if _, err := CalibrateDiscoveryRule(input); err == nil {
			t.Errorf("accepted invalid %s calibration", name)
		}
	}
	rule.Expression = "(a)\\1"
	if _, err := CalibrateDiscoveryRule(RuleCalibrationInput{Rule: rule, Samples: []RuleCalibrationSample{{Value: "aa"}}}); err == nil {
		t.Fatal("non-RE2 rule accepted")
	}
}

func TestDiscoveryBuiltinChecksumFormats(t *testing.T) {
	rules := BuiltinDiscoveryRules()
	for _, rule := range rules {
		if err := ValidateDiscoveryRule(rule); err != nil {
			t.Fatalf("invalid builtin %s: %v", rule.Key, err)
		}
	}
	field := DiscoveryField{Name: "unknown_value"}
	values := []string{"11010519491231002X", "110105194912310021", "4111111111111111", "4111111111111112", "8888888888888888"}
	discoveryFieldHits(&field, rules, values)
	found := map[string]bool{}
	for _, hit := range field.Hits {
		if hit.RuleKey != "builtin.national_id" && hit.RuleKey != "builtin.financial_account" {
			t.Fatalf("unexpected format %+v", hit)
		}
		if hit.MatchCount != 1 || hit.ScannedCount != 5 || hit.Method != "bounded_customer_sample" {
			t.Fatalf("invalid checksum accepted: %+v", hit)
		}
		found[hit.RuleKey] = true
		var rule DiscoveryRule
		for _, candidate := range rules {
			if candidate.Key == hit.RuleKey {
				rule = candidate
			}
		}
		samples := []RuleCalibrationSample{}
		for _, value := range values {
			samples = append(samples, RuleCalibrationSample{Value: value})
		}
		calibration, err := CalibrateDiscoveryRule(RuleCalibrationInput{Rule: rule, Samples: samples})
		if err != nil || calibration.Matched != 1 {
			t.Fatalf("builtin calibration omitted checksum: %+v %v", calibration, err)
		}
	}
	if !found["builtin.national_id"] || !found["builtin.financial_account"] {
		t.Fatalf("checksum formats missing: %+v", field.Hits)
	}
}
