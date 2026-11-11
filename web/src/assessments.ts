export interface AssessmentQuestion {
  key: string; title: string; kind: 'single' | 'multi' | 'text' | 'number' | 'evidence'
  options?: string[]; basis: string; basisUrl: string; applicability: string
  evidence: string; acceptance: string; requirementCode: string
  obligation: 'conditional_law' | 'recommended'; required: boolean; factKey?: string
}
export interface AssessmentTemplate {
  id: number; revisionId?: number; key: string; title: string; kind: string; state: string
  revision: number; builtIn: boolean; questions: AssessmentQuestion[]
}
export interface AssessmentAnswer {
  key: string; value?: string; values?: string[]; number?: number; evidenceIds?: number[]; note?: string
}
export interface Assessment {
  id: number; kind: string; title: string; status: string; activityId: number
  templateRevisionId: number; creatorId: number; updatedAt: string; dueAt: string; stale: boolean
}
export interface AssessmentFact {
  key: string; state: string; value?: string; source: string; record?: string; observedAt?: string
}
export interface AssessmentDetail extends Assessment {
  automaticApplicability?: { state: string; reason: string; source: string; record?: string }
  revision: number; creator: string; reviewer?: string; reviewNote: string
  template: AssessmentTemplate; activity: { id: number; name: string; assetId: number; purpose: string; kind: string }
  facts: AssessmentFact[]; answers: AssessmentAnswer[]
  evidence: { id: number; checkId: number; kind: string; source: string; record: string; summary: string; observedAt: string }[]
  evidenceTruncated?: boolean
  checks: { id: number; requirementCode: string; status: string; ownerTeam: string }[]
  gaps: { key: string; title: string; reason: string }[]; risks: { key: string; title: string; reason: string }[]
  events: { id: number; action: string; actor: string; note: string; createdAt: string }[]
  applicability: string; reassessmentPending: boolean; factsRefreshedAt: string; refreshMinutes: number
  canEdit: boolean; canReview: boolean; canApprove: boolean; basis: string
}
export const assessmentKinds: Record<string, string> = {
  pia: '个人信息影响评估', cross_border: '数据出境评估', ai: 'AI 使用评估', business_change: '业务变更重评',
}
export const assessmentStatuses: Record<string, string> = {
  draft: '草稿', submitted: '待独立复核', needs_work: '退回补充', approved: '独立通过',
}
export const assessmentOptions: Record<string, string> = {
  yes: '是', no: '否', unknown: '尚未核实', cii: '关键信息基础设施运营者', important_data: '重要数据',
  personal: '个人信息', sensitive_personal: '敏感个人信息', exception_claimed: '拟适用例外',
  security_assessment: '数据出境安全评估', standard_contract: '标准合同', certification: '个人信息保护认证',
  exception: '经核实的适用例外', business: '经营与业务数据', public: '公开数据',
  automated_decision: '个人信息自动化决策', providing: '向其他处理者提供', outbound: '向境外提供', none: '均不涉及',
  purpose: '处理目的', method: '处理方式', data_types: '数据种类', recipient: '接收方', region: '处理地域',
  access: '访问权限', model: '模型与服务',
}
export const assessmentFactLabels: Record<string, string> = {
  assessment_scope: '按登记活动判定的评估范围', activity_kind: '活动类型', activity_purpose: '登记处理目的', activity_recipient: '登记接收方',
  activity_region: '登记地域', activity_retention: '登记保留策略', activity_transfer: '登记传输路径',
  activity_safeguards: '登记保护措施', asset_category: '资产登记分类', asset_business_level: '业务保护等级',
  asset_statutory_category: '登记法定类别', source_observed_types: '来源识别类型', source_coverage: '来源观察范围',
  deduplicated_subject_count: '去重主体数量', cii_status: '关基身份', cross_border_exception: '出境例外条件',
  public_ai_service: '境内公众服务范围', automated_decision: '自动化决策用途', processing_start_time: '实际处理开始时间',
  lawful_basis: '适用处理依据', notice_consent: '告知与同意事实',
}
