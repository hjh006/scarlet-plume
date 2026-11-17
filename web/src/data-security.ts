export const dataChannelLabels: Record<string,string> = {web_upload:'网页上传',mail:'邮件',im:'即时通信',usb:'移动介质',print:'打印',clipboard:'剪贴板',file_copy:'受控文件复制',database:'数据库审计',ai_prompt:'AI 输入',file_change:'文件变更'}
export const dataEnforcementLabels: Record<string,string> = {observed:'来源观察',allowed:'来源报告允许',blocked:'来源报告阻断',failed:'来源执行失败',unknown:'执行情况未知'}
export const dataEndpointScopeLabels: Record<string,string> = {observation:'原生观察',controlled_operations:'受控动作',native_control:'设备原生控制'}
export const dataEndpointStageLabels: Record<string,string> = {preflight:'执行前停止 / 未执行',completed:'已完成受控提交',observed:'仅观察',post_action:'执行后失败，可能部分完成'}
export interface DataEndpointEvidence {version:number;policyId?:number;policyRevision?:number;scope:string;stage:string;executor:string;operationRef:string}
export interface DataContext {kind:string;channel:string;subjectRef?:string;deviceRef?:string;fileDigest?:string;fileType?:string;categories:string[];destinationDomain?:string;operation:string;enforcement:string;capture:string;databaseRef?:string;objectRef?:string;statementRef?:string;records?:number;policyDigest?:string;endpoint?:DataEndpointEvidence}
export interface DataEvent {id:number;sourceId:number;source:string;sourceCurrent:boolean;checkId:number;observedAt:string;rule:string;severity:string;dataContext:DataContext}
export interface DataScope {sourceId?:number;subjectRef?:string;deviceRef?:string;channel?:string;from:string;to:string}
export interface DataWindow {scope:DataScope;items:DataEvent[];total:number;truncated:boolean}
export interface DataChain {eventIds:number[];sourceId:number;subjectRef?:string;deviceRef?:string;channels:string[];summary:string}
export interface DataCase {id:number;title:string;scope:DataScope;snapshotDigest:string;actor:string;createdAt:string;snapshot:{window:DataWindow;chains:DataChain[];coverage:string[];engineVersion:string};notes:{id:number;kind:string;snapshot:Record<string,unknown>;actor:string;createdAt:string}[]}
export function sameDataScope(left:DataScope,right:DataScope):boolean {
  const from=Date.parse(left.from),to=Date.parse(left.to)
  return Number.isFinite(from) && Number.isFinite(to) && from===Date.parse(right.from) && to===Date.parse(right.to) && (left.sourceId || 0)===(right.sourceId || 0) && (left.subjectRef || '')===(right.subjectRef || '') && (left.deviceRef || '')===(right.deviceRef || '') && (left.channel || '')===(right.channel || '')
}
export function checkedDataCase(id:number,received:DataCase,original?:DataCase):DataCase {
  if(received.id!==id || original && (original.id!==id || original.snapshotDigest!==received.snapshotDigest || !sameDataScope(original.scope,received.scope)))throw new Error('返回调查与原快照绑定不一致，请重新读取原记录，不能改用其他范围。')
  return received
}
export function dataCaseAIPreview(c:DataCase){const chains=new Map<number,number>();c.snapshot.chains.forEach((chain,index)=>chain.eventIds.forEach(id=>chains.set(id,index+1)));return c.snapshot.window.items.map(e=>({eventId:e.id,chain:chains.get(e.id),kind:e.dataContext.kind,channel:e.dataContext.channel,rule:e.rule,severity:e.severity,enforcement:e.dataContext.enforcement,categories:e.dataContext.categories,sourceCurrentAtSnapshot:e.sourceCurrent,capture:e.dataContext.capture}))}

export function dataCaseCreatePayload(title:string,displayed:DataWindow,requested:DataScope):{title:string;scope:DataScope} {
  if(!displayed || !Array.isArray(displayed.items) || !displayed.items.length)throw new Error('至少需要一条已读取事件，才能创建调查快照。')
  if(!sameDataScope(displayed.scope,requested))throw new Error('筛选范围已变化，请先重新查询事件。')
  const {sourceId,subjectRef,deviceRef,channel,from,to}=displayed.scope
  return {title:title.trim(),scope:{sourceId,subjectRef,deviceRef,channel,from,to}}
}
