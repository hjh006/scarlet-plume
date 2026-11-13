<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { apiValidationOutcomes, apiValidationPlanStates, apiValidationRuleDescriptions, apiValidationRules, apiValidationRunStates, validationGap, validationStatusCode, type APIValidationPlan, type APIValidationPlanInput, type APIValidationProbePolicy, type APIValidationRun, type APIValidationSource } from './api-validation'
import Icon from './Icon.vue'

const props = defineProps<{ workspace: Workspace; planId?: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const workspaceUrl = workspacePath(props.workspace.slug)
const base = workspaceUrl + '/api-validation'
const editor = computed(() => ['owner', 'operator'].includes(props.workspace.role))
const approver = computed(() => ['owner', 'reviewer'].includes(props.workspace.role))
const plans = ref<APIValidationPlan[]>([])
const sources = ref<APIValidationSource[]>([])
const loading = ref(false)
const plansError = ref('')
const sourcesError = ref('')
const selectedId = ref(props.planId || '')
const selected = computed(() => plans.value.find(plan => plan.id === Number(selectedId.value)))
const activeSources = computed(() => sources.value.filter(source => source.kind === 'security_events' && !source.revokedAt))
const sourceForPlan = computed(() => sources.value.find(source => source.id === selected.value?.sourceId))
const sourceReady = computed(() => activeSources.value.some(source => source.id === selected.value?.sourceId))
const stateFilter = ref('')
const listLimit = ref(25)
const filteredPlans = computed(() => plans.value.filter(plan => !stateFilter.value || plan.state === stateFilter.value))
const notice = ref('')
const formVisible = ref(false)
const editId = ref<number | null>(null)
const draft = reactive({ revision: 0, sourceId: '', name: '', service: '', method: 'GET' as const, route: '', targetOrigin: '', manifestRef: '', rules: ['bola'] as APIValidationPlanInput['rules'], intervalMinutes: 60 })
const probeAuthRequired=ref(false),sensitiveFieldRows=ref(''),responseHeaderRows=ref('')
const saving = ref(false)
const formError = ref('')
const formConflict = ref(false)
const draftReloading = ref(false)
const formHeading = ref<HTMLElement | null>(null)
const detailHeading = ref<HTMLElement | null>(null)
const actionError = ref('')
const acting = ref('')
const conflictIds = ref<Set<number>>(new Set())
const runs = ref<APIValidationRun[]>([])
const runsLoading = ref(false)
const runsError = ref('')
const runLimit = ref(10)
const latestRun = computed(() => runs.value[0])
const latestProof = computed(() => latestRun.value?.result?.results[0])
const retrying = ref<number | null>(null)
const retryErrors = reactive<Record<number, string>>({})
const active = new AbortController()
let runRequest: AbortController | undefined
const activeLease = computed(() => Boolean(selected.value?.leaseExpiresAt && new Date(selected.value.leaseExpiresAt).valueOf() > Date.now()))
const actionLocked = computed(() => Boolean(loading.value || plansError.value || acting.value || selected.value && conflictIds.value.has(selected.value.id)))
const canApprove = computed(() => Boolean(approver.value && selected.value?.state === 'draft' && selected.value.canApprove === true && sourceReady.value))
const canRun = computed(() => Boolean(editor.value && selected.value?.state === 'approved' && sourceReady.value && !activeLease.value))
const draftSourceReady = computed(() => activeSources.value.some(source => source.id === Number(draft.sourceId)))

function failure(error: unknown) {
  if (active.signal.aborted) return ''
  if (error instanceof ApiError && error.status === 401) { emit('sessionExpired'); return '' }
  return describeError(error)
}
function sourceLabel(id: number) { return sources.value.find(source => source.id === id)?.name || '来源 #' + id }
function updatePlan(plan: APIValidationPlan) {
  const index = plans.value.findIndex(item => item.id === plan.id)
  if (index < 0) plans.value.unshift(plan)
  else plans.value[index] = plan
}
async function refreshAfterMutation() {
  try { await loadPlans() } catch (error) { plansError.value = '操作已登记，但刷新任务失败：' + failure(error) }
}
function applyDraft(plan?: APIValidationPlan) {
  probeAuthRequired.value=!!plan?.probePolicy?.authRequired
  sensitiveFieldRows.value=plan?.probePolicy?.sensitiveFields?.map(f=>f.path+', '+f.category).join('\n')||''
  responseHeaderRows.value=plan?.probePolicy?.responseHeaders?.map(h=>h.name+': '+h.value).join('\n')||''
  editId.value = plan?.id ?? null
  Object.assign(draft, plan ? { revision: plan.revision, sourceId: String(plan.sourceId), name: plan.name, service: plan.service, method: 'GET', route: plan.route, targetOrigin: plan.targetOrigin, manifestRef: plan.manifestRef, rules: [...plan.rules], intervalMinutes: plan.intervalMinutes } : { revision: 0, sourceId: '', name: '', service: '', method: 'GET', route: '', targetOrigin: '', manifestRef: '', rules: ['bola'], intervalMinutes: 60 })
}
async function loadPlans() {
  const result = await request<{items: APIValidationPlan[]; limit: number}>(base + '/plans', { signal: active.signal })
  if (active.signal.aborted) return
  plans.value = result.items ?? []; plansError.value = ''; conflictIds.value = new Set()
  if (selectedId.value && !selected.value) plansError.value = '找不到所选任务，或该任务不在当前工作区。'
}
async function refresh() {
  if (loading.value || saving.value || acting.value) return
  loading.value = true
  const results = await Promise.allSettled([
    loadPlans(), request<{items: APIValidationSource[]}>(workspaceUrl + '/operations/sources', { signal: active.signal }),
  ])
  if (!active.signal.aborted) {
    if (results[0].status === 'rejected') plansError.value = failure(results[0].reason)
    if (results[1].status === 'fulfilled') { sources.value = results[1].value.items ?? []; sourcesError.value = '' }
    else sourcesError.value = failure(results[1].reason)
  }
  loading.value = false
  if (selected.value) await loadRuns(selected.value.id)
}
async function loadRuns(id: number) {
  runRequest?.abort(); const pending = new AbortController(); runRequest = pending
  runsLoading.value = true; runsError.value = ''; runs.value = []; runLimit.value = 10
  try { const value = await request<{items: APIValidationRun[]; limit: number}>(base + '/plans/' + id + '/runs', { signal: pending.signal }); if (!pending.signal.aborted) runs.value = value.items ?? [] }
  catch (error) { if (!pending.signal.aborted) runsError.value = failure(error) }
  finally { if (!pending.signal.aborted) runsLoading.value = false }
}
function returnToList() { runRequest?.abort(); selectedId.value = ''; runs.value = []; formVisible.value = false; actionError.value = ''; window.location.hash = home + '/validation' }
async function choose(id: number) {
  selectedId.value = String(id); actionError.value = ''; await loadRuns(id)
  await nextTick(); detailHeading.value?.focus({ preventScroll: true }); detailHeading.value?.scrollIntoView({ block: 'nearest' })
}
async function openForm(plan?: APIValidationPlan) {
  if (!editor.value) return
  applyDraft(plan); formError.value = ''; formConflict.value = false; formVisible.value = true; notice.value = ''
  await nextTick(); formHeading.value?.focus({ preventScroll: true }); formHeading.value?.scrollIntoView({ block: 'nearest' })
}
function originInput(value: string) {
  try {
    const url = new URL(value.trim())
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash || url.pathname !== '/') return ''
    return url.origin
  } catch { return '' }
}
async function save() {
  if (!editor.value || saving.value || formConflict.value || draftReloading.value || !draftSourceReady.value) return
  formError.value = ''
  const targetOrigin = originInput(draft.targetOrigin)
  if (!targetOrigin) { formError.value = '请填写合法源站 Origin；不能包含账号、密码、对象路径、查询参数或片段。'; return }
  if (!draft.rules.length) { formError.value = '请至少选择一项已授权的验证规则。'; return }
  const fields=sensitiveFieldRows.value.split('\n').map(s=>s.trim()).filter(Boolean).map(line=>{const [path='',category='']=line.split(',');return {path:path.trim(),category:category.trim()}})
  const headers=responseHeaderRows.value.split('\n').map(s=>s.trim()).filter(Boolean).map(line=>{const index=line.indexOf(':');return {name:index<0?line:line.slice(0,index).trim(),value:index<0?'':line.slice(index+1).trim()}})
  if(fields.length>8||headers.length>8||fields.some(f=>!f.path||!f.category)||headers.some(h=>!h.name||!h.value)){formError.value='请核对基线格式，每类最多8项；字段为路径,类别，响应头为名称:明确值。';return}
  if((draft.rules.includes('auth_required')||draft.rules.includes('sensitive_exposure'))&&!probeAuthRequired.value){formError.value='请先明确确认本测试对象必须认证，再选择匿名对照规则。';return}
  const probePolicy:APIValidationProbePolicy={authRequired:probeAuthRequired.value,sensitiveFields:fields,responseHeaders:headers}
  const input: APIValidationPlanInput = { revision: draft.revision, sourceId: Number(draft.sourceId), name: draft.name.trim(), service: draft.service.trim(), method: 'GET', route: draft.route.trim(), targetOrigin, manifestRef: draft.manifestRef.trim(), rules: [...draft.rules], intervalMinutes: draft.intervalMinutes,probePolicy }
  saving.value = true
  try {
    const value = await request<APIValidationPlan>(base + '/plans' + (editId.value ? '/' + editId.value : ''), { method: editId.value ? 'PUT' : 'POST', body: JSON.stringify(input), signal: active.signal })
    notice.value = editId.value && value.revision === draft.revision ? '任务配置未变化，保留现有状态。' : `任务 #${value.id} 已保存，${apiValidationPlanStates[value.state] || '请核对任务状态'}。`
    updatePlan(value); formVisible.value = false; selectedId.value = String(value.id)
    await refreshAfterMutation(); await loadRuns(value.id)
  } catch (error) { formConflict.value = error instanceof ApiError && error.status === 409; formError.value = formConflict.value ? '任务版本已变化。未保存的输入已保留，请显式重新读取后核对。' : failure(error) }
  finally { saving.value = false }
}
async function reloadDraft() {
  if (!editId.value || draftReloading.value || saving.value) return
  draftReloading.value = true; formError.value = ''
  try {
    await loadPlans()
    const plan = plans.value.find(plan => plan.id === editId.value)
    if (!plan) { formError.value = '找不到正在编辑的任务。未保存的输入仍已保留。'; return }
    applyDraft(plan); formConflict.value = false; notice.value = '已重新读取当前配置，未保存输入已替换。'
  } catch (error) { formError.value = failure(error) }
  finally { draftReloading.value = false }
}
async function act(action: 'approve' | 'request' | 'disable', mode?: 'validation' | 'connection') {
  const plan = selected.value
  if (!plan || actionLocked.value || (action === 'approve' ? !canApprove.value : !editor.value) || action === 'request' && !canRun.value) return
  acting.value = action + (mode || ''); actionError.value = ''
  try {
    const value = await request<APIValidationPlan>(base + '/plans/' + plan.id + '/' + action, { method: 'POST', body: JSON.stringify({ revision: plan.revision, ...(mode ? { mode } : {}) }), signal: active.signal })
    updatePlan(value)
    notice.value = action === 'approve' ? '已独立批准当前修订，等待客户侧 Agent 领取。' : action === 'disable' ? '任务已停用，不再安排后续领取；已有运行请核对后台版本校验与回执。' : mode === 'connection' ? '连接诊断已排队，等待客户侧 Agent 执行。' : '验证已排队，等待客户侧 Agent 执行。'
    await refreshAfterMutation(); await loadRuns(plan.id)
  } catch (error) {
    if (error instanceof ApiError && error.status === 409) { conflictIds.value = new Set([...conflictIds.value, plan.id]); actionError.value = '任务版本或执行状态已变化，请刷新任务后重试。' }
    else actionError.value = failure(error)
  } finally { acting.value = '' }
}
async function retryProjection(run: APIValidationRun) {
  if (!editor.value || !selected.value || retrying.value !== null || run.projectionState !== 'error') return
  retrying.value = run.id; retryErrors[run.id] = ''
  try {
    const result = await request<APIValidationRun>(base + '/plans/' + run.planId + '/runs/' + encodeURIComponent(run.runRef) + '/retry', { method: 'POST', signal: active.signal })
    const index = runs.value.findIndex(item => item.id === run.id)
    if (index >= 0) runs.value[index] = result
    notice.value = '已重新汇总留存结果，请核对关联检查。'
  } catch (error) { retryErrors[run.id] = error instanceof ApiError && error.status === 503 ? '运行结果已留存，关联检查汇总仍未完成。可稍后重试。' : failure(error) }
  finally { retrying.value = null }
}
onMounted(() => { void refresh() })
onBeforeUnmount(() => { active.abort(); runRequest?.abort() })
</script>

