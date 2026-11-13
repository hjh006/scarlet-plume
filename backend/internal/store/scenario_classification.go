package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type ScenarioClassificationPublication struct {
	ID             int64      `json:"id"`
	Title          string     `json:"title"`
	SnapshotDigest string     `json:"snapshotDigest"`
	ReviewedAt     *time.Time `json:"reviewedAt,omitempty"`
	AssetRevision  int64      `json:"assetRevision"`
	Current        bool       `json:"current"`
}
type ScenarioClassification struct {
	Item              ClassificationWorkbenchItem        `json:"item"`
	ManuallyConfirmed bool                               `json:"manuallyConfirmed"`
	Usable            bool                               `json:"usable"`
	Trust             string                             `json:"trust"`
	Publication       *ScenarioClassificationPublication `json:"publication,omitempty"`
	Warnings          []string                           `json:"warnings"`
}

func scenarioClassificationLabel(value string) string {
	labels := map[string]string{"business": "业务数据", "personal": "个人信息", "sensitive_personal": "敏感个人信息", "internal": "内部", "confidential": "机密", "general": "一般数据", "important": "重要数据", "core": "核心数据", "unknown": "待确认"}
	if label := labels[value]; label != "" {
		return label
	}
	return value
}

func scenarioClassification(ctx context.Context, tx pgx.Tx, ws, assetID int64) (*ScenarioClassification, error) {
	items, e := readClassificationWorkbenchItems(ctx, tx, ws, []int64{assetID})
	if e != nil {
		return nil, e
	}
	if len(items) != 1 {
		return nil, ErrNotFound
	}
	item := items[0]
	out := &ScenarioClassification{Item: item, Trust: "unconfirmed", Warnings: []string{}}
	if item.Origin == "manual" && item.Revision > 0 {
		var current bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.asset_classification_history h JOIN plume.assets a ON a.id=h.asset_id AND a.workspace_id=h.workspace_id WHERE a.workspace_id=$1 AND a.id=$2 AND h.revision=a.classification_revision AND h.origin='manual' AND (h.category,h.business_level,h.statutory_category,h.basis)=(a.category,a.business_level,a.statutory_category,a.classification_basis) AND h.standard_reference IS NOT DISTINCT FROM a.classification_standard AND h.taxonomy_reference IS NOT DISTINCT FROM a.classification_taxonomy)`, ws, assetID).Scan(&current)
		if e != nil {
			return nil, e
		}
		out.ManuallyConfirmed = current
	}
	out.Usable = out.ManuallyConfirmed && item.Asset.Category != "unknown" && item.Asset.BusinessLevel != "unknown" && !item.NeedsReview
	if item.Catalog != nil && item.Catalog.SourceID != nil {
		sourceFacts, e := catalogAssessmentFacts(ctx, tx, ws, assetID)
		if e != nil {
			return nil, e
		}
		if !scenarioKnownFact(sourceFacts, "source_coverage") {
			out.Usable = false
			out.Warnings = append(out.Warnings, "目录来源的资产身份、当前运行或授权范围未验证，保留原记录并等待核对。")
		}
	}
	switch {
	case out.ManuallyConfirmed:
		out.Trust = "reviewed"
	case item.Origin == "manual" && strings.TrimSpace(item.Asset.ClassificationBasis) != "" && (item.Asset.Category != "unknown" || item.Asset.BusinessLevel != "unknown" || item.Asset.StatutoryCategory != "unknown"):
		out.Trust = "legacy_declared"
		out.Warnings = append(out.Warnings, "保留已有人工登记值和判定理由；缺少与当前值匹配的确认历史，请核对后确认，不能当作已复核结论。")
	case item.Origin == "automatic" || item.CandidateCount > 0:
		out.Trust = "candidate"
		out.Warnings = append(out.Warnings, "自动识别和AI建议只作候选，不代表人工确认或法定类别认定。")
	}
	if item.NeedsReview || item.PublicationState == "changed" {
		out.Warnings = append(out.Warnings, "来源、识别规则、分类修订、业务目录或发布采用范围已有变化，需重新核对当前依据。")
	}
	var p ScenarioClassificationPublication
	e = tx.QueryRow(ctx, `SELECT p.id,p.title,p.snapshot_digest,p.reviewed_at,i.revision FROM plume.classification_publications p JOIN plume.classification_publication_items i ON i.publication_id=p.id AND i.workspace_id=p.workspace_id WHERE p.workspace_id=$1 AND i.asset_id=$2 AND p.status='published' ORDER BY p.reviewed_at DESC,p.id DESC LIMIT 1`, ws, assetID).Scan(&p.ID, &p.Title, &p.SnapshotDigest, &p.ReviewedAt, &p.AssetRevision)
	if e != nil && e != pgx.ErrNoRows {
		return nil, e
	}
	if e == nil {
		p.Current = out.Usable && p.AssetRevision == item.Revision && item.PublicationState == "published"
		out.Publication = &p
	}
	if item.Asset.StatutoryCategory == "important" || item.Asset.StatutoryCategory == "core" {
		out.Warnings = append(out.Warnings, "重要/核心类别是已登记的判定值；主管部门认定、处理者身份、法律触发条件和适用手续仍需独立证据。")
	}
	return out, nil
}

func classificationAssessmentFacts(ctx context.Context, tx pgx.Tx, ws, assetID int64) ([]AssessmentFact, *ScenarioClassification, error) {
	c, e := scenarioClassification(ctx, tx, ws, assetID)
	if e != nil {
		return nil, nil, e
	}
	item := c.Item
	record := fmt.Sprintf("asset:%d/classification:%d", assetID, item.Revision)
	facts := []AssessmentFact{}
	add := func(key, value, source string, known bool) {
		state := "unknown"
		if known && value != "" && value != "unknown" {
			state = "known"
		} else {
			value = ""
		}
		facts = append(facts, AssessmentFact{Key: key, State: state, Value: value, Source: source, Record: record})
	}
	add("asset_category", item.Asset.Category, "manual_classification", c.Usable)
	add("asset_business_level", item.Asset.BusinessLevel, "manual_classification", c.Usable)
	add("asset_statutory_category", item.Asset.StatutoryCategory, "manual_classification_declared", c.Usable)
	add("classification_record", assessmentFactJSON(map[string]any{"assetId": assetID, "revision": item.Revision, "origin": item.Origin, "category": item.Asset.Category, "businessLevel": item.Asset.BusinessLevel, "statutoryCategory": item.Asset.StatutoryCategory, "basis": item.Asset.ClassificationBasis, "trust": c.Trust, "manuallyConfirmed": c.ManuallyConfirmed, "usable": c.Usable}), "classification_record", true)
	if item.Standard != nil {
		add("classification_standard", assessmentFactJSON(item.Standard), "classification_snapshot", c.Usable)
	} else {
		add("classification_standard", "", "classification_snapshot", false)
	}
	if item.Taxonomy != nil {
		add("classification_taxonomy", assessmentFactJSON(item.Taxonomy), "classification_snapshot", c.Usable && !item.TaxonomyStale)
	} else {
		add("classification_taxonomy", "", "classification_snapshot", false)
	}
	if c.Publication != nil {
		add("classification_publication", assessmentFactJSON(c.Publication), "classification_publication", true)
	} else {
		add("classification_publication", "", "classification_publication", false)
	}
	if c.Trust == "legacy_declared" {
		add("classification_legacy_record", assessmentFactJSON(item.Asset), "manual_legacy_declared", true)
	}
	if item.CandidateCount > 0 || item.Origin == "automatic" {
		add("classification_candidates", assessmentFactJSON(map[string]any{"candidateCount": item.CandidateCount, "suggestedCategory": item.SuggestedCategory, "suggestedBusinessLevel": item.SuggestedBusinessLevel, "automatic": automaticClassificationFacts(item.Automatic)}), "recognition_candidate", true)
	}
	summary := fmt.Sprintf("资产#%d，分类修订%d；数据属性：%s，内部级别：%s，法定类别登记值：%s；判定理由：%s。", assetID, item.Revision, scenarioClassificationLabel(item.Asset.Category), scenarioClassificationLabel(item.Asset.BusinessLevel), scenarioClassificationLabel(item.Asset.StatutoryCategory), item.Asset.ClassificationBasis)
	if item.Standard != nil {
		summary += fmt.Sprintf("采用标准：%s %s，条款%s，等级%s。", item.Standard.Code, item.Standard.Title, item.Standard.RuleClause, item.Standard.LevelLabel)
	} else {
		summary += "尚未登记采用标准。"
	}
	if item.Taxonomy != nil {
		summary += "业务目录：" + item.Taxonomy.Path + "。"
	}
	if c.Publication != nil {
		summary += fmt.Sprintf("发布#%d，采用分类修订%d，当前有效=%t。", c.Publication.ID, c.Publication.AssetRevision, c.Publication.Current)
	} else {
		summary += "尚未独立发布分类清单。"
	}
	summary += "人工分类记录与有限来源事实不替代业务适用条件、法定认定或外部审批。"
	add("classification_context", summary, "manual_classification_context", c.Usable)
	return facts, c, nil
}
