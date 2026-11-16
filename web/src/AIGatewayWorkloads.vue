<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import type { GatewayConfiguration } from './ai-gateway'
import { workloadDraft, workloadExchangeExample, workloadPayload, workloadPublicKeys, type GatewayWorkloadBinding, type GatewayWorkloadPage } from './ai-gateway-workloads'

const props=defineProps<{workspace:Workspace;configuration:GatewayConfiguration}>()
const emit=defineEmits<{sessionExpired:[];refreshCredentials:[]}>()
const base=workspacePath(props.workspace.slug)+'/ai-gateway/workloads'
const controller=new AbortController()
const owner=computed(()=>props.workspace.role==='owner')
const page=ref<GatewayWorkloadPage|null>(null),before=ref(''),history=ref<string[]>([])
const loading=ref(false),busy=ref(''),error=ref(''),feedback=ref(''),conflict=ref(false)
const selected=ref<GatewayWorkloadBinding|null>(null),editing=ref(false),draft=ref(workloadDraft()),snapshot=ref('')
const dirty=computed(()=>editing.value && JSON.stringify(draft.value)!==snapshot.value)
const editor=ref<HTMLElement|null>(null),fileInput=ref<HTMLInputElement|null>(null),importing=ref(false)
const origin=window.location.origin
let generation=0,editorVersion=0
function fail(cause:unknown){if(controller.signal.aborted)return;if(cause instanceof ApiError && cause.status===401)emit('sessionExpired');error.value=describeError(cause);if(cause instanceof ApiError && cause.status===409){conflict.value=true;error.value+=' 未保存的输入已保留，请重新读取绑定并核对修订，不会自动覆盖。'}}
async function load(cursor=before.value,remember=false){
  const version=++generation;loading.value=true;error.value=''
  try{const next=await request<GatewayWorkloadPage>(base+'?limit=50'+(cursor?'&before='+encodeURIComponent(cursor):''),{signal:controller.signal});if(controller.signal.aborted || version!==generation)return;if(remember)history.value.push(before.value);else if(!cursor)history.value=[];page.value={...next,items:next.items || []};before.value=cursor;if(selected.value && !editing.value){const current=next.items.find(item=>item.id===selected.value?.id);if(current)selected.value=current}return true}catch(cause){if(version===generation)fail(cause)}finally{if(version===generation)loading.value=false}
}
async function previous(){const cursor=history.value.at(-1);if(cursor!==undefined && await load(cursor))history.value.pop()}
function choose(binding:GatewayWorkloadBinding){if(dirty.value || busy.value)return;editorVersion++;selected.value=binding;editing.value=false;error.value='';conflict.value=false}
async function edit(binding?:GatewayWorkloadBinding){if(!owner.value || dirty.value || busy.value)return;editorVersion++;selected.value=binding || null;draft.value=workloadDraft(binding);snapshot.value=JSON.stringify(draft.value);editing.value=true;conflict.value=false;error.value='';feedback.value='';await nextTick();editor.value?.scrollIntoView({block:'nearest'});editor.value?.querySelector<HTMLInputElement>('input')?.focus({preventScroll:true})}
function cancel(){editorVersion++;editing.value=false;error.value='';conflict.value=false}
async function importKeys(event:Event){
  const file=(event.target as HTMLInputElement).files?.[0];if(!file)return
  const version=editorVersion
  importing.value=true;error.value=''
  try{if(file.size>16384)throw new Error('仅导入最多 16 KiB 的公钥 JWKS 文件。');const value=await file.text();workloadPublicKeys(value);if(controller.signal.aborted || !editing.value || version!==editorVersion)return;draft.value.jwksJSON=value;feedback.value='公钥已载入待保存草稿，签名与密钥强度仍由服务端校验。'}catch(cause){fail(cause)}finally{importing.value=false;if(fileInput.value)fileInput.value.value=''}
}
async function save(){
  if(!owner.value || busy.value || conflict.value || importing.value)return
  let payload:ReturnType<typeof workloadPayload>
  try{payload=workloadPayload(draft.value)}catch(cause){error.value=cause instanceof Error?cause.message:'请核对工作负载绑定。';return}
  busy.value='save';error.value='';feedback.value=''
  try{const binding=await request<GatewayWorkloadBinding>(base+(draft.value.id?'/'+draft.value.id:''),{method:draft.value.id?'PUT':'POST',body:JSON.stringify(payload),signal:controller.signal});selected.value=binding;editing.value=false;feedback.value='工作负载绑定已保存。修改会立即使该绑定先前兑换的凭据失效，请用新签名和唯一 jti 重新兑换。';emit('refreshCredentials');await load('')}catch(cause){fail(cause)}finally{busy.value=''}
}
async function revoke(binding:GatewayWorkloadBinding){
  if(!owner.value || busy.value || !binding.enabled || dirty.value)return
  busy.value='revoke';error.value='';feedback.value=''
  try{selected.value=await request<GatewayWorkloadBinding>(base+'/'+binding.id+'/revoke',{method:'POST',body:JSON.stringify({expectedRevision:binding.revision}),signal:controller.signal});feedback.value='工作负载绑定已撤销，先前兑换的凭据立即失效；历史调用证据保留。';emit('refreshCredentials');await load()}catch(cause){fail(cause)}finally{busy.value=''}
}
function expiry(binding:GatewayWorkloadBinding){return '不超过 '+binding.maxTtlSeconds+' 秒，且不长于 JWT 剩余有效期'}
onMounted(()=>{void load('')})
onUnmounted(()=>controller.abort())
</script>

