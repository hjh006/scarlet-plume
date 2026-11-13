<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ApiError, describeError, formatDate, providerLabel, request, workspacePath, type Asset, type AssetPage, type Workspace } from './api'
import { activityKindLabels, type Activity, type ActivityPage } from './activity'
import { assessmentFactLabels, assessmentFactDisplayValue, assessmentFactSourceLabel, assessmentFactStateLabel, structuredAssessmentFact, type AssessmentFact } from './assessments'
import Icon from './Icon.vue'
import { useScenarioContext, type ScenarioMaterial, type ScenarioTaskData } from './scenario-context'
import { scenarios } from './scenario-navigation'
import { assetBusinessLevelLabels, assetCategoryLabels, assetStatutoryCategoryLabels } from './api'

interface ApplicationSource { id: number | string; name: string; assetIds: number[]; maxAgeMinutes: number; revokedAt?: string; lastObservedAt?: string; status: string }

const props = defineProps<{ workspace: Workspace; scenarioId?: string; taskId?: string; assetId?: string; activityId?: string; compact?: boolean; technical?: boolean }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const api = workspacePath(props.workspace.slug)
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const { context, setScope, href } = useScenarioContext(props.workspace.slug)
const currentScenario = computed(() => props.scenarioId ?? context.value.scene)
const currentTask = computed(() => props.taskId ?? context.value.task)
const scenarioTasks = computed(() => scenarios.find(scene => scene.id === currentScenario.value)?.tasks ?? [])
const supportedScenario = computed(() => ['personal', 'domestic', 'crossborder'].includes(currentScenario.value))
const materialStatusLabels = { auto_filled: '已自动带入', needs_confirmation: '需人工核实', missing: '待补充', not_applicable: '有据不适用' }
const materialHref = (page: string) => href(page, { scene: currentScenario.value, task: currentTask.value, assetId: selectedAssetId.value, activityId: selectedActivityId.value })
const owner = computed(() => props.workspace.role === 'owner')
const assets = ref<Asset[]>([])
const activities = ref<Activity[]>([])
const assetCursor = ref<number | null>(null)
const activityCursor = ref<number | null>(null)
const assetsLoading = ref(false)
const activitiesLoading = ref(false)
const assetsError = ref('')
const activitiesError = ref('')
const selectedAssetId = ref(props.assetId ?? context.value.assetId)
const selectedActivityId = ref(props.activityId ?? context.value.activityId)
const selectedAsset = computed(() => assets.value.find(item => String(item.id) === selectedAssetId.value))
const selectedActivity = computed(() => activities.value.find(item => String(item.id) === selectedActivityId.value))
const selectedScope = computed(() => selectedActivity.value ? `处理活动 · ${selectedActivity.value.name}` : selectedAssetId.value ? `资产 · ${selectedAsset.value?.name || '#' + selectedAssetId.value}` : '尚未选择资产或处理活动')
const registrationAssetId = computed(() => selectedActivity.value?.assetId ?? (selectedAssetId.value ? Number(selectedAssetId.value) : undefined))
const data = ref<ScenarioTaskData | null>(null)
const materialFilter = ref<'pending' | 'filled' | 'all'>('all')
const pendingMaterials = computed(() => data.value?.materials?.filter(item => item.status === 'missing' || item.status === 'needs_confirmation') ?? [])
const filledMaterials = computed(() => data.value?.materials?.filter(item => item.status === 'auto_filled') ?? [])
const visibleMaterials = computed(() => materialFilter.value === 'pending' ? pendingMaterials.value : materialFilter.value === 'filled' ? filledMaterials.value : data.value?.materials ?? [])
const loading = ref(false)
const error = ref('')
const applicationSources = ref<ApplicationSource[]>([])
const applicationLoading = ref(false)
const applicationError = ref('')
const sourceActionBusy = ref(false)
const sourceActionError = ref('')
const registrationName = ref('')
const setupSourceId = ref<string>('')
const visibleApplicationSources = computed(() => applicationSources.value.filter(item => !registrationAssetId.value || item.assetIds.includes(registrationAssetId.value)))
const setupSource = computed(() => visibleApplicationSources.value.find(item => String(item.id) === setupSourceId.value && !item.revokedAt && item.status !== 'revoked'))
const collectorExample = computed(() => setupSource.value?.assetIds[0] ? JSON.stringify({ items: [{ assetId: setupSource.value.assetIds[0], facts: { application_model: '<模型配置>' } }] }, null, 2) : '')
const tokenDialog = ref<HTMLDialogElement | null>(null)
const oneTimeToken = ref('')
const issuedSourceName = ref('')
const copied = ref(false)
const copyError = ref('')
let dataController: AbortController | undefined
let assetController: AbortController | undefined
let activityController: AbortController | undefined
let applicationController: AbortController | undefined
let actionController: AbortController | undefined

const statusLabels: Record<string, string> = {
  current: '近期有记录', never_run: '尚未采集', paused: '已暂停', failed: '采集失败',
  stale: '记录已过期', revoked: '已撤销', observing: '接收中',
}
const collectorSetup = 'SCARLET_URL=<采集器可访问的赤翎 HTTPS 源站地址>\nSCARLET_APPLICATION_TOKEN=<此来源的一次性令牌>\nSCARLET_APPLICATION_API_URL=<已批准的客户侧 HTTPS 只读快照 API>\nSCARLET_APPLICATION_API_TOKEN=<可选，仅客户侧调用业务 API 的 Bearer 令牌>'

function handleError(cause: unknown) {
  if (cause instanceof ApiError && cause.status === 401) {
    clearToken()
    emit('sessionExpired')
  }
  return describeError(cause)
}

