import { assetBusinessLevelLabels, assetCategoryLabels, assetStatutoryCategoryLabels, providerLabel } from './api'
import { activityKindLabels } from './activity'
import { postgresSensitivityLabel } from './postgresql'

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
  classification_context: '已确认分类与业务目录上下文', classification_standard: '采用标准与条款', classification_taxonomy: '采用业务分类目录', classification_publication: '分类清单发布记录', classification_record: '分类原记录', classification_candidates: '识别候选与快照', classification_legacy_record: '历史人工登记', scenario_assessment: '同范围专项评估记录', scenario_check_records: '同范围检查与证据', assessment_scope: '按登记活动判定的评估范围', activity_kind: '活动类型', activity_purpose: '登记处理目的', activity_recipient: '登记接收方',
  activity_region: '登记地域', activity_retention: '登记保留策略', activity_transfer: '登记传输路径',
  activity_safeguards: '登记保护措施', asset_category: '资产登记分类', asset_business_level: '业务保护等级',
  asset_statutory_category: '登记法定类别', source_observed_types: '来源识别类型', source_coverage: '来源观察范围',
  deduplicated_subject_count: '去重主体数量', cii_status: '关基身份', cross_border_exception: '出境例外条件',
  public_ai_service: '境内公众服务范围', automated_decision: '自动化决策用途', processing_start_time: '实际处理开始时间',
  lawful_basis: '适用处理依据', notice_consent: '告知与同意事实',
  source_metadata: '来源元数据', source_permissions: '来源权限证据', catalog_business_metadata: '目录业务属性及确认冲突',
  application_name: '配置应用名称', application_vendor: '配置供应商', application_model: '配置模型与服务', application_region: '应用配置地域',
  application_retention: '配置留存安排', application_training_use: '训练使用设置', application_service_audience: '服务对象声明',
  application_knowledge_bases: '知识库配置', application_tools: '工具配置', application_data_categories: '数据类别声明',
  application_configuration: '应用配置采集摘要',
}

export function assessmentFactSourceLabel(source: string) {
  const labels: Record<string, string> = {
    manual_classification: '人工分类确认', manual_classification_declared: '法定类别登记', manual_classification_context: '人工分类及业务目录', manual_legacy_declared: '历史人工登记', classification_snapshot: '分类采用快照', classification_record: '分类原记录', classification_publication: '独立发布记录', recognition_candidate: '识别候选', assessment_record: '专项评估记录', approved_assessment: '独立通过的评估记录', approved_assessment_with_evidence: '独立评估及采用证据', checks: '同范围检查与证据',
    activity: '处理活动登记', asset: '资产登记', classification: '分类分级登记', catalog_declared: '业务目录登记',
    business_fact: '业务事实待取证', source: '来源采集',
    postgresql: 'PostgreSQL 采集', application: '应用 API 配置声明', application_api: '应用 API 配置声明',
    application_api_conflict: '应用 API 配置声明（来源冲突）',
  }
  if (source.startsWith('application:')) return '应用 API 配置声明 · ' + source.slice('application:'.length)
  return labels[source] || providerLabel(source)
}

export function assessmentFactStateLabel(fact: AssessmentFact) {
  if (fact.state !== 'known') return '待核实'
  return ['activity', 'asset', 'classification', 'catalog_declared', 'manual_classification', 'manual_classification_declared', 'manual_classification_context', 'manual_legacy_declared', 'classification_snapshot', 'classification_record', 'classification_publication', 'assessment_record', 'approved_assessment', 'approved_assessment_with_evidence', 'checks'].includes(fact.source) ? '已登记' : '已获取'
}

export function assessmentFactDisplayValue(fact: AssessmentFact) {
  const value = fact.value || ''
  if (fact.key === 'asset_category') return assetCategoryLabels[value] || value
  if (fact.key === 'asset_business_level') return assetBusinessLevelLabels[value] || value
  if (fact.key === 'asset_statutory_category') return assetStatutoryCategoryLabels[value] || value
  if (fact.key === 'activity_kind' || fact.key === 'assessment_scope') return (activityKindLabels as Record<string, string>)[value] || value
  if (fact.key === 'source_observed_types') return value.split(',').map(item => postgresSensitivityLabel(item.trim())).join('、')
  return value
}

export function structuredAssessmentFact(value?: string) {
  if (!value || !/^[\[{]/.test(value.trim())) return undefined
  try {
    const parsed: unknown = JSON.parse(value)
    return parsed !== null && typeof parsed === 'object' ? JSON.stringify(parsed, null, 2) : undefined
  } catch {
    return undefined
  }
}
