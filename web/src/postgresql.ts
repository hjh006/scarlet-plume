import type { AutomaticClassification } from './classification-standards'

export interface PostgresInspection {
  assetId: number
  jobId: number
  runId: string
  observedAt: string
  classification?: AutomaticClassification
  table: {
    sourceRef: string
    name: string
    schema: string
    owner: string
    columns: PostgresColumn[]
    grants: PostgresGrant[]
    publicSchemaUsage: boolean
    publicSelect: boolean
    publicSelectColumns: string[]
    comment?: string
    metadataStatus?: string
    estimatedRows?: number
    rowSecurity?: { enabled: boolean; forced: boolean; active: boolean }
    constraints?: { name: string; type: string; columns: string[]; validated: boolean }[]
    contentAnalysis?: PostgresContentAnalysis
    warnings?: string[]
  }
}

export interface PostgresColumn {
  name: string
  type: string
  ordinal: number
  nullable: boolean
  sensitivity?: string
  comment?: string
  canSelect?: boolean
}

export interface PostgresContentAnalysis {
  status: string
  method: string
  rowLimit: number
  rowsScanned: number
  columns: {
    column: string
    status: string
    nonNullRows: number
    limitedRows?: number
    matches: { category: string; count: number }[]
  }[]
  warnings: string[]
}

export interface PostgresGrant {
  grantee: string
  grantor: string
  public: boolean
  privilege: string
  grantable: boolean
  column?: string
  schemaUsage: boolean
}

export interface PostgresEvidence {
  sourceRef: string
  schema: string
  table: string
  database?: string
  scope: string
  columnCount: number
  sensitiveCandidateCount: number
  grantCount: number
  publicSchemaUsage: boolean
  publicSelect: boolean
  publicSelectColumnCount: number
  assessment?: 'review_required' | 'no_signal'
  assessmentReasons?: string[]
  sensitiveFields?: { name: string; sensitivity: string }[]
  selectGrants?: Pick<PostgresGrant, 'grantee' | 'public' | 'column' | 'schemaUsage'>[]
  sensitiveFieldsOmitted?: number
  selectGrantsOmitted?: number
  classification?: AutomaticClassification
}

export const postgresProfileStatusLabels: Record<string, string> = {
  complete: '已完成可见范围采集',
  partial: '部分范围可见',
  unknown: '观察范围不可用',
}

const profileWarningLabels: Record<string, string> = {
  constraints_unavailable: '约束信息未取得',
  constraint_columns_truncated: '约束字段过多，部分字段未展示',
  comments_truncated: '部分注释超出采集长度范围',
  table_budget_exceeded: '本轮表级采集预算已用完',
  collection_deadline: '采集时间预算不足',
  columns_without_select: '部分字段缺少 SELECT 权限',
  unsupported_column_types: '部分字段类型尚不支持格式聚合',
  column_budget_exceeded: '部分字段超出本轮聚合范围',
  no_readable_supported_columns: '没有可读取且受支持的字段',
  row_security_filtered_scope: '行级策略限制了本次可见范围',
  column_value_length_limit: '部分字段值超过格式分析长度范围',
  row_limit_reached: '已达到本轮样本行数上限',
  aggregate_unavailable: '内容聚合结果未取得',
  source_relation_changed: '采集期间来源表发生变化',
  aggregate_permission_denied: '源库拒绝本次内容聚合',
  aggregate_timeout: '本次内容聚合超时',
}

export function postgresProfileWarning(code: string) {
  return profileWarningLabels[code] || code
}

export const postgresAssessmentLabels = {
  review_required: '检出线索 · 待核对',
  no_signal: '本次未检出线索',
}

const assessmentReasonLabels: Record<string, string> = {
  sensitive_candidate: '检出疑似敏感字段',
  no_sensitive_candidate: '未检出疑似敏感字段',
  public_select: '检出 PUBLIC 直接 SELECT 授权',
  named_select: '检出表所有者之外的具名角色直接 SELECT 授权',
  no_non_owner_select: '未检出表所有者之外的直接 SELECT 授权',
  classification_matched: '自动判定与当前分类相容',
  classification_conflict: '自动判定与当前分类存在差异',
  classification_unconfirmed: '分类结果待确认',
  classification_insufficient: '采集事实不足以比对分类',
}

export function postgresAssessmentReasons(reasons: string[] = []) {
  return reasons.map(reason => assessmentReasonLabels[reason] || reason).join(' · ')
}

export const postgresPermissionScope = '权限范围为表和字段直接授权及 PUBLIC 权限。角色成员关系、权限继承和行级策略需单独核验。'

export function postgresPublicSelect(tableGrant: boolean, columnCount: number) {
  if (tableGrant) return 'PUBLIC 有表级 SELECT 授权'
  if (columnCount > 0) return 'PUBLIC 有 ' + columnCount + ' 个字段的 SELECT 授权'
  return '未检出 PUBLIC SELECT 授权'
}

const sensitivityLabels: Record<string, string> = {
  email: '邮箱',
  phone: '电话号码',
  national_id: '身份号码',
  financial_account: '金融账号',
  credential: '凭据',
  personal_name: '个人姓名',
  address: '地址',
  ip_address: '网络地址',
}

export function postgresSensitivityLabel(value: string) {
  return sensitivityLabels[value] || value
}
