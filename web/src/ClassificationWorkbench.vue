<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ApiError, assetBusinessLevelLabels, assetCategoryLabels, assetStatutoryCategoryLabels, describeError, downloadFile, formatDate, providerLabel, request, workspacePath, type Workspace } from './api'
import { standardsByIndustry, type ClassificationStandards } from './classification-standards'
import { catalogKinds } from './discovery'
import { useScenarioContext } from './scenario-context'
import { availableClassificationStandards, classificationCandidateMethodLabel, emptyClassificationFilters, publicationStateLabels, publicationStatusLabels, taxonomyPath, type ClassificationPublication, type ClassificationTaxonomy, type ClassificationWorkbenchPage, type WorkbenchItem } from './classification-workbench'
import ClassificationEvaluation from './ClassificationEvaluation.vue'
import { postgresSensitivityLabel } from './postgresql'
import Icon from './Icon.vue'
import ClassificationEngineSettings from './ClassificationEngineSettings.vue'

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const base = workspacePath(props.workspace.slug) + '/classification'
const active = new AbortController()
const canEdit = computed(() => ['owner', 'operator'].includes(props.workspace.role))
const canReview = computed(() => ['owner', 'reviewer'].includes(props.workspace.role))
const tab = ref('ledger')
const tabs = [{ key: 'ledger', label: '资产台账' }, { key: 'distribution', label: '分类与分级分布' }, { key: 'publications', label: '发布记录' }, { key: 'taxonomy', label: '业务分类目录' }, { key: 'engine', label: '识别引擎与 AI' }]
const initialKind=new URLSearchParams(location.hash.split('?')[1]||'').get('kind')||''
const filters = reactive({...emptyClassificationFilters(),...(['file','table','field','business','application','database'].includes(initialKind)?{kind:initialKind}:{})})
const page = ref<ClassificationWorkbenchPage>()
const pageSize = ref(50)
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const notice = ref('')
const selectedId = ref('')
const checked = ref<string[]>([])
const selected = computed(() => page.value?.items.find(item => String(item.asset.id) === selectedId.value) ?? page.value?.items[0])
const selectedItems = computed(() => page.value?.items.filter(item => checked.value.includes(String(item.asset.id))) ?? [])
const allChecked = computed(() => !!page.value?.items.length && checked.value.length === page.value.items.length)
const pageCount = computed(() => Math.max(1, Math.ceil((page.value?.total ?? 0) / pageSize.value)))
const taxonomy = ref<ClassificationTaxonomy[]>([])
const activeTaxonomy = computed(() => taxonomy.value.filter(node => {
  let current: ClassificationTaxonomy | undefined = node
  const seen = new Set<string>()
  while (current) {
    if (!current.enabled || seen.has(String(current.id))) return false
    seen.add(String(current.id))
    current = current.parentId ? taxonomy.value.find(parent => parent.id === current?.parentId) : undefined
  }
  return true
}))
const objectKindLabels: Record<string, string> = { asset: '登记资产', ...catalogKinds }
const categoryLabels: Record<string, string> = { unknown: '待判定', ...assetCategoryLabels }
const levelLabels: Record<string, string> = { unknown: '待判定', ...assetBusinessLevelLabels }
const statutoryLabels: Record<string, string> = { ...assetStatutoryCategoryLabels }
const statusLabels: Record<string, string> = { pending: '待确认', reviewed: '人工确认', automatic: '自动识别', changed: '发布后有变化', unpublished: '尚未发布' }
const metrics = [{ key: 'total', label: '登记资产' }, { key: 'pending', label: '待确认' }, { key: 'reviewed', label: '人工确认' }, { key: 'automatic', label: '自动识别' }, { key: 'changed', label: '发布后变化' }, { key: 'unpublished', label: '尚未发布' }] as const
const distributionDimensions = [
  { key: 'category', title: '数据属性', labels: categoryLabels },
  { key: 'businessLevel', title: '企业内部敏感级别', labels: levelLabels },
  { key: 'statutoryCategory', title: '法定数据类别', labels: statutoryLabels },
] as const

const { context: scenarioContext, href: scopedHref } = useScenarioContext(props.workspace.slug)
const connectedScenarios = [{ id: 'personal', label: '个人信息保护' }, { id: 'domestic', label: '国内合规与迎检' }, { id: 'crossborder', label: '数据跨境合规' }]
function link(path: string) {
  const assetId = path.match(/^classification\/(\d+)$/)?.[1]
  return scopedHref(path, assetId ? { assetId, activityId: scenarioContext.value.assetId === assetId ? scenarioContext.value.activityId : '' } : {})
}

function failed(cause: unknown) {
  if (active.signal.aborted) return
  if (cause instanceof ApiError && cause.status === 401) { emit('sessionExpired'); return }
  error.value = describeError(cause)
}
function clearSelection() { checked.value = []; action.value = '' }
watch(filters, clearSelection)
async function load(targetPage = 1) {
  if (loading.value) return
  loading.value = true
  error.value = ''
  clearSelection()
  const query = new URLSearchParams({ page: String(targetPage), pageSize: String(pageSize.value) })
  for (const [key, value] of Object.entries(filters)) if (value.trim()) query.set(key, value.trim())
  try {
    page.value = await request<ClassificationWorkbenchPage>(base + '/workbench?' + query, { signal: active.signal })
    taxonomy.value = page.value.taxonomy ?? []
    if (!page.value.items.some(item => String(item.asset.id) === selectedId.value)) selectedId.value = ''
  } catch (cause) { page.value = undefined; failed(cause) }
  finally { loading.value = false }
}
function filterBy(key: 'category' | 'businessLevel' | 'statutoryCategory' | 'taxonomyId', value: string) {
  filters[key] = filters[key] === value ? '' : value
  tab.value = 'ledger'
  void load()
}
function reset() { Object.assign(filters, emptyClassificationFilters()); void load() }
function toggleAll() { checked.value = allChecked.value ? [] : (page.value?.items.map(item => String(item.asset.id)) ?? []) }
function itemPath(item: WorkbenchItem) { return [item.catalog?.database, item.catalog?.schema, item.catalog?.table, item.catalog?.field].filter(Boolean).join(' / ') }
function kindLabel(item: WorkbenchItem) { return objectKindLabels[item.catalog?.kind ?? ''] || '登记资产' }
function originLabel(item: WorkbenchItem) { return item.revision ? item.origin === 'automatic' ? '自动识别' : '已人工确认' : '待确认' }
function reviewReasons(item: WorkbenchItem) {
  const reasons: string[] = []
  if (item.catalog && !item.catalog.current) reasons.push('当前来源授权或采集批次需要更新')
  if (item.automatic?.stale) reasons.push('自动判定快照需要更新')
  if (item.automatic?.comparison === 'conflict') reasons.push('自动判定与当前分类存在差异')
  if (item.taxonomyStale) reasons.push('采用业务分类的目录版本已变化')
  if (item.needsReview && !reasons.length) reasons.push('分类后的当前来源事实需重新核对')
  return reasons
}
function facet(key: 'category' | 'businessLevel' | 'statutoryCategory' | 'source' | 'system' | 'kind') { return page.value?.facets[key] ?? [] }

