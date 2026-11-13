package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

type ScenarioMaterialAction struct {
	Page  string `json:"page"`
	Label string `json:"label"`
}
type ScenarioMaterial struct {
	Key      string                 `json:"key"`
	Title    string                 `json:"title"`
	Status   string                 `json:"status"`
	Value    string                 `json:"value,omitempty"`
	FactKeys []string               `json:"factKeys"`
	Records  []string               `json:"records"`
	Guidance string                 `json:"guidance"`
	Action   ScenarioMaterialAction `json:"action"`
}
type ScenarioMaterialCounts struct {
	AutoFilled        int `json:"autoFilled"`
	NeedsConfirmation int `json:"needsConfirmation"`
	Missing           int `json:"missing"`
	NotApplicable     int `json:"notApplicable"`
}

var scenarioTaskIDs = map[string][]string{
	"personal":    {"inventory", "pia", "audit", "rights-retention"},
	"domestic":    {"applicability", "self-check", "important-data", "personal-audit", "inspection"},
	"crossborder": {"scope", "assessment", "procedures", "review"},
	// Existing scenes still use the shared source panel. Their task-specific
	// workflows remain in their existing specialist tools.
	"governance": {"inventory", "classification", "access", "lifecycle"},
	"ai":         {"assessment", "data", "suppliers-tools", "public-service"},
	"sharing":    {"providing", "outsourcing", "permissions", "exit"},
	"incident":   {"investigation", "response", "reporting", "retest"},
}

func validScenarioTask(scenario, task string) bool {
	if scenario == "" {
		return task == ""
	}
	tasks, ok := scenarioTaskIDs[scenario]
	if !ok {
		return false
	}
	return task == "" || classificationFilterValue(task, tasks...)
}

