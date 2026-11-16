<script setup lang="ts">
import { containDialogTab } from './dialog-keyboard'
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { ApiError, downloadFile, formatDate, request, workspacePath, type Workspace } from './api'
import { gatewayCredentialActive, gatewayCredentialQuery, gatewayDollars, gatewayRevocationLabel, type GatewayCredential, type GatewayCredentialPage } from './ai-gateway'
import { bindingProposalLabels, bindingProposalPayload, bindingReviewPayload, leaseFailure, machineRenewalExample, taskLeaseProtocol, type BindingProposal, type BindingProposalInput, type LeaseChange, type LeaseIssue, type LeasePage, type TaskLeaseNode, type TaskLeaseView } from './ai-gateway-task-leases'
import Icon from './Icon.vue'

const props=defineProps<{workspace:Workspace;taskId:string}>()
const emit=defineEmits<{sessionExpired:[];selectTask:[id:string];changed:[]}>()
const root=workspacePath(props.workspace.slug)+'/ai-gateway'
const base=computed(()=>root+'/tasks/'+encodeURIComponent(props.taskId))
const lifetime=new AbortController()
const canWrite=computed(()=>['owner','operator'].includes(props.workspace.role))
const lease=ref<TaskLeaseView>(),loading=ref(false),readError=ref(''),authorizationError=ref('')
interface AuthorizationStatus {credentialChainStatus:'current'|'unknown'|'blocked';authorization:{gatewayEnabled:boolean;catalogStatus:string;gatewayApplicationStatus:string;principalStatus:string;principalAuthorizationExpiresAt?:string};blockers:LeaseIssue[]}
const authorization=ref<AuthorizationStatus>()
const tab=ref<'current'|'requests'|'history'|'machine'>('current')
const busy=ref(''),feedback=ref(''),actionFailure=ref<ReturnType<typeof leaseFailure>>()
const proposals=ref<LeasePage<BindingProposal>>(),proposalsBefore=ref(''),proposalsTrail=ref<string[]>([]),proposalsLoading=ref(false),proposalsError=ref('')
const history=ref<LeasePage<LeaseChange>>(),historyBefore=ref(''),historyTrail=ref<string[]>([]),historyLoading=ref(false),historyError=ref('')
const proposalDialog=ref<HTMLDialogElement>(),proposalPlan=ref<TaskLeaseView>(),proposalReason=ref(''),proposalExpiry=ref(''),operationId=ref(''),pendingPayload=ref<BindingProposalInput>(),proposalUncertain=ref(false),proposalFailure=ref<ReturnType<typeof leaseFailure>>()
const targetIds=reactive<Record<string,number>>(Object.create(null)),selectedTargets=reactive<Record<number,GatewayCredential>>(Object.create(null))
const targetsPage=ref<GatewayCredentialPage>(),targetsBefore=ref(''),targetsTrail=ref<string[]>([]),targetsLoading=ref(false),targetsError=ref('')
const reviewDialog=ref<HTMLDialogElement>(),reviewPlan=ref<BindingProposal>(),reviewNote=ref(''),reviewDecision=ref<''|'approve'|'reject'>(''),reviewFailure=ref<ReturnType<typeof leaseFailure>>(),reviewUncertain=ref(false)
let generation=0,proposalsGeneration=0,historyGeneration=0,targetsGeneration=0
const canResumeProposal=computed(()=>proposalUncertain.value && !!pendingPayload.value && proposalPlan.value?.taskId===props.taskId)
const hasTargetChange=computed(()=>Object.values(targetIds).some(value=>Number.isSafeInteger(value) && value>0))
const canPropose=computed(()=>canWrite.value && !!lease.value?.canPropose && lease.value.leaseProtocolVersion===taskLeaseProtocol && !loading.value && !readError.value && !busy.value)
const allTargets=computed(()=>[...new Map([...(targetsPage.value?.items || []),...Object.values(selectedTargets)].map(item=>[item.id,item])).values()])
const machineExample=computed(()=>{try{return lease.value?machineRenewalExample(props.workspace.slug,lease.value,window.location.origin):''}catch{return ''}})
const modeLabels:Record<string,string>={initial:'初始绑定',renewal:'同身份续期',rebind:'独立复核换绑'}
const bindingLabels:Record<string,string>={current:'当前信任一致',disabled:'绑定已停用',changed:'信任修订已变化',unknown:'信任待核对',not_applicable:'静态应用凭据'}
const authorizationLabels:Record<string,string>={current:'当前一致',changed:'采用版本已变化',blocked:'存在阻断',unknown:'待核对'}
const chainLabels:Record<string,string>={current:'所列门槛未发现阻断',blocked:'发现授权阻断条件',unknown:'部分事实待核对'}
function fail(cause:unknown){if(cause instanceof ApiError && cause.status===401)emit('sessionExpired');return leaseFailure(cause)}
function issueContext(issue:LeaseIssue){return [issue.taskId?'任务 '+issue.taskId:'',issue.stepId?'步骤 '+issue.stepId:'',issue.reservationId?'预留 '+issue.reservationId:'',issue.reservedMicros!==undefined?'保留报价 USD '+gatewayDollars(issue.reservedMicros):''].filter(Boolean).join(' · ')}
function credentialState(item:GatewayCredential){if(item.revokedAt)return gatewayRevocationLabel(item.revocationReason);if(item.expiresAt && Date.parse(item.expiresAt)<=Date.now())return '已到期';return item.enabled?'读取时有效':'已停用，原因需核对'}
function proposalState(item:BindingProposal){return item.status==='proposed' && Date.parse(item.expiresAt)<=Date.now()?'已过期，不可应用':bindingProposalLabels[item.status] || item.status}
async function load(){
  const sequence=++generation,path=base.value
  loading.value=true;readError.value='';authorizationError.value=''
  const results=await Promise.allSettled([request<TaskLeaseView>(path+'/credential-lease',{signal:lifetime.signal}),request<AuthorizationStatus>(path+'/credential-status',{signal:lifetime.signal})])
  if(lifetime.signal.aborted || sequence!==generation)return
  const current=results[0],status=results[1]
  if(current.status==='fulfilled')lease.value=current.value
  else readError.value=fail(current.reason).message
  if(status.status==='fulfilled')authorization.value=status.value
  else authorizationError.value=fail(status.reason).message
  loading.value=false
}
async function loadProposals(before='',remember=false){
  if(proposalsLoading.value)return
  const sequence=++proposalsGeneration,path=base.value
  proposalsLoading.value=true;proposalsError.value=''
  try{const result=await request<LeasePage<BindingProposal>>(path+'/credential-bindings?limit=50'+(before?'&before='+encodeURIComponent(before):''),{signal:lifetime.signal});if(lifetime.signal.aborted || sequence!==proposalsGeneration)return;if(remember)proposalsTrail.value.push(proposalsBefore.value);else if(!before)proposalsTrail.value=[];proposals.value={...result,items:result.items || []};proposalsBefore.value=before;return true}
  catch(cause){if(!lifetime.signal.aborted && sequence===proposalsGeneration)proposalsError.value=fail(cause).message}
  finally{if(sequence===proposalsGeneration)proposalsLoading.value=false}
}
async function previousProposals(){const before=proposalsTrail.value.at(-1);if(before!==undefined && await loadProposals(before))proposalsTrail.value.pop()}
async function loadHistory(before='',remember=false){
  if(historyLoading.value)return
  const sequence=++historyGeneration,path=base.value
  historyLoading.value=true;historyError.value=''
  try{const result=await request<LeasePage<LeaseChange>>(path+'/credential-lease/history?limit=50'+(before?'&before='+encodeURIComponent(before):''),{signal:lifetime.signal});if(lifetime.signal.aborted || sequence!==historyGeneration)return;if(remember)historyTrail.value.push(historyBefore.value);else if(!before)historyTrail.value=[];history.value={...result,items:result.items || []};historyBefore.value=before;return true}
  catch(cause){if(!lifetime.signal.aborted && sequence===historyGeneration)historyError.value=fail(cause).message}
  finally{if(sequence===historyGeneration)historyLoading.value=false}
}
async function previousHistory(){const before=historyTrail.value.at(-1);if(before!==undefined && await loadHistory(before))historyTrail.value.pop()}
async function loadTargets(before='',remember=false){
  const app=proposalPlan.value?.chain[0]?.applicationId
  if(!app || targetsLoading.value)return
  const sequence=++targetsGeneration
  targetsLoading.value=true;targetsError.value=''
  try{const query=new URLSearchParams(gatewayCredentialQuery({state:'active',applicationId:app},before));query.set('limit','50');const result=await request<GatewayCredentialPage>(root+'/credentials?'+query,{signal:lifetime.signal});if(lifetime.signal.aborted || sequence!==targetsGeneration)return;if(remember)targetsTrail.value.push(targetsBefore.value);else if(!before)targetsTrail.value=[];targetsPage.value=result;targetsBefore.value=before;return true}
  catch(cause){if(!lifetime.signal.aborted && sequence===targetsGeneration)targetsError.value=fail(cause).message}
  finally{if(sequence===targetsGeneration)targetsLoading.value=false}
}
async function previousTargets(){const before=targetsTrail.value.at(-1);if(before!==undefined && await loadTargets(before))targetsTrail.value.pop()}
function targetOptions(node:TaskLeaseNode){return allTargets.value.filter(item=>item.applicationId===node.applicationId && item.id!==node.lease.credentialId && (item.id===targetIds[node.taskId] || gatewayCredentialActive(item) && (!node.credential.workloadBindingId || !!item.workloadBindingId)))}
function selectTarget(taskId:string){const id=targetIds[taskId],item=allTargets.value.find(value=>value.id===id);if(item)selectedTargets[id]=item;const used=new Set(Object.values(targetIds));for(const key of Object.keys(selectedTargets))if(!used.has(Number(key)))delete selectedTargets[Number(key)]}
async function openProposal(){
  if(canResumeProposal.value){await nextTick();proposalDialog.value?.showModal();return}
  if(!canPropose.value || !lease.value)return
  proposalPlan.value=lease.value;operationId.value='rebind-'+crypto.randomUUID();pendingPayload.value=undefined;proposalUncertain.value=false;proposalFailure.value=undefined
  const pathIds=new Set(lease.value.chain.map(node=>node.taskId))
  for(const key of Object.keys(targetIds))if(!pathIds.has(key))delete targetIds[key]
  for(const node of lease.value.chain)if(!Number.isSafeInteger(targetIds[node.taskId]) || targetIds[node.taskId]===node.lease.credentialId)targetIds[node.taskId]=0
  const used=new Set(Object.values(targetIds));for(const key of Object.keys(selectedTargets))if(!used.has(Number(key)))delete selectedTargets[Number(key)]
  targetsGeneration++;targetsLoading.value=false;targetsPage.value=undefined;targetsBefore.value='';targetsTrail.value=[]
  await nextTick();proposalDialog.value?.showModal();void loadTargets()
}
function closeProposal(){if(busy.value!=='propose' && busy.value!=='recover-proposal')proposalDialog.value?.close()}
async function submittedProposal(result:BindingProposal){
  proposalDialog.value?.close();proposalReason.value='';proposalExpiry.value='';pendingPayload.value=undefined;proposalUncertain.value=false;feedback.value='申请 #'+result.id+' 已记录，状态为“'+proposalState(result)+'”。提出申请不会自动完成换绑。';tab.value='requests';await loadProposals()
}
async function createProposal(){
  if(!proposalPlan.value || proposalPlan.value.taskId!==props.taskId || busy.value)return
  const scope=props.taskId
  let payload:BindingProposalInput
  try{if(!proposalUncertain.value)for(const id of Object.values(targetIds)){if(id && !allTargets.value.some(item=>item.id===id && gatewayCredentialActive(item)))throw new Error('目标凭据 #'+id+' 已到期、停用或尚未取得，请重新读取并选择。')}payload=proposalUncertain.value && pendingPayload.value?pendingPayload.value:bindingProposalPayload(proposalPlan.value,targetIds,proposalReason.value,operationId.value,proposalExpiry.value)}catch(cause){proposalFailure.value={message:cause instanceof Error?cause.message:'请核对换绑表单。',code:'',blockers:[],recovery:[]};return}
  pendingPayload.value=payload;busy.value='propose';proposalFailure.value=undefined;feedback.value=''
  try{const result=await request<BindingProposal>(base.value+'/credential-bindings',{method:'POST',body:JSON.stringify(payload),signal:lifetime.signal});if(!lifetime.signal.aborted && scope===props.taskId)await submittedProposal(result)}
  catch(cause){if(!lifetime.signal.aborted){proposalFailure.value=fail(cause);proposalUncertain.value=!(cause instanceof ApiError) || cause.status>=500}}
  finally{busy.value=''}
}
async function recoverProposal(){
  if(!operationId.value || busy.value)return
  const scope=props.taskId
  busy.value='recover-proposal';proposalFailure.value=undefined
  try{const result=await request<BindingProposal>(base.value+'/credential-bindings/by-operation/'+encodeURIComponent(operationId.value),{signal:lifetime.signal});if(!lifetime.signal.aborted && scope===props.taskId)await submittedProposal(result)}
  catch(cause){if(!lifetime.signal.aborted)proposalFailure.value=cause instanceof ApiError && cause.status===404?{message:'尚未查到此操作。保留原标识与载荷后再核对，或按原申请幂等重试。',code:'not_found',blockers:[],recovery:[]}:fail(cause)}
  finally{busy.value=''}
}
async function openReview(item:BindingProposal){
  if(busy.value)return
  const scope=props.taskId
  busy.value='read-proposal';actionFailure.value=undefined
  try{const result=await request<BindingProposal>(base.value+'/credential-bindings/'+item.id,{signal:lifetime.signal});if(lifetime.signal.aborted || scope!==props.taskId)return;reviewPlan.value=result;reviewNote.value='';reviewDecision.value='';reviewUncertain.value=false;reviewFailure.value=undefined;await nextTick();reviewDialog.value?.showModal()}
  catch(cause){if(!lifetime.signal.aborted)actionFailure.value=fail(cause)}
  finally{busy.value=''}
}
function closeReview(){if(!['review','recover-review'].includes(busy.value))reviewDialog.value?.close()}
async function recoverReview(){
  if(!reviewPlan.value || busy.value)return
  busy.value='recover-review';reviewFailure.value=undefined
  try{reviewPlan.value=await request<BindingProposal>(base.value+'/credential-bindings/'+reviewPlan.value.id,{signal:lifetime.signal});reviewUncertain.value=false;await loadProposals(proposalsBefore.value);await load();if(reviewPlan.value.status==='applied')emit('changed')}
  catch(cause){if(!lifetime.signal.aborted)reviewFailure.value=fail(cause)}
  finally{busy.value=''}
}
async function submitReview(){
  const proposal=reviewPlan.value
  const scope=props.taskId
  if(!proposal || !reviewDecision.value || busy.value || reviewUncertain.value)return
  let payload:ReturnType<typeof bindingReviewPayload>
  try{payload=bindingReviewPayload(proposal,reviewDecision.value,reviewNote.value)}catch(cause){reviewFailure.value={message:cause instanceof Error?cause.message:'请核对复核条件。',code:'',blockers:[],recovery:[]};return}
  busy.value='review';reviewFailure.value=undefined;feedback.value=''
  try{const result=await request<BindingProposal>(base.value+'/credential-bindings/'+proposal.id+'/review',{method:'POST',body:JSON.stringify(payload),signal:lifetime.signal});if(lifetime.signal.aborted || scope!==props.taskId)return;reviewDialog.value?.close();feedback.value='申请 #'+result.id+' 已'+(result.status==='applied'?'批准并应用；原任务范围、步骤和账务保持。':'记录复核结果。');await load();await loadProposals();history.value=undefined;if(result.status==='applied')emit('changed')}
  catch(cause){if(!lifetime.signal.aborted){reviewFailure.value=fail(cause);reviewUncertain.value=!(cause instanceof ApiError) || cause.status>=500 || cause.status===409}}
  finally{busy.value=''}
}
async function exportHistory(){if(historyLoading.value || busy.value || !history.value)return;busy.value='export';try{await downloadFile(base.value+'/credential-lease/history?limit=50'+(historyBefore.value?'&before='+encodeURIComponent(historyBefore.value):''),'scarlet-task-'+props.taskId+'-credential-history-page.json',lifetime.signal)}catch(cause){if(!lifetime.signal.aborted)actionFailure.value=fail(cause)}finally{busy.value=''}}
watch(tab,value=>{if(value==='requests' && !proposals.value)void loadProposals();if(value==='history' && !history.value)void loadHistory()})
watch(()=>props.taskId,()=>{proposalsGeneration++;historyGeneration++;targetsGeneration++;proposalsLoading.value=false;historyLoading.value=false;targetsLoading.value=false;proposalDialog.value?.close();reviewDialog.value?.close();proposalPlan.value=undefined;reviewPlan.value=undefined;pendingPayload.value=undefined;proposalUncertain.value=false;lease.value=undefined;authorization.value=undefined;proposals.value=undefined;history.value=undefined;tab.value='current';void load()},{immediate:true})
onBeforeUnmount(()=>{generation++;proposalsGeneration++;historyGeneration++;lifetime.abort();proposalDialog.value?.close();reviewDialog.value?.close()})
</script>