const action = ref('')
const actionHeading = ref<HTMLElement>()
const standards = ref<ClassificationStandards>()
const standardsLoading = ref(false)
const draft = reactive({ category: 'unknown', businessLevel: 'unknown', statutoryCategory: 'unknown', basis: '', standardId: '', levelCode: '', ruleId: '', taxonomyId: '' })
const standardOptions = computed(() => availableClassificationStandards(standards.value))
const standardGroups = computed(() => standardsByIndustry(standardOptions.value))
const selectedStandard = computed(() => standardOptions.value.find(item => item.id === draft.standardId))
const selectedLevel = computed(() => selectedStandard.value?.levels?.find(item => item.code === draft.levelCode))
const rules = computed(() => selectedStandard.value?.rules?.filter(rule => !rule.levelCodes?.length || rule.levelCodes.includes(draft.levelCode)) ?? [])
const selectedRule = computed(() => rules.value.find(rule => rule.id === draft.ruleId))
const validStandard = computed(() => !!selectedStandard.value && (!selectedStandard.value.levels?.length || selectedLevel.value || !draft.levelCode && draft.statutoryCategory === 'unknown') && (!selectedStandard.value.rules?.length || selectedRule.value))
const publicationDraft = reactive({ title: '', note: '' })
const publicationIssues = computed(() => selectedItems.value.filter(item => item.needsReview || item.taxonomyStale || item.catalog && !item.catalog.current || item.automatic && (item.automatic.stale || item.automatic.comparison === 'conflict')).map(item => item.asset.name))
const publicationEligible = computed(() => !publicationIssues.value.length && selectedItems.value.length > 0 && selectedItems.value.every(item => item.revision > 0 && item.origin === 'manual' && item.asset.category && item.asset.category !== 'unknown' && item.asset.businessLevel && item.asset.businessLevel !== 'unknown' && item.standard))
async function openAction(next: string) {
  if (!selectedItems.value.length || busy.value) return
  error.value = ''; notice.value = ''; action.value = next
  await nextTick()
  actionHeading.value?.focus({ preventScroll: true })
  actionHeading.value?.scrollIntoView({ block: 'start', behavior: 'smooth' })
  if (next === 'review' && !standards.value) {
    standardsLoading.value = true
    try {
      standards.value = await request<ClassificationStandards>(base + '/standards', { signal: active.signal })
      draft.standardId = standards.value.defaultStandardId
    } catch (cause) { failed(cause) }
    finally { standardsLoading.value = false }
  }
}
async function batchReview() {
  if (!canEdit.value || !validStandard.value || busy.value || !selectedItems.value.length) return
  busy.value = true; error.value = ''; notice.value = ''
  try {
    await request(base + '/assets/batch-review', { method: 'POST', signal: active.signal, body: JSON.stringify({
      items: selectedItems.value.map(item => ({ assetId: Number(item.asset.id), revision: item.revision })),
      category: draft.category, businessLevel: draft.businessLevel, statutoryCategory: selectedLevel.value?.statutoryCategory ?? draft.statutoryCategory,
      basis: draft.basis.trim(), standardId: draft.standardId, levelCode: draft.levelCode || undefined, ruleId: draft.ruleId || undefined,
      taxonomyId: draft.taxonomyId ? Number(draft.taxonomyId) : undefined,
    }) })
    action.value = ''; await load(page.value?.page ?? 1); notice.value = '所选资产已统一确认，采用标准、经办人与分类版本已留存。'
  } catch (cause) { failed(cause) }
  finally { busy.value = false }
}
async function submitPublication() {
  if (!canEdit.value || !publicationEligible.value || busy.value) return
  busy.value = true; error.value = ''; notice.value = ''
  try {
    const result = await request<ClassificationPublication>(base + '/publications', { method: 'POST', signal: active.signal, body: JSON.stringify({ ...publicationDraft, items: selectedItems.value.map(item => ({ assetId: Number(item.asset.id), revision: item.revision })) }) })
    action.value = ''; clearSelection(); tab.value = 'publications'; await loadPublications(); await choosePublication(result.id)
    notice.value = '分类快照已提交，等待未参与分类确认的人员独立复核。'
  } catch (cause) { failed(cause) }
  finally { busy.value = false }
}

const publications = ref<ClassificationPublication[]>([])
const publication = ref<ClassificationPublication>()
const publicationsLoading = ref(false)
const publicationsLoaded = ref(false)
const reviewNote = ref('')
let publicationChoice = 0
async function loadPublications() {
  publicationsLoading.value = true
  try {
    const result = await request<{ items: ClassificationPublication[] }>(base + '/publications', { signal: active.signal })
    publications.value = result.items; publicationsLoaded.value = true
    const current = result.items.find(item => item.id === publication.value?.id) ?? result.items[0]
    if (current) await choosePublication(current.id)
  } catch (cause) { failed(cause) }
  finally { publicationsLoading.value = false }
}
async function choosePublication(id: number | string) {
  const choice = ++publicationChoice
  try {
    const result = await request<ClassificationPublication>(base + '/publications/' + encodeURIComponent(id), { signal: active.signal })
    if (choice === publicationChoice) { publication.value = result; reviewNote.value = '' }
  } catch (cause) { failed(cause) }
}
async function reviewPublication(action: 'publish' | 'reject') {
  if (!publication.value || !canReview.value || busy.value) return
  busy.value = true; error.value = ''; notice.value = ''
  try {
    await request(base + '/publications/' + encodeURIComponent(publication.value.id) + '/review', { method: 'POST', signal: active.signal, body: JSON.stringify({ action, note: reviewNote.value.trim() }) })
    await choosePublication(publication.value.id); await loadPublications(); await load(page.value?.page ?? 1)
    notice.value = action === 'publish' ? '独立复核已通过，冻结版本可供下载与下游使用。' : '该快照已退回，复核意见已保存。'
  } catch (cause) { failed(cause) }
  finally { busy.value = false }
}
async function exportPublication(format: 'csv' | 'json') {
  if (!publication.value || publication.value.status !== 'published' || busy.value) return
  busy.value = true; error.value = ''
  try { await downloadFile(base + '/publications/' + encodeURIComponent(publication.value.id) + '/export.' + format, `classification-v${publication.value.id}.${format}`, active.signal) }
  catch (cause) { failed(cause) }
  finally { busy.value = false }
}