<template>
  <div class="validation-view">
    <header class="page-header"><div><p class="eyebrow">API 安全 / 授权验证</p><h1>{{ formVisible ? (editId ? '编辑主动验证范围' : '新建主动验证任务') : selected ? selected.name + ' · 运行结果' : 'API 主动验证任务' }}</h1><p>按已批准范围由客户侧 Agent 验证，留存判定依据、验证缺口与关联检查。</p></div><div class="page-actions"><a v-if="selected || formVisible" class="button button-secondary" :href="home + '/validation'" @click.prevent="returnToList">任务台账</a><button class="button button-secondary" type="button" :disabled="loading || saving || Boolean(acting)" @click="refresh"><Icon name="automation" />{{ loading ? '刷新中…' : '刷新任务与结果' }}</button><button v-if="editor" class="button button-primary" type="button" :disabled="saving" @click="openForm()"><Icon name="plus" />新建验证任务</button></div></header>
    <p v-if="notice" class="message message-success" role="status">{{ notice }}</p><p v-if="plansError" class="message message-error" role="alert">{{ plansError }}</p><p v-if="sourcesError" class="message message-error" role="alert">来源列表：{{ sourcesError }}</p>


    <div v-if="formVisible && editor" class="validation-form-workbench">    <section class="panel" aria-labelledby="validation-form-title"><div class="panel-heading"><div><h2 id="validation-form-title" ref="formHeading" tabindex="-1">{{ editId ? '编辑验证任务 #' + editId : '新建验证任务' }}</h2><p v-if="editId">基于修订 v{{ draft.revision }}；配置变化将回到待批准状态。</p></div></div><form class="validation-form" @submit.prevent="save"><div v-if="formError" class="message message-error validation-full" role="alert"><p>{{ formError }}</p><button v-if="formConflict" type="button" class="button button-secondary" :disabled="draftReloading || saving" @click="reloadDraft">{{ draftReloading ? '读取中…' : '重新读取并替换未保存输入' }}</button></div><div class="field"><label for="validation-name">任务名称</label><input id="validation-name" v-model="draft.name" maxlength="100" required></div><div class="field"><label for="validation-source">安全事件来源</label><select id="validation-source" v-model="draft.sourceId" required><option value="">选择未撤销的安全事件来源</option><option v-for="source in activeSources" :key="source.id" :value="String(source.id)">{{ source.name }} · {{ source.ownerTeam }}</option></select><p class="field-hint"><a :href="home + '/operations'">在运营与处置中配置来源</a>；使用来源令牌接入客户侧验证 Agent。</p></div><div class="field"><label for="validation-service">服务别名</label><input id="validation-service" v-model="draft.service" maxlength="80" required></div><div class="field"><label for="validation-origin">授权源站 Origin</label><input id="validation-origin" v-model="draft.targetOrigin" type="url" maxlength="300" placeholder="https://10.0.0.10:8443" required><p class="field-hint">仅源站，不含用户信息、对象路径、查询参数或原始请求。</p></div><div class="field validation-full"><label for="validation-route">GET 路由模板</label><input id="validation-route" v-model="draft.route" maxlength="300" placeholder="/orders/{id}" required><p class="field-hint">填写已授权路由模板；真实测试对象及其本地映射保留在清单中。</p></div><div class="field"><label for="validation-manifest">Agent 本地清单引用</label><input id="validation-manifest" v-model="draft.manifestRef" pattern="[A-Za-z0-9][A-Za-z0-9_-]{0,59}" maxlength="60" autocomplete="off" required><p class="field-hint">仅填写引用名，不填写文件路径、账号、密码、令牌或对象值。</p></div><div class="field"><label for="validation-frequency">验证周期（分钟）</label><input id="validation-frequency" v-model.number="draft.intervalMinutes" type="number" min="5" max="43200" step="1" required><p class="field-hint">5–43200 分钟；批准后按周期等待 Agent 领取。</p></div><fieldset class="validation-rule-options validation-full"><legend>已授权验证规则</legend><label v-for="(label, rule) in apiValidationRules" :key="rule"><span><input v-model="draft.rules" type="checkbox" :value="rule"><strong>{{ label }}</strong></span><small>{{ apiValidationRuleDescriptions[rule] }}</small></label></fieldset><p v-if="draft.sourceId && !draftSourceReady" class="message message-warning validation-full">所选来源已不可用或未载入，请重新选择可用来源。</p><fieldset class="validation-full validation-approved-policy"><legend>需独立批准的检测基线（客户侧manifest须完全一致）</legend><label><input v-model="probeAuthRequired" type="checkbox">明确确认本测试对象必须身份认证</label><div class="field"><label for="validation-sensitive-fields">匿名不得返回的敏感字段（每行：路径,类别，最多8项）</label><textarea id="validation-sensitive-fields" v-model="sensitiveFieldRows" rows="3" maxlength="1600" placeholder="contact.email,email"></textarea><small>类别仅email、phone、national_id、financial_account；仅检查标量字段，不上传原值。</small></div><div class="field"><label for="validation-response-headers">响应头基线（每行：名称:明确值，最多8项）</label><textarea id="validation-response-headers" v-model="responseHeaderRows" rows="3" maxlength="3000" placeholder="X-Content-Type-Options: nosniff"></textarea><small>允许Cache-Control、X-Content-Type-Options、Referrer-Policy、Content-Security-Policy及仅HTTPS的Strict-Transport-Security。按明确值完整比较。</small></div><p>认证、字段或响应头基线变化将增加修订并撤销审批；缺少正向对照或完整响应时保持无法判定。</p></fieldset><div class="form-actions validation-full"><button class="button button-primary" type="submit" :disabled="saving || draftReloading || formConflict || !draftSourceReady">{{ saving ? '保存中…' : '保存任务配置' }}</button><button class="button button-secondary" type="button" :disabled="saving || draftReloading" @click="formVisible = false">取消编辑</button></div></form></section>

