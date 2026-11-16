<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { ApiError, describeError, downloadFile, formatDate, request, workspacePath, type Workspace } from './api'
import Icon from './Icon.vue'
import SecurityPostureExplorer from './SecurityPostureExplorer.vue'
import './ai-security.css'

const props = defineProps<{ workspace: Workspace; initialTab?:string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
interface Capability { id:string; name:string; stage:string; records:number; href:string; coverage:string; warnings:string[] }
interface Finding { key:string; assetId:number; assetName:string; severity:string; code:string; title:string; detail:string; sourceRef:string; observedAt?:string; evidenceState:string }
interface Binding { id:number; revision:number; site:string; circuit:string; vrf:string; applicationRef:string; assetIds:number[]; activityIds:number[]; region:string; receiver:string; purpose:string; evidenceRef:string; updatedAt:string; evidenceType:string }
interface Change { id:number; bindingId:number; bindingRevision:number; proposedRegion:string; proposedApplicationRef:string; reason:string; affectedAssetIds:number[]; affectedActivityIds:number[]; warnings:string[]; status:string; createdBy:number|string; createdAt:string; reviewer?:number|string; reviewedAt?:string; reviewNote?:string }
interface Snapshot { id:number; digest:string; createdAt:string; findingsCount:number }
interface Platform { generatedAt:string; counts:Record<string,number>; capabilities:Capability[]; findings:Finding[]; networkBindings:Binding[]; networkChanges:Change[]; snapshots:Snapshot[]; truncated:boolean; coverage?:string[] }
const base = workspacePath(props.workspace.slug) + '/security-platform'
const home = '#/' + encodeURIComponent(props.workspace.slug)
const data = ref<Platform | null>(null)
const loading = ref(true), busy = ref(''), error = ref(''), feedback = ref('')
const tab = ref(['coverage','risks','network','changes','snapshots'].includes(props.initialTab || '')?props.initialTab!:'coverage')
const controller = new AbortController()
const canWrite = computed(() => ['owner','operator'].includes(props.workspace.role))
const canReview = computed(() => ['owner','reviewer'].includes(props.workspace.role))
const bindingDraft = reactive({ id:0, revision:0, site:'', circuit:'', vrf:'', applicationRef:'', assetIds:'', activityIds:'', region:'', receiver:'', purpose:'', evidenceRef:'' })
const changeDraft = reactive({ bindingId:0, proposedRegion:'', proposedApplicationRef:'', reason:'' })
const reviewNotes = reactive<Record<number,string>>({})
const selectedBinding = computed(() => data.value?.networkBindings.find(item => item.id === changeDraft.bindingId))
const stageLabels:Record<string,string> = { implemented:'已有功能', needs_source:'等待来源接入', needs_configuration:'等待配置', not_reported:'尚未收到状态' }
const severityLabels:Record<string,string> = { critical:'严重', high:'高', medium:'中', low:'低', info:'提示' }
const evidenceLabels:Record<string,string> = { observed:'已有来源观察', needs_verification:'待核验', registered:'登记事实', unknown:'未知', historical:'历史证据' }
const changeLabels:Record<string,string> = { pending_review:'待独立复核', accepted:'已接受', rejected:'已拒绝' }

function handleError(cause:unknown) {
  if (controller.signal.aborted) return
  if (cause instanceof ApiError && cause.status === 401) emit('sessionExpired')
  error.value = cause instanceof ApiError && cause.status === 409 ? '记录已被其他成员更新，请刷新后核对最新版本再提交。' : describeError(cause)
}
async function load() {
  loading.value = true; error.value = ''
  try { data.value = await request<Platform>(base,{signal:controller.signal}) }
  catch(cause) { handleError(cause) }
  finally { loading.value = false }
}
async function mutate(action:string,path:string,payload:unknown,message:string) {
  if (busy.value) return
  busy.value = action; error.value = ''; feedback.value = ''
  try {
    await request(base+path,{method:'POST',body:JSON.stringify(payload),signal:controller.signal})
    feedback.value = message
    await load()
    return true
  } catch(cause) { handleError(cause) }
  finally { busy.value = '' }
}
function ids(value:string) {
  if (!value.trim()) return []
  const items = value.split(/[\s,，]+/).filter(Boolean)
  if (items.some(item => !/^\d+$/.test(item) || Number(item) <= 0 || !Number.isSafeInteger(Number(item)))) throw new Error('资产和活动编号应为正整数，多个编号用逗号或空格分隔。')
  return [...new Set(items.map(Number))]
}
async function saveBinding() {
  if (!canWrite.value) return
  try {
    const {id,revision,...draft} = bindingDraft
    const saved = await mutate('binding','/network-bindings',{...draft,assetIds:ids(draft.assetIds),activityIds:ids(draft.activityIds),...(id ? {id,expectedRevision:revision}: {})},'网络业务关联已保存为人工登记。')
    if (saved) resetBinding()
  } catch(cause) { error.value = cause instanceof Error ? cause.message : '请核对输入。' }
}
function resetBinding() { Object.assign(bindingDraft,{id:0,revision:0,site:'',circuit:'',vrf:'',applicationRef:'',assetIds:'',activityIds:'',region:'',receiver:'',purpose:'',evidenceRef:''}) }
function editBinding(item:Binding) { Object.assign(bindingDraft,{id:item.id,revision:item.revision,site:item.site,circuit:item.circuit,vrf:item.vrf,applicationRef:item.applicationRef,region:item.region,receiver:item.receiver,purpose:item.purpose,evidenceRef:item.evidenceRef,assetIds:(item.assetIds||[]).join(', '),activityIds:(item.activityIds||[]).join(', ')}) }
async function createChange() {
  if (!canWrite.value || !selectedBinding.value) return
  const saved = await mutate('change','/network-changes',{...changeDraft,expectedRevision:selectedBinding.value.revision},'变更影响已生成，等待独立复核。')
  if (saved) changeDraft.reason = ''
}
async function review(item:Change,decision:'accepted'|'rejected') {
  if (!canReview.value) return
  await mutate('review-'+item.id,`/network-changes/${item.id}/review`,{decision,note:reviewNotes[item.id] || ''},'独立复核意见已记录，网络设备配置未由本操作修改。')
}
async function exportData() {
  busy.value = 'export'; error.value = ''
  try { await downloadFile(base+'/export','scarlet-security-platform.json',controller.signal) }
  catch(cause) { handleError(cause) }
  finally { busy.value = '' }
}
async function downloadSnapshot(id:number) {
  if (busy.value) return
  busy.value='snapshot-download';error.value=''
  try { await downloadFile(base+'/snapshots/'+id,'scarlet-security-snapshot-'+id+'.json',controller.signal) }
  catch(cause) { handleError(cause) }
  finally { busy.value='' }
}
function href(value:string) { return value==='security-platform'?home+'/security-platform?tab=risks':value.startsWith('#/') ? value : home+'/'+value.replace(/^\//,'') }
onMounted(load)
onUnmounted(() => controller.abort())
</script>

<template>
  <section class="ai-security-workspace" :aria-busy="loading || !!busy">
    <header class="page-header"><div><p class="eyebrow">数据与 AI 安全 / 能力与覆盖</p><h1>把数据、AI 与业务连接起来</h1><p>核对八个方向的实际来源、可用功能和待补证据，围绕真实业务关联开展变更复核。</p></div><div class="page-actions"><a class="button button-primary" :href="home+'/ai-security'"><Icon name="shield"/>AI 运行时安全</a><button class="button button-secondary" :disabled="loading || !!busy" @click="load">刷新</button></div></header>
    <p v-if="error" class="message message-error" role="alert">{{ error }}{{ data?' 下方保留上次成功读取的汇总，本次刷新未更新。':'' }}</p>
    <p v-if="feedback" class="message message-success" role="status">{{ feedback }}</p>
    <p v-if="loading && !data" class="message" role="status">正在核对工作区能力与来源…</p><section v-if="!loading && !data" class="panel ai-security-empty"><h2>尚未取得能力与来源汇总</h2><p>当前无法确认资产、AI 来源与业务关联覆盖。重新读取后再判断可用能力和待补证据。</p><button class="button button-secondary" @click="load">重新读取汇总</button></section>
    <template v-if="data">
      <div class="panel ai-security-config-source"><span>汇总时间 {{ formatDate(data.generatedAt) }}</span><span>资产 {{ data.counts.assets ?? 0 }}</span><span>已分类 {{ data.counts.classifiedAssets ?? 0 }}</span><span>活动 {{ data.counts.activities ?? 0 }}</span><span>有效来源 {{ data.counts.activeSources ?? 0 }}</span><span>未关闭发现 {{ data.counts.openFindings ?? 0 }}</span></div>
      <nav class="ai-security-tabs" aria-label="安全能力工作台"><button type="button" :aria-pressed="tab==='coverage'" @click="tab='coverage'">八方向覆盖</button><button type="button" :aria-pressed="tab==='risks'" @click="tab='risks'">数据风险与证据</button><button type="button" :aria-pressed="tab==='network'" @click="tab='network'">网络与业务关联</button><button type="button" :aria-pressed="tab==='changes'" @click="tab='changes'">变更影响与复核</button><button type="button" :aria-pressed="tab==='snapshots'" @click="tab='snapshots'">冻结与导出</button></nav>
      <section v-if="tab==='coverage'" class="panel"><div class="ai-security-heading"><div><h2>八个方向，共用真实数据与证据</h2><p>“已有功能”说明实现入口可用；接入、运行结果和控制效果仍需分别核对。</p></div></div><div v-if="data.capabilities.length" class="table-scroll" role="region" aria-label="八方向功能与覆盖" tabindex="0"><table class="data-table ai-security-coverage"><thead><tr><th scope="col">能力方向</th><th scope="col">当前状态</th><th scope="col">实际范围与缺口</th><th scope="col">进入能力</th></tr></thead><tbody><tr v-for="item in data.capabilities" :key="item.id"><th scope="row">{{ item.name }}<span class="table-secondary">{{ item.records }} 条相关记录</span></th><td><span class="badge" :class="item.stage==='implemented'?'status-verified':'status-draft'">{{ stageLabels[item.stage] || item.stage }}</span></td><td>{{ item.coverage }}<span v-for="warning in item.warnings" :key="warning" class="table-secondary">{{ warning }}</span></td><td><a :href="href(item.href)">查看功能与来源</a></td></tr></tbody></table></div><p v-else class="empty-state">尚未收到能力覆盖状态，请刷新重试。</p></section>
      <SecurityPostureExplorer v-else-if="tab==='risks'" :workspace="workspace" @session-expired="emit('sessionExpired')" />
      <template v-else-if="tab==='network'">
        <section class="panel"><div class="ai-security-heading"><div><h2>站点、线路与业务关联</h2><p>以下关联由授权成员登记，用于定位业务与触发复核；它们不表示线路已被自动观测。</p></div></div><div v-if="data.networkBindings.length" class="table-scroll" role="region" aria-label="网络业务关联台账" tabindex="0"><table class="data-table"><thead><tr><th scope="col">站点与线路</th><th scope="col">应用与数据范围</th><th scope="col">地域与接收方</th><th scope="col">登记依据</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="item in data.networkBindings" :key="item.id"><th scope="row">{{ item.site }}<span class="table-secondary">{{ item.circuit }} · {{ item.vrf || '未填 VRF' }}</span></th><td>{{ item.applicationRef }}<span class="table-secondary">资产 {{ (item.assetIds||[]).join('、') || '未关联' }} · 活动 {{ (item.activityIds||[]).join('、') || '未关联' }}</span></td><td>{{ item.region || '未确认地域' }}<span class="table-secondary">{{ item.receiver || '未确认接收方' }}</span></td><td>人工登记 · 修订 {{ item.revision }}<span class="table-secondary">{{ item.evidenceRef }}</span><span class="table-secondary">{{ formatDate(item.updatedAt) }}</span></td><td><button v-if="canWrite" class="button button-secondary" type="button" @click="editBinding(item)">编辑关联</button><span v-else>只读</span></td></tr></tbody></table></div><p v-else class="empty-state">尚未登记网络业务关联。先选一条承载明确应用的线路建立范围。</p></section>
        <section v-if="canWrite" class="panel"><div class="ai-security-heading"><div><h2>{{ bindingDraft.id ? '编辑业务关联 #'+bindingDraft.id : '登记网络业务关联' }}</h2><p>只关联本工作区的真实资产与活动，地域和接收方应填写客户确认的依据。</p></div></div><form class="ai-security-body" @submit.prevent="saveBinding"><fieldset :disabled="!!busy" class="ai-security-fields"><label class="field">站点名称<input v-model.trim="bindingDraft.site" required maxlength="160"></label><label class="field">线路引用<input v-model.trim="bindingDraft.circuit" required maxlength="160"></label><label class="field">VRF / 隔离域<input v-model.trim="bindingDraft.vrf" maxlength="160"></label><label class="field">应用引用<input v-model.trim="bindingDraft.applicationRef" maxlength="160"></label><label class="field">资产编号<input v-model="bindingDraft.assetIds" placeholder="多个编号用逗号分隔"><small>取自当前工作区资产台账。</small></label><label class="field">处理活动编号<input v-model="bindingDraft.activityIds" placeholder="多个编号用逗号分隔"></label><label class="field">业务处理地域<input v-model.trim="bindingDraft.region" maxlength="160"></label><label class="field">接收方<input v-model.trim="bindingDraft.receiver" maxlength="240"></label><label class="field field-wide">业务用途<textarea v-model.trim="bindingDraft.purpose" required maxlength="2000" rows="2"></textarea></label><label class="field field-wide">依据引用<input v-model.trim="bindingDraft.evidenceRef" required maxlength="500"><small>填写客户确认单、工单或可复核材料引用；IP 所在地不单独证明数据处理位置。</small></label></fieldset><div class="ai-security-actions"><button class="button button-primary" :disabled="!!busy">{{ busy==='binding'?'正在保存…':'保存人工登记' }}</button><button v-if="bindingDraft.id" class="button button-secondary" type="button" :disabled="!!busy" @click="resetBinding">取消编辑</button></div></form></section>
      </template>
      <template v-else-if="tab==='changes'">
        <section v-if="canWrite" class="panel"><div class="ai-security-heading"><div><h2>复核连接变更的数据影响</h2><p>冻结当前关联版本，列出受影响资产、活动和待确认事项。此操作不改变设备或选路。</p></div></div><form class="ai-security-body" @submit.prevent="createChange"><fieldset class="ai-security-fields" :disabled="!!busy"><label class="field field-wide">当前网络业务关联<select v-model.number="changeDraft.bindingId" required><option :value="0" disabled>选择已登记关联</option><option v-for="item in data.networkBindings" :key="item.id" :value="item.id">{{ item.site }} / {{ item.circuit }} / {{ item.applicationRef }} · 修订 {{ item.revision }}</option></select></label><label class="field">拟采用地域<input v-model.trim="changeDraft.proposedRegion" required maxlength="160"></label><label class="field">拟采用应用引用<input v-model.trim="changeDraft.proposedApplicationRef" required maxlength="160"></label><label class="field field-wide">变更原因<textarea v-model.trim="changeDraft.reason" required maxlength="2000" rows="2"></textarea></label></fieldset><div class="ai-security-actions"><button class="button button-primary" :disabled="!!busy || !selectedBinding">{{ busy==='change'?'正在生成…':'生成影响清单' }}</button><a :href="home+'/ai-security'">核对 AI 数据与工具策略</a></div></form></section>
        <section class="panel"><div class="ai-security-heading"><div><h2>变更影响与独立复核记录</h2><p>复核人须独立于变更提交人，接受复核不表示网络配置或 AI 策略已经执行。</p></div></div><p v-if="!data.networkChanges.length" class="empty-state">暂无变更影响记录。</p><article v-for="item in data.networkChanges" :key="item.id" class="ai-security-body"><h3>变更 #{{ item.id }} <span class="badge">{{ changeLabels[item.status] || item.status }}</span></h3><dl class="ai-security-facts"><div><dt>采用关联</dt><dd>#{{ item.bindingId }} · 修订 {{ item.bindingRevision }}</dd></div><div><dt>拟采用地域</dt><dd>{{ item.proposedRegion || '未填写' }}</dd></div><div><dt>拟采用应用</dt><dd>{{ item.proposedApplicationRef || '未填写' }}</dd></div><div><dt>提交时间</dt><dd>{{ formatDate(item.createdAt) }}</dd></div><div><dt>受影响资产</dt><dd>{{ (item.affectedAssetIds||[]).join('、') || '未关联' }}</dd></div><div><dt>受影响活动</dt><dd>{{ (item.affectedActivityIds||[]).join('、') || '未关联' }}</dd></div></dl><p>{{ item.reason }}</p><ul v-if="item.warnings?.length"><li v-for="warning in item.warnings" :key="warning">{{ warning }}</li></ul><form v-if="item.status==='pending_review' && canReview" @submit.prevent="review(item,'accepted')"><label class="field">独立复核意见<textarea v-model.trim="reviewNotes[item.id]" required rows="2" maxlength="2000"></textarea></label><div class="ai-security-actions"><button class="button button-primary" :disabled="!!busy">接受并记录复核</button><button class="button button-secondary" type="button" :disabled="!!busy || !reviewNotes[item.id]?.trim()" @click="review(item,'rejected')">拒绝并记录原因</button></div></form><p v-else-if="item.reviewedAt" class="table-secondary">独立复核人 #{{ item.reviewer }} · {{ formatDate(item.reviewedAt) }}<span v-if="item.reviewNote"> · {{ item.reviewNote }}</span></p></article></section>
      </template>
      <section v-else-if="tab==='snapshots'" class="panel"><div class="ai-security-heading"><div><h2>冻结当前来源与风险证据</h2><p>快照保存本次汇总的范围及发现，之后的变化不会覆盖历史。冻结不替代来源复测。</p></div><div class="page-actions"><button v-if="canWrite" class="button button-primary" :disabled="!!busy || loading" @click="mutate('snapshot','/snapshots',{},'当前汇总证据已冻结。')">{{ busy==='snapshot'?'正在冻结…':'冻结当前证据' }}</button><button class="button button-secondary" :disabled="!!busy" @click="exportData">导出工作区证据</button></div></div><div v-if="data.snapshots.length" class="table-scroll" role="region" aria-label="安全平台冻结快照" tabindex="0"><table class="data-table"><thead><tr><th scope="col">快照</th><th scope="col">冻结时间</th><th scope="col">发现数</th><th scope="col">摘要</th><th scope="col">冻结内容</th></tr></thead><tbody><tr v-for="item in data.snapshots" :key="item.id"><th scope="row">#{{ item.id }}</th><td>{{ formatDate(item.createdAt) }}</td><td>{{ item.findingsCount }}</td><td class="mono">{{ item.digest }}</td><td><button class="button button-secondary" :disabled="!!busy" @click="downloadSnapshot(item.id)">下载此快照</button></td></tr></tbody></table></div><p v-else class="empty-state">暂无冻结快照。可在核对来源与范围后冻结本次证据。</p></section>
      <details v-if="data.coverage?.length" class="panel ai-security-body"><summary>本次汇总的来源与覆盖说明</summary><ul><li v-for="item in data.coverage" :key="item">{{ item }}</li></ul></details>
    </template>
  </section>
</template>
