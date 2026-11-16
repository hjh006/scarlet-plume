<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { ApiError, describeError, downloadFile, formatDate, request, workspacePath, type Workspace } from './api'
import { networkHistoryQuery, networkRecordId, networkScopeIds, type NetworkBinding, type NetworkChange, type NetworkHistoryFilters, type NetworkHistoryKind, type NetworkHistoryPage, type NetworkSnapshot } from './security-network'

const props = defineProps<{ workspace:Workspace; tab:'network'|'changes'|'snapshots' }>()
const emit = defineEmits<{ sessionExpired:[]; changed:[]; selectTab:[tab:'network'|'changes'|'snapshots'] }>()
const base = workspacePath(props.workspace.slug)+'/security-platform'
const home = '#/'+encodeURIComponent(props.workspace.slug)
const lifetime = new AbortController()
const canWrite = computed(()=>['owner','operator'].includes(props.workspace.role))
const canReview = computed(()=>['owner','reviewer'].includes(props.workspace.role))
const kind = computed<NetworkHistoryKind>(()=>props.tab==='network'?'network-bindings':props.tab==='changes'?'network-changes':'snapshots')
type Row = NetworkBinding | NetworkChange | NetworkSnapshot
interface PageState { data?:NetworkHistoryPage<Row>; cursor:string; history:string[]; applied:NetworkHistoryFilters; loading:boolean; error:string; sequence:number }
const emptyFilters = ():NetworkHistoryFilters=>({q:'',status:'',bindingId:'',createdFrom:'',createdTo:''})
const newPage = ():PageState=>({cursor:'',history:[],applied:emptyFilters(),loading:false,error:'',sequence:0})
const pages = reactive<Record<NetworkHistoryKind,PageState>>({'network-bindings':newPage(),'network-changes':newPage(),snapshots:newPage()})
const filters = reactive<Record<NetworkHistoryKind,NetworkHistoryFilters>>({'network-bindings':emptyFilters(),'network-changes':emptyFilters(),snapshots:emptyFilters()})
const current = computed(()=>pages[kind.value])
const bindings = computed(()=>(pages['network-bindings'].data?.items || []) as NetworkBinding[])
const changes = computed(()=>(pages['network-changes'].data?.items || []) as NetworkChange[])
const snapshots = computed(()=>(pages.snapshots.data?.items || []) as NetworkSnapshot[])
const busy = ref(''), error = ref(''), feedback = ref(''), bindingLookup = ref(''), lookupBusy = ref(false)
const selectedBinding = ref<NetworkBinding>(), bindingForm = ref<HTMLFormElement>()
const blankBinding = ()=>({id:0,revision:0,site:'',circuit:'',vrf:'',applicationRef:'',assetIds:'',activityIds:'',region:'',receiver:'',purpose:'',evidenceRef:''})
const bindingDraft = reactive(blankBinding())
const changeDraft = reactive({proposedRegion:'',proposedApplicationRef:'',reason:''})
const reviewNotes = reactive<Record<number,string>>({})
const changeLabels:Record<string,string> = {pending_review:'待独立复核',accepted:'已接受',rejected:'已拒绝'}
let lookupSequence = 0
function message(cause:unknown):string {
  if (cause instanceof ApiError && cause.status===401) emit('sessionExpired')
  return cause instanceof ApiError && cause.status===409?'记录版本已变化，请刷新并核对最新记录；输入草稿已保留。':describeError(cause)
}
async function load(targetKind=kind.value, cursor='', values={...pages[targetKind].applied}, history:string[]=[]) {
  const state = pages[targetKind], sequence = ++state.sequence
  state.loading=true; state.error=''
  let path:string
  try { path=networkHistoryQuery(targetKind,values,cursor) }
  catch(cause) { state.error=cause instanceof Error?cause.message:'请核对筛选条件。';state.loading=false;return }
  try {
    const result = await request<NetworkHistoryPage<Row>>(base+path,{signal:lifetime.signal})
    if (lifetime.signal.aborted || state.sequence!==sequence) return
    state.data={...result,items:result.items || []}; state.cursor=cursor; state.history=history; state.applied={...values}
  } catch (cause) { if (!lifetime.signal.aborted && state.sequence===sequence) state.error=message(cause) }
  finally { if (state.sequence===sequence) state.loading=false }
}
function refresh(){void load(kind.value,'',{...current.value.applied})}
function previous(){const state=current.value;void load(kind.value,state.history.at(-1) || '',{...state.applied},state.history.slice(0,-1))}
function next(){const state=current.value;if(state.data?.nextCursor)void load(kind.value,state.data.nextCursor,{...state.applied},[...state.history,state.cursor])}
async function mutate(action:string,path:string,payload:unknown,success:string,targetKind:NetworkHistoryKind) {
  if (busy.value) return false
  busy.value=action; error.value=''; feedback.value=''
  try {
    await request(base+path,{method:'POST',body:JSON.stringify(payload),signal:lifetime.signal})
    feedback.value=success
    await load(targetKind,'',{...pages[targetKind].applied})
    emit('changed')
    return true
  } catch(cause) { if(!lifetime.signal.aborted)error.value=message(cause);return false }
  finally { busy.value='' }
}
async function saveBinding() {
  if (!canWrite.value || busy.value) return
  try {
    const {id,revision,...draft}=bindingDraft
    const success=await mutate('binding','/network-bindings',{...draft,assetIds:networkScopeIds(draft.assetIds),activityIds:networkScopeIds(draft.activityIds),...(id?{id,expectedRevision:revision}:{})},'网络关联已保存。登记依据仍需结合实际来源核验。','network-bindings')
    if(success) Object.assign(bindingDraft,blankBinding())
  } catch(cause) {error.value=cause instanceof Error?cause.message:'请核对资产和活动编号。'}
}
async function editBinding(item:NetworkBinding) {
  if (busy.value) return
  Object.assign(bindingDraft,{id:item.id,revision:item.revision,site:item.site,circuit:item.circuit,vrf:item.vrf,applicationRef:item.applicationRef,region:item.region,receiver:item.receiver,purpose:item.purpose,evidenceRef:item.evidenceRef,assetIds:(item.assetIds || []).join(', '),activityIds:(item.activityIds || []).join(', ')})
  await nextTick()
  bindingForm.value?.scrollIntoView({block:'start',behavior:window.matchMedia('(prefers-reduced-motion: reduce)').matches?'auto':'smooth'})
  bindingForm.value?.querySelector('input')?.focus({preventScroll:true})
}
function selectForChange(item:NetworkBinding) {
  if (busy.value || lookupBusy.value) return
  bindingLookup.value=String(item.id);selectedBinding.value=item
  changeDraft.proposedRegion=item.region;changeDraft.proposedApplicationRef=item.applicationRef
  emit('selectTab','changes')
}
async function lookupBinding() {
  if (lookupBusy.value || busy.value) return
  const sequence=++lookupSequence, value=bindingLookup.value.trim()
  error.value=''; selectedBinding.value=undefined
  let id:number
  try {id=networkRecordId(value)}
  catch(cause) {error.value=cause instanceof Error?cause.message:'请核对关联编号。';return}
  try {
    lookupBusy.value=true
    const result=await request<NetworkBinding>(base+'/network-bindings/'+id,{signal:lifetime.signal})
    if(lifetime.signal.aborted || sequence!==lookupSequence || bindingLookup.value.trim()!==value)return
    selectedBinding.value=result
    changeDraft.proposedRegion=result.region;changeDraft.proposedApplicationRef=result.applicationRef
  } catch(cause) {if(!lifetime.signal.aborted && sequence===lookupSequence)error.value=message(cause)}
  finally {if(sequence===lookupSequence)lookupBusy.value=false}
}
async function createChange() {
  if (!canWrite.value || !selectedBinding.value || lookupBusy.value || busy.value) return
  if(await mutate('change','/network-changes',{...changeDraft,bindingId:selectedBinding.value.id,expectedRevision:selectedBinding.value.revision},'变更影响已冻结，等待独立复核。','network-changes')) changeDraft.reason=''
}
async function review(item:NetworkChange,decision:'accepted'|'rejected') {
  if (!canReview.value || busy.value) return
  if(await mutate('review-'+item.id,'/network-changes/'+item.id+'/review',{decision,note:reviewNotes[item.id] || ''},'独立复核已记录；请另行核对网络配置与实际执行结果。','network-changes')) delete reviewNotes[item.id]
}
async function download(path:string,name:string) {
  if (busy.value || current.value.loading) return
  busy.value='download';error.value=''
  try {await downloadFile(base+path,name,lifetime.signal)}
  catch(cause) {if(!lifetime.signal.aborted)error.value=message(cause)}
  finally {busy.value=''}
}
watch(bindingLookup,()=>{lookupSequence++;lookupBusy.value=false;selectedBinding.value=undefined},{flush:'sync'})
watch(kind,value=>{error.value='';feedback.value='';if(!pages[value].data && !pages[value].loading)void load(value)},{immediate:true})
onBeforeUnmount(()=>{lifetime.abort();lookupSequence++;for(const state of Object.values(pages))state.sequence++})
</script>