<template>
  <section class="task-credential-status" :aria-busy="loading || !!busy" aria-label="任务凭据与祖先授权核对">
    <div class="ai-security-heading"><div><h3>任务凭据与祖先授权</h3><p>保留原授权凭据与范围，通过独立执行租约记录续期、换绑和每层当前约束。</p></div><div class="page-actions"><button type="button" class="button button-secondary" :disabled="loading || !!busy" @click="load">{{ loading?'正在核对…':'重新核对凭据链' }}</button><button v-if="canWrite" type="button" class="button button-primary" :disabled="!canPropose && !canResumeProposal" @click="openProposal">{{ canResumeProposal?'继续核对上次申请':'提出受控换绑' }}</button></div></div>
    <p v-if="feedback" class="message message-success" role="status">{{ feedback }}</p>
    <div v-if="actionFailure" class="message message-error" role="alert"><p>{{ actionFailure.message }}</p><ul v-if="actionFailure.recovery.length"><li v-for="item in actionFailure.recovery" :key="item">{{ item }}</li></ul></div>
    <p v-if="readError" class="message message-error" role="alert">{{ readError }}{{ lease?' 下方保留上次读取的租约，当前禁止提出换绑。':'' }}</p>
    <p v-if="loading && !lease" class="ai-security-body" role="status">正在读取当前任务与全部祖先的执行凭据…</p>
    <nav class="ai-security-tabs lease-tabs" aria-label="任务凭据操作"><button type="button" :aria-pressed="tab==='current'" @click="tab='current'">当前凭据链</button><button type="button" :aria-pressed="tab==='requests'" @click="tab='requests'">换绑申请与复核</button><button type="button" :aria-pressed="tab==='history'" @click="tab='history'">续期与换绑历史</button><button type="button" :aria-pressed="tab==='machine'" @click="tab='machine'">机器安全续期</button></nav>
    <template v-if="tab==='current' && lease">
      <div class="lease-summary"><span class="badge" :class="authorization?.credentialChainStatus==='blocked' || lease.blockers?.some(item=>item.state==='blocked')?'status-needs-work':'status-draft'">{{ chainLabels[authorization?.credentialChainStatus || 'unknown'] }}</span><span>读取于 {{ formatDate(lease.observedAt) }}</span><p>所列门槛未发现阻断也不代表请求已获准。模型、资料、工具、额度、独立批准和实际运行时检查仍需逐次通过。</p><ul v-if="lease.blockers?.length" class="lease-issues"><li v-for="(issue,index) in lease.blockers" :key="issue.code+':'+index">{{ issue.message }}<span v-if="issueContext(issue)" class="table-secondary">{{ issueContext(issue) }}</span></li></ul></div>
      <div v-if="lease.chain?.length" class="table-scroll" role="region" aria-label="任务及祖先执行租约" tabindex="0"><table class="data-table lease-chain-table"><thead><tr><th scope="col">任务与原授权</th><th scope="col">当前执行凭据</th><th scope="col">信任身份与修订</th><th scope="col">当前限制</th></tr></thead><tbody><tr v-for="(node,index) in lease.chain" :key="node.taskId"><th scope="row"><button v-if="node.taskId!==taskId" class="lease-task-link mono" type="button" @click="emit('selectTask',node.taskId)">{{ node.taskId }}</button><strong v-else class="mono">{{ node.taskId }}</strong><span class="table-secondary">{{ index?'第 '+index+' 层委托':'根任务' }} · 任务修订 {{ node.taskRevision }}</span><span class="table-secondary">原授权凭据 #{{ node.originalCredentialId }}</span><span class="table-secondary table-date">任务期限 {{ formatDate(node.taskExpiresAt) }}</span></th><td><strong>#{{ node.lease.credentialId }} · {{ node.credential.name || '凭据事实待核对' }}</strong><span class="table-secondary">租约修订 {{ node.lease.revision }} · {{ modeLabels[node.lease.mode] || node.lease.mode }}</span><span class="table-secondary">{{ credentialState(node.credential) }}</span><span v-if="node.credential.expiresAt" class="table-secondary">凭据到期 {{ formatDate(node.credential.expiresAt) }}</span><span v-if="node.credential.revokedAt" class="table-secondary">撤销于 {{ formatDate(node.credential.revokedAt) }}</span></td><td>{{ bindingLabels[node.bindingStatus] || node.bindingStatus }}<span class="table-secondary">应用 {{ node.applicationId }}</span><span class="table-secondary">主体 {{ node.principalId }}</span><span v-if="node.credential.workloadBindingId" class="table-secondary">绑定 #{{ node.credential.workloadBindingId }} · 签发修订 {{ node.credential.workloadBindingRevision ?? '历史未知' }} / 当前修订 {{ node.currentBindingRevision ?? '未知' }}</span><details v-if="node.identity" class="lease-evidence"><summary>当前信任绑定配置</summary><dl><dt>签发者 issuer</dt><dd>{{ node.identity.issuer }}</dd><dt>接收方 audience</dt><dd>{{ node.identity.audience }}</dd><dt>工作负载 subject</dt><dd>{{ node.identity.subject }}</dd></dl><p class="table-secondary">当前配置不能替代历史签发证明；信任未知或变化时仍按阻断事实处理。</p></details></td><td><ul v-if="node.blockers?.length" class="lease-issues"><li v-for="(issue,position) in node.blockers" :key="issue.code+':'+position">{{ issue.message }}<span v-if="issueContext(issue)" class="table-secondary">{{ issueContext(issue) }}</span></li></ul><span v-else>本层未列示阻断；后续执行继续核对完整祖先链。</span></td></tr></tbody></table></div>
      <div class="ai-security-body"><p v-if="authorizationError" class="message message-warning" role="alert">应用与主体诊断未更新：{{ authorizationError }}</p><dl v-if="authorization && !authorizationError" class="ai-security-facts"><div><dt>网关开关</dt><dd>{{ authorization.authorization.gatewayEnabled?'已启用':'已停用' }}</dd></div><div><dt>目录采用版本</dt><dd>{{ authorizationLabels[authorization.authorization.catalogStatus] || authorization.authorization.catalogStatus }}</dd></div><div><dt>应用授权</dt><dd>{{ authorizationLabels[authorization.authorization.gatewayApplicationStatus] || authorization.authorization.gatewayApplicationStatus }}</dd></div><div><dt>主体授权</dt><dd>{{ authorizationLabels[authorization.authorization.principalStatus] || authorization.authorization.principalStatus }}<span v-if="authorization.authorization.principalAuthorizationExpiresAt"> · {{ formatDate(authorization.authorization.principalAuthorizationExpiresAt) }}</span></dd></div></dl><details class="lease-evidence"><summary>完整凭据链摘要与采用边界</summary><p class="mono">{{ lease.leaseChainDigest }}</p><p>{{ lease.boundary }}</p></details><p class="table-secondary">计划轮换提交成功后，已退役旧凭据不再代表新租约。要停止当前任务及后代，请停止任务，或撤销当前执行凭据及对应信任绑定。</p></div>
    </template>
    <section v-else-if="tab==='requests'" class="lease-section">
      <div class="ai-security-heading"><div><h4>独立提出、独立复核、原子应用</h4><p>申请冻结完整祖先链和目标凭据。另一位负责人或复核人批准时重新核对，批准即应用。</p></div><button type="button" class="button button-secondary" :disabled="proposalsLoading || !!busy" @click="loadProposals()">刷新最新申请</button></div>
      <p v-if="proposalsError" class="message message-error" role="alert">{{ proposalsError }}{{ proposals?' 保留上次读取的申请页。':'' }}</p><p v-if="proposalsLoading && !proposals" class="empty-state" role="status">正在读取换绑申请…</p>
      <div v-if="proposals?.items.length" class="table-scroll" role="region" aria-label="换绑申请与独立复核" tabindex="0"><table class="data-table lease-proposals-table"><thead><tr><th scope="col">申请与状态</th><th scope="col">冻结变更</th><th scope="col">办理人员</th><th scope="col">有效期限</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="item in proposals.items" :key="item.id"><th scope="row">申请 #{{ item.id }}<span class="table-secondary">{{ proposalState(item) }}</span><span class="table-secondary mono">{{ item.operationId }}</span></th><td><ul class="lease-transitions"><li v-for="change in item.changes" :key="change.taskId"><span class="mono">{{ change.taskId }}</span><span>目标凭据 #{{ change.newCredentialId }}</span></li></ul><span class="table-secondary">{{ item.reason }}</span></td><td>提出人 #{{ item.proposedBy }}<span v-if="item.reviewedBy" class="table-secondary">复核人 #{{ item.reviewedBy }}</span><span v-if="item.note" class="table-secondary">{{ item.note }}</span></td><td><span class="table-date">{{ formatDate(item.expiresAt) }}</span><span v-if="item.appliedAt" class="table-secondary">应用于 {{ formatDate(item.appliedAt) }}</span></td><td><button type="button" class="button button-secondary" :disabled="!!busy" @click="openReview(item)">{{ item.canReview?'独立复核':'查看申请' }}</button><span v-if="item.status==='proposed' && !item.canReview" class="table-secondary">本人提出、角色不足或当前条件不满足时，不能批准。</span></td></tr></tbody></table></div>
      <p v-else-if="proposals && !proposalsLoading" class="empty-state">本页没有换绑申请。机器同身份续期记录请到“续期与换绑历史”核对。</p><div v-if="proposals" class="ai-security-pagination"><button type="button" class="button button-secondary" :disabled="proposalsLoading || !proposalsTrail.length" @click="previousProposals">较新申请</button><button type="button" class="button button-secondary" :disabled="proposalsLoading || !proposals.hasMore || !proposals.nextCursor" @click="loadProposals(proposals.nextCursor,true)">较早申请</button><p>第 {{ proposalsTrail.length+1 }} 页 · 本页 {{ proposals.items.length }} 条</p></div>
    </section>
    <section v-else-if="tab==='history'" class="lease-section">
      <div class="ai-security-heading"><div><h4>续期与换绑事实</h4><p>保留操作标识、前后凭据和租约修订。历史记录不授予当前执行权。</p></div><div class="page-actions"><button type="button" class="button button-secondary" :disabled="historyLoading || !!busy" @click="loadHistory()">刷新最新记录</button><button type="button" class="button button-secondary" :disabled="historyLoading || !!busy || !history" @click="exportHistory">导出本页</button></div></div>
      <p v-if="historyError" class="message message-error" role="alert">{{ historyError }}{{ history?' 保留上次读取的历史页。':'' }}</p><p v-if="historyLoading && !history" class="empty-state" role="status">正在读取凭据变更事实…</p>
      <div v-if="history?.items.length" class="table-scroll" role="region" aria-label="任务凭据续期与换绑历史" tabindex="0"><table class="data-table lease-history-table"><thead><tr><th scope="col">时间与操作</th><th scope="col">执行凭据变更</th><th scope="col">执行及批准依据</th><th scope="col">证据摘要</th></tr></thead><tbody><tr v-for="item in history.items" :key="item.id"><th scope="row">{{ modeLabels[item.mode] || item.mode }} · #{{ item.id }}<span class="table-secondary table-date">{{ formatDate(item.createdAt) }}</span><span class="table-secondary mono">{{ item.operationId }}</span></th><td><ul class="lease-transitions"><li v-for="change in item.changes" :key="change.taskId"><span class="mono">{{ change.taskId }}</span><span>凭据 #{{ change.oldCredentialId }} → #{{ change.newCredentialId }}</span><span class="table-secondary">租约修订 {{ change.oldLeaseRevision }} → {{ change.newLeaseRevision }}</span></li></ul></td><td><span v-if="item.actorId">操作者 #{{ item.actorId }}</span><span v-if="item.actorCredentialId">机器凭据 #{{ item.actorCredentialId }}</span><span v-if="item.proposalId" class="table-secondary">独立复核申请 #{{ item.proposalId }}</span><span class="table-secondary">{{ item.reason }}</span></td><td><details class="lease-evidence"><summary>核对变更前后链摘要</summary><dl><dt>变更前</dt><dd class="mono">{{ item.beforeDigest }}</dd><dt>变更后</dt><dd class="mono">{{ item.afterDigest }}</dd></dl></details></td></tr></tbody></table></div>
      <p v-else-if="history && !historyLoading" class="empty-state">本页没有凭据续期或换绑事实。初始授权凭据仍保留在任务快照中。</p><div v-if="history" class="ai-security-pagination"><button type="button" class="button button-secondary" :disabled="historyLoading || !historyTrail.length" @click="previousHistory">较新记录</button><button type="button" class="button button-secondary" :disabled="historyLoading || !history.hasMore || !history.nextCursor" @click="loadHistory(history.nextCursor,true)">较早记录</button><p>第 {{ historyTrail.length+1 }} 页 · 本页 {{ history.items.length }} 条 · 导出仅含当前页</p></div>
    </section>
    <section v-else-if="tab==='machine'" class="ai-security-body lease-machine"><h4>让机器证明同一身份，再延续执行租约</h4><ol><li>由相同可信工作负载签发新的短期 JWT，并兑换新的应用令牌。浏览器不收集或保存这些秘密。</li><li>持新令牌、无 Cookie 调用续期预检，采用当次返回的完整祖先 CAS 与链摘要。</li><li>用稳定操作标识提交要续期的链内任务。源凭据可自然到期，明确撤销、历史签发未知或信任修订变化不能靠续期恢复。</li><li>存在不同身份的过期祖先时，先用对应身份的新令牌续期祖先，再重新读取当前任务。续期不会释放未决费用；旧步骤需用真实回执完成或封存后，才可新增执行。</li></ol><p>以下仅展示当前页面读取时的结构；实际机器必须采用自己的预检结果，不能把此处历史快照直接当作当前授权。</p><pre v-if="machineExample" class="ai-security-code">{{ machineExample }}</pre><p v-else class="message message-warning">尚未取得兼容的完整凭据链，请先重新核对。</p></section>

    <dialog @keydown="containDialogTab" ref="proposalDialog" class="app-dialog app-dialog--wide" aria-labelledby="lease-proposal-title" aria-describedby="lease-proposal-description" @cancel="event=>!!busy && event.preventDefault()">
      <form v-if="proposalPlan" class="dialog-content" @submit.prevent="createProposal"><header class="dialog-heading"><h2 id="lease-proposal-title">提出受控换绑</h2><button class="icon-button" type="button" aria-label="关闭换绑申请，保留原因草稿" :disabled="!!busy" @click="closeProposal"><Icon name="close"/></button></header><div class="dialog-body"><p id="lease-proposal-description" class="dialog-description">选择需要变更的层。原任务范围、期限、步骤和预算保持；另一位负责人或复核人批准后才应用。工作负载凭据不能降级为静态凭据。</p><p class="lease-operation">操作标识 <code>{{ operationId }}</code></p><p v-if="proposalUncertain" class="message message-warning" role="status">提交结果尚未确认。表单已冻结，请按操作标识核对，或使用相同载荷幂等重试。</p><p v-if="targetsError" class="message message-error" role="alert">目标凭据未更新：{{ targetsError }}<button type="button" class="button button-secondary" :disabled="targetsLoading || !!busy" @click="loadTargets(targetsBefore)">重试当前凭据页</button></p><fieldset :disabled="!!busy || proposalUncertain" class="lease-proposal-fields"><div v-for="(node,index) in proposalPlan.chain" :key="node.taskId" class="lease-target-row"><div><strong>{{ index?'第 '+index+' 层委托':'根任务' }}</strong><p class="mono">{{ node.taskId }}</p><span class="table-secondary">当前 #{{ node.lease.credentialId }} · {{ node.credential.name }} · 租约修订 {{ node.lease.revision }}</span></div><label class="field">目标执行凭据<span class="visually-hidden">（任务 {{ node.taskId }}）</span><select v-model.number="targetIds[node.taskId]" :disabled="targetsLoading" @change="selectTarget(node.taskId)"><option :value="0">此层保持当前凭据</option><option v-for="item in targetOptions(node)" :key="item.id" :value="item.id" :disabled="!gatewayCredentialActive(item)">#{{ item.id }} · {{ item.name }}{{ item.expiresAt?' · 到期 '+formatDate(item.expiresAt):' · 静态应用凭据' }}{{ gatewayCredentialActive(item)?'':' · '+credentialState(item) }}</option></select><small>仅列同应用的有效目标；当前源为工作负载时，不列静态目标。</small></label></div><label class="field">换绑原因<input v-model.trim="proposalReason" required maxlength="240" placeholder="说明计划轮换或身份迁移原因，不填写敏感正文"></label><label class="field">申请有效至（可选，本机时区）<input v-model="proposalExpiry" type="datetime-local" step="1"><small>不超过 15 分钟；服务端还会缩短至目标短期凭据和任务的最早期限。</small></label></fieldset><div class="lease-target-pagination"><button type="button" class="button button-secondary" :disabled="targetsLoading || !!busy || !targetsTrail.length" @click="previousTargets">较新凭据</button><button type="button" class="button button-secondary" :disabled="targetsLoading || !!busy || !targetsPage?.hasMore || !targetsPage.nextCursor" @click="loadTargets(targetsPage?.nextCursor || '',true)">较早凭据</button><span>{{ targetsLoading?'正在读取有效凭据…':'目标凭据第 '+(targetsTrail.length+1)+' 页，本页 '+(targetsPage?.items.length || 0)+' 条' }}</span></div><div v-if="proposalFailure" class="message message-error" role="alert"><p>{{ proposalFailure.message }}</p><ul v-if="proposalFailure.blockers.length" class="lease-issues"><li v-for="(issue,index) in proposalFailure.blockers" :key="issue.code+':'+index">{{ issue.message }}<span class="table-secondary">{{ issueContext(issue) }}</span></li></ul><ul v-if="proposalFailure.recovery.length"><li v-for="item in proposalFailure.recovery" :key="item">{{ item }}</li></ul></div></div><footer class="dialog-actions"><button type="button" class="button button-secondary" :disabled="!!busy" @click="closeProposal">返回核对</button><button v-if="proposalUncertain" type="button" class="button button-secondary" :disabled="!!busy" @click="recoverProposal">按操作标识核对</button><button class="button button-primary" type="submit" :disabled="!!busy || targetsLoading || !!targetsError || !hasTargetChange && !proposalUncertain">{{ busy==='propose'?'正在提交…':proposalUncertain?'幂等重试原申请':'提交给独立复核' }}</button></footer></form>
    </dialog>
    <dialog @keydown="containDialogTab" ref="reviewDialog" class="app-dialog app-dialog--wide" aria-labelledby="lease-review-title" aria-describedby="lease-review-description" @cancel="event=>!!busy && event.preventDefault()">
      <form v-if="reviewPlan" class="dialog-content" @submit.prevent="submitReview"><header class="dialog-heading"><h2 id="lease-review-title">换绑申请 #{{ reviewPlan.id }} · {{ proposalState(reviewPlan) }}</h2><button class="icon-button" type="button" aria-label="关闭换绑复核" :disabled="!!busy" @click="closeReview"><Icon name="close"/></button></header><div class="dialog-body"><p id="lease-review-description" class="dialog-description">独立核对冻结变更、当前凭据与信任事实。批准将立即应用，相关旧审批与旧链版本不能继续放行；不会回滚动作、退还步骤或释放未知费用。</p><dl class="lease-review-facts"><div><dt>提出人</dt><dd>#{{ reviewPlan.proposedBy }}</dd></div><div><dt>采用修订</dt><dd>{{ reviewPlan.revision }}</dd></div><div><dt>截止时间</dt><dd>{{ formatDate(reviewPlan.expiresAt) }}</dd></div><div><dt>申请原因</dt><dd>{{ reviewPlan.reason }}</dd></div></dl><div v-for="change in reviewPlan.changes" :key="change.taskId" class="lease-review-change"><strong class="mono">{{ change.taskId }}</strong><p>凭据 #{{ reviewPlan.expectedChain.find(node=>node.taskId===change.taskId)?.credentialId }} → #{{ change.newCredentialId }} · 冻结租约修订 {{ reviewPlan.expectedChain.find(node=>node.taskId===change.taskId)?.leaseRevision }}</p><template v-for="plan in (reviewPlan.credentialPlan || []).filter(item=>item.taskId===change.taskId)" :key="plan.taskId"><div class="lease-credential-plan"><section><h4>源凭据 · #{{ plan.source.id }}</h4><p>{{ plan.source.name }}</p><p class="table-secondary">应用 {{ plan.source.applicationId }} · 前缀 {{ plan.source.prefix }}</p><p class="table-secondary">{{ plan.source.expiresAt?'到期 '+formatDate(plan.source.expiresAt):'静态应用凭据' }}</p><dl v-if="plan.sourceIdentity"><dt>issuer</dt><dd>{{ plan.sourceIdentity.issuer }}</dd><dt>audience</dt><dd>{{ plan.sourceIdentity.audience }}</dd><dt>subject</dt><dd>{{ plan.sourceIdentity.subject }}</dd><dt>绑定及签发修订</dt><dd>#{{ plan.sourceIdentity.bindingId }} / {{ plan.sourceIdentity.revision }}</dd></dl></section><section><h4>目标凭据 · #{{ plan.target.id }}</h4><p>{{ plan.target.name }}</p><p class="table-secondary">应用 {{ plan.target.applicationId }} · 前缀 {{ plan.target.prefix }}</p><p class="table-secondary">{{ plan.target.expiresAt?'到期 '+formatDate(plan.target.expiresAt):'静态应用凭据' }}</p><dl v-if="plan.targetIdentity"><dt>issuer</dt><dd>{{ plan.targetIdentity.issuer }}</dd><dt>audience</dt><dd>{{ plan.targetIdentity.audience }}</dd><dt>subject</dt><dd>{{ plan.targetIdentity.subject }}</dd><dt>绑定及签发修订</dt><dd>#{{ plan.targetIdentity.bindingId }} / {{ plan.targetIdentity.revision }}</dd></dl></section></div></template></div><p class="table-secondary">以上身份及期限为提出时冻结的事实。批准时服务端会再次核对当前状态与完整链版本。</p><details class="lease-evidence"><summary>核对冻结摘要与操作标识</summary><dl><dt>操作标识</dt><dd class="mono">{{ reviewPlan.operationId }}</dd><dt>申请摘要</dt><dd class="mono">{{ reviewPlan.proposalDigest }}</dd><dt>原凭据链摘要</dt><dd class="mono">{{ reviewPlan.expectedLeaseChainDigest }}</dd></dl></details><ul v-if="reviewPlan.blockers?.length" class="lease-issues"><li v-for="(issue,index) in reviewPlan.blockers" :key="issue.code+':'+index">{{ issue.message }}<span class="table-secondary">{{ issueContext(issue) }}</span></li></ul><fieldset v-if="reviewPlan.canReview && reviewPlan.status==='proposed'" class="lease-review-controls" :disabled="!!busy || reviewUncertain"><legend>独立复核决定</legend><label class="lease-radio"><input v-model="reviewDecision" type="radio" value="approve" required>批准并立即应用换绑</label><label class="lease-radio"><input v-model="reviewDecision" type="radio" value="reject" required>拒绝此申请</label><label class="field">独立复核意见<input v-model.trim="reviewNote" required maxlength="240" placeholder="说明核对依据与决定，不填写敏感正文"></label></fieldset><p v-else class="message message-warning">本申请当前不可由你复核；本人提出、已过期、已办理或条件发生变化时不能批准。</p><div v-if="reviewFailure" class="message message-error" role="alert"><p>{{ reviewFailure.message }}</p><ul v-if="reviewFailure.blockers.length" class="lease-issues"><li v-for="(issue,index) in reviewFailure.blockers" :key="issue.code+':'+index">{{ issue.message }}<span class="table-secondary">{{ issueContext(issue) }}</span></li></ul><ul v-if="reviewFailure.recovery.length"><li v-for="item in reviewFailure.recovery" :key="item">{{ item }}</li></ul></div></div><footer class="dialog-actions"><button type="button" class="button button-secondary" :disabled="!!busy" @click="closeReview">关闭并返回</button><button type="button" class="button button-secondary" :disabled="!!busy" @click="recoverReview">重新核对本申请</button><button v-if="reviewPlan.canReview" type="submit" class="button button-primary" :disabled="!!busy || reviewUncertain || !reviewDecision || !reviewNote.trim()">{{ busy==='review'?'正在提交…':reviewDecision==='approve'?'批准并应用换绑':'提交复核决定' }}</button></footer></form>
    </dialog>
  </section>