const taxonomyForm = ref(false)
const taxonomyConflict = ref(false)
const taxonomyEditing = ref<ClassificationTaxonomy>()
const taxonomyDraft = reactive({ name: '', description: '', parentId: '', enabled: true })
function editTaxonomy(node?: ClassificationTaxonomy) {
  taxonomyEditing.value = node
  Object.assign(taxonomyDraft, { name: node?.name ?? '', description: node?.description ?? '', parentId: node?.parentId ? String(node.parentId) : '', enabled: node?.enabled ?? true })
  taxonomyForm.value = true; taxonomyConflict.value = false; error.value = ''; notice.value = ''
}
async function saveTaxonomy() {
  if (!canEdit.value || busy.value) return
  busy.value = true; error.value = ''; notice.value = ''
  try {
    const node = taxonomyEditing.value
    await request(base + '/taxonomy' + (node ? '/' + encodeURIComponent(node.id) : ''), { method: node ? 'PUT' : 'POST', signal: active.signal, body: JSON.stringify({ name: taxonomyDraft.name.trim(), description: taxonomyDraft.description.trim(), parentId: taxonomyDraft.parentId ? Number(taxonomyDraft.parentId) : null, enabled: taxonomyDraft.enabled, ...(node ? { revision: node.revision } : {}) }) })
    taxonomyForm.value = false; await load(page.value?.page ?? 1); notice.value = '业务分类目录已保存。已有分类保留采用时的目录版本。'
  } catch (cause) { taxonomyConflict.value = cause instanceof ApiError && cause.status === 409; failed(cause) }
  finally { busy.value = false }
}
async function reloadTaxonomy() {
  const id = taxonomyEditing.value?.id
  await load(page.value?.page ?? 1)
  const current = taxonomy.value.find(node => node.id === id)
  if (current) editTaxonomy(current)
}
watch(tab, next => { error.value = ''; if (next === 'publications' && !publicationsLoaded.value) void loadPublications() })
function applyDeepLinkKind(){const kind=new URLSearchParams(location.hash.split('?')[1]||'').get('kind');if(kind!==null&&['','file','table','field','business','application','database'].includes(kind)&&filters.kind!==kind){filters.kind=kind;clearSelection();void load(1)}}
onMounted(() => { void load();window.addEventListener('hashchange',applyDeepLinkKind) })
onBeforeUnmount(() => {active.abort();window.removeEventListener('hashchange',applyDeepLinkKind)})
</script>

