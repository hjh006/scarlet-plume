<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { ApiError, describeError, downloadFile, formatDate, request, workspacePath, type Workspace } from './api'
import Icon from './Icon.vue'
import AISecurityEntityEditor from './AISecurityEntityEditor.vue'
import AISecurityEvidence from './AISecurityEvidence.vue'
import AISecurityRuntimeChains from './AISecurityRuntimeChains.vue'
import AISecurityRegression from './AISecurityRegression.vue'
import AISecurityHistory from './AISecurityHistory.vue'
import AISecurityApprovalReview from './AISecurityApprovalReview.vue'
import AISecurityMediaRollout from './AISecurityMediaRollout.vue'
import { aiApprovalId, aiApprovalReviewNote, type AIApprovalDecision, type AIApprovalReviewIntent } from './ai-security-approvals'
import { aiConfigHasDuplicateIds, aiText, buildAIRequest, buildAIWireRequest, validateAIConfigurationExtensions, validateAIModelPayload, validateAIToolProtocol, validateAIToolRequest, newAIRequestId, type AIRecord, type AIRequestDraft } from './ai-security-editor'
import { aiDecision, aiDecisionLabel, aiReason, aiRecords, aiSections, aiString, type AISecurityBundle } from './ai-security'
import { runtimeEvidenceQuery, type RuntimeEvidencePage, type RuntimeFilters, type RuntimeSource } from './ai-security-runtime'
import './ai-security.css'

const props=defineProps<{workspace:Workspace;initialTab?:string}>()
const emit=defineEmits<{sessionExpired:[]}>()
const base=workspacePath(props.workspace.slug)+'/ai-security'
const home='#/'+encodeURIComponent(props.workspace.slug)
const controller=new AbortController()
const bundle=ref<AISecurityBundle|null>(null)
const draftRevision=ref(0)
const configuration=ref<AIRecord>({})
const savedConfiguration=ref('')
const loading=ref(true),busy=ref(''),error=ref(''),feedback=ref('')
const requestedTab=props.initialTab || 'configuration'
const initialKind:AIRequestDraft['kind']=requestedTab==='runtime'?'model':requestedTab==='agent'?'tool':'rag'
const initialTab=['rag','runtime','agent'].includes(requestedTab)?'evaluate':['configuration','evaluate','approvals','evaluations','evidence','deploy'].includes(requestedTab)?requestedTab:'configuration'
const tab=ref(initialTab),section=ref('applications')
const editorValid=ref(true),advanced=ref(''),advancedOpen=ref(false)
const editorVersion=ref(0)
const result=ref<AIRecord|null>(null),selectedEvidence=ref<AIRecord|null>(null)
const requestForm=ref<HTMLFormElement|null>(null)
const wireTemplate=ref('')
const reviewNotes=reactive<Record<string,string>>({})
const reviewIntent=ref<AIApprovalReviewIntent|null>(null)
const reviewTrigger=ref<HTMLElement>()
const canWrite=computed(()=>['owner','operator'].includes(props.workspace.role))
const canReview=computed(()=>['owner','reviewer'].includes(props.workspace.role))
const canManageSources=computed(()=>props.workspace.role==='owner')
const currentSection=computed(()=>aiSections.find(item=>item.key===section.value) || aiSections[0]!)
const dirty=computed(()=>JSON.stringify(configuration.value)!==savedConfiguration.value)
const configurationOutdated=computed(()=>bundle.value!==null && draftRevision.value!==bundle.value.revision)
const currentRecords=computed(()=>aiRecords(configuration.value,section.value))
const referenceRecords=computed(()=>Object.fromEntries(aiSections.map(item=>[item.key,aiRecords(configuration.value,item.key)])))
const requestDraft=reactive<AIRequestDraft>({requestId:newAIRequestId(),taskId:'',kind:initialKind,applicationId:'',principalId:'',documentIds:[],modelId:'',toolId:'',action:'',target:'',argumentsJSON:'{}',input:'',output:'',approvalId:'',issuedAt:new Date().toISOString(),expiresAt:new Date(Date.now()+300_000).toISOString()})
watch(requestDraft,()=>{wireTemplate.value=''})
const kindLabels:Record<string,string>={rag:'知识检索授权',model:'模型输入、外发与输出',tool:'Agent 工具动作'}
const requestConfiguration=computed(()=>bundle.value?.configuration || {})
const applications=computed(()=>aiRecords(requestConfiguration.value,'applications'))
const principals=computed(()=>aiRecords(requestConfiguration.value,'principals'))
const documents=computed(()=>aiRecords(requestConfiguration.value,'documents'))
const models=computed(()=>aiRecords(requestConfiguration.value,'models'))
const tools=computed(()=>aiRecords(requestConfiguration.value,'tools'))
const approvalHistory=ref<{refresh:()=>Promise<unknown>}|null>(null)
const savedTool=computed(()=>aiRecords(bundle.value?.configuration || {},'tools').find(item=>item.id===requestDraft.toolId))
const savedToolHighImpact=computed(()=>savedTool.value?.highImpact===true)
const runtimeEvidence=ref<RuntimeEvidencePage|null>(null)
const runtimeReadAt=ref('')
const runtimeLoading=ref(false),runtimeError=ref(''),runtimeBefore=ref(''),runtimeHistory=ref<string[]>([])
const runtimeFilters=reactive<RuntimeFilters>({sourceId:'',kind:'',outcome:''})
const appliedRuntimeFilters=ref<RuntimeFilters>({...runtimeFilters})
let runtimeGeneration=0
const sourceDraft=reactive({name:'',applicationId:''})
const issuedToken=ref(''),issuedSource=ref<number|null>(null),showToken=ref(false)
let tokenTimer:ReturnType<typeof setTimeout>|undefined
let tokenGeneration=0

