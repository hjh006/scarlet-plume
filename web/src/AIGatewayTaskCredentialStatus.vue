<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { gatewayRevocationLabel, type GatewayCredential } from './ai-gateway'

const props=defineProps<{workspace:Workspace;taskId:string}>()
const emit=defineEmits<{sessionExpired:[];selectTask:[id:string]}>()
interface Blocker {taskId?:string;code:string;state:'blocked'|'unknown';message:string}
interface PathEntry {taskId:string;depth:number;taskRevision:number;taskStatus:string;taskExpiresAt:string;scopeDigest:string;credential:GatewayCredential;bindingStatus:string;currentBindingRevision?:number;credentialState:string;blockers:Blocker[];renewalBlockers:Blocker[]}
interface CredentialStatus {taskId:string;rootTaskId:string;observedAt:string;renewalSupported:false;credentialChainStatus:'blocked'|'unknown'|'current';blockers:Blocker[];renewalBlockers:Blocker[];ancestorPath:PathEntry[];authorization:{gatewayEnabled:boolean;catalogStatus:string;gatewayApplicationStatus:string;principalStatus:string;principalAuthorizationExpiresAt?:string};boundary:string}
const controller=new AbortController()
const data=ref<CredentialStatus>(),loading=ref(false),error=ref('')
const chainLabels={current:'当前凭据链未发现阻断',blocked:'发现授权阻断条件',unknown:'部分凭据事实待核对'}
const credentialLabels:Record<string,string>={active:'读取时有效',expired:'已到期',revoked:'已明确撤销',disabled_unknown:'已停用，原因待核对',unknown:'尚不能确认'}
const bindingLabels:Record<string,string>={current:'当前修订一致',disabled:'绑定已停用',changed:'绑定修订已变化',unknown:'信任绑定待核对',not_applicable:'静态应用凭据'}
const authorizationLabels:Record<string,string>={current:'当前一致',changed:'采用版本已变化',blocked:'存在阻断',unknown:'尚不能确认'}
let generation=0
async function load(){
  const sequence=++generation,id=props.taskId
  loading.value=true;error.value=''
  try{
    const result=await request<CredentialStatus>(workspacePath(props.workspace.slug)+'/ai-gateway/tasks/'+encodeURIComponent(id)+'/credential-status',{signal:controller.signal})
    if(!controller.signal.aborted && sequence===generation)data.value=result
  }catch(cause){if(controller.signal.aborted || sequence!==generation)return;if(cause instanceof ApiError && cause.status===401)emit('sessionExpired');error.value=describeError(cause)}
  finally{if(sequence===generation)loading.value=false}
}
watch(()=>props.taskId,()=>{data.value=undefined;void load()},{immediate:true})
onBeforeUnmount(()=>{generation++;controller.abort()})
</script>