<template>
  <div class="security-network" :aria-busy="current.loading || !!busy">
    <p v-if="error" class="message message-error" role="alert">{{ error }}</p>
    <p v-if="feedback" class="message message-success" role="status">{{ feedback }}</p>
    <section class="panel">
      <div class="ai-security-heading"><div><h2>{{ tab==='network'?'站点、线路与业务关联':tab==='changes'?'变更影响与独立复核记录':'冻结证据与历史导出' }}</h2><p>{{ tab==='network'?'搜索已登记的站点、线路、应用或地域，逐页核对完整关联台账。':tab==='changes'?'按关联与复核状态追踪历史影响，复核不代表线路配置已执行。':'按冻结时间查询历史快照，导出保留原始范围、发现和覆盖缺口。' }}</p></div><div class="page-actions"><button class="button button-secondary" :disabled="current.loading || !!busy" @click="refresh">刷新当前范围</button><button v-if="tab==='snapshots' && canWrite" class="button button-primary" :disabled="current.loading || !!busy" @click="mutate('snapshot','/snapshots',{},'当前汇总已冻结，快照继续保留原有覆盖范围。','snapshots')">{{ busy==='snapshot'?'正在冻结…':'冻结当前汇总' }}</button></div></div>
      <form class="filter-bar network-filters" @submit.prevent="load(kind,'',{...filters[kind]})">
        <label v-if="tab==='network'" class="field">搜索业务关联<input v-model.trim="filters['network-bindings'].q" maxlength="120" placeholder="站点、线路、应用、地域或依据"></label>
        <template v-else-if="tab==='changes'"><label class="field">关联编号<input v-model.trim="filters['network-changes'].bindingId" inputmode="numeric" pattern="[1-9][0-9]*" placeholder="全部关联"></label><label class="field">复核状态<select v-model="filters['network-changes'].status"><option value="">全部状态</option><option v-for="(label,status) in changeLabels" :key="status" :value="status">{{ label }}</option></select></label></template>
        <template v-else><label class="field">冻结时间自（含）<input v-model="filters.snapshots.createdFrom" type="datetime-local" step="1"></label><label class="field">冻结时间至（不含）<input v-model="filters.snapshots.createdTo" type="datetime-local" step="1"></label></template>
        <button class="button button-primary" :disabled="current.loading || !!busy">{{ current.loading?'正在查询…':'查询历史' }}</button>
      </form>
      <p v-if="tab==='snapshots'" class="network-caption">时间按浏览器本机时区输入，提交时转换为带时区的时间。</p>
      <div v-if="current.error" class="message message-error" role="alert"><p>{{ current.error }}{{ current.data?' 下方保留上次成功读取的结果，筛选条件未替换。':'' }}</p><button type="button" class="button button-secondary" :disabled="current.loading" @click="load(kind,current.cursor,{...current.applied},[...current.history])">重试当前页</button></div>
      <p v-if="current.loading && !current.data" class="empty-state" role="status">正在读取{{ tab==='network'?'业务关联':tab==='changes'?'变更影响':'冻结快照' }}…</p>
      <template v-if="current.data">
        <div class="scope-strip" role="status"><span>第 {{ current.history.length+1 }} 页 · 本页 {{ current.data.items.length }} 条</span><span>读取于 {{ formatDate(current.data.generatedAt) }}</span><span v-if="current.applied.q">搜索：{{ current.applied.q }}</span><span v-if="current.applied.status">状态：{{ changeLabels[current.applied.status] }}</span><span v-if="current.applied.bindingId">关联 #{{ current.applied.bindingId }}</span><span v-if="current.applied.createdFrom">自 {{ formatDate(current.applied.createdFrom) }}（含）</span><span v-if="current.applied.createdTo">至 {{ formatDate(current.applied.createdTo) }}（不含）</span></div>
        <div v-if="tab==='network' && bindings.length" class="table-scroll" role="region" aria-label="网络业务关联台账" tabindex="0"><table class="data-table"><thead><tr><th scope="col">站点与线路</th><th scope="col">应用与数据范围</th><th scope="col">地域与接收方</th><th scope="col">登记依据</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="item in bindings" :key="item.id"><th scope="row">{{ item.site }}<span class="table-secondary">#{{ item.id }} · {{ item.circuit }}</span><span class="table-secondary">{{ item.vrf || '未填 VRF' }}</span></th><td>{{ item.applicationRef || '未填应用' }}<span class="table-secondary">资产 {{ (item.assetIds || []).join('、') || '未关联' }} · 活动 {{ (item.activityIds || []).join('、') || '未关联' }}</span></td><td>{{ item.region || '未确认地域' }}<span class="table-secondary">{{ item.receiver || '未确认接收方' }}</span></td><td>人工登记 · 修订 {{ item.revision }}<span class="table-secondary">{{ item.evidenceRef }}</span><span class="table-secondary">{{ formatDate(item.updatedAt) }}</span></td><td><div v-if="canWrite" class="network-row-actions"><button class="button button-secondary" :disabled="!!busy" @click="editBinding(item)">编辑关联</button><button class="button button-secondary" :disabled="!!busy || lookupBusy" @click="selectForChange(item)">复核此关联变更</button></div><span v-else>只读</span></td></tr></tbody></table></div>
        <template v-if="tab==='changes'"><article v-for="item in changes" :key="item.id" class="network-change"><header><h3>变更 #{{ item.id }} <span class="badge" :class="item.status==='accepted'?'status-verified':item.status==='rejected'?'status-needs-work':'status-draft'">{{ changeLabels[item.status] || item.status }}</span></h3><p>{{ item.binding?.site || '已冻结关联' }} · {{ item.binding?.circuit || '#' + item.bindingId }}</p></header><dl class="ai-security-facts"><div><dt>冻结关联</dt><dd>#{{ item.bindingId }} · 修订 {{ item.bindingRevision }}</dd></div><div><dt>地域变更</dt><dd>{{ item.binding?.region || '原地域未填' }} → {{ item.proposedRegion || '未填写' }}</dd></div><div><dt>应用变更</dt><dd>{{ item.binding?.applicationRef || '原应用未填' }} → {{ item.proposedApplicationRef || '未填写' }}</dd></div><div><dt>提交人及时间</dt><dd>#{{ item.createdBy }} · {{ formatDate(item.createdAt) }}</dd></div><div><dt>受影响资产</dt><dd>{{ (item.affectedAssetIds || []).join('、') || '未关联' }}</dd></div><div><dt>受影响活动</dt><dd>{{ (item.affectedActivityIds || []).join('、') || '未关联' }}</dd></div></dl><p class="network-reason">{{ item.reason }}</p><ul v-if="item.warnings?.length" class="network-warnings"><li v-for="warning in item.warnings" :key="warning">{{ warning }}</li></ul><form v-if="item.status==='pending_review' && canReview && !item.submittedByCurrentUser" @submit.prevent="review(item,'accepted')"><label class="field">独立复核意见<textarea v-model.trim="reviewNotes[item.id]" required rows="2" maxlength="2000" :disabled="!!busy"></textarea></label><div class="ai-security-actions"><button class="button button-primary" :disabled="!!busy || !reviewNotes[item.id]?.trim()">接受并记录复核</button><button class="button button-secondary" type="button" :disabled="!!busy || !reviewNotes[item.id]?.trim()" @click="review(item,'rejected')">拒绝并记录原因</button></div></form><p v-else-if="item.reviewedAt" class="table-secondary">复核人 #{{ item.reviewer }} · {{ formatDate(item.reviewedAt) }}<span v-if="item.reviewNote"> · {{ item.reviewNote }}</span></p><p v-else class="table-secondary">等待独立复核，提交人不能复核自己的变更。</p></article></template>
        <div v-if="tab==='snapshots' && snapshots.length" class="table-scroll" role="region" aria-label="冻结快照历史" tabindex="0"><table class="data-table"><thead><tr><th scope="col">快照</th><th scope="col">冻结时间</th><th scope="col" class="table-number">发现数</th><th scope="col">摘要</th><th scope="col">原始内容</th></tr></thead><tbody><tr v-for="item in snapshots" :key="item.id"><th scope="row">#{{ item.id }}</th><td>{{ formatDate(item.createdAt) }}</td><td class="table-number">{{ item.findingsCount }}</td><td class="mono">{{ item.digest }}</td><td><button class="button button-secondary" :disabled="!!busy || current.loading" @click="download('/snapshots/'+item.id,'scarlet-security-snapshot-'+item.id+'.json')">下载此快照</button></td></tr></tbody></table></div>
        <div v-if="!current.data.items.length" class="empty-state"><h3>当前范围没有{{ tab==='network'?'业务关联':tab==='changes'?'变更影响记录':'冻结快照' }}</h3><p>{{ tab==='network'?'可调整搜索词，或在下方登记一条有明确依据的关联。':tab==='changes'?'可调整筛选条件，或在下方选择关联生成变更影响。':'可调整时间范围，或冻结当前有界汇总。' }}</p></div>
        <footer class="network-footer"><div><p>{{ current.data.coverage }}</p><small>{{ current.data.hasMore?'仍有较早记录可继续查询。':'已到当前筛选范围的最早记录。' }} 刷新范围后纳入新增记录，后续页可能反映已更新的登记事实。</small></div><div class="page-actions"><button class="button button-secondary" :disabled="current.loading || !!busy || !current.history.length" @click="previous">较新一页</button><button class="button button-secondary" :disabled="current.loading || !!busy || !current.data.hasMore || !current.data.nextCursor" @click="next">较早一页</button><button class="button button-secondary" :disabled="current.loading || !!busy || !!current.error" @click="download(networkHistoryQuery(kind,current.applied,current.cursor),'scarlet-'+kind+'-page.json')">导出本页</button></div></footer>
      </template>
    </section>

    <form v-if="tab==='network' && canWrite" ref="bindingForm" class="panel" @submit.prevent="saveBinding"><div class="ai-security-heading"><div><h2>{{ bindingDraft.id?'编辑业务关联 #'+bindingDraft.id:'登记网络业务关联' }}</h2><p>登记真实资产和处理活动；地域、接收方和依据须经过业务确认。</p></div></div><div class="ai-security-body"><fieldset class="ai-security-fields" :disabled="!!busy"><label class="field">站点名称<input v-model.trim="bindingDraft.site" required maxlength="160"></label><label class="field">线路引用<input v-model.trim="bindingDraft.circuit" required maxlength="160"></label><label class="field">VRF / 隔离域<input v-model.trim="bindingDraft.vrf" maxlength="160"></label><label class="field">应用引用<input v-model.trim="bindingDraft.applicationRef" maxlength="160"></label><label class="field">资产编号<input v-model="bindingDraft.assetIds" placeholder="多个编号用逗号分隔"><small>仅采用本工作区资产编号。</small></label><label class="field">处理活动编号<input v-model="bindingDraft.activityIds" placeholder="多个编号用逗号分隔"></label><label class="field">业务处理地域<input v-model.trim="bindingDraft.region" maxlength="160"></label><label class="field">接收方<input v-model.trim="bindingDraft.receiver" maxlength="240"></label><label class="field field-wide">业务用途<textarea v-model.trim="bindingDraft.purpose" required maxlength="2000" rows="2"></textarea></label><label class="field field-wide">依据引用<input v-model.trim="bindingDraft.evidenceRef" required maxlength="500"><small>客户确认单、工单或可复核材料引用；线路 IP 所在地不能单独证明处理位置。</small></label></fieldset><div class="ai-security-actions"><button class="button button-primary" :disabled="!!busy">{{ busy==='binding'?'正在保存…':'保存人工登记' }}</button><button v-if="bindingDraft.id" class="button button-secondary" type="button" :disabled="!!busy" @click="Object.assign(bindingDraft,blankBinding())">取消编辑</button></div></div></form>
    <section v-if="tab==='changes' && canWrite" class="panel"><div class="ai-security-heading"><div><h2>生成连接变更的数据影响</h2><p>采用当前关联版本，冻结受影响资产、活动和待确认事项。</p></div><button class="button button-secondary" :disabled="!!busy" @click="emit('selectTab','network')">搜索业务关联</button></div><div class="ai-security-body"><form class="network-binding-lookup" @submit.prevent="lookupBinding"><label class="field">业务关联编号<input v-model.trim="bindingLookup" required inputmode="numeric" pattern="[1-9][0-9]*" :disabled="!!busy" placeholder="输入台账中的编号"></label><button class="button button-secondary" :disabled="lookupBusy || !!busy">{{ lookupBusy?'正在核对…':'读取当前关联' }}</button></form><div v-if="selectedBinding" class="network-selected" role="status"><strong>#{{ selectedBinding.id }} · {{ selectedBinding.site }} / {{ selectedBinding.circuit }}</strong><p>{{ selectedBinding.applicationRef || '未填应用' }} · {{ selectedBinding.region || '未确认地域' }} · 修订 {{ selectedBinding.revision }}</p><p class="table-secondary">{{ selectedBinding.evidenceRef }} · {{ formatDate(selectedBinding.updatedAt) }}</p></div><form @submit.prevent="createChange"><fieldset class="ai-security-fields" :disabled="!!busy || lookupBusy || !selectedBinding"><label class="field">拟采用地域<input v-model.trim="changeDraft.proposedRegion" required maxlength="160"></label><label class="field">拟采用应用引用<input v-model.trim="changeDraft.proposedApplicationRef" required maxlength="160"></label><label class="field field-wide">变更原因<textarea v-model.trim="changeDraft.reason" required maxlength="2000" rows="2"></textarea></label></fieldset><div class="ai-security-actions"><button class="button button-primary" :disabled="!!busy || lookupBusy || !selectedBinding">{{ busy==='change'?'正在生成…':'生成影响清单' }}</button><a :href="home+'/ai-security'">核对 AI 数据与工具策略</a></div><p class="table-secondary">本操作不修改设备或选路。若关联版本变化，请重新读取并核对；业务原因草稿会保留。</p></form></div></section>
    <section v-if="tab==='snapshots'" class="panel ai-security-body"><h3>导出当前汇总与冻结历史</h3><p>当前汇总保留概览的有界预览。完整网络台账请在对应页逐页查询、导出；完整资产风险请进入“数据风险与证据”。快照下载保留冻结时的覆盖范围。</p><button class="button button-secondary" :disabled="!!busy || current.loading" @click="download('/export','scarlet-security-platform-preview.json')">导出当前有界汇总</button></section>
  </div>