<aside class="context-rail"><section class="panel"><div class="panel-heading"><h2>待批准范围</h2></div><div class="validation-rail-body"><dl><div><dt>来源</dt><dd>{{ draft.sourceId ? sourceLabel(Number(draft.sourceId)) : '待选择' }}</dd></div><div><dt>方法与路由</dt><dd class="mono">GET {{ draft.route || '待填写' }}</dd></div><div><dt>固定Origin</dt><dd class="mono">{{ draft.targetOrigin || '待填写' }}</dd></div><div><dt>清单引用</dt><dd>{{ draft.manifestRef || '待填写' }}</dd></div><div><dt>采用修订</dt><dd>{{ draft.revision ? 'v' + draft.revision : '尚未保存' }}</dd></div></dl><p>范围变化会重新要求独立批准，账号、密码和真实测试对象保留在客户侧清单。</p></div></section><section class="panel"><div class="panel-heading"><h3>三请求对照</h3></div><ol class="validation-probe-spine"><li><strong>A → A 自有对象</strong><span>确认账号A与其本地对象</span></li><li><strong>B → B 自有对象</strong><span>独立确认第二个账号</span></li><li><strong>B → A 对象</strong><span>在批准范围核对对象归属</span></li></ol></section></aside></div>
    <div v-if="!selected && !formVisible" class="validation-master-detail"><section class="panel" aria-labelledby="validation-plans-title" :aria-busy="loading"><div class="panel-heading"><div><h2 id="validation-plans-title">验证任务</h2><p>最多 100 项任务；任务执行与安全结论分别记录。</p></div><span>{{ plans.length }} 项</span></div><div class="filter-bar"><label for="validation-state">任务状态</label><select id="validation-state" v-model="stateFilter" @change="listLimit = 25"><option value="">全部状态</option><option v-for="(label, state) in apiValidationPlanStates" :key="state" :value="state">{{ label }}</option></select></div><p v-if="loading && !plans.length" class="empty-state" role="status">正在读取任务范围与审批状态…</p><div v-if="filteredPlans.length" class="table-scroll" tabindex="0" role="region" aria-labelledby="validation-plans-title"><table class="data-table validation-plan-table"><thead><tr><th scope="col">任务与来源</th><th scope="col">已登记范围</th><th scope="col">状态与周期</th><th scope="col">下次领取</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="plan in filteredPlans.slice(0, listLimit)" :key="plan.id"><th scope="row">{{ plan.name }}<span class="table-secondary">#{{ plan.id }} · {{ sourceLabel(plan.sourceId) }} · v{{ plan.revision }}</span></th><td>{{ plan.service }}<span class="table-secondary mono">GET {{ plan.route }}</span></td><td>{{ apiValidationPlanStates[plan.state] || '状态待核验' }}<span class="table-secondary">每 {{ plan.intervalMinutes }} 分钟</span></td><td>{{ plan.state === 'approved' ? formatDate(plan.nextRunAt) : '批准后安排' }}</td><td><button class="button button-secondary" type="button" @click="choose(plan.id)">范围与运行结果</button></td></tr></tbody></table></div><p v-else-if="!loading" class="empty-state">当前范围内暂无验证任务。配置来源和 Agent 本地清单后，创建任务并交独立人员批准。</p><div v-if="filteredPlans.length > listLimit" class="panel-footer"><button class="button button-secondary" type="button" @click="listLimit += 25">再显示 25 项</button></div></section>

    <aside class="context-rail"><section class="panel"><div class="panel-heading"><h2>授权验证范围</h2></div><div class="validation-rail-body"><dl><div><dt>请求方式</dt><dd>已批准的GET目标</dd></div><div><dt>测试账号</dt><dd>A自有；BOLA另需B自有/交叉</dd></div><div><dt>凭据位置</dt><dd>客户侧本地清单</dd></div><div><dt>结论口径</dt><dd>风险 / 本轮未观测 / 无法判定</dd></div></dl></div></section>    <section class="panel validation-boundary" aria-label="验证范围与凭据边界"><p>仅面向明确授权的私有或 loopback 源站，仅执行已批准GET与所选5类有界验证规则。真实测试账号、凭据、对象标识及原始响应保留在 Agent 本地清单；平台只保存范围引用和验证元数据。</p><p>修改配置后需重新独立批准；批准人须与创建及编辑人员分离。HTTP 200 仅表示响应状态，不能独立证明对象级越权。</p></section></aside></div>
    <section v-if="selected && !formVisible" class="panel validation-detail" aria-labelledby="validation-detail-title"><div class="panel-heading"><div><h2 id="validation-detail-title" ref="detailHeading" tabindex="-1">任务 #{{ selected.id }} · {{ selected.name }}</h2><p>{{ apiValidationPlanStates[selected.state] || '状态待核验' }} · 当前修订 v{{ selected.revision }}</p></div><div class="page-actions"><a class="button button-secondary" :href="home + '/validation/' + selected.id">任务链接</a><a class="button button-secondary" :href="base + '/plans/' + selected.id + '/export?format=json'" :download="'scarlet-api-validation-' + selected.id + '.json'"><Icon name="download" />导出 JSON</a><a class="button button-secondary" :href="base + '/plans/' + selected.id + '/export?format=html'">导出报告</a></div></div><dl class="detail-list"><div><dt>服务与来源</dt><dd>{{ selected.service }} · {{ sourceLabel(selected.sourceId) }}<span v-if="sourceForPlan?.revokedAt">（来源已撤销）</span></dd></div><div><dt>授权源站</dt><dd class="mono">{{ selected.targetOrigin }}</dd></div><div><dt>GET 路由模板</dt><dd class="mono">{{ selected.route }}</dd></div><div><dt>Agent 本地清单引用</dt><dd class="mono">{{ selected.manifestRef }}</dd></div><div><dt>验证规则</dt><dd>{{ selected.rules.map(rule => apiValidationRules[rule]).join('、') }}</dd></div><div><dt>周期</dt><dd>{{ selected.intervalMinutes }} 分钟</dd></div><div><dt>批准的检测基线</dt><dd><pre>{{JSON.stringify(selected.probePolicy||{},null,2)}}</pre></dd></div><div><dt>创建账号</dt><dd>#{{ selected.createdBy }}</dd></div><div><dt>独立批准</dt><dd>{{ selected.approvedBy ? '账号 #' + selected.approvedBy + ' · ' + formatDate(selected.approvedAt || '') : '待独立批准' }}</dd></div><div><dt>最近更新</dt><dd>{{ formatDate(selected.updatedAt) }}</dd></div><div v-if="selected.leaseExpiresAt"><dt>本轮领取期限</dt><dd>{{ formatDate(selected.leaseExpiresAt) }}</dd></div></dl><div class="validation-plan-actions"><p v-if="actionError" class="message message-error" role="alert">{{ actionError }}</p><p v-if="selected.state === 'draft'">创建或编辑人员不能批准同一任务。批准前请核对源站、路由、清单引用及规则范围。</p><p v-if="selected.state === 'draft' && approver && !selected.canApprove">当前账号不满足独立批准条件。</p><p v-if="activeLease">来源已领取当前修订，运行结果尚待回传。</p><div class="page-actions"><button v-if="canApprove" class="button button-primary" type="button" :disabled="actionLocked" @click="act('approve')">独立批准当前修订</button><button v-if="editor" class="button button-secondary" type="button" :disabled="actionLocked || saving" @click="openForm(selected)">编辑配置</button><button v-if="editor" class="button button-secondary" type="button" :disabled="actionLocked || !canRun" @click="act('request', 'connection')">测试连接</button><button v-if="editor" class="button button-primary" type="button" :disabled="actionLocked || !canRun" @click="act('request', 'validation')">立即验证</button><button v-if="editor && selected.state !== 'disabled'" class="button button-secondary" type="button" :disabled="actionLocked" @click="act('disable')">停用任务</button></div><p>测试连接也须先批准，仅诊断源站连接，不产生漏洞确认或“未发现”结论。配置变化后重新批准。</p></div></section>

    <section v-if="selected && !formVisible && latestProof?.rule === 'bola'" class="panel validation-proof-panel"><div class="panel-heading"><div><h2>本次来源验证依据</h2><p>采用运行 #{{ latestRun?.id }} · 修订 v{{ latestRun?.revision }} · {{ latestRun?.result?.mode === 'connection' ? '连接诊断，无安全结论' : apiValidationOutcomes[latestProof.outcome] }}</p></div></div><div class="validation-proof-grid"><article><span>A → A 自有对象</span><strong>{{ validationStatusCode(latestProof.ownerStatus) }}</strong><small>{{ latestProof.ownerConfirmed ? '来源已确认账号与对象' : '账号或对象确认不足' }}</small></article><article><span>B → B 自有对象</span><strong>{{ validationStatusCode(latestProof.otherOwnedStatus) }}</strong><small>{{ latestProof.otherAccountConfirmed ? '第二个账号已独立确认' : '第二个账号确认不足' }}</small></article><article><span>B → A 交叉请求</span><strong>{{ latestRun?.result?.mode === 'connection' ? '未执行' : validationStatusCode(latestProof.otherStatus) }}</strong><small>{{ latestProof.comparisonComplete ? '来源完成完整对照' : '对照范围尚不完整' }}</small></article></div><p class="validation-proof-note">{{ latestProof.rule === 'bola' ? '对照身份和对象归属在来源侧确认；HTTP200本身不能确认越权。' : '仅验证明确提供的测试凭据策略，不进行字典猜测。' }} 来源结果不能自动替代独立复核。</p></section>
    <section v-if="selected && !formVisible" class="panel validation-runs" aria-labelledby="validation-runs-title" :aria-busy="runsLoading"><div class="panel-heading"><div><h2 id="validation-runs-title">运行结果与验证依据</h2><p>界面最多显示最近 100 次；后台每任务保留 200 次，导出按接口上限记录。</p></div><button class="button button-secondary" type="button" :disabled="runsLoading" @click="loadRuns(selected.id)">{{ runsLoading ? '读取中…' : '刷新运行结果' }}</button></div><p v-if="runsError" class="message message-error" role="alert">{{ runsError }}</p><p v-if="runsLoading" class="empty-state" role="status">正在读取来源回传的结果…</p><div v-if="runs.length" class="validation-run-list"><details v-for="(run, index) in runs.slice(0, runLimit)" :key="run.id" :open="index === 0"><summary><strong>运行 #{{ run.id }}</strong><span>{{ apiValidationRunStates[run.state] || '状态待核验' }}</span><span>{{ (run.mode || run.result?.mode) === 'connection' ? '连接诊断' : '规则验证' }}</span><span>采用修订 v{{ run.revision }}</span><time :datetime="run.createdAt">{{ formatDate(run.createdAt) }}</time></summary><div class="validation-run-context"><p class="mono">{{ run.plan.targetOrigin }} · GET {{ run.plan.route }} · 清单 {{ run.plan.manifestRef }}</p><p v-if="run.revision !== selected.revision">该运行采用历史修订，不替代当前修订范围的验证。</p><p v-if="run.errorCode">未完成原因：{{ validationGap(run.errorCode) }}</p><p v-if="run.result?.mode === 'connection'">本轮为连接诊断；状态码用于核对连通性，不作安全结论。</p><p v-if="run.projectionState === 'pending'">运行结果已留存，检查汇总尚在处理中。</p><p v-else-if="run.projectionState === 'error'" class="validation-projection-error">运行结果已留存，检查汇总未完成；可刷新查看后续状态。</p><button v-if="editor && run.projectionState === 'error'" type="button" class="button button-secondary" :disabled="retrying !== null" @click="retryProjection(run)">{{ retrying === run.id ? '汇总中…' : '重试检查汇总' }}</button><p v-if="retryErrors[run.id]" class="message message-error" role="alert">{{ retryErrors[run.id] }}</p><p v-if="run.checkIds?.length">关联检查：<a v-for="id in run.checkIds" :key="id" :href="home + '/checks/' + id">检查 #{{ id }}</a></p></div><div v-if="run.result?.results?.length" class="table-scroll" tabindex="0" role="region" :aria-label="'运行' + run.id + '的验证依据'"><table class="data-table validation-proof-table"><thead><tr><th scope="col">规则与结论</th><th scope="col">响应状态</th><th scope="col">来源确认的对照条件</th><th scope="col">验证缺口</th></tr></thead><tbody><tr v-for="(result, resultIndex) in run.result.results" :key="resultIndex"><th scope="row">{{ apiValidationRules[result.rule as keyof typeof apiValidationRules] || '连接诊断' }}<span v-if="run.result.mode === 'validation'" class="badge validation-outcome" :data-outcome="result.outcome">{{ apiValidationOutcomes[result.outcome] || '结果待核验' }}</span><span v-else class="table-secondary">连接诊断，无安全结论</span></th><td>A 账号：{{ validationStatusCode(result.ownerStatus) }}<span v-if="result.otherOwnedStatus" class="table-secondary">B 账号自有对象：{{ validationStatusCode(result.otherOwnedStatus) }}</span><span v-if="result.otherStatus" class="table-secondary">B 账号交叉访问：{{ validationStatusCode(result.otherStatus) }}</span><span v-if="result.anonymousStatus" class="table-secondary">匿名：{{validationStatusCode(result.anonymousStatus)}}</span><span class="table-secondary">{{ result.requests }} 次请求</span></td><td><template v-if="run.result.mode === 'validation' && result.rule === 'bola'"><span>A 账号对象归属：{{ result.ownerConfirmed ? '已确认' : '未确认' }}</span><span>独立 B 测试账号：{{ result.otherAccountConfirmed ? '已确认' : '未确认' }}</span><span>同一测试对象：{{ result.sameObject ? '已确认' : '未确认' }}</span><span>本地对照：{{ result.comparisonComplete ? '已完成' : '未完成' }}</span></template><template v-else-if="run.result.mode === 'validation' && result.rule === 'weak_credential'"><span>测试账号访问确认：{{ result.ownerConfirmed ? '已确认' : '未确认' }}</span><span>测试凭据被接受：{{ result.credentialAccepted ? '已确认' : '未确认' }}</span><span>所选弱规则命中：{{ result.weakRuleMatched ? '已确认' : '未确认' }}</span><span>本地对照：{{ result.comparisonComplete ? '已完成' : '未完成' }}</span></template><template v-else-if="run.result.mode==='validation'"><span>A对象正向确认：{{result.ownerConfirmed?'已确认':'未确认'}}</span><span>批准基线匹配：{{result.policyConfirmed?'已确认':'未确认'}}</span><span v-if="result.rule!=='response_headers'">匿名同对象：{{result.anonymousObjectConfirmed?'已确认':'未确认或已拒绝'}}</span><span v-if="result.rule==='sensitive_exposure'">字段检查{{result.sensitiveChecked||0}}项；A格式命中{{result.ownerSensitiveCount||0}}，匿名命中{{result.anonymousSensitiveCount||0}}</span><span v-if="result.rule==='response_headers'">响应头检查{{result.headerChecked||0}}项；不符：{{result.headerMismatches?.join('、')||'未报告'}}</span><span>完整比较：{{result.comparisonComplete?'已完成':'未完成'}}</span><small v-if="result.policyDigest">基线摘要 {{result.policyDigest}}</small></template><span v-else>仅记录连接响应</span></td><td><ul v-if="result.gaps?.length" class="validation-gap-list"><li v-for="gap in result.gaps" :key="gap">{{ validationGap(gap) }}</li></ul><span v-else>本轮未报告缺口；结论仍限于批准范围。</span></td></tr></tbody></table></div><p v-else class="validation-run-context">本轮暂无可用于判定的规则结果。</p><p v-if="run.result" class="validation-observed-at">来源观测于 {{ formatDate(run.result.observedAt) }}；运行引用 <span class="mono">{{ run.runRef }}</span>。</p></details></div><p v-else-if="!runsLoading && !runsError" class="empty-state">暂无运行结果。批准后由客户侧 Agent 按周期或立即验证请求领取任务。</p><div v-if="runs.length > runLimit" class="panel-footer"><button class="button button-secondary" type="button" @click="runLimit += 10">再显示 10 次</button></div></section>
  </div>
