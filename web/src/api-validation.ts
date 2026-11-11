export const apiValidationRules = {
  bola: '双测试账号对象级授权验证',
  weak_credential: '显式弱凭据验证',
}
export const apiValidationRuleDescriptions = {
  bola: '由 Agent 在本地确认对象归属，并对照两个已授权测试账号是否获得同一测试对象。仅 HTTP 200 不构成越权证据。',
  weak_credential: '只使用清单中明确授权的测试凭据，核对是否被接受及是否命中所选弱规则。',
}
export const apiValidationPlanStates: Record<string, string> = { draft: '待独立批准', approved: '已批准', disabled: '已停用' }
export const apiValidationRunStates: Record<string, string> = { leased: '来源执行中', complete: '已完成', failed: '未完成验证' }
export const apiValidationOutcomes: Record<string, string> = {
  vulnerable: '后台确认风险', not_observed: '本轮未观测到风险', inconclusive: '无法判定',
}
export interface APIValidationPlanInput {
  revision: number; sourceId: number; name: string; service: string; method: 'GET'; route: string
  targetOrigin: string; manifestRef: string; rules: (keyof typeof apiValidationRules)[]; intervalMinutes: number
}
export interface APIValidationPlan extends APIValidationPlanInput {
  id: number; state: string; createdBy: number; approvedBy?: number; approvedAt?: string
  nextRunAt: string; leaseExpiresAt?: string; updatedAt: string; canApprove?: boolean
}
export interface APIValidationRuleResult {
  rule: string; outcome: string; ownerStatus: number; otherStatus?: number; otherOwnedStatus?: number; otherAccountConfirmed: boolean
  ownerConfirmed: boolean; sameObject: boolean; credentialAccepted: boolean; weakRuleMatched: boolean
  comparisonComplete: boolean; requests: number; gaps: string[]
}
export interface APIValidationResult {
  planId: number; revision: number; runRef: string; observedAt: string; mode: 'validation' | 'connection'
  results: APIValidationRuleResult[]
}
export interface APIValidationRun {
  id: number; planId: number; revision: number; runRef: string; plan: APIValidationPlan; state: string; mode: 'validation' | 'connection'
  result?: APIValidationResult; eventIds: string[]; checkIds?: number[]; projectionState?: string
  errorCode?: string; createdAt: string; completedAt?: string
}
export interface APIValidationSource { id: number; name: string; kind: string; ownerTeam: string; revokedAt?: string; lastSeen?: string }
export function validationStatusCode(code?: number) { return code && code >= 100 && code <= 599 ? 'HTTP ' + code : '未取得响应状态' }
const validationGapLabels: Record<string, string> = {
  plan_changed: '任务版本已变化，旧运行结果不能用于当前范围',
  lease_expired: '运行领取期限已过',
  lease_lost: '运行领取状态已失效',
  source_revoked: '来源已撤销',
  owner_unconfirmed: '未确认 A 测试账号对自有测试对象的访问',
  other_account_unconfirmed: '未确认独立 B 测试账号及其自有对象访问',
  comparison_missing: '本地对照尚未完成',
  cross_response_unexpected: '交叉访问响应不满足可判定条件',
  target_not_private: '目标不在获准的私有或 loopback 范围',
  manifest_mismatch: '本地清单与批准范围不匹配',
  credential_missing: '本地清单缺少所需的测试凭据',
  network_error: '来源端未能完成连接或请求',
  invalid_json: '来源响应不是预期的受支持 JSON',
  response_too_large: '来源响应超过本轮验证大小范围',
  connection_only: '本轮仅作连接诊断',
  projection_failed: '运行结果已留存，关联检查汇总未完成',
}
export function validationGap(code: string) { return validationGapLabels[code] || code }
