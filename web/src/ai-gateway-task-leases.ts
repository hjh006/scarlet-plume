import { ApiError, describeError } from './api.ts'
import type { GatewayCredential } from './ai-gateway.ts'

export const taskLeaseProtocol = 'scarlet-task-lease-v1'
export interface LeaseIssue { code:string; taskId?:string; stepId?:string; reservationId?:string; state?:string; reservedMicros?:number; message:string }
export interface TaskLease { credentialId:number; revision:number; mode:'initial'|'renewal'|'rebind'; updatedAt:string }
export interface LeaseIdentity {bindingId:number;revision:number;issuer:string;audience:string;subject:string}
export interface CredentialPlan {taskId:string;source:GatewayCredential;target:GatewayCredential;sourceIdentity?:LeaseIdentity;targetIdentity?:LeaseIdentity}
export interface TaskLeaseNode { taskId:string; taskRevision:number; scopeDigest:string; taskStatus:string; taskExpiresAt:string; applicationId:string; principalId:string; originalCredentialId:number; lease:TaskLease; credential:GatewayCredential; identity?:LeaseIdentity; bindingStatus:string; currentBindingRevision?:number; blockers:LeaseIssue[] }
export interface TaskLeaseView { taskId:string; rootTaskId:string; leaseChainDigest:string; leaseProtocolVersion:string; observedAt:string; chain:TaskLeaseNode[]; blockers:LeaseIssue[]; canPropose:boolean; boundary:string }
export interface LeaseExpectation { taskId:string; taskRevision:number; leaseRevision:number; credentialId:number }
export interface LeaseReplacement { taskId:string; newCredentialId:number }
export interface BindingProposalInput { operationId:string; expectedLeaseChainDigest:string; expectedChain:LeaseExpectation[]; changes:LeaseReplacement[]; reason:string; expiresAt?:string }
export interface BindingProposal extends BindingProposalInput { id:number; taskId:string; status:'proposed'|'applied'|'rejected'|'invalidated'; revision:number; note:string; proposedBy:number; reviewedBy?:number; createdAt:string; expiresAt:string; appliedAt?:string; proposalDigest:string; credentialPlan:CredentialPlan[]; canReview:boolean; blockers:LeaseIssue[] }
export interface LeaseChange { id:number; operationId:string; mode:'renewal'|'rebind'; taskId:string; changedTaskIds:string[]; actorId?:number; actorCredentialId?:number; proposalId?:number; reason:string; beforeDigest:string; afterDigest:string; changes:{taskId:string;oldCredentialId:number;newCredentialId:number;oldLeaseRevision:number;newLeaseRevision:number}[]; createdAt:string }
export interface LeasePage<T> { items:T[]; limit:number; hasMore:boolean; nextCursor?:string }
export const bindingProposalLabels:Record<string,string> = { proposed:'待独立复核', applied:'已应用', rejected:'已拒绝', invalidated:'已失效' }
const identifier = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,79}$/
const positive = (value:number)=>Number.isSafeInteger(value) && value>0

