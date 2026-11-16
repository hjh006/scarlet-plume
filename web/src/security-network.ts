export type NetworkHistoryKind = 'network-bindings' | 'network-changes' | 'snapshots'
export interface NetworkBinding { id:number; revision:number; site:string; circuit:string; vrf:string; applicationRef:string; assetIds:number[]; activityIds:number[]; region:string; receiver:string; purpose:string; evidenceRef:string; updatedAt:string; evidenceType:string }
export interface NetworkChange { id:number; bindingId:number; bindingRevision:number; binding:NetworkBinding; proposedRegion:string; proposedApplicationRef:string; reason:string; affectedAssetIds:number[]; affectedActivityIds:number[]; warnings:string[]; status:string; createdBy:number; submittedByCurrentUser:boolean; createdAt:string; reviewer?:number; reviewedAt?:string; reviewNote?:string }
export interface NetworkSnapshot { id:number; digest:string; createdAt:string; findingsCount:number }
export interface NetworkHistoryPage<T> { items:T[]; limit:number; hasMore:boolean; nextCursor?:string; generatedAt:string; coverage:string }
export interface NetworkHistoryFilters { q:string; status:string; bindingId:string; createdFrom?:string; createdTo?:string }
export function networkRecordId(value:string):number {
  if (!/^[1-9]\d*$/.test(value) || !Number.isSafeInteger(Number(value))) throw new Error('记录编号应为正整数。')
  return Number(value)
}
export function networkScopeIds(value:string):number[] {
  return [...new Set(value.split(/[\s,，]+/).filter(Boolean).map(networkRecordId))]
}
export function networkHistoryQuery(kind:NetworkHistoryKind, filters:NetworkHistoryFilters, cursor=''):string {
  const query = new URLSearchParams({limit:'50'})
  if (cursor) query.set('cursor',cursor)
  if (kind==='network-bindings' && filters.q.trim()) query.set('q',filters.q.trim())
  if (kind==='network-changes') {
    if (filters.status) {
      if (!['pending_review','accepted','rejected'].includes(filters.status)) throw new Error('请选择有效的复核状态。')
      query.set('status',filters.status)
    }
    if (filters.bindingId.trim()) query.set('bindingId',String(networkRecordId(filters.bindingId.trim())))
  }
  if (kind==='snapshots') {
    for (const key of ['createdFrom','createdTo'] as const) {
      if (!filters[key]) continue
      const time = Date.parse(filters[key])
      if (!Number.isFinite(time)) throw new Error('请填写有效的冻结时间。')
      query.set(key,new Date(time).toISOString())
    }
    if (filters.createdFrom && filters.createdTo && Date.parse(filters.createdFrom)>=Date.parse(filters.createdTo)) throw new Error('结束时间应晚于开始时间。')
  }
  return '/'+kind+'?'+query
}
