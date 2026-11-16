<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { ApiError, describeError, formatDate, request } from './api'
import { aiDecisionLabel, aiString } from './ai-security'
import type { AIRecord } from './ai-security-editor'
import { aiApprovalId, buildAIApprovalReviewPlan, sameAIApprovalBinding, type AIApprovalReviewIntent, type AIApprovalReviewPlan } from './ai-security-approvals'
import { containDialogTab } from './dialog-keyboard'
import Icon from './Icon.vue'

const props=defineProps<{base:string;intent:AIApprovalReviewIntent|null;trigger?:HTMLElement}>()
const emit=defineEmits<{close:[];reviewed:[record:AIRecord];sessionExpired:[]}>()
const dialog=ref<HTMLDialogElement>(),record=shallowRef<AIRecord>(),plan=shallowRef<AIApprovalReviewPlan>(),anchor=shallowRef<AIRecord>()
const loading=ref(false),submitting=ref(false),needsRefresh=ref(false),error=ref(''),observedAt=ref(''),clock=ref(Date.now())
let operation:AbortController|undefined,returnFocus:HTMLElement|undefined,timer:ReturnType<typeof setInterval>|undefined
const expired=computed(()=>!!record.value && Date.parse(aiString(record.value,'expiresAt'))<=clock.value)
const ready=computed(()=>!!plan.value && !expired.value && !loading.value && !submitting.value && !needsRefresh.value)
const argumentsRows=computed(()=>record.value?.arguments && typeof record.value.arguments==='object' && !Array.isArray(record.value.arguments)?Object.entries(record.value.arguments):[])

async function loadRecord() {
  const intent=props.intent
  if(!intent || submitting.value)return
  operation?.abort();const active=new AbortController();operation=active
  loading.value=true;error.value=''
  try {
    const [fresh,configuration]=await Promise.all([
      request<AIRecord>(props.base+'/approvals/'+encodeURIComponent(intent.id),{signal:active.signal}),
      request<{revision:number}>(props.base,{signal:active.signal}),
    ])
    if(active.signal.aborted)return
    if(aiApprovalId(fresh.id)!==intent.id)throw new Error('返回记录与所选申请不一致，请返回列表重新核对。')
    if(anchor.value && !sameAIApprovalBinding(anchor.value,fresh))throw new Error('申请绑定事实已变化，请返回列表重新核对；本弹窗不会改用新的动作范围。')
    record.value=fresh;observedAt.value=new Date().toISOString();clock.value=Date.now()
    if(fresh.status!=='proposed'){plan.value=undefined;needsRefresh.value=false;return}
    plan.value=buildAIApprovalReviewPlan(fresh,intent,configuration.revision,clock.value)
    anchor.value ??= structuredClone(fresh)
    needsRefresh.value=false
  }catch(cause){
    if(active.signal.aborted)return
    plan.value=undefined
    if(cause instanceof ApiError && cause.status===401)emit('sessionExpired')
    error.value=describeError(cause)
  }finally{if(!active.signal.aborted)loading.value=false}
}
async function submit() {
  if(!ready.value || !plan.value)return
  const frozen=plan.value
  try{buildAIApprovalReviewPlan(frozen.record,frozen,Number(frozen.record.configurationRevision),Date.now())}
  catch(cause){error.value=describeError(cause);return}
  operation?.abort();const active=new AbortController();operation=active
  submitting.value=true;error.value=''
  try{
    const out=await request<AIRecord>(props.base+'/approvals/'+encodeURIComponent(frozen.id)+'/review',{method:'POST',body:JSON.stringify({decision:frozen.decision,note:frozen.note}),signal:active.signal})
    if(active.signal.aborted)return
    if(!sameAIApprovalBinding(frozen.record,out) || out.status!==frozen.decision)throw new Error('服务端返回与本次复核不一致，请重新核对原申请的实际记录。')
    record.value=out;plan.value=undefined;emit('reviewed',out)
  }catch(cause){
    if(active.signal.aborted)return
    needsRefresh.value=true
    if(cause instanceof ApiError && cause.status===401)emit('sessionExpired')
    error.value=describeError(cause)
  }finally{if(!active.signal.aborted)submitting.value=false}
}
function close(){if(!submitting.value)dialog.value?.close()}
async function closed(){
  operation?.abort();clearInterval(timer);timer=undefined
  record.value=undefined;plan.value=undefined;anchor.value=undefined;emit('close')
  await nextTick()
  const active=document.activeElement,target=returnFocus
  returnFocus=undefined
  if(!dialog.value?.isConnected || active instanceof HTMLElement && active!==document.body && !dialog.value.contains(active) && active.isConnected && !active.matches(':disabled'))return
  if(target?.isConnected && !target.matches(':disabled'))target.focus({preventScroll:true})
  else document.getElementById('ai-approvals-title')?.focus({preventScroll:true})
}
watch(()=>props.intent,async intent=>{
  operation?.abort();clearInterval(timer);timer=undefined
  record.value=undefined;plan.value=undefined;anchor.value=undefined;error.value='';needsRefresh.value=false;submitting.value=false
  if(!intent){dialog.value?.close();return}
  returnFocus=props.trigger || (document.activeElement instanceof HTMLElement && document.activeElement!==document.body?document.activeElement:undefined)
  await nextTick()
  if(props.intent!==intent)return
  dialog.value?.showModal();timer=setInterval(()=>clock.value=Date.now(),1000)
  void loadRecord()
})
onBeforeUnmount(()=>{operation?.abort();clearInterval(timer);returnFocus=undefined})
</script>

