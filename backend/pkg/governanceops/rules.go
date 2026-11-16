package governanceops

import (
	"slices"
	"time"
)

type Regulation struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Version   string `json:"version"`
	Published string `json:"published"`
	Effective string `json:"effective"`
	Status    string `json:"status"`
	SourceURL string `json:"sourceUrl"`
	Scope     string `json:"scope"`
	Boundary  string `json:"boundary"`
}

func Regulations() []Regulation {
	return []Regulation{
		{ID: "cn-pipl-2021", Title: "中华人民共和国个人信息保护法", Version: "2021-08-20", Published: "2021-08-20", Effective: "2021-11-01", Status: "in_force", SourceURL: "https://www.cac.gov.cn/2021-08/20/c_1631050028355286.htm", Scope: "按处理地域、处理角色和个人信息活动核对适用性。", Boundary: "权利请求、同意及撤回按实际处理系统取证；不设置通用72小时或统一权利答复法定期限。"},
		{ID: "cn-network-data-2025", Title: "网络数据安全管理条例", Version: "2024-09-30", Published: "2024-09-30", Effective: "2025-01-01", Status: "in_force", SourceURL: "https://www.cac.gov.cn/2024-09/30/c_1729384452307680.htm", Scope: "适用的网络数据处理者；提供或委托处理个人信息、重要数据按第12条留痕。", Boundary: "处理者、受托者、共同处理角色分别确认；合同与监督不等于已执行删除。"},
		{ID: "cn-csl-2026", Title: "中华人民共和国网络安全法（2025修正）", Version: "2025-10-28", Published: "2025-10-29", Effective: "2026-01-01", Status: "in_force", SourceURL: "https://www.cac.gov.cn/2025-12/29/c_1768735112911946.htm", Scope: "按网络运营者身份与实际系统范围核查。", Boundary: "现行第23条与旧条号分开保留；平台办理记录不代替日志及实际安全措施。"},
		{ID: "cn-incident-2025", Title: "国家网络安全事件报告管理办法", Version: "2025-09-11", Published: "2025-09-15", Effective: "2025-11-01", Status: "in_force", SourceURL: "https://www.cac.gov.cn/2025-09/15/c_1759583017717009.htm", Scope: "境内网络运营者发现或获知较大以上事件后，按主体与行业渠道报告。", Boundary: "关基1小时、中央国家机关及直属单位2小时、其他运营者4小时；结束后30日总结。分级与并行行业义务需人工核定。"},
		{ID: "cn-pi-audit-2025", Title: "个人信息保护合规审计管理办法", Version: "2025-02-12", Published: "2025-02-14", Effective: "2025-05-01", Status: "in_force", SourceURL: "https://www.cac.gov.cn/2025-02/14/c_1741233507681519.htm", Scope: "超过1000万人至少每两年审计；100万人以上负责人要求另行判断。", Boundary: "自然人数必须有去重口径证据；审计机构关联方、负责人及连续委托记录需核对；审计计划不等于审计完成。"},
		{ID: "cn-small-pi-2026", Title: "小型个人信息处理者个人信息保护简化措施规定", Version: "2026-07-22", Published: "2026-07-24", Effective: "2026-09-01", Status: "in_force", SourceURL: "https://www.cac.gov.cn/2026-07/24/c_1786638889704872.htm", Scope: "境内处理不满10万人；简化审计至少每五年，未成年人等特别规定另核。", Boundary: "不是全面豁免；自查表至少五年，PIA表至少三年；平台覆盖免重复须逐项证明适用条件。"},
		{ID: "cn-network-risk-2026", Title: "网络数据安全风险评估办法", Version: "2026-06-18", Published: "2026-06-18", Effective: "2026-08-20", Status: "in_force", SourceURL: "https://www.cac.gov.cn/2026-06/18/c_1783525609815499.htm", Scope: "重要数据年度评估及重大变更补评；行业规定优先。", Boundary: "年度报告完成后20个工作日内报送；无适用版本化工作日历时保留待核定。一般数据三年评估为鼓励性要求。"},
		{ID: "cn-miit-risk-2024", Title: "工业和信息化领域数据安全风险评估实施细则（试行）", Version: "2024-05-10", Published: "2024-05-24", Effective: "2024-06-01", Status: "in_force", SourceURL: "https://www.miit.gov.cn/cms_files/filemanager/1226211233/attach/20244/1e8f671034ea40a796ca155145c86081.pdf", Scope: "工信领域重要数据和核心数据处理者，年度与变更风险评估。", Boundary: "行业报告为10个工作日；不得被一般办法20个工作日覆盖。"},
		{ID: "cn-crossborder-2024", Title: "促进和规范数据跨境流动规定", Version: "2024-03-22", Published: "2024-03-22", Effective: "2024-03-22", Status: "in_force", SourceURL: "https://www.cac.gov.cn/2024-03/22/c_1712776611775634.htm", Scope: "依据关基、重要数据、累计出境自然人数与例外分别核定。", Boundary: "已有出境评估工作流保留，不能由样本行数推断自然人数、用认证替代必须安全评估或拆分规避。"},
		{ID: "cn-export-cert-2026", Title: "个人信息出境认证办法", Version: "2025-10-14", Published: "2025-10-17", Effective: "2026-01-01", Status: "in_force", SourceURL: "https://www.cac.gov.cn/2025-10/17/c_1762449728720008.htm", Scope: "适用认证路径或自愿认证；证书三年，期满前六个月申请续证。", Boundary: "自愿认证不替代依法必须安全评估；需核对证书机构、范围及实际有效状态。"},
		{ID: "cn-aigc-label-2025", Title: "人工智能生成合成内容标识办法", Version: "2025-03-07", Published: "2025-03-14", Effective: "2025-09-01", Status: "in_force", SourceURL: "https://www.cac.gov.cn/2025-03/14/c_1743654684782215.htm", Scope: "按算法推荐、深度合成、生成式AI服务规定中的实际角色、场景判定。", Boundary: "逐模态验证显式、隐式及导出保留；生成概率与来源确认分开；记录验证不代替全格式标准符合性。"},
		{ID: "cn-gb45438-2025", Title: "GB 45438—2025 人工智能生成合成内容标识方法", Version: "GB 45438-2025", Published: "2025-02-28", Effective: "2025-09-01", Status: "mandatory_standard_in_force", SourceURL: "https://openstd.samr.gov.cn/bzgk/std/newGbInfo?hcno=F32EA2A561F1886CD8D606513512D547", Scope: "按具体服务角色、文件格式和标识要求验证。", Boundary: "未实现格式保持未知；人工或元数据观察不是C2PA密码学验签。"},
		{ID: "cn-genai-2023", Title: "生成式人工智能服务管理暂行办法", Version: "2023-07-10", Published: "2023-07-13", Effective: "2023-08-15", Status: "in_force", SourceURL: "https://www.cac.gov.cn/2023-07/13/c_1690898327029107.htm", Scope: "向境内公众提供生成式AI服务；非向境内公众提供的内部研发应用不适用本办法。", Boundary: "排除此办法不排除个人信息、数据安全及其他实际适用的义务。"},
		{ID: "cn-anthropomorphic-2026", Title: "人工智能拟人化互动服务管理暂行办法", Version: "2026-04-10", Published: "2026-04-10", Effective: "2026-07-15", Status: "in_force", SourceURL: "https://www.cac.gov.cn/2026-04/10/c_1777558395078289.htm", Scope: "面向境内公众模拟人格、持续情感互动的服务。", Boundary: "无持续情感互动的普通客服、问答、工作助手等不据此自动适用；训练与互动数据、未成年人及退出控制按真实服务核查。"},
	}
}

