export const operationsSourceKinds = { security_events: '安全事件集成', postgres_control: 'PostgreSQL 权限执行器' }
export type OperationsSourceKind = keyof typeof operationsSourceKinds
export interface OperationsSource {
  id: number; name: string; kind: OperationsSourceKind; ownerTeam: string
  scope: { jobId?: number; source?: string; host?: string; port?: number; database?: string; schemas?: string[] }
  createdAt: string; lastSeen?: string; revokedAt?: string
}
export interface OperationsOverview {
  generatedAt: string; windowDays: number
  counts: { sources: number; events: number; openEvents: number; controls: number; pendingApprovals: number; appliedControls: number; overdueRisks: number }
  teams: { name: string; assets: number; checks: number; openChecks: number; overdue: number }[]
  trend: { day: string; events: number; verified: number }[]
}
export const securityEventCategoryLabels: Record<string, string> = {
  api: 'API 安全', data_leak: '数据泄露信号', access: '访问控制', config: '配置风险', malware: '恶意活动信号', backup: '备份安全', key: '密钥安全', custom: '自定义信号',
}
export const securityEventOutcomeLabels: Record<string, string> = { observed: '来源报告发现', cleared: '来源报告消除', activity: '来源活动审计' }
export const operationsSeverityLabels: Record<string, string> = { low: '低', medium: '中', high: '高', critical: '严重' }
export interface OperationsSecurityEvent {
  id: number; sourceId: number; source: string; eventId: string; observedAt: string
  category: string; rule: string; severity: string; outcome: string
  assetId?: number; checkId: number; checkStatus: Check['status'] | ''
  service?: string; method?: string; route?: string; count: number; windowSeconds: number
}
export const controlKindLabels = { block_api: '临时阻断 API', revoke_select: '撤销表级 SELECT' }
export const controlStatusLabels: Record<string, string> = {
  proposed: '待独立审批', approved: '已审批 · 待来源执行', applied: '来源回执执行成功', failed: '来源回执执行失败', rollback_requested: '已申请回滚 · 待回执', rolled_back: '来源回执已回滚',
}
export const controlEventLabels: Record<string, string> = {
  proposed: '创建处置申请', created: '创建处置申请', approved: '独立审批', applied: '来源执行回执', failed: '失败回执', rollback_requested: '申请回滚', rolled_back: '回滚回执', expired: '有效期到期',
}
export interface ControlActionInput {
  kind: keyof typeof controlKindLabels; runtimeSourceId?: number; operationsSourceId?: number; assetId?: number; checkId?: number
  method?: string; route?: string; role?: string; reason: string; durationMinutes: number
}
export interface ControlResult {
  phase: string; success: boolean; code: string; hadSelect?: boolean; hasSelect?: boolean; observedAt: string
}
export interface ControlAction {
  id: number; kind: keyof typeof controlKindLabels; actor: string; approvedBy?: string
  runtimeSourceId?: number; operationsSourceId?: number; assetId?: number; checkId?: number
  spec: { service?: string; method?: string; route?: string; host?: string; port?: number; database?: string; schema?: string; table?: string; role?: string; expectedSelect: boolean }
  reason: string; status: string; revision: number; result?: ControlResult; expiresAt: string; createdAt: string; updatedAt: string
  events?: { action: string; actor: string; note: string; automated: boolean; createdAt: string }[]
}
export interface OperationsRuntimeSource { id: number; name: string; service: string; kind: 'gateway' | 'otlp'; revokedAt?: string }
export interface OperationsRuntimeRoute { sourceId: number; source: string; service: string; method: string; route: string }
const controlResultLabels: Record<string, string> = {
  route_blocked: '阻断规则已加载', route_released: '阻断规则已移除', select_revoked: 'SELECT 授权已撤销', select_restored: '原始授权已恢复',
  scope_changed: '来源范围已变化', database_unavailable: '来源数据库当前不可用', configuration_mismatch: '执行器配置与绑定范围不一致',
  applied: '已执行', blocked: '阻断规则已加载', revoked: 'SELECT 授权已撤销', rolled_back: '已回滚', restored: '原始授权已恢复',
  expired: '动作已到期', already_absent: '原先未有该授权', already_applied: '目标状态已符合', source_unavailable: '来源不可用',
  scope_mismatch: '来源范围与申请不一致', stale_scope: '来源范围已变化', permission_denied: '执行账号权限不足', execution_failed: '来源执行失败', rollback_failed: '来源回滚失败',
}
export function controlResultLabel(code: string) { return controlResultLabels[code] || '查看来源回执与执行记录（' + code + '）' }
export function controlEventNote(note: string, automated: boolean) { return automated ? controlResultLabels[note] || note : note }
import type { Check } from './workflow'
