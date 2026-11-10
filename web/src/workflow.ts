import type { ActivityLink } from './activity'
import type { TemplateBinding } from './template'
import type { PostgresEvidence } from './postgresql'

export const checkStatusLabels = {
  draft: '待取证',
  in_review: '待复核',
  needs_work: '待整改',
  verified: '已验证',
}

export const evidenceKindLabels = {
  observation: '检查记录',
  remediation: '整改记录',
  retest: '复测记录',
}

export const eventLabels = {
  created: '创建检查',
  evidence: '登记证据',
  postgres_binding: '关联 PostgreSQL 采集任务',
  submitted: '提交复核',
  changes: '要求整改',
  verified: '复核通过',
}

export interface Requirement {
  code: string
  title: string
  citation: string
  sourceUrl: string
  summary: string
}

export interface Check {
  id: number
  djangoId?: number
  assetId: number
  assetName: string
  assetSystem: string
  activity?: ActivityLink
  template?: TemplateBinding
  requirement: Requirement
  ownerTeam: string
  note: string
  status: keyof typeof checkStatusLabels
  updatedAt: string
}

export interface CheckPage {
  items: Check[]
  nextCursor?: number | null
}

export interface Evidence {
  id: number
  kind: keyof typeof evidenceKindLabels
  sourceSystem: string
  sourceRecord: string
  summary: string
  observedAt: string
  createdAt: string
  recordedBy: string
  automationJobId?: number
  automationRunId?: string
  postgres?: PostgresEvidence
  feishu?: {
    memberPresent: boolean
    permission?: string
    targetDigest: string
    snapshotDigest: string
  }
}

export interface CheckEvent {
  id: number
  action: keyof typeof eventLabels
  actor: string
  note: string
  createdAt: string
}

export interface CheckDetail extends Check {
  evidence: Evidence[]
  events: CheckEvent[]
  canAddEvidence: boolean
  canSubmit: boolean
  canReview: boolean
  canVerify: boolean
  feishuTargetDigest?: string
  postgresJobId?: number
  availablePostgresJobId?: number
  reviewRevision?: number
}

export function canManageChecks(role: string) {
  return role === 'owner' || role === 'operator'
}