<template>
  <section class="classification-workbench" :aria-busy="loading || busy">
    <div class="scope-strip workbench-scope"><span>统计范围 <strong>当前工作区全部登记资产</strong></span><span>业务场景用于导航，筛选条件决定本次台账范围。</span></div>
    <dl class="workbench-metrics" aria-label="工作区分类统计"><div v-for="metric in metrics" :key="metric.key"><dt>{{ metric.label }}</dt><dd>{{ page?.counts[metric.key] ?? '—' }}</dd></div></dl>
    <p class="workbench-capability-link"><a :href="scopedHref('capabilities?tab=knowledge')">分类知识与本地训练</a> · <a :href="scopedHref('capabilities?tab=browser')">页面字段标注</a> · <a :href="scopedHref('capabilities?tab=delivery')">已发布快照交付</a></p>
    <nav class="workbench-tabs" aria-label="分类分级工作视图"><button v-for="item in tabs" :key="item.key" type="button" :disabled="busy" :aria-pressed="tab === item.key" :class="{ active: tab === item.key }" @click="tab = item.key">{{ item.label }}</button></nav>
    <p v-if="error" class="message message-error workbench-message" role="alert">{{ error }}<button v-if="!busy && !loading" type="button" class="table-link" @click="tab === 'publications' ? loadPublications() : load(page?.page ?? 1)">重新读取</button></p>
    <p v-if="notice" class="message message-success workbench-message" role="status">{{ notice }}</p>

    <template v-if="tab === 'ledger'">
      <form class="panel workbench-filters" @submit.prevent="load()">
        <div class="workbench-search"><label for="classification-q">搜索台账</label><input id="classification-q" v-model="filters.q" :disabled="loading || busy" maxlength="100" placeholder="资产名、来源或库表字段"><button class="button button-primary" :disabled="loading || busy">查询</button><button class="button button-secondary" type="button" :disabled="loading || busy" @click="reset">重置</button></div>
        <fieldset class="workbench-filter-grid" :disabled="loading || busy"><legend class="visually-hidden">分类台账筛选条件</legend>
          <label>对象类型<select v-model="filters.kind"><option value="">全部对象</option><option v-for="entry in facet('kind')" :key="entry.key" :value="entry.key">{{ objectKindLabels[entry.key] || entry.key }}（{{ entry.count }}）</option></select></label>
          <label>来源<select v-model="filters.source"><option value="">全部来源</option><option v-for="entry in facet('source')" :key="entry.key" :value="entry.key">{{ entry.key || '未记录' }}（{{ entry.count }}）</option></select></label>
          <label>系统<select v-model="filters.system"><option value="">全部系统</option><option v-for="entry in facet('system')" :key="entry.key" :value="entry.key">{{ entry.key || '未记录' }}（{{ entry.count }}）</option></select></label>
          <label>数据属性<select v-model="filters.category"><option value="">全部属性</option><option v-for="(label, key) in categoryLabels" :key="key" :value="key">{{ label }}</option></select></label>
          <label>内部敏感级别<select v-model="filters.businessLevel"><option value="">全部级别</option><option v-for="(label, key) in levelLabels" :key="key" :value="key">{{ label }}</option></select></label>
          <label>法定类别<select v-model="filters.statutoryCategory"><option value="">全部类别</option><option v-for="(label, key) in statutoryLabels" :key="key" :value="key">{{ label }}</option></select></label>
          <label>确认与发布<select v-model="filters.status"><option value="">全部状态</option><option v-for="(label, key) in statusLabels" :key="key" :value="key">{{ label }}</option></select></label>
          <label>业务分类<select v-model="filters.taxonomyId"><option value="">全部业务分类</option><option v-for="node in taxonomy" :key="node.id" :value="String(node.id)">{{ taxonomyPath(node) }}{{ node.enabled ? '' : '（已停用）' }}</option></select></label>
        </fieldset>
      </form>
      <div class="workbench-body">
        <section class="panel workbench-ledger" aria-labelledby="workbench-ledger-title">
          <div class="panel-heading"><div><h2 id="workbench-ledger-title">分类分级资产台账</h2><p>{{ page ? '本次条件共 ' + page.total + ' 项 · 当前页 ' + page.items.length + ' 项' : '按来源与对象逐项核对分类结果' }}</p></div><button class="button button-secondary" type="button" :disabled="loading || busy" @click="load(page?.page ?? 1)"><Icon name="automation"/>刷新</button></div>
          <div class="workbench-selection"><span>当前页已选 <strong>{{ checked.length }}</strong> 项</span><div v-if="canEdit" class="workbench-inline-actions"><button class="button button-secondary" type="button" :disabled="!checked.length || busy || loading" @click="openAction('review')">批量确认</button><button class="button button-secondary" type="button" :disabled="!checked.length || busy || loading" @click="openAction('publish')">提交发布</button></div><span v-else class="field-hint">当前角色可查看{{ canReview ? '与独立复核' : '' }}</span></div>
          <p v-if="loading && !page" class="empty-state" role="status">正在读取分类台账…</p>
          <div v-else-if="page?.items.length" class="table-scroll workbench-ledger-scroll" role="region" tabindex="0" aria-labelledby="workbench-ledger-title">
            <table class="data-table workbench-table"><thead><tr><th scope="col" class="check-cell"><input type="checkbox" :checked="allChecked" aria-label="选择当前页全部资产" :disabled="busy || loading" @change="toggleAll"></th><th scope="col">资产与对象</th><th scope="col">分类与分级</th><th scope="col">业务分类</th><th scope="col">确认与发布</th><th scope="col">操作</th></tr></thead><tbody>
              <tr v-for="item in page.items" :key="item.asset.id" :class="{ selected: selected?.asset.id === item.asset.id }"><td class="check-cell"><input v-model="checked" type="checkbox" :value="String(item.asset.id)" :aria-label="'选择' + item.asset.name" :disabled="busy || loading"></td><th scope="row"><button class="workbench-select" type="button" :aria-pressed="selected?.asset.id === item.asset.id" @click="selectedId = String(item.asset.id)">{{ item.asset.name }}</button><span class="table-secondary">{{ kindLabel(item) }} · {{ item.catalog?.system || item.asset.system }}</span><span class="table-secondary workbench-path">{{ itemPath(item) || item.asset.sourceRef || '来源路径未记录' }}</span></th><td><strong>{{ categoryLabels[item.asset.category || 'unknown'] || item.asset.category }}</strong><span class="table-secondary">{{ levelLabels[item.asset.businessLevel || 'unknown'] || item.asset.businessLevel }} · {{ statutoryLabels[item.asset.statutoryCategory || 'unknown'] }}</span><span class="table-secondary">{{ item.candidateCount }} 项识别线索</span></td><td>{{ item.taxonomy ? taxonomyPath(item.taxonomy) : '待指定' }}<span v-if="item.taxonomy" class="table-secondary">目录 v{{ item.taxonomy.revision }}{{ item.taxonomyStale ? ' · 目录已变更' : '' }}</span></td><td><span class="badge" :class="item.origin === 'manual' && item.revision ? 'status-verified' : 'status-draft'">{{ originLabel(item) }}</span><span class="table-secondary">分类 v{{ item.revision }}</span><span class="table-secondary" :class="{ 'workbench-changed': item.publicationState === 'changed' }">{{ publicationStateLabels[item.publicationState] }}</span><span v-if="item.needsReview" class="badge status-needs_work">需重新核对</span></td><td><a class="table-link" :href="link('classification/' + item.asset.id)">{{ canEdit ? '确认 / 依据' : '查看依据' }}</a></td></tr>
            </tbody></table>
          </div><p v-else-if="!loading" class="empty-state">当前条件下没有分类资产。调整筛选，或先完成来源采集。</p>
          <div v-if="page" class="workbench-pagination"><label>每页<select v-model.number="pageSize" :disabled="loading || busy" @change="load()"><option :value="25">25</option><option :value="50">50</option><option :value="100">100</option></select>项</label><span>第 {{ page.page }} / {{ pageCount }} 页 · 共 {{ page.total }} 项</span><div><button class="button button-secondary" type="button" :disabled="page.page <= 1 || loading || busy" @click="load(page.page - 1)">上一页</button><button class="button button-secondary" type="button" :disabled="page.page >= pageCount || loading || busy" @click="load(page.page + 1)">下一页</button></div></div>
        </section>
        <aside class="context-rail workbench-context"><section class="panel"><div class="panel-heading"><div><h2>判定与来源</h2><p>选择资产核对本次采用依据</p></div></div><template v-if="selected"><div class="workbench-context-body"><h3>{{ selected.asset.name }}</h3><dl class="workbench-defs"><div><dt>资产身份</dt><dd>#{{ selected.asset.id }} · {{ kindLabel(selected) }}</dd></div><div><dt>来源</dt><dd>{{ selected.catalog?.source || selected.asset.source || '未记录' }}</dd></div><div><dt>对象路径</dt><dd>{{ itemPath(selected) || selected.asset.sourceRef || '未记录' }}</dd></div><div v-if="selected.catalog"><dt>来源批次</dt><dd>{{ selected.catalog.sourceId ? (selected.catalog.current ? '当前来源观察' : '历史 / 未观察') : '业务目录登记，未关联自动采集' }}<span class="table-secondary">{{ formatDate(selected.catalog.observedAt) }}<template v-if="selected.catalog.runId"> · 运行 #{{ selected.catalog.runId }}</template></span></dd></div><div><dt>采用标准</dt><dd>{{ selected.standard?.code || '尚未确认' }}<span v-if="selected.standard" class="table-secondary">{{ selected.standard.title }}</span></dd></div><div v-if="selected.standard?.ruleTitle"><dt>采用规则</dt><dd>{{ selected.standard.ruleClause }} · {{ selected.standard.ruleTitle }}</dd></div><div><dt>业务分类</dt><dd>{{ taxonomyPath(selected.taxonomy) }}<span v-if="selected.taxonomyStale" class="table-secondary workbench-changed">目录已调整，需核对采用版本</span></dd></div><div><dt>发布状态</dt><dd>{{ publicationStateLabels[selected.publicationState] }}</dd></div><div v-if="reviewReasons(selected).length"><dt>需重新核对</dt><dd><ul class="workbench-review-reasons"><li v-for="reason in reviewReasons(selected)" :key="reason">{{ reason }}</li></ul></dd></div></dl><a class="button button-primary" :href="link('classification/' + selected.asset.id)">查看判定与变更历史</a><div class="workbench-scenario-actions"><h4>按所选资产进入场景检查</h4><a v-for="scene in connectedScenarios" :key="scene.id" class="table-link" :href="scopedHref('scenarios/' + scene.id, { assetId: String(selected.asset.id), activityId: scenarioContext.assetId === String(selected.asset.id) ? scenarioContext.activityId : '' })">{{ scene.label }} <Icon name="chevron"/></a></div><details v-if="selected.automatic" class="workbench-evidence"><summary>自动判定、采集事实与缺口</summary><ClassificationEvaluation :automatic="selected.automatic" compact/></details><details v-if="selected.candidates?.length" class="workbench-evidence"><summary>识别线索（{{ selected.candidateCount }}）</summary><ul><li v-for="(candidate, index) in selected.candidates" :key="index"><strong>{{ candidate.name }}</strong><span>{{ categoryLabels[candidate.category] || postgresSensitivityLabel(candidate.category) }} · {{ providerLabel(candidate.source) }}<template v-if="candidate.sourceId != null"> · 来源 #{{ candidate.sourceId }}</template></span><span v-if="candidate.ruleKey">{{ candidate.ruleKey }}<template v-if="candidate.ruleVersion != null"> · v{{ candidate.ruleVersion }}</template></span><span v-if="candidate.method">{{ classificationCandidateMethodLabel(candidate.method) }}</span><span v-if="candidate.matchCount != null || candidate.scannedCount != null">命中 {{ candidate.matchCount ?? '—' }} / 已观察 {{ candidate.scannedCount ?? '—' }}</span><span v-if="candidate.current === false">历史线索，需重新采集</span><span v-if="candidate.runId" class="mono">运行 {{ candidate.runId }}</span><time>{{ formatDate(candidate.observedAt) }}</time></li></ul></details></div></template><p v-else class="empty-state">读取台账后，选择一项资产。</p></section><section class="panel workbench-context-note"><h3>三个独立维度</h3><p>数据属性、内部敏感级别与法定类别分别确认。来源规则命中是判定线索；重要与核心数据需要适用条款和业务事实。</p><a class="table-link" :href="link('discovery')">核对来源与识别规则 <Icon name="chevron"/></a></section></aside>
      </div>

      <section v-if="action" class="panel workbench-action" aria-labelledby="classification-batch-title"><div class="panel-heading"><div><h2 id="classification-batch-title" ref="actionHeading" tabindex="-1">{{ action === 'review' ? '批量确认当前所选资产' : '提交分类发布快照' }}</h2><p>当前页 {{ selectedItems.length }} 项：{{ selectedItems.slice(0, 3).map(item => item.asset.name).join('、') }}{{ selectedItems.length > 3 ? '等' : '' }}</p></div><button class="button button-secondary" type="button" :disabled="busy" @click="action = ''">取消</button></div>
        <form v-if="action === 'review'" class="workbench-action-form" @submit.prevent="batchReview"><p class="workbench-form-note">以下分类、标准与业务目录将统一用于所选资产。请确认它们具有相同的适用条件；任一资产版本变化时整批操作会拒绝。</p><p v-if="standardsLoading" role="status">正在读取可采用标准…</p><div class="workbench-form-grid"><label>采用标准<select v-model="draft.standardId" required :disabled="standardsLoading || busy" @change="draft.levelCode = ''; draft.ruleId = ''"><option value="" disabled>请选择</option><optgroup v-for="[industry, entries] in standardGroups" :key="industry" :label="industry"><option v-for="standard in entries" :key="standard.id" :value="standard.id">{{ standard.code }} · {{ standard.title }}</option></optgroup></select></label><label>标准等级<select v-model="draft.levelCode" :disabled="busy || !selectedStandard?.levels.length" @change="draft.ruleId = ''"><option value="">未选择 / 不适用</option><option v-for="level in selectedStandard?.levels" :key="level.code" :value="level.code">{{ level.label }}</option></select></label><label>采用规则<select v-model="draft.ruleId" :required="!!rules.length" :disabled="busy || !rules.length"><option value="">请选择 / 不适用</option><option v-for="rule in rules" :key="rule.id" :value="rule.id">{{ rule.clause }} · {{ rule.title }}</option></select></label><label>数据属性<select v-model="draft.category" :disabled="busy"><option v-for="(label, key) in categoryLabels" :key="key" :value="key">{{ label }}</option></select></label><label>企业内部敏感级别<select v-model="draft.businessLevel" :disabled="busy"><option v-for="(label, key) in levelLabels" :key="key" :value="key">{{ label }}</option></select></label><label>法定类别<select v-model="draft.statutoryCategory" :disabled="busy || !!selectedLevel?.statutoryCategory"><option v-for="(label, key) in statutoryLabels" :key="key" :value="key">{{ label }}{{ selectedLevel?.statutoryCategory === key ? ' · 标准等级采用值' : '' }}</option></select><span v-if="selectedLevel?.statutoryCategory" class="field-hint">实际采用：{{ statutoryLabels[selectedLevel.statutoryCategory] }}</span></label><label class="workbench-form-wide">业务分类<select v-model="draft.taxonomyId" :disabled="busy"><option value="">保留各资产当前业务分类</option><option value="0">清除所选资产业务分类</option><option v-for="node in activeTaxonomy" :key="node.id" :value="String(node.id)">{{ taxonomyPath(node) }}</option></select></label><label class="workbench-form-wide">共同判定依据<textarea v-model="draft.basis" required maxlength="400" rows="3" :disabled="busy" placeholder="填写实际适用条款、业务事实及审批记录"></textarea></label></div><div v-if="selectedRule" class="workbench-form-note"><strong>{{ selectedRule.clause }} · {{ selectedRule.title }}</strong><p>{{ selectedRule.criterion }}</p><p>取证要求：{{ selectedRule.evidence }}</p></div><div class="workbench-form-actions"><button class="button button-primary" :disabled="busy || standardsLoading || !validStandard || !draft.basis.trim()">{{ busy ? '正在保存…' : '确认所选 ' + selectedItems.length + ' 项资产' }}</button></div></form>
        <form v-else class="workbench-action-form" @submit.prevent="submitPublication"><p class="workbench-form-note">冻结所选资产的分类值、标准、业务分类与来源快照，提交给独立人员复核。发布通过后可导出 CSV 或 JSON 交付下游。</p><p v-if="!publicationEligible" class="message message-warning">所选资产须全部完成人工确认，并具有已知数据属性、内部级别和采用标准，来源与采用目录须仍有效。请先核对分类。</p><p v-if="publicationIssues.length" class="field-hint">来源、自动判定或业务目录需更新：{{ publicationIssues.join('、') }}</p><label>快照标题<input v-model="publicationDraft.title" required maxlength="120" :disabled="busy" placeholder="例如：本月客户数据分类确认"></label><label>发布说明<textarea v-model="publicationDraft.note" maxlength="1000" rows="3" :disabled="busy"></textarea></label><div class="workbench-form-actions"><button class="button button-primary" :disabled="busy || !publicationEligible">{{ busy ? '正在提交…' : '提交独立复核' }}</button></div></form>
      </section>
    </template>

    <section v-else-if="tab === 'distribution'" class="panel"><div class="panel-heading"><div><h2>工作区分类与分级分布</h2><p>按全部登记资产统计，点击一项进入对应筛选台账。各维度独立展示。</p></div></div><div class="workbench-distributions"><section v-for="dimension in distributionDimensions" :key="dimension.key"><h3>{{ dimension.title }}</h3><button v-for="entry in facet(dimension.key)" :key="entry.key" class="workbench-distribution-row" type="button" @click="filterBy(dimension.key, entry.key)"><span>{{ dimension.labels[entry.key] || entry.key }}</span><strong>{{ entry.count }}</strong><span class="workbench-bar-track"><span :style="{ width: Math.max(1, entry.count / Math.max(1, page?.counts.total ?? 0) * 100) + '%' }"></span></span></button><p v-if="!facet(dimension.key).length" class="empty-state">暂无登记分布。</p></section></div><div class="workbench-distribution-note"><strong>重要与核心数据 {{ page?.counts.importantOrCore ?? '—' }} 项</strong><span>以已登记法定类别为准。格式命中数不作为重要或核心数据的自动认定数量。</span></div></section>

    <div v-else-if="tab === 'publications'" class="workbench-publications"><section class="panel"><div class="panel-heading"><div><h2>发布与独立复核</h2><p>展示最近 50 个不可变分类快照</p></div><button class="button button-secondary" type="button" :disabled="publicationsLoading || busy" @click="loadPublications">刷新</button></div><p v-if="publicationsLoading && !publicationsLoaded" class="empty-state" role="status">正在读取发布记录…</p><div class="workbench-publication-list"><button v-for="entry in publications" :key="entry.id" type="button" :class="{ selected: publication?.id === entry.id }" @click="choosePublication(entry.id)"><span class="workbench-publication-title"><strong>{{ entry.title }}</strong><span class="badge" :class="entry.status === 'published' ? 'status-verified' : 'status-draft'">{{ publicationStatusLabels[entry.status] }}</span></span><span>{{ entry.itemCount }} 项 · {{ entry.creator }} · {{ formatDate(entry.createdAt) }}</span><span>快照 #{{ entry.id }}</span></button><p v-if="publicationsLoaded && !publications.length" class="empty-state">暂无发布快照。从资产台账选择已人工确认的资产后提交发布。</p></div></section>
      <section class="panel workbench-publication-detail"><template v-if="publication"><div class="panel-heading"><div><h2>{{ publication.title }}</h2><p>快照 #{{ publication.id }} · {{ publicationStatusLabels[publication.status] }}</p></div><div v-if="publication.status === 'published'" class="workbench-inline-actions"><button class="button button-secondary" type="button" :disabled="busy" @click="exportPublication('csv')">导出 CSV</button><button class="button button-secondary" type="button" :disabled="busy" @click="exportPublication('json')">导出 JSON</button></div></div><div class="workbench-publication-meta"><dl class="workbench-defs"><div><dt>提交人</dt><dd>{{ publication.creator }} · {{ formatDate(publication.createdAt) }}</dd></div><div><dt>发布说明</dt><dd>{{ publication.note || '未填写' }}</dd></div><div><dt>快照摘要</dt><dd class="mono">{{ publication.snapshotDigest }}</dd></div><div v-if="publication.reviewer"><dt>独立复核</dt><dd>{{ publication.reviewer }} · {{ publication.reviewedAt ? formatDate(publication.reviewedAt) : '' }}</dd></div><div v-if="publication.reviewNote"><dt>复核意见</dt><dd>{{ publication.reviewNote }}</dd></div></dl><p class="field-hint">下列内容固定为提交时版本。后续采集或分类变更在资产台账单独显示；导出提供已发布快照，供下游校验和导入。</p></div><div v-if="publication.items?.length" class="table-scroll" role="region" tabindex="0" aria-label="发布快照资产"><table class="data-table workbench-snapshot-table"><thead><tr><th>资产</th><th>分类与级别</th><th>采用标准 / 版本</th><th>业务分类</th></tr></thead><tbody><tr v-for="item in publication.items" :key="item.asset.id"><th scope="row">{{ item.asset.name }}<span class="table-secondary">#{{ item.asset.id }} · {{ item.asset.system }}</span></th><td>{{ categoryLabels[item.asset.category || 'unknown'] }}<span class="table-secondary">{{ levelLabels[item.asset.businessLevel || 'unknown'] }} · {{ statutoryLabels[item.asset.statutoryCategory || 'unknown'] }}</span></td><td>{{ item.standard?.code || '未记录' }}<span class="table-secondary">分类 v{{ item.revision }}</span></td><td>{{ taxonomyPath(item.taxonomy) }}</td></tr></tbody></table></div><form v-if="publication.status === 'submitted' && canReview" class="workbench-review-form" @submit.prevent="reviewPublication('publish')"><h3>独立复核</h3><p class="field-hint">复核人须独立于提交人及所选资产的人工分类确认人，服务端将核验。</p><label>复核意见<textarea v-model="reviewNote" maxlength="1000" rows="3" :disabled="busy"></textarea></label><div class="workbench-form-actions"><button class="button button-secondary" type="button" :disabled="busy || !reviewNote.trim()" @click="reviewPublication('reject')">退回并记录意见</button><button class="button button-primary" :disabled="busy">{{ busy ? '处理中…' : '通过复核并发布' }}</button></div></form><p v-else-if="publication.status === 'submitted'" class="workbench-publication-meta field-hint">等待有复核权限且未参与本次分类确认的人员审核。</p></template><p v-else class="empty-state">选择一份快照，查看固定版本与复核记录。</p></section>
    </div>

    <ClassificationEngineSettings v-else-if="tab === 'engine'" :workspace="workspace" :asset="selected" @session-expired="emit('sessionExpired')" @select-asset="tab = 'ledger'" />

    <div v-else-if="tab === 'taxonomy'" class="workbench-taxonomy"><section class="panel"><div class="panel-heading"><div><h2>业务分类目录</h2><p>按实际业务建立分类层次，最多 200 个节点、5 层。</p></div><button v-if="canEdit" class="button button-primary" type="button" :disabled="busy" @click="editTaxonomy()">新增分类</button></div><div class="table-scroll"><table class="data-table workbench-taxonomy-table"><thead><tr><th>分类路径</th><th>状态与版本</th><th>操作</th></tr></thead><tbody><tr v-for="node in taxonomy" :key="node.id"><th scope="row">{{ taxonomyPath(node) }}<span class="table-secondary">节点 #{{ node.id }}</span></th><td><span class="badge" :class="node.enabled ? 'status-verified' : 'status-draft'">{{ node.enabled ? '启用' : '已停用' }}</span><span class="table-secondary">目录 v{{ node.revision }}</span></td><td><div class="workbench-inline-actions"><button class="table-link" type="button" @click="filterBy('taxonomyId', String(node.id))">查看资产</button><button v-if="canEdit" class="table-link" type="button" :disabled="busy" @click="editTaxonomy(node)">编辑</button></div></td></tr></tbody></table><p v-if="!taxonomy.length" class="empty-state">尚未建立业务分类目录。有权限的人员可创建分类，再在分类确认中采用。</p></div></section><section class="panel workbench-taxonomy-editor"><template v-if="taxonomyForm"><div class="panel-heading"><h2>{{ taxonomyEditing ? '编辑业务分类' : '新增业务分类' }}</h2></div><form class="workbench-action-form" @submit.prevent="saveTaxonomy"><label>分类名称<input v-model="taxonomyDraft.name" required maxlength="80" :disabled="busy"></label><label>上级分类<select v-model="taxonomyDraft.parentId" :disabled="busy"><option value="">顶层分类</option><option v-for="node in taxonomy.filter(item => item.id !== taxonomyEditing?.id)" :key="node.id" :value="String(node.id)">{{ taxonomyPath(node) }}</option></select></label><label>分类说明<textarea v-model="taxonomyDraft.description" maxlength="400" rows="3" :disabled="busy"></textarea></label><label class="workbench-checkbox"><input v-model="taxonomyDraft.enabled" type="checkbox" :disabled="busy">启用此分类</label><p class="field-hint">编辑使用目录版本校验。停用后保留历史采用记录，不供新的分类确认选择。</p><div class="workbench-form-actions"><button v-if="taxonomyConflict" class="button button-secondary" type="button" :disabled="busy" @click="reloadTaxonomy">重新读取并替换草稿</button><button class="button button-secondary" type="button" :disabled="busy" @click="taxonomyForm = false">取消</button><button class="button button-primary" :disabled="busy || taxonomyConflict || !taxonomyDraft.name.trim()">{{ busy ? '保存中…' : '保存分类' }}</button></div></form></template><div v-else class="workbench-context-note"><h3>业务分类与法定类别分别管理</h3><p>业务目录表达组织业务层次，采用时固定节点与版本。目录名称和层次不替代个人信息属性、保护级别或法定类别的判定。</p></div></section></div>
  </section>