function clearToken() {
  issuedToken.value='';issuedSource.value=null;showToken.value=false
  if(tokenTimer) clearTimeout(tokenTimer)
  tokenTimer=undefined
}
watch(tab,()=>{tokenGeneration++;clearToken()})
async function loadRuntimeEvidence(before='',remember=false,filters:RuntimeFilters=appliedRuntimeFilters.value) {
  const generation=++runtimeGeneration
  runtimeLoading.value=true;runtimeError.value=''
  try {
    const query=runtimeEvidenceQuery(filters,before)
    const next=await request<RuntimeEvidencePage>(base+'/runtime-evidence?'+query,{signal:controller.signal})
    if(controller.signal.aborted || generation!==runtimeGeneration)return
    if(remember)runtimeHistory.value.push(runtimeBefore.value)
    else if(!before)runtimeHistory.value=[]
    runtimeEvidence.value=next;runtimeBefore.value=before;appliedRuntimeFilters.value={...filters};runtimeReadAt.value=new Date().toISOString()
    return true
  } catch(cause) {
    if(controller.signal.aborted || generation!==runtimeGeneration)return
    if(cause instanceof ApiError && cause.status===401)emit('sessionExpired')
    runtimeError.value=describeError(cause)
  } finally { if(generation===runtimeGeneration)runtimeLoading.value=false }
}
async function applyRuntimeFilters() { await loadRuntimeEvidence('',false,{...runtimeFilters}) }
async function resetRuntimeFilters() { Object.assign(runtimeFilters,{sourceId:'',kind:'',outcome:''});await applyRuntimeFilters() }
async function previousRuntimePage() {
  const before=runtimeHistory.value.at(-1)
  if(before!==undefined && await loadRuntimeEvidence(before))runtimeHistory.value.pop()
}
async function reload() { await Promise.all([load(),loadRuntimeEvidence()]) }
async function createRuntimeSource() {
  if(!canManageSources.value || busy.value || issuedToken.value || !bundle.value?.publishedDigest) return
  busy.value='source';error.value='';feedback.value=''
  const generation=tokenGeneration
  try{
    const issued=await request<{source:RuntimeSource;token:string}>(base+'/runtime-sources',{method:'POST',body:JSON.stringify({...sourceDraft,catalogDigest:bundle.value.publishedDigest}),signal:controller.signal})
    if(controller.signal.aborted) return
    if(generation!==tokenGeneration) {
      issued.token=''
      feedback.value='来源已创建，但已离开凭证显示页，令牌未保留；请根据部署需要撤销该来源并重新创建。'
      await loadRuntimeEvidence()
      return
    }
    clearToken();issuedToken.value=issued.token;issuedSource.value=issued.source.id
    tokenTimer=setTimeout(clearToken,60_000)
    sourceDraft.name='';feedback.value='独立运行来源已创建。请在本页清除前将令牌配置到授权客户侧执行服务。'
    await loadRuntimeEvidence()
  }catch(cause){handleError(cause)}finally{busy.value=''}
}
async function revokeRuntimeSource(source:RuntimeSource) {
  if(!canManageSources.value || busy.value) return
  busy.value='revoke-source';error.value='';feedback.value=''
  try{
    await request(base+'/runtime-sources/'+source.id+'/revoke',{method:'POST',body:'{}',signal:controller.signal})
    if(issuedSource.value===source.id)clearToken()
    feedback.value='来源已撤销，历史回执保留；后续报告将按撤销状态校验。';await loadRuntimeEvidence()
  }catch(cause){handleError(cause)}finally{busy.value=''}
}
async function rotateRuntimeSource(source:RuntimeSource) {
  if(!canManageSources.value || busy.value || issuedToken.value || source.revokedAt)return
  busy.value='rotate-source-'+source.id;error.value='';feedback.value=''
  const generation=tokenGeneration
  try {
    const issued=await request<{source:RuntimeSource;token:string}>(base+'/runtime-sources/'+source.id+'/rotate-token',{method:'POST',body:JSON.stringify({expectedTokenVersion:source.tokenVersion || 1}),signal:controller.signal})
    if(controller.signal.aborted)return
    if(generation!==tokenGeneration) {
      issued.token='';feedback.value='令牌已轮换，旧令牌已失效；已离开显示页，新令牌未保留。请返回部署页再次轮换并更新执行服务。'
    } else {
      clearToken();issuedToken.value=issued.token;issuedSource.value=issued.source.id
      tokenTimer=setTimeout(clearToken,60_000)
      feedback.value='来源 #'+source.id+' 的令牌已轮换，旧令牌立即失效。请更新客户侧上报凭证，再用真实回执验证接入。'
    }
    await loadRuntimeEvidence()
  } catch(cause) { handleError(cause) } finally { busy.value='' }
}
async function copyToken() {
  if(!issuedToken.value) return
  try{await navigator.clipboard.writeText(issuedToken.value);feedback.value='令牌已复制，请存放到客户侧受限凭证配置。'}
  catch{error.value='未能访问剪贴板，请临时显示后手动复制，并及时清除此页令牌。'}
}
const runtimeStageLabels:Record<string,string>={decision:'策略决策',prepared:'执行前准备',completed:'执行后回执'}
function executionLabel(value:string) {
  return ({not_attempted:'未发起上游动作',blocked_output:'上游已返回，输出被阻止',not_executed:'未执行上游动作',blocked:'已拒绝执行',succeeded:'来源报告执行成功',upstream_succeeded:'来源报告上游成功',failed:'来源报告执行失败',upstream_failed:'来源报告上游失败',unknown:'执行状态未知'} as Record<string,string>)[value] || value
}

