<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ApiError, describeError, formatDate, request, sessionPath, workspacePath, type Asset, type AssetPage, type Session, type Workspace } from './api'
import { type AutomationJob } from './automation'
import { type PostgresInspection } from './postgresql'
import { checkStatusLabels } from './workflow'
import { controlEventLabels, controlEventNote, controlKindLabels, controlResultLabel, controlStatusLabels, operationsSeverityLabels, operationsSourceKinds, securityEventCategoryLabels, securityEventOutcomeLabels, type ControlAction, type ControlActionInput, type OperationsOverview, type OperationsRuntimeRoute, type OperationsRuntimeSource, type OperationsSecurityEvent, type OperationsSource } from './security-operations'
import Icon from './Icon.vue'

const props = defineProps<{ workspace: Workspace; controlId?: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const workspaceUrl = workspacePath(props.workspace.slug)
const base = workspaceUrl + '/operations'
const owner = computed(() => props.workspace.role === 'owner')
const editor = computed(() => ['owner', 'operator'].includes(props.workspace.role))
const reviewer = computed(() => ['owner', 'reviewer'].includes(props.workspace.role))
const username = ref('')
const overview = ref<OperationsOverview | null>(null)
const sources = ref<OperationsSource[]>([])
const events = ref<OperationsSecurityEvent[]>([])
const controls = ref<ControlAction[]>([])
const loading = ref(false)
const errors = reactive<Record<string, string>>({})
const notice = ref('')
const tab = ref<'overview' | 'events' | 'controls' | 'sources'>(props.controlId ? 'controls' : 'overview')
const eventLimit = ref(25)
const controlLimit = ref(25)
const eventCategory = ref('')
const eventSeverity = ref('')
const eventOpen = ref(false)
const controlStatus = ref('')
const lifetime = new AbortController()
const sourceForm = ref(false)
const sourceDraft = reactive({ name: '', kind: 'security_events' as OperationsSource['kind'], ownerTeam: '', jobId: '' })
const sourceSaving = ref(false)
const sourceError = ref('')
const issued = ref<{ source: OperationsSource; token: string } | null>(null)
const tokenInput = ref<HTMLInputElement | null>(null)
const copyNotice = ref('')
const revokeTarget = ref<OperationsSource | null>(null)
const revoking = ref(false)
const revokeError = ref('')
const actionForm = ref(false)
const actionDraft = reactive({ kind: 'block_api' as ControlActionInput['kind'], runtimeSourceId: '', route: '', operationsSourceId: '', assetId: '', role: 'PUBLIC', checkId: '', reason: '', durationMinutes: 60 })
const actionSaving = ref(false)
const actionError = ref('')
const choicesLoading = ref(false)
const choicesError = ref('')
const runtimeSources = ref<OperationsRuntimeSource[]>([])
const runtimeRoutes = ref<OperationsRuntimeRoute[]>([])
const jobs = ref<AutomationJob[]>([])
const assets = ref<Asset[]>([])
const assetCursor = ref<number | null>(null)
const inspection = ref<PostgresInspection | null>(null)
const inspecting = ref(false)
const inspectionError = ref('')
let inspectionRequest: AbortController | undefined
const selected = ref<ControlAction | null>(null)
const detailLoading = ref(false)
const detailError = ref('')
const detailBusy = ref(false)
const detailConflict = ref(false)
const rollbackForm = ref(false)
const rollbackReason = ref('')
const detailHeading = ref<HTMLElement | null>(null)
let detailRequest: AbortController | undefined

const filteredEvents = computed(() => events.value.filter(event => (!eventCategory.value || event.category === eventCategory.value) && (!eventSeverity.value || event.severity === eventSeverity.value) && (!eventOpen.value || event.checkStatus !== 'verified')))
const filteredControls = computed(() => controls.value.filter(action => !controlStatus.value || action.status === controlStatus.value))
const pgJobs = computed(() => jobs.value.filter(job => job.provider === 'postgresql' && job.managed && !job.revokedAt))
const gatewaySources = computed(() => runtimeSources.value.filter(source => source.kind === 'gateway' && !source.revokedAt))
const selectableRoutes = computed(() => runtimeRoutes.value.filter(route => route.sourceId === Number(actionDraft.runtimeSourceId)))
const pgSources = computed(() => sources.value.filter(source => source.kind === 'postgres_control' && !source.revokedAt))
const selectedPgSource = computed(() => pgSources.value.find(source => source.id === Number(actionDraft.operationsSourceId)))
const pgAssets = computed(() => assets.value.filter(asset => asset.sourceProvider === 'postgresql' && asset.source === selectedPgSource.value?.scope.source))
const selectedPgJob = computed(() => pgJobs.value.find(job => job.id === Number(sourceDraft.jobId)))
const eligibleGrants = computed(() => inspection.value?.table.grants.filter(grant => grant.privilege === 'SELECT' && !grant.column && !grant.grantable) ?? [])
const roleOptions = computed(() => [...new Set(['PUBLIC', ...eligibleGrants.value.map(grant => grant.public ? 'PUBLIC' : grant.grantee)])])
const validInspection = computed(() => inspection.value?.assetId === Number(actionDraft.assetId) && inspection.value?.jobId === selectedPgSource.value?.scope.jobId && Boolean(selectedPgSource.value?.scope.schemas?.includes(inspection.value?.table.schema ?? '')))
const validRevokeRole = computed(() => eligibleGrants.value.some(grant => actionDraft.role === 'PUBLIC' ? grant.public : !grant.public && grant.grantee === actionDraft.role))
const canApprove = computed(() => Boolean(selected.value && reviewer.value && username.value && selected.value.actor !== username.value && selected.value.status === 'proposed' && !detailConflict.value))
const canRollback = computed(() => Boolean(editor.value && selected.value && ['approved', 'applied', 'failed'].includes(selected.value.status) && !detailConflict.value))
const recentTrend = computed(() => [...(overview.value?.trend ?? [])].sort((a, b) => a.day.localeCompare(b.day)).slice(-7))
const trendMax = computed(() => Math.max(1, ...recentTrend.value.flatMap(point => [point.events, point.verified])))
const sourceEndpoint = window.location.origin + '/api/v1/operations/security-events'
const bridgeCommand = `scarlet-event-bridge -format wazuh -url '${sourceEndpoint}'${/^http:\/\/(127\.0\.0\.1|localhost)(:\d+)?$/.test(window.location.origin) ? ' -local' : ''}`

function failure(caught: unknown) {
  if (caught instanceof ApiError && caught.status === 401) { emit('sessionExpired'); return '' }
  return describeError(caught)
}
async function loadPart<T>(key: string, path: string, save: (value: T) => void) {
  errors[key] = ''
  try { const result = await request<T>(path, { signal: lifetime.signal }); if (!lifetime.signal.aborted) save(result) }
  catch (caught) { if (!lifetime.signal.aborted) errors[key] = failure(caught) }
}
async function load() {
  if (loading.value) return
  loading.value = true
  await Promise.all([
    loadPart<OperationsOverview>('overview', base + '/overview', value => overview.value = value),
    loadPart<{items: OperationsSource[]}>('sources', base + '/sources', value => sources.value = value.items ?? []),
    loadPart<{items: OperationsSecurityEvent[]}>('events', base + '/events', value => events.value = value.items ?? []),
    loadPart<{items: ControlAction[]}>('controls', base + '/controls', value => controls.value = value.items ?? []),
    loadPart<Session>('session', sessionPath, value => username.value = value.username),
  ])
  loading.value = false
}
async function loadChoices() {
  if (choicesLoading.value) return
  choicesLoading.value = true; choicesError.value = ''
  const results = await Promise.allSettled([
    request<{items: OperationsRuntimeSource[]}>(workspaceUrl + '/api-security/runtime/sources', { signal: lifetime.signal }),
    request<{items: OperationsRuntimeRoute[]}>(workspaceUrl + '/api-security/runtime/routes', { signal: lifetime.signal }),
    request<{items: AutomationJob[]}>(workspaceUrl + '/automation/jobs', { signal: lifetime.signal }),
    request<AssetPage>(workspaceUrl + '/assets', { signal: lifetime.signal }),
  ])
  if (!lifetime.signal.aborted) {
    if (results[0].status === 'fulfilled') runtimeSources.value = results[0].value.items ?? []
    if (results[1].status === 'fulfilled') runtimeRoutes.value = results[1].value.items ?? []
    if (results[2].status === 'fulfilled') jobs.value = results[2].value.items ?? []
    if (results[3].status === 'fulfilled') { assets.value = results[3].value.items; assetCursor.value = results[3].value.nextCursor }
    for (const result of results) if (result.status === 'rejected') choicesError.value = failure(result.reason)
  }
  choicesLoading.value = false
}
async function moreAssets() {
  if (assetCursor.value === null || choicesLoading.value) return
  choicesLoading.value = true; choicesError.value = ''
  try { const page = await request<AssetPage>(workspaceUrl + '/assets?after=' + assetCursor.value, { signal: lifetime.signal }); assets.value.push(...page.items); assetCursor.value = page.nextCursor }
  catch (caught) { choicesError.value = failure(caught) }
  finally { choicesLoading.value = false }
}
async function inspectAsset() {
  inspectionRequest?.abort(); inspection.value = null; inspectionError.value = ''; actionDraft.role = 'PUBLIC'
  if (!actionDraft.assetId) return
  const pending = new AbortController(); inspectionRequest = pending; inspecting.value = true
  try { const value = await request<PostgresInspection>(workspaceUrl + '/assets/' + actionDraft.assetId + '/postgresql', { signal: pending.signal }); if (!pending.signal.aborted) inspection.value = value }
  catch (caught) { if (!pending.signal.aborted) inspectionError.value = failure(caught) }
  finally { if (!pending.signal.aborted) inspecting.value = false }
}
function changePgSource() { actionDraft.assetId = ''; inspection.value = null; inspectionError.value = ''; inspectionRequest?.abort(); inspecting.value = false }
async function openSourceForm() { issued.value = null; copyNotice.value = ''; sourceForm.value = true; sourceError.value = ''; await loadChoices() }
async function createSource() {
  if (!owner.value || sourceSaving.value) return
  sourceSaving.value = true; sourceError.value = ''; issued.value = null
  try {
    const result = await request<{source: OperationsSource; token: string}>(base + '/sources', { method: 'POST', body: JSON.stringify({ name: sourceDraft.name.trim(), kind: sourceDraft.kind, ownerTeam: sourceDraft.ownerTeam.trim(), ...(sourceDraft.kind === 'postgres_control' ? { jobId: Number(sourceDraft.jobId) } : {}) }) })
    issued.value = result; sourceForm.value = false; sourceDraft.name = ''; sourceDraft.ownerTeam = ''; sourceDraft.jobId = ''
    await load(); tab.value = 'sources'; await nextTick(); tokenInput.value?.focus()
  } catch (caught) { sourceError.value = failure(caught) }
  finally { sourceSaving.value = false }
}
async function copyToken() {
  if (!issued.value) return
  try { await navigator.clipboard.writeText(issued.value.token); copyNotice.value = '令牌已复制。请存入来源侧的秘密管理工具。' }
  catch { tokenInput.value?.select(); copyNotice.value = '令牌已选中，可使用系统复制快捷键。' }
}
async function revokeSource() {
  if (!owner.value || !revokeTarget.value || revoking.value) return
  revoking.value = true; revokeError.value = ''
  try { await request(base + '/sources/' + revokeTarget.value.id, { method: 'DELETE' }); issued.value = null; revokeTarget.value = null; notice.value = '来源令牌已撤销。'; await load() }
  catch (caught) { revokeError.value = failure(caught) }
  finally { revoking.value = false }
}
async function openActionForm() { actionForm.value = true; actionError.value = ''; await loadChoices() }
async function createAction() {
  if (!editor.value || actionSaving.value) return
  actionError.value = ''
  const input: ControlActionInput = { kind: actionDraft.kind, reason: actionDraft.reason.trim(), durationMinutes: actionDraft.durationMinutes }
  if (actionDraft.checkId) input.checkId = Number(actionDraft.checkId)
  if (input.kind === 'block_api') {
    const route = selectableRoutes.value.find(item => JSON.stringify([item.method, item.route]) === actionDraft.route)
    if (!route) { actionError.value = '请选择该来源实际观测到的方法和路由。'; return }
    Object.assign(input, { runtimeSourceId: Number(actionDraft.runtimeSourceId), method: route.method, route: route.route })
  } else {
    if (!validInspection.value || !validRevokeRole.value) { actionError.value = '请选择范围内已采集到的直接表级 SELECT 授权。'; return }
    Object.assign(input, { operationsSourceId: Number(actionDraft.operationsSourceId), assetId: Number(actionDraft.assetId), role: actionDraft.role })
  }
  actionSaving.value = true
  try { const result = await request<ControlAction>(base + '/controls', { method: 'POST', body: JSON.stringify(input) }); actionForm.value = false; actionDraft.reason = ''; notice.value = `处置申请 #${result.id} 已创建，等待符合独立性要求的账号审批。`; await load(); tab.value = 'controls'; await loadDetail(result.id) }
  catch (caught) { actionError.value = failure(caught) }
  finally { actionSaving.value = false }
}
async function loadDetail(id: number, focus = true) {
  detailRequest?.abort(); const pending = new AbortController(); detailRequest = pending
  if (selected.value?.id !== id) selected.value = null
  detailLoading.value = true; detailError.value = ''; rollbackForm.value = false; rollbackReason.value = ''
  try { const value = await request<ControlAction>(base + '/controls/' + id, { signal: pending.signal }); if (!pending.signal.aborted) { selected.value = value; detailConflict.value = false; if (focus) { await nextTick(); detailHeading.value?.focus({ preventScroll: true }); detailHeading.value?.scrollIntoView({ block: 'nearest' }) } } }
  catch (caught) { if (!pending.signal.aborted) detailError.value = failure(caught) }
  finally { if (!pending.signal.aborted) detailLoading.value = false }
}
async function act(kind: 'approve' | 'rollback') {
  if (!selected.value || detailBusy.value || detailConflict.value || (kind === 'approve' ? !canApprove.value : !canRollback.value)) return
  detailBusy.value = true; detailError.value = ''
  try {
    const id = selected.value.id
    await request(base + '/controls/' + id + '/' + kind, { method: 'POST', body: JSON.stringify({ revision: selected.value.revision, ...(kind === 'rollback' ? { reason: rollbackReason.value.trim() } : {}) }) })
    notice.value = kind === 'approve' ? '审批已登记，等待来源执行与回执。' : '回滚申请已登记，等待来源执行与回执。'
    await Promise.all([load(), loadDetail(id, false)])
  } catch (caught) { detailConflict.value = caught instanceof ApiError && caught.status === 409; detailError.value = detailConflict.value ? '处置版本已变化，请刷新详情后重试。' : failure(caught) }
  finally { detailBusy.value = false }
}
onMounted(() => { void load(); if (props.controlId && /^\d+$/.test(props.controlId)) void loadDetail(Number(props.controlId), false) })
onBeforeUnmount(() => { lifetime.abort(); inspectionRequest?.abort(); detailRequest?.abort(); issued.value = null })
</script>

<template>
  <div class="operations-view">
    <header class="page-header"><div><p class="eyebrow">安全运营 / 处置中心</p><h1>安全运营与处置</h1><p>汇总来源安全信号，跟踪独立审批、来源执行与回滚回执。</p></div><div class="page-actions"><button class="button button-secondary" type="button" :disabled="loading" @click="load"><Icon name="automation" />{{ loading ? '刷新中…' : '刷新数据' }}</button><button v-if="editor" class="button button-primary" type="button" :aria-expanded="actionForm" aria-controls="operations-control-form" @click="openActionForm"><Icon name="plus" />申请处置</button></div></header>
    <p v-if="notice" class="message message-success" role="status">{{ notice }}</p>
    <p v-for="(message, key) in errors" v-show="message" :key="key" class="message message-error" role="alert">{{ message }} 可刷新数据重试。</p>
    <nav class="operations-tabs" aria-label="安全运营视图"><button v-for="item in ([{id:'overview',label:'运营概览'},{id:'events',label:'安全事件'},{id:'controls',label:'处置动作'},{id:'sources',label:'集成来源'}] as const)" :key="item.id" type="button" class="button" :class="tab === item.id ? 'button-primary' : 'button-secondary'" :aria-pressed="tab === item.id" @click="tab = item.id">{{ item.label }}</button></nav>

    <section v-if="actionForm && editor" id="operations-control-form" class="panel" aria-labelledby="operations-control-title"><div class="panel-heading"><div><h2 id="operations-control-title">申请受控处置</h2><p>目标从已采集范围推导；审批与执行分离，执行成功不代表检查复核通过。</p></div><button class="button button-secondary" type="button" :disabled="actionSaving" @click="actionForm = false">收起</button></div><form class="operations-form" @submit.prevent="createAction"><p v-if="choicesError || actionError" class="message message-error operations-full" role="alert">{{ actionError || choicesError }}</p><div class="field"><label for="operations-kind">处置类型</label><select id="operations-kind" v-model="actionDraft.kind"><option v-for="(label, key) in controlKindLabels" :key="key" :value="key">{{ label }}</option></select></div>
      <template v-if="actionDraft.kind === 'block_api'"><div class="field"><label for="operations-runtime-source">独立 Agent / 网关来源</label><select id="operations-runtime-source" v-model="actionDraft.runtimeSourceId" required :disabled="choicesLoading" @change="actionDraft.route = ''"><option value="">请选择未撤销的来源</option><option v-for="source in gatewaySources" :key="source.id" :value="String(source.id)">{{ source.name }} · {{ source.service }}</option></select></div><div class="field operations-full"><label for="operations-route">已观测接口</label><select id="operations-route" v-model="actionDraft.route" required :disabled="!actionDraft.runtimeSourceId"><option value="">请选择实际方法与路由</option><option v-for="route in selectableRoutes" :key="JSON.stringify([route.method, route.route])" :value="JSON.stringify([route.method, route.route])">{{ route.method }} {{ route.route }}</option></select><p class="field-hint">仅具备处置指令领取能力的独立 Agent 会执行阻断并回执；仅上报网关日志不代表能执行。</p><a class="table-link" :href="home + '/api-security/runtime'">管理运行时来源</a></div></template>
      <template v-else><div class="field"><label for="operations-pg-source">PostgreSQL 执行来源</label><select id="operations-pg-source" v-model="actionDraft.operationsSourceId" required @change="changePgSource"><option value="">请选择已绑定采集任务的来源</option><option v-for="source in pgSources" :key="source.id" :value="String(source.id)">{{ source.name }} · {{ source.scope.database }}</option></select></div><div class="field"><label for="operations-pg-asset">范围内数据库表</label><select id="operations-pg-asset" v-model="actionDraft.assetId" required :disabled="!selectedPgSource" @change="inspectAsset"><option value="">请选择已采集资产</option><option v-for="asset in pgAssets" :key="asset.id" :value="String(asset.id)">{{ asset.name }}</option></select><button v-if="assetCursor !== null" type="button" class="button button-secondary" :disabled="choicesLoading" @click="moreAssets">加载更多资产</button></div><div class="field"><label for="operations-pg-role">授权角色</label><select id="operations-pg-role" v-model="actionDraft.role" required><option v-for="role in roleOptions" :key="role" :value="role">{{ role }}</option></select><p class="field-hint">仅支持已直接授予且不含 GRANT OPTION 的表级 SELECT；列级或间接授权需另行核对。</p></div><p v-if="inspecting" class="field-hint operations-full" role="status">正在读取当前范围与授权快照…</p><p v-if="inspectionError" class="message message-error operations-full" role="alert">{{ inspectionError }}</p><p v-if="inspection" class="operations-scope operations-full">后端依据来源范围及当前采集结果定位目标：{{ selectedPgSource?.scope.host }}:{{ selectedPgSource?.scope.port }} / {{ selectedPgSource?.scope.database }} / {{ inspection.table.schema }}.{{ inspection.table.name }}。浏览器不提交自定义主机、库名、schema 或表名。</p></template>
      <div class="field"><label for="operations-duration">有效期（分钟）</label><input id="operations-duration" v-model.number="actionDraft.durationMinutes" type="number" min="1" max="1440" step="1" required><p class="field-hint">1–1440 分钟。到期后请核对来源的回滚回执。</p></div><div class="field"><label for="operations-check">关联监管检查编号（可选）</label><input id="operations-check" v-model="actionDraft.checkId" type="number" min="1" step="1"></div><div class="field operations-full"><label for="operations-reason">处置原因</label><textarea id="operations-reason" v-model="actionDraft.reason" maxlength="400" required></textarea></div><div class="form-actions operations-full"><button class="button button-primary" type="submit" :disabled="actionSaving || choicesLoading || inspecting || (actionDraft.kind === 'revoke_select' && (!validInspection || !validRevokeRole))">{{ actionSaving ? '提交中…' : '提交独立审批' }}</button><button type="button" class="button button-secondary" :disabled="actionSaving" @click="actionForm = false">取消</button></div></form></section>

    <template v-if="tab === 'overview'"><section class="panel" aria-labelledby="operations-overview-title"><div class="panel-heading"><div><h2 id="operations-overview-title">运营概况</h2><p v-if="overview">最近 {{ overview.windowDays }} 天 · 更新于 {{ formatDate(overview.generatedAt) }}</p></div></div><p v-if="!overview" class="empty-state" role="status">{{ loading ? '正在汇总安全事件与处置记录…' : '暂未取得运营概况，可刷新重试。' }}</p><template v-if="overview"><dl class="operations-metrics"><div><dt>安全事件</dt><dd>{{ overview.counts.events }}</dd></div><div><dt>未闭环事件</dt><dd>{{ overview.counts.openEvents }}</dd></div><div><dt>待独立审批</dt><dd>{{ overview.counts.pendingApprovals }}</dd></div><div><dt>逾期风险</dt><dd>{{ overview.counts.overdueRisks }}</dd></div></dl><p class="operations-summary-line">{{ overview.counts.sources }} 个集成来源 · {{ overview.counts.controls }} 项处置动作 · {{ overview.counts.appliedControls }} 项来源执行成功回执</p></template></section><div class="operations-columns"><section class="panel" aria-labelledby="operations-trend-title"><div class="panel-heading"><h2 id="operations-trend-title">近 7 日事件与复核</h2></div><div class="operations-trend-legend"><span>安全事件</span><span>复核完成</span></div><ul v-if="recentTrend.length" class="operations-trend"><li v-for="point in recentTrend" :key="point.day"><time :datetime="point.day">{{ point.day.slice(5) }}</time><span><span class="operations-trend-bar" :style="{ width: point.events / trendMax * 100 + '%' }" aria-hidden="true"></span><b>{{ point.events }} 事件</b></span><span><span class="operations-trend-bar operations-trend-bar-verified" :style="{ width: point.verified / trendMax * 100 + '%' }" aria-hidden="true"></span><b>{{ point.verified }} 复核</b></span></li></ul><p v-else class="empty-state">当前窗口暂无趋势记录。</p></section><section class="panel" aria-labelledby="operations-team-title"><div class="panel-heading"><h2 id="operations-team-title">责任团队</h2></div><div v-if="overview?.teams.length" class="table-scroll" tabindex="0" role="region" aria-labelledby="operations-team-title"><table class="data-table operations-team-table"><thead><tr><th scope="col">团队</th><th scope="col">资产</th><th scope="col">检查</th><th scope="col">未闭环</th><th scope="col">逾期</th></tr></thead><tbody><tr v-for="team in overview.teams" :key="team.name"><th scope="row">{{ team.name || '责任团队待确认' }}</th><td>{{ team.assets }}</td><td>{{ team.checks }}</td><td>{{ team.openChecks }}</td><td>{{ team.overdue }}</td></tr></tbody></table></div><p v-else class="empty-state">暂无责任团队汇总。</p></section></div></template>

    <section v-if="tab === 'events'" class="panel" aria-labelledby="operations-events-title"><div class="panel-heading"><div><h2 id="operations-events-title">安全事件</h2><p>最多显示最近 500 条来源信号。来源声明已消除后，仍须完成整改复测与独立复核。</p></div><span>{{ events.length }} 条</span></div><div class="filter-bar"><label for="operations-event-category">类别</label><select id="operations-event-category" v-model="eventCategory" @change="eventLimit = 25"><option value="">全部类别</option><option v-for="(label, key) in securityEventCategoryLabels" :key="key" :value="key">{{ label }}</option></select><label for="operations-event-severity">风险等级</label><select id="operations-event-severity" v-model="eventSeverity" @change="eventLimit = 25"><option value="">全部等级</option><option v-for="(label, key) in operationsSeverityLabels" :key="key" :value="key">{{ label }}</option></select><label class="operations-checkbox"><input v-model="eventOpen" type="checkbox" @change="eventLimit = 25">仅未闭环</label></div><div v-if="filteredEvents.length" class="table-scroll" tabindex="0" role="region" aria-labelledby="operations-events-title"><table class="data-table operations-events-table"><thead><tr><th scope="col">信号与来源</th><th scope="col">类别与等级</th><th scope="col">关联对象</th><th scope="col">计数与窗口</th><th scope="col">检查状态</th><th scope="col">观测时间</th></tr></thead><tbody><tr v-for="event in filteredEvents.slice(0, eventLimit)" :key="event.id"><th scope="row">{{ event.rule }}<span class="table-secondary">{{ event.source }} · {{ securityEventOutcomeLabels[event.outcome] || '来源声明' }}</span><span class="table-secondary mono">{{ event.eventId }}</span></th><td>{{ securityEventCategoryLabels[event.category] || event.category }}<span class="table-secondary">{{ operationsSeverityLabels[event.severity] || event.severity }}风险</span></td><td>{{ event.service || '—' }}<span v-if="event.method" class="table-secondary mono">{{ event.method }} {{ event.route }}</span><a v-if="event.assetId" class="table-link" :href="home + '/classification/' + event.assetId">资产 #{{ event.assetId }}</a></td><td>{{ event.count }} 次 / {{ event.windowSeconds }} 秒</td><td><a class="table-link" :href="home + '/checks/' + event.checkId">检查 #{{ event.checkId }}</a><span class="table-secondary">{{ checkStatusLabels[event.checkStatus] || event.checkStatus }}</span></td><td>{{ formatDate(event.observedAt) }}</td></tr></tbody></table></div><p v-else class="empty-state">当前筛选范围暂无安全事件。使用集成来源对接现有安全系统，上报规则、窗口计数与范围元数据。</p><div v-if="filteredEvents.length > eventLimit" class="panel-footer"><button class="button button-secondary" type="button" @click="eventLimit += 25">再显示 25 条</button></div></section>

    <template v-if="tab === 'controls'"><section class="panel" aria-labelledby="operations-actions-title"><div class="panel-heading"><div><h2 id="operations-actions-title">处置动作</h2><p>执行状态来自来源回执，与监管检查的复核状态分别记录。</p></div><span>{{ controls.length }} 项</span></div><div class="filter-bar"><label for="operations-status">动作状态</label><select id="operations-status" v-model="controlStatus" @change="controlLimit = 25"><option value="">全部状态</option><option v-for="(label, key) in controlStatusLabels" :key="key" :value="key">{{ label }}</option></select></div><div v-if="filteredControls.length" class="table-scroll" tabindex="0" role="region" aria-labelledby="operations-actions-title"><table class="data-table operations-controls-table"><thead><tr><th scope="col">动作与目标</th><th scope="col">申请与审批</th><th scope="col">状态</th><th scope="col">有效期</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="action in filteredControls.slice(0, controlLimit)" :key="action.id"><th scope="row">#{{ action.id }} · {{ controlKindLabels[action.kind] }}<span class="table-secondary mono">{{ action.kind === 'block_api' ? [action.spec.method, action.spec.route].join(' ') : [action.spec.database, action.spec.schema, action.spec.table].filter(Boolean).join('.') }}</span></th><td>{{ action.actor }}<span class="table-secondary">{{ action.approvedBy ? '独立审批：' + action.approvedBy : '尚未审批' }}</span></td><td><span class="badge" :class="action.status === 'failed' ? 'status-needs_work' : 'status-draft'">{{ controlStatusLabels[action.status] || action.status }}</span></td><td>{{ formatDate(action.expiresAt) }}</td><td><button type="button" class="button button-secondary" @click="loadDetail(action.id)">详情与回执</button></td></tr></tbody></table></div><p v-else class="empty-state">当前筛选范围暂无处置动作。申请后，由符合独立性要求的账号审批，再等待来源领取并回执。</p><div v-if="filteredControls.length > controlLimit" class="panel-footer"><button class="button button-secondary" type="button" @click="controlLimit += 25">再显示 25 项</button></div></section>
      <p v-if="detailError" class="message message-error" role="alert">{{ detailError }} <button v-if="selected" type="button" class="button button-secondary" @click="loadDetail(selected.id, false)">刷新详情</button></p><p v-if="detailLoading && !selected" class="message" role="status">正在读取处置详情…</p><section v-if="selected" class="panel" aria-labelledby="operations-detail-title" :aria-busy="detailLoading"><div class="panel-heading"><div><h2 id="operations-detail-title" ref="detailHeading" tabindex="-1">处置 #{{ selected.id }} · {{ controlKindLabels[selected.kind] }}</h2><p>{{ controlStatusLabels[selected.status] || selected.status }} · 版本 {{ selected.revision }}</p></div><button type="button" class="button button-secondary" :disabled="detailBusy || detailLoading" @click="loadDetail(selected.id, false)">刷新详情</button></div><dl class="detail-list"><div><dt>申请人</dt><dd>{{ selected.actor }}</dd></div><div><dt>独立审批人</dt><dd>{{ selected.approvedBy || '待审批' }}</dd></div><div><dt>处置目标</dt><dd class="mono">{{ selected.kind === 'block_api' ? selected.spec.service + ' / ' + selected.spec.method + ' ' + selected.spec.route : selected.spec.host + ':' + selected.spec.port + ' / ' + [selected.spec.database, selected.spec.schema, selected.spec.table].join('.') }}</dd></div><div><dt>作用范围</dt><dd>{{ selected.kind === 'block_api' ? '运行时来源 #' + selected.runtimeSourceId : '执行来源 #' + selected.operationsSourceId + ' · 角色 ' + selected.spec.role }}</dd></div><div><dt>申请原因</dt><dd>{{ selected.reason }}</dd></div><div><dt>有效期至</dt><dd>{{ formatDate(selected.expiresAt) }}</dd></div><div v-if="selected.assetId"><dt>关联资产</dt><dd><a :href="home + '/classification/' + selected.assetId">资产 #{{ selected.assetId }}</a></dd></div><div v-if="selected.checkId"><dt>关联监管检查</dt><dd><a :href="home + '/checks/' + selected.checkId">检查 #{{ selected.checkId }}</a></dd></div></dl><div v-if="selected.result" class="operations-receipt"><h3>来源执行回执</h3><p>{{ selected.result.success ? '来源报告成功' : '来源报告失败' }} · {{ selected.result.phase === 'rollback' ? '回滚阶段' : '执行阶段' }} · {{ controlResultLabel(selected.result.code) }}</p><p v-if="selected.result.hadSelect !== undefined">执行前直接 SELECT：{{ selected.result.hadSelect ? '有' : '无' }}；执行后直接 SELECT：{{ selected.result.hasSelect === undefined ? '未返回' : selected.result.hasSelect ? '有' : '无' }}</p><time :datetime="selected.result.observedAt">{{ formatDate(selected.result.observedAt) }}</time><p>回执证明来源报告的执行结果；整改、自动复测和独立复核需在关联检查中完成。</p></div><div class="operations-detail-actions"><p v-if="selected.status === 'proposed'">审批人须具备 owner 或 reviewer 角色，且不能是申请人或关联检查的证据登记人，系统按实际记录校验。</p><button v-if="canApprove" class="button button-primary" type="button" :disabled="detailBusy || detailLoading" @click="act('approve')">{{ detailBusy ? '登记中…' : '独立审批此动作' }}</button><p v-else-if="selected.status === 'proposed' && selected.actor === username">当前账号是申请人，请由符合独立性要求的账号审批。</p><button v-if="canRollback && !rollbackForm" class="button button-secondary" type="button" :disabled="detailBusy || detailLoading" @click="rollbackForm = true">申请回滚</button><form v-if="rollbackForm && canRollback" @submit.prevent="act('rollback')"><div class="field"><label for="operations-rollback-reason">回滚原因</label><textarea id="operations-rollback-reason" v-model="rollbackReason" maxlength="400" required></textarea></div><div class="form-actions"><button class="button button-primary" type="submit" :disabled="detailBusy">{{ detailBusy ? '登记中…' : '提交回滚申请' }}</button><button class="button button-secondary" type="button" :disabled="detailBusy" @click="rollbackForm = false">取消</button></div></form></div><div class="panel-heading"><h3>审批与执行记录</h3></div><ol v-if="selected.events?.length" class="timeline"><li v-for="(entry, index) in selected.events" :key="index"><strong>{{ controlEventLabels[entry.action] || entry.action }}</strong><span class="table-secondary">{{ entry.actor }}{{ entry.automated ? ' · 自动记录' : '' }} · {{ formatDate(entry.createdAt) }}</span><p v-if="entry.note">{{ controlEventNote(entry.note, entry.automated) }}</p></li></ol><p v-else class="empty-state">暂无附加执行记录。</p></section>
    </template>

    <template v-if="tab === 'sources'"><section class="panel" aria-labelledby="operations-sources-title"><div class="panel-heading"><div><h2 id="operations-sources-title">集成来源</h2><p>令牌仅在创建时显示一次；执行器的数据库凭据保留在来源侧。</p></div><button v-if="owner" type="button" class="button button-primary" :aria-expanded="sourceForm" aria-controls="operations-source-form" @click="openSourceForm">新增来源</button></div><div v-if="sources.length" class="table-scroll" tabindex="0" role="region" aria-labelledby="operations-sources-title"><table class="data-table operations-sources-table"><thead><tr><th scope="col">来源与团队</th><th scope="col">用途与范围</th><th scope="col">最近接收</th><th scope="col">状态</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="source in sources" :key="source.id"><th scope="row">{{ source.name }}<span class="table-secondary">{{ source.ownerTeam }}</span></th><td>{{ operationsSourceKinds[source.kind] }}<span v-if="source.scope.jobId" class="table-secondary">采集任务 #{{ source.scope.jobId }} · {{ source.scope.database }} / {{ source.scope.schemas?.join('、') }}</span></td><td>{{ source.lastSeen ? formatDate(source.lastSeen) : '尚无记录' }}</td><td>{{ source.revokedAt ? '已撤销' : '已配置' }}</td><td><button v-if="owner && !source.revokedAt" class="button button-secondary" type="button" @click="revokeTarget = source; revokeError = ''">撤销令牌</button><span v-else>—</span></td></tr></tbody></table></div><p v-else class="empty-state">尚未接入安全事件或 PostgreSQL 权限执行来源。</p></section>
      <section v-if="sourceForm && owner" id="operations-source-form" class="panel" aria-labelledby="operations-source-title"><div class="panel-heading"><h2 id="operations-source-title">新增集成来源</h2></div><form class="operations-form" @submit.prevent="createSource"><p v-if="sourceError || choicesError" class="message message-error operations-full" role="alert">{{ sourceError || choicesError }}</p><div class="field"><label for="operations-source-name">来源名称</label><input id="operations-source-name" v-model="sourceDraft.name" maxlength="80" required></div><div class="field"><label for="operations-source-kind">用途</label><select id="operations-source-kind" v-model="sourceDraft.kind" @change="sourceDraft.jobId = ''"><option v-for="(label, key) in operationsSourceKinds" :key="key" :value="key">{{ label }}</option></select></div><div class="field"><label for="operations-source-team">责任团队</label><input id="operations-source-team" v-model="sourceDraft.ownerTeam" maxlength="80" required></div><div v-if="sourceDraft.kind === 'postgres_control'" class="field"><label for="operations-source-job">绑定 PostgreSQL 采集任务</label><select id="operations-source-job" v-model="sourceDraft.jobId" required :disabled="choicesLoading"><option value="">请选择已有托管任务</option><option v-for="job in pgJobs" :key="job.id" :value="String(job.id)">#{{ job.id }} · {{ job.source }}</option></select></div><p v-if="selectedPgJob && sourceDraft.kind === 'postgres_control'" class="operations-scope operations-full">绑定范围只读：{{ selectedPgJob.managed?.host }}:{{ selectedPgJob.managed?.port }} / {{ selectedPgJob.managed?.database }} / {{ selectedPgJob.managed?.schemas.join('、') }}。执行器在客户侧提供数据库凭据。</p><div class="form-actions operations-full"><button class="button button-primary" type="submit" :disabled="sourceSaving || choicesLoading">{{ sourceSaving ? '创建中…' : '创建并签发令牌' }}</button><button class="button button-secondary" type="button" :disabled="sourceSaving" @click="sourceForm = false">取消</button></div></form></section>
      <section v-if="issued" class="panel" aria-labelledby="operations-token-title"><div class="panel-heading"><h2 id="operations-token-title">来源令牌 · 仅显示这一次</h2></div><div class="operations-body"><p>请将令牌存入来源侧的秘密管理工具，再关闭此区域。</p><div class="field"><label for="operations-token">{{ issued.source.name }} 的访问令牌</label><input id="operations-token" ref="tokenInput" :value="issued.token" type="password" readonly autocomplete="off" spellcheck="false" @focus="tokenInput?.select()"></div><div class="form-actions"><button type="button" class="button button-primary" @click="copyToken">复制令牌</button><button type="button" class="button button-secondary" @click="issued = null; copyNotice = ''">我已保存，关闭显示</button></div><p v-if="copyNotice" role="status">{{ copyNotice }}</p></div></section>
      <section v-if="revokeTarget" class="panel" aria-labelledby="operations-revoke-title"><div class="panel-heading"><h2 id="operations-revoke-title">撤销 {{ revokeTarget.name }} 的令牌</h2></div><div class="operations-body"><p>撤销后，该来源不能继续上报或领取新的处置指令。已有处置的实际回滚结果仍须核对来源回执。</p><p v-if="revokeError" class="message message-error" role="alert">{{ revokeError }}</p><div class="form-actions"><button type="button" class="button button-primary" :disabled="revoking" @click="revokeSource">{{ revoking ? '撤销中…' : '确认撤销令牌' }}</button><button type="button" class="button button-secondary" :disabled="revoking" @click="revokeTarget = null">取消</button></div></div></section>
      <details class="panel operations-protocol"><summary>接入协议与执行边界</summary><div class="operations-body"><p>安全事件由已授权的来源上报，页面不提供手工构造事件的入口。仅发送规则、范围、窗口计数与观测时间，原始安全日志保留在来源侧。</p><p>接收地址：<code>{{ sourceEndpoint }}</code>。使用 <code>Authorization: Bearer</code> 认证，事件标识用于幂等去重。</p><p>Wazuh / 规范化事件可通过 <code>scarlet-event-bridge</code> 转发，将令牌放在环境变量 <code>SCARLET_OPERATIONS_TOKEN</code>。示例命令从标准输入读取来源数据：</p><pre><code>{{ bridgeCommand }}</code></pre><p>来源上报 <code>observed</code> 表示发现信号；<code>cleared</code> 表示在同一来源的后续窗口未再检出，仍按证据时序完成整改复测与独立复核。</p><p>PostgreSQL 执行来源绑定已配置的采集任务，平台只下发范围内的表级权限动作；执行器在客户侧使用数据库凭据，审批后的指令与回执分别留档。</p></div></details>
    </template>
  </div>
</template>

<style scoped>
.operations-view { display: grid; gap: 1rem; min-width: 0; }
.operations-view .page-header { margin-bottom: 0; }
.operations-tabs, .operations-checkbox { display: flex; flex-wrap: wrap; align-items: center; gap: .5rem; }
.operations-form { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: .9rem 1rem; padding: 1rem; }
.operations-form .field { margin: 0; min-width: 0; }
.operations-full { grid-column: 1 / -1; }
.operations-form .form-actions { margin-top: .2rem; }
.operations-scope { padding: .7rem .8rem; border-radius: .4rem; background: var(--surface-subtle); color: var(--secondary); font-size: .78rem; overflow-wrap: anywhere; }
.operations-metrics { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); padding: 1rem 0; }
.operations-metrics > div { padding: 0 1.25rem; }
.operations-metrics > div + div { border-left: 1px solid var(--line); }
.operations-metrics dt { color: var(--secondary); font-size: .8rem; }
.operations-metrics dd { margin-top: .2rem; font-size: 1.8rem; line-height: 1.25; font-weight: 700; font-variant-numeric: tabular-nums; }
.operations-summary-line { border-top: 1px solid var(--line); padding: .7rem 1rem; color: var(--secondary); font-size: .78rem; }
.operations-columns { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.2fr); gap: 1rem; }
.operations-trend-legend { display: flex; gap: 1rem; padding: .6rem 1rem; color: var(--secondary); font-size: .75rem; }
.operations-trend-legend > span { display: inline-flex; align-items: center; gap: .4rem; }
.operations-trend-legend > span::before { content: ''; width: .6rem; height: .6rem; border-radius: .15rem; background: var(--accent); }
.operations-trend-legend > span:last-child::before { background: var(--teal); }
.operations-trend { display: grid; gap: .7rem; margin: 0; padding: .7rem 1rem 1.2rem; list-style: none; }
.operations-trend > li { display: grid; grid-template-columns: 3rem minmax(0, 1fr) minmax(0, 1fr); align-items: center; gap: .6rem; font-size: .75rem; }
.operations-trend > li > span { position: relative; min-width: 0; min-height: 1.5rem; background: var(--surface-subtle); border-radius: .2rem; overflow: hidden; }
.operations-trend b { position: relative; display: block; padding: .2rem .4rem; font-weight: 400; }
.operations-trend-bar { position: absolute; inset: 0 auto 0 0; background: #d5e9f5; }
.operations-trend-bar-verified { background: #d8efe8; }
.operations-team-table { min-width: 26rem; }
.operations-events-table { min-width: 63rem; }
.operations-controls-table { min-width: 55rem; }
.operations-sources-table { min-width: 46rem; }
.operations-receipt { padding: 1rem; border-top: 1px solid var(--line); background: var(--surface-subtle); }
.operations-receipt p, .operations-receipt time { display: block; margin-top: .4rem; font-size: .8rem; color: var(--secondary); }
.operations-detail-actions { display: grid; justify-items: start; gap: .8rem; padding: 1rem; border-top: 1px solid var(--line); }
.operations-detail-actions > p { max-width: 70ch; font-size: .8rem; color: var(--secondary); }
.operations-detail-actions > form { width: min(100%, 40rem); }
.operations-body { display: grid; gap: .8rem; padding: 1rem; }
.operations-body .form-actions { margin-top: 0; }
.operations-body pre { max-width: 100%; overflow: auto; margin: 0; padding: .8rem; border: 1px solid var(--line); border-radius: .4rem; background: var(--surface-subtle); font-size: .78rem; }
.operations-protocol > summary { display: flex; align-items: center; min-height: 2.75rem; padding: .8rem 1rem; cursor: pointer; font-weight: 700; }
.operations-protocol code { overflow-wrap: anywhere; }
@media (max-width: 64rem) { .operations-columns { grid-template-columns: minmax(0, 1fr); } }
@media (max-width: 43.99rem) { .operations-form { grid-template-columns: minmax(0, 1fr); } .operations-metrics { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1rem 0; } .operations-metrics > div:nth-child(3) { border-left: 0; } .operations-metrics > div { padding-inline: 1rem; } .operations-metrics dd { font-size: 1.55rem; } .operations-tabs .button { flex: 1; } }
</style>
