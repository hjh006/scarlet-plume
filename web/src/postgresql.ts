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
  sensitiveFields?: { name: string; sensitivity: string }[]
  selectGrants?: Pick<PostgresGrant, 'grantee' | 'public' | 'column' | 'schemaUsage'>[]
  sensitiveFieldsOmitted?: number
  selectGrantsOmitted?: number
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