<template>
  <dialog ref="dialog" class="app-dialog app-dialog--wide" aria-labelledby="ai-approval-review-title" aria-describedby="ai-approval-review-description" @keydown="containDialogTab" @cancel="event=>submitting && event.preventDefault()" @close="closed">
    <form v-if="intent" class="dialog-content" @submit.prevent="submit">
      <header class="dialog-heading"><h2 id="ai-approval-review-title">核对批准 #{{ intent.id }} · {{ intent.decision==='approved'?'批准限定动作':'拒绝申请' }}</h2><button type="button" class="icon-button" aria-label="返回核对批准申请" :disabled="submitting" @click="close"><Icon name="close"/></button></header>
      <div class="dialog-body">
        <p id="ai-approval-review-description" class="dialog-description">决定将绑定下面的原请求。发起人和当前配置编辑人不能复核；目录、任务、期限及使用状态仍需在提交和执行时核对。</p>
        <p v-if="loading" role="status">正在重新读取原申请及当前配置修订…</p>
        <template v-if="record">
          <dl class="ai-security-facts"><div><dt>服务端记录状态</dt><dd>{{ aiDecisionLabel(record) }}</dd></div><div><dt>读取时间</dt><dd>{{ formatDate(observedAt) }}</dd></div><div><dt>应用 / 主体</dt><dd>{{ aiString(record,'applicationId') }} / {{ aiString(record,'principalId') }}</dd></div><div><dt>工具 / 动作</dt><dd>{{ aiString(record,'toolId') }} / {{ aiString(record,'action') }}</dd></div><div><dt>准确目标</dt><dd><code class="approval-exact">{{ aiString(record,'target') }}</code></dd></div><div><dt>申请人 / 复核人</dt><dd>#{{ aiString(record,'requestedBy') }} / {{ record.reviewedBy?'#'+aiString(record,'reviewedBy'):'尚未复核' }}</dd></div><div><dt>配置修订</dt><dd>{{ aiString(record,'configurationRevision') }}</dd></div><div><dt>申请截止时间</dt><dd>{{ formatDate(aiString(record,'expiresAt')) }}</dd></div></dl>
          <h3 class="approval-section-title">限定参数</h3>
          <div v-if="argumentsRows.length" class="table-scroll" role="region" aria-label="批准申请的准确参数" tabindex="0"><table class="data-table approval-parameters"><thead><tr><th scope="col">参数</th><th scope="col">原始限定值</th></tr></thead><tbody><tr v-for="[key,value] in argumentsRows" :key="key"><th scope="row"><code>{{ key }}</code></th><td><pre class="approval-exact">{{ typeof value==='string'?value:JSON.stringify(value) }}</pre></td></tr></tbody></table></div><p v-else class="table-secondary">本申请没有参数项。</p>
          <details class="approval-binding"><summary>核对请求、目录与任务租约绑定</summary><dl class="ai-security-facts"><div><dt>请求引用</dt><dd class="mono">{{ aiString(record,'requestId') }}</dd></div><div><dt>请求摘要</dt><dd class="mono">{{ aiString(record,'requestDigest') }}</dd></div><div><dt>目录摘要</dt><dd class="mono">{{ aiString(record,'catalogDigest') }}</dd></div><div><dt>原请求签发时间</dt><dd>{{ formatDate(aiString(record,'issuedAt')) }}</dd></div><template v-if="record.taskId"><div><dt>任务</dt><dd class="mono">{{ aiString(record,'taskId') }}</dd></div><div><dt>祖先租约协议</dt><dd class="mono">{{ aiString(record,'leaseProtocolVersion') || '未提供' }}</dd></div><div><dt>完整祖先租约摘要</dt><dd class="mono">{{ aiString(record,'leaseChainDigest') || '未提供，不能复核任务请求' }}</dd></div></template></dl></details>
          <p v-if="record.status!=='proposed'" class="message">当前服务器记录为“{{ aiDecisionLabel(record) }}”，此处仅供核对，不重复提交。</p>
          <p v-else-if="expired" class="message message-warning" role="status">本申请已到期，请返回原范围重新申请。</p>
        </template>
        <h3 class="approval-section-title">本次独立复核意见</h3><p class="approval-exact approval-note">{{ intent.note }}</p>
        <p v-if="error" class="message message-error" role="alert">{{ error }}</p><p v-if="needsRefresh" class="table-secondary">请重新核对原申请的服务端记录后继续；结果未核实时不会重复提交。</p>
      </div>
      <footer class="dialog-actions"><button type="button" class="button button-secondary" :disabled="submitting" autofocus @click="close">返回核对</button><button type="button" class="button button-secondary" :disabled="loading || submitting" @click="loadRecord">重新核对原申请</button><button type="submit" class="button" :class="intent.decision==='approved'?'button-primary':'button-danger'" :disabled="!ready">{{ submitting?'正在提交复核…':intent.decision==='approved'?'确认批准限定动作':'确认拒绝申请' }}</button></footer>
    </form>
  </dialog>
</template>

<style scoped>
.approval-section-title{margin:24px 0 10px;font-size:15px;line-height:1.6}
.approval-exact{margin:0;white-space:pre-wrap;overflow-wrap:anywhere;font-size:14px;line-height:1.75}
.approval-parameters{min-width:420px}.approval-parameters th:first-child{width:30%}
.approval-binding{margin-top:20px}.approval-binding summary{padding:12px 0;cursor:pointer;color:var(--accent);font-size:14px}
.approval-note{padding:14px;background:var(--surface-subtle);border-radius:7px}
</style>