function handleError(cause:unknown) {
  if(controller.signal.aborted) return
  if(cause instanceof ApiError && cause.status===401) emit('sessionExpired')
  error.value=cause instanceof ApiError && cause.status===409 ? (cause.message || '配置或批准状态已发生变化。')+' 请核对服务端提示及采用范围；当前草稿已保留。' : describeError(cause)
}
async function load(replaceDraft=true) {
  loading.value=true;error.value=''
  try {
    const next=await request<AISecurityBundle>(base,{signal:controller.signal})
    bundle.value=next
    if(replaceDraft) {
      configuration.value=JSON.parse(JSON.stringify(next.configuration)) as AIRecord
      savedConfiguration.value=JSON.stringify(configuration.value)
      draftRevision.value=next.revision
      advanced.value=JSON.stringify(configuration.value,null,2)
      editorValid.value=true
      editorVersion.value++
      renewRequest()
    }
  }catch(cause){handleError(cause)}finally{loading.value=false}
}
function updateRecords(records:AIRecord[]) { configuration.value={...configuration.value,[section.value]:section.value==='policy' ? records[0] || {} : records} }
function applyAdvanced() {
  try {
    const value:unknown=JSON.parse(advanced.value)
    if(!value || typeof value!=='object' || Array.isArray(value)) throw new Error('配置应是 JSON 对象。')
    configuration.value=value as AIRecord;editorValid.value=true;editorVersion.value++;feedback.value='配置已载入待保存草稿，请核对后保存。';error.value=''
  }catch(cause){error.value=cause instanceof Error ? cause.message : '配置 JSON 格式无效。'}
}
async function save() {
  if(!canWrite.value || !bundle.value || busy.value || !editorValid.value) return
  if(Array.isArray(configuration.value.approvals) && configuration.value.approvals.length) { error.value='动作批准由独立审批生成，不能通过配置导入或编辑写入。请移除该字段中的批准记录。';return }
  const duplicate=aiConfigHasDuplicateIds(configuration.value,aiSections)
  if(duplicate.length){error.value=duplicate.join('；');return}
  try { validateAIConfigurationExtensions(configuration.value) }
  catch(cause) { error.value=cause instanceof Error?cause.message:'请核对工具协议配置。';return }
  busy.value='save';error.value='';feedback.value=''
  try {
    await request(base+'/configuration',{method:'PUT',body:JSON.stringify({expectedRevision:draftRevision.value,configuration:configuration.value}),signal:controller.signal})
    feedback.value='配置版本已保存。客户侧执行服务需要采用该版本后，才能按该配置执行。'
    await load()
  }catch(cause){handleError(cause)}finally{busy.value=''}
}
function currentRequest():AIRecord { return buildAIRequest(requestDraft) }
function renewRequest() {
  const policy=bundle.value?.configuration.policy as AIRecord | undefined
  const seconds=Math.max(1,Math.min(Number(policy?.maxRequestAgeSeconds)||300,3600))
  const now=Date.now()
  requestDraft.requestId=newAIRequestId()
  requestDraft.issuedAt=new Date(now).toISOString()
  requestDraft.expiresAt=new Date(now+seconds*1000).toISOString()
  requestDraft.approvalId=''
  result.value=null;wireTemplate.value=''
}
function prepareWireTemplate() {
  try {
    const wire=buildAIWireRequest(requestDraft)
    if(requestDraft.kind==='model') {
      const model=aiRecords(bundle.value?.configuration || {},'models').find(item=>item.id===requestDraft.modelId)
      validateAIModelPayload(requestDraft.input,model ? aiString(model,'upstreamModel','id') : '',model?.media)
    }
    if(requestDraft.kind==='tool') validateAIToolRequest(savedTool.value,wire.arguments)
    wireTemplate.value=JSON.stringify(wire,null,2)
    error.value=''
  } catch(cause) {
    wireTemplate.value=''
    error.value=cause instanceof SyntaxError?'客户侧模型执行需要合法 JSON 正文；普通文本仍可在控制台作策略评估。':cause instanceof Error?cause.message:'请核对请求格式。'
  }
}
async function evaluate() {
  if(!canWrite.value || busy.value) return
  let payload:AIRecord
  try { payload=currentRequest() } catch(cause) { error.value=cause instanceof Error ? cause.message : '请核对请求格式。';return }
  busy.value='evaluate';error.value='';feedback.value='';result.value=null
  try {
    result.value=await request<AIRecord>(base+'/evaluate',{method:'POST',body:JSON.stringify(payload),signal:controller.signal})
    await load(false)
  }catch(cause){if(cause instanceof SyntaxError) error.value='工具参数 JSON 格式无效。';else handleError(cause)}finally{busy.value=''}
}
async function requestApproval() {
  if(!canWrite.value || requestDraft.kind!=='tool' || busy.value || !savedToolHighImpact.value) return
  if(requestForm.value && !requestForm.value.reportValidity()) return
  let payload:AIRecord
  try { payload=currentRequest() } catch(cause) { error.value=cause instanceof Error ? cause.message : '请核对请求格式。';return }
  busy.value='approval';error.value='';feedback.value=''
  try {
    await request(base+'/approvals',{method:'POST',body:JSON.stringify({request:payload,expectedRevision:bundle.value?.revision}),signal:controller.signal})
    feedback.value='限定工具请求已提交，等待独立批准。参数、请求引用或配置变化后需重新申请。';await load(false);tab.value='approvals'
  }catch(cause){handleError(cause)}finally{busy.value=''}
}
function review(item:AIRecord,decision:AIApprovalDecision) {
  if(!canReview.value || busy.value) return
  try {
    const id=aiApprovalId(item.id),note=aiApprovalReviewNote(reviewNotes[id] || '')
    reviewTrigger.value=document.activeElement instanceof HTMLElement?document.activeElement:undefined
    reviewIntent.value={id,decision,note};busy.value='review-dialog';error.value='';feedback.value=''
  }catch(cause){error.value=describeError(cause)}
}
async function closeReview() {
  reviewIntent.value=null
  if(busy.value==='review-dialog')busy.value=''
  await nextTick();await approvalHistory.value?.refresh()
  if(document.activeElement===document.body || document.activeElement?.closest('dialog:not([open])'))document.getElementById('ai-approvals-title')?.focus({preventScroll:true})
}
async function reviewed(item:AIRecord) {
  feedback.value='批准 #'+aiString(item,'id')+' 的独立复核结果已记录：'+aiDecisionLabel(item)+'。实际执行仍按原绑定范围核对。'
  reviewIntent.value=null;busy.value=''
  await load(false);await nextTick();await approvalHistory.value?.refresh()
  if(document.activeElement===document.body || document.activeElement?.closest('dialog:not([open])'))document.getElementById('ai-approvals-title')?.focus({preventScroll:true})
}
function approvalExpired(item:AIRecord) {
  return ['proposed','approved'].includes(aiString(item,'status')) && Date.parse(aiString(item,'expiresAt'))<=Date.now()
}
function restoreApprovalRequest(item:AIRecord) {
  const requestId=aiString(item,'requestId'),issuedAt=aiString(item,'issuedAt'),expiresAt=aiString(item,'expiresAt')
  if(!requestId || !issuedAt || !expiresAt) { error.value='此记录未提供可恢复的完整请求引用，请由申请方保留并使用原请求。';return }
  Object.assign(requestDraft,{requestId,taskId:aiString(item,'taskId'),kind:'tool',applicationId:aiString(item,'applicationId'),principalId:aiString(item,'principalId'),toolId:aiString(item,'toolId'),action:aiString(item,'action'),target:aiString(item,'target'),argumentsJSON:JSON.stringify(item.arguments || {},null,2),approvalId:item.status==='approved'?aiString(item,'id'):'',issuedAt,expiresAt,documentIds:[],modelId:'',input:'',output:''})
  result.value=null;tab.value='evaluate';error.value=''
  feedback.value=Date.parse(expiresAt)<=Date.now()?'原请求已经到期，可用于核对拒绝行为；继续实际操作需要重新申请。':'已恢复原请求引用、时效与限定参数。此处仅作策略评估，不会实际执行工具。'
}
async function download(kind:'export'|'runtime-config') {
  if(busy.value) return
  busy.value='download';error.value=''
  try{await downloadFile(base+'/'+kind,kind==='export'?'scarlet-ai-security-evidence.json':'scarlet-ai-runtime-config.json',controller.signal)}catch(cause){handleError(cause)}finally{busy.value=''}
}
function openAdvanced() { advancedOpen.value=!advancedOpen.value;if(advancedOpen.value)advanced.value=JSON.stringify(configuration.value,null,2) }
function timestamp(item:AIRecord) { const value=aiString(item,'createdAt','evaluatedAt','observedAt');return value?formatDate(value):'未提供时点' }
onMounted(reload)
onUnmounted(()=>{clearToken();controller.abort()})
</script>

