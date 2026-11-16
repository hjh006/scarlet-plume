import { ApiError, retryAfterSeconds } from './api.ts'
import { validateAIAuthorizationExpiry } from './ai-security-editor.ts'
import type { ReportArchive } from './reports'

export interface ArchiveRetention {revision:number;state:'review_required'|'retained'|'rotation_allowed';retainUntil?:string;legalHold:boolean;holdReason:string;basis:string;updatedBy?:number;updatedAt:string;contributors:number[]}
export interface RetainedReportArchive extends ReportArchive {retention:ArchiveRetention;contentDigest:string;summaryDigest:string;integrityStatus:string}
export interface RetainedReportArchivePage {items:RetainedReportArchive[];retentionPerJob:number;nextCursor?:number;hasMore:boolean;boundary:string;externalStorageStatus:string}
export interface ArchiveRetentionEvent {id:number;action:string;actorId?:number;actor:string;note:string;evidenceId?:number;snapshot:Record<string,unknown>;createdAt:string}
export interface ArchiveRetentionHistory {items:ArchiveRetentionEvent[];nextCursor?:number;hasMore:boolean}
export interface ArchiveIntegrity {archiveId:number;contentDigest:string;summaryDigest:string;contentBytes:number;integrityStatus:string;algorithm:string;canonicalization:string;externalStorageStatus:string;boundary:string}
export interface ArchiveRetentionDraft {action:string;basis:string;retainUntil:string;evidenceId:string}
export const archiveRetentionStates:Record<string,string>={review_required:'用途与期限待核定，保留中',retained:'按已登记期限保留',rotation_allowed:'已独立批准参与轮换'}
export const archiveRetentionActions:Record<string,string>={hold:'设置保全保护',extend:'设置或延长保留期限',release_hold:'独立解除保全保护',allow_rotation:'独立批准参与轮换'}
export function archiveActions(record:RetainedReportArchive,role:string,now=Date.now()):string[] {
  const actions:string[]=[]
  if(['owner','operator'].includes(role)){if(!record.retention.legalHold)actions.push('hold');actions.push('extend')}
  if(['owner','reviewer'].includes(role)) {
    if(record.retention.legalHold)actions.push('release_hold')
    else if(record.retention.state!=='rotation_allowed' && (!record.retention.retainUntil || Date.parse(record.retention.retainUntil)<=now))actions.push('allow_rotation')
  }
  return actions
}
export function archiveRetentionPayload(record:RetainedReportArchive,draft:ArchiveRetentionDraft,now=Date.now()) {
  if(!Number.isSafeInteger(record.retention.revision) || record.retention.revision<1)throw new Error('归档留存修订无效，请重新读取。')
  if(Array.from(draft.basis.trim()).length>600 || /[\x00\r]/.test(draft.basis))throw new Error('留存依据最多 600 字，不能包含无效控制字符。')
  if(!Object.prototype.hasOwnProperty.call(archiveRetentionActions,draft.action) || !draft.basis.trim())throw new Error('请选择留存动作并填写实际依据。')
  const payload:{revision:number;action:string;basis:string;retainUntil?:string;evidenceId?:number}={revision:record.retention.revision,action:draft.action,basis:draft.basis.trim()}
  if(draft.action==='extend') {
    validateAIAuthorizationExpiry(draft.retainUntil)
    const until=Date.parse(draft.retainUntil)
    if(!Number.isFinite(until) || until<=now || record.retention.retainUntil && until<=Date.parse(record.retention.retainUntil))throw new Error('新期限须带时区、晚于当前时间且长于原保留期限。')
    payload.retainUntil=draft.retainUntil
  }
  if(['allow_rotation','release_hold'].includes(draft.action)) {
    if(!/^[1-9]\d*$/.test(draft.evidenceId) || !Number.isSafeInteger(Number(draft.evidenceId)))throw new Error('独立解除或批准轮换须选择当前工作区真实检查证据。')
    payload.evidenceId=Number(draft.evidenceId)
  }
  if(draft.action==='allow_rotation' && (record.retention.legalHold || record.retention.retainUntil && Date.parse(record.retention.retainUntil)>now))throw new Error('保全保护未解除或保留期限未到，不能批准参与轮换。')
  return payload
}
export async function verifyArchiveResponse(bytes:ArrayBuffer,contentDigest:string|null):Promise<boolean> {
  if(!contentDigest)return false
  const expected=/^sha-256=:([A-Za-z0-9+/]{43}=):$/.exec(contentDigest.trim())?.[1]
  if(!expected)throw new Error('导出响应的 Content-Digest 格式未受支持，本次未发起文件保存。')
  const digest=new Uint8Array(await crypto.subtle.digest('SHA-256',bytes))
  const actual=btoa(String.fromCharCode(...digest))
  if(actual!==expected)throw new Error('导出响应内容与 SHA-256 摘要不匹配，本次未发起文件保存。请重新读取并核对来源。')
  return true
}
export async function downloadArchive(path:string,filename:string,signal:AbortSignal,expectedArchiveDigest?:string):Promise<{receiptId:string;responseVerified:boolean}> {
  const response=await fetch(path,{credentials:'same-origin',signal:AbortSignal.any([signal,AbortSignal.timeout(120000)])})
  if(!response.ok){const body=await response.json().catch(()=>null);throw new ApiError(response.status,typeof body?.error==='string'?body.error.slice(0,300):'',retryAfterSeconds(response.headers.get('Retry-After')))}
  const archiveDigest=response.headers.get('X-Archive-Digest')
  if(expectedArchiveDigest && archiveDigest && archiveDigest!==expectedArchiveDigest)throw new Error('导出采用的归档摘要与当前选择不一致，本次未发起文件保存。请刷新归档后核对。')
  const bytes=await response.arrayBuffer()
  const responseVerified=await verifyArchiveResponse(bytes,response.headers.get('Content-Digest'))
  if(signal.aborted)throw new DOMException('已取消','AbortError')
  const blob=new Blob([bytes],{type:response.headers.get('Content-Type') || 'application/octet-stream'})
  const url=URL.createObjectURL(blob),link=document.createElement('a')
  link.href=url;link.download=filename;document.body.append(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(url),1000)
  return {receiptId:response.headers.get('X-Export-Receipt-Id') || '',responseVerified}
}
