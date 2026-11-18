<script setup lang="ts">
import {computed,nextTick,onMounted,onUnmounted,ref,watch} from 'vue'
import {ApiError,describeError,mutationNeedsReadback,downloadFile,formatDate,request,workspacePath,type Workspace} from './api'
import AIGatewayIdentity from './AIGatewayIdentity.vue'
import AIGatewayCatalog from './AIGatewayCatalog.vue'
import AIGatewayTasks from './AIGatewayTasks.vue'
import AIGatewayWorkloads from './AIGatewayWorkloads.vue'
import Icon from './Icon.vue'
import {containDialogTab} from './dialog-keyboard'
import {gatewayCredentialCanPrepare,gatewayApplicationDraft,gatewayProviderDraft,gatewayCachePolicy,gatewayCapabilities,gatewayConfigurationPayload,gatewayDollars,gatewayAuditSummary,gatewayCredentialActive,gatewayRevocationLabel,gatewayCredentialQuery,type GatewayCredentialPage,type GatewayCredentialFilters,type GatewayApplicationDraft,type GatewayProviderDraft,type GatewayBundle,type GatewayCredential,type GatewayAuditPage} from './ai-gateway'
import './ai-security.css'
import './ai-gateway.css'

const props=defineProps<{workspace:Workspace;initialTab?:string}>()
const emit=defineEmits<{sessionExpired:[]}>()
const tabs={overview:'接入概况',applications:'应用、授权与预算',providers:'模型与提供方',credentials:'应用凭证',tasks:'Agent任务与委托',nodes:'节点与健康',identity:'企业身份',audit:'审计与导出'}
type Tab=keyof typeof tabs
const initial=props.initialTab==='models'?'providers':props.initialTab || 'overview'
const tab=ref<Tab>(initial in tabs?initial as Tab:'overview')
const base=workspacePath(props.workspace.slug)+'/ai-gateway'
const home='#/'+encodeURIComponent(props.workspace.slug)
const controller=new AbortController()
const bundle=ref<GatewayBundle|null>(null)
const loading=ref(true),busy=ref(''),error=ref(''),feedback=ref('')
const owner=computed(()=>props.workspace.role==='owner')
const canCredentials=computed(()=>['owner','operator'].includes(props.workspace.role))
const configNeedsReadback=ref(false)
const draftRevision=ref(0),enabled=ref(false),catalogDigest=ref(''),savedSnapshot=ref('')
const applications=ref<GatewayApplicationDraft[]>([]),providers=ref<GatewayProviderDraft[]>([])
const selectedApp=ref(0),selectedProvider=ref(0)
const app=computed(()=>applications.value[selectedApp.value])
const provider=computed(()=>providers.value[selectedProvider.value])
const snapshot=()=>JSON.stringify({enabled:enabled.value,catalogDigest:catalogDigest.value,applications:applications.value,providers:providers.value})
const dirty=computed(()=>snapshot()!==savedSnapshot.value)
const outdated=computed(()=>bundle.value!==null && draftRevision.value!==bundle.value.config.revision)
const credentialApp=ref(''),credentialName=ref(''),fallbackChoice=ref('')
const credentialPage=ref<GatewayCredentialPage|null>(null),credentialBefore=ref(''),credentialHistory=ref<string[]>([]),credentialsLoading=ref(false),credentialsError=ref('')
const credentialFilters=ref<GatewayCredentialFilters>({state:'',applicationId:''}),appliedCredentialFilters=ref<GatewayCredentialFilters>({...credentialFilters.value})
let credentialsGeneration=0,credentialsRequest:AbortController|undefined
watch(selectedProvider,()=>{fallbackChoice.value=''})
const credentialRecovery=ref<{applicationId:string;name:string;read:boolean;issuedId?:number}>(),credentialsHeading=ref<HTMLElement>(),credentialsReadAt=ref(''),auditReadAt=ref('')
const cacheDialog=ref<HTMLDialogElement>(),applicationsHeading=ref<HTMLElement>(),cacheDialogError=ref('')
const cachePlan=ref<{id:string;name:string;revision:number}>()
const cacheRecovery=ref<{id:string;invalidated?:number}>()
let cacheReturnFocus:HTMLElement|undefined
const savedApp=computed(()=>bundle.value?.config.applications.find(item=>item.id===app.value?.id))
const cachePlanCurrent=computed(()=>!!cachePlan.value && cachePlan.value.revision===bundle.value?.config.revision && cachePlan.value.id===savedApp.value?.id)
const credentialFilterDraft=computed(()=>JSON.stringify(credentialFilters.value)!==JSON.stringify(appliedCredentialFilters.value))
const credentialRecoveryReady=computed(()=>gatewayCredentialCanPrepare(credentialRecovery.value,credentialPage.value,appliedCredentialFilters.value))
const token=ref(''),tokenId=ref<number|null>(null),showToken=ref(false)
let tokenGeneration=0,tokenTimer:ReturnType<typeof setTimeout>|undefined
const audit=ref<GatewayAuditPage|null>(null),auditLoading=ref(false),auditError=ref(''),auditAfter=ref(0)
const auditHistory=ref<number[]>([]),auditProvider=ref('')
function clearToken(){token.value='';tokenId.value=null;showToken.value=false;if(tokenTimer)clearTimeout(tokenTimer);tokenTimer=undefined}
watch(tab,()=>{tokenGeneration++;clearToken();error.value='';feedback.value='';if(tab.value==='audit' && !audit.value)void loadAudit(0);if(tab.value==='credentials')void loadCredentials(credentialBefore.value)})
function handleError(cause:unknown){if(controller.signal.aborted)return;if(cause instanceof ApiError && cause.status===401)emit('sessionExpired');error.value=cause instanceof ApiError && cause.status===409?cause.message+' 当前草稿已保留，请重新载入后核对。':describeError(cause)}
async function load(replaceDraft=true){
  loading.value=true;error.value=''
  try{
    const next=await request<GatewayBundle>(base,{signal:controller.signal})
    if(controller.signal.aborted)return
    if(!next?.config || !Number.isSafeInteger(next.config.revision) || next.config.revision<0 || !Array.isArray(next.config.applications) || !Array.isArray(next.config.providers))throw new Error('未取得有效网关配置，未更新草稿。')
    next.credentials ||= [];next.nodes ||= [];next.usage ||= [];next.routeHealth ||= []
    bundle.value=next
    if(replaceDraft){
      const applicationId=app.value?.id,providerId=provider.value?.id
      configNeedsReadback.value=false
      draftRevision.value=next.config.revision;enabled.value=next.config.enabled;catalogDigest.value=next.config.catalogDigest
      applications.value=(next.config.applications || []).map(gatewayApplicationDraft)
      providers.value=(next.config.providers || []).map(gatewayProviderDraft)
      selectedApp.value=Math.max(0,applications.value.findIndex(item=>item.id===applicationId));selectedProvider.value=Math.max(0,providers.value.findIndex(item=>item.id===providerId));savedSnapshot.value=snapshot()
    }
    return next
  }catch(cause){handleError(cause)}finally{loading.value=false}
}
function addApp(){applications.value.push({id:'',name:'',enabled:false,allowRag:false,principalId:'',modelIdsText:'',toolIdsText:'',budgetUSD:'0',requestsPerMinute:0,tokensPerMinute:0,cachePolicy:gatewayCachePolicy()});selectedApp.value=applications.value.length-1}
function addProvider(){providers.value.push({id:'',name:'',provider:'',modelId:'',upstreamModel:'',region:'',responseFormat:'openai_chat',endpointRef:'',secretRef:'',enabled:false,inputUSDPerMillion:'0',outputUSDPerMillion:'0',fallbackModelIds:[]});selectedProvider.value=providers.value.length-1}
function removeApp(){applications.value.splice(selectedApp.value,1);selectedApp.value=Math.max(0,selectedApp.value-1)}
function removeProvider(){providers.value.splice(selectedProvider.value,1);selectedProvider.value=Math.max(0,selectedProvider.value-1)}
async function saveConfig(){
  if(!owner.value || !bundle.value || busy.value || loading.value || outdated.value || configNeedsReadback.value || cacheRecovery.value)return
  let payload:ReturnType<typeof gatewayConfigurationPayload>
  try{payload=gatewayConfigurationPayload(draftRevision.value,enabled.value,applications.value,providers.value,catalogDigest.value)}catch(cause){error.value=cause instanceof Error?cause.message:'请核对网关字段。';return}
  busy.value='save';error.value='';feedback.value=''
  try{await request(base+'/config',{method:'PUT',body:JSON.stringify(payload),signal:controller.signal});await load();feedback.value='网关治理配置已保存。实际授权、预算占用与节点接入仍需核对运行证据。'}catch(cause){if(mutationNeedsReadback(cause,true))configNeedsReadback.value=true;handleError(cause)}finally{busy.value=''}
}
const fallbackOptions=computed(()=>providers.value.filter(item=>item.enabled && item.modelId && item.modelId!==provider.value?.modelId && !provider.value?.fallbackModelIds.includes(item.modelId)))
function addFallback(){if(provider.value && fallbackChoice.value && provider.value.fallbackModelIds.length<3){provider.value.fallbackModelIds.push(fallbackChoice.value);fallbackChoice.value=''}}
function moveFallback(index:number,direction:number){const values=provider.value?.fallbackModelIds;if(!values || index+direction<0 || index+direction>=values.length)return;[values[index],values[index+direction]]=[values[index+direction]!,values[index]!]}
function fallbackProvider(modelId:string){return providers.value.find(item=>item.enabled && item.modelId===modelId)}
function missingFallbackAuthorizations(modelId:string){return applications.value.filter(item=>item.enabled && item.modelIdsText.split(/[,，\n]+/).map(value=>value.trim()).includes(provider.value?.modelId || '') && !item.modelIdsText.split(/[,，\n]+/).map(value=>value.trim()).includes(modelId)).map(item=>item.name || item.id).join('、')}
async function openCacheReview(){
  const target=savedApp.value,revision=bundle.value?.config.revision
  if(!owner.value || !target || revision===undefined || loading.value || busy.value || cacheRecovery.value)return
  cachePlan.value={id:target.id,name:target.name,revision}
  cacheDialogError.value=''
  cacheReturnFocus=document.activeElement instanceof HTMLElement?document.activeElement:undefined
  await nextTick()
  cacheDialog.value?.showModal()
}
function closeCacheReview(){if(busy.value!=='cache')cacheDialog.value?.close()}
async function cacheReviewClosed(){
  const target=cacheReturnFocus
  cacheReturnFocus=undefined;cachePlan.value=undefined
  await nextTick()
  if(controller.signal.aborted || tab.value!=='applications' || cacheDialog.value?.open || document.activeElement!==document.body)return
  if(target?.isConnected && !target.matches(':disabled'))target.focus({preventScroll:true})
  else applicationsHeading.value?.focus({preventScroll:true})
}
async function readCacheRevision(){
  const pending=cacheRecovery.value
  if(!pending || loading.value || busy.value)return
  const next=await load(!dirty.value)
  if(!next || cacheRecovery.value!==pending)return
  cacheRecovery.value=undefined
  feedback.value=pending.invalidated===undefined?`已读取当前修订 ${next.config.revision}。此前的失效结果仍需查阅缓存失效审计。`:`应用 ${pending.id} 的 ${pending.invalidated} 条缓存记录已清除，已读取当前修订 ${next.config.revision}。`
}
async function invalidateCache(){
  const plan=cachePlan.value
  if(!owner.value || !plan || !cachePlanCurrent.value || loading.value || busy.value || cacheRecovery.value)return
  busy.value='cache';error.value='';feedback.value=''
  cacheDialogError.value=''
  try{
    const result=await request<{invalidated:number}>(base+'/cache/invalidate',{method:'POST',body:JSON.stringify({applicationId:plan.id}),signal:controller.signal})
    if(controller.signal.aborted)return
    if(!Number.isSafeInteger(result?.invalidated) || result.invalidated<0)throw new Error('未返回有效的失效条目数。')
    cacheRecovery.value={id:plan.id,invalidated:result.invalidated}
  }catch(cause){
    if(controller.signal.aborted)return
    if(cause instanceof ApiError && cause.status===401)emit('sessionExpired')
    if(mutationNeedsReadback(cause,true))cacheRecovery.value={id:plan.id}
    else cacheDialogError.value=describeError(cause)
  }finally{busy.value=''}
  if(!cacheRecovery.value || controller.signal.aborted)return
  cacheDialog.value?.close()
  if(cacheRecovery.value.invalidated!==undefined)await readCacheRevision()
}
async function exportProvider(){
  if(!provider.value?.id || !bundle.value?.config.providers.some(item=>item.id===provider.value?.id) || busy.value)return
  busy.value='export';error.value=''
  try{await downloadFile(base+'/config/export?providerId='+encodeURIComponent(provider.value.id),'scarlet-ai-gateway-provider-config.json',controller.signal)}catch(cause){handleError(cause)}finally{busy.value=''}
}
async function restoreCredentialsFocus(trigger:Element|null,generation:number,applicationId:string,recovery:typeof credentialRecovery.value){
  await nextTick()
  const scope=credentialsHeading.value?.closest('section.panel')
  if(controller.signal.aborted || tab.value!=='credentials' || tokenGeneration!==generation || credentialApp.value!==applicationId || credentialRecovery.value!==recovery || busy.value || document.activeElement!==document.body || !scope?.isConnected)return
  if(trigger instanceof HTMLElement && scope.contains(trigger) && trigger.isConnected && !trigger.matches(':disabled'))trigger.focus({preventScroll:true})
  else credentialsHeading.value?.focus({preventScroll:true})
}
async function loadCredentials(before=credentialBefore.value,remember=false,filters:GatewayCredentialFilters=appliedCredentialFilters.value){
  const focusTrigger=document.activeElement,focusGeneration=tokenGeneration,focusApp=credentialApp.value,focusRecovery=credentialRecovery.value
  const generation=++credentialsGeneration;credentialsRequest?.abort();credentialsRequest=new AbortController()
  credentialsLoading.value=true;credentialsError.value=''
  try{const next=await request<GatewayCredentialPage>(base+'/credentials?'+gatewayCredentialQuery(filters,before),{signal:AbortSignal.any([controller.signal,credentialsRequest.signal])});if(controller.signal.aborted || generation!==credentialsGeneration)return;if(!next || !Array.isArray(next.items))throw new Error('凭据列表响应缺少记录数组，原读取范围与签发锁保持不变。');if(remember)credentialHistory.value.push(credentialBefore.value);else if(!before)credentialHistory.value=[];credentialPage.value={...next,items:next.items || []};credentialBefore.value=before;appliedCredentialFilters.value={...filters};credentialsReadAt.value=new Date().toISOString();return true}catch(cause){if(generation!==credentialsGeneration || controller.signal.aborted)return;if(cause instanceof ApiError && cause.status===401)emit('sessionExpired');credentialsError.value=describeError(cause)}finally{if(generation===credentialsGeneration){credentialsLoading.value=false;await restoreCredentialsFocus(focusTrigger,focusGeneration,focusApp,focusRecovery)}}
}
async function previousCredentials(){const before=credentialHistory.value.at(-1);if(before!==undefined && await loadCredentials(before))credentialHistory.value.pop()}
async function resetCredentialFilters(){credentialFilters.value={state:'',applicationId:''};await loadCredentials('',false,{...credentialFilters.value})}
async function readCredentialRecovery(){const target=credentialRecovery.value;if(!target || credentialsLoading.value || busy.value)return;target.read=false;const filters={state:'',applicationId:target.applicationId};credentialFilters.value={...filters};if(await loadCredentials('',false,filters) && credentialRecovery.value===target)target.read=true}
async function createCredential(){
  if(!canCredentials.value || !credentialApp.value || !credentialName.value.trim() || busy.value || token.value || credentialRecovery.value)return
  busy.value='credential';error.value='';feedback.value=''
  const generation=tokenGeneration,target={applicationId:credentialApp.value,name:credentialName.value.trim()},focusTrigger=document.activeElement
  try{
    const result=await request<{credential:GatewayCredential;token:string}>(base+'/credentials',{method:'POST',body:JSON.stringify(target),signal:controller.signal})
    if(controller.signal.aborted)return
    if(generation===tokenGeneration){clearToken();token.value=result.token;tokenId.value=result.credential.id;tokenTimer=setTimeout(clearToken,60_000);feedback.value='应用凭证已创建。令牌只在本次显示，请交给授权调用方保管。'}
    else{result.token='';credentialRecovery.value={...target,read:false,issuedId:result.credential.id};feedback.value='凭证已创建，但已离开显示页，令牌未保留。请撤销此凭证后重新创建。'}
    credentialName.value='';await load(false);await loadCredentials('')
  }catch(cause){if(mutationNeedsReadback(cause))credentialRecovery.value={...target,read:false};handleError(cause)}finally{busy.value='';await restoreCredentialsFocus(focusTrigger,generation,target.applicationId,credentialRecovery.value)}
}
async function revokeCredential(item:GatewayCredential){
  if(!canCredentials.value || busy.value)return
  const trigger=document.activeElement
  busy.value='revoke';error.value='';feedback.value=''
  try{await request(base+'/credentials/'+item.id,{method:'DELETE',signal:controller.signal});if(tokenId.value===item.id)clearToken();await load(false);await loadCredentials();await nextTick();const focused=document.activeElement;if(tab.value==='credentials' && (!(focused instanceof HTMLElement) || focused===document.body || !focused.isConnected || focused.matches(':disabled'))){if(trigger instanceof HTMLElement && trigger.isConnected && !trigger.matches(':disabled'))trigger.focus({preventScroll:true});else credentialsHeading.value?.focus({preventScroll:true})}feedback.value='凭证已撤销，后续调用须使用其他有效凭证；历史调用记录保留。'}catch(cause){handleError(cause)}finally{busy.value=''}
}
async function copyToken(){try{await navigator.clipboard.writeText(token.value);feedback.value='令牌已复制，请存放到调用方受限凭证配置。'}catch{error.value='剪贴板不可用，请临时显示后手动复制令牌。'}}
async function loadAudit(after:number,remember=false,providerId=audit.value?.providerId || ''){
  if(auditLoading.value)return
  auditLoading.value=true;auditError.value=''
  try{
    const next=await request<GatewayAuditPage>(base+'/audit?after='+after+'&limit=100'+(providerId?'&providerId='+encodeURIComponent(providerId):''),{signal:controller.signal})
    if(controller.signal.aborted)return
    if(!Array.isArray(next?.items) || typeof next.valid!=='boolean' || !Number.isSafeInteger(next.headSequence) || !Number.isSafeInteger(next.nextCursor) || typeof next.headHash!=='string' || (next.providerId || '')!==providerId){
      auditError.value='未取得当前路由的有效审计页。';return
    }
    if(remember)auditHistory.value.push(auditAfter.value)
    audit.value=next;auditAfter.value=after;auditReadAt.value=new Date().toISOString()
    return true
  }catch(cause){if(controller.signal.aborted)return;if(cause instanceof ApiError&&cause.status===401)emit('sessionExpired');auditError.value=describeError(cause)}finally{auditLoading.value=false}
}
async function changeAuditRoute(){if(await loadAudit(0,false,auditProvider.value))auditHistory.value=[]}
async function previousAudit(){const previous=auditHistory.value.at(-1);if(previous!==undefined && await loadAudit(previous))auditHistory.value.pop()}
async function download(kind:'config'|'audit'){
  if(busy.value)return
  busy.value='export';error.value=''
  try{await downloadFile(kind==='config'?base+'/config/export':base+'/audit/export?after='+auditAfter.value+'&limit=100'+(audit.value?.providerId?'&providerId='+encodeURIComponent(audit.value.providerId):''),kind==='config'?'scarlet-ai-gateway-config.json':'scarlet-ai-gateway-audit-page.json',controller.signal)}catch(cause){handleError(cause)}finally{busy.value=''}
}
const totalSpent=computed(()=>bundle.value?.usage.reduce((sum,item)=>sum+item.spentMicros,0) || 0)
const totalReserved=computed(()=>bundle.value?.usage.reduce((sum,item)=>sum+item.reservedMicros,0) || 0)
const usageFor=(id:string)=>bundle.value?.usage.find(item=>item.applicationId===id)
function nodeStatus(value:string){return ({healthy:'来源报告健康',online:'来源报告在线',active:'来源报告活跃',stale:'回报已过时',offline:'来源报告离线',unknown:'状态未知',unhealthy:'来源报告异常'} as Record<string,string>)[value] || value}
function auditAction(value:string){return ({'gateway.cache_invalidated':'清除应用缓存','gateway.cache_hit':'缓存命中'} as Record<string,string>)[value] || value}
onMounted(()=>{void load();if(tab.value==='audit')void loadAudit(0);if(tab.value==='credentials')void loadCredentials('')})
onUnmounted(()=>{clearToken();controller.abort()})
</script>

