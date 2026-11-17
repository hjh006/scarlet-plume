export type GatewayRole='viewer'|'operator'|'reviewer'
export interface GatewayIdentity {
  revision:number;enabled:boolean;issuer:string;clientId:string;clientSecretRef:string;scimEnabled:boolean;hasScimToken:boolean;defaultRole:GatewayRole;groupRoles:Record<string,GatewayRole>;updatedAt?:string
}
export interface GatewayIdentityUser {id:number|string;externalId:string;userName:string;displayName:string;active:boolean;role:string;groups:string[];updatedAt:string}
export interface GatewayGroupRole {externalId:string;role:GatewayRole}
export type GatewayIdentityDraft=Omit<GatewayIdentity,'hasScimToken'|'updatedAt'|'groupRoles'>
export const gatewayRoleLabels:Record<GatewayRole,string>={viewer:'查看人',operator:'经办人',reviewer:'复核人'}
export function gatewayIdentityDraft(value:GatewayIdentity):GatewayIdentityDraft {
  return {revision:value.revision,enabled:value.enabled,issuer:value.issuer,clientId:value.clientId,clientSecretRef:value.clientSecretRef,scimEnabled:value.scimEnabled,defaultRole:value.defaultRole}
}
export function gatewayIdentityPayload(draft:GatewayIdentityDraft,groups:GatewayGroupRole[]) {
  const roles=Object.keys(gatewayRoleLabels)
  if(!roles.includes(draft.defaultRole))throw new Error('默认角色必须为查看人、经办人或复核人。')
  if(draft.enabled || draft.scimEnabled || draft.issuer || draft.clientId || draft.clientSecretRef) {
    let issuer:URL
    try {issuer=new URL(draft.issuer)}catch{throw new Error('请填写有效的 OIDC issuer HTTPS 地址。')}
    if(issuer.protocol!=='https:' || issuer.username || issuer.password || issuer.search || issuer.hash)throw new Error('OIDC issuer 应为不含凭证、查询或片段的 HTTPS 地址。')
    if(!draft.clientId.trim())throw new Error('启用企业登录需要 client ID。')
    if(!/^env:SCARLET_IDP_[A-Z0-9_]{1,96}$/.test(draft.clientSecretRef))throw new Error('仅支持 env:SCARLET_IDP_* 环境变量引用，不填写密钥正文。')
  }
  if(groups.length>64)throw new Error('组角色映射不能超过 64 项。')
  const groupRoles:Record<string,GatewayRole>={}
  for(const item of groups) {
    const id=item.externalId.trim()
    if(!id || Array.from(id).length>255 || /\p{Cc}/u.test(id) || Object.prototype.hasOwnProperty.call(groupRoles,id))throw new Error('组 externalId 需要唯一、没有控制字符且不超过 255 字。')
    if(!roles.includes(item.role))throw new Error('组映射不能授予负责人角色。')
    Object.defineProperty(groupRoles,id,{value:item.role,enumerable:true,writable:true,configurable:true})
  }
  const {revision,...configuration}=gatewayIdentityDraft({...draft,groupRoles:{},hasScimToken:false})
  return {expectedRevision:revision,...configuration,groupRoles}
}
export function gatewayPublicURLs(origin:string,slug:string) {
  const prefix=origin.replace(/\/$/,'')
  return {login:`${prefix}/api/v1/identity/${encodeURIComponent(slug)}/oidc/start`,callback:`${prefix}/api/v1/identity/${encodeURIComponent(slug)}/oidc/callback`,scim:`${prefix}/scim/v2/${encodeURIComponent(slug)}`}
}
export interface GatewayCachePolicy {enabled:boolean;ttlSeconds:number;maxEntries:number;maxBytes:number;keyRef:string;keyVersion:string}
export interface GatewayApplication {id:string;name:string;enabled:boolean;allowRag:boolean;requireTask?:boolean;principalId:string;modelIds:string[];toolIds:string[];budgetMicros:number;requestsPerMinute:number;tokensPerMinute:number;cachePolicy?:GatewayCachePolicy}
export interface GatewayProvider {id:string;name:string;provider:string;modelId:string;upstreamModel:string;region:string;responseFormat:string;endpointRef:string;secretRef:string;enabled:boolean;inputMicrosPerMillion:number;outputMicrosPerMillion:number;fallbackModelIds?:string[]}
export interface GatewayConfiguration {revision:number;enabled:boolean;catalogDigest:string;applications:GatewayApplication[];providers:GatewayProvider[]}
export interface GatewayCredential {id:number;applicationId:string;name:string;prefix:string;enabled:boolean;createdAt:string;expiresAt?:string;workloadBindingId?:number;workloadBindingRevision?:number;revokedAt?:string;revocationReason?:string}
export function gatewayCredentialActive(credential:GatewayCredential,now=Date.now()):boolean {return credential.enabled && !credential.revokedAt && (!credential.expiresAt || Number.isFinite(Date.parse(credential.expiresAt)) && Date.parse(credential.expiresAt)>now)}
export function gatewayRevocationLabel(reason?:string):string {return ({manual:'管理员明确撤销',binding_changed:'可信身份绑定已修改',binding_revoked:'可信身份绑定已撤销'} as Record<string,string>)[reason || ''] || '撤销原因未记录'}
export interface GatewayCredentialPage {items:(GatewayCredential & {status:string})[];limit:number;hasMore:boolean;nextCursor:string;boundary:string}
export interface GatewayCredentialFilters {state:string;applicationId:string}
export function gatewayCredentialQuery(filters:GatewayCredentialFilters,before=''):string {
  const query=new URLSearchParams({limit:'100'})
  if(filters.state){if(!['active','expired','disabled'].includes(filters.state))throw new Error('凭据状态筛选无效。');query.set('state',filters.state)}
  if(filters.applicationId){if(!/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,79}$/.test(filters.applicationId))throw new Error('应用标识筛选无效。');query.set('applicationId',filters.applicationId)}
  if(before)query.set('before',before)
  return query.toString()
}
export interface GatewayNode {id:string;status:string;lastSeenAt:string;failures:number;capabilities?:string[];version?:string}
export interface GatewayRouteHealth {providerId:string;failures:number;openUntil:string|null;lastOutcome:string}
export const gatewayCapabilities=[
  {id:'streaming_strict_hold',label:'流式安全缓冲',boundary:'strict_hold：完整接收、检查及重新授权后回放；不承诺首令牌低延迟。'},
  {id:'multimodal_projection',label:'多模态媒体检查',boundary:'仅支持已配置的 PNG/JPEG 与受限 WAV；实际 OCR/ASR 成功且策略通过后才外发。'},
  {id:'encrypted_exact_cache',label:'应用隔离加密缓存',boundary:'仅按明确应用策略缓存精确请求；命中仍需检查授权与策略。'},
  {id:'governed_fallback',label:'逐目的端受控回退',boundary:'按已保存顺序尝试；每个目的端独立核对可信目录、授权、地域与预算。'},
  {id:'task_scope_and_kill',label:'任务范围与中止约束',boundary:'任务、精确工具动作/目标、祖先中止与跨节点步数/预算均需真实请求验证。节点上报不代表执行效果已验收。'},
  {id:'source_document_binding',label:'实际资料来源绑定',boundary:'只代表节点配置了资料来源；源 ACL、正文摘要、租约与撤权需在各实际链路核对。'},
]
export interface GatewayBundle {config:GatewayConfiguration;usage:{applicationId:string;reservedMicros:number;spentMicros:number}[];credentials:GatewayCredential[];credentialPreview?:{returned:number;limit:number;hasMore:boolean;order:string;boundary:string};nodes:GatewayNode[];routeHealth?:GatewayRouteHealth[];runtime:{configured:boolean}}
export interface GatewayAudit {sequence:number;workspaceId:number;actorId:number;action:string;target:string;payload:Record<string,unknown>;occurredAt:string;previousHash:string;hash:string}
export interface GatewayAuditPage {items:GatewayAudit[];nextCursor:number;valid:boolean;previousHash:string;headHash:string;headSequence:number;providerId?:string;routeSequences?:number[]}
export interface GatewayApplicationDraft extends Omit<GatewayApplication,'modelIds'|'toolIds'|'budgetMicros'|'cachePolicy'> {modelIdsText:string;toolIdsText:string;budgetUSD:string;cachePolicy:GatewayCachePolicy}
export interface GatewayProviderDraft extends Omit<GatewayProvider,'inputMicrosPerMillion'|'outputMicrosPerMillion'|'fallbackModelIds'> {inputUSDPerMillion:string;outputUSDPerMillion:string;fallbackModelIds:string[]}
const gatewayRuntimeID=/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,79}$/
const gatewayProviderID=/^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$/
const gatewayReference=/^env:SCARLET_GATEWAY_[A-Z0-9_]{1,96}$/
export function gatewayMicros(value:string):number {
  const parts=/^(\d+)(?:\.(\d{1,6}))?$/.exec(value.trim())
  if(!parts)throw new Error('金额应为非负美元数，最多 6 位小数。')
  const micros=BigInt(parts[1]!)*1000000n+BigInt((parts[2] || '').padEnd(6,'0'))
  if(micros>BigInt(Number.MAX_SAFE_INTEGER))throw new Error('金额超过前端可精确处理范围。')
  return Number(micros)
}
export function gatewayDollars(micros:number):string {
  if(!Number.isSafeInteger(micros) || micros<0)return '金额无效'
  const amount=BigInt(micros),fraction=(amount%1000000n).toString().padStart(6,'0').replace(/0+$/,'')
  return (amount/1000000n).toString()+(fraction?'.'+fraction:'')
}
export function gatewayCachePolicy(value?:GatewayCachePolicy):GatewayCachePolicy {
  return {enabled:value?.enabled ?? false,ttlSeconds:value?.ttlSeconds ?? 300,maxEntries:value?.maxEntries ?? 100,maxBytes:value?.maxBytes ?? 1048576,keyRef:value?.keyRef ?? '',keyVersion:value?.keyVersion ?? ''}
}
function gatewayCachePayload(value?:GatewayCachePolicy):GatewayCachePolicy {
  const policy=gatewayCachePolicy(value)
  if(typeof policy.enabled!=='boolean')throw new Error('缓存开关应为布尔值。')
  if(policy.enabled && (!Number.isSafeInteger(policy.ttlSeconds) || policy.ttlSeconds<1 || policy.ttlSeconds>86400 || !Number.isSafeInteger(policy.maxEntries) || policy.maxEntries<1 || policy.maxEntries>10000 || !Number.isSafeInteger(policy.maxBytes) || policy.maxBytes<1 || policy.maxBytes>16777216))throw new Error('启用缓存需要 TTL 1—86400 秒、1—10000 条目、1—16777216 字节的整数限制。')
  if((policy.enabled || policy.keyRef) && !gatewayReference.test(policy.keyRef))throw new Error('缓存加密密钥仅支持 env:SCARLET_GATEWAY_* 环境引用，不能填写密钥正文。')
  if((policy.enabled || policy.keyVersion) && !gatewayRuntimeID.test(policy.keyVersion))throw new Error('缓存加密密钥版本需要明确有效标识。')
  return policy
}
export function gatewayApplicationDraft(value:GatewayApplication):GatewayApplicationDraft {
  return {id:value.id,name:value.name,enabled:value.enabled,allowRag:value.allowRag,requireTask:value.requireTask ?? false,principalId:value.principalId,modelIdsText:(value.modelIds || []).join(', '),toolIdsText:(value.toolIds || []).join(', '),budgetUSD:gatewayDollars(value.budgetMicros),requestsPerMinute:value.requestsPerMinute,tokensPerMinute:value.tokensPerMinute,cachePolicy:gatewayCachePolicy(value.cachePolicy)}
}
export function gatewayProviderDraft(value:GatewayProvider):GatewayProviderDraft {
  return {id:value.id,name:value.name,provider:value.provider,modelId:value.modelId,upstreamModel:value.upstreamModel,region:value.region,responseFormat:value.responseFormat,endpointRef:value.endpointRef,secretRef:value.secretRef,enabled:value.enabled,inputUSDPerMillion:gatewayDollars(value.inputMicrosPerMillion),outputUSDPerMillion:gatewayDollars(value.outputMicrosPerMillion),fallbackModelIds:[...(value.fallbackModelIds || [])]}
}
function gatewayIDs(value:string):string[] {
  const ids=[...new Set(value.split(/[,，\n]+/).map(item=>item.trim()).filter(Boolean))]
  if(ids.length>200 || ids.some(id=>!gatewayRuntimeID.test(id)))throw new Error('授权范围最多 200 个明确 ID，不支持通配符，多个 ID 用逗号分隔。')
  return ids
}
export function gatewayConfigurationPayload(expectedRevision:number,enabled:boolean,applications:GatewayApplicationDraft[],providers:GatewayProviderDraft[],catalogDigest='') {
  if(!Number.isSafeInteger(expectedRevision) || expectedRevision<0)throw new Error('配置修订应为服务端返回的非负安全整数，请重新载入配置。')
  if((enabled || catalogDigest) && !/^[a-f0-9]{64}$/.test(catalogDigest))throw new Error('启用网关需要填写真实运行时目录的 64 位小写 SHA-256 摘要。')
  if(applications.length>200 || providers.length>200)throw new Error('网关应用与提供方分别最多 200 项。')
  const seenApplications=new Set<string>(),seenProviders=new Set<string>()
  const appValues=applications.map(app=>{
    if(!gatewayRuntimeID.test(app.id) || !gatewayRuntimeID.test(app.principalId) || !app.name.trim() || Array.from(app.name.trim()).length>128 || /\p{Cc}/u.test(app.name) || seenApplications.has(app.id))throw new Error('应用及主体需要有效标识，应用名称为 1—128 字，应用标识不能重复。')
    seenApplications.add(app.id)
    if(!Number.isSafeInteger(app.requestsPerMinute) || app.requestsPerMinute<0 || app.requestsPerMinute>1000000 || !Number.isSafeInteger(app.tokensPerMinute) || app.tokensPerMinute<0 || app.tokensPerMinute>1000000000)throw new Error('RPM 应为 0—1000000、TPM 为 0—1000000000 的整数；0 表示拒绝调用。')
    const budgetMicros=gatewayMicros(app.budgetUSD)
    if(app.requireTask!==undefined && typeof app.requireTask!=='boolean')throw new Error('应用强制任务约束应为布尔值。')
    if(budgetMicros>9000000000000000)throw new Error('应用累计额度超过服务端允许范围。')
    return {id:app.id,name:app.name.trim(),enabled:app.enabled,allowRag:app.allowRag,requireTask:app.requireTask ?? false,principalId:app.principalId,modelIds:gatewayIDs(app.modelIdsText),toolIds:gatewayIDs(app.toolIdsText),budgetMicros,requestsPerMinute:app.requestsPerMinute,tokensPerMinute:app.tokensPerMinute,cachePolicy:gatewayCachePayload(app.cachePolicy)}
  })
  const providerValues=providers.map(provider=>{
    if(!gatewayProviderID.test(provider.id) || !gatewayRuntimeID.test(provider.modelId) || !gatewayRuntimeID.test(provider.provider) || !provider.name.trim() || Array.from(provider.name.trim()).length>128 || /\p{Cc}/u.test(provider.name) || !provider.upstreamModel.trim() || seenProviders.has(provider.id))throw new Error('提供方、逻辑模型及上游映射需要明确标识，名称为 1—128 字，提供方标识不能重复。')
    seenProviders.add(provider.id)
    if(!gatewayRuntimeID.test(provider.region) || provider.responseFormat!=='openai_chat')throw new Error('模型提供方需要明确地域，并采用严格 openai_chat 纯文本响应格式。')
    if(!gatewayReference.test(provider.endpointRef) || !gatewayReference.test(provider.secretRef))throw new Error('端点和密钥仅支持 env:SCARLET_GATEWAY_* 环境引用，不能填写 URL 或密钥正文。')
    const inputMicrosPerMillion=gatewayMicros(provider.inputUSDPerMillion),outputMicrosPerMillion=gatewayMicros(provider.outputUSDPerMillion)
    if(inputMicrosPerMillion>1000000000000 || outputMicrosPerMillion>1000000000000)throw new Error('提供方每百万令牌报价超过服务端允许范围。')
    const fallbackModelIds=[...(provider.fallbackModelIds || [])]
    if(fallbackModelIds.length>3 || new Set(fallbackModelIds).size!==fallbackModelIds.length || fallbackModelIds.some(id=>!gatewayRuntimeID.test(id) || id===provider.modelId))throw new Error('回退列表最多 3 个唯一明确模型 ID，不能包含自身或通配符。')
    return {id:provider.id,name:provider.name.trim(),provider:provider.provider.trim(),modelId:provider.modelId,upstreamModel:provider.upstreamModel.trim(),region:provider.region,responseFormat:provider.responseFormat,endpointRef:provider.endpointRef,secretRef:provider.secretRef,enabled:provider.enabled,inputMicrosPerMillion,outputMicrosPerMillion,fallbackModelIds}
  })
  const routes=new Set<string>()
  for(const provider of providerValues.filter(item=>item.enabled)) {
    if(routes.has(provider.modelId))throw new Error('同一逻辑模型只能有一个启用提供方，避免路由或故障切换越过授权。')
    routes.add(provider.modelId)
  }
  const routeByModel=new Map(providerValues.filter(item=>item.enabled).map(item=>[item.modelId,item]))
  function validateFallback(modelId:string,path:string[]=[]):void {
    if(path.includes(modelId) || path.length>3)throw new Error('回退路由不得循环，总链深度不能超过 3。')
    const current=routeByModel.get(modelId)
    for(const target of current?.fallbackModelIds || []) {
      if(!routeByModel.has(target))throw new Error('每个回退目的端都需要独立启用的模型提供方。')
      validateFallback(target,[...path,modelId])
    }
  }
  for(const modelId of routes)validateFallback(modelId)
  for(const app of appValues.filter(item=>item.enabled))for(const modelId of app.modelIds) {
    if(routeByModel.get(modelId)?.fallbackModelIds.some(target=>!app.modelIds.includes(target)))throw new Error('启用应用必须逐个授权回退目的端的模型 ID；回退不扩大应用范围。')
  }
  if(appValues.some(app=>app.enabled && app.modelIds.some(id=>!routes.has(id))))throw new Error('启用应用允许的每个模型都需要一个明确启用的提供方。')
  return {expectedRevision,enabled,catalogDigest,applications:appValues,providers:providerValues}
}
export function gatewayAuditSummary(event:GatewayAudit):string {
  const values=['revision','taskId','taskStepId','taskStepDigest','taskScopeDigest','taskPurpose','parentTaskId','rootTaskId','scopeDigest','purpose','stepsUsed','maxSteps','credentialId','applicationId','principalId','modelId','toolId','kind','status','outcome','requestId','requestDigest','catalogDigest','reservedMicros','spentMicros','chargedMicros','chargeMicros','chargedQuoteMicros','chargedBoundTokens','providerId','reservationId','rootModelId','previousReservationId','configRevision','attempt','attemptNumber','attemptIndex','rootRequestId','parentRequestId','cacheHit','cacheOutcome','failureCode','outcomeCode','openUntil'].flatMap(key=>{
    const value=event.payload?.[key]
    return typeof value==='string' || typeof value==='number' || typeof value==='boolean'?[`${key}: ${value}`]:[]
  })
  const object=(value:unknown):value is Record<string,unknown>=>!!value && typeof value==='object' && !Array.isArray(value)
  const audit=event.payload?.audit
  if(object(audit)) {
    for(const key of ['requestId','stage'])if(typeof audit[key]==='string')values.push(`${key}: ${audit[key]}`)
    if(object(audit.execution))for(const key of ['outcome','httpStatus','outputReleased']) {
      const value=audit.execution[key]
      if(typeof value==='string' || typeof value==='number' || typeof value==='boolean')values.push(`execution.${key}: ${value}`)
    }
    if(object(audit.decision))for(const key of ['outcome','code','taskId','taskStepId','taskStepDigest','taskScopeDigest','taskPurpose'])if(typeof audit.decision[key]==='string')values.push(`decision.${key}: ${audit.decision[key]}`)
  }
  return values.join(' · ') || '查看动作、对象与链摘要；完整非正文证据可分页导出。'
}

export function gatewayCredentialCanPrepare(recovery:{applicationId:string;read:boolean;issuedId?:number}|undefined,page:GatewayCredentialPage|null,filters:GatewayCredentialFilters):boolean {
  if(!recovery?.read || !page || filters.applicationId!==recovery.applicationId || filters.state)return false
  return !recovery.issuedId || page.items.some(item=>item.id===recovery.issuedId && item.applicationId===recovery.applicationId && !item.enabled)
}
