import type { AIRecord } from './ai-security-editor'
export type AIHistoryCollection='decisions'|'approvals'|'evaluations'|'regression-packs'
export interface AIHistoryFilters {kind:string;outcome:string;status:string}
export interface AIHistoryPage {items:AIRecord[];hasMore:boolean;nextCursor:string;limit:number;engineVersion?:string;inspectionVersion?:string;latestGate?:string;requiredDimensions?:string[]}
export const aiHistoryLabels:Record<AIHistoryCollection,string>={decisions:'策略决策历史',approvals:'限定工具动作批准历史',evaluations:'安全回归历史','regression-packs':'版本化测试包历史'}
export const aiHistoryStatuses:Partial<Record<AIHistoryCollection,Record<string,string>>>={approvals:{proposed:'待独立批准',approved:'已批准',rejected:'已拒绝',invalidated:'已失效'},evaluations:{passed:'通过',failed:'未通过'}}
export function aiHistoryQuery(collection:AIHistoryCollection,filters:AIHistoryFilters,before=''):string {
  const query=new URLSearchParams({limit:'100'})
  if(before)query.set('before',before)
  if(collection==='decisions') {
    if(filters.kind){if(!['rag','model','tool','unknown'].includes(filters.kind))throw new Error('决策类型筛选无效。');query.set('kind',filters.kind)}
    if(filters.outcome){if(!['allow','deny','approval_required'].includes(filters.outcome))throw new Error('决策结果筛选无效。');query.set('outcome',filters.outcome)}
  } else if(collection!=='regression-packs' && filters.status) {
    if(!Object.prototype.hasOwnProperty.call(aiHistoryStatuses[collection] || {},filters.status))throw new Error('历史状态筛选无效。')
    query.set('status',filters.status)
  }
  return query.toString()
}
export function aiHistoryRecordId(value:string|number):string {
  const id=String(value)
  if(!/^[1-9]\d*$/.test(id) || !Number.isSafeInteger(Number(id)))throw new Error('历史记录编号应为安全范围内的正整数。')
  return id
}
