import type { Check } from './workflow'
import type { RuntimeDetectionPolicy } from './runtime-policies'

export const runtimeRuleLabels = {
  over_collection:'超出允许数据类型',credential_cleartext_transport:'认证材料明文传输',subject_multi_ip:'同一账号多IP',subject_multi_region:'同一账号多来源地域',client_region_mismatch:'来源地域超出允许范围',recipient_scope:'共享接收方超范围',destination_scope:'目的地超范围',volume_limit:'传输量超阈值',subject_repeated_access:'主体反复访问',
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