</template>

<style scoped>
.security-network { display:grid; gap:16px; min-width:0; }
.security-network>.message { margin:0; }
.network-filters { align-items:end; }
.network-filters .field { flex:1 1 220px; min-width:0; margin:0; }
.network-filters input,.network-filters select { width:100%; }
.network-caption { margin:0; padding:0 20px 12px; color:var(--secondary); font-size:13px; }
.security-network .data-table { min-width:760px; }
.security-network :is(th,td) { vertical-align:top; overflow-wrap:anywhere; }
.network-row-actions { display:flex; flex-wrap:wrap; gap:8px; }
.network-change { padding:22px 20px; border-top:1px solid var(--line); overflow-wrap:anywhere; }
.network-change header { margin-bottom:16px; }
.network-change h3 { margin:0 0 8px; }
.network-change header p,.network-reason { margin:0 0 16px; }
.network-reason { white-space:pre-wrap; }
.network-warnings { padding-inline-start:20px; color:var(--secondary); line-height:1.7; }
.network-footer { display:flex; flex-wrap:wrap; align-items:center; gap:16px; padding:18px 20px; border-top:1px solid var(--line); }
.network-footer>div:first-child { flex:1 1 320px; min-width:0; }
.network-footer p { margin:0 0 6px; color:var(--secondary); font-size:14px; }
.network-footer small { color:var(--secondary); font-size:13px; }
.network-binding-lookup { display:flex; flex-wrap:wrap; gap:12px; align-items:end; margin-bottom:20px; }
.network-binding-lookup .field { flex:1 1 220px; margin:0; }
.network-selected { padding:16px; margin-bottom:20px; border:1px solid var(--line); border-radius:10px; background:var(--surface-alt,#f5f8fc); overflow-wrap:anywhere; }
.network-selected p { margin:8px 0 0; }
@media(max-width:640px) { .network-filters { display:grid; grid-template-columns:minmax(0,1fr); } .network-footer .page-actions { width:100%; } .network-footer .button { flex:1 1 auto; } }
</style>