func RegulationByID(id string) (Regulation, bool) {
	for _, r := range Regulations() {
		if r.ID == id {
			return r, true
		}
	}
	return Regulation{}, false
}
func Adopted(ids []string) []Regulation {
	out := []Regulation{}
	for _, id := range ids {
		if r, ok := RegulationByID(id); ok {
			out = append(out, r)
		}
	}
	return out
}

type Deadline struct {
	Code           string     `json:"code"`
	Title          string     `json:"title"`
	RegulationID   string     `json:"regulationId,omitempty"`
	DueAt          *time.Time `json:"dueAt,omitempty"`
	Status         string     `json:"status"`
	Unit           string     `json:"unit,omitempty"`
	Amount         int        `json:"amount,omitempty"`
	ReceiptPresent bool       `json:"receiptPresent"`
	Reason         string     `json:"reason"`
}

func Deadlines(in Input, now time.Time) []Deadline {
	out := []Deadline{}
	d := in.Details
	add := func(code, title, reg, unit string, amount int, due *time.Time, purpose, reason string) {
		s := "pending_scope"
		if due != nil {
			s = "upcoming"
			if due.Before(now) {
				s = "overdue"
			}
		}
		if unit == "working_days" {
			s = "pending_calendar"
		}
		out = append(out, Deadline{Code: code, Title: title, RegulationID: reg, DueAt: due, Status: s, Unit: unit, Amount: amount, ReceiptPresent: hasReceipt(in, purpose, ""), Reason: reason})
	}
	if in.Applicability != "applicable" {
		return out
	}
	selected := func(id string) bool { return slices.Contains(in.RegulationIDs, id) }
	if in.Kind == "incident" && selected("cn-incident-2025") {
		if d.DetectedAt != nil && d.DetectedAt.Before(time.Date(2025, 11, 1, 0, 0, 0, 0, time.FixedZone("CST", 8*3600))) {
			add("incident_initial", "初报期限待核定", "cn-incident-2025", "", 0, nil, "initial_report", "发现时间早于本版本生效时间，需选用当时适用规则。")
			return out
		}
		if d.Level == "unknown" || d.OperatorType == "unknown" {
			add("incident_initial", "初报期限待核定", "cn-incident-2025", "hours", 0, nil, "initial_report", "先确认较大以上分级、主体及并行行业渠道，不因未知暂停真实法定义务。")
		} else if d.Level != "general" {
			h := map[string]int{"cii": 1, "central": 2, "other": 4}[d.OperatorType]
			due := d.DetectedAt.Add(time.Duration(h) * time.Hour)
			add("incident_initial", "较大以上事件初报", "cn-incident-2025", "hours", h, &due, "initial_report", "从发现或获知计时；仅有证据引用不表示主管机关收件，逐渠道核验回执。")
			if d.ResponseEndedAt != nil {
				due := d.ResponseEndedAt.In(time.FixedZone("Asia/Shanghai", 8*3600)).AddDate(0, 0, 30)
				add("incident_final", "处置结束后总结", "cn-incident-2025", "calendar_days", 30, &due, "final_report", "按原渠道报送；当前为同一当地时刻的保守计划值，日末认定与行业并行义务需人工核定。")
			} else {
				add("incident_final", "总结期限待定", "cn-incident-2025", "calendar_days", 30, nil, "final_report", "尚无处置结束时间。")
			}
		}
	}
	if in.Kind == "rights_request" && d.ResponseDueAt != nil {
		add("rights_response", "权利请求内部承诺期限", "cn-pipl-2021", "manual", 0, d.ResponseDueAt, "response_delivery", "此日期是人工设定的内部期限，不表示法律统一规定该时长。")
	}
	if in.Kind == "vendor" {
		if d.ContractExpiresAt != nil {
			add("vendor_contract", "合同到期复核", "cn-network-data-2025", "manual", 0, d.ContractExpiresAt, "contract", "按真实合同时间；合同到期不代表受托者已返还或删除。")
		}
		if d.ReviewDueAt != nil {
			add("vendor_review", "供应商监督复核", "cn-network-data-2025", "manual", 0, d.ReviewDueAt, "", "内部计划，监督结果关联检查证据。")
		}
	}
	if in.Kind == "audit" {
		if d.PlannedAt != nil {
			add("audit_plan", "内部审计计划", "", "manual", 0, d.PlannedAt, "", "此日期是内部计划，不代替法定审计周期或监管指定期限。")
		}
		if d.CompletedAt != nil && d.ProcessorPeople != nil && d.ScaleEvidenceID > 0 {
			if selected("cn-pi-audit-2025") && *d.ProcessorPeople > 10_000_000 {
				due := addMonths(*d.CompletedAt, 24)
				add("audit_next", "定期审计复核上限", "cn-pi-audit-2025", "months", 24, &due, "", "以经证据支持的自然人数为口径；重大变化及特别要求可能要求更早审计。")
			} else if selected("cn-small-pi-2026") && *d.ProcessorPeople < 100_000 && !d.MinorData {
				due := addMonths(*d.CompletedAt, 60)
				add("audit_next", "小型处理者简化审计上限", "cn-small-pi-2026", "months", 60, &due, "", "需确认境内小型处理者资格及无优先特别规则；认证豁免须另核真实证书。")
			} else {
				add("audit_next", "审计周期待核定", "", "", 0, nil, "", "当前规模或未成年人特别规则不支持自动套用两年或五年。")
			}
		}
		if d.CompletedAt != nil && (selected("cn-network-risk-2026") || selected("cn-miit-risk-2024")) {
			reg := "cn-network-risk-2026"
			n := 20
			if selected("cn-miit-risk-2024") {
				reg = "cn-miit-risk-2024"
				n = 10
			}
			add("risk_report", "风险评估报告报送待核定", reg, "working_days", n, nil, "audit_report", "仅对实际适用的重要/核心数据风险评估；未配置权威版本化工作日历，不以周一至周五计算。")
		}
	}
	return out
}

func addMonths(t time.Time, n int) time.Time {
	base := time.Date(t.Year(), t.Month()+time.Month(n), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	last := base.AddDate(0, 1, -1).Day()
	day := t.Day()
	if day > last {
		day = last
	}
	return time.Date(base.Year(), base.Month(), day, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
}