<template>
  <section class="panel ai-workloads" :aria-busy="loading || !!busy">
    <div class="ai-security-heading"><div><h2>工作负载身份与短期凭据</h2><p>以受信任公钥验证精确 issuer、audience 与 subject，将机器身份绑定到已保存应用。签名证明继续受任务、文档 ACL、数据策略和额度约束。</p></div><div class="page-actions"><button class="button button-secondary" :disabled="loading || !!busy" @click="load()">刷新绑定列表</button><button v-if="owner" class="button button-secondary" :disabled="!!busy || dirty" @click="edit()">新建身份绑定</button></div></div>
    <p v-if="error" class="message message-error" role="alert">{{ error }}</p><p v-if="feedback" class="message message-success" role="status">{{ feedback }}</p><p v-if="loading" class="ai-security-body" role="status">正在读取工作负载身份绑定…</p><p v-if="dirty" class="ai-security-body message message-warning" role="status">有未保存的身份绑定草稿。保存或取消编辑后可切换记录。</p>
    <div v-if="page?.items.length" class="table-scroll" role="region" aria-label="工作负载身份绑定" tabindex="0"><table class="data-table"><thead><tr><th scope="col">绑定与应用</th><th scope="col">可信身份</th><th scope="col">公钥与期限</th><th scope="col">状态与修订</th></tr></thead><tbody><tr v-for="binding in page.items" :key="binding.id"><th scope="row"><button class="ai-workload-link" :aria-pressed="selected?.id===binding.id" :disabled="dirty || !!busy" @click="choose(binding)">{{ binding.name }}</button><span class="table-secondary">#{{ binding.id }} · {{ binding.applicationId }}</span></th><td><span class="table-secondary">iss {{ binding.issuer }}</span><span class="table-secondary">aud {{ binding.audience }}</span><span class="table-secondary">sub {{ binding.subject }}</span></td><td>{{ binding.jwks.keys.length }} 个受信任公钥<span class="table-secondary">最长 {{ binding.maxTtlSeconds }} 秒</span></td><td>{{ binding.enabled?'已启用绑定':'已撤销绑定' }}<span class="table-secondary">修订 {{ binding.revision }} · {{ formatDate(binding.updatedAt) }}</span></td></tr></tbody></table></div>
    <div v-else-if="page && !loading" class="ai-security-empty"><h3>当前页没有工作负载绑定</h3><p>先取得工作负载身份提供方的真实公钥与精确声明范围。只登记公钥不表示已经验证过实际机器调用。</p></div>
    <div v-if="page" class="ai-security-pagination"><button class="button button-secondary" :disabled="loading || !history.length" @click="previous">较新一页</button><button class="button button-secondary" :disabled="loading || !page.hasMore || !page.nextCursor" @click="load(page.nextCursor,true)">较早一页</button><p>第 {{ history.length+1 }} 页 · 本页 {{ page.items.length }} 条</p></div>
    <div v-if="selected && !editing" class="ai-security-body"><div class="ai-security-heading"><div><h3>{{ selected.name }} · 精确身份范围</h3><p>修订 {{ selected.revision }} · {{ expiry(selected) }}</p></div><div v-if="owner" class="page-actions"><button class="button button-secondary" :disabled="!!busy" @click="edit(selected)">编辑并轮换可信公钥</button><button v-if="selected.enabled" class="button button-secondary" :disabled="!!busy" @click="revoke(selected)">撤销绑定及已兑换凭据</button></div></div><dl class="ai-security-facts"><div><dt>应用</dt><dd>{{ selected.applicationId }}</dd></div><div><dt>签发者 issuer</dt><dd>{{ selected.issuer }}</dd></div><div><dt>接收方 audience</dt><dd>{{ selected.audience }}</dd></div><div><dt>工作负载 subject</dt><dd>{{ selected.subject }}</dd></div></dl><details class="ai-security-evidence-json"><summary>核对已保存的公钥 JWKS</summary><pre class="ai-security-code">{{ JSON.stringify(selected.jwks,null,2) }}</pre></details><details class="ai-security-manual"><summary>机器如何兑换一次性返回的短期凭据</summary><p>由工作负载持有的私钥签发 JWT，精确匹配本绑定的 iss / aud / sub，并包含 iat、exp 和唯一 jti。签名头 typ 采用 JWT 或 at+jwt，alg / kid 与已导入公钥精确一致；aud 为一个字符串或仅含一个值的数组。JWT 有效期不超过 300 秒，iat 最多允许 30 秒未来偏差。jti 只能兑换一次；重复请求返回冲突，应核对第一次结果并使用新 jti，不能假设旧凭据未签发。</p><pre class="ai-security-code">{{ workloadExchangeExample(workspace.slug,selected.id,origin) }}</pre><p>成功响应仅当次返回 credential、token 与 expiresAt。机器应在受限内存中采用凭据，实际过期后重新签发和兑换。平台不保存原始 JWT，也不会回显旧令牌。绑定更新或撤销后已兑换凭据立即失效；任务停止不会回滚已发生的副作用。</p></details></div>
    <form v-if="editing && owner" ref="editor" class="ai-security-body" @submit.prevent="save"><h3>{{ draft.id?'更新身份绑定 #'+draft.id:'新建工作负载身份绑定' }}</h3><p class="table-secondary">仅导入可信公钥。私钥留在工作负载的身份提供方，不上传平台。</p><fieldset class="ai-security-fields" :disabled="!!busy || importing"><legend class="sr-only">工作负载身份绑定</legend><label class="field">绑定名称<input v-model.trim="draft.name" required maxlength="128"></label><label class="field">已保存应用<select v-model="draft.applicationId" required><option value="">选择已保存应用</option><option v-for="app in configuration.applications" :key="app.id" :value="app.id">{{ app.name }} · {{ app.id }}{{ app.enabled?'':'（应用未启用）' }}</option></select><small>绑定不能扩大应用原有主体、模型或工具授权。</small></label><label class="field field-wide">精确 issuer<input v-model.trim="draft.issuer" required maxlength="512" spellcheck="false"></label><label class="field">精确 audience<input v-model.trim="draft.audience" required maxlength="512" spellcheck="false"></label><label class="field">精确 subject<input v-model.trim="draft.subject" required maxlength="255" spellcheck="false"></label><label class="field">最长凭据有效期（秒）<input v-model.number="draft.maxTtlSeconds" type="number" required min="1" max="300" step="1"></label><label v-if="draft.id" class="ai-security-toggle"><input v-model="draft.enabled" type="checkbox">启用此身份绑定<small>任何更新立即使先前兑换的凭据失效。</small></label><label class="field field-wide">可信公钥 JWKS JSON<textarea v-model="draft.jwksJSON" required rows="10" maxlength="16384" spellcheck="false" class="mono"></textarea><small>1—8 个唯一 kid，use=sig。支持 RS256（RSA 2048—4096 位、e=65537）、ES256（P-256）与 EdDSA（Ed25519）；key_ops 仅可省略或为 ["verify"]。拒绝私钥、对称密钥、远程地址与动态取钥。</small></label><label class="field field-wide">从本地公钥文件导入<input ref="fileInput" type="file" accept="application/json,.json,.jwks" @change="importKeys"><small>只在浏览器读取并验证公钥结构；点击保存后才发送到服务端。</small></label></fieldset><div class="ai-security-actions"><button class="button button-primary" :disabled="!!busy || importing || conflict || !dirty">{{ busy==='save'?'正在保存…':draft.id?'保存新修订并使旧凭据失效':'保存可信身份绑定' }}</button><button class="button button-secondary" type="button" :disabled="!!busy || importing" @click="cancel">{{ dirty?'取消并丢弃草稿':'关闭编辑' }}</button><p v-if="conflict">草稿仍保留。取消编辑后刷新绑定，采用最新修订重新核对；保存不会自动重试。</p></div></form>
  </section>
</template>

<style scoped>
.ai-workload-link { min-height:44px; padding:8px 0; border:0; background:none; color:var(--accent); font:inherit; text-align:left; cursor:pointer; overflow-wrap:anywhere; }
.ai-workload-link[aria-pressed=true] { text-decoration:underline; text-underline-offset:4px; }
.ai-workloads > .ai-security-body { border-top:1px solid var(--line); }
.ai-workloads .ai-security-body > .ai-security-heading { padding:0 0 16px; margin-bottom:16px; }
.ai-workloads .ai-security-body > h3 { margin-top:0; }
</style>