</template>

<style scoped>
.validation-view { display: grid; gap: 1rem; min-width: 0; }
.validation-view .page-header { margin-bottom: 0; }
.validation-boundary { padding: .9rem 1rem; color: var(--secondary); font-size: .8rem; }
.validation-boundary p + p { margin-top: .45rem; }
.validation-form { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: .9rem 1rem; padding: 1rem; }
.validation-form .field { min-width: 0; margin: 0; }
.validation-full { grid-column: 1 / -1; }
.validation-form .form-actions { margin-top: .1rem; }
.validation-form .message .button { margin-top: .6rem; }
.validation-rule-options { display: grid; gap: .7rem; min-width: 0; margin: 0; padding: .8rem 1rem; border: 1px solid var(--line); border-radius: .5rem; }
.validation-rule-options legend { padding-inline: .4rem; font-size: .82rem; font-weight: 700; }
.validation-rule-options label { display: grid; gap: .25rem; }
.validation-rule-options label > span { display: flex; align-items: center; gap: .5rem; min-height: 2.75rem; }
.validation-rule-options small { max-width: 75ch; color: var(--secondary); font-size: .78rem; }
.validation-plan-table { min-width: 56rem; }
.validation-plan-actions { display: grid; gap: .8rem; padding: 1rem; border-top: 1px solid var(--line); }
.validation-plan-actions > p { max-width: 80ch; color: var(--secondary); font-size: .8rem; }
.validation-run-list > details + details { border-top: 1px solid var(--line); }
.validation-run-list summary { display: flex; flex-wrap: wrap; align-items: center; gap: .4rem 1rem; min-height: 3rem; padding: .8rem 1rem; cursor: pointer; }
.validation-run-list summary span, .validation-run-list summary time { color: var(--secondary); font-size: .78rem; }
.validation-run-context { padding: .65rem 1rem; color: var(--secondary); background: var(--surface-subtle); font-size: .78rem; }
.validation-run-context p + p { margin-top: .4rem; }
.validation-run-context a { display: inline-flex; align-items: center; min-height: 2.75rem; margin-left: .6rem; }
.validation-projection-error { color: var(--warning); }
.validation-proof-table { min-width: 60rem; }
.validation-proof-table td:nth-child(3) > span { display: block; margin-top: .2rem; }
.validation-outcome { display: block; margin-top: .4rem; white-space: normal; }
.validation-outcome[data-outcome="vulnerable"] { color: var(--danger); background: var(--danger-soft); }
.validation-outcome[data-outcome="not_observed"] { color: var(--success); background: var(--success-soft); }
.validation-outcome[data-outcome="inconclusive"] { color: var(--warning); background: var(--warning-soft); }
.validation-gap-list { display: grid; gap: .3rem; margin: 0; padding-left: 1rem; font-size: .8rem; }
.validation-observed-at { padding: .65rem 1rem; border-top: 1px solid var(--line); color: var(--secondary); font-size: .75rem; }
@media (max-width: 43.99rem) { .validation-form { grid-template-columns: minmax(0, 1fr); } .validation-detail .panel-heading, .validation-runs .panel-heading { flex-wrap: wrap; } }