async function loadAssets(append = false) {
  assetController?.abort()
  const operation = new AbortController()
  assetController = operation
  assetsLoading.value = true
  assetsError.value = ''
  try {
    const query = append && assetCursor.value !== null ? `?after=${assetCursor.value}` : ''
    const result = await request<AssetPage>(api + '/assets' + query, { signal: operation.signal })
    if (operation.signal.aborted) return
    assets.value = append ? [...assets.value, ...result.items] : result.items
    assetCursor.value = result.nextCursor ?? null
  } catch (cause) {
    if (!operation.signal.aborted) assetsError.value = handleError(cause)
  } finally {
    if (!operation.signal.aborted) assetsLoading.value = false
  }
}

async function loadActivities(append = false) {
  activityController?.abort()
  const operation = new AbortController()
  activityController = operation
  activitiesLoading.value = true
  activitiesError.value = ''
  try {
    const query = append && activityCursor.value !== null ? `?before=${activityCursor.value}` : ''
    const result = await request<ActivityPage>(api + '/activities' + query, { signal: operation.signal })
    if (operation.signal.aborted) return
    activities.value = append ? [...activities.value, ...result.items] : result.items
    activityCursor.value = result.nextCursor ?? null
  } catch (cause) {
    if (!operation.signal.aborted) activitiesError.value = handleError(cause)
  } finally {
    if (!operation.signal.aborted) activitiesLoading.value = false
  }
}

async function loadData() {
  dataController?.abort()
  const operation = new AbortController()
  dataController = operation
  loading.value = true
  error.value = ''
  data.value = null
  try {
    const query = new URLSearchParams()
    if (selectedActivityId.value) query.set('activityId', selectedActivityId.value)
    if (selectedAssetId.value) query.set('assetId', selectedAssetId.value)
    if ((supportedScenario.value || props.technical) && scenarios.some(scene => scene.id === currentScenario.value)) { query.set('scenario', currentScenario.value); if (currentTask.value) query.set('task', currentTask.value) }
    const result = await request<ScenarioTaskData>(api + '/scenario-data' + (query.size ? '?' + query.toString() : ''), { signal: operation.signal })
    if (!operation.signal.aborted) {
      data.value = result
      materialFilter.value = result.materials?.some(item => item.status === 'missing' || item.status === 'needs_confirmation') ? 'pending' : 'all'
    }
  } catch (cause) {
    if (!operation.signal.aborted) error.value = handleError(cause)
  } finally {
    if (!operation.signal.aborted) loading.value = false
  }
}

function selectActivity() {
  selectedAssetId.value = selectedActivity.value ? String(selectedActivity.value.assetId) : ''
  setupSourceId.value = ''
  sourceActionError.value = ''
  setScope({ assetId: selectedAssetId.value, activityId: selectedActivityId.value })
}

function selectAsset() {
  selectedActivityId.value = ''
  setupSourceId.value = ''
  sourceActionError.value = ''
  setScope({ assetId: selectedAssetId.value, activityId: '' })
}

