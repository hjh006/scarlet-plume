<script setup lang="ts">
import {computed,nextTick,onMounted,onUnmounted,reactive,ref} from 'vue'
import {ApiError,describeError,downloadFile,formatDate,request,workspacePath,type Workspace} from './api'
import {gatewayDollars,gatewayCredentialActive,type GatewayConfiguration,type GatewayCredential} from './ai-gateway'
import {type AISecurityBundle} from './ai-security'
import {type AIRecord} from './ai-security-editor'
import AIGatewayTaskCredentialStatus from './AIGatewayTaskCredentialStatus.vue'
import Icon from './Icon.vue'
import {containDialogTab} from './dialog-keyboard'
import {taskAvailableBudget,taskCreatePayload,taskDelegationIssue,taskDraft,taskHeaderTemplate,taskLifecycleState,taskScope,taskStatusLabels,taskStatusReview,taskStatusReviewFailed,taskStatusReviewReadBack,taskStatusReviewReady,type GatewayTask,type GatewayTaskDetail,type GatewayTaskPage,type GatewayTaskStepPage,type GatewayTaskStatusReview} from './ai-gateway-tasks'

const props=defineProps<{workspace:Workspace;configuration:GatewayConfiguration;credentials:GatewayCredential[]}>()
const emit=defineEmits<{sessionExpired:[]}>()
const base=workspacePath(props.workspace.slug)+'/ai-gateway/tasks'
const home='#/'+encodeURIComponent(props.workspace.slug)
const controller=new AbortController()
const canWrite=computed(()=>['owner','operator'].includes(props.workspace.role))
const page=ref<GatewayTaskPage|null>(null),after=ref(''),history=ref<string[]>([])
const selectedId=ref(''),detail=ref<GatewayTaskDetail|null>(null),parent=ref<GatewayTask|null>(null)
const stepsPage=ref<GatewayTaskStepPage|null>(null),stepsBefore=ref(''),stepsHistory=ref<string[]>([]),stepsLoading=ref(false),stepsError=ref(''),catalogError=ref('')
const loading=ref(false),detailLoading=ref(false),busy=ref(''),error=ref(''),feedback=ref(''),reason=ref('')
const pageError=ref(''),detailError=ref(''),pageReadAt=ref('')
const draft=ref(taskDraft()),form=ref<HTMLFormElement|null>(null)
const statusDialog=ref<HTMLDialogElement>(),statusError=ref('')
const taskDetailTitle=ref<HTMLElement>()
const statusPlan=ref<GatewayTaskStatusReview>()
const unverifiedStatusTasks=reactive(new Set<string>())
const statusUnverified=computed(()=>unverifiedStatusTasks.has(selectedId.value))
const documentScope=ref(''),categoryScope=ref('')
const toolActions=ref<Record<string,string>>(Object.create(null)),toolTargets=ref<Record<string,string>>(Object.create(null)),catalogTools=ref<AIRecord[]>([])
const clock=ref(Date.now())
let generation=0,timer:ReturnType<typeof setInterval>|undefined,statusReturnFocus:HTMLElement|undefined
const app=computed(()=>props.configuration.applications.find(item=>item.id===draft.value.applicationId && item.enabled))
const availableCredentials=computed(()=>props.credentials.filter(item=>gatewayCredentialActive(item,clock.value) && item.applicationId===draft.value.applicationId))
const modelOptions=computed(()=>(app.value?.modelIds || []).filter(id=>!parent.value || (parent.value.modelIds || []).includes(id)))
const toolOptions=computed(()=>(app.value?.toolIds || []).filter(id=>!parent.value || (parent.value.toolIds || []).includes(id)))
const delegationIssue=computed(()=>statusUnverified.value?'结束操作尚未核实，先重新读取此任务，再准备委托。':!detailError.value && detail.value && detail.value.task.id===selectedId.value?taskDelegationIssue(detail.value.task,clock.value):'请先读取当前任务范围。')
const canDelegate=computed(()=>!!detail.value && !delegationIssue.value)
function lifecycle(task:GatewayTask){return taskLifecycleState(task,clock.value)}
function status(task:GatewayTask){const value=lifecycle(task);return taskStatusLabels[value] || value}
function errorText(cause:unknown){
  if(cause instanceof ApiError && cause.status===401)emit('sessionExpired')
  return cause instanceof ApiError && [400,403,409].includes(cause.status) && cause.message?cause.message:describeError(cause)
}
function handleError(cause:unknown){
  if(!controller.signal.aborted)error.value=errorText(cause)
}
function usableTask(task:GatewayTask|undefined,id?:string):boolean {
  return !!task && typeof task.id==='string' && task.id.length>0 && (!id || task.id===id) &&
    ['active','cancelled','completed'].includes(task.status) && Number.isSafeInteger(task.revision) && task.revision>0 &&
    Number.isFinite(Date.parse(task.expiresAt)) && [task.applicationId,task.principalId,task.purpose].every(value=>typeof value==='string' && value.length>0) &&
    [task.stepsUsed,task.maxSteps,task.spentMicros,task.reservedMicros,task.budgetMicros,task.depth,task.maxDelegationDepth,task.credentialId].every(Number.isSafeInteger)
}
async function loadPage(cursor=after.value,remember=false){
  if(loading.value)return
  loading.value=true;pageError.value=''
  try{
    const next=await request<GatewayTaskPage>(base+'?limit=100&after='+encodeURIComponent(cursor),{signal:controller.signal})
    if(controller.signal.aborted)return
    if(!Array.isArray(next?.items) || !next.items.every(item=>usableTask(item)) || typeof next.hasMore!=='boolean' || typeof next.nextCursor!=='string'){
      pageError.value='任务列表响应不完整，本次查询未更新。';return
    }
    if(remember)history.value.push(after.value)
    else if(!cursor)history.value=[]
    page.value=next;after.value=cursor;pageReadAt.value=new Date().toISOString()
    return true
  }catch(cause){if(!controller.signal.aborted)pageError.value=errorText(cause)}finally{loading.value=false}
}
async function previousPage(){const cursor=history.value.at(-1);if(cursor!==undefined && await loadPage(cursor))history.value.pop()}
async function selectTask(id:string,clearReason=true){
  const next=++generation;selectedId.value=id;detailLoading.value=true;detailError.value=''
  if(detail.value?.task.id!==id){detail.value=null;stepsPage.value=null;stepsBefore.value='';stepsHistory.value=[];stepsError.value=''}
  if(clearReason)reason.value=''
  else if(statusPlan.value?.id===id && statusPlan.value.requiresReadback)reason.value=statusPlan.value.payload.reason
  try{
    const result=await request<GatewayTaskDetail>(base+'/'+encodeURIComponent(id)+'?limit=100',{signal:controller.signal})
    if(controller.signal.aborted || next!==generation)return
    if(!usableTask(result?.task,id)){
      detailError.value='未取得所选任务的有效状态、期限和修订，请重新读取原任务。'
      if(statusPlan.value?.id===id)statusError.value=detailError.value
      return
    }
    detail.value=result;unverifiedStatusTasks.delete(id)
    if(statusPlan.value?.id===id && statusPlan.value.requiresReadback){statusPlan.value=taskStatusReviewReadBack(statusPlan.value,result.task);statusError.value=''}
    if(page.value)page.value.items=page.value.items.map(item=>item.id===result.task.id?result.task:item)
    if(parent.value?.id===result.task.id)parent.value=result.task
    await loadSteps('',false,id,next)
  }catch(cause){
    if(controller.signal.aborted || next!==generation)return
    detailError.value=errorText(cause)
    if(statusPlan.value?.id===id && statusPlan.value.requiresReadback){statusPlan.value=taskStatusReviewFailed(statusPlan.value);unverifiedStatusTasks.add(id);statusError.value=detailError.value}
  }finally{if(next===generation)detailLoading.value=false}
}
async function loadSteps(before='',remember=false,id=selectedId.value,expectedGeneration=generation){
  if(!id)return
  stepsLoading.value=true;stepsError.value=''
  try {
    const next=await request<GatewayTaskStepPage>(base+'/'+encodeURIComponent(id)+'/steps?limit=100'+(before?'&before='+encodeURIComponent(before):''),{signal:controller.signal})
    if(controller.signal.aborted || expectedGeneration!==generation || id!==selectedId.value)return
    if(!Array.isArray(next?.items) || typeof next.hasMore!=='boolean' || typeof next.nextCursor!=='string'){stepsError.value='步骤页响应不完整，本次查询未更新。';return}
    if(remember)stepsHistory.value.push(stepsBefore.value)
    else if(!before)stepsHistory.value=[]
    stepsPage.value=next;stepsBefore.value=before
    return true
  } catch(cause) {
    if(controller.signal.aborted || expectedGeneration!==generation)return
    if(cause instanceof ApiError && cause.status===401)emit('sessionExpired')
    stepsError.value=describeError(cause)
  } finally {if(expectedGeneration===generation)stepsLoading.value=false}
}
async function previousSteps(){const before=stepsHistory.value.at(-1);if(before!==undefined && await loadSteps(before))stepsHistory.value.pop()}
function focusTaskForm(){
  form.value?.scrollIntoView({block:'nearest',behavior:window.matchMedia('(prefers-reduced-motion: reduce)').matches?'auto':'smooth'})
  form.value?.querySelector<HTMLInputElement|HTMLSelectElement>('input:not([disabled]),select:not([disabled])')?.focus({preventScroll:true})
}
async function refresh(){await loadPage();if(selectedId.value)await selectTask(selectedId.value,false)}
async function refreshCredentialEvidence(){
  const id=selectedId.value,version=generation
  if(!id)return
  try{
    const result=await request<GatewayTaskDetail>(base+'/'+encodeURIComponent(id)+'?limit=100',{signal:controller.signal})
    if(controller.signal.aborted || generation!==version || selectedId.value!==id)return
    if(!usableTask(result?.task,id)){detailError.value='未取得所选任务的有效状态、期限和修订，请重新读取原任务。';return}
    detailError.value='';detail.value=result;await loadSteps('',false,id,version);await loadPage()
  }catch(cause){if(!controller.signal.aborted && generation===version)detailError.value=errorText(cause)}
}
async function loadToolCatalog(){
  catalogError.value=''
  try{const value=await request<AISecurityBundle>(workspacePath(props.workspace.slug)+'/ai-security',{signal:controller.signal});catalogTools.value=Array.isArray(value.configuration?.tools)?value.configuration.tools as AIRecord[]:[]}
  catch(cause){if(controller.signal.aborted)return;if(cause instanceof ApiError && cause.status===401)emit('sessionExpired');catalogError.value=describeError(cause)}
}
function toolCandidates(id:string,key:'actions'|'targets'):string[] {if(parent.value)return parent.value.toolScopes?.find(scope=>scope.toolId===id)?.[key] || [];const values=catalogTools.value.find(item=>item.id===id)?.[key];return Array.isArray(values)?values.filter((value):value is string=>typeof value==='string'):[]}
function newRoot(){parent.value=null;draft.value=taskDraft();documentScope.value='';categoryScope.value='';toolActions.value=Object.create(null);toolTargets.value=Object.create(null);error.value='';feedback.value='';focusTaskForm()}
function delegate(){
  if(!canWrite.value || !detail.value || detailError.value || detail.value.task.id!==selectedId.value)return
  clock.value=Date.now()
  const issue=taskDelegationIssue(detail.value.task,clock.value)
  if(issue){error.value=issue;feedback.value='';return}
  parent.value=detail.value.task;draft.value=taskDraft(parent.value);documentScope.value=draft.value.documentIds.join('\n');categoryScope.value=draft.value.allowedCategories.join('\n');toolActions.value=Object.fromEntries(draft.value.toolScopes.map(scope=>[scope.toolId,scope.actions.join('\n')]));toolTargets.value=Object.fromEntries(draft.value.toolScopes.map(scope=>[scope.toolId,scope.targets.join('\n')]));error.value='';feedback.value='委托草稿已采用当前父任务范围，请继续缩小需要的权限。';focusTaskForm()
}
function changeApp(){draft.value.credentialId=0;draft.value.modelIds=[];draft.value.toolIds=[];draft.value.documentIds=[];draft.value.allowedCategories=[];documentScope.value='';categoryScope.value='';toolActions.value=Object.create(null);toolTargets.value=Object.create(null)}
async function create(){
  if(!canWrite.value || busy.value)return
  if(parent.value && unverifiedStatusTasks.has(parent.value.id)){error.value='父任务的结束操作尚未核实，请先重新读取该任务。';return}
  let payload:ReturnType<typeof taskCreatePayload>
  try{payload=taskCreatePayload({...draft.value,documentIds:taskScope(documentScope.value),allowedCategories:taskScope(categoryScope.value),toolScopes:draft.value.toolIds.map(toolId=>({toolId,actions:taskScope(toolActions.value[toolId] || ''),targets:(toolTargets.value[toolId] || '').split(/\r?\n/).filter(value=>value.length>0)}))},props.configuration,props.credentials,parent.value || undefined)}catch(cause){error.value=cause instanceof Error?cause.message:'请核对任务范围。';return}
  busy.value='create';error.value='';feedback.value=''
  try{const result=await request<GatewayTask>(base,{method:'POST',body:JSON.stringify(payload),signal:controller.signal});parent.value=null;draft.value=taskDraft();documentScope.value='';categoryScope.value='';toolActions.value=Object.create(null);toolTargets.value=Object.create(null);await loadPage('');await selectTask(result.id);feedback.value='任务授权已创建。采用此任务的实际调用会留下步骤与用量记录。'}catch(cause){handleError(cause)}finally{busy.value=''}
}
async function requestStatus(state:'cancelled'|'completed'){
  if(!canWrite.value || !detail.value || detailLoading.value || detailError.value || detail.value.task.id!==selectedId.value || busy.value)return
  try{statusPlan.value=taskStatusReview(detail.value.task,state,reason.value,unverifiedStatusTasks)}catch(cause){error.value=cause instanceof Error?cause.message:'请核对任务状态。';return}
  statusError.value=''
  statusReturnFocus=document.activeElement instanceof HTMLElement && document.activeElement!==document.body?document.activeElement:undefined
  await nextTick();statusDialog.value?.showModal()
}
function closeStatusDialog(){if(busy.value!=='status')statusDialog.value?.close()}
async function statusClosed(){
  const taskId=statusPlan.value?.id,target=statusReturnFocus
  statusReturnFocus=undefined;statusPlan.value=undefined
  await nextTick()
  if(controller.signal.aborted || !statusDialog.value?.isConnected || statusDialog.value.open || !taskId || selectedId.value!==taskId)return
  const active=document.activeElement
  if(active instanceof HTMLElement && active!==document.body && !statusDialog.value.contains(active) && active.isConnected && !active.matches(':disabled'))return
  if(target?.isConnected && !target.matches(':disabled'))target.focus({preventScroll:true})
  else if(taskDetailTitle.value?.isConnected && taskDetailTitle.value.dataset.taskId===taskId)taskDetailTitle.value.focus({preventScroll:true})
}
async function submitStatus(){
  const plan=statusPlan.value
  if(!canWrite.value || !plan || busy.value || detailLoading.value || detailError.value || selectedId.value!==plan.id || !taskStatusReviewReady(plan,detail.value?.task))return
  busy.value='status';error.value='';feedback.value=''
  try{
    const result=await request<GatewayTask>(base+'/'+encodeURIComponent(plan.id)+'/status',{method:'POST',body:JSON.stringify(plan.payload),signal:controller.signal})
    if(!usableTask(result,plan.id) || result.status!==plan.payload.status || result.revision<=plan.payload.expectedRevision)throw new Error('缺少匹配的任务状态回执。')
    if(detail.value?.task.id===plan.id)detail.value={...detail.value,task:result}
    statusDialog.value?.close();reason.value=''
    await selectTask(plan.id,false);await loadPage()
    feedback.value='任务状态已更新。后续执行须通过祖先状态核对，此前已发生的副作用及未决费用仍需核对。'
  }catch(cause){if(!controller.signal.aborted){statusError.value=errorText(cause);unverifiedStatusTasks.add(plan.id);statusPlan.value=taskStatusReviewFailed(plan)}}finally{busy.value=''}
}
async function exportDetail(){if(!detail.value || detail.value.task.id!==selectedId.value || busy.value)return;busy.value='export';error.value='';try{await downloadFile(base+'/'+encodeURIComponent(detail.value.task.id)+'?limit=200','scarlet-agent-task-'+detail.value.task.id+'.json',controller.signal)}catch(cause){handleError(cause)}finally{busy.value=''}}
function openLinkedTask(){const id=new URLSearchParams(window.location.hash.split('?')[1] || '').get('task');if(id && /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,79}$/.test(id) && selectedId.value!==id)void selectTask(id)}
onMounted(()=>{timer=setInterval(()=>clock.value=Date.now(),1000);void loadPage('');void loadToolCatalog();openLinkedTask();window.addEventListener('hashchange',openLinkedTask)})
onUnmounted(()=>{statusReturnFocus=undefined;clearInterval(timer);controller.abort();window.removeEventListener('hashchange',openLinkedTask)})
</script>

