import type { Check } from './workflow'

export const runtimeRuleLabels = {
  unregistered_success: '未登记接口成功响应',
  deprecated_success: '已弃用接口成功响应',
}

export interface RuntimeFindingContext {
  id: number
  service: string
  method: string
  route: string
  rule: keyof typeof runtimeRuleLabels
  sourceId: number
  source: string
  baselineScanId: number
  firstSeen: string
  lastSeen: string
  retestAt?: string
}

export interface RuntimeFinding extends RuntimeFindingContext {
  checkId: number
  checkStatus: Check['status']
  previousCheckId?: number
  risk?: RuntimeRisk
}

export const runtimeRiskSeverityLabels = {
  low: '低',
  medium: '中',
  high: '高',
  critical: '严重',
}

export interface RuntimeRisk {
  revision: number
  severity: keyof typeof runtimeRiskSeverityLabels
  ownerTeam: string
  dueDate?: string
  reason: string
  updatedBy: string
  updatedAt: string
}
