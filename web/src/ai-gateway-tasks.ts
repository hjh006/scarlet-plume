import {gatewayMicros,gatewayCredentialActive,type GatewayConfiguration,type GatewayCredential} from './ai-gateway.ts'
import {validateAIAuthorizationExpiry} from './ai-security-editor.ts'

export interface GatewayTaskSource {documentId:string;version:string;aclVersion:string;contentDigest:string}
export interface GatewayTaskToolScope {toolId:string;actions:string[];targets:string[]}
export interface GatewayTaskInput {applicationId:string;principalId:string;credentialId:number;parentTaskId?:string;purpose:string;catalogDigest:string;modelIds:string[];toolIds:string[];toolScopes:GatewayTaskToolScope[];documentIds:string[];allowedCategories:string[];maxSteps:number;budgetMicros:number;maxDelegationDepth:number;expiresAt:string}
export interface GatewayTask extends GatewayTaskInput {id:string;rootTaskId:string;depth:number;status:string;revision:number;scopeDigest:string;stepsUsed:number;reservedMicros:number;spentMicros:number;observedCategories:string[];sources:GatewayTaskSource[];createdAt:string;updatedAt:string}
export interface GatewayTaskStep {id:number;stepId:string;stepDigest:string;kind:string;modelId:string;toolId:string;documentIds:string[];categories:string[];status:string;sourceEvidence:GatewayTaskSource[];createdAt:string;completedAt?:string}
export interface GatewayTaskPage {items:GatewayTask[];nextCursor:string;hasMore:boolean}
export interface GatewayTaskDetail {task:GatewayTask;steps:GatewayTaskStep[];stepsTruncated:boolean}
export interface GatewayTaskStepPage {items:GatewayTaskStep[];nextCursor:string;hasMore:boolean}
export interface GatewayTaskDraft extends Omit<GatewayTaskInput,'budgetMicros'|'catalogDigest'|'principalId'> {budgetUSD:string}
export const taskStatusLabels:Record<string,string>={active:'活动任务',cancelled:'已停止后续执行',completed:'已关闭后续执行',expired:'期限已过',unknown:'期限待核对'}
export function taskLifecycleState(task:Pick<GatewayTask,'status'|'expiresAt'>,now=Date.now()):string {
  if(task.status!=='active')return task.status
  const expiry=Date.parse(task.expiresAt)
  return !Number.isFinite(expiry)?'unknown':expiry<=now?'expired':'active'
}
const identifier=/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,79}$/
export function taskScope(value:string):string[] {return value.split(/[,，\n]+/).map(part=>part.trim()).filter(Boolean)}
export function taskDraft(parent?:GatewayTask,now=Date.now()):GatewayTaskDraft {
  return {applicationId:parent?.applicationId || '',credentialId:parent?.credentialId || 0,parentTaskId:parent?.id || '',purpose:parent?.purpose || '',modelIds:[...(parent?.modelIds || [])],toolIds:[...(parent?.toolIds || [])],toolScopes:(parent?.toolScopes || []).map(scope=>({toolId:scope.toolId,actions:[...scope.actions],targets:[...scope.targets]})),documentIds:[...(parent?.documentIds || [])],allowedCategories:[...(parent?.allowedCategories || [])],maxSteps:parent?Math.min(10,Math.max(0,parent.maxSteps-parent.stepsUsed)):10,budgetUSD:'0',maxDelegationDepth:parent?Math.max(0,parent.maxDelegationDepth-1):0,expiresAt:new Date(Math.min(now+3600000,parent?Date.parse(parent.expiresAt):Infinity)).toISOString()}
}
function validScope(values:string[],limit:number,label:string):string[] {
  if(!Array.isArray(values) || values.length>limit || new Set(values).size!==values.length || values.some(value=>typeof value!=='string' || !identifier.test(value)))throw new Error(`${label}需要最多 ${limit} 个唯一明确标识，不接受通配符。`)
  return [...values]
}
export function taskAvailableBudget(task:GatewayTask):number {return Math.max(0,task.budgetMicros-task.spentMicros-task.reservedMicros)}
export function taskCreatePayload(draft:GatewayTaskDraft,config:GatewayConfiguration,credentials:GatewayCredential[],parent?:GatewayTask,now=Date.now()):GatewayTaskInput {
  if(!config.enabled || !/^[a-f0-9]{64}$/.test(config.catalogDigest))throw new Error('请先保存已启用的网关与实际可信目录摘要。')
  const app=config.applications.find(item=>item.id===draft.applicationId && item.enabled)
  if(!app)throw new Error('请选择一个已保存且启用的应用。')
  if(!Number.isSafeInteger(draft.credentialId) || draft.credentialId<1 || !credentials.some(item=>item.id===draft.credentialId && item.applicationId===app.id && gatewayCredentialActive(item,now)))throw new Error('任务需要绑定所选应用的有效调用凭证。')
  if(!identifier.test(draft.purpose))throw new Error('用途需要 1—80 位明确标识，例如 support-answer，不能填写任务正文。')
  const modelIds=validScope(draft.modelIds,200,'模型范围'),toolIds=validScope(draft.toolIds,200,'工具范围'),documentIds=validScope(draft.documentIds,64,'资料范围'),allowedCategories=validScope(draft.allowedCategories,64,'允许数据类别')
  const scopes=draft.toolScopes || []
  if(scopes.length!==toolIds.length || new Set(scopes.map(scope=>scope.toolId)).size!==scopes.length)throw new Error('每个工具必须恰好声明一组明确动作与目标范围。')
  const toolScopes=scopes.map(scope=>{
    if(!toolIds.includes(scope.toolId) || !scope.actions?.length || !scope.targets?.length || scope.targets.length>200 || new Set(scope.targets).size!==scope.targets.length || scope.targets.some(target=>typeof target!=='string' || !target.trim() || Array.from(target).length>2048 || target.includes('*') || /\p{Cc}/u.test(target)))throw new Error('工具需要明确动作及最多 200 个唯一精确目标；每个目标最多 2048 字，不支持通配符。')
    return {toolId:scope.toolId,actions:validScope(scope.actions,200,'工具动作'),targets:[...scope.targets]}
  })
  if(modelIds.some(id=>!(app.modelIds || []).includes(id)) || toolIds.some(id=>!(app.toolIds || []).includes(id)) || documentIds.length && !app.allowRag)throw new Error('任务的模型、工具或资料范围不能扩大已保存的应用授权。')
  if(!Number.isSafeInteger(draft.maxSteps) || draft.maxSteps<1 || draft.maxSteps>1000000 || !Number.isSafeInteger(draft.maxDelegationDepth) || draft.maxDelegationDepth<0 || draft.maxDelegationDepth>4)throw new Error('任务步数应为 1—1000000，剩余可委托层数为 0—4 的整数。')
  const budgetMicros=gatewayMicros(draft.budgetUSD)
  if(budgetMicros>9000000000000000)throw new Error('任务累计额度超过服务端允许范围。')
  validateAIAuthorizationExpiry(draft.expiresAt)
  const expires=Date.parse(draft.expiresAt)
  if(!Number.isFinite(expires) || expires<=now || expires>now+7*86400000)throw new Error('任务到期时间需要带时区，晚于当前时间且不超过 7 天。')
  if(draft.parentTaskId) {
    if(!parent || parent.id!==draft.parentTaskId || parent.status!=='active' || Date.parse(parent.expiresAt)<=now || parent.applicationId!==app.id || parent.principalId!==app.principalId || parent.catalogDigest!==config.catalogDigest || parent.purpose!==draft.purpose)throw new Error('父任务当前不可用于此委托，请刷新父任务与网关配置。')
    if(parent.depth>=4 || parent.maxDelegationDepth<1 || draft.maxDelegationDepth>parent.maxDelegationDepth-1 || expires>Date.parse(parent.expiresAt) || draft.maxSteps>parent.maxSteps-parent.stepsUsed || budgetMicros>taskAvailableBudget(parent))throw new Error('子任务的期限、可委托层数、步数或预算不能超过父任务当前剩余范围。')
    for(const [values,allowed] of [[modelIds,parent.modelIds],[toolIds,parent.toolIds],[documentIds,parent.documentIds],[allowedCategories,parent.allowedCategories]])if(values.some(id=>!(allowed || []).includes(id)))throw new Error('子任务只能缩小父任务的模型、工具、资料与数据类别范围。')
    if((parent.observedCategories || []).some(category=>!allowedCategories.includes(category)))throw new Error('子任务必须保留父任务已经积累的类别约束，不能通过委托清空历史上下文。')
    for(const scope of toolScopes){const allowed=parent.toolScopes?.find(item=>item.toolId===scope.toolId);if(!allowed || scope.actions.some(action=>!allowed.actions.includes(action)) || scope.targets.some(target=>!allowed.targets.includes(target)))throw new Error('子任务的工具动作与精确目标不能扩大父任务范围。')}
  }
  return {applicationId:app.id,principalId:app.principalId,credentialId:draft.credentialId,...(draft.parentTaskId?{parentTaskId:draft.parentTaskId}:{}),purpose:draft.purpose,catalogDigest:config.catalogDigest,modelIds,toolIds,toolScopes,documentIds,allowedCategories,maxSteps:draft.maxSteps,budgetMicros,maxDelegationDepth:draft.maxDelegationDepth,expiresAt:draft.expiresAt}
}
export function taskStatusPayload(task:GatewayTask,status:string,reason:string) {
  if(task.status!=='active' || !Number.isSafeInteger(task.revision) || task.revision<1 || !['cancelled','completed'].includes(status))throw new Error('仅能对最新活动任务停止或关闭后续执行。')
  const text=reason.trim()
  if(!text || Array.from(text).length>240 || /\p{Cc}/u.test(text))throw new Error('请填写 1—240 字且不含控制字符的结束原因，不填写敏感正文。')
  return {expectedRevision:task.revision,status,reason:text}
}
export function taskHeaderTemplate(task:GatewayTask):string {return `X-Scarlet-Task-Id: ${task.id}\nIdempotency-Key: <每次逻辑请求使用新的稳定请求标识>`}
