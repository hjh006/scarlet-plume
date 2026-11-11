import type { Check } from './workflow'
import type { RuntimeDetectionPolicy } from './runtime-policies'

export const runtimeRuleLabels = {
  unregistered_success: '未登记接口成功响应',
  deprecated_success: '已弃用接口成功响应',
  sensitive_response: '敏感数据响应', credential_response: '疑似凭据响应', missing_auth: '需认证接口匿名成功',
  subject_bulk_access: '主体访问频次超阈值', error_spike: '接口错误比例超阈值', off_hours: '非工作时段访问',
  sql_injection_signal: '疑似 SQL 注入模式', xss_signal: '疑似 XSS 模式', path_traversal_signal: '疑似路径穿越模式',
}

export interface RuntimeDetection {
  sourceId: number; source: string; policy: RuntimeDetectionPolicy
  windowStart: string; windowEnd: string; requests: number; matched: number; observed: number
  subjectRef?: string; categories: string[]; warnings: string[]
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
  detection?: RuntimeDetection
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