</template>

<style scoped>
.classification-workbench{display:grid;gap:16px;min-width:0}.workbench-scope{display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:10px;padding:12px 16px;border:1px solid var(--line);border-radius:10px;background:var(--surface);font-size:12px;color:var(--secondary)}.workbench-scope strong{margin-left:10px;color:var(--ink);font-weight:600}.workbench-metrics{display:grid;grid-template-columns:repeat(6,minmax(0,1fr));margin:0;background:var(--surface);border:1px solid var(--line);border-radius:12px}.workbench-metrics>div{padding:14px 18px}.workbench-metrics>div+div{border-left:1px solid var(--line)}.workbench-metrics dt{font-size:12px;color:var(--secondary)}.workbench-metrics dd{margin:4px 0 0;color:var(--ink);font-size:23px;font-weight:650;line-height:1.35;font-variant-numeric:tabular-nums}.workbench-metrics>div:nth-child(3) dd{color:var(--teal)}.workbench-metrics>div:nth-child(5) dd{color:var(--violet)}.workbench-tabs{display:flex;gap:24px;padding:0 4px;border-bottom:1px solid var(--line);overflow:auto}.workbench-tabs button{flex-shrink:0;min-height:44px;border:0;border-bottom:2px solid transparent;background:none;color:var(--secondary);font:inherit;font-size:13px;cursor:pointer}.workbench-tabs button.active{border-color:var(--accent);color:var(--accent);font-weight:700}.workbench-message{margin:0;display:flex;justify-content:space-between;gap:16px}.workbench-message .table-link{flex-shrink:0}.workbench-filters{padding:16px;display:grid;gap:14px}.workbench-search{display:flex;align-items:center;gap:10px}.workbench-search>label{font-size:12px;font-weight:650;white-space:nowrap}.workbench-search>input{max-width:420px;flex:1}.workbench-filter-grid{border:0;padding:0;margin:0;min-width:0;display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:12px}.classification-workbench label:not(.workbench-checkbox){display:grid;gap:5px;font-size:12px;color:var(--secondary);min-width:0}.classification-workbench .workbench-search>label{display:block;color:var(--ink)}.classification-workbench :is(input:not([type=checkbox]),select,textarea){min-width:0;max-width:100%;min-height:38px;border:1px solid var(--input-line);border-radius:6px;padding:7px 9px;background:var(--surface);color:var(--ink);font:inherit;font-size:12px}.classification-workbench textarea{width:100%;resize:vertical}.workbench-body{display:grid;grid-template-columns:minmax(0,1fr) 290px;gap:16px;align-items:start}.workbench-body>*{min-width:0}.workbench-ledger .panel-heading{align-items:center}.workbench-selection{display:flex;justify-content:space-between;align-items:center;gap:12px;padding:10px 16px;border-block:1px solid var(--line);font-size:12px;background:var(--surface-subtle)}.workbench-selection strong{color:var(--accent);margin-inline:4px}.workbench-inline-actions{display:flex;align-items:center;gap:8px;flex-wrap:wrap}.workbench-selection .button{min-height:34px;padding:5px 10px;font-size:12px}.workbench-ledger-scroll{max-height:600px;overflow:auto}.workbench-table{min-width:760px;table-layout:fixed}.workbench-table .check-cell{width:36px;padding-inline:10px}.workbench-table th:nth-child(2){width:27%}.workbench-table th:nth-child(3){width:23%}.workbench-table th:nth-child(4){width:18%}.workbench-table th:nth-child(5){width:19%}.workbench-table th:last-child{width:13%}.workbench-table :is(th,td){overflow-wrap:anywhere;vertical-align:top}.workbench-table tbody th{background:transparent}.workbench-table tr.selected{background:var(--accent-soft)}.workbench-table tr.selected>td:first-child{box-shadow:inset 1px 0 var(--accent)}.workbench-table input[type=checkbox]{width:16px;height:16px;accent-color:var(--accent);cursor:pointer}.workbench-select{border:0;background:none;text-align:left;padding:0;font:inherit;font-weight:650;color:var(--ink);cursor:pointer;overflow-wrap:anywhere}.workbench-select:hover{color:var(--accent)}.workbench-path{font-size:11px}.workbench-changed{color:var(--violet)}.workbench-review-reasons{margin:0;padding-left:15px;color:var(--warning);line-height:1.8}.workbench-pagination{display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:10px;padding:12px 16px;border-top:1px solid var(--line);font-size:12px;color:var(--secondary)}.workbench-pagination>label{display:flex!important;align-items:center;gap:6px!important}.workbench-pagination>div{display:flex;gap:8px}.workbench-pagination .button{min-height:36px;padding:6px 10px;font-size:12px}.workbench-pagination select{min-height:32px;padding:3px 7px}.workbench-context{display:grid;gap:16px;min-width:0}.workbench-context-body{padding:16px;min-width:0}.workbench-context-body h3{font-size:14px;overflow-wrap:anywhere}.workbench-defs{display:grid;margin:10px 0 16px;font-size:12px}.workbench-defs>div{display:grid;grid-template-columns:70px minmax(0,1fr);gap:8px;padding:10px 0;border-bottom:1px solid var(--line)}.workbench-defs dt{color:var(--secondary)}.workbench-defs dd{margin:0;overflow-wrap:anywhere;white-space:pre-wrap}.workbench-context-body>.button{width:100%;font-size:12px}.workbench-context-note{padding:18px;font-size:12px;line-height:1.8}.workbench-context-note h3{font-size:14px}.workbench-context-note p{color:var(--secondary);margin:8px 0 12px}.workbench-context-note .table-link{display:inline-flex;align-items:center;gap:6px}.workbench-evidence{margin-top:14px;padding-top:12px;border-top:1px solid var(--line);font-size:12px}.workbench-evidence summary{cursor:pointer;color:var(--accent);line-height:1.7}.workbench-evidence ul{margin:10px 0;padding:0;list-style:none;display:grid;gap:10px}.workbench-evidence li{display:grid;gap:3px;overflow-wrap:anywhere}.workbench-evidence li span,.workbench-evidence time{color:var(--secondary)}.workbench-action{scroll-margin-top:85px}.workbench-action-form{display:grid;gap:16px;padding:20px}.workbench-form-grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:16px}.workbench-form-wide{grid-column:1/-1}.workbench-form-note{font-size:12px;line-height:1.8;color:var(--secondary);background:var(--surface-subtle);padding:12px 14px;border-radius:6px;margin:0}.workbench-form-note p{margin:6px 0 0}.workbench-form-actions{display:flex;justify-content:flex-end;gap:10px;flex-wrap:wrap}.workbench-distributions{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:28px;padding:24px}.workbench-distributions h3{font-size:14px;margin-bottom:18px}.workbench-distribution-row{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:8px 12px;width:100%;margin:0 0 18px;padding:0;border:0;background:none;text-align:left;font:inherit;font-size:12px;color:var(--ink);cursor:pointer}.workbench-distribution-row strong{font-variant-numeric:tabular-nums;font-weight:650}.workbench-bar-track{grid-column:1/-1;height:8px;overflow:hidden;background:var(--surface-subtle);border-radius:2px}.workbench-bar-track>span{display:block;height:100%;background:var(--accent);border-radius:2px}.workbench-distributions>section:nth-child(2) .workbench-bar-track>span{background:var(--teal)}.workbench-distributions>section:nth-child(3) .workbench-bar-track>span{background:var(--violet)}.workbench-distribution-note{display:flex;flex-wrap:wrap;gap:12px;padding:16px 24px;border-top:1px solid var(--line);font-size:12px;color:var(--secondary)}.workbench-distribution-note strong{color:var(--ink)}.workbench-publications{display:grid;grid-template-columns:310px minmax(0,1fr);gap:16px;align-items:start}.workbench-publications>*{min-width:0}.workbench-publication-list{max-height:700px;overflow:auto}.workbench-publication-list>button{display:grid;gap:6px;width:100%;padding:16px;border:0;border-bottom:1px solid var(--line);background:none;text-align:left;color:var(--secondary);font:inherit;font-size:12px;cursor:pointer}.workbench-publication-list>button.selected{background:var(--accent-soft)}.workbench-publication-title{display:flex;gap:8px;justify-content:space-between;align-items:start;flex-wrap:wrap}.workbench-publication-title strong{color:var(--ink);font-size:13px;overflow-wrap:anywhere}.workbench-publication-meta{padding:2px 20px 16px;font-size:12px}.workbench-publication-meta .workbench-defs>div{grid-template-columns:85px minmax(0,1fr)}.workbench-snapshot-table{min-width:550px;table-layout:fixed}.workbench-snapshot-table :is(th,td){overflow-wrap:anywhere;vertical-align:top}.workbench-review-form{padding:20px;border-top:1px solid var(--line);display:grid;gap:12px}.workbench-review-form h3{font-size:14px}.workbench-taxonomy{display:grid;grid-template-columns:minmax(0,1fr) 330px;gap:16px;align-items:start}.workbench-taxonomy>*{min-width:0}.workbench-taxonomy-table{min-width:450px;table-layout:fixed}.workbench-taxonomy-table th:first-child{width:53%}.workbench-taxonomy-table th:nth-child(2){width:23%}.workbench-taxonomy-table :is(th,td){overflow-wrap:anywhere}.workbench-checkbox{display:flex;align-items:center;gap:8px;font-size:12px}.classification-workbench button.table-link{border:0;background:none;padding:0;font:inherit;color:var(--accent);cursor:pointer;font-size:12px}.classification-workbench .table-secondary{font-size:12px}.workbench-ledger .panel-heading p,.workbench-context .panel-heading p{font-size:12px}.workbench-action .panel-heading p{overflow-wrap:anywhere}
@media(max-width:1399px){.workbench-body{grid-template-columns:minmax(0,1fr) 260px}.workbench-metrics>div{padding:13px}.workbench-publications{grid-template-columns:270px minmax(0,1fr)}}
@media(max-width:1099px){.workbench-body{grid-template-columns:minmax(0,1fr)}.workbench-context{grid-template-columns:minmax(0,1fr) 270px}.workbench-filter-grid{grid-template-columns:repeat(3,minmax(0,1fr))}.workbench-taxonomy{grid-template-columns:minmax(0,1fr) 280px}.workbench-publications{grid-template-columns:240px minmax(0,1fr)}}
@media(max-width:767px){.workbench-metrics{grid-template-columns:repeat(3,minmax(0,1fr))}.workbench-metrics>div:nth-child(4){border-left:0}.workbench-metrics>div:nth-child(n+4){border-top:1px solid var(--line)}.workbench-filter-grid{grid-template-columns:repeat(2,minmax(0,1fr))}.workbench-search{flex-wrap:wrap}.workbench-search>input{max-width:none;min-width:180px}.workbench-context,.workbench-publications,.workbench-taxonomy,.workbench-distributions{grid-template-columns:minmax(0,1fr)}.workbench-form-grid{grid-template-columns:repeat(2,minmax(0,1fr))}.workbench-publication-list{max-height:300px}.workbench-selection{flex-wrap:wrap}.workbench-tabs{gap:20px}.workbench-distributions{gap:18px}.workbench-distributions>section+section{border-top:1px solid var(--line);padding-top:18px}.workbench-pagination{align-items:flex-start}.workbench-pagination>div{margin-left:auto}}
@media(prefers-reduced-motion:reduce){.classification-workbench *{scroll-behavior:auto;transition:none}}
</style>

<style scoped>.workbench-scenario-actions{margin-top:16px;padding-top:14px;border-top:1px solid var(--line);display:grid;gap:10px;font-size:12px}.workbench-scenario-actions h4{font-size:12px;color:var(--secondary);font-weight:600}.workbench-scenario-actions a{display:flex;justify-content:space-between;align-items:center}.workbench-scenario-actions .icon{width:14px;height:14px}</style>