</template>

<style scoped>
.task-credential-status { border-top:1px solid var(--line); min-width:0; }
.task-credential-status h3,.task-credential-status h4 { margin:0; }
.task-credential-status .ai-security-heading { align-items:center; }
.task-credential-status>.message { margin:16px 20px; }
.lease-tabs { padding:8px 12px; border-bottom:1px solid var(--line); }
.lease-summary { display:flex; flex-wrap:wrap; gap:12px; padding:18px 20px; align-items:center; font-size:14px; }
.lease-summary>p,.lease-summary>ul { flex-basis:100%; margin:0; }
.lease-summary>span:not(.badge) { color:var(--secondary); font-size:13px; }
.lease-chain-table { min-width:960px; }
.lease-chain-table th:first-child { width:28%; min-width:260px; }
.lease-chain-table th:nth-child(2) { min-width:240px; }
.lease-chain-table th:nth-child(3) { min-width:200px; }
.lease-chain-table th:nth-child(4) { min-width:240px; }
.lease-proposals-table { min-width:920px; }
.lease-history-table { min-width:900px; }
.lease-issues { margin:0; padding-inline-start:20px; display:grid; gap:8px; }
.lease-task-link { min-height:44px; padding:6px 0; color:var(--accent); border:0; background:transparent; cursor:pointer; font:inherit; text-align:start; overflow-wrap:anywhere; }
.lease-evidence { margin-block:16px; overflow-wrap:anywhere; }
.lease-evidence summary { min-height:32px; cursor:pointer; color:var(--accent); }
.lease-evidence dl { margin:12px 0 0; display:grid; gap:6px; }
.lease-evidence dt { color:var(--secondary); font-size:13px; }
.lease-evidence dd { margin:0 0 10px; font-size:13px; }
.lease-transitions { display:grid; gap:14px; margin:0; padding:0; list-style:none; }
.lease-transitions li { display:grid; gap:4px; }
.lease-target-row { display:grid; grid-template-columns:minmax(0,1fr) minmax(0,1.2fr); gap:20px; padding-block:18px; border-bottom:1px solid var(--line); }
.lease-target-row:first-child { border-top:1px solid var(--line); }
.lease-target-row p { margin:8px 0 0; font-size:13px; }
.lease-target-row .field { margin:0; }
.lease-proposal-fields { display:grid; gap:20px; padding:0; margin:0; border:0; min-width:0; }
.lease-operation { overflow-wrap:anywhere; color:var(--secondary); font-size:13px; }
.lease-operation code { white-space:normal; }
.lease-target-pagination { display:flex; flex-wrap:wrap; align-items:center; gap:10px; margin-top:20px; }
.lease-target-pagination>span { color:var(--secondary); font-size:13px; }
.lease-review-facts { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:16px 24px; margin:0 0 20px; }
.lease-review-facts>div { min-width:0; }
.lease-review-facts dt { color:var(--secondary); font-size:13px; }
.lease-review-facts dd { margin:6px 0 0; font-size:14px; }
.lease-review-change { padding-block:16px; border-block-start:1px solid var(--line); overflow-wrap:anywhere; }
.lease-credential-plan {display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:24px;margin-top:16px;padding:16px;background:var(--surface-subtle);border:1px solid var(--line);border-radius:10px}
.lease-credential-plan>section {min-width:0}
.lease-credential-plan h4 {font-size:14px}
.lease-credential-plan dl {display:grid;gap:4px;margin:16px 0 0}
.lease-credential-plan dt {color:var(--secondary);font-size:13px}
.lease-credential-plan dd {margin:0 0 10px;overflow-wrap:anywhere;font-size:13px}
.lease-review-change p { margin:8px 0 0; }
.lease-review-controls { display:grid; gap:14px; padding:16px 0 0; border:0; margin-top:20px; }
.lease-review-controls legend { font-weight:600; padding:0; }
.lease-review-controls .field { margin:0; }
.lease-radio { display:flex; align-items:center; gap:10px; min-height:44px; }
.lease-radio input { width:18px; height:18px; flex:none; accent-color:var(--accent); }
.lease-machine ol { display:grid; gap:14px; padding-inline-start:22px; max-width:80ch; }
.lease-machine .ai-security-code { font-size:13px; }
@media(max-width:640px) { .lease-target-row { grid-template-columns:minmax(0,1fr); gap:12px; } .lease-review-facts,.lease-credential-plan { grid-template-columns:minmax(0,1fr); } .lease-tabs { padding:8px; } .lease-summary { padding:16px; } }
</style>
