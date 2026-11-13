package store

import (
	"context"
	"github.com/jackc/pgx/v5"
)

func scenarioCheckReportRequirements(ctx context.Context, tx pgx.Tx, ws int64, run *ScenarioCheckRun) ([]ScenarioCheckReportRequirement, error) {
	g03, e := scenarioCheckBasis(ctx, tx, "G03")
	if e != nil {
		return nil, e
	}
	refs := []string{}
	ids := []string{}
	observed := false
	for _, e := range run.Evidence {
		refs = append(refs, e.ID)
		if e.Source == "postgresql" || e.Source == "discovery" || e.Source == "api_validation" || e.Source == "runtime" {
			observed = true
		}
	}
	for _, f := range run.Findings {
		ids = append(ids, f.ID)
	}
	internal := ScenarioCheckReportRequirement{Key: "technical_check_report", Title: "本次内部技术检查及证据包", Basis: g03, Applicability: "applicable", RequiredDeliverables: []string{"检查对象、范围及来源时点", "采用要求、技术基线与规则版本", "逐项结果、证据摘要、未知条件及覆盖限制", "整改申请、执行回执、新来源复查与独立复核引用"}, FindingIDs: ids, EvidenceRefs: refs, Coverage: "仅本次快照列示的技术检查；不含源数据库原始记录内容。", Status: "available", MissingConditions: []string{}, ExternalSteps: []string{"导出后核对组织、本次通知和实际检查范围；监管要求的完整报告仍需补齐适用章节与责任人确认。"}, RetentionNote: "本检查快照随工作区保留；应按实际法定要求另行保全导出文件。现有自动PG报告归档轮转不能替代长期法定保存。"}
	result := []ScenarioCheckReportRequirement{internal}
	code, key, title := "", "", ""
	deliverables := []string{}
	missing := []string{"需由业务及合规人员确认本次法定报告适用性，技术检查不能单独确定触发条件。"}
	switch run.Scenario {
	case "personal":
		code = "P04"
		key = "personal_impact_report"
		title = "个人信息保护影响评估及处理记录（按适用情形）"
		deliverables = []string{"处理目的、方式等是否合法正当必要", "个人权益影响及安全风险", "保护措施是否合法有效且与风险相适应", "事前评估、处理记录及责任人确认"}
		missing = append(missing, "处理合法性依据、实际必要性、完整主体及处理范围、处理开始时点、完整风险影响和措施验证。")
		if run.Task == "audit" {
			code = "P05"
			key = "personal_compliance_audit_report"
			title = "个人信息保护合规审计材料（独立于影响评估）"
			deliverables = []string{"实际审计范围、组织与审计底稿", "审计发现、整改及复核依据", "按适用情形签章的专业审计报告和真实报送材料"}
			missing = append(missing, "审计触发、机构及专业人员、完整审计程序、签章和适用报送要求。")
		}
	case "domestic":
		code = "I03"
		key = "important_data_risk_report"
		title = "重要数据安全风险评估报告（按适用情形）"
		deliverables = []string{"处理者基本情况、安全管理机构及负责人", "重要数据数量、种类和处理情况（不含数据内容）", "制度、技术措施及其有效性", "风险、事件及处置情况", "提供、委托及共同处理的评估情况", "数据出境情况", "适用的行业补充内容、整改与责任人确认"}
		missing = append(missing, "重要数据识别或认定与处理者身份、完整制度和风险评估、真实事件处置、合同及行业要求。")
		if run.Task == "personal-audit" {
			code = "P05"
			key = "personal_compliance_audit_report"
			title = "个人信息保护合规审计材料（按适用情形）"
			deliverables = []string{"审计范围与组织", "审计程序、底稿与发现", "整改复核、专业报告签章及适用报送材料"}
		}
	case "crossborder":
		code = "T03"
		key = "crossborder_report"
		title = "数据出境评估及办理材料（按适用路径）"
		deliverables = []string{"处理者、境外接收方、地域与处理范围", "数据类别、去重主体规模、关基身份与适用例外证据", "处理目的、路径条件、权益影响和安全风险", "保护措施、法律文件及实际验证证据", "按适用路径准备的正式报告、签章、提交材料和真实外部结果"}
		missing = append(missing, "真实境外访问与链路、去重主体口径、关基与例外依据、法律文件、完整风险评估及外部手续。")
	}
	if code != "" {
		basis, e := scenarioCheckBasis(ctx, tx, code)
		if e != nil {
			return nil, e
		}
		status := "missing"
		if observed {
			status = "partial"
		}
		result = append(result, ScenarioCheckReportRequirement{Key: key, Title: title, Basis: basis, Applicability: "conditional", RequiredDeliverables: deliverables, FindingIDs: ids, EvidenceRefs: refs, Coverage: "自动证据只支持列示技术章节，不能由技术计数生成完整法定报告、签章或外部办理成功结论。", Status: status, MissingConditions: missing, ExternalSteps: []string{"按主管部门或行业的实际要求核对正式报告结构和适用表单。", "补齐业务、法律、组织材料并由责任人及适用的专业机构核实签章。", "需要正式提交时在相应渠道办理，另行保存真实提交凭证、补充要求及外部结果。"}, RetentionNote: "按实际适用的保存期限保全完整评估及处理记录；个人信息影响评估报告和处理记录适用时至少保存三年，重要数据报告按相应办法核对保存要求。"})
	}
	return result, nil
}
