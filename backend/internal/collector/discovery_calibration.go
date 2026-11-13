package collector

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

type RuleCalibrationSample struct {
	Value    string `json:"value"`
	Expected *bool  `json:"expected,omitempty"`
}

type RuleCalibrationInput struct {
	Rule    DiscoveryRule           `json:"rule"`
	Samples []RuleCalibrationSample `json:"samples"`
}

type RuleCalibrationOutcome struct {
	Index    int   `json:"index"`
	Matched  bool  `json:"matched"`
	Expected *bool `json:"expected,omitempty"`
	Skipped  bool  `json:"skipped,omitempty"`
}

type RuleCalibrationResult struct {
	Matched       int                      `json:"matched"`
	Scanned       int                      `json:"scanned"`
	TruePositive  int                      `json:"truePositive"`
	FalsePositive int                      `json:"falsePositive"`
	TrueNegative  int                      `json:"trueNegative"`
	FalseNegative int                      `json:"falseNegative"`
	Precision     *float64                 `json:"precision"`
	Recall        *float64                 `json:"recall"`
	Accuracy      *float64                 `json:"accuracy"`
	Outcomes      []RuleCalibrationOutcome `json:"outcomes"`
	Warnings      []string                 `json:"warnings"`
}

// CalibrateDiscoveryRule applies the collection matcher to synthetic examples.
// Metadata examples are the scoped name/comment/type text seen by the matcher.
// Results contain example indexes and statistics, never the submitted text.
func CalibrateDiscoveryRule(input RuleCalibrationInput) (RuleCalibrationResult, error) {
	if err := ValidateDiscoveryRule(input.Rule); err != nil {
		return RuleCalibrationResult{}, err
	}
	if len(input.Samples) == 0 || len(input.Samples) > 100 {
		return RuleCalibrationResult{}, errors.New("请提供1至100条合成测试样本")
	}
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) > 64<<10 {
		return RuleCalibrationResult{}, errors.New("规则和合成测试样本总计不能超过64KiB")
	}
	for _, sample := range input.Samples {
		if !utf8.ValidString(sample.Value) || utf8.RuneCountInString(sample.Value) > 1000 {
			return RuleCalibrationResult{}, errors.New("每条合成测试样本不能超过1000个字符")
		}
	}
	rule := prepareDiscoveryRule(input.Rule)
	result := RuleCalibrationResult{Outcomes: []RuleCalibrationOutcome{}, Warnings: []string{"synthetic_examples_only_not_production_accuracy"}}
	longContent := false
	for index, sample := range input.Samples {
		outcome := RuleCalibrationOutcome{Index: index, Expected: sample.Expected}
		if rule.Target == "content" && len(sample.Value) > 256 {
			outcome.Skipped = true
			longContent = true
			result.Outcomes = append(result.Outcomes, outcome)
			continue
		}
		result.Scanned++
		outcome.Matched = discoveryMatch(rule, sample.Value)
		if outcome.Matched {
			result.Matched++
		}
		if sample.Expected != nil {
			switch {
			case *sample.Expected && outcome.Matched:
				result.TruePositive++
			case *sample.Expected:
				result.FalseNegative++
			case outcome.Matched:
				result.FalsePositive++
			default:
				result.TrueNegative++
			}
		}
		result.Outcomes = append(result.Outcomes, outcome)
	}
	if longContent {
		result.Warnings = append(result.Warnings, "content_values_over_256_bytes_not_scanned")
	}
	ratio := func(numerator, denominator int) *float64 {
		if denominator == 0 {
			return nil
		}
		value := float64(numerator) / float64(denominator)
		return &value
	}
	result.Precision = ratio(result.TruePositive, result.TruePositive+result.FalsePositive)
	result.Recall = ratio(result.TruePositive, result.TruePositive+result.FalseNegative)
	result.Accuracy = ratio(result.TruePositive+result.TrueNegative, result.TruePositive+result.FalsePositive+result.TrueNegative+result.FalseNegative)
	return result, nil
}
