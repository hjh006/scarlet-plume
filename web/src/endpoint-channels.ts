export type EndpointChannel = 'usb' | 'print' | 'clipboard'
export type EndpointMode = 'monitor' | 'block_sensitive' | 'block_all' | 'clear_sensitive' | 'clear_all'
export type EndpointEnforcementScope = 'controlled_operations' | 'native_control' | 'session_response'
export type EndpointCapabilityScope = 'observation' | EndpointEnforcementScope

export const endpointChannelLabels: Record<EndpointChannel, string> = {
  usb: 'USB / 移动介质', print: '打印', clipboard: '剪贴板',
}
export const endpointModeLabels: Record<EndpointMode, string> = {
  monitor: '仅观察', block_sensitive: '阻止敏感内容', block_all: '阻止全部动作',
  clear_sensitive: '指定格式命中后条件清除', clear_all: '变化后条件清除全部当前内容',
}
export const endpointScopeLabels: Record<EndpointEnforcementScope, string> = {
  controlled_operations: '指定的受控动作', native_control: '设备原生控制',
  session_response: '当前登录会话的变化后响应',
}

export const endpointNativeActionLabels: Record<EndpointChannel, string> = {
  usb: '可移动存储写保护', print: '暂停指定打印队列', clipboard: '禁用 RDP 剪贴板重定向',
}
export const endpointNativeBoundaries: Record<EndpointChannel, string> = {
  usb: '仅针对可移动存储写入。配置回读不等于具体设备的写入效果已完成功能验证。',
  print: '仅暂停客户端预先配置的打印队列。仍可能接收提交，已经开始的任务可能继续，不表示已撤回打印内容。',
  clipboard: '仅限制 RDP 会话的剪贴板重定向，不拦截本地应用间的剪贴板；既有会话可能需要重连并验证。',
}
export const endpointNativeTargetScopes: Record<EndpointChannel, string> = {
  usb: 'removable_storage_write', print: 'configured_print_queues', clipboard: 'rdp_clipboard_redirection',
}
export const endpointPrivilegeLabels: Record<string, string> = {
  administrator: '源端报告管理员权限', system: '源端报告 System 服务权限',
  not_elevated: '尚未取得所需提升权限', unknown: '权限状态未知',
  interactive_user: '源端报告当前交互用户会话',
}
export const endpointNativePhaseLabels: Record<string, string> = {
  prepared: '来源报告已准备，尚未写入',
  configured: '来源报告已配置（未证明阻断）', readback: '配置回读匹配（效果待验证）',
  functional_verified: '来源报告功能验证完成', rolled_back: '来源报告已恢复采用前状态',
  conflict: '设置冲突，未强行覆盖', failed: '来源报告操作失败',
}
export const endpointNativePhaseTitles: Record<string, string> = {
  prepared: '准备与原状态保全', configured: '配置写入', readback: '配置回读',
  functional_verified: '功能验证', rolled_back: '恢复采用前状态', conflict: '设置冲突', failed: '失败回执',
}
export const endpointNativeStateLabels: Record<string, string> = {
  ...endpointNativePhaseLabels,
  issued: '已签发租约，等待源端配置', pending: '等待原生控制回执',
  rollback_pending: '等待源端回滚', awaiting_rollback: '策略已停止批准，等待回滚回执',
  expired: '策略已过期，仍需核对回滚', unavailable: '当前来源或采用能力不可用',
  unsupported: '来源报告不支持此原生控制',
}

export const endpointChannelCoverage: Record<EndpointChannel, string> = {
  usb: '设备插拔记录不能证明文件已写入移动介质。',
  print: '打印队列观察不含打印正文，不能据此确认内容已检查或已阻止。',
  clipboard: '剪贴板变化后的观察不等于对所有应用的预先阻断。',
}
export const endpointSessionDeploymentLabels: Record<string, string> = {
  pending: '尚无监听加载回执', loaded: '来源报告监听已加载（不代表已经清除）',
  listening: '来源报告监听已加载（不代表已经清除）', stopped: '来源报告监听已停止（不恢复内容）',
  awaiting_stop: '不再批准后续响应，等待来源停止状态',
  failed: '来源报告监听加载失败', unsupported: '来源报告不支持会话响应',
  expired: '有效期已结束，后续响应须停止', unavailable: '当前来源、登录会话或配置不可用',
}
export const endpointSessionCurrentReasons: Record<string, string> = {
  session_changed: '登录会话已变化，旧版本不能再次采用', configuration_changed: '会话配置已变化，须重新申请审批',
  ended: '此版本已结束，不能重新采用', stopped: '来源报告监听已停止（不恢复内容）',
  source_unavailable: '来源当前不可用', capability_changed: '会话响应能力已变化',
}
export const endpointSessionCodeLabels: Record<string, string> = {
  session_clear_intent: '仅登记了清除意图，最终结果仍未知', session_clear_failed: '源端清除调用失败，不能认定已清除',
  session_capture_unknown: '捕获不完整或未知，未形成确定结论', session_sequence_unknown: '无法核对内容序列，未形成清除结论',
  session_policy_inactive: '策略已不可采用，不能继续响应', session_changed: '登录会话已变化，未继续清除',
}
export const endpointSessionResultLabels: Record<string, string> = {
  observed: '仅观察，未清除', cleared_after_change: '来源报告变化后已清除（未证明阻断）',
  stale: '内容已变化，本次未清除', busy: '剪贴板占用，未完成响应',
  unavailable: '会话或剪贴板当前不可用', unsupported: '格式不支持或覆盖不足',
  unknown: '结果未知，不能认定安全', not_required: '未触发本次清除条件（不代表安全）',
}