<template>
  <section class="task-credential-status" :aria-busy="loading" aria-label="任务凭据与祖先授权核对">
    <div class="ai-security-heading"><div><h3>任务凭据与祖先授权</h3><p>逐层核对到期、明确撤销和可信绑定变化，保留原有任务范围及预算。</p></div><button type="button" class="button button-secondary" :disabled="loading" @click="load">{{ loading?'正在核对…':'重新核对凭据链' }}</button></div>
    <p v-if="error" class="message message-error" role="alert">{{ error }}{{ data?' 下方保留上次成功读取的状态，不能作为当前执行依据。':'' }}</p>
    <p v-if="loading && !data" class="ai-security-body" role="status">正在读取当前任务与全部祖先的凭据事实…</p>
    <template v-if="data">
      <div class="task-credential-summary"><span class="badge" :class="data.credentialChainStatus==='blocked'?'status-needs-work':data.credentialChainStatus==='current'?'status-verified':'status-draft'">{{ chainLabels[data.credentialChainStatus] || data.credentialChainStatus }}</span><span>核对于 {{ formatDate(data.observedAt) }}</span><p>此结果仅检查已列示的授权门槛。实际调用仍需核对模型、资料、工具、剩余额度及独立批准。</p><ul v-if="data.blockers?.length"><li v-for="(blocker,index) in data.blockers" :key="blocker.code+':'+index">{{ blocker.state==='unknown'?'待核对：':'阻断：' }}{{ blocker.message }}<span v-if="blocker.taskId" class="table-secondary">任务 {{ blocker.taskId }}</span></li></ul></div>
      <div class="table-scroll" role="region" aria-label="任务及祖先凭据路径" tabindex="0"><table class="data-table"><thead><tr><th scope="col">任务与期限</th><th scope="col">绑定凭据</th><th scope="col">可信身份修订</th><th scope="col">当前限制</th></tr></thead><tbody><tr v-for="item in data.ancestorPath" :key="item.taskId"><th scope="row"><button v-if="item.taskId!==taskId" class="task-credential-link" type="button" @click="emit('selectTask',item.taskId)">{{ item.taskId }}</button><strong v-else class="mono">{{ item.taskId }}</strong><span class="table-secondary">{{ item.depth?'第 '+item.depth+' 层委托':'根任务' }} · 修订 {{ item.taskRevision }}</span><span class="table-secondary">任务期限 {{ formatDate(item.taskExpiresAt) }}</span></th><td>#{{ item.credential.id }} · {{ item.credential.name }}<span class="table-secondary mono">{{ item.credential.prefix }}</span><span class="table-secondary">{{ credentialLabels[item.credentialState] || item.credentialState }}</span><span v-if="item.credential.expiresAt" class="table-secondary">凭据到期 {{ formatDate(item.credential.expiresAt) }}</span><span v-if="item.credential.revokedAt" class="table-secondary">{{ gatewayRevocationLabel(item.credential.revocationReason) }} · {{ formatDate(item.credential.revokedAt) }}</span></td><td>{{ bindingLabels[item.bindingStatus] || item.bindingStatus }}<span v-if="item.credential.workloadBindingId" class="table-secondary">绑定 #{{ item.credential.workloadBindingId }}</span><span v-if="item.credential.workloadBindingId" class="table-secondary">签发修订 {{ item.credential.workloadBindingRevision ?? '历史未记录' }} · 当前修订 {{ item.currentBindingRevision ?? '未知' }}</span></td><td><ul v-if="item.blockers?.length"><li v-for="(blocker,index) in item.blockers" :key="blocker.code+':'+index">{{ blocker.message }}</li></ul><span v-else>本层未列示阻断，仍受当前运行时检查。</span></td></tr></tbody></table></div>
      <div class="ai-security-body"><dl class="ai-security-facts"><div><dt>网关开关</dt><dd>{{ data.authorization.gatewayEnabled?'已启用':'已停用' }}</dd></div><div><dt>目录采用版本</dt><dd>{{ authorizationLabels[data.authorization.catalogStatus] || data.authorization.catalogStatus }}</dd></div><div><dt>应用授权</dt><dd>{{ authorizationLabels[data.authorization.gatewayApplicationStatus] || data.authorization.gatewayApplicationStatus }}</dd></div><div><dt>主体授权</dt><dd>{{ authorizationLabels[data.authorization.principalStatus] || data.authorization.principalStatus }}<span v-if="data.authorization.principalAuthorizationExpiresAt"> · {{ formatDate(data.authorization.principalAuthorizationExpiresAt) }}</span></dd></div></dl><p class="task-credential-boundary">{{ data.boundary }}</p><details v-if="data.renewalBlockers?.length" class="task-credential-renewal"><summary>续期边界与签发事实缺口</summary><ul><li v-for="(issue,index) in data.renewalBlockers" :key="issue.code+':'+index">{{ issue.message }}<span v-if="issue.taskId" class="table-secondary">任务 {{ issue.taskId }}</span></li></ul></details><p v-if="!data.renewalSupported" class="table-secondary">当前任务不支持原地续期或换绑凭据。重新兑换只会签发新凭据；旧任务与全部祖先仍采用各自原凭据，任何一层到期或撤销都会阻断后续执行。</p></div>
    </template>
  </section>
</template>

<style scoped>
.task-credential-status { border-top:1px solid var(--line); min-width:0; }
.task-credential-status .ai-security-heading { align-items:center; }
.task-credential-status h3 { margin:0 0 6px; }
.task-credential-summary { display:flex; flex-wrap:wrap; gap:12px; padding:16px 20px; align-items:center; font-size:14px; }
.task-credential-summary>p,.task-credential-summary>ul { flex-basis:100%; margin:0; }
.task-credential-summary>span:not(.badge) { color:var(--secondary); }
.task-credential-status :is(th,td) { vertical-align:top; overflow-wrap:anywhere; }
.task-credential-status .data-table { min-width:800px; }
.task-credential-status td ul { margin:0; padding-inline-start:18px; }
.task-credential-link { min-height:44px; padding:6px 0; color:var(--accent); border:0; background:transparent; cursor:pointer; font:inherit; text-align:start; overflow-wrap:anywhere; }
.task-credential-renewal { margin-block:16px; overflow-wrap:anywhere; }
.task-credential-renewal summary { cursor:pointer; color:var(--accent); min-height:32px; }
.task-credential-boundary { margin:0 0 12px; overflow-wrap:anywhere; }
</style>
