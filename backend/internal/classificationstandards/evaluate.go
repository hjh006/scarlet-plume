package classificationstandards

import (
	"fmt"
	"slices"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
)

type Fact struct {
	Kind     string `json:"kind"`
	Column   string `json:"column,omitempty"`
	Category string `json:"category,omitempty"`
	Source   string `json:"source"`
	Count    *int64 `json:"count,omitempty"`
	Note     string `json:"note,omitempty"`
}

type RuleEvaluation struct {
	Rule              Rule     `json:"rule"`
	Status            string   `json:"status"`
	Facts             []Fact   `json:"facts"`
	MissingConditions []string `json:"missingConditions"`
}

type Evaluation struct {
	Version           string           `json:"version"`
	Standard          Standard         `json:"standard"`
	Status            string           `json:"status"`
	AutoApplicable    bool             `json:"autoApplicable"`
	Category          string           `json:"category"`
	BusinessLevel     string           `json:"businessLevel"`
	LevelCode         string           `json:"levelCode,omitempty"`
	StatutoryCategory string           `json:"statutoryCategory,omitempty"`
	Rules             []RuleEvaluation `json:"rules"`
	Facts             []Fact           `json:"facts"`
	MissingConditions []string         `json:"missingConditions"`
}

// EvaluatePostgreSQL evaluates observed attributes, not unobserved statutory
// impact or person counts. Every provision of the selected standard remains
// visible, including those requiring evidence outside this connector's scope.
func EvaluatePostgreSQL(table collector.PostgreSQLTable, standardID string) (Evaluation, error) {
	standard, err := lookup(standardID)
	if err != nil {
		return Evaluation{}, err
	}
	if standard == nil || standard.Status != "effective" {
		return Evaluation{}, ErrSelection
	}
	copyStandard := *standard
	copyStandard.Levels = slices.Clone(standard.Levels)
	copyStandard.Rules = slices.Clone(standard.Rules)
	for i := range copyStandard.Rules {
		copyStandard.Rules[i].LevelCodes = slices.Clone(copyStandard.Rules[i].LevelCodes)
	}
	e := Evaluation{Version: "postgresql-rules-v1", Standard: copyStandard, Status: "insufficient_data", Category: "unknown", BusinessLevel: "unknown",
		Rules: []RuleEvaluation{}, Facts: []Fact{}, MissingConditions: []string{}}
	addCount := func(kind, source string, count int64, note string) {
		value := count
		e.Facts = append(e.Facts, Fact{Kind: kind, Source: source, Count: &value, Note: note})
	}
	addCount("column_count", "catalog", int64(len(table.Columns)), "字段数；不表示记录数或人数")
	if table.MetadataStatus == "complete" {
		addCount("constraint_count", "catalog", int64(len(table.Constraints)), "仅约束类型与列，不含表达式")
	}
	if table.EstimatedRows != nil && *table.EstimatedRows >= 0 {
		addCount("estimated_row_count", "catalog", *table.EstimatedRows, "优化器估计行数，可能陈旧；不是人数或准确总量")
	}
	if table.RowSecurity != nil {
		e.Facts = append(e.Facts, Fact{Kind: "row_security", Source: "catalog", Note: fmt.Sprintf("enabled=%t;forced=%t;active=%t", table.RowSecurity.Enabled, table.RowSecurity.Forced, table.RowSecurity.Active)})
	}
	metadataSignals := 0
	for _, column := range table.Columns {
		category := collector.PostgreSQLMetadataSensitivity(column.Name, column.Type, column.Comment)
		if category == "" {
			continue
		}
		source := "column_name"
		if collector.MetadataSensitivity(column.Name, column.Type) == "" {
			source = "column_comment"
		}
		if column.Type == "inet" || column.Type == "cidr" {
			source = "catalog"
		}
		e.Facts = append(e.Facts, Fact{Kind: "metadata_candidate", Column: column.Name, Category: category, Source: source, Note: "元数据候选，不是内容确认"})
		metadataSignals++
	}
	positive, sensitive := false, false
	analysis := table.ContentAnalysis
	if analysis != nil {
		warnings := slices.Clone(analysis.Warnings)
		slices.Sort(warnings)
		warnings = slices.Compact(warnings)
		e.Facts = append(e.Facts, Fact{Kind: "content_scope", Source: "source_aggregate", Note: analysis.Status + ";" + analysis.Method + ";" + strings.Join(warnings, ",")})
		if analysis.Status == "complete" || analysis.Status == "partial" {
			addCount("sampled_row_count", "source_aggregate", analysis.RowsScanned, "有界前缀样本内可见行数；不是去重人数，也不是随机样本或全库统计")
			for _, column := range analysis.Columns {
				if column.Status != "complete" || column.NonNullRows < 0 || column.NonNullRows > analysis.RowsScanned {
					continue
				}
				for _, match := range column.Matches {
					if match.Count <= 0 || match.Count > column.NonNullRows {
						continue
					}
					if !slices.Contains([]string{"email", "phone", "national_id", "financial_account"}, match.Category) {
						continue
					}
					count := match.Count
					e.Facts = append(e.Facts, Fact{Kind: "format_match", Column: column.Column, Category: match.Category, Source: "source_aggregate", Count: &count,
						Note: "命中格式的抽样行数；身份证含校验位、银行卡含Luhn；不核验真实主体、业务用途或去重人数"})
					positive = true
					if match.Category == "national_id" || match.Category == "financial_account" {
						sensitive = true
					}
				}
			}
		}
	}
	if positive {
		e.Status = "detected"
		e.Category = "personal"
		e.BusinessLevel = "internal"
		if sensitive {
			e.Category = "sensitive_personal"
			e.BusinessLevel = "confidential"
		}
		e.AutoApplicable = true
	} else if metadataSignals > 0 {
		e.Status = "review_required"
	} else if analysis != nil && analysis.Status == "complete" {
		e.Status = "no_signal"
	}
	if table.MetadataStatus != "complete" {
		e.MissingConditions = append(e.MissingConditions, "元数据采集完整性待核验")
	}
	if len(table.Warnings) > 0 {
		warnings := slices.Clone(table.Warnings)
		slices.Sort(warnings)
		e.MissingConditions = append(e.MissingConditions, "元数据范围缺口："+strings.Join(slices.Compact(warnings), ","))
	}
	if analysis == nil || analysis.Status == "unknown" {
		e.MissingConditions = append(e.MissingConditions, "缺少可用的源内内容聚合")
	}
	if analysis != nil && analysis.Status == "partial" {
		e.MissingConditions = append(e.MissingConditions, "内容观察范围有限，未覆盖数据仍未知")
	}
	if analysis != nil && len(analysis.Warnings) > 0 {
		warnings := slices.Clone(analysis.Warnings)
		slices.Sort(warnings)
		e.MissingConditions = append(e.MissingConditions, "内容范围缺口："+strings.Join(slices.Compact(warnings), ","))
	}
	e.MissingConditions = append(e.MissingConditions, "实际业务用途、数据主体和行业适用范围", "影响对象、危害程度及主管部门目录或认定依据", "精度、覆盖区域和可核验的去重人数，不能用估计行数或匹配行数代替")
	slices.SortFunc(e.Facts, func(a, b Fact) int {
		return strings.Compare(a.Kind+"\x00"+a.Column+"\x00"+a.Category+"\x00"+a.Source, b.Kind+"\x00"+b.Column+"\x00"+b.Category+"\x00"+b.Source)
	})
	for _, rule := range copyStandard.Rules {
		r := RuleEvaluation{Rule: rule, Status: "review_required", Facts: []Fact{}, MissingConditions: []string{"本条尚未实现完整自动判定；需采证：" + rule.Evidence}}
		if standardID == DefaultID && rule.ID == "industry-business-classification" || standardID == "gbt-45574-2025" && rule.ID == "sensitive-pi-identification" {
			for _, fact := range e.Facts {
				if fact.Kind == "metadata_candidate" || fact.Kind == "format_match" {
					r.Facts = append(r.Facts, fact)
				}
			}
			if positive && (standardID == DefaultID || sensitive) {
				r.Status = "detected"
			}
			r.MissingConditions = []string{"格式和元数据只支持数据属性信号，真实主体、用途及影响仍需复核", rule.Evidence}
		}
		e.Rules = append(e.Rules, r)
	}
	// A selected sector's grades and clauses never come from a different sector.
	if standardID != DefaultID && standardID != "gbt-45574-2025" {
		e.Status = "review_required"
		if len(copyStandard.Rules) == 0 {
			e.MissingConditions = append(e.MissingConditions, "所选标准仅有公开目录信息，正文规则尚未取得")
		}
	}
	return e, nil
}