.validation-master-detail,.validation-form-workbench{display:grid;grid-template-columns:minmax(0,1fr) 300px;gap:16px;align-items:start}.validation-master-detail>aside,.validation-form-workbench>aside{display:grid;gap:16px;min-width:0}.validation-boundary{font-size:12px;padding:18px;line-height:1.8;color:var(--secondary)}.validation-rail-body{padding:18px;font-size:12px;line-height:1.8;overflow-wrap:anywhere}.validation-rail-body dl{margin:0 0 14px}.validation-rail-body dl>div{display:grid;grid-template-columns:80px minmax(0,1fr);gap:10px;padding:11px 0;border-bottom:1px solid var(--line)}.validation-rail-body dt{color:var(--secondary)}.validation-rail-body dd{margin:0}.validation-probe-spine{padding:8px 18px 18px 37px;list-style:none;position:relative}.validation-probe-spine>li{position:relative;display:grid;gap:6px;margin:14px 0 0;padding:0 0 17px;font-size:12px;border-bottom:1px solid var(--line)}.validation-probe-spine>li::before{content:'';position:absolute;left:-18px;top:4px;width:8px;height:8px;border-radius:50%;background:var(--accent)}.validation-probe-spine span{color:var(--secondary);font-size:12px}.validation-form{padding:20px;gap:18px}.validation-form label{font-size:12px}.validation-form .field-hint{font-size:12px}.validation-rule-options{padding:15px}.validation-rule-options label{font-size:12px}.validation-rule-options small{font-size:12px}.validation-plan-table{min-width:720px}.validation-proof-grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:0;padding:22px 0}.validation-proof-grid>article{padding:0 24px;display:grid;gap:8px;border-right:1px solid var(--line)}.validation-proof-grid>article:last-child{border:0}.validation-proof-grid span{font-size:12px}.validation-proof-grid strong{font-size:26px;color:var(--accent)}.validation-proof-grid small{font-size:12px;color:var(--secondary)}.validation-proof-note{padding:14px 18px;border-top:1px solid var(--line);font-size:12px;color:var(--secondary)}.validation-detail .detail-list{grid-template-columns:repeat(3,minmax(0,1fr));gap:16px}.validation-detail .detail-list dt{font-size:12px}.validation-detail .detail-list dd{font-size:12px}.validation-run-list>details{border:1px solid var(--line);border-radius:8px;margin-bottom:12px;overflow:hidden}.validation-run-list>details>summary{padding:14px 18px;font-size:12px;background:var(--surface-subtle)}.validation-run-list .data-table{font-size:12px}.validation-plan-actions p{font-size:12px}@media(min-width:1100px) and (max-width:1400px){.validation-master-detail,.validation-form-workbench{grid-template-columns:minmax(0,1fr) 280px}}
.validation-approved-policy{display:grid;gap:15px;min-width:0;margin:0;padding:16px;border:1px solid var(--line);border-radius:8px}.validation-approved-policy legend{font-size:12px;font-weight:600}.validation-approved-policy textarea{width:100%;max-width:100%;font:inherit;font-size:12px;color:var(--ink);background:var(--surface);padding:9px 10px;border:1px solid var(--input-line);border-radius:6px}.validation-approved-policy p,.validation-approved-policy small{font-size:12px;line-height:1.8;color:var(--secondary)}.validation-detail pre{white-space:pre-wrap;overflow-wrap:anywhere;max-height:220px;overflow:auto}
</style>
