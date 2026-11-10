export const automationProviders = {
  postgresql: 'PostgreSQL',
  openmetadata: 'OpenMetadata',
  feishu: '飞书文档权限',
}

export type AutomationProvider = keyof typeof automationProviders

export const automationRunStatuses = {
  success: '已采集',
  error: '采集失败',
  skipped: '无需写入',
}

const resultLabels: Record<string, string> = {
  collection_failed: '采集端未能读取来源。请查看采集端日志，核对来源配置和权限。',
  collection_timeout: '采集超时。请查看采集端网络和来源服务状态。',
  check_closed: '检查当前阶段已锁定证据。',
  unchanged: '采集结果与本轮已登记的证据一致。',
  awaiting_remediation: '已读取权限结果，等待可用于复测的整改记录。',
  stale_observation: '观察时间不满足本轮复测要求。',
}

export interface AutomationRun {
  id: number
  jobId: number
  runId: string
  status: keyof typeof automationRunStatuses
  code?: string
  observedAt: string
  createdAt: string
  createdCount: number
  existingCount: number
  memberPresent?: boolean
  permission?: string
  evidenceId?: number
}

export interface AutomationJob {
  id: number
  provider: AutomationProvider
  source?: string
  checkId?: number
  createdAt: string
  revokedAt?: string
  latestRun?: AutomationRun
}

export interface IssuedAutomationJob {
  job: AutomationJob
  token: string
}

export function automationRunDetail(run: AutomationRun) {
  if (run.code) return resultLabels[run.code] || '查看采集端运行记录。'
  if (run.memberPresent !== undefined) {
    return run.memberPresent ? '检出目标协作者的直接授权。' : '未检出目标协作者的直接授权。'
  }
  return `新增 ${run.createdCount} 项，已有 ${run.existingCount} 项。`
}

export function automationRunClass(status: AutomationRun['status']) {
  if (status === 'success') return 'status-verified'
  if (status === 'error') return 'status-in_review'
  return 'status-draft'
}