func scenarioMaterials(data ScenarioData) ([]ScenarioMaterial, ScenarioMaterialCounts) {
	materials := []ScenarioMaterial{}
	counts := ScenarioMaterialCounts{}
	if data.Scenario == "" || data.Task == "" {
		return materials, counts
	}
	activityPage := "activities/new"
	if data.ActivityID > 0 {
		activityPage = fmt.Sprintf("activities/%d?edit=1", data.ActivityID)
	}
	classifyPage := "classification"
	if data.AssetID > 0 {
		classifyPage = fmt.Sprintf("classification/%d", data.AssetID)
	}
	add := func(key, title, guidance, page string, keys ...string) {
		item := ScenarioMaterial{Key: key, Title: title, Status: "missing", FactKeys: keys, Records: []string{}, Guidance: guidance, Action: ScenarioMaterialAction{Page: page, Label: "补充或核对"}}
		for _, key := range keys {
			for _, fact := range data.Facts {
				if fact.Key == key && fact.State == "known" && fact.Value != "" {
					if item.Value != "" {
						item.Value += "；"
					}
					item.Value += fact.Value
					item.Records = append(item.Records, fact.Record)
				}
			}
		}
		if item.Value != "" {
			item.Status = "auto_filled"
		}
		if item.Value == "" && page == "checks" && data.ActivityID > 0 {
			item.Action.Page = fmt.Sprintf("checks/new/%d", data.ActivityID)
			item.Action.Label = "新建检查并附证据"
		}
		materials = append(materials, item)
	}
	add("classification", "分类分级及采用依据", "复用同一资产的人工确认修订；自动识别、AI建议、历史登记和法定认定分别核对。", classifyPage, "classification_context")
	if data.Classification != nil && !data.Classification.Usable && (data.Classification.Item.Revision > 0 || data.Classification.Trust != "unconfirmed") {
		materials[len(materials)-1].Status = "needs_confirmation"
		materials[len(materials)-1].Value = fmt.Sprintf("分类修订%d，登记属性%s，内部级别%s，法定类别登记值%s", data.Classification.Item.Revision, scenarioClassificationLabel(data.Classification.Item.Asset.Category), scenarioClassificationLabel(data.Classification.Item.Asset.BusinessLevel), scenarioClassificationLabel(data.Classification.Item.Asset.StatutoryCategory))
	}
	add("adopted_standard", "采用标准与条款快照", "保留真实采用的标准版本和条款；行业适用条件仍须独立核对。", classifyPage, "classification_standard")
	add("business_taxonomy", "业务分类目录", "如本次材料引用业务目录，核对确认时的路径与当前目录修订，不以业务目录代替法定类别。", classifyPage, "classification_taxonomy")
	add("publication", "分类清单发布版本", "如需引用固化清单，核对独立发布版本与当前范围；发布不代替监管认定。", "classification", "classification_publication")
	if data.Classification != nil && data.Classification.Publication != nil && !data.Classification.Publication.Current {
		materials[len(materials)-1].Status = "needs_confirmation"
	}
	add("source_scope", "来源范围与采集覆盖", "已采集范围不代表全部数据；规则命中、采样规模和去重主体人数分别核对。", "discovery", "source_coverage", "source_metadata", "source_observed_types")
	if !scenarioKnownFact(data.Facts, "source_coverage") {
		materials[len(materials)-1].Status = "missing"
	}
	add("purpose", "处理活动与实际用途", "带入已登记用途；实际必要性和适用范围须结合业务确认。", activityPage, "activity_purpose")
	add("retention", "保存安排", "自动带入登记安排，不代表已经执行删除或符合全部保存要求。", activityPage, "activity_retention")
	taskKey := data.Scenario + "/" + data.Task
	switch data.Scenario {
	case "personal":
		add("lawful_basis", "处理依据与必要性", "在评估答复中说明真实业务依据并引用证据；分类标签不自动证明处理合法。", "assessments?kind=pia&create=1", "lawful_basis")
		add("notice_consent", "告知、同意及其他适用依据", "核对告知和适用处理依据；未获取到材料不等于不适用。", "checks", "notice_consent")
		if data.Task == "pia" {
			add("pia_applicability", "影响评估适用条件", "在同一活动的个人信息影响评估中核对触发条件，不从敏感字段自动给出结论。", "assessments?kind=pia&create=1", "scenario_assessment_applicability")
			add("pia_record", "影响评估与独立复核记录", "复用同一活动的评估事实；内部评估状态不替代外部批准。", "assessments?kind=pia", "scenario_assessment")
		}
		if data.Task == "audit" {
			add("audit_organization", "审计范围、组织安排与审计底稿", "按实际审计任务记录组织安排及底稿，影响评估不替代合规审计。", "checks", "audit_organization")
		}
		if data.Task == "rights-retention" {
			add("rights_records", "权利响应与保存删除执行材料", "引用源系统的实际受理、核验、执行和结果凭证。平台不代为执行删除。", "checks", "rights_execution")
		}
	case "domestic":
		add("regulatory_scope", "任务触发、行业和适用要求", "对照真实通知、行业与处理者身份确认范围；标准库不自动覆盖全部监管要求。", "checks", "regulatory_scope")
		if data.Task == "important-data" {
			add("important_identification", "重要数据识别或认定依据", "已登记important/core值仅作为待核对记录，需补充真实识别、目录或认定依据及处理者身份。", "checks", "important_data_identification")
			add("risk_report", "风险评估与真实报送材料", "内部检查报告不替代正式风险评估及主管部门回执，按实际要求补充。", "checks", "risk_report_submission")
		}
		if data.Task == "personal-audit" {
			add("audit_organization", "个人信息审计组织与底稿", "复用实际审计证据，不能以影响评估代替审计。", "checks", "audit_organization")
		}
		if data.Task == "inspection" {
			add("inspection_notice", "检查通知、材料范围及提交凭证", "依据本次实际通知准备材料；已有内部报告不表示已经向监管提交。", "checks", "inspection_notice")
		}
	case "crossborder":
		add("recipient", "境外接收方与处理地域", "复用已登记接收方/地域；配置地域或IP不能单独证明完整出境范围。", activityPage, "activity_recipient", "activity_region")
		if !scenarioKnownFact(data.Facts, "activity_recipient") || !scenarioKnownFact(data.Facts, "activity_region") {
			materials[len(materials)-1].Status = "missing"
		}
		add("transfer", "传输与境外访问路径", "核对实际远程访问、备份及传输安排。", activityPage, "activity_transfer")
		add("subject_count", "去重主体规模与统计口径", "源采样行数和敏感字段数不能替代经核验的去重自然人数及期间口径。", "assessments?kind=cross_border&create=1", "deduplicated_subject_count")
		add("cii_status", "处理者与关基身份依据", "由业务和合规人员补充真实身份与适用证据，技术连接状态不能推定。", "assessments?kind=cross_border&create=1", "cii_status")
		add("transfer_path", "适用路径与拟用例外依据", "根据核实后的条件判断路径；数据类别或未知数量不能自动选定手续或豁免。", "assessments?kind=cross_border&create=1", "cross_border_exception")
		if data.Task == "assessment" || data.Task == "review" {
			add("crossborder_assessment", "内部出境评估与当前复核", "采用同一活动和当前事实；内部批准不表示主管部门安全评估通过。", "assessments?kind=cross_border", "scenario_assessment")
		}
		if data.Task == "procedures" {
			add("external_procedure", "协议、正式提交及外部办理结果", "按已核实路径引用真实协议、提交凭证与外部结果，不从内部报告生成办理成功状态。", "checks", "external_procedure")
		}
		if data.Task == "review" {
			add("change_record", "变更前后范围与复查依据", "从活动修订历史核对数据、用途、接收方和地域变化，重新评估适用条件。", activityPage, "activity_change_scope")
		}
	}
	if taskKey == "personal/pia" || taskKey == "crossborder/assessment" || taskKey == "crossborder/review" {
		for i := range materials {
			if scenarioKnownFact(data.Facts, "scenario_assessment_not_applicable") && (materials[i].Key == "pia_record" || materials[i].Key == "crossborder_assessment") {
				materials[i].Status = "not_applicable"
				materials[i].Value = scenarioFactValue(data.Facts, "scenario_assessment_not_applicable")
				materials[i].Records = []string{scenarioFactRecord(data.Facts, "scenario_assessment_not_applicable")}
			}
		}
	}
	for i := range materials {
		if (materials[i].Key == "pia_record" || materials[i].Key == "crossborder_assessment") && materials[i].Status == "auto_filled" && !scenarioKnownFact(data.Facts, "scenario_assessment_current") {
			materials[i].Status = "needs_confirmation"
		}
	}
	add("safeguards", "保护措施登记与执行证据", "已登记措施可带入，措施有效性仍须同一范围的新证据和必要的独立复核。", activityPage, "activity_safeguards")
	add("check_records", "同范围检查、证据及整改引用", "已建立记录可以复用；数量或状态不代表本次任务要求已完整覆盖。", "checks", "scenario_check_records")
	if scenarioKnownFact(data.Facts, "scenario_check_records") {
		materials[len(materials)-1].Status = "needs_confirmation"
	}
	relatedRecord := scenarioFactRecord(data.Facts, "scenario_assessment")
	var relatedID int64
	_, _ = fmt.Sscanf(relatedRecord, "assessment:%d/revision:", &relatedID)
	answerKeys := map[string][]string{"lawful_basis": {"purpose"}, "notice_consent": {"purpose", "evidence"}, "subject_count": {"subject_count"}, "cii_status": {"factors"}, "transfer_path": {"path", "factors"}, "pia_applicability": {"applicable"}, "important_identification": {"factors"}}
	for i := range materials {
		item := &materials[i]
		if relatedID > 0 && strings.HasPrefix(item.Action.Page, "assessments") {
			item.Action.Page = fmt.Sprintf("assessments/%d", relatedID)
			item.Action.Label = "继续已有评估"
		}
		if item.Status != "missing" {
			continue
		}
		for _, key := range answerKeys[item.Key] {
			factKey := "scenario_answer_" + key
			if scenarioKnownFact(data.Facts, factKey) {
				item.Status = "needs_confirmation"
				item.Value = scenarioFactValue(data.Facts, factKey)
				item.Records = []string{scenarioFactRecord(data.Facts, factKey)}
				item.Guidance += " 已有同一活动的相关答复，请核对该答复是否充分覆盖本项条件及证据。"
				break
			}
		}
		if item.Status == "missing" && relatedID > 0 && len(answerKeys[item.Key]) > 0 {
			item.Status = "needs_confirmation"
			item.Value = fmt.Sprintf("已有关联评估#%d；本项事实与证据待补充核对。", relatedID)
			item.Records = []string{relatedRecord}
			item.Guidance += " 请在已有评估的处理目的、范围、路径条件或证据题中补充真实依据。"
		}
		if item.Status == "missing" && scenarioKnownFact(data.Facts, "scenario_check_records") && strings.HasPrefix(item.Action.Page, "checks") {
			item.Status = "needs_confirmation"
			item.Action.Page = "checks"
			item.Value = scenarioFactValue(data.Facts, "scenario_check_records")
			item.Records = []string{scenarioFactRecord(data.Facts, "scenario_check_records")}
			item.Guidance += " 已有同范围检查可引用，请核对是否包含本项材料，不能仅以记录存在认定完成。"
		}
	}
	for _, m := range materials {
		switch m.Status {
		case "auto_filled":
			counts.AutoFilled++
		case "needs_confirmation":
			counts.NeedsConfirmation++
		case "missing":
			counts.Missing++
		case "not_applicable":
			counts.NotApplicable++
		}
	}
	return materials, counts
}
func scenarioKnownFact(facts []AssessmentFact, key string) bool {
	return scenarioFactValue(facts, key) != ""
}
func scenarioFactValue(facts []AssessmentFact, key string) string {
	values := []string{}
	for _, f := range facts {
		if f.Key == key && f.State == "known" && strings.TrimSpace(f.Value) != "" {
			values = append(values, f.Value)
		}
	}
	if len(values) != 1 {
		return ""
	}
	return values[0]
}
func scenarioFactRecord(facts []AssessmentFact, key string) string {
	for _, f := range facts {
		if f.Key == key && f.State == "known" {
			return f.Record
		}
	}
	return ""
}

