import { validateAIAuthorizationExpiry, type AIRecord } from './ai-security-editor.ts'

export type AIApprovalDecision = 'approved' | 'rejected'
export interface AIApprovalReviewIntent { id: string; decision: AIApprovalDecision; note: string }
export interface AIApprovalReviewPlan extends AIApprovalReviewIntent { record: AIRecord }
const digest = /^[a-f0-9]{64}$/
const bindingFields = ['id','configurationRevision','catalogDigest','requestDigest','requestId','issuedAt','expiresAt','applicationId','principalId','toolId','action','target','taskId','leaseChainDigest','leaseProtocolVersion'] as const

export function aiApprovalId(value: unknown): string {
  if (typeof value === 'number' && Number.isSafeInteger(value) && value > 0) return String(value)
  if (typeof value === 'string' && /^[1-9][0-9]{0,18}$/.test(value) && BigInt(value) <= 9223372036854775807n) return value
  throw new Error('批准记录编号无效，请刷新记录后重试。')
}

export function aiApprovalReviewNote(value: string): string {
  const note = value.trim()
  if (!note || new TextEncoder().encode(note).length > 1000) throw new Error('请填写独立复核意见，最多 1000 字节。')
  return note
}

function argumentsObject(value: unknown): Record<string,string> {
  // Empty Go maps may be omitted from the stored request and returned as null.
  if (value === null || value === undefined) return {}
  if (!value || typeof value !== 'object' || Array.isArray(value) || Object.values(value).some(item => typeof item !== 'string')) throw new Error('限定参数不完整，不能提交复核。')
  return value as Record<string,string>
}

export function sameAIApprovalBinding(before: AIRecord, after: AIRecord): boolean {
  try {
    const args = (value: unknown) => Object.entries(argumentsObject(value)).sort(([a],[b]) => a.localeCompare(b))
    return bindingFields.every(key => before[key] === after[key]) && JSON.stringify(args(before.arguments)) === JSON.stringify(args(after.arguments))
  } catch { return false }
}

export function buildAIApprovalReviewPlan(record: AIRecord, intent: AIApprovalReviewIntent, currentRevision: number, now = Date.now()): AIApprovalReviewPlan {
  if (aiApprovalId(record.id) !== intent.id) throw new Error('返回记录与所选申请不一致，请返回列表重新核对。')
  if (intent.decision !== 'approved' && intent.decision !== 'rejected') throw new Error('请选择明确的复核决定。')
  if (record.status !== 'proposed') throw new Error('本申请已办理或已失效，此处仅供核对，不再提交复核。')
  if (!Number.isSafeInteger(currentRevision) || currentRevision < 1 || record.configurationRevision !== currentRevision) throw new Error('配置修订已变化，请返回列表核对并重新申请。')
  if (typeof record.expiresAt !== 'string' || typeof record.issuedAt !== 'string') throw new Error('申请时效不完整，不能提交复核。')
  validateAIAuthorizationExpiry(record.expiresAt); validateAIAuthorizationExpiry(record.issuedAt)
  if (!Number.isFinite(now) || Date.parse(record.expiresAt) <= now) throw new Error('本申请已到期或未记录有效期限，不能提交复核。')
  for (const key of ['requestId','issuedAt','applicationId','principalId','toolId','action','target']) if (typeof record[key] !== 'string' || !String(record[key]).trim()) throw new Error('申请绑定事实不完整，不能提交复核。')
  if (!Number.isFinite(Date.parse(String(record.issuedAt))) || Date.parse(String(record.issuedAt)) >= Date.parse(String(record.expiresAt))) throw new Error('申请时效不完整，不能提交复核。')
  if (!digest.test(String(record.requestDigest)) || !digest.test(String(record.catalogDigest))) throw new Error('申请缺少有效请求或目录摘要，不能提交复核。')
  if (record.taskId !== undefined && record.taskId !== '' && (typeof record.taskId !== 'string' || !digest.test(String(record.leaseChainDigest)) || record.leaseProtocolVersion !== 'scarlet-task-lease-v1')) throw new Error('任务申请缺少完整祖先租约绑定，不能提交复核。')
  argumentsObject(record.arguments)
  return { id: intent.id, decision: intent.decision, note: aiApprovalReviewNote(intent.note), record: structuredClone(record) }
}