<template>
  <div class="ai-gateway-sections ai-gateway-tasks" :aria-busy="loading || detailLoading || !!busy">
    <section class="panel">
      <div class="ai-security-heading"><div><h2>Agent 任务与受限委托</h2><p>将业务用途、调用凭证、数据范围及额度绑定为有期限的任务。子任务只能收窄权限，所有祖先限制同时生效。列表为任务自身登记状态，不代表祖先授权或真实执行成功。</p></div><div class="page-actions"><button class="button button-secondary" :disabled="loading || detailLoading || !!busy" @click="refresh">刷新任务与执行记录</button><button v-if="canWrite" class="button button-primary" :disabled="loading || detailLoading || !!busy" @click="newRoot">新建根任务</button></div></div>
      <p v-if="error" class="message message-error" role="alert">{{ error }}</p><p v-if="feedback" class="message message-success" role="status">{{ feedback }}</p>
      <p v-if="pageError" class="message message-error" role="alert">{{ pageError }}{{ page?' 仍显示上次成功读取的分页。':'' }}</p>
      <p v-if="loading" class="ai-security-body" role="status">正在读取任务授权与累计用量…</p>
      <div v-if="page?.items.length" class="table-scroll" role="region" aria-label="Agent 任务列表" tabindex="0">
        <table class="data-table task-register"><thead><tr><th scope="col">任务与用途</th><th scope="col">调用身份</th><th scope="col">状态与期限</th><th scope="col" class="table-number">累计步骤</th><th scope="col" class="table-number">模型报价（USD）</th><th scope="col">委托范围</th></tr></thead>
          <tbody><tr v-for="item in page.items" :key="item.id" :aria-selected="selectedId===item.id">
            <th scope="row"><button class="ai-gateway-task-link mono" :aria-pressed="selectedId===item.id" :disabled="detailLoading || !!busy" @click="selectTask(item.id)">{{ item.id }}</button><span class="table-secondary">用途 {{ item.purpose }}</span></th>
            <td><strong>{{ item.applicationId }}</strong><span class="table-secondary">主体 {{ item.principalId }}</span><span class="table-secondary">初始授权凭据 #{{ item.credentialId }}</span></td>
            <td><span class="badge" :class="lifecycle(item)==='active'?'status-verified':'status-draft'">{{ status(item) }}</span><time class="table-secondary table-date" :datetime="item.expiresAt" :title="item.expiresAt">{{ formatDate(item.expiresAt) }}</time></td>
            <td class="table-number"><strong>{{ item.stepsUsed.toLocaleString() }}</strong><span class="table-secondary">上限 {{ item.maxSteps.toLocaleString() }}</span></td>
            <td class="table-number"><dl class="task-cost"><dt>累计报价</dt><dd>{{ gatewayDollars(item.spentMicros) }}</dd><dt>未决预留</dt><dd>{{ gatewayDollars(item.reservedMicros) }}</dd><dt>任务额度</dt><dd>{{ gatewayDollars(item.budgetMicros) }}</dd></dl></td>
            <td>{{ item.depth?'第 '+item.depth+' 层委托':'根任务' }}<span class="table-secondary">登记剩余委托深度 {{ item.maxDelegationDepth }} 层</span><span v-if="item.parentTaskId" class="table-secondary mono">父任务 {{ item.parentTaskId }}</span></td>
          </tr></tbody>
        </table>
      </div>
      <p v-else-if="page && !loading && !pageError" class="empty-state">当前分页没有任务。任务登记不会自动执行模型或工具。</p>
      <div v-if="page" class="ai-security-body ai-security-actions"><button class="button button-secondary" :disabled="loading || !history.length" @click="previousPage">上一页</button><button class="button button-secondary" :disabled="loading || !page.hasMore || !page.nextCursor" @click="loadPage(page.nextCursor,true)">下一页</button><span>第 {{ history.length+1 }} 页 · 本页 {{ page.items.length }} 条 · {{ formatDate(pageReadAt,true) }} 读取</span></div>
    </section>

    <section v-if="selectedId" class="panel">
      <div class="ai-security-heading"><div><h2 ref="taskDetailTitle" tabindex="-1" :data-task-id="selectedId" aria-describedby="task-detail-reference">任务范围与步骤证据</h2><p id="task-detail-reference" class="mono">{{ selectedId }}</p></div><div class="page-actions"><button v-if="canWrite" class="button button-secondary" :disabled="detailLoading || !!busy || !canDelegate" aria-describedby="task-delegation-state" @click="delegate">基于此任务创建受限委托</button><button class="button button-secondary" :disabled="detailLoading || !detail || !!busy" @click="exportDetail">导出任务与最近 200 步</button></div></div>
      <p v-if="detailLoading" class="ai-security-body" role="status">正在核对当前任务范围与步骤…</p>
      <p v-if="detailError" class="message message-error" role="alert">{{ detailError }}{{ detail?' 下方保留此前的任务快照，当前授权待核对。':'' }}</p>
      <p v-if="statusUnverified" class="ai-security-body" role="status">结束操作的结果尚未核实，需要重新读取原任务。结束原因仍保留。</p>
      <template v-if="detail && detail.task.id===selectedId && !detailLoading">
        <div class="ai-security-body task-detail-body">
          <dl class="task-overview">
            <div><dt>任务状态</dt><dd><span class="badge" :class="lifecycle(detail.task)==='active'?'status-verified':'status-draft'">{{ status(detail.task) }}</span><span class="table-secondary">修订 {{ detail.task.revision }} · 到期 <time :datetime="detail.task.expiresAt" :title="detail.task.expiresAt">{{ formatDate(detail.task.expiresAt) }}</time></span></dd></div>
            <div><dt>固定用途与调用身份</dt><dd>{{ detail.task.purpose }}<span class="table-secondary">应用 {{ detail.task.applicationId }} · 主体 {{ detail.task.principalId }}</span></dd></div>
            <div><dt>本任务剩余范围</dt><dd>{{ Math.max(0,detail.task.maxSteps-detail.task.stepsUsed).toLocaleString() }} 步 · USD {{ gatewayDollars(taskAvailableBudget(detail.task)) }}<span class="table-secondary">仍受每个祖先与应用的状态、额度约束</span></dd></div>
          </dl>
          <p v-if="canWrite" id="task-delegation-state" class="table-secondary">{{ delegationIssue || '可基于当前登记范围创建委托草稿；保存和执行仍核对全部祖先约束。' }}</p>
          <section v-if="canWrite && detail.task.status==='active'" class="task-end-actions" aria-labelledby="task-end-title">
            <div><h3 id="task-end-title">结束后续执行</h3><p class="table-secondary">停止或关闭不会回滚此前的动作，也不释放未知执行结果的占额。运行中的输出会重新核对任务及祖先状态。</p></div>
            <label class="field">结束原因<input v-model="reason" maxlength="240" :disabled="!!busy" placeholder="填写简短业务原因，不填写敏感正文"></label>
            <div class="task-end-buttons"><button class="button button-secondary" :disabled="!!busy || !!detailError || statusUnverified || !reason.trim()" @click="requestStatus('cancelled')">停止此任务及后续委托执行</button><button class="button button-secondary" :disabled="!!busy || !!detailError || statusUnverified || !reason.trim()" @click="requestStatus('completed')">关闭任务，结束后续执行</button></div>
          </section>
          <p v-if="canWrite && detail.task.status!=='active' && reason" class="ai-security-note">未提交的结束原因草稿：{{ reason }}</p>
          <details class="task-scope-disclosure">
            <summary>核对已登记的权限范围<span class="table-secondary">{{ detail.task.modelIds?.length || 0 }} 个模型 · {{ detail.task.toolIds?.length || 0 }} 个工具 · {{ detail.task.documentIds?.length || 0 }} 个资料引用</span></summary>
            <dl class="ai-security-facts"><div><dt>模型范围</dt><dd>{{ (detail.task.modelIds || []).join('、') || '无' }}</dd></div><div><dt>工具范围</dt><dd>{{ (detail.task.toolIds || []).join('、') || '无' }}</dd></div><div><dt>资料范围</dt><dd>{{ (detail.task.documentIds || []).join('、') || '无' }}</dd></div><div><dt>允许类别</dt><dd>{{ (detail.task.allowedCategories || []).join('、') || '无已标记类别' }}</dd></div><div><dt>累计观察类别</dt><dd>{{ (detail.task.observedCategories || []).join('、') || '尚未记录类别' }}</dd></div></dl>
            <div v-for="scope in detail.task.toolScopes || []" :key="scope.toolId" class="task-tool-scope">
              <h4>工具 {{ scope.toolId }}</h4><p class="table-secondary">仅为任务登记范围，实际调用仍与工具目录、参数及独立批准取交集。</p>
              <details><summary>允许动作 · {{ scope.actions.length }} 项</summary><div class="task-scope-scroll" role="region" :aria-label="'工具 '+scope.toolId+' 的登记动作'" tabindex="0"><ol class="task-exact-list"><li v-for="(value,index) in scope.actions" :key="index"><code>{{ value }}</code></li></ol></div></details>
              <details><summary>精确目标 · {{ scope.targets.length }} 项</summary><p class="table-secondary">逐项显示原值，保留空格与逗号。</p><div class="task-scope-scroll" role="region" :aria-label="'工具 '+scope.toolId+' 的精确目标'" tabindex="0"><ol class="task-exact-list"><li v-for="(value,index) in scope.targets" :key="index"><code>{{ value }}</code></li></ol></div></details>
            </div><p v-if="!detail.task.toolScopes?.length" class="table-secondary">未声明工具动作与目标。</p>
          </details>
          <details class="task-scope-disclosure"><summary>核对目录摘要与任务关联</summary><dl class="ai-security-facts"><div><dt>目录摘要</dt><dd class="mono">{{ detail.task.catalogDigest }}</dd></div><div><dt>任务范围摘要</dt><dd class="mono">{{ detail.task.scopeDigest }}</dd></div><div><dt>根任务</dt><dd class="mono">{{ detail.task.rootTaskId }}</dd></div><div><dt>父任务</dt><dd><button v-if="detail.task.parentTaskId" class="ai-gateway-task-link" :disabled="!!busy" @click="selectTask(detail.task.parentTaskId)">{{ detail.task.parentTaskId }}</button><span v-else>无</span></dd></div></dl></details>
          <details class="task-scope-disclosure"><summary>核对调用方如何绑定此任务</summary><pre class="ai-security-code">{{ taskHeaderTemplate(detail.task) }}</pre><p class="table-secondary">任务标识是授权范围选择符，不能代替应用凭证。用途取自本任务记录，调用者不能自报用途或可信来源。执行点从逻辑请求标识派生步骤；重试不能复用已消费执行许可。</p></details>
        </div>
        <AIGatewayTaskCredentialStatus :workspace="workspace" :task-id="detail.task.id" @session-expired="emit('sessionExpired')" @select-task="selectTask($event)" @changed="refreshCredentialEvidence" />
        <div v-if="detail.task.sources?.length" class="table-scroll" role="region" aria-label="任务累积的实际资料来源" tabindex="0"><table class="data-table"><thead><tr><th scope="col">实际来源资料</th><th scope="col">来源 / ACL 版本</th><th scope="col">实际正文摘要</th></tr></thead><tbody><tr v-for="source in detail.task.sources" :key="source.documentId"><th scope="row">{{ source.documentId }}</th><td>{{ source.version }} / {{ source.aclVersion }}</td><td class="mono">{{ source.contentDigest }}</td></tr></tbody></table></div>
        <div class="ai-security-heading"><div><h3>完整步骤历史，逐页核对</h3><p>每页最多 100 条，按受理顺序从新到旧。任务导出仍仅含最近 200 步，需保留其截断标记。</p></div><button class="button button-secondary" :disabled="stepsLoading" @click="loadSteps()">返回最新步骤</button></div>
        <p v-if="stepsLoading" class="ai-security-body" role="status">正在读取步骤历史…</p><p v-if="stepsError" class="message message-error" role="alert">{{ stepsError }}{{ stepsPage?' 当前保留上次读取的步骤页。':'' }}</p>
        <div v-if="stepsPage?.items.length" class="table-scroll" role="region" aria-label="任务实际执行步骤" tabindex="0"><table class="data-table"><thead><tr><th scope="col">步骤与时间</th><th scope="col">目的端</th><th scope="col">阶段与类别</th><th scope="col">绑定证据</th></tr></thead><tbody><tr v-for="step in stepsPage.items" :key="step.id"><th scope="row">{{ step.stepId }}<time class="table-secondary table-date" :datetime="step.createdAt" :title="step.createdAt">{{ formatDate(step.createdAt) }}</time></th><td>{{ step.kind }} · {{ step.modelId || step.toolId || '资料检索' }}<span class="table-secondary">资料 {{ step.documentIds?.join('、') || '无' }}</span></td><td>{{ step.status==='admitted'?'已受理，终态待核对':step.status==='completed'?'步骤处理结束，效果需核对':step.status==='closed'?'已封存，保留原执行与费用事实':step.status }}<span class="table-secondary">{{ step.categories?.join('、') || '无已标记类别' }}</span></td><td class="mono">{{ step.stepDigest }}<span v-for="source in step.sourceEvidence || []" :key="source.documentId" class="table-secondary mono">{{ source.documentId }} · {{ source.version }} / {{ source.aclVersion }} · {{ source.contentDigest }}</span></td></tr></tbody></table></div>
        <p v-else-if="!stepsLoading && !stepsError" class="empty-state">当前步骤页没有记录。登记任务不表示执行点已经接入。</p>
        <div v-if="stepsPage" class="ai-security-pagination"><button class="button button-secondary" :disabled="stepsLoading || !stepsHistory.length" @click="previousSteps">较新一页</button><button class="button button-secondary" :disabled="stepsLoading || !stepsPage.hasMore || !stepsPage.nextCursor" @click="loadSteps(stepsPage.nextCursor,true)">较早一页</button><p>第 {{ stepsHistory.length+1 }} 页 · 本页 {{ stepsPage.items.length }} 条 · {{ stepsPage.hasMore?'仍有较早步骤':'已到最早步骤' }}</p></div>
        <p class="ai-security-body table-secondary">步骤 completed 是处理阶段记录，closed 是有终态证据的历史封存；两者均不证明客户端完整收取内容或独立安全效果，封存不会退还未知费用。请结合<a :href="home+'/ai-gateway?tab=audit'">调用审计</a>核对每次提供方尝试、输出阻断及未决费用。</p>
      </template>
    </section>

    <form v-if="canWrite" ref="form" class="panel" @submit.prevent="create">
      <div class="ai-security-heading"><div><h2>{{ parent?'创建受限子委托':'创建根任务授权' }}</h2><p>{{ parent?'父任务 '+parent.id+' 的范围、用途和全部祖先约束同时适用。':'先选择已保存的应用与有效凭证，再明确本次业务用途和最小执行范围。' }}</p></div><button class="button button-primary" :disabled="!!busy || !configuration.enabled">{{ busy==='create'?'正在创建…':'保存任务授权' }}</button></div>
      <div class="ai-security-body"><p v-if="catalogError" class="message message-warning" role="status">工具目录候选未能载入：{{ catalogError }}。任务仍由服务端核对真实目录；请重试读取后填写。<button class="button button-secondary" type="button" @click="loadToolCatalog">重试工具目录</button></p><p v-if="!configuration.enabled" class="message message-warning">当前网关未启用。请先保存启用的网关配置与可信目录摘要。</p><fieldset class="ai-security-fields" :disabled="!!busy">
        <label class="field">已保存应用<select v-model="draft.applicationId" required :disabled="!!parent" @change="changeApp"><option value="">请选择启用应用</option><option v-for="item in configuration.applications.filter(item=>item.enabled)" :key="item.id" :value="item.id">{{ item.name }} · {{ item.id }}</option></select><small>固定主体 {{ app?.principalId || '待选择应用' }}；任务不能改变调用主体。</small></label>
        <label class="field">绑定应用凭证<select v-model.number="draft.credentialId" required><option :value="0">请选择有效凭证</option><option v-for="item in availableCredentials" :key="item.id" :value="item.id">#{{ item.id }} · {{ item.name }} · {{ item.prefix }}</option></select><small>委托可绑定同应用、同主体的另一有效凭证，不传递明文令牌。</small></label>
        <label class="field">业务用途标识<input v-model.trim="draft.purpose" required maxlength="80" :readonly="!!parent" placeholder="support-answer"><small>子委托必须沿用父任务用途。这里填写明确标识，不填写提示词或业务正文。</small></label>
        <label class="field">到期时间（带时区）<input v-model.trim="draft.expiresAt" required maxlength="40" placeholder="2026-11-17T18:00:00+08:00"><small>未来 7 天内；子任务不得晚于父任务。当前显示为 {{ formatDate(draft.expiresAt) }}。</small></label>
        <label class="field">允许模型<select v-model="draft.modelIds" multiple size="4"><option v-for="id in modelOptions" :key="id" :value="id">{{ id }}</option></select><small>需要自动切换时，将每条备用模型也明确包含在任务范围。</small></label>
        <label class="field">允许工具<select v-model="draft.toolIds" multiple size="4"><option v-for="id in toolOptions" :key="id" :value="id">{{ id }}</option></select><small>任务范围不会代替工具动作、参数或高影响独立批准。</small></label>
        <div v-for="id in draft.toolIds" :key="id" class="field-wide ai-gateway-policy"><h3>工具 {{ id }} 的精确动作与目标</h3><div class="ai-security-fields">
          <div class="field"><label :for="'task-tool-actions-'+id">工具 {{ id }} 允许动作</label><textarea :id="'task-tool-actions-'+id" v-model="toolActions[id]" rows="3" required></textarea><small>每行一个明确动作，最多 200 个。</small><details v-if="toolCandidates(id,'actions').length" class="task-candidates"><summary>核对{{ parent?'父任务':'当前目录' }}动作候选 · {{ toolCandidates(id,'actions').length }} 项</summary><div class="task-scope-scroll" role="region" :aria-label="'工具 '+id+' 的动作候选'" tabindex="0"><ol class="task-exact-list"><li v-for="(value,index) in toolCandidates(id,'actions')" :key="index"><code>{{ value }}</code></li></ol></div></details><small v-else>未提供候选，需依据已审核客户侧目录填写。</small></div>
          <div class="field"><label :for="'task-tool-targets-'+id">工具 {{ id }} 精确目标</label><textarea :id="'task-tool-targets-'+id" v-model="toolTargets[id]" rows="3" required></textarea><small>每行一个，最多 200 个；保留逗号及空格，不支持 * 通配。</small><details v-if="toolCandidates(id,'targets').length" class="task-candidates"><summary>核对{{ parent?'父任务':'当前目录' }}目标候选 · {{ toolCandidates(id,'targets').length }} 项</summary><div class="task-scope-scroll" role="region" :aria-label="'工具 '+id+' 的目标候选'" tabindex="0"><ol class="task-exact-list"><li v-for="(value,index) in toolCandidates(id,'targets')" :key="index"><code>{{ value }}</code></li></ol></div></details><small v-else>未提供候选，需依据已审核客户侧目录填写。</small></div>
        </div><p class="table-secondary">这是任务的收窄范围，实际调用还与已采用工具目录的动作、目标、参数和独立批准取交集。</p></div>
        <label class="field">允许资料标识<textarea v-model="documentScope" rows="3" :disabled="!app?.allowRag"></textarea><small>每行一个，最多 64 项；需与实际可信目录一致，资料 ACL 与正文摘要仍逐项核对。可以缩小直接读取范围，但历史来源的敏感上下文约束仍继承；不会自动授予子任务原文读取权。</small></label>
        <label class="field">允许数据类别<textarea v-model="categoryScope" rows="3"></textarea><small>每行一个，最多 64 项；任务实际积累的敏感标签会约束后续模型及工具外发，子任务不得扩大父范围，也不能移除已积累的类别约束。</small></label>
        <label class="field">累计逻辑步骤上限<input v-model.number="draft.maxSteps" type="number" required min="1" :max="parent?Math.max(0,parent.maxSteps-parent.stepsUsed):1000000" step="1"><small>模型、资料、工具与缓存执行均受约束；后续调用同时计入全部祖先。</small></label>
        <label class="field">累计模型报价额度（USD）<input v-model.trim="draft.budgetUSD" required inputmode="decimal" pattern="[0-9]+(\.[0-9]{1,6})?"><small>0 可用于无需提供方费用的缓存、资料和工具步骤，仍计步数且受应用限流。未知费用保留预留。</small></label>
        <label class="field">剩余可委托层数<input v-model.number="draft.maxDelegationDepth" type="number" required min="0" :max="parent?Math.max(0,parent.maxDelegationDepth-1):4" step="1"><small>0 禁止继续委托；每层递减，绝对深度最多 4。</small></label>
        <p class="ai-security-note">模型、工具与资料范围空列表代表没有相应权限。任务授权不能放宽应用、主体、来源、地域、数据处理约定或独立批准。创建后还需由客户侧执行点采用任务绑定并提交真实步骤。</p>
      </fieldset><div v-if="parent" class="ai-security-actions"><button class="button button-secondary" type="button" :disabled="loading || detailLoading || !!busy" @click="newRoot">改为新建根任务</button><span>父任务剩余 {{ Math.max(0,parent.maxSteps-parent.stepsUsed) }} 步，未消费或预留额度 USD {{ gatewayDollars(taskAvailableBudget(parent)) }}；保存时服务端再次核对。</span></div></div>
    </form>
    <p v-else class="ai-security-body table-secondary">当前账号可核对任务与步骤；任务授权及停止后续执行需要负责人或经办人。</p>
    <dialog @keydown="containDialogTab" ref="statusDialog" class="app-dialog" aria-labelledby="task-status-dialog-title" aria-describedby="task-status-dialog-description" @cancel="event=>busy==='status' && event.preventDefault()" @close="statusClosed">
      <form v-if="statusPlan" class="dialog-content" @submit.prevent="submitStatus">
        <header class="dialog-heading"><h2 id="task-status-dialog-title">{{ statusPlan.payload.status==='cancelled'?'停止任务及后续委托':'关闭任务，结束后续执行' }}</h2><button type="button" class="icon-button" aria-label="返回核对任务" :disabled="busy==='status'" @click="closeStatusDialog"><Icon name="close"/></button></header>
        <div class="dialog-body"><p id="task-status-dialog-description" class="dialog-description">操作将阻止此任务及后续委托继续执行，任务不能恢复为活动状态。核对下方范围后确认，已经发生的动作和未决费用将保留。</p><dl class="task-status-facts"><div><dt>任务</dt><dd class="mono">{{ statusPlan.id }}</dd></div><div><dt>固定用途</dt><dd>{{ statusPlan.purpose }}</dd></div><div><dt>应用 / 主体</dt><dd>{{ statusPlan.applicationId }} / {{ statusPlan.principalId }}</dd></div><div><dt>采用修订</dt><dd>{{ statusPlan.payload.expectedRevision }}</dd></div><div><dt>结束原因</dt><dd>{{ statusPlan.payload.reason }}</dd></div></dl><p v-if="statusError" class="message message-error" role="alert">{{ statusError }}</p><p v-if="statusPlan.requiresReadback" class="ai-security-note" role="status">{{ statusPlan.readBack?'已重新读取原任务：'+(taskStatusLabels[statusPlan.readBack.status] || statusPlan.readBack.status)+'，修订 '+statusPlan.readBack.revision+'。请返回详情重新确认，本次冻结确认不再提交。':'本次提交结果尚未核实。请先重新读取原任务；读取失败时保留原因草稿，不重复提交。' }}</p></div>
        <footer class="dialog-actions"><button type="button" class="button button-secondary" autofocus :disabled="busy==='status'" @click="closeStatusDialog">返回核对</button><button v-if="statusPlan.requiresReadback" type="button" class="button button-secondary" :disabled="detailLoading || !!busy" @click="selectTask(statusPlan.id,false)">{{ detailLoading?'正在读取原任务…':'重新读取原任务' }}</button><button type="submit" class="button button-danger" :disabled="!!busy || detailLoading || !taskStatusReviewReady(statusPlan,detail?.task) || selectedId!==statusPlan.id || !!detailError">{{ busy==='status'?'正在提交…':statusPlan.payload.status==='cancelled'?'确认停止后续执行':'确认关闭任务' }}</button></footer>
      </form>
    </dialog>
  </div>
