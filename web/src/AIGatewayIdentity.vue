<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { gatewayIdentityDraft, gatewayIdentityPayload, gatewayPublicURLs, gatewayRoleLabels, type GatewayGroupRole, type GatewayIdentity, type GatewayIdentityDraft, type GatewayIdentityUser } from './ai-gateway'

const props=defineProps<{workspace:Workspace}>()
const emit=defineEmits<{sessionExpired:[]}>()
const base=workspacePath(props.workspace.slug)+'/ai-gateway/identity'
const canManage=computed(()=>props.workspace.role==='owner')
const controller=new AbortController()
const identity=ref<GatewayIdentity|null>(null),users=ref<GatewayIdentityUser[]>([])
interface UserPage {items:GatewayIdentityUser[];limit:number;hasMore:boolean;nextCursor?:string;accountCount:number;matchingCount:number}
const accountCount=ref(0),matchingCount=ref(0),usersHasMore=ref(false),usersNext=ref(''),usersCursor=ref(''),usersPrevious=ref<string[]>([]),usersLoading=ref(false)
const userFilters=reactive({q:'',active:'',group:''}),appliedUserFilters=ref({...userFilters})
let usersSequence=0
const loading=ref(true),busy=ref(''),error=ref(''),usersError=ref(''),feedback=ref('')
const draft=reactive<GatewayIdentityDraft>({revision:0,enabled:false,issuer:'',clientId:'',clientSecretRef:'',scimEnabled:false,defaultRole:'viewer'})
const groups=ref<GatewayGroupRole[]>([])
const savedSnapshot=ref('')
const dirty=computed(()=>JSON.stringify({draft,groups:groups.value})!==savedSnapshot.value)
const configured=computed(()=>Boolean(draft.enabled || draft.scimEnabled || draft.issuer || draft.clientId || draft.clientSecretRef))
const token=ref(''),showToken=ref(false)
let clearTimer:ReturnType<typeof setTimeout>|undefined
const addresses=gatewayPublicURLs(window.location.origin,props.workspace.slug)
function clearToken(){token.value='';showToken.value=false;if(clearTimer)clearTimeout(clearTimer);clearTimer=undefined}
function handleError(cause:unknown){if(controller.signal.aborted)return;if(cause instanceof ApiError && cause.status===401)emit('sessionExpired');error.value=cause instanceof ApiError && cause.status===409?cause.message+' 当前草稿已保留，请重新载入后核对修订。':describeError(cause)}
async function loadUsers(cursor='',trail:string[]=[],filters={...appliedUserFilters.value}) {
  const sequence=++usersSequence
  usersLoading.value=true;usersError.value=''
  const query=new URLSearchParams({limit:'50',...filters})
  if(cursor)query.set('cursor',cursor)
  try {
    const page=await request<UserPage>(base+'/users?'+query,{signal:controller.signal})
    if(sequence!==usersSequence)return
    users.value=page.items;accountCount.value=page.accountCount;matchingCount.value=page.matchingCount;usersHasMore.value=page.hasMore;usersNext.value=page.nextCursor||'';usersCursor.value=cursor;usersPrevious.value=trail;appliedUserFilters.value={...filters}
  } catch(cause) {if(sequence===usersSequence&&!controller.signal.aborted){usersError.value=describeError(cause);if(cause instanceof ApiError&&cause.status===401)emit('sessionExpired')}}
  finally {if(sequence===usersSequence)usersLoading.value=false}
}
async function load(){
  if(!canManage.value){loading.value=false;return}
  loading.value=true;error.value='';usersError.value=''
  const results=await Promise.allSettled([request<GatewayIdentity>(base,{signal:controller.signal}),loadUsers()])
  if(controller.signal.aborted)return
  const config=results[0]
  if(config.status==='fulfilled'){identity.value=config.value;Object.assign(draft,gatewayIdentityDraft(config.value));groups.value=Object.entries(config.value.groupRoles || {}).map(([externalId,role])=>({externalId,role}));savedSnapshot.value=JSON.stringify({draft,groups:groups.value})}else handleError(config.reason)
  loading.value=false
}
async function save(){
  if(!canManage.value || !identity.value || busy.value)return
  let payload:ReturnType<typeof gatewayIdentityPayload>
  try{payload=gatewayIdentityPayload(draft,groups.value)}catch(cause){error.value=cause instanceof Error?cause.message:'请核对身份配置。';return}
  busy.value='save';error.value='';feedback.value=''
  try{await request(base,{method:'PUT',body:JSON.stringify(payload),signal:controller.signal});await load();feedback.value='企业身份配置已保存；请继续验证实际登录及开通、停用行为。'}catch(cause){handleError(cause)}finally{busy.value=''}
}
async function issueToken(){
  if(!canManage.value || !identity.value?.scimEnabled || busy.value || token.value || dirty.value)return
  busy.value='token';error.value='';feedback.value=''
  try{const result=await request<{token:string}>(base+'/scim-token',{method:'POST',body:'{}',signal:controller.signal});if(controller.signal.aborted)return;clearToken();token.value=result.token;clearTimer=setTimeout(clearToken,60_000);identity.value.hasScimToken=true;await load();feedback.value='新 SCIM 令牌已生成，旧令牌立即失效。请在身份提供商更新后验证实际同步。'}catch(cause){handleError(cause)}finally{busy.value=''}
}
async function copy(value:string){try{await navigator.clipboard.writeText(value);feedback.value='已复制。'}catch{error.value='剪贴板不可用，请手动复制当前显示内容。'}}
onMounted(load)
onUnmounted(()=>{clearToken();controller.abort()})
</script>

