import type { AIRecord } from './ai-security-editor'

export interface RuntimeSource { id:number; name:string; applicationId:string; catalogDigest:string; createdAt:string; revokedAt?:string; lastSeen?:string; receiptCount:number; current:boolean; tokenVersion:number }
export interface RuntimeEvent { id:number; sourceId:number; eventId:string; applicationId:string; catalogDigest:string; requestDigest:string; kind:string; outcome:string; executionStatus:string; occurredAt:string; receivedAt:string; stage:string; current:boolean; record?:AIRecord }
export interface RuntimeEvidencePage { sources:RuntimeSource[]; events:RuntimeEvent[]; requests?:AIRecord[]; limit:number; coverage?:string[]; hasMore:boolean; nextCursor:string }
export interface RuntimeFilters { sourceId:string; kind:string; outcome:string }

export function runtimeEvidenceQuery(filters:RuntimeFilters,before=''):string {
  const query=new URLSearchParams({limit:'100'})
  if(before) {
    query.set('before',before)
  }
  if(filters.sourceId.trim()) {
    const sourceId=filters.sourceId.trim()
    if(!/^[1-9]\d*$/.test(sourceId) || !Number.isSafeInteger(Number(sourceId)))throw new Error('来源编号应为正整数。')
    query.set('sourceId',sourceId)
  }
  if(filters.kind) {
    if(!['rag','model','tool','output'].includes(filters.kind))throw new Error('请选择受支持的回执类型。')
    query.set('kind',filters.kind)
  }
  if(filters.outcome) {
    if(!['allow','deny','approval_required','unknown'].includes(filters.outcome))throw new Error('请选择受支持的回执决策。')
    query.set('outcome',filters.outcome)
  }
  return query.toString()
}