export function leaseExpectations(view:TaskLeaseView):LeaseExpectation[] {
  if (view.leaseProtocolVersion!==taskLeaseProtocol || !/^[a-f0-9]{64}$/.test(view.leaseChainDigest) || !view.chain.length || view.chain.length>5 || view.chain[0]?.taskId!==view.rootTaskId || view.chain.at(-1)?.taskId!==view.taskId || new Set(view.chain.map(node=>node.taskId)).size!==view.chain.length) throw new Error('凭据链缺失或协议不兼容，请重新读取当前任务。')
  return view.chain.map(node=>{
    if (!identifier.test(node.taskId) || !positive(node.taskRevision) || !positive(node.lease.revision) || !positive(node.lease.credentialId)) throw new Error('凭据链修订无效，请重新读取后再提交。')
    return {taskId:node.taskId,taskRevision:node.taskRevision,leaseRevision:node.lease.revision,credentialId:node.lease.credentialId}
  })
}
function actionNote(value:string,label:string):string {
  const text=value.trim()
  if (!text || Array.from(text).length>240 || /\p{Cc}/u.test(text)) throw new Error(`${label}需要 1—240 字的简短说明，不能包含控制字符或敏感正文。`)
  return text
}
export function bindingProposalPayload(view:TaskLeaseView, targets:Record<string,number>, reason:string, operationId:string, expiresAt='', now=Date.now()):BindingProposalInput {
  if (!view.canPropose) throw new Error('当前凭据链不满足换绑条件，请先核对阻断原因。')
  if (!identifier.test(operationId)) throw new Error('操作标识无效，请关闭后重新打开换绑表单。')
  const expectedChain=leaseExpectations(view)
  const changes=Object.entries(targets).filter(([,id])=>id!==0).map(([taskId,newCredentialId])=>{
    const source=expectedChain.find(node=>node.taskId===taskId)
    if (!source || !positive(newCredentialId) || source.credentialId===newCredentialId) throw new Error('请为链内任务选择另一枚有效凭据，不要沿用当前凭据。')
    return {taskId,newCredentialId}
  })
  if (!changes.length || changes.length>5) throw new Error('至少选择一层需要换绑的任务，最多 5 层。')
  let expiry:string|undefined
  if (expiresAt) {
    const value=Date.parse(expiresAt)
    if (!Number.isFinite(value) || value<=now || value>now+15*60*1000) throw new Error('申请有效至应晚于当前时间且不超过 15 分钟。')
    expiry=new Date(value).toISOString()
  }
  return {operationId,expectedLeaseChainDigest:view.leaseChainDigest,expectedChain,changes,reason:actionNote(reason,'换绑原因'),...(expiry?{expiresAt:expiry}:{})}
}
export function bindingReviewPayload(proposal:BindingProposal, decision:'approve'|'reject', note:string, now=Date.now()) {
  if (!proposal.canReview || proposal.status!=='proposed' || !positive(proposal.revision) || !Number.isFinite(Date.parse(proposal.expiresAt)) || Date.parse(proposal.expiresAt)<=now) throw new Error('申请已过期、状态已变化或当前账号不能独立复核，请重新读取。')
  if (decision==='approve' && (!proposal.credentialPlan?.length || proposal.credentialPlan.length!==proposal.changes.length || proposal.changes.some(change=>!proposal.credentialPlan.some(plan=>plan.taskId===change.taskId && plan.target.id===change.newCredentialId && plan.source.id===proposal.expectedChain.find(node=>node.taskId===change.taskId)?.credentialId)))) throw new Error('冻结的源与目标身份事实不完整，不能批准。请重新核对申请。')
  return {expectedRevision:proposal.revision,decision,note:actionNote(note,'复核意见')}
}
export function leaseFailure(cause:unknown):{message:string;code:string;blockers:LeaseIssue[];recovery:string[]} {
  const details=cause instanceof ApiError && cause.details && typeof cause.details==='object'?cause.details as Record<string,unknown>:{}
  const blockers:LeaseIssue[]=Array.isArray(details.blockers)?details.blockers.slice(0,20).flatMap(value=>{
    if (!value || typeof value!=='object') return []
    const row=value as Record<string,unknown>
    if (typeof row.message!=='string') return []
    const issue:LeaseIssue={code:typeof row.code==='string'?row.code.slice(0,80):'unknown',message:row.message.slice(0,600)}
    for (const key of ['taskId','stepId','reservationId','state'] as const) if (typeof row[key]==='string') issue[key]=row[key].slice(0,200)
    if (typeof row.reservedMicros==='number' && Number.isSafeInteger(row.reservedMicros) && row.reservedMicros>=0) issue.reservedMicros=row.reservedMicros
    return [issue]
  }):[]
  return {message:describeError(cause),code:typeof details.code==='string'?details.code.slice(0,80):'',blockers,recovery:Array.isArray(details.recovery)?details.recovery.filter((value):value is string=>typeof value==='string').slice(0,8).map(value=>value.slice(0,600)):[]}
}
export function machineRenewalExample(workspace:string, view:TaskLeaseView, origin:string):string {
  const url=origin+'/api/v1/workspaces/'+encodeURIComponent(workspace)+'/ai-gateway/tasks/'+encodeURIComponent(view.taskId)+'/credential-renewal'
  const body={operationId:'<稳定且唯一的续期操作标识>',expectedLeaseChainDigest:view.leaseChainDigest,expectedChain:leaseExpectations(view),replaceTaskIds:[view.taskId]}
  return `GET ${url}\nAuthorization: Bearer <已兑换的新工作负载短期令牌>\nCookie: 不发送\n\nPOST ${url}\nAuthorization: Bearer <同一枚新令牌>\nContent-Type: application/json\n\n${JSON.stringify(body,null,2)}`
}
