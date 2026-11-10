export interface PostgresInspection {
  assetId: number
  jobId: number
  runId: string
  observedAt: string
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
  }
}

export interface PostgresColumn {
  name: string
  type: string
  ordinal: number
  nullable: boolean
  sensitivity?: string
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