<template>
  <section class="ai-security-workspace" :aria-busy="loading || !!busy">
    <header class="page-header"><div><p class="eyebrow">数据与 AI 安全 / 检索、模型与工具</p><h1>让 AI 的数据使用和动作有据可控</h1><p>维护可信配置，评估知识访问、模型外发与 Agent 工具请求，保留版本及验证记录。</p></div><div class="page-actions"><a class="button button-secondary" :href="home+'/security-platform'">八方向能力工作台</a><button class="button button-secondary" :disabled="loading || !!busy" @click="reload">{{ dirty?'重新载入并丢弃配置草稿':'刷新配置与证据' }}</button></div></header>
    <p v-if="error" class="message message-error" role="alert">{{ error }}</p><p v-if="feedback" class="message message-success" role="status">{{ feedback }}</p>
    <p v-if="loading && !bundle" class="message" role="status">正在载入 AI 配置与证据…</p><section v-if="!bundle && !loading" class="panel ai-security-empty"><h2>尚未取得 AI 安全配置</h2><p>当前无法确认已保存策略、运行来源或发布版本。可重新读取，或联系工作区负责人核对服务状态。</p><button class="button button-secondary" @click="reload">重新读取配置与证据</button></section>
    <template v-if="bundle">
      <div class="panel ai-security-config-source"><strong>服务端配置修订 {{ bundle.revision }}</strong><span>草稿基于修订 {{ draftRevision }}</span><span>{{ dirty?'有尚未保存的草稿':configurationOutdated?'当前草稿需要重新载入':'当前草稿与已保存版本一致' }}</span><span>{{ bundle.revision>0 && bundle.updatedAt && !bundle.updatedAt.startsWith('0001-')?formatDate(bundle.updatedAt):'尚未保存配置' }}</span><span>已发布修订 {{ bundle.publishedRevision || '尚未发布' }}</span><span>控制台评估不会实际调用模型或工具</span></div>
      <p v-if="configurationOutdated" class="message message-warning">其他成员已更新配置。当前草稿仍保留旧版本，提交时将检查原修订；请重新载入并核对后再修改。</p>
      <nav class="ai-security-tabs" aria-label="AI 运行时安全"><button :aria-pressed="tab==='configuration'" @click="tab='configuration'">资产与授权配置</button><button :aria-pressed="tab==='evaluate'" @click="tab='evaluate'">请求策略验证</button><button :aria-pressed="tab==='approvals'" @click="tab='approvals'">工具动作批准</button><button :aria-pressed="tab==='evaluations'" @click="tab='evaluations'">策略回归</button><button :aria-pressed="tab==='evidence'" @click="tab='evidence'">决策证据</button><button :aria-pressed="tab==='deploy'" @click="tab='deploy'">部署与覆盖</button></nav>
      <template v-if="tab==='configuration'">
        <form class="panel" @submit.prevent="save"><div class="ai-security-heading"><div><h2>可信资产与授权配置</h2><p>主体、资料权限和固定执行目标由配置提供；请求自报的权限和类别不会自动成为可信授权。</p></div><button v-if="canWrite" class="button button-primary" :disabled="!!busy || !dirty || !editorValid">{{ busy==='save'?'正在保存…':'保存配置版本' }}</button></div><div class="ai-security-split"><nav class="ai-security-list" aria-label="AI 配置对象"><button v-for="item in aiSections" :key="item.key" type="button" :aria-pressed="section===item.key" @click="section=item.key"><span>{{ item.label }}</span><span>{{ aiRecords(configuration,item.key).length }}</span></button></nav><AISecurityEntityEditor :key="`${editorVersion}/${section}`" :section="currentSection" :records="currentRecords" :references="referenceRecords" :singleton="section==='policy'" :disabled="!canWrite || !!busy" @update="updateRecords" @validation="editorValid=$event"/></div></form>
        <section class="panel"><div class="ai-security-heading"><div><h2>完整配置导入与核对</h2><p>用于保留当前引擎支持的完整策略字段；导入只修改待保存草稿，服务端仍会校验版本、引用与授权。</p></div><button class="button button-secondary" type="button" :aria-expanded="advancedOpen" @click="openAdvanced">{{ advancedOpen?'收起完整配置':'查看完整配置' }}</button></div><div v-if="advancedOpen" class="ai-security-body"><label class="field">配置 JSON<textarea v-model="advanced" class="mono" rows="16" spellcheck="false" :readonly="!canWrite"></textarea></label><div v-if="canWrite" class="ai-security-actions"><button class="button button-secondary" type="button" :disabled="!!busy" @click="applyAdvanced">载入待保存草稿</button></div></div></section>
      </template>
      <template v-else-if="tab==='evaluate'">
        <p v-if="dirty" class="message message-warning">评估采用服务端已保存修订 {{ bundle.revision }}，当前未保存草稿不参与判断。</p>
        <div class="ai-security-review"><section class="panel"><div class="ai-security-heading"><div><h2>按已保存配置评估一次请求</h2><p>使用合成测试内容。评估记录证明策略决策，真实拒绝和上游执行回执由客户侧运行时另行验证。</p></div></div><form ref="requestForm" class="ai-security-body" @submit.prevent="evaluate"><fieldset class="ai-security-fields" :disabled="!canWrite || !!busy"><label class="field">请求类型<select v-model="requestDraft.kind"><option v-for="(label,key) in kindLabels" :key="key" :value="key">{{ label }}</option></select></label><label class="field">应用<select v-model="requestDraft.applicationId" required><option value="">选择应用</option><option v-for="item in applications" :key="aiString(item,'id')" :value="aiString(item,'id')">{{ aiString(item,'name','id') }}</option></select></label><label class="field field-wide">本次评估主体<select v-model="requestDraft.principalId" required><option value="">选择主体</option><option v-for="item in principals" :key="aiString(item,'id')" :value="aiString(item,'id')">{{ aiString(item,'name','id') }}</option></select><small>此处选择身份用于策略验证；真实执行须通过可信凭证绑定主体。</small></label><label v-if="requestDraft.kind==='rag' || requestDraft.kind==='model'" class="field field-wide">请求引用的知识资料<select v-model="requestDraft.documentIds" multiple size="5"><option v-for="item in documents" :key="aiString(item,'id')" :value="aiString(item,'id')">{{ aiString(item,'name','id') }} · {{ aiString(item,'version') }}</option></select><small>可以选择范围外资料验证拒绝行为；空列表不代表已取得知识来源。</small></label><template v-if="requestDraft.kind==='model'"><label class="field field-wide">模型目标<select v-model="requestDraft.modelId" required><option value="">选择固定模型目标</option><option v-for="item in models" :key="aiString(item,'id')" :value="aiString(item,'id')">{{ aiString(item,'name','id') }} · {{ aiString(item,'region') }}</option></select></label><label class="field field-wide">合成模型输入<textarea v-model="requestDraft.input" rows="5" maxlength="64000" :required="!requestDraft.output"></textarea><small>使用合成内容验证请求侧策略，不粘贴生产秘密或真实个人信息。</small></label><label class="field field-wide">合成模型输出<textarea v-model="requestDraft.output" rows="4" maxlength="64000" :required="!requestDraft.input"></textarea><small>此处为输出策略评估材料；客户侧执行服务会检查真实模型返回，不采用调用者自报输出。</small></label></template><template v-if="requestDraft.kind==='tool'"><label class="field field-wide">工具<select v-model="requestDraft.toolId" required><option value="">选择固定工具</option><option v-for="item in tools" :key="aiString(item,'id')" :value="aiString(item,'id')">{{ aiString(item,'name','id') }}</option></select></label><label class="field">请求动作<input v-model.trim="requestDraft.action" required maxlength="200"></label><label class="field">请求目标<input v-model="requestDraft.target" required maxlength="2048"></label><label class="field field-wide">限定参数 JSON<textarea v-model="requestDraft.argumentsJSON" rows="4" spellcheck="false"></textarea><small v-if="savedTool?.protocol==='mcp_http'">已保存配置采用 MCP HTTP 2026-07-28：动作映射为 tools/call 的 name，目标注入 {{ savedTool.targetArgument }}。此处只填其余允许的字符串参数。</small><small v-else>普通 JSON 工具采用固定端点；参数值只支持字符串，仍需满足已保存的允许列表。</small></label><label class="field">已有批准引用<input v-model.trim="requestDraft.approvalId" maxlength="200"></label></template><label class="field field-wide">可信任务标识（可选）<input v-model.trim="requestDraft.taskId" maxlength="80" spellcheck="false"><small>控制台评估不解析或占用真实任务；缺少可信任务上下文会拒绝，不能作为执行授权。实际调用通过 taskId 或 X-Scarlet-Task-Id 绑定任务，由执行点核对用途与范围。<a :href="home+'/ai-gateway?tab=tasks'">查看任务与委托</a></small></label><label class="field field-wide">请求唯一引用<input v-model.trim="requestDraft.requestId" required minlength="16" maxlength="80"><small>发起 {{ formatDate(requestDraft.issuedAt) }} · 到期 {{ formatDate(requestDraft.expiresAt) }}。刷新引用会清除已有批准引用。</small></label></fieldset><div class="ai-security-actions"><button class="button button-primary" :disabled="!canWrite || !!busy">{{ busy==='evaluate'?'正在评估…':'评估当前请求' }}</button><button class="button button-secondary" type="button" :disabled="!canWrite || !!busy" @click="renewRequest">刷新请求引用及有效期</button><button class="button button-secondary" type="button" :disabled="!canWrite || !!busy" @click="prepareWireTemplate">生成客户侧请求模板</button><button class="button button-secondary" type="button" @click="tab='evaluations'">查看引擎回归与发布</button><button v-if="requestDraft.kind==='tool' && savedToolHighImpact" class="button button-secondary" type="button" :disabled="!canWrite || !!busy" @click="requestApproval">申请限定动作批准</button></div></form><div v-if="wireTemplate" class="ai-security-body"><h3>客户侧执行请求模板</h3><p class="table-secondary">提交到客户侧 /v1/execute。身份通过调用凭证绑定，不发送控制台主体字段或合成输出。模型输入使用受支持的 JSON 对象，真实模型／部署选择须与固定映射一致；工具协议与可信目标参数由客户侧采用的配置决定，调用者不传协议或上游地址；此模板未实际执行。</p><pre class="ai-security-code">{{ wireTemplate }}</pre></div><div v-if="result" class="ai-security-result"><h3>最近一次已保存评估决策：{{ aiDecisionLabel(result) }}</h3><p>{{ aiReason(result) }}</p><AISecurityEvidence :record="result"/></div></section><aside class="panel ai-security-assurance"><h2>三条控制链</h2><ol><li><strong>检索前授权：</strong>核对真实主体与文档权限，未授权资料不进入模型上下文。</li><li><strong>模型外发前检查：</strong>核对实际数据范围、固定模型目标和采用策略。</li><li><strong>工具执行前授权：</strong>核对工具、动作、目标、参数及有效批准。</li></ol><p>网络通断、模型回答和 Agent 自报结果，都不能替代实际执行证据。</p><a :href="home+'/security-platform'">查看来源覆盖与缺口</a></aside></div>
      </template>
      <section v-else-if="tab==='approvals'" class="panel"><div class="ai-security-heading"><div><h2 id="ai-approvals-title" tabindex="-1">高影响工具动作的独立批准</h2><p>申请与批准分别留痕。批准绑定请求及版本；参数变化、过期或已消费的批准不能继续采用。</p></div><button class="button button-secondary" @click="tab='evaluate';requestDraft.kind='tool'">准备工具请求</button></div><AISecurityHistory ref="approvalHistory" :base="base" collection="approvals" @session-expired="emit('sessionExpired')"><template #default="{items:approvals}"><article v-for="item in approvals" :key="aiString(item,'id')" class="ai-security-body"><h3 :id="'ai-approval-record-'+aiString(item,'id')">批准 #{{ aiString(item,'id') }} <span class="badge">{{ approvalExpired(item)?'已到期':aiDecisionLabel(item) }}</span></h3><dl class="ai-security-facts"><div><dt>提交时间</dt><dd>{{ timestamp(item) }}</dd></div><div><dt>配置修订</dt><dd>{{ aiString(item,'configurationRevision','revision') || '未记录' }}</dd></div><div><dt>绑定请求</dt><dd>{{ aiString(item,'requestDigest','digest') }}</dd></div><div><dt>有效期</dt><dd><time v-if="aiString(item,'expiresAt')" :datetime="aiString(item,'expiresAt')" :title="aiString(item,'expiresAt')">{{ formatDate(aiString(item,'expiresAt'),true) }}</time><span v-else>核对请求范围</span></dd></div></dl><dl class="ai-security-facts"><div><dt>请求应用与主体</dt><dd>{{ aiString(item,'applicationId') }} / {{ aiString(item,'principalId') }}</dd></div><div><dt>绑定任务</dt><dd><a v-if="item.taskId" :href="home+'/ai-gateway?tab=tasks&task='+encodeURIComponent(aiString(item,'taskId'))">{{ aiString(item,'taskId') }}</a><span v-else>未绑定任务</span><span class="table-secondary">批准还需与任务的当前用途、来源及剩余执行范围一致。</span></dd></div><div><dt>工具与动作</dt><dd>{{ aiString(item,'toolId') }} / {{ aiString(item,'action') }}</dd></div><div><dt>目标</dt><dd>{{ aiString(item,'target') }}</dd></div><div><dt>限定参数</dt><dd>{{ aiText(item.arguments) }}</dd></div></dl><div v-if="item.requestId && item.issuedAt" class="ai-security-actions"><button class="button button-secondary" type="button" @click="restoreApprovalRequest(item)">载入此绑定请求进行验证</button></div><details class="ai-security-evidence-json"><summary>查看完整批准记录</summary><pre class="ai-security-code">{{ JSON.stringify(item,null,2) }}</pre></details><form v-if="canReview && !approvalExpired(item) && Number(item.configurationRevision)===bundle.revision && ['proposed','pending','pending_review'].includes(aiString(item,'status'))" :aria-labelledby="'ai-approval-record-'+aiString(item,'id')" @submit.prevent="review(item,'approved')"><label class="field">独立批准意见<span class="visually-hidden">，批准 #{{ aiString(item,'id') }}</span><textarea v-model.trim="reviewNotes[aiString(item,'id')]" rows="2" required maxlength="1000"></textarea></label><div class="ai-security-actions"><button class="button button-primary" :disabled="!!busy">批准限定动作</button><button class="button button-secondary" type="button" :disabled="!!busy || !reviewNotes[aiString(item,'id')]?.trim()" @click="review(item,'rejected')">拒绝申请</button></div></form></article></template></AISecurityHistory></section>
      <AISecurityRegression v-else-if="tab==='evaluations'" :base="base" :bundle="bundle" :can-write="canWrite" :can-review="canReview" :draft-changed="dirty || configurationOutdated" @refresh="load(false)" @session-expired="emit('sessionExpired')" />
      <section v-else-if="tab==='evidence'" class="panel"><div class="ai-security-heading"><div><h2>决策、来源与版本证据</h2><p>保留实际采用配置与决策理由，区分控制台评估和客户侧真实执行。</p></div><button class="button button-secondary" :disabled="!!busy" @click="download('export')">导出有界配置概览</button></div><AISecurityHistory :base="base" collection="decisions" @session-expired="emit('sessionExpired')"><template #default="{items}"><div v-if="items.length" class="table-scroll" role="region" aria-label="AI 安全决策记录" tabindex="0"><table class="data-table"><thead><tr><th scope="col">时间与记录</th><th scope="col">采用范围</th><th scope="col">决策与原因</th><th scope="col">证据</th></tr></thead><tbody><tr v-for="item in items" :key="aiString(item,'id')"><th scope="row">{{ timestamp(item) }}<span class="table-secondary">#{{ aiString(item,'id') }} · {{ aiString(item,'mode') || '调用模式未记录' }}</span></th><td>{{ aiString(aiDecision(item),'applicationId') || aiString(item,'applicationId') }}<span class="table-secondary">{{ aiString(aiDecision(item),'principalId') || aiString(item,'principalId') }} · {{ kindLabels[aiString(aiDecision(item),'kind')] || aiString(aiDecision(item),'kind') }}</span></td><td><strong>{{ aiDecisionLabel(item) }}</strong><span class="table-secondary">{{ aiReason(item) }}</span></td><td><button class="button button-secondary" @click="selectedEvidence=item">展开决策依据</button></td></tr></tbody></table></div></template></AISecurityHistory><div v-if="selectedEvidence" class="ai-security-body"><AISecurityEvidence :record="selectedEvidence"/></div>
        <AISecurityRuntimeChains v-if="runtimeEvidence" :requests="runtimeEvidence.requests || []" :limit="runtimeEvidence.limit" :historical="!!runtimeBefore" />
        <div class="ai-security-heading"><div><h2>客户侧报告的实际执行回执</h2><p>通过独立来源凭证接收，按应用、目录摘要和请求摘要定位。来源报告与独立效果复核分别核对。</p></div><button class="button button-secondary" :disabled="runtimeLoading" @click="loadRuntimeEvidence()">刷新实际回执</button></div>
        <form class="ai-security-filter" :aria-busy="runtimeLoading" @submit.prevent="applyRuntimeFilters">
          <label class="field">来源编号<input v-model.trim="runtimeFilters.sourceId" type="text" inputmode="numeric" pattern="[0-9]+" maxlength="20" placeholder="全部来源" :disabled="runtimeLoading"></label>
          <label class="field">回执类型<select v-model="runtimeFilters.kind" :disabled="runtimeLoading"><option value="">全部类型</option><option v-for="(label,key) in kindLabels" :key="key" :value="key">{{ label }}</option><option value="output">模型输出决策</option></select></label>
          <label class="field">决策结果<select v-model="runtimeFilters.outcome" :disabled="runtimeLoading"><option value="">全部决策</option><option value="allow">允许</option><option value="deny">拒绝</option><option value="approval_required">需要批准</option><option value="unknown">未知</option></select></label>
          <button class="button button-primary" :disabled="runtimeLoading">查询回执</button><button class="button button-secondary" type="button" :disabled="runtimeLoading" @click="resetRuntimeFilters">重置筛选</button>
        </form>
        <p v-if="runtimeError" class="message message-error" role="alert">{{ runtimeError }}{{ runtimeEvidence?' 下方保留上次成功载入的范围，本次查询未更新。':'' }}</p>
        <p v-if="runtimeLoading" class="ai-security-body" role="status">正在读取客户侧运行回执…</p>
        <div v-if="runtimeEvidence?.events.length" class="table-scroll ai-runtime-events" role="region" aria-label="客户侧真实运行回执" tabindex="0"><table class="data-table"><thead><tr><th scope="col">发生与接收时间</th><th scope="col">来源与应用</th><th scope="col">决策类型</th><th scope="col">来源报告执行阶段</th><th scope="col">绑定证据</th></tr></thead><tbody><tr v-for="item in runtimeEvidence.events" :key="item.id"><th scope="row">{{ formatDate(item.occurredAt) }}<span class="table-secondary">接收 {{ formatDate(item.receivedAt) }}</span></th><td>来源 #{{ item.sourceId }} · {{ item.applicationId }}<span class="table-secondary">事件 {{ item.eventId }}</span></td><td>{{ item.kind==='output'?'模型输出决策':kindLabels[item.kind] || item.kind }}<span class="table-secondary">{{ aiDecisionLabel({outcome:item.outcome}) }} · {{ runtimeStageLabels[item.stage] || item.stage }}</span></td><td>{{ item.stage==='prepared'?'执行前准备，不能据此认定成功':executionLabel(item.executionStatus) }}<span class="table-secondary">{{ item.current?'当前发布目录的来源记录':'历史来源或历史目录记录' }}</span></td><td><span class="table-secondary mono">目录 {{ item.catalogDigest }}</span><span class="table-secondary mono">请求 {{ item.requestDigest }}</span></td></tr></tbody></table></div>
        <p v-else-if="!runtimeLoading && !runtimeError" class="empty-state">当前查询范围没有执行回执。可重置筛选或核对独立来源；控制台策略评估不会自动生成真实运行记录。</p>
        <div v-if="runtimeEvidence" class="ai-security-pagination"><button class="button button-secondary" :disabled="runtimeLoading || !runtimeHistory.length" @click="previousRuntimePage">较新一页</button><button class="button button-secondary" :disabled="runtimeLoading || !runtimeEvidence.hasMore || !runtimeEvidence.nextCursor" @click="loadRuntimeEvidence(runtimeEvidence.nextCursor,true)">较早一页</button><p>第 {{ runtimeHistory.length+1 }} 页 · 本页 {{ runtimeEvidence.events.length }} 条 · {{ runtimeEvidence.hasMore?'仍有较早回执':'当前查询已到最早记录' }}。执行链只关联本页回执，跨页缺失不等于阶段从未发生。</p></div><p v-if="runtimeEvidence" class="ai-security-body table-secondary">拒绝发生在上游调用前还是输出返回前，应按报告阶段核对；已应用来源 {{ appliedRuntimeFilters.sourceId || '全部' }}、类型 {{ appliedRuntimeFilters.kind || '全部' }}、决策 {{ appliedRuntimeFilters.outcome || '全部' }}。</p><ul v-if="runtimeEvidence?.coverage?.length" class="ai-security-body table-secondary"><li v-for="item in runtimeEvidence.coverage" :key="item">{{ item }}</li></ul>
      </section>
      <div v-else-if="tab==='deploy'" class="ai-security-review"><section class="panel"><div class="ai-security-heading"><div><h2>部署客户侧安全执行点</h2><p>控制台配置和评估与客户侧真实执行分别核对。凭证由部署人员在客户环境独立配置。</p></div></div><ol class="ai-security-flow"><li>确认应用与身份</li><li>授权知识与固定上游</li><li>采用配置版本</li><li>实际允许／拒绝验收</li></ol><div class="ai-security-body"><div class="ai-security-note"><strong>客户侧执行边界</strong><details class="ai-security-manual" open><summary>执行点与身份边界</summary><p>RAG 检索、模型调用和工具动作需要经过对应受控入口。未接入的调用路径、未知格式和未确认来源，应继续保留覆盖缺口。</p></details><details class="ai-security-manual"><summary>模型协议、流式与响应校验</summary><p>应用调用凭证、平台回执上报令牌和上游模型密钥分别配置。客户侧接口拒绝调用者自报的主体、权限、输出及上游地址。模型转发采用固定真实模型／部署映射和受支持的有界 JSON 协议。stream:true 采用 strict_hold：完整接收、检测并重新核对授权后回放 SSE，不承诺首令牌低延迟；未完成、被阻断或授权失效时不释放内容或 DONE。模型正文采用 model、prompt/input/messages 三选一、stream、max_tokens（0—4096）及 temperature（0—2），消息角色仅支持 system/user/assistant。严格 openai_chat 核对实际响应模型、内容与工具边界；默认 JSON 不代表任意响应协议已经验证。</p></details><details class="ai-security-manual"><summary>Agent / MCP 工具协议</summary><p>工具可选择普通 HTTPS JSON，或固定为 MCP HTTP 2026-07-28 的无会话 tools/call 适配。MCP 工具名采用已授权动作，可信目标参数采用已授权请求目标，其余参数仅支持已允许的字符串值。MCP 可显式固定 2026-07-28 协议版本；设置非空定义摘要时，必须覆盖全部允许动作，实际执行前通过单页 tools/list 核对真实工具定义，摘要不符即拒绝。未设置定义摘要时，保留未固定定义的覆盖缺口。MCP 支持有界 JSON 与 SSE 汇聚，只消费匹配请求标识的最终 RPC 响应，通知不作为执行成功，最终内容继续核对输出策略；不提供 SSE 透传、自动重连、自动重试、多轮请求交互（MRTR）、旧版 initialize/session 或任意 MCP 方法。模型流式能力与 MCP 协议分别验收；实际 MCP 服务需单独验证兼容性。</p></details><details class="ai-security-manual"><summary>多模态输入与扫描</summary><p>多模态仅支持模型 media 配置明确允许的 PNG/JPEG 与 PCM16、单声道 16 kHz、最长 30 秒 WAV。图片采用内联 Base64 data URI，音频采用 input_audio wav，不抓取远程媒体 URL。实际 OCR/ASR 扫描成功、摘要绑定与类别策略全部通过后才外发；输出媒体同样检查，流式媒体输出不在当前范围。image、audio 与必要时的 unclassified_media 类别须在模型和共同策略中分别批准。配置和节点声明不证明扫描器已就绪，真实请求失败时拒绝。</p></details><details class="ai-security-manual"><summary>协议版本与授权租约</summary><p>可要求 scarlet-ai-runtime-v2 运行时协议和 datasafety-four-format-v1 检测器版本，实际运行须采用符合要求的执行点。主体与资料的授权租约一旦到期立即拒绝；启用强制租约后，未声明也拒绝。租约由可信管理员维护，不能据此宣称外部身份系统已自动同步撤权。</p></details><details class="ai-security-manual"><summary>模板、可信配置与部署验收</summary><p>请求模板不携带运行时版本声明、授权租约、模型响应格式或工具定义摘要，这些限制来自已采用的可信配置。调用身份仍由客户侧凭证绑定，协议与上游目标不由调用参数选择；配置发布后仍需部署并验收真实允许、拒绝与输出阻断。</p></details><details class="ai-security-manual"><summary>知识来源与数据处理约定</summary><p>知识资料可以采用固定、经认证的 HTTPS 来源连接器，按实际身份与资料范围重新核对源 ACL、短期授权及实际正文摘要；与本地文档快照二选一。正文保留在客户侧来源，调用方不能指定来源地址或自报 ACL。资料用途及留存/训练限制由可信配置与任务用途共同约束，目的端处理约定有期限，声明不独立证明远端行为。</p></details><details class="ai-security-manual"><summary>任务与委托范围</summary><p>任务绑定采用 taskId 或 X-Scarlet-Task-Id。有效任务仍需与应用凭证匹配，并持续核对步数、预算、精确工具动作/目标、来源标签及全部祖先状态；停止任务不会回滚已发生的副作用。</p></details><details class="ai-security-manual"><summary>地域与供应商依据</summary><p>配置中的地域和供应商信息需保留确认依据；网络出口与 IP 信息不单独证明模型后台的数据处理位置。</p></details></div><div class="ai-security-actions"><button class="button button-primary" :disabled="!!busy || !bundle.publishedRevision" @click="download('runtime-config')">下载已发布修订 {{ bundle.publishedRevision }} 的模板</button><button class="button button-secondary" :disabled="!!busy" @click="download('export')">导出当前配置与证据</button></div><h3>接入后需要验证</h3><ul><li>可信凭证与实际主体绑定，不能通过请求字段切换成其他用户。</li><li>未授权知识不会进入模型上下文，被拒绝内容没有发送至上游。资料配置 SHA-256 固定摘要后，需验证版本未变而正文被替换时也会拒绝；未配置摘要时仅核对来源版本与 ACL。</li><li>工具动作遵守明确权限，批准后变更参数、重放或过期请求不能绕过限制。</li><li>审计、网络或上游异常时，执行行为和恢复过程符合约定。</li></ul></div></section><aside class="panel ai-security-assurance"><h2>当前覆盖记录</h2><p>以下内容来自服务端采用范围，缺少运行证据时应继续按待接入或待验证处理。</p><pre class="ai-security-code">{{ JSON.stringify(bundle.coverage ?? {},null,2) }}</pre><div class="ai-security-links"><a :href="home+'/discovery'">数据来源</a><a :href="home+'/operations'">实际安全事件</a><a :href="home+'/security-platform'">能力覆盖</a></div></aside>
        <AISecurityMediaRollout :models="models" :events="runtimeEvidence?.events || []" :filters="appliedRuntimeFilters" :loading="runtimeLoading" :error="runtimeError" :read-at="runtimeReadAt" @configuration="tab='configuration';section='models'" @evidence="tab='evidence'" @refresh="loadRuntimeEvidence()" />
        <section class="panel ai-security-runtime-sources"><div class="ai-security-heading"><div><h2>独立运行来源与凭证</h2><p>每个来源绑定已发布应用和目录摘要。创建、轮换与撤销仅限负责人；新令牌只在对应响应中返回一次，轮换后旧令牌立即失效。</p></div><button class="button button-secondary" :disabled="runtimeLoading" @click="loadRuntimeEvidence()">刷新来源</button></div>
          <p v-if="runtimeError" class="message message-error" role="alert">{{ runtimeError }}{{ runtimeEvidence?' 下方保留上次读取的来源，本次刷新未更新。':'' }}</p>
          <form v-if="canManageSources" class="ai-security-body" @submit.prevent="createRuntimeSource"><fieldset class="ai-security-fields" :disabled="!!busy || !!issuedToken || !bundle.publishedDigest"><label class="field">来源名称<input v-model.trim="sourceDraft.name" required maxlength="160"></label><label class="field">已发布应用标识<input v-model.trim="sourceDraft.applicationId" list="ai-runtime-applications" required maxlength="160"><datalist id="ai-runtime-applications"><option v-for="item in applications" :key="aiString(item,'id')" :value="aiString(item,'id')">{{ aiString(item,'name','id') }}</option></datalist><small>服务端按已发布版本校验应用，当前未发布草稿不作为运行授权。</small></label></fieldset><p class="table-secondary mono">绑定发布摘要：{{ bundle.publishedDigest || '尚未取得已发布目录摘要' }}</p><div class="ai-security-actions"><button class="button button-primary" :disabled="!!busy || !!issuedToken || !bundle.publishedRevision || !bundle.publishedDigest">{{ busy==='source'?'正在创建…':'创建独立运行来源' }}</button><p v-if="!bundle.publishedDigest">先完成独立发布，再创建绑定来源。</p></div></form>
          <div v-if="issuedToken" class="ai-security-body"><div class="ai-security-note"><strong>来源 #{{ issuedSource }} 的一次性令牌显示</strong><p>仅用于向平台上报回执，与应用调用凭证和模型供应商密钥分别配置。本页将在 60 秒后清除显示，切换页签也会清除；这不表示凭证自动过期。</p><label class="field">仅本次返回的回执上报令牌<input :type="showToken?'text':'password'" :value="issuedToken" readonly autocomplete="new-password" spellcheck="false"></label><div class="ai-security-actions"><button class="button button-secondary" @click="copyToken">复制令牌</button><button class="button button-secondary" @click="showToken=!showToken">{{ showToken?'遮挡令牌':'临时显示' }}</button><button class="button button-secondary" @click="clearToken">已配置，清除此页令牌</button></div></div></div>
          <div v-if="runtimeEvidence?.sources.length" class="table-scroll" role="region" aria-label="客户侧运行来源" tabindex="0"><table class="data-table"><thead><tr><th scope="col">来源与应用</th><th scope="col">状态与最近回执</th><th scope="col">采用目录摘要</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="item in runtimeEvidence.sources" :key="item.id"><th scope="row">{{ item.name }}<span class="table-secondary">#{{ item.id }} · {{ item.applicationId }}</span></th><td>{{ item.revokedAt?'已撤销':!item.current?'历史目录或来源授权已变化':item.lastSeen?'当前绑定，已有来源回执':'当前绑定，等待首次回执' }}<span class="table-secondary">{{ item.receiptCount }} 条 · {{ item.lastSeen?formatDate(item.lastSeen):'暂无观察时点' }}</span><span class="table-secondary">凭证版本 {{ item.tokenVersion || 1 }}</span></td><td class="mono">{{ item.catalogDigest }}</td><td><div v-if="canManageSources && !item.revokedAt" class="page-actions"><button class="button button-secondary" :disabled="!!busy || !!issuedToken" @click="rotateRuntimeSource(item)">{{ busy==='rotate-source-'+item.id?'正在轮换…':'轮换令牌（旧令牌失效）' }}</button><button class="button button-secondary" :disabled="!!busy" @click="revokeRuntimeSource(item)">撤销来源凭证</button></div><span v-else>{{ item.revokedAt?'历史记录保留':'只读' }}</span></td></tr></tbody></table></div><p v-else-if="!runtimeLoading && !runtimeError" class="empty-state">尚未创建独立运行来源。</p>
          <p class="ai-security-body table-secondary">当前概览最多列示 100 个来源，不表示完整历史来源清单。撤销本平台来源凭证，不表示客户侧执行服务已经停止或配置已经恢复；实际运行状态需要另行核对。</p>
        </section>
</div>
    </template>
    <AISecurityApprovalReview :base="base" :intent="reviewIntent" :trigger="reviewTrigger" @close="closeReview" @reviewed="reviewed" @session-expired="emit('sessionExpired')" />
  </section>
</template>