<template>
  <section class="ai-security-workspace ai-gateway-workspace" :aria-busy="loading || !!busy">
    <header class="page-header"><div><p class="eyebrow">数据与 AI 安全 / 网关与企业治理</p><h1>AI 网关与企业治理</h1><p>管理 Bifrost 接入、可信应用授权和预算，核对实际节点与审计证据。</p></div><div class="page-actions"><a class="button button-secondary" :href="home+'/ai-security'">AI 运行时安全</a><button class="button button-secondary" :disabled="loading || !!busy" @click="load()">{{ dirty?'重新载入并丢弃网关草稿':'刷新网关配置与状态' }}</button></div></header>
    <p v-if="configNeedsReadback" class="message message-warning" role="status">上次配置写入需要重新核对服务端修订，草稿保持原值。请明确重新载入已保存配置后再准备新提交。</p><p v-if="error" class="message message-error" role="alert">{{ error }}</p><p v-if="feedback" class="message message-success" role="status">{{ feedback }}</p>
    <div v-if="cacheRecovery" class="message message-warning">
      <p role="status">{{ cacheRecovery.invalidated===undefined?'应用 '+cacheRecovery.id+' 的缓存失效结果尚未核实。':'应用 '+cacheRecovery.id+' 已清除 '+cacheRecovery.invalidated+' 条缓存记录，最新修订尚未读取。' }} 先读取当前配置修订，再准备新的操作。</p>
      <button type="button" class="button button-secondary" :disabled="loading || !!busy" @click="readCacheRevision">读取当前修订并保留未保存的草稿</button>
      <a :href="home+'/ai-gateway?tab=audit'" @click.prevent="tab='audit'">查看调用审计</a>
    </div>
    <nav class="ai-security-tabs" aria-label="AI 网关管理"><button v-for="(label,key) in tabs" :key="key" :aria-pressed="tab===key" @click="tab=key">{{ label }}</button></nav>
    <AIGatewayIdentity v-if="tab==='identity'" :workspace="workspace" @session-expired="emit('sessionExpired')"/>
    <template v-else>
      <p v-if="loading && !bundle" class="message" role="status">正在读取网关配置、额度与运行记录…</p>
      <section v-if="!bundle && !loading" class="panel ai-security-empty"><h2>网关管理接口当前不可用</h2><p>尚未取得服务端配置，无法确认路由、额度或运行节点。企业身份管理可在独立页签核对。</p><button class="button button-secondary" @click="load()">重试读取</button></section>
      <template v-if="bundle">
        <div class="panel ai-security-config-source"><strong>服务端修订 {{ bundle.config.revision }}</strong><span>草稿基于修订 {{ draftRevision }}</span><span>网关调用{{ bundle.config.enabled?'已启用':'未启用' }}</span><span>{{ dirty?'有未保存的网关草稿':'草稿与已载入配置一致' }}</span><span>仅负责人可修改治理配置</span></div>
        <p v-if="outdated" class="message message-warning">服务端配置已更新，当前草稿仍保留原修订。请重新载入后核对再保存。</p>
        <AIGatewayTasks v-if="tab==='tasks'" :workspace="workspace" :configuration="bundle.config" :credentials="bundle.credentials" @session-expired="emit('sessionExpired')"/>
        <template v-if="tab==='overview'">
          <div class="ai-gateway-metrics"><article class="panel"><span>已登记应用</span><strong>{{ bundle.config.applications.length }}</strong></article><article class="panel"><span>已登记模型路由</span><strong>{{ bundle.config.providers.length }}</strong></article><article class="panel"><span>累计报价计费（USD）</span><strong>{{ gatewayDollars(totalSpent) }}</strong></article><article class="panel"><span>当前预留（USD）</span><strong>{{ gatewayDollars(totalReserved) }}</strong></article></div>
          <div class="ai-gateway-layout"><section class="panel"><div class="ai-security-heading"><div><h2>接入状态与运行边界</h2><p>管理配置、运行时集成和实际健康分别核对。</p></div></div><div class="ai-security-body"><dl class="ai-security-facts"><div><dt>治理开关</dt><dd>{{ bundle.config.enabled?'已启用':'未启用，调用拒绝' }}</dd></div><div><dt>运行时集成</dt><dd>{{ bundle.runtime.configured?'已配置集成参数，仍需验证实际调用':'未配置，不能宣称网关已接入' }}</dd></div><div><dt>运行节点回报</dt><dd>{{ bundle.nodes.length }} 条节点记录</dd></div><div><dt>概览返回的有效凭据</dt><dd>{{ bundle.credentials.filter(item=>gatewayCredentialActive(item)).length }} 条（有界预览）</dd></div></dl><p>流式协议采用 strict_hold：完整接收、检测并重新核对授权后回放，不承诺首令牌低延迟。多模态内容仅在对应检测配置和格式受支持时处理；MCP 工具动作仍须独立授权和批准。</p><p class="table-secondary">节点登记或配置成功不证明控制效果，需用允许、拒绝、额度不足和输出阻断的真实请求分别验收。</p><div class="ai-security-actions"><a class="button button-secondary" :href="home+'/ai-security?tab=deploy'">查看客户侧部署边界</a><button class="button button-secondary" :disabled="!!busy" @click="download('config')">导出已保存网关配置</button></div></div></section><aside class="panel ai-security-assurance"><h2>预算口径</h2><p>应用预算采用 USD 微美元，界面以美元填写和展示；它是累计终身额度，不自动按月重置。</p><p>模型预算为 0 时拒绝提供方调用；缓存命中不新增提供方费用，仍受 RPM／TPM 限制。工具／RAG 不计模型占额，RPM 或 TPM 为 0 时所有调用拒绝。预留金额仍占用可用额度，不能当作已经释放的余额。</p><p>每次回退独立保守占额，未决预留不会自动视为已释放；需按同一父请求核对每次尝试及结算。提供方价格由负责人配置，治理记录采用保守报价计费，不是提供方实际账单。</p></aside></div>
          <section class="panel"><div class="ai-security-heading"><div><h2>能力与实际验收边界</h2><p>能力仅来自运行节点上报；配置开启、节点上报与真实请求验收分别核对。</p></div></div><div class="ai-gateway-capabilities"><article v-for="capability in gatewayCapabilities" :key="capability.id"><h3>{{ capability.label }}</h3><p>{{ capability.boundary }}</p><p class="table-secondary">{{ bundle.nodes.filter(item=>item.capabilities?.includes(capability.id)).length }} 个节点上报此能力；未提供独立验收结论。</p></article></div><p class="ai-security-body table-secondary">实际允许、拒绝、输出阻断、缓存命中后重新授权、逐目的端回退与预算结算，需逐项核对真实审计。<a :href="home+'/ai-security?tab=evaluations'">打开运行时安全回归与证据</a></p></section>
          <section class="panel"><div class="ai-security-heading"><div><h2>实际应用额度记录</h2><p>累计花费与当前预留来自服务端；未收到使用记录的应用单独标注。</p></div></div><div v-if="bundle.config.applications.length" class="table-scroll" role="region" aria-label="应用预算与用量" tabindex="0"><table class="data-table"><thead><tr><th scope="col">应用</th><th scope="col" class="table-number">终身额度（USD）</th><th scope="col" class="table-number">累计报价计费（USD）</th><th scope="col" class="table-number">预留（USD）</th><th scope="col" class="table-number">RPM / TPM</th></tr></thead><tbody><tr v-for="item in bundle.config.applications" :key="item.id"><th scope="row">{{ item.name }}<span class="table-secondary">{{ item.id }} · {{ item.enabled?'启用':'停用' }}</span></th><td class="table-number">{{ gatewayDollars(item.budgetMicros) }}</td><td class="table-number">{{ usageFor(item.id)?gatewayDollars(usageFor(item.id)!.spentMicros):'未收到使用记录' }}</td><td class="table-number">{{ usageFor(item.id)?gatewayDollars(usageFor(item.id)!.reservedMicros):'未收到预留记录' }}</td><td class="table-number">{{ item.requestsPerMinute }} / {{ item.tokensPerMinute }}</td></tr></tbody></table></div><p v-else class="empty-state">尚未登记网关应用。</p></section>
        </template>

        <template v-else-if="tab==='applications'">
          <form class="panel" @submit.prevent="saveConfig"><div class="ai-security-heading"><div><h2 ref="applicationsHeading" tabindex="-1">应用授权与预算限流</h2><p>应用 ID、主体与模型／工具范围共同限制调用。此处授权不能放宽 AI 运行时可信目录。</p></div><button v-if="owner" class="button button-primary" :disabled="loading || !!busy || !dirty || outdated || configNeedsReadback || !!cacheRecovery">{{ busy==='save'?'正在保存…':'保存全部网关配置' }}</button></div><div class="ai-security-body"><label class="ai-security-toggle"><input v-model="enabled" type="checkbox" :disabled="!owner || !!busy">启用网关应用调用<small>关闭时所有网关调用按服务端开关拒绝。</small></label><label class="field">运行时可信目录 SHA-256<input v-model.trim="catalogDigest" :required="enabled" maxlength="64" :disabled="!owner || !!busy" spellcheck="false"><small>启用时必填真实私网运行时的 catalogDigest；服务端调用授权强制核对，不能用请求自报摘要替代。</small></label><div class="ai-security-records"><button v-for="(item,index) in applications" :key="index" type="button" :aria-pressed="selectedApp===index" @click="selectedApp=index">{{ item.name || item.id || '未命名应用' }}</button></div><fieldset v-if="app" class="ai-security-fields" :disabled="!owner || !!busy"><label class="field">稳定应用 ID<input v-model.trim="app.id" required maxlength="80"></label><label class="field">应用名称<input v-model.trim="app.name" required maxlength="128"></label><label class="field">固定主体 ID<input v-model.trim="app.principalId" required maxlength="80"><small>应用凭证采用这个可信主体，调用参数不能切换身份。</small></label><label class="ai-security-toggle"><input v-model="app.enabled" type="checkbox">启用此应用</label><label class="ai-security-toggle"><input v-model="app.allowRag" type="checkbox">允许受控 RAG 检索<small>仍须满足主体、文档 ACL、租约及正文摘要策略；也会消耗调用限额。</small></label><label class="ai-security-toggle"><input v-model="app.requireTask" type="checkbox">所有执行必须绑定有效任务<small>任务范围、用途、步数与祖先状态继续由实际执行点核对，不能用任务 ID 代替应用凭证。</small></label><label class="field field-wide">允许模型 ID<input v-model="app.modelIdsText" maxlength="4000"><small>多个明确 ID 用逗号分隔；空范围不会放行全部模型。</small></label><label class="field field-wide">允许工具 ID<input v-model="app.toolIdsText" maxlength="4000"><small>工具动作、目标和批准继续由运行时安全策略限制。</small></label><label class="field">模型累计终身额度（USD）<input v-model.trim="app.budgetUSD" required inputmode="decimal" maxlength="24"><small>最多 6 位小数，不自动重置；0 拒绝提供方调用，缓存命中仍受 RPM／TPM 限制，工具／RAG 不计模型占额。</small></label><label class="field">每分钟请求上限（RPM）<input v-model.number="app.requestsPerMinute" type="number" required min="0" max="1000000" step="1"><small>0 表示拒绝调用。</small></label><label class="field">每分钟令牌上限（TPM）<input v-model.number="app.tokensPerMinute" type="number" required min="0" max="1000000000" step="1"><small>0 表示拒绝调用，以服务端保守预留与结算为准。</small></label><div class="field-wide ai-gateway-policy"><h3>受控缓存</h3><label class="ai-security-toggle"><input v-model="app.cachePolicy.enabled" type="checkbox">启用此应用的加密精确缓存<small>仅缓存受支持的规范化文本，不缓存媒体正文；配置不证明节点已使用缓存，命中仍须重新核对授权与策略。</small></label><div class="ai-security-fields"><label class="field">缓存 TTL（秒）<input v-model.number="app.cachePolicy.ttlSeconds" :disabled="!app.cachePolicy.enabled" type="number" :required="app.cachePolicy.enabled" min="1" max="86400" step="1"></label><label class="field">最多条目<input v-model.number="app.cachePolicy.maxEntries" :disabled="!app.cachePolicy.enabled" type="number" :required="app.cachePolicy.enabled" min="1" max="10000" step="1"></label><label class="field">容量上限（字节）<input v-model.number="app.cachePolicy.maxBytes" :disabled="!app.cachePolicy.enabled" type="number" :required="app.cachePolicy.enabled" min="1" max="16777216" step="1"></label><label class="field">加密密钥版本<input v-model.trim="app.cachePolicy.keyVersion" :disabled="!app.cachePolicy.enabled" :required="app.cachePolicy.enabled" maxlength="80" autocomplete="off"><small>轮换版本会隔离旧缓存，实际密钥由部署环境提供。</small></label><label class="field field-wide">缓存加密密钥环境引用<input v-model.trim="app.cachePolicy.keyRef" :disabled="!app.cachePolicy.enabled" :required="app.cachePolicy.enabled" maxlength="116" autocomplete="off" spellcheck="false"><small>必须为 env:SCARLET_GATEWAY_* 引用；不保存密钥正文，不支持未加密缓存。</small></label></div></div></fieldset><p v-else class="empty-state">尚无网关应用配置。</p><div v-if="owner && savedApp" class="ai-security-actions"><button type="button" class="button button-secondary" :disabled="loading || !!busy || !!cacheRecovery" @click="openCacheReview">核对并清除此应用缓存</button><span>用于提前失效。到期缓存已停止使用，由后台分批清理。</span></div><div v-if="owner" class="ai-security-actions"><button class="button button-secondary" type="button" :disabled="!!busy" @click="addApp">添加应用</button><button v-if="app" class="button button-secondary" type="button" :disabled="!!busy" @click="removeApp">从草稿移除此应用</button></div><p v-else class="table-secondary">当前账号仅可查看；预算及授权范围修改需要负责人。</p></div></form>
          <AIGatewayWorkloads :workspace="workspace" :configuration="bundle.config" @session-expired="emit('sessionExpired')" @refresh-credentials="load(false)"/>
          <details class="panel"><summary class="ai-security-body">核对原有可信应用授权目录</summary><AIGatewayCatalog :workspace="workspace" kind="applications" @session-expired="emit('sessionExpired')"/></details>
        </template>

        <template v-else-if="tab==='providers'">
          <form class="panel" @submit.prevent="saveConfig"><div class="ai-security-heading"><div><h2>模型提供方与有序回退</h2><p>端点与密钥只保存受限环境引用，实际秘密由部署环境独立提供。</p></div><button v-if="owner" class="button button-primary" :disabled="loading || !!busy || !dirty || outdated || configNeedsReadback || !!cacheRecovery">{{ busy==='save'?'正在保存…':'保存全部网关配置' }}</button></div><div class="ai-security-body"><div class="ai-security-records"><button v-for="(item,index) in providers" :key="index" type="button" :aria-pressed="selectedProvider===index" @click="selectedProvider=index">{{ item.name || item.id || '未命名提供方' }}</button></div><fieldset v-if="provider" class="ai-security-fields" :disabled="!owner || !!busy"><label class="field">提供方记录 ID<input v-model.trim="provider.id" required maxlength="128"></label><label class="field">显示名称<input v-model.trim="provider.name" required maxlength="128"></label><label class="field">提供方标识<input v-model.trim="provider.provider" required maxlength="80"></label><label class="field">逻辑模型 ID<input v-model.trim="provider.modelId" required maxlength="80"></label><label class="field field-wide">固定上游模型／部署映射<input v-model.trim="provider.upstreamModel" required maxlength="160"><small>调用者不能通过正文选择未授权的实际模型。</small></label><label class="field">模型处理地域<input v-model.trim="provider.region" required maxlength="80"></label><label class="field">响应格式<select v-model="provider.responseFormat" required><option value="">选择受支持格式</option><option value="openai_chat">openai_chat 严格响应校验</option></select><small>核对固定模型映射并拒绝未授权工具调用；媒体还须有可信目录授权与实际扫描器就绪。</small></label><label class="field">固定端点环境引用<input v-model.trim="provider.endpointRef" required maxlength="160" autocomplete="off" spellcheck="false"><small>仅支持 env:SCARLET_GATEWAY_*，不直接填写 URL。</small></label><label class="field">密钥环境引用<input v-model.trim="provider.secretRef" required maxlength="160" autocomplete="off" spellcheck="false"><small>仅支持 env:SCARLET_GATEWAY_*，不填写密钥正文。</small></label><label class="field">每百万输入令牌价格（USD）<input v-model.trim="provider.inputUSDPerMillion" required inputmode="decimal" maxlength="24"></label><label class="field">每百万输出令牌价格（USD）<input v-model.trim="provider.outputUSDPerMillion" required inputmode="decimal" maxlength="24"></label><label class="ai-security-toggle"><input v-model="provider.enabled" type="checkbox">启用此提供方路由</label><div class="field-wide ai-gateway-policy"><h3>逐目的端回退顺序</h3><p class="table-secondary">根路由失败后按此顺序尝试，最多 3 项。每个目标独立核对目录策略、应用授权、地域、加密与预算；当前执行不递归展开目标列表。</p><ol v-if="provider.fallbackModelIds.length" class="ai-gateway-fallbacks"><li v-for="(modelId,index) in provider.fallbackModelIds" :key="modelId"><div><strong>{{ modelId }}</strong><span class="table-secondary">{{ fallbackProvider(modelId)?.provider || '未找到启用提供方' }} · {{ fallbackProvider(modelId)?.region || '地域未确认' }}</span><span v-if="missingFallbackAuthorizations(modelId)" class="ai-gateway-warning">以下应用尚未授权此目的端：{{ missingFallbackAuthorizations(modelId) }}</span><span v-else class="table-secondary">应用目录范围已包含此目的端；可信目录与运行时策略仍需独立批准。</span></div><div class="ai-security-actions"><button type="button" class="button button-secondary" :disabled="index===0" :aria-label="'上移回退目标 '+modelId" @click="moveFallback(index,-1)">上移</button><button type="button" class="button button-secondary" :disabled="index===provider.fallbackModelIds.length-1" :aria-label="'下移回退目标 '+modelId" @click="moveFallback(index,1)">下移</button><button type="button" class="button button-secondary" :aria-label="'移除回退目标 '+modelId" @click="provider.fallbackModelIds.splice(index,1)">移除</button></div></li></ol><p v-else>未配置回退；失败后不会自动切换提供方。</p><div class="ai-gateway-address"><label class="field">添加已登记目的端<select v-model="fallbackChoice" :disabled="provider.fallbackModelIds.length>=3"><option value="">选择独立启用的逻辑模型</option><option v-for="item in fallbackOptions" :key="item.id" :value="item.modelId">{{ item.name || item.id }} · {{ item.modelId }} · {{ item.region }}</option></select></label><button type="button" class="button button-secondary" :disabled="!fallbackChoice || provider.fallbackModelIds.length>=3" @click="addFallback">加入回退顺序</button></div></div></fieldset><p v-else class="empty-state">尚未登记网关模型提供方。</p><div v-if="owner" class="ai-security-actions"><button class="button button-secondary" type="button" :disabled="!!busy" @click="addProvider">添加提供方</button><button v-if="provider" class="button button-secondary" type="button" :disabled="!!busy" @click="removeProvider">从草稿移除此提供方</button></div><p class="table-secondary">此处为负责人维护的路由和价格配置；实际端点接入、授权及供应商计费需要分别验证。</p><div v-if="provider && bundle.config.providers.some(item=>item.id===provider?.id)" class="ai-security-actions"><button type="button" class="button button-secondary" :disabled="!!busy" @click="exportProvider">导出此已保存路由配置</button><span>导出按路由筛选的已保存配置，需分别验证完整回退链。</span></div></div></form>
          <details class="panel"><summary class="ai-security-body">核对原有可信模型目标目录</summary><AIGatewayCatalog :workspace="workspace" kind="models" @session-expired="emit('sessionExpired')"/></details>
        </template>

        <section v-else-if="tab==='credentials'" class="panel"><div class="ai-security-heading"><div><h2 ref="credentialsHeading" tabindex="-1">应用授权凭证</h2><p>凭证绑定已保存应用及其主体。历史列表只返回前缀，不返回秘密值。凭据有效不表示应用、主体、任务或资料权限仍然放行。</p></div><button class="button button-secondary" :disabled="credentialsLoading || !!busy" @click="loadCredentials()">刷新当前凭据页</button></div><div v-if="credentialRecovery" class="ai-security-body message message-warning" role="status"><p>应用 {{credentialRecovery.applicationId}} 的签发“{{credentialRecovery.name}}”结果需要核对。秘密仅响应一次，列表不能恢复旧令牌；不能把没有收到回包视为没有签发。</p><button type="button" class="button button-secondary" :disabled="credentialsLoading || !!busy" @click="readCredentialRecovery">读取原应用最新凭据元数据</button><p v-if="credentialRecovery.read">已读取原应用的有界最新页。请核对前缀、用途与创建时间，并撤销确认多余的凭据；未出现在本页不证明没有签发。</p><p v-if="credentialRecovery.issuedId">令牌未保留的已知凭据 #{{credentialRecovery.issuedId}} 须先在元数据列表撤销。</p><button v-if="credentialRecoveryReady" type="button" class="button button-secondary" :disabled="credentialsLoading || !!busy" @click="credentialRecovery=undefined">已核对并处理记录，准备另一次签发</button></div><form v-if="canCredentials" class="ai-security-body" @submit.prevent="createCredential"><fieldset class="ai-security-fields" :disabled="!!busy || !!token || !!credentialRecovery"><label class="field">已保存应用<select v-model="credentialApp" required><option value="">选择应用</option><option v-for="item in bundle.config.applications" :key="item.id" :value="item.id">{{ item.name }} · {{ item.id }}</option></select></label><label class="field">凭证用途名称<input v-model.trim="credentialName" required maxlength="160"></label></fieldset><div class="ai-security-actions"><button class="button button-primary" :disabled="!!busy || !!token || !!credentialRecovery || !bundle.config.applications.length">{{ busy==='credential'?'正在创建…':'创建应用凭证' }}</button><span>凭证不绕过应用开关、预算及运行时授权。</span></div></form><p v-else class="ai-security-body table-secondary">当前账号仅可查看，创建及撤销凭证需要负责人或经办人。</p><div v-if="token" class="ai-security-body"><div class="ai-security-note"><strong>凭证 #{{ tokenId }} 的令牌仅本次返回</strong><p>默认遮挡，60 秒后或离开页签清除显示；这不表示凭证自动过期。</p><label class="field">应用调用令牌<input :type="showToken?'text':'password'" :value="token" readonly autocomplete="new-password" spellcheck="false"></label><div class="ai-security-actions"><button class="button button-secondary" @click="copyToken">复制令牌</button><button class="button button-secondary" @click="showToken=!showToken">{{ showToken?'遮挡令牌':'临时显示' }}</button><button class="button button-secondary" @click="clearToken">清除此页令牌</button></div></div></div><form class="ai-security-filter" @submit.prevent="loadCredentials('',false,{...credentialFilters})"><label class="field">凭据状态<select v-model="credentialFilters.state" :disabled="credentialsLoading"><option value="">全部状态</option><option value="active">有效且未过期</option><option value="expired">已过期（含已停用）</option><option value="disabled">已停用且未过期</option></select></label><label class="field">已保存应用<select v-model="credentialFilters.applicationId" :disabled="credentialsLoading"><option value="">全部应用</option><option v-for="app in bundle.config.applications" :key="app.id" :value="app.id">{{ app.name }} · {{ app.id }}</option></select></label><button class="button button-primary" :disabled="credentialsLoading">查询凭据</button><button class="button button-secondary" type="button" :disabled="credentialsLoading" @click="resetCredentialFilters">重置筛选</button></form><p v-if="credentialFilterDraft && credentialPage" class="ai-security-body table-secondary">筛选尚未应用；列表与分页保留上次成功读取的范围。</p><p v-if="credentialsReadAt" class="ai-security-body table-secondary">凭据元数据读取于 {{formatDate(credentialsReadAt,true)}}，不代表执行时权限仍然有效。</p><p v-if="credentialsError" class="message message-error" role="alert">{{ credentialsError }}{{ credentialPage?' 下方保留上次成功读取的凭据范围。':'' }}<button class="button button-secondary" :disabled="credentialsLoading" @click="loadCredentials()">重试此页</button></p><p v-if="credentialsLoading" class="ai-security-body" role="status">正在读取凭据元数据…</p><div v-if="credentialPage?.items.length" class="table-scroll" role="region" aria-label="应用授权凭证" tabindex="0"><table class="data-table"><thead><tr><th scope="col">用途与应用</th><th scope="col">安全前缀</th><th scope="col">状态</th><th scope="col" class="table-date">创建时间</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="item in credentialPage.items" :key="item.id"><th scope="row">{{ item.name }}<span class="table-secondary">{{ item.applicationId }} · #{{ item.id }}</span></th><td class="mono">{{ item.prefix }}</td><td>{{ ({active:'读取时有效且未过期',expired:'短期凭据已到期',disabled:'已停用'} as Record<string,string>)[item.status] || '状态未记录' }}<span v-if="item.expiresAt" class="table-secondary">到期 {{ formatDate(item.expiresAt) }}</span><span v-if="item.workloadBindingId" class="table-secondary">工作负载绑定 #{{ item.workloadBindingId }} · 签发修订 {{ item.workloadBindingRevision ?? '历史未记录' }}</span><span v-if="item.revokedAt" class="table-secondary">{{ gatewayRevocationLabel(item.revocationReason) }} · {{ formatDate(item.revokedAt) }}</span></td><td class="table-date">{{ formatDate(item.createdAt) }}</td><td><button v-if="canCredentials && !item.revokedAt" class="button button-secondary" :disabled="!!busy" @click="revokeCredential(item)">{{ item.enabled?'撤销凭证':'明确撤销此凭证' }}</button><span v-else>只读历史</span></td></tr></tbody></table></div><p v-else-if="credentialPage && !credentialsLoading && !credentialsError" class="empty-state">当前筛选分页没有凭据。可重置筛选，或核对所选应用；空列表不表示应用已经接入。</p><div v-if="credentialPage" class="ai-security-pagination"><button class="button button-secondary" :disabled="credentialsLoading || !credentialHistory.length" @click="previousCredentials">较新一页</button><button class="button button-secondary" :disabled="credentialsLoading || !credentialPage.hasMore || !credentialPage.nextCursor" @click="loadCredentials(credentialPage.nextCursor,true)">较早一页</button><p>第 {{ credentialHistory.length+1 }} 页 · 本页 {{ credentialPage.items.length }} 条 · 每页最多 {{ credentialPage.limit }} 条 · {{ credentialPage.hasMore?'仍有较早凭据':'已到当前范围最早凭据' }}</p><p>已应用状态 {{ ({active:'有效',expired:'已过期',disabled:'已停用'} as Record<string,string>)[appliedCredentialFilters.state] || '全部' }} · 应用 {{ appliedCredentialFilters.applicationId || '全部' }}。创建或撤销不改变秘密仅返回一次的规则。</p></div><p v-if="credentialPage?.boundary" class="ai-security-body table-secondary">{{ credentialPage.boundary }}</p></section>

        <div v-else-if="tab==='nodes'" class="ai-gateway-sections">
          <section class="panel"><div class="ai-security-heading"><div><h2>实际运行节点与能力回报</h2><p>仅展示服务端收到的节点状态及观察时点，未回报不视为健康。节点能力声明不等同于控制验收。</p></div><button class="button button-secondary" :disabled="loading || !!busy" @click="load(false)">刷新运行记录</button></div><div v-if="bundle.nodes.length" class="table-scroll" role="region" aria-label="网关运行节点" tabindex="0"><table class="data-table"><thead><tr><th scope="col">节点与版本</th><th scope="col">来源报告状态</th><th scope="col">最后回报时间</th><th scope="col">节点上报能力</th><th scope="col">失败记录</th></tr></thead><tbody><tr v-for="item in bundle.nodes" :key="item.id"><th scope="row">{{ item.id }}<span class="table-secondary">{{ item.version || '未上报版本' }}</span></th><td>{{ nodeStatus(item.status) }}</td><td>{{ item.lastSeenAt?formatDate(item.lastSeenAt):'尚未收到观察时点' }}</td><td><span v-for="capability in item.capabilities || []" :key="capability" class="table-secondary">{{ gatewayCapabilities.find(value=>value.id===capability)?.label || capability }}</span><span v-if="!item.capabilities?.length">未上报能力</span></td><td>{{ item.failures }}</td></tr></tbody></table></div><p v-else class="empty-state">尚未收到运行节点记录。配置了集成参数并不代表已建立实际连接。</p><p class="ai-security-body table-secondary">当前返回最多 1000 条节点记录；超过 90 秒未回报的节点由服务端标为过时。</p></section>
          <section class="panel"><div class="ai-security-heading"><div><h2>共享路由健康与熔断</h2><p>来自控制面的实际路由结果；节点共享同一熔断记录。空列表表示尚无路由观察。</p></div></div><div v-if="bundle.routeHealth?.length" class="table-scroll" role="region" aria-label="共享路由健康与熔断" tabindex="0"><table class="data-table"><thead><tr><th scope="col">提供方记录</th><th scope="col">失败次数</th><th scope="col">熔断截止时间</th><th scope="col">最近结果</th></tr></thead><tbody><tr v-for="item in bundle.routeHealth" :key="item.providerId"><th scope="row">{{ item.providerId }}</th><td>{{ item.failures }}</td><td>{{ item.openUntil?formatDate(item.openUntil):'无生效的熔断截止时间' }}</td><td>{{ item.lastOutcome || '尚无结果' }}</td></tr></tbody></table></div><p v-else class="empty-state">尚未收到共享路由健康记录。</p></section>
        </div>

        <section v-else-if="tab==='audit'" class="panel"><div class="ai-security-heading"><div><h2>配置与调用审计</h2><p>按顺序读取配置、调用与每次尝试的服务端证据。回退尝试需分别核对目的端、结果及占额；导出仅覆盖当前分页范围。</p></div><div class="page-actions"><button class="button button-secondary" :disabled="auditLoading" @click="loadAudit(auditAfter)">刷新当前页</button><button class="button button-secondary" :disabled="!!busy" @click="download('config')">导出已保存配置</button><button class="button button-secondary" :disabled="!!busy || !audit || auditLoading" @click="download('audit')">导出当前审计页</button></div></div><div class="ai-security-body"><label class="field">核对某条已保存路由<select v-model="auditProvider" :disabled="auditLoading" @change="changeAuditRoute"><option value="">完整审计页</option><option v-for="item in bundle.config.providers" :key="item.id" :value="item.id">{{ item.name }} · {{ item.id }}</option></select><small>保留完整分页链；服务端仅标记关联路由序号，导出携带该关联范围，不删除无关事件破坏链连续性。</small></label></div><p v-if="audit && auditProvider!==(audit.providerId || '')" class="ai-security-body table-secondary">路由选择尚未应用；分页、导出与下方关联标记仍采用已载入路由 {{audit.providerId || '全部'}}。</p><p v-if="auditReadAt" class="ai-security-body table-secondary">审计页读取于 {{formatDate(auditReadAt,true)}}。</p><p v-if="auditError" class="message message-error" role="alert">{{ auditError }}{{ audit?' 下方保留上次成功读取的分页与校验结果，本次查询未更新。':'' }}</p><p v-if="auditLoading" class="ai-security-body" role="status">正在读取并核对审计页…</p><div v-if="audit" class="ai-security-body"><p :class="audit.valid?'message':'message message-error'" :role="audit.valid?'status':'alert'">服务端当前页链校验：{{ audit.valid?'通过':'未通过' }} · 链头序号 {{ audit.headSequence }}。此结果不替代独立归档及外部核验。</p><p v-if="audit.providerId" class="table-secondary">已载入关联路由 {{ audit.providerId }}；本页 {{ audit.routeSequences?.length || 0 }} 条关联事件。下方保留完整链范围。</p><p class="table-secondary mono">链头 {{ audit.headHash }}</p><p class="table-secondary mono">页前摘要 {{ audit.previousHash || '初始链' }}</p></div><div v-if="audit?.items.length" class="table-scroll" role="region" aria-label="网关配置与调用审计" tabindex="0"><table class="data-table"><thead><tr><th scope="col">序号与时间</th><th scope="col">动作与对象</th><th scope="col">执行主体</th><th scope="col">绑定证据</th><th scope="col">链摘要</th></tr></thead><tbody><tr v-for="item in audit.items" :key="item.sequence"><th scope="row">#{{ item.sequence }}<span v-if="audit.providerId && audit.routeSequences?.includes(item.sequence)" class="table-secondary">与所选路由关联</span><span class="table-secondary">{{ formatDate(item.occurredAt) }}</span></th><td><span :title="item.action">{{ auditAction(item.action) }}</span><span class="table-secondary">{{ item.target }}</span></td><td>{{ item.actorId || '机器／运行时' }}</td><td>{{ gatewayAuditSummary(item) }}</td><td class="mono">{{ item.hash }}</td></tr></tbody></table></div><p v-else-if="audit && !auditLoading" class="empty-state">当前分页范围内没有审计记录。</p><div v-if="audit" class="ai-security-body ai-security-actions"><button class="button button-secondary" :disabled="auditLoading || !auditHistory.length" @click="previousAudit">上一页</button><button class="button button-secondary" :disabled="auditLoading || !audit.nextCursor || audit.nextCursor<=auditAfter || audit.nextCursor>=audit.headSequence" @click="loadAudit(audit.nextCursor,true)">下一页</button><span>当前 after={{ auditAfter }}，每页最多 100 条。需要逐页核对和导出完整范围。</span></div></section>
      </template>
    </template>
    <dialog ref="cacheDialog" class="app-dialog" aria-labelledby="cache-review-title" aria-describedby="cache-review-description" @keydown="containDialogTab" @cancel="event=>busy==='cache' && event.preventDefault()" @close="cacheReviewClosed">
      <form v-if="cachePlan" class="dialog-content" @submit.prevent="invalidateCache">
        <header class="dialog-heading"><h2 id="cache-review-title">清除已保存应用的缓存</h2><button type="button" class="icon-button" aria-label="关闭缓存清除确认" :disabled="busy==='cache'" @click="closeCacheReview"><Icon name="close" /></button></header>
        <div class="dialog-body">
          <p id="cache-review-description" class="dialog-description">清除该应用的加密缓存记录，并推进工作区网关配置修订。其他应用按旧修订保存的缓存也会停止命中。操作保留账务、历史调用记录和未保存的草稿。</p>
          <dl class="ai-security-facts"><div><dt>已保存应用</dt><dd>{{ cachePlan.name }}<span class="table-secondary mono">{{ cachePlan.id }}</span></dd></div><div><dt>当前核对修订</dt><dd>{{ cachePlan.revision }}</dd></div></dl>
          <p v-if="!cachePlanCurrent" class="message message-warning" role="status">已读取的应用或修订有变化，请返回重新核对。</p>
          <p v-if="cacheDialogError" class="message message-error" role="alert">{{ cacheDialogError }}</p>
        </div>
        <footer class="dialog-actions"><button type="button" class="button button-secondary" autofocus :disabled="busy==='cache'" @click="closeCacheReview">返回核对</button><button type="submit" class="button button-danger" :disabled="!cachePlanCurrent || !!busy || !!cacheRecovery">{{ busy==='cache'?'正在清除…':'确认清除此应用缓存' }}</button></footer>
      </form>
    </dialog>
  </section>
</template>

<style scoped>
.ai-gateway-workspace .message p{margin:0 0 10px}.ai-gateway-workspace .message button{margin:4px 8px 4px 0}.ai-gateway-workspace .data-table td{overflow-wrap:anywhere}.ai-gateway-workspace .data-table .mono{font-size:13px}
</style>