func scenarioRecordFacts(ctx context.Context, tx pgx.Tx, ws int64, data *ScenarioData) error {
	if data.AssetID == 0 {
		return nil
	}
	var checkCount, evidenceCount int
	e := tx.QueryRow(ctx, `SELECT count(DISTINCT c.id),count(DISTINCT e.id) FROM plume.checks c LEFT JOIN plume.check_evidence e ON e.check_id=c.id WHERE c.workspace_id=$1 AND c.asset_id=$2 AND ($3::bigint=0 OR c.activity_id IS NULL OR c.activity_id=$3)`, ws, data.AssetID, data.ActivityID).Scan(&checkCount, &evidenceCount)
	if e != nil {
		return e
	}
	if checkCount > 0 {
		data.Facts = append(data.Facts, AssessmentFact{Key: "scenario_check_records", State: "known", Value: fmt.Sprintf("同范围检查%d项，已登记证据%d项；请核对实际覆盖和复核状态。", checkCount, evidenceCount), Source: "checks", Record: fmt.Sprintf("asset:%d/activity:%d", data.AssetID, data.ActivityID)})
	}
	if data.ActivityID == 0 {
		return nil
	}
	kind := ""
	if data.Scenario == "personal" {
		kind = "pia"
	}
	if data.Scenario == "crossborder" {
		kind = "cross_border"
	}
	if kind == "" {
		return nil
	}
	var id int64
	e = tx.QueryRow(ctx, `SELECT id FROM plume.assessments WHERE workspace_id=$1 AND activity_id=$2 AND kind=$3 ORDER BY id DESC LIMIT 1`, ws, data.ActivityID, kind).Scan(&id)
	if e == pgx.ErrNoRows {
		return nil
	}
	if e != nil {
		return e
	}
	r, e := readAssessmentRecord(ctx, tx, ws, id, false)
	if e != nil {
		return e
	}
	currentFacts := data.Facts
	filtered := make([]AssessmentFact, 0, len(currentFacts))
	for _, f := range currentFacts {
		if !strings.HasPrefix(f.Key, "scenario_") {
			filtered = append(filtered, f)
		}
	}
	actualActivity, e := scanActivity(tx.QueryRow(ctx, activitySelect+` WHERE p.workspace_id=$1 AND p.id=$2`, ws, data.ActivityID))
	if e != nil {
		return e
	}
	current := !r.Stale && r.FactsDigest == assessmentFactsDigest(filtered) && r.ActivityDigest == assessmentActivityDigest(actualActivity)
	fact := AssessmentFact{Key: "scenario_assessment", State: "known", Value: fmt.Sprintf("评估#%d（%s），状态%s，修订%d，当前事实匹配=%t。", r.ID, r.Kind, r.Status, r.Revision, current), Source: "assessment_record", Record: fmt.Sprintf("assessment:%d/revision:%d", r.ID, r.Revision)}
	data.Facts = append(data.Facts, fact)
	for _, answer := range r.Answers {
		provided := strings.TrimSpace(answer.Value) != "" && answer.Value != "unknown" || len(answer.Values) > 0 && !classificationFilterValue("unknown", answer.Values...) || answer.Number != nil || len(answer.EvidenceIDs) > 0
		if provided {
			data.Facts = append(data.Facts, AssessmentFact{Key: "scenario_answer_" + answer.Key, State: "known", Value: assessmentFactJSON(answer), Source: "assessment_answer_declared", Record: fact.Record + "/answer:" + answer.Key})
		}
	}
	if current && r.Status == "approved" {
		data.Facts = append(data.Facts, AssessmentFact{Key: "scenario_assessment_current", State: "known", Value: "approved_current", Source: "assessment_record", Record: fact.Record})
	}
	// Non-applicability is adopted only from an independently approved, current
	// assessment with an explicit reason and real evidence in this same scope.
	if r.Status != "approved" || r.Reviewer == "" || !current || r.Activity.ID != data.ActivityID {
		return nil
	}
	for _, answer := range r.Answers {
		if answer.Key == "applicable" && (answer.Value == "yes" || answer.Value == "no") {
			data.Facts = append(data.Facts, AssessmentFact{Key: "scenario_assessment_applicability", State: "known", Value: answer.Value + "；" + answer.Note, Source: "approved_assessment", Record: fact.Record})
			break
		}
	}
	if automaticAssessmentApplicability(r.Kind, actualActivity).State == "applicable" {
		return nil
	}
	for _, answer := range r.Answers {
		if answer.Key != "applicable" || answer.Value != "no" || strings.TrimSpace(answer.Note) == "" {
			continue
		}
		ids := []int64{}
		for _, a := range r.Answers {
			ids = append(ids, a.EvidenceIDs...)
		}
		if len(ids) == 0 {
			return nil
		}
		var verified bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.check_evidence e JOIN plume.checks c ON c.id=e.check_id WHERE e.id=ANY($1) AND c.workspace_id=$2 AND c.asset_id=$3 AND (c.activity_id IS NULL OR c.activity_id=$4))`, ids, ws, data.AssetID, data.ActivityID).Scan(&verified)
		if e != nil {
			return e
		}
		if !verified {
			return nil
		}
		data.Facts = append(data.Facts, AssessmentFact{Key: "scenario_assessment_not_applicable", State: "known", Value: answer.Note, Source: "approved_assessment_with_evidence", Record: fact.Record})
		break
	}
	return nil
}