</template>

<style scoped>
.ai-gateway-task-link {min-height:44px;padding:6px 0;border:0;background:transparent;color:var(--accent);font:inherit;text-align:left;overflow-wrap:anywhere;cursor:pointer}
.ai-gateway-task-link[aria-pressed="true"] {font-weight:700;text-decoration:underline}
.ai-gateway-tasks .message {padding:14px 20px}
.ai-gateway-tasks select[multiple] {min-height:120px}
.ai-gateway-tasks .data-table {min-width:800px}
.ai-gateway-tasks .task-register {min-width:960px;table-layout:fixed}
.task-register thead th:first-child {width:23%}
.task-register thead th:nth-child(2) {width:15%}
.task-register thead th:nth-child(3) {width:16%}
.task-register thead th:nth-child(4) {width:10%}
.task-register thead th:nth-child(5) {width:20%}
.task-register td {overflow-wrap:anywhere}
.task-register .ai-gateway-task-link {display:block;width:100%;font-weight:600}
.task-cost {display:grid;grid-template-columns:minmax(0,1fr) auto;gap:6px 16px;margin:0;min-width:160px;font-size:13px;font-variant-numeric:tabular-nums}
.task-cost dt {text-align:start;color:var(--secondary);font-weight:400}
.task-cost dd {margin:0;text-align:end;white-space:nowrap}
.task-detail-body {display:grid;gap:18px}
.task-detail-body>p {margin:0}
.task-overview {display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:20px;margin:0}
.task-overview>div {min-width:0}
.task-overview dt {margin-bottom:8px;color:var(--secondary);font-size:13px}
.task-overview dd {margin:0;font-weight:600;overflow-wrap:anywhere;font-variant-numeric:tabular-nums}
.task-overview .table-secondary {display:block;margin-top:7px;font-size:13px;font-weight:400}
.task-end-actions {display:grid;gap:14px;padding:18px;border:1px solid var(--line);border-radius:8px;background:var(--surface-subtle)}
.task-end-actions h3,.task-tool-scope h4 {margin:0;font-size:14px}
.task-end-actions p,.task-tool-scope>p {margin:6px 0 0}
.task-end-buttons {display:flex;flex-wrap:wrap;gap:8px}
.task-scope-disclosure {padding:4px 0;border-top:1px solid var(--line);min-width:0}
.task-scope-disclosure>summary {padding:10px 0;color:var(--accent);font-weight:600;cursor:pointer}
.task-scope-disclosure>summary .table-secondary {display:block;margin:4px 0 0;font-size:13px;font-weight:400;color:var(--secondary)}
.task-tool-scope {margin-top:18px;padding-top:16px;border-top:1px solid var(--line)}
.task-tool-scope summary,.task-candidates summary {padding:10px 0;cursor:pointer;color:var(--accent)}
.task-scope-scroll {max-height:260px;overflow:auto;overscroll-behavior:contain;border:1px solid var(--line);border-radius:6px;background:var(--surface)}
.task-exact-list {margin:0;padding:0 14px 0 44px}
.task-exact-list li {padding:10px 0;border-bottom:1px solid var(--line);overflow-wrap:anywhere}
.task-exact-list li:last-child {border-bottom:0}
.task-exact-list code {white-space:pre-wrap;font-size:13px;line-height:1.7}
.task-status-facts {display:grid;gap:0;margin:20px 0 0}
.task-status-facts>div {display:grid;grid-template-columns:88px minmax(0,1fr);gap:16px;padding:14px 0;border-top:1px solid var(--line)}
.task-status-facts dt {color:var(--secondary)}
.task-status-facts dd {margin:0;overflow-wrap:anywhere}
.ai-gateway-tasks .ai-security-note {margin:0}
.ai-gateway-tasks .field textarea {min-height:100px}
@media(max-width:800px){.task-overview{grid-template-columns:minmax(0,1fr);gap:16px}.task-overview>div+div{padding-top:16px;border-top:1px solid var(--line)}.task-end-actions{padding:14px}.task-end-buttons>button{flex:1 1 220px}}
@media(max-width:480px){.task-status-facts>div{grid-template-columns:minmax(0,1fr);gap:4px}}
</style>