export interface EndpointEventScope {
  sourceId: number
  deviceRef: string
  channel?: EndpointChannel
}

export interface EndpointCapability {
  channel: EndpointChannel
  scope: EndpointCapabilityScope
  supported: boolean
  modes: EndpointMode[]
  executor: string
  reason: string
  targetScope?: string
  targetRefs?: string[]
  configDigest?: string
  privilege?: 'administrator' | 'system' | 'not_elevated' | 'unknown' | 'interactive_user'
}

export interface EndpointDevice {
  sourceId: number
  sourceName: string
  deviceRef: string
  os: string
  agentVersion: string
  lastSeenAt: string
  observedAt: string
  capabilities: EndpointCapability[]
  online: boolean
  current: boolean
}

export interface EndpointReceipt {
  id?: number
  sourceId?: number
  receiptRef: string
  deviceRef: string
  policyId: number
  policyRevision: number
  policyDigest: string
  channel: EndpointChannel
  scope: EndpointEnforcementScope
  executor: string
  status: 'applied' | 'loaded' | 'stopped' | 'failed' | 'unsupported'
  code: string
  observedAt: string
  receivedAt?: string
  snapshotDigest?: string
  phase?: string
  leaseRef?: string
  operationRef?: string
  action?: 'apply' | 'rollback'
  targetScope?: string
  targetRefs?: string[]
  configDigest?: string
  priorStateDigest?: string
  stateDigest?: string
  verificationMethod?: string
  verificationRef?: string
}

export interface EndpointNativeLease {
  leaseRef: string
  applyOperationRef: string
  rollbackOperationRef: string
  state: string
  issuedAt: string
  expiresAt: string
  priorStateDigest?: string
  appliedStateDigest?: string
  plannedStateDigest?: string
  preparedAt?: string
  updatedAt: string
}

export interface EndpointSessionEvent {
  id: number
  sourceId: number
  eventRef: string
  operationRef: string
  deviceRef: string
  sessionRef: string
  policyId: number
  policyRevision: number
  policyDigest: string
  scope: string
  executor: string
  configDigest: string
  status: string
  code: string
  observedAt: string
  responseAt: string
  capture: string
  categories: string[]
  sequenceRef?: string
  currentSequenceRef?: string
  contentRef?: string
  clearAttempted: boolean
  sequenceMatched: boolean
  clipboardEmpty: boolean
  receivedAt: string
  snapshotDigest: string
  historical: boolean
  currentResponse: boolean
}

export interface EndpointPolicy {
  id: number
  sourceId: number
  sourceName: string
  deviceRef: string
  os: string
  channel: EndpointChannel
  scope: EndpointEnforcementScope
  mode: EndpointMode
  categories: string[]
  revision: number
  reason: string
  durationMinutes: number
  createdAt: string
  expiresAt: string
  creator: string
  agentVersion?: string
  capability?: EndpointCapability
  approver?: string
  approvedAt?: string
  decisionNote?: string
  endedAt?: string
  sessionCurrent?: boolean
  currentReason?: string
  status: string
  digest: string
  capabilityDigest: string
  executor: string
  current: boolean
  deploymentState: string
  canReview: boolean
  latestReceipt?: EndpointReceipt
  receipts?: EndpointReceipt[]
  targetScope?: string
  targetRefs?: string[]
  configDigest?: string
  nativeLease?: EndpointNativeLease
  sessionEvents?: EndpointSessionEvent[]
  sessionEventsTotal?: number
  sessionEventsTruncated?: boolean
  sessionCoverageMissing?: string[]
}

export interface EndpointPolicyExport {
  id: number
  policyId: number
  createdAt: string
  actor: string
  snapshotDigest: string
  snapshot: unknown
}

export const endpointPolicyStatusLabels: Record<string, string> = {
  draft: '等待独立审批', approved: '已独立批准',
  rejected: '已拒绝', revoked: '已撤销', superseded: '已有新版本',
}
export const endpointReceiptLabels: Record<string, string> = {
  applied: '来源报告已加载', failed: '来源报告应用失败', unsupported: '来源报告不支持',
}
export const endpointDeploymentLabels: Record<string, string> = {
  pending: '尚无源端应用回执', awaiting_receipt: '等待源端应用回执',
  applied: '来源报告已加载', failed: '来源报告应用失败', unsupported: '来源报告不支持',
  expired: '策略已过期', unavailable: '来源或采用能力当前不可用',
}
export const endpointCategoryLabels: Record<string, string> = {
  email: '邮箱', phone: '手机号', national_id: '身份证格式', financial_account: '银行卡格式',
}

export function endpointCapability(device: EndpointDevice | undefined, channel: EndpointChannel, scope: EndpointCapabilityScope) {
  return device?.capabilities.find(capability => capability.channel === channel && capability.scope === scope)
}