<template>
  <section v-if="!canManage" class="panel ai-security-empty"><h2>企业身份配置仅负责人可管理</h2><p>当前账号不能读取身份配置、轮换 SCIM 凭证或查看开通账号。</p></section>
  <template v-else>
    <p v-if="error" class="message message-error" role="alert">{{ error }}</p><p v-if="feedback" class="message message-success" role="status">{{ feedback }}</p>
    <p v-if="loading" class="message" role="status">正在读取企业身份配置与开通状态…</p>
    <section v-if="!identity && !loading" class="panel ai-security-empty"><h2>身份管理接口当前不可用</h2><p>尚未取得服务端配置，无法确认登录或 SCIM 接入状态。</p><button class="button button-secondary" @click="load">重新读取</button></section>
    <template v-if="identity">
      <form class="panel" @submit.prevent="save">
        <div class="ai-security-heading"><div><h2>企业 OIDC 与 SCIM</h2><p>配置修订 {{ identity.revision }} · {{ identity.revision && identity.updatedAt?formatDate(identity.updatedAt):'尚未保存配置' }}。配置启用与实际联邦登录成功分别核对。</p></div><button class="button button-secondary" type="button" :disabled="loading || !!busy" @click="load">{{ dirty?'重新载入并丢弃身份草稿':'刷新身份配置与账号' }}</button></div>
        <div class="ai-security-body"><fieldset class="ai-security-fields" :disabled="loading || !!busy">
          <label class="ai-security-toggle"><input v-model="draft.enabled" type="checkbox">启用企业 OIDC 登录<small>关闭后将撤销本工作区联邦账号的会话和成员访问。</small></label>
          <label class="ai-security-toggle"><input v-model="draft.scimEnabled" type="checkbox">启用 SCIM 机器开通入口<small>仅控制机器同步入口；不会代替 OIDC 登录验证。</small></label>
          <label class="field field-wide">OIDC issuer<input v-model.trim="draft.issuer" type="url" maxlength="2048" :required="configured" :readonly="accountCount>0"><small>已有联邦账号后不能更换 issuer 或 client ID，服务端会校验，避免账号重新绑定。</small></label>
          <label class="field">Client ID<input v-model.trim="draft.clientId" maxlength="255" :required="configured" :readonly="accountCount>0"></label>
          <label class="field">客户端密钥环境引用<input v-model.trim="draft.clientSecretRef" maxlength="112" :required="configured" autocomplete="off" spellcheck="false"><small>仅填写 env:SCARLET_IDP_*；服务端从部署环境解析，管理配置不保存或回显密钥正文。</small></label>
          <label class="field">默认账号角色<select v-model="draft.defaultRole"><option v-for="(label,role) in gatewayRoleLabels" :key="role" :value="role">{{ label }}</option></select><small>企业身份和 SCIM 映射不能授予负责人角色。</small></label>
        </fieldset><h3>SCIM 组 externalId 与角色</h3><p class="table-secondary">多个组同时命中时采用经办人、复核人、查看人的优先顺序。</p>
        <div v-for="(group,index) in groups" :key="index" class="ai-gateway-group-row"><label class="field">组 externalId（{{ index+1 }}）<input v-model.trim="group.externalId" required maxlength="255" :disabled="!!busy || loading"></label><label class="field">授权角色<select v-model="group.role" :disabled="!!busy || loading"><option v-for="(label,role) in gatewayRoleLabels" :key="role" :value="role">{{ label }}</option></select></label><button class="button button-secondary" type="button" :disabled="!!busy || loading" @click="groups.splice(index,1)":aria-label="'移除组映射 '+(group.externalId || String(index+1))">移除此组映射</button></div>
        <div class="ai-security-actions"><button class="button button-secondary" type="button" :disabled="!!busy || loading || groups.length>=64" @click="groups.push({externalId:'',role:'viewer'})">添加组映射</button><button class="button button-primary" :disabled="loading || !!busy || !dirty">{{ busy==='save'?'正在保存…':'保存身份配置' }}</button></div></div>
      </form>
      <section class="panel"><div class="ai-security-heading"><div><h2>接入地址与一次性凭证</h2><p>只允许已由 SCIM 开通且 externalId 与 OIDC sub 一致的账号，不按邮箱自动链接本地账号。</p></div></div><div class="ai-security-body">
        <div v-for="(value,key) in addresses" :key="key" class="ai-gateway-address"><label class="field">{{ {login:'企业登录地址',callback:'固定回调地址',scim:'SCIM 服务地址'}[key] }}<input :value="value" readonly></label><button class="button button-secondary" @click="copy(value)">复制地址</button></div>
        <p>服务端 SCIM 令牌状态：{{ identity.hasScimToken?'已设置（秘密值不回显）':'尚未设置' }}；SCIM 入口{{ identity.scimEnabled?'已启用':'未启用' }}。</p>
        <div class="ai-security-actions"><button class="button button-secondary" :disabled="!!busy || !!token || !identity.scimEnabled || dirty" @click="issueToken">{{ busy==='token'?'正在生成…':identity.hasScimToken?'轮换 SCIM 令牌':'生成 SCIM 令牌' }}</button><span>{{ dirty?'先保存身份草稿，再生成或轮换令牌。':'轮换后旧令牌立即失效，需要更新身份提供商。' }}</span></div>
        <div v-if="token" class="ai-security-note"><strong>新令牌仅本次响应返回</strong><p>默认遮挡，60 秒后或离开页签时清除显示，不表示令牌自动过期。</p><label class="field">SCIM Bearer 令牌<input :value="token" :type="showToken?'text':'password'" readonly autocomplete="new-password" spellcheck="false"></label><div class="ai-security-actions"><button class="button button-secondary" @click="copy(token)">复制新令牌</button><button class="button button-secondary" @click="showToken=!showToken">{{ showToken?'遮挡':'临时显示' }}</button><button class="button button-secondary" @click="clearToken">清除此页令牌</button></div></div>
      </div></section>
      <section class="panel"><div class="ai-security-heading"><div><h2>实际开通的联邦账号</h2><p>来自工作区身份管理接口；开通记录不代表已完成一次真实 OIDC 登录。</p></div></div><p v-if="usersError" class="message message-error" role="alert">{{ usersError }}{{ users.length?' 下方为上次载入记录，本次未能刷新。':'' }}</p><form class="filter-bar" @submit.prevent="loadUsers('',[],{...userFilters})"><label class="field">账号、名称或外部主体<input v-model.trim="userFilters.q" maxlength="120"></label><label class="field">开通状态<select v-model="userFilters.active"><option value="">全部状态</option><option value="true">已开通</option><option value="false">已停用</option></select></label><label class="field">组 externalId<input v-model.trim="userFilters.group" maxlength="255" placeholder="精确匹配"></label><button class="button button-secondary" :disabled="usersLoading">{{ usersLoading?'读取中…':'查询账号' }}</button></form><p class="ai-security-body" role="status">工作区共 {{ accountCount }} 个联邦账号，当前条件匹配 {{ matchingCount }} 个；本页 {{ users.length }} 个。每页读取时的开通状态分别核对。</p><div v-if="users.length" class="table-scroll" role="region" aria-label="联邦账号状态" tabindex="0"><table class="data-table"><thead><tr><th scope="col">账号</th><th scope="col">外部主体</th><th scope="col">状态与角色</th><th scope="col">组</th><th scope="col">更新时间</th></tr></thead><tbody><tr v-for="user in users" :key="user.id"><th scope="row">{{ user.displayName || user.userName }}<span class="table-secondary">{{ user.userName }}</span></th><td>{{ user.externalId }}</td><td>{{ user.active?'已开通':'已停用' }} · {{ gatewayRoleLabels[user.role as keyof typeof gatewayRoleLabels] || user.role || '无当前授权' }}</td><td>{{ user.groups.join('、') || '无组声明' }}</td><td>{{ formatDate(user.updatedAt) }}</td></tr></tbody></table></div><p v-else-if="!usersError && !usersLoading" class="empty-state">当前条件没有匹配的联邦账号。</p><div class="ai-security-actions ai-security-body"><button type="button" class="button button-secondary" :disabled="usersLoading||!usersPrevious.length" @click="loadUsers(usersPrevious[usersPrevious.length-1]||'',usersPrevious.slice(0,-1))">上一页</button><button type="button" class="button button-secondary" :disabled="usersLoading||!usersHasMore||!usersNext" @click="loadUsers(usersNext,[...usersPrevious,usersCursor])">下一页</button><button type="button" class="button button-secondary" :disabled="usersLoading" @click="loadUsers(usersCursor,[...usersPrevious])">重试或刷新本页账号</button></div></section>
    </template>
  </template>
</template>