function materialDetail(material: ScenarioMaterial) {
  const value = material.value ?? ''
  if (material.key === 'classification' && value) return structuredAssessmentFact(value) || value
  return structuredAssessmentFact(value) || (value.length > 240 || /^[\[{]/.test(value.trim()) ? value : '')
}
function materialSummary(material: ScenarioMaterial) {
  const classification = data.value?.classification
  if (material.key === 'classification' && classification) {
    const item = classification.item
    const taxonomy = item.taxonomy?.name || '未指定业务目录'
    return [assetCategoryLabels[item.asset.category || ''] || '属性待判定', assetBusinessLevelLabels[item.asset.businessLevel || ''] || '内部级别待判定', assetStatutoryCategoryLabels[item.asset.statutoryCategory || 'unknown'], '分类 v' + item.revision, taxonomy.length > 48 ? taxonomy.slice(0, 48) + '…' : taxonomy].join(' · ')
  }
  if (material.key === 'adopted_standard' && material.value && classification?.item.standard) {
    const standard = classification.item.standard
    return [standard.code, standard.title, standard.levelLabel, standard.ruleClause, standard.ruleTitle].filter(Boolean).join(' · ')
  }
  if (material.key === 'business_taxonomy' && material.value && classification?.item.taxonomy) return classification.item.taxonomy.path + ' · 目录 v' + classification.item.taxonomy.revision
  if (material.key === 'publication' && material.value && classification?.publication) return '#' + classification.publication.id + ' · ' + classification.publication.title + ' · 分类 v' + classification.publication.assetRevision + ' · ' + (classification.publication.current ? '当前版本相符' : '发布后有变化')
  if (/^[\[{]/.test(material.value?.trim() ?? '')) {
    const labels = [...new Set(data.value?.facts.filter(fact => material.factKeys.includes(fact.key) && fact.state === 'known' && fact.value).map(fact => assessmentFactLabels[fact.key] || fact.key) ?? [])]
    return (labels.join('、') || '结构化来源记录') + '已带入；请展开核对完整内容与观察范围。'
  }
  const value = material.value || '尚无可采用内容'
  return value.length > 240 ? value.slice(0, 220) + '…' : value
}

async function loadApplicationSources() {
  applicationController?.abort()
  const operation = new AbortController()
  applicationController = operation
  applicationLoading.value = true
  applicationError.value = ''
  try {
    const result = await request<{ items: ApplicationSource[] }>(api + '/application-sources', { signal: operation.signal })
    if (!operation.signal.aborted) applicationSources.value = result.items ?? []
  } catch (cause) {
    if (!operation.signal.aborted) applicationError.value = handleError(cause)
  } finally {
    if (!operation.signal.aborted) applicationLoading.value = false
  }
}

async function registerSource() {
  const assetId = registrationAssetId.value
  if (!owner.value || sourceActionBusy.value || !assetId || !registrationName.value.trim()) return
  actionController?.abort()
  const operation = new AbortController()
  actionController = operation
  sourceActionBusy.value = true
  sourceActionError.value = ''
  clearToken()
  try {
    const result = await request<{ source: ApplicationSource; token: string }>(api + '/application-sources', {
      method: 'POST', signal: operation.signal,
      body: JSON.stringify({ name: registrationName.value.trim(), assetIds: [assetId], maxAgeMinutes: 60 }),
    })
    if (operation.signal.aborted) return
    registrationName.value = ''
    setupSourceId.value = String(result.source.id)
    if (result.token) {
      issuedSourceName.value = result.source.name
      oneTimeToken.value = result.token
      await nextTick()
      if (operation.signal.aborted) return
      tokenDialog.value?.showModal()
    } else sourceActionError.value = '来源已创建，但本次响应没有令牌。请撤销该来源后重新创建。'
    await Promise.all([loadApplicationSources(), loadData()])
  } catch (cause) {
    if (!operation.signal.aborted) sourceActionError.value = handleError(cause)
  } finally {
    if (!operation.signal.aborted) sourceActionBusy.value = false
  }
}

async function revokeSource(source: ApplicationSource) {
  if (!owner.value || sourceActionBusy.value || source.revokedAt || source.status === 'revoked') return
  if (!window.confirm(`撤销后，此来源令牌将无法继续上报。确认撤销“${source.name}”？`)) return
  actionController?.abort()
  const operation = new AbortController()
  actionController = operation
  sourceActionBusy.value = true
  sourceActionError.value = ''
  try {
    await request<void>(api + '/application-sources/' + encodeURIComponent(String(source.id)), { method: 'DELETE', signal: operation.signal })
    if (operation.signal.aborted) return
    setupSourceId.value = ''
    await Promise.all([loadApplicationSources(), loadData()])
  } catch (cause) {
    if (!operation.signal.aborted) sourceActionError.value = handleError(cause)
  } finally {
    if (!operation.signal.aborted) sourceActionBusy.value = false
  }
}

function clearToken() {
  oneTimeToken.value = ''
  issuedSourceName.value = ''
  copied.value = false
  copyError.value = ''
}

function closeToken() {
  tokenDialog.value?.close()
  clearToken()
}

async function copyToken() {
  if (!oneTimeToken.value) return
  const value = oneTimeToken.value
  copyError.value = ''
  try {
    await navigator.clipboard.writeText(value)
    if (oneTimeToken.value === value) copied.value = true
  } catch {
    if (oneTimeToken.value === value) copyError.value = '无法写入剪贴板，请在支持剪贴板的安全连接中重试。'
  }
}

async function ensureSelections() {
  try {
    if (selectedActivityId.value && !selectedActivity.value) {
      const item = await request<Activity>(api + '/activities/' + encodeURIComponent(selectedActivityId.value), { signal: activityController?.signal })
      activities.value = [...activities.value.filter(value => value.id !== item.id), item]
      if (!selectedAssetId.value && props.assetId === undefined) { selectedAssetId.value = String(item.assetId); setScope({ assetId: selectedAssetId.value, activityId: selectedActivityId.value }) }
    }
    if (selectedAssetId.value && !selectedAsset.value) {
      const item = await request<{ asset: Asset }>(api + '/classification/assets/' + encodeURIComponent(selectedAssetId.value), { signal: assetController?.signal })
      assets.value = [...assets.value.filter(value => value.id !== item.asset.id), item.asset]
    }
  } catch (cause) { error.value = handleError(cause) }
}
watch(() => [context.value.assetId, context.value.activityId, props.assetId, props.activityId, currentScenario.value, currentTask.value], () => {
  selectedAssetId.value = props.assetId ?? context.value.assetId
  selectedActivityId.value = props.activityId ?? context.value.activityId
  if (!props.compact) void ensureSelections()
  void loadData()
})
onMounted(async () => {
  if (props.compact) { void loadData(); return }
  await Promise.all([loadAssets(), loadActivities()])
  await ensureSelections()
  void loadData(); void loadApplicationSources()
})
onBeforeUnmount(() => {
  dataController?.abort()
  assetController?.abort()
  activityController?.abort()
  applicationController?.abort()
  actionController?.abort()
  closeToken()
})
</script>

<template>
  <section class="panel scenario-data" aria-labelledby="scenario-data-title" :aria-busy="loading">
    <div class="panel-heading"><div><h2 id="scenario-data-title">{{ technical ? '检查范围与采集依据' : supportedScenario ? '检查证据与辅助资料' : '已接入来源与采集数据' }}</h2><p>{{ technical ? '选择已授权资产或处理活动，运行技术检查并追踪发现、整改与复测。' : supportedScenario ? '按所选范围引用已有事实，逐项核对检查证据、报告内容和待补条件。' : '读取客户侧采集结果，分别保留登记与来源观察。应用 API 返回的是配置声明，仍需与实际处理情况核对。' }}</p></div><button class="button button-secondary" type="button" :disabled="loading || sourceActionBusy" @click="loadData">{{ technical ? '刷新采集依据' : '刷新已采集数据' }}</button></div>

    <div v-if="!compact" class="scenario-data-scope">
      <label class="field" for="scenario-data-activity">按已有处理活动查看<select id="scenario-data-activity" v-model="selectedActivityId" :disabled="activitiesLoading || sourceActionBusy" @change="selectActivity"><option value="">选择处理活动</option><option v-if="selectedActivityId && !selectedActivity" :value="selectedActivityId">处理活动 #{{ selectedActivityId }} · 正在核对身份</option><option v-for="activity in activities" :key="activity.id" :value="String(activity.id)">{{ activity.name }} · {{ activityKindLabels[activity.kind] }} · {{ activity.assetName }}</option></select></label>
      <label class="field" for="scenario-data-asset">或按已有资产查看<select id="scenario-data-asset" v-model="selectedAssetId" :disabled="assetsLoading || sourceActionBusy" @change="selectAsset"><option value="">选择资产</option><option v-if="selectedAssetId && !selectedAsset" :value="selectedAssetId">{{ selectedActivity?.assetName || '资产 #' + selectedAssetId }} · 活动关联资产</option><option v-for="asset in assets" :key="asset.id" :value="String(asset.id)">{{ asset.name }} · {{ asset.system || asset.source }}</option></select></label>
      <div class="scenario-scope-feedback"><span v-if="assetsLoading || activitiesLoading" role="status">正在读取可选择的记录…</span><template v-if="assetsError"><p role="alert">资产列表：{{ assetsError }}</p><button type="button" class="button button-secondary" @click="loadAssets()">重试资产列表</button></template><template v-if="activitiesError"><p role="alert">活动列表：{{ activitiesError }}</p><button type="button" class="button button-secondary" @click="loadActivities()">重试活动列表</button></template><button v-if="assetCursor !== null" type="button" class="button button-secondary" :disabled="assetsLoading" @click="loadAssets(true)">加载更多资产</button><button v-if="activityCursor !== null" type="button" class="button button-secondary" :disabled="activitiesLoading" @click="loadActivities(true)">加载更多活动</button><p v-if="!assetsLoading && !activitiesLoading && !assetsError && !activitiesError && !assets.length && !activities.length">尚无可选择的资产或处理活动。先接入数据来源或在已有台账登记实际业务。</p></div>
      <p class="scenario-scope-caption">{{ selectedScope }}<span>{{ technical ? '选择范围不自动运行；点击运行检查后保存本次结果，来源覆盖与报告缺口分别列示。' : '选择范围仅用于读取已有事实，不会新建检查或评估。' }}</span></p>
    </div>

    <div v-if="technical && data?.classification" class="scenario-technical-classification"><strong>{{ data.classification.item.asset.name }}</strong><span>资产 #{{ data.assetId }} · 分类 v{{ data.classification.item.revision }}</span><span>{{ assetCategoryLabels[data.classification.item.asset.category || ''] || '数据属性待判定' }} · {{ assetBusinessLevelLabels[data.classification.item.asset.businessLevel || ''] || '内部级别待判定' }}</span><span class="badge" :class="data.classification.usable ? 'status-verified' : 'status-needs_work'">{{ data.classification.usable ? '当前分类依据可引用' : '分类依据需核实' }}</span></div>
    <slot v-if="technical" name="checks" :scope="{ assetId: selectedAssetId, activityId: selectedActivityId, ready: !!data && !loading && !error }" />
    <p v-if="loading" class="scenario-data-message" role="status">正在读取来源状态与所选范围的事实…</p>
    <div v-else-if="error" class="scenario-data-message"><p role="alert">{{ error }}</p><button type="button" class="button button-secondary" @click="loadData">重新读取</button></div>
    <template v-else-if="data">
      <component v-if="supportedScenario" :is="technical ? 'details' : 'div'" class="scenario-auxiliary-evidence"><summary v-if="technical">辅助证据与报告准备</summary><section class="scenario-materials-data" aria-labelledby="scenario-prepared-title">
        <div v-if="data.classification" class="scenario-classification-scope"><div><strong>{{ data.classification.item.asset.name }}</strong><span>资产 #{{ data.assetId }} · 分类 v{{ data.classification.item.revision }}</span></div><span class="badge" :class="data.classification.usable ? 'status-verified' : 'status-needs_work'">{{ data.classification.usable ? '分类依据可带入' : '分类依据需核对' }}</span><p>{{ assetCategoryLabels[data.classification.item.asset.category || ''] || '数据属性待判定' }} · {{ assetBusinessLevelLabels[data.classification.item.asset.businessLevel || ''] || '内部级别待判定' }} · {{ assetStatutoryCategoryLabels[data.classification.item.asset.statutoryCategory || 'unknown'] }}<span> · {{ data.classification.manuallyConfirmed ? '已人工确认' : data.classification.trust === 'legacy_declared' ? '历史登记值' : '候选 / 待确认' }}</span></p><p v-if="data.classification.publication">采用发布 #{{ data.classification.publication.id }} · {{ data.classification.publication.title }} · {{ data.classification.publication.current ? '当前版本相符' : '发布后有变化' }}</p><ul v-if="data.classification.warnings.length"><li v-for="warning in data.classification.warnings" :key="warning">{{ warning }}</li></ul></div>
        <div class="scenario-material-summary"><h3 id="scenario-prepared-title">{{ currentTask ? '本项辅助证据与报告内容' : '本场景辅助证据' }}</h3><p v-if="data.materialCounts"><span>已自动带入 <strong>{{ data.materialCounts.autoFilled }}</strong></span><span>需核实 <strong>{{ data.materialCounts.needsConfirmation }}</strong></span><span>待补充 <strong>{{ data.materialCounts.missing }}</strong></span><span v-if="data.materialCounts.notApplicable">有据不适用 <strong>{{ data.materialCounts.notApplicable }}</strong></span></p></div>
        <nav v-if="data.materials?.length" class="scenario-material-filters" aria-label="辅助证据状态筛选"><button type="button" :class="{active:materialFilter === 'pending'}" :aria-pressed="materialFilter === 'pending'" @click="materialFilter = 'pending'">待补充与需核实 <span>{{ pendingMaterials.length }}</span></button><button type="button" :class="{active:materialFilter === 'filled'}" :aria-pressed="materialFilter === 'filled'" @click="materialFilter = 'filled'">已带入 <span>{{ filledMaterials.length }}</span></button><button type="button" :class="{active:materialFilter === 'all'}" :aria-pressed="materialFilter === 'all'" @click="materialFilter = 'all'">全部 <span>{{ data.materials.length }}</span></button></nav>
        <div v-if="data.materials?.length" class="table-scroll" role="region" tabindex="0" aria-label="已获取与待补充的检查证据"><table class="data-table scenario-materials-table"><thead><tr><th scope="col">证据与报告内容</th><th scope="col">自动带入内容</th><th scope="col">采用来源与待补条件</th><th scope="col">核对入口</th></tr></thead><tbody><tr v-for="material in visibleMaterials" :key="material.key"><th scope="row">{{ material.title }}<span class="badge" :class="material.status === 'auto_filled' ? 'status-verified' : material.status === 'not_applicable' ? 'status-draft' : 'status-needs_work'">{{ materialStatusLabels[material.status] }}</span></th><td class="text-block">{{ materialSummary(material) }}<details v-if="materialDetail(material)" class="scenario-material-value"><summary>完整自动带入记录</summary><pre>{{ materialDetail(material) }}</pre></details></td><td><p>{{ material.guidance }}</p><details v-if="material.records.length || material.factKeys.length"><summary>查看采用记录</summary><ul><li v-for="record in material.records" :key="record">{{ record }}</li><li v-for="key in material.factKeys" :key="'fact:' + key">{{ assessmentFactLabels[key] || key }}</li></ul></details></td><td><a v-if="material.action.page" class="table-link" :href="materialHref(material.action.page)">{{ material.action.label }}</a></td></tr></tbody></table><p v-if="!visibleMaterials.length" class="scenario-collection-empty">此筛选下暂无材料，可切换查看全部。</p></div><p v-else-if="!selectedAssetId && !selectedActivityId" class="scenario-collection-empty">先选择实际资产或处理活动，再选择本次技术检查，读取对应材料与缺口。</p><div v-else-if="!currentTask" class="scenario-collection-empty"><p>已保留所选范围。请选择本场景的具体检查项，逐项核对已有证据和报告缺口。</p><nav class="scenario-material-task-links" aria-label="选择本场景技术检查"><a v-for="task in scenarioTasks" :key="task.id" class="table-link" :href="href('scenarios/' + currentScenario + '/' + task.id)">{{ task.label }}<Icon name="chevron"/></a></nav></div><p v-else class="scenario-collection-empty">当前检查未返回辅助证据清单，请刷新或核对相应技术检查入口。</p>
      </section></component>
      <component v-if="data.gaps.length" :is="technical ? 'details' : 'section'" class="scenario-data-gaps" aria-labelledby="scenario-gaps-title"><summary v-if="technical" id="scenario-gaps-title">辅助证据与来源缺口（{{ data.gaps.length }}）</summary><h3 v-else id="scenario-gaps-title">尚待接入或补充</h3><ul><li v-for="gap in data.gaps" :key="gap">{{ gap }}</li></ul></component>
      <details v-if="!compact" class="scenario-technical-details"><summary>来源连接与最近观察（技术核对）</summary><div class="scenario-source-section"><h3>来源连接与最近观察</h3><p>连接与近期上报不代表内容、权限和业务条件已全部核实；事实以所选范围逐项列示。</p><ul v-if="data.sources.length" class="scenario-source-connections" tabindex="0" aria-label="来源连接列表"><li v-for="source in data.sources" :key="source.id"><strong>{{ source.name }}</strong><span>{{ source.kind === 'application_api' ? '应用 API 采集' : providerLabel(source.kind) }} · {{ statusLabels[source.status] || '状态待确认' }}</span><time v-if="source.lastObservedAt" :datetime="source.lastObservedAt">最近观察 {{ formatDate(source.lastObservedAt) }}</time><span v-else>尚无来源观察时间</span></li></ul><p v-else class="scenario-collection-empty">当前没有返回已接入来源。请先核对工作区的数据接入与采集配置。</p><a class="table-link" :href="href('tools')">查看数据接入与采集工具<Icon name="chevron" /></a></div></details>

      <details v-if="!compact" class="scenario-technical-details"><summary>原始采集事实与来源明细</summary><section class="scenario-facts-section" aria-labelledby="scenario-facts-title"><h3 id="scenario-facts-title">所选范围的事实</h3><p v-if="!selectedActivityId && !selectedAssetId" class="scenario-collection-empty">选择已有资产或处理活动后，展示该范围的采集结果、业务登记、来源记录和观察时间。</p><div v-else-if="data.facts.length" class="table-scroll" role="region" aria-labelledby="scenario-facts-title" tabindex="0"><table class="data-table scenario-facts-table"><thead><tr><th scope="col">事实与状态</th><th scope="col">结果与范围</th><th scope="col">来源、记录与时点</th></tr></thead><tbody><tr v-for="fact in data.facts" :key="fact.key + ':' + fact.source + ':' + (fact.record || '')"><th scope="row">{{ assessmentFactLabels[fact.key] || fact.key }}<span class="scenario-fact-state" :class="{ 'is-unknown': fact.state !== 'known' }">{{ assessmentFactStateLabel(fact) }}</span></th><td><template v-if="structuredAssessmentFact(fact.value)"><p>{{ fact.state === 'known' ? '来源已返回记录，请展开核对范围与限制。' : '仍需核实，已保留来源说明。' }}</p><details class="scenario-fact-detail"><summary>查看来源明细</summary><pre>{{ structuredAssessmentFact(fact.value) }}</pre></details></template><template v-else><span v-if="fact.state !== 'known'" class="scenario-fact-unknown">待核实<span v-if="fact.value"> · {{ fact.value }}</span></span><span v-else>{{ assessmentFactDisplayValue(fact) || '来源未返回结果内容' }}</span></template></td><td>{{ assessmentFactSourceLabel(fact.source) }}<span v-if="fact.record" class="table-secondary">{{ fact.record }}</span><time v-if="fact.observedAt" class="table-secondary" :datetime="fact.observedAt">{{ formatDate(fact.observedAt) }}</time><span v-else class="table-secondary">观察时间未提供</span></td></tr></tbody></table></div><p v-else class="scenario-collection-empty">所选范围暂无可展示的事实，请核对关联来源、最近采集结果和材料缺口。</p></section></details>


    </template>

    <details v-if="!compact" class="scenario-technical-details"><summary>应用 API 来源与采集设置</summary><section class="scenario-application-sources" aria-labelledby="scenario-application-title"><div class="scenario-application-heading"><div><h3 id="scenario-application-title">应用 API 采集来源</h3><p>客户侧采集器按批准范围读取应用配置与服务清单，再将允许的事实字段上报赤翎。</p></div><button class="button button-secondary" type="button" :disabled="applicationLoading || sourceActionBusy" @click="loadApplicationSources">刷新来源</button></div><p v-if="applicationLoading" role="status">正在读取应用采集来源…</p><div v-else-if="applicationError"><p role="alert">{{ applicationError }}</p><button class="button button-secondary" type="button" @click="loadApplicationSources">重试读取来源</button></div><ul v-else-if="visibleApplicationSources.length" class="scenario-application-list"><li v-for="source in visibleApplicationSources" :key="source.id"><div><strong>{{ source.name }}</strong><span>{{ source.revokedAt ? '已撤销' : statusLabels[source.status] || '状态待确认' }} · 记录有效期 {{ source.maxAgeMinutes }} 分钟</span><time v-if="source.lastObservedAt" :datetime="source.lastObservedAt">最近观察 {{ formatDate(source.lastObservedAt) }}</time><span v-else>尚无应用 API 上报记录</span></div><div class="scenario-source-actions"><button v-if="!source.revokedAt && source.status !== 'revoked'" class="button button-secondary" type="button" :aria-expanded="setupSourceId === String(source.id)" @click="setupSourceId = setupSourceId === String(source.id) ? '' : String(source.id)">采集配置说明</button><button v-if="owner && !source.revokedAt && source.status !== 'revoked'" class="button button-secondary" type="button" :disabled="sourceActionBusy" @click="revokeSource(source)">撤销来源</button></div></li></ul><p v-else class="scenario-collection-empty">{{ registrationAssetId ? '此资产尚未配置应用 API 采集来源。' : '当前没有应用 API 采集来源。选择已有资产后，可由工作区负责人创建来源。' }}</p>

      <div v-if="setupSource" class="scenario-collector-setup"><h4>{{ setupSource.name }} · 客户侧采集配置</h4><p>允许上报的资产 ID：<code>{{ setupSource.assetIds.join('、') }}</code>。请按已批准的资产映射上报，不能凭名称自动匹配。</p><p>在能访问获准业务 API 的客户侧环境中设置下列变量。业务 API 的认证凭据只在客户侧配置，不提交到赤翎。HTTPS 地址不能包含账号、查询参数或片段。</p><pre>{{ collectorSetup }}</pre><p>运行采集器，每分钟读取并上报一次：</p><pre>scarlet-application-agent -interval 1m</pre><p>单次读取与上报核验可使用 <code>scarlet-application-agent -once</code>。</p><h4 class="scenario-response-example-title">响应格式示例（不是已采集结果）</h4><p>来源接口需返回 <code>application/json</code>，事实字段使用字符串。现有厂商 API 若不符合约定格式，需要在客户侧配置获批的只读适配接口与字段映射；填写任意 API 地址不会自动完成适配。</p><pre v-if="collectorExample">{{ collectorExample }}</pre><p>只上报允许的配置字段，不包含用户记录、对话正文或凭据。来源令牌仅在创建时显示一次；已有来源无法再次读取令牌，需要替换时由负责人撤销旧来源并创建新来源。</p></div>

      <form v-if="owner" class="scenario-source-registration" @submit.prevent="registerSource"><h4>接入应用 API 采集</h4><p v-if="!registrationAssetId">先选择已有资产或处理活动，来源将绑定其关联资产。</p><template v-else><p>绑定资产：{{ selectedActivity?.assetName || selectedAsset?.name || '所选资产' }}。上报事实按该范围核对，有效期 60 分钟。</p><label class="field" for="scenario-application-name">来源名称<input id="scenario-application-name" v-model="registrationName" maxlength="80" placeholder="例如，客服 AI 配置 API" autocomplete="off" required :disabled="sourceActionBusy"></label><button class="button button-primary" type="submit" :disabled="sourceActionBusy || !registrationName.trim()">{{ sourceActionBusy ? '正在处理…' : '创建来源与一次性令牌' }}</button></template><p v-if="sourceActionError" class="message message-error" role="alert">{{ sourceActionError }}</p></form><p v-else class="scenario-source-readonly">当前账号可读取来源与事实；创建和撤销来源由工作区负责人办理。</p>
    </section></details>

    <dialog ref="tokenDialog" class="scenario-token-dialog" aria-labelledby="scenario-token-title" @cancel="clearToken" @close="clearToken"><h2 id="scenario-token-title">保存一次性采集令牌</h2><p>{{ issuedSourceName }} 已创建。令牌只显示本次，关闭后将从当前页面清除。</p><label class="field" for="scenario-one-time-token">来源令牌<input id="scenario-one-time-token" :value="oneTimeToken" type="password" readonly autocomplete="off" spellcheck="false"></label><div class="scenario-token-actions"><button class="button button-primary" type="button" :disabled="!oneTimeToken" @click="copyToken">{{ copied ? '已复制令牌' : '复制令牌' }}</button><button class="button button-secondary" type="button" @click="closeToken">关闭并清除</button></div><p v-if="copied" role="status">令牌已复制。请仅在客户侧采集器环境中配置。</p><p v-if="copyError" role="alert">{{ copyError }}</p></dialog>
  </section>
</template>

<style scoped>
.scenario-data { min-width: 0; }
.scenario-data > .panel-heading { flex-wrap: wrap; align-items: start; }
.scenario-data > .panel-heading .button { flex: none; }
.scenario-data-scope { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px 20px; padding: 20px 22px; border-bottom: 1px solid var(--line); }
.scenario-data-scope .field { min-width: 0; margin: 0; font-size: 12px; font-weight: 600; }
.scenario-data-scope select { width: 100%; max-width: 100%; }
.scenario-scope-feedback, .scenario-scope-caption { grid-column: 1 / -1; min-width: 0; margin: 0; font-size: 12px; color: var(--secondary); line-height: 1.8; }
.scenario-scope-feedback { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 12px; }
.scenario-scope-feedback:empty { display: none; }
.scenario-scope-feedback p { margin: 0; }
.scenario-scope-caption { color: var(--ink); overflow-wrap: anywhere; }
.scenario-scope-caption span { display: block; margin-top: 4px; color: var(--secondary); }
.scenario-data-message { margin: 0; padding: 20px 22px; font-size: 13px; line-height: 1.8; }
.scenario-data-message .button { margin-top: 12px; }
.scenario-source-section, .scenario-facts-section, .scenario-data-gaps, .scenario-application-sources { padding: 20px 22px; border-top: 1px solid var(--line); min-width: 0; }
.scenario-data-scope + .scenario-source-section { border-top: 0; }
.scenario-source-section h3, .scenario-facts-section h3, .scenario-data-gaps h3, .scenario-application-sources h3 { margin: 0 0 10px; font-size: 15px; }
.scenario-source-section > p, .scenario-application-heading p { margin: 0 0 14px; color: var(--secondary); font-size: 12px; line-height: 1.9; }
.scenario-source-connections { max-height: 280px; overflow: auto; display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px 20px; padding: 0; margin: 0 0 14px; list-style: none; }
.scenario-source-connections li { display: grid; align-content: start; gap: 7px; min-width: 0; padding: 10px 0; font-size: 12px; line-height: 1.7; overflow-wrap: anywhere; }
.scenario-source-connections strong { font-size: 13px; }
.scenario-source-connections span, .scenario-source-connections time { color: var(--secondary); }
.scenario-source-section > .table-link { display: inline-flex; align-items: center; gap: 6px; min-height: 36px; font-size: 12px; }
.scenario-source-section > .table-link .icon { width: 15px; height: 15px; }
.scenario-collection-empty { margin: 0; color: var(--secondary); font-size: 13px; line-height: 1.9; }
.scenario-facts-section .table-scroll { max-height: 500px; overflow: auto; margin: 16px -22px -20px; max-width: calc(100% + 44px); }
.scenario-facts-table { min-width: 740px; table-layout: fixed; }
.scenario-facts-table thead th { position: sticky; top: 0; z-index: 1; }
.scenario-facts-table th:first-child { width: 23%; }
.scenario-facts-table th:nth-child(2) { width: 43%; }
.scenario-facts-table th:last-child { width: 34%; }
.scenario-facts-table :is(th, td) { vertical-align: top; overflow-wrap: anywhere; white-space: normal; }
.scenario-facts-table td p { margin: 0; }
.scenario-fact-state { display: block; margin-top: 6px; color: var(--secondary); font-size: 12px; font-weight: 400; }
.scenario-fact-state.is-unknown, .scenario-fact-unknown { color: var(--warning); }
.scenario-fact-detail { margin-top: 10px; }
.scenario-fact-detail summary { min-height: 32px; color: var(--accent); cursor: pointer; font-weight: 500; }
.scenario-fact-detail pre, .scenario-collector-setup pre { max-width: 100%; max-height: 280px; overflow: auto; margin: 10px 0 0; padding: 14px; border: 1px solid var(--line); border-radius: 7px; background: var(--surface-subtle); font-size: 12px; line-height: 1.8; white-space: pre-wrap; overflow-wrap: anywhere; }
.scenario-data-gaps { background: var(--surface-subtle); }
.scenario-data-gaps ul { margin: 0; padding-left: 20px; font-size: 13px; line-height: 1.9; }
.scenario-data-gaps li + li { margin-top: 8px; }
.scenario-application-heading { display: flex; align-items: start; justify-content: space-between; gap: 20px; }
.scenario-application-heading .button { flex: none; }
.scenario-application-list { margin: 0; padding: 0; list-style: none; }
.scenario-application-list li { display: flex; align-items: start; justify-content: space-between; gap: 16px; padding: 16px 0; border-top: 1px solid var(--line); }
.scenario-application-list li > div:first-child { display: grid; gap: 7px; min-width: 0; font-size: 12px; line-height: 1.8; overflow-wrap: anywhere; }
.scenario-application-list strong { font-size: 13px; }
.scenario-application-list span, .scenario-application-list time { color: var(--secondary); }
.scenario-source-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; flex: none; max-width: 260px; }
.scenario-collector-setup { margin-top: 16px; padding: 18px; background: var(--surface-subtle); border: 1px solid var(--line); border-radius: 8px; }
.scenario-collector-setup h4, .scenario-source-registration h4 { margin: 0 0 10px; font-size: 14px; }
.scenario-collector-setup .scenario-response-example-title { margin-top: 22px; }
.scenario-collector-setup p, .scenario-source-registration > p, .scenario-source-readonly { margin: 10px 0 0; color: var(--secondary); font-size: 12px; line-height: 1.9; }
.scenario-source-registration { display: grid; justify-items: start; gap: 14px; margin-top: 20px; padding-top: 20px; border-top: 1px solid var(--line); }
.scenario-source-registration > p { margin: 0; }
.scenario-source-registration .field { width: min(100%, 520px); min-width: 0; margin: 0; font-size: 12px; }
.scenario-source-registration input { width: 100%; }
.scenario-token-dialog { width: min(540px, calc(100vw - 40px)); padding: 26px; border: 1px solid var(--line); border-radius: 12px; background: var(--surface); color: var(--ink); }
.scenario-token-dialog::backdrop { background: rgb(12 22 49 / 42%); }
.scenario-token-dialog h2 { margin: 0 0 14px; font-size: 20px; }
.scenario-token-dialog p { margin: 12px 0; font-size: 13px; line-height: 1.8; }
.scenario-token-dialog .field { margin: 22px 0; font-size: 12px; }
.scenario-token-dialog input { width: 100%; }
.scenario-token-actions { display: flex; flex-wrap: wrap; gap: 10px; }
@media (max-width: 1399px) { .scenario-source-connections { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
@media (max-width: 699px) { .scenario-data-scope, .scenario-source-connections { grid-template-columns: minmax(0, 1fr); }.scenario-application-heading, .scenario-application-list li { flex-wrap: wrap; }.scenario-source-actions { justify-content: flex-start; } }
@media print { .scenario-token-dialog { display: none !important; } }
</style>

<style scoped>
.scenario-materials-data{padding:20px;border-bottom:1px solid var(--line);min-width:0}.scenario-classification-scope{padding:14px 16px;background:var(--surface-subtle);border:1px solid var(--line);border-radius:8px;font-size:12px;margin-bottom:18px;line-height:1.8}.scenario-classification-scope>div{display:flex;gap:12px;align-items:center;flex-wrap:wrap;margin-bottom:6px}.scenario-classification-scope>div>span{color:var(--secondary)}.scenario-classification-scope>p{margin-top:8px}.scenario-classification-scope ul{padding-left:18px;color:var(--warning);margin-top:8px}.scenario-material-summary{display:flex;justify-content:space-between;align-items:center;gap:12px;flex-wrap:wrap;margin-bottom:14px}.scenario-material-summary h3{font-size:15px}.scenario-material-summary p{display:flex;gap:14px;flex-wrap:wrap;font-size:12px;color:var(--secondary)}.scenario-material-summary strong{color:var(--ink);margin-left:5px}.scenario-materials-table{min-width:660px;table-layout:fixed;font-size:12px}.scenario-materials-table th:first-child{width:23%}.scenario-materials-table th:nth-child(2){width:27%}.scenario-materials-table th:nth-child(3){width:36%}.scenario-materials-table th:last-child{width:14%}.scenario-materials-table :is(th,td){overflow-wrap:anywhere;vertical-align:top}.scenario-materials-table .badge{display:table;margin-top:7px}.scenario-materials-table details{margin-top:8px;font-size:12px}.scenario-materials-table summary{cursor:pointer;color:var(--accent)}.scenario-materials-table ul{margin:8px 0;padding-left:14px;line-height:1.8}.scenario-materials-table td p{font-size:12px;line-height:1.8}
</style>

<style scoped>.scenario-technical-details{border-top:1px solid var(--line)}.scenario-technical-details>summary{padding:16px 22px;font-size:13px;color:var(--accent);cursor:pointer;font-weight:600}.scenario-technical-details .scenario-source-section,.scenario-technical-details .scenario-facts-section,.scenario-technical-details .scenario-application-sources{border-top:0;padding-top:0}</style>

<style scoped>.scenario-material-value pre{max-height:300px;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;font-size:12px;line-height:1.8;margin-top:10px}.scenario-material-task-links{display:flex;flex-wrap:wrap;gap:10px 18px;margin-top:12px}.scenario-material-task-links a{display:inline-flex;gap:5px;align-items:center}.scenario-material-task-links .icon{width:14px;height:14px}</style>

<style scoped>.scenario-material-filters{display:flex;flex-wrap:wrap;gap:8px;margin:0 0 14px}.scenario-material-filters button{display:inline-flex;align-items:center;gap:8px;min-height:40px;padding:8px 12px;border:1px solid var(--line);border-radius:6px;background:var(--surface);color:var(--secondary);font:inherit;font-size:12px;cursor:pointer}.scenario-material-filters button.active{border-color:var(--accent);background:var(--accent-soft);color:var(--accent);font-weight:650}.scenario-material-filters button span{font-variant-numeric:tabular-nums;font-weight:650}</style>

<style scoped>.scenario-technical-classification{display:flex;align-items:center;flex-wrap:wrap;gap:10px 14px;padding:14px 22px;border-bottom:1px solid var(--line);background:var(--surface-subtle);font-size:12px}.scenario-technical-classification>span:not(.badge){color:var(--secondary)}.scenario-auxiliary-evidence>summary{padding:16px 22px;font-size:13px;font-weight:600;color:var(--accent);cursor:pointer;border-top:1px solid var(--line)}</style>

<style scoped>details.scenario-data-gaps>summary{font-size:13px;color:var(--accent);font-weight:600;cursor:pointer}details.scenario-data-gaps>ul{margin-top:12px}</style>
