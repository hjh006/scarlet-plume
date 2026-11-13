<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ApiError, assetCategoryLabels, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { dataFlowEvidenceLabels, dataFlowNodeLabels, dataFlowQuery, dataFlowRelationLabels, dataFlowWarning, type DataFlowComparison, type DataFlowFilter, type DataFlowGraph, type DataFlowInvestigation, type DataFlowNode, type DataFlowSnapshot, type DataFlowSnapshotSummary } from './data-flows'
import Icon from './Icon.vue'
import FeatherGraph from './FeatherGraph.vue'
import { postgresSensitivityLabel } from './postgresql'

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const base = workspacePath(props.workspace.slug) + '/data-flows'
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const canSnapshot = computed(() => ['owner', 'operator'].includes(props.workspace.role))
const draft = reactive({ service: '', category: '', subjectRef: '', traceRef: '', focus: '' })
const applied = ref<DataFlowFilter>({})
const graph = ref<DataFlowGraph | null>(null)
const loading = ref(false)
const error = ref('')
const notice = ref('')
const tab = ref<'graph' | 'compare' | 'investigation'>('graph')
const snapshots = ref<DataFlowSnapshotSummary[]>([])
const snapshotsError = ref('')
const selectedSnapshot = ref('')
const currentSnapshot = ref<DataFlowSnapshot | null>(null)
const creating = ref(false)
const selectedNodeId = ref('')
const nodeHeading = ref<HTMLElement | null>(null)
const nodeLimit = ref(25)
const edgeLimit = ref(25)
const compareFrom = ref('')
const compareTo = ref('')
const comparison = ref<DataFlowComparison | null>(null)
const comparisonFromGraph = ref<DataFlowGraph | null>(null)
const comparisonToGraph = ref<DataFlowGraph | null>(null)
const comparisonSelected = ref('')
const comparisonNodeNames = ref<Map<string, string>>(new Map())
const comparing = ref(false)
const compareError = ref('')
const investigation = ref<DataFlowInvestigation | null>(null)
const investigating = ref(false)
const investigationError = ref('')
const requestLimit = ref(25)
const selectedRequestId = ref(0)
const selectedRequest = computed(() => investigation.value?.items.find(item => item.id === selectedRequestId.value) ?? investigation.value?.items[0] ?? null)
const relatedRequests = computed(() => { const selected = selectedRequest.value; return selected ? (selected.traceRef ? investigation.value?.items.filter(item => item.traceRef === selected.traceRef) ?? [] : [selected]) : [] })
const lifetime = new AbortController()
let graphRequest: AbortController | undefined
let comparisonRequest: AbortController | undefined
let investigationRequest: AbortController | undefined

const categories = computed(() => [...new Set([draft.category, ...['email', 'phone', 'national_id', 'financial_account', 'credential', 'personal_name', 'address', 'ip_address'], ...(graph.value?.nodes.flatMap(node => node.categories ?? []) ?? [])])].filter(Boolean))
const selectedNode = computed(() => graph.value?.nodes.find(node => node.id === selectedNodeId.value))
const nodeNames = computed(() => new Map(graph.value?.nodes.map(node => [node.id, node.label]) ?? []))
const exportPath = computed(() => currentSnapshot.value ? base + '/snapshots/' + currentSnapshot.value.id : base + '/export' + dataFlowQuery(applied.value))
const compareGroups = computed(() => comparison.value ? [
  { title: '新增节点', nodes: comparison.value.addedNodes, edges: [] },
  { title: '未再观察的节点', nodes: comparison.value.removedNodes, edges: [] },
  { title: '信息变化的节点', nodes: comparison.value.changedNodes, edges: [] },
  { title: '新增关系', nodes: [], edges: comparison.value.addedEdges },
  { title: '未再观察的关系', nodes: [], edges: comparison.value.removedEdges },
  { title: '信息变化的关系', nodes: [], edges: comparison.value.changedEdges },
] : [])
function currentFilter(): DataFlowFilter {
  return Object.fromEntries(Object.entries(draft).map(([key, value]) => [key, value.trim()]).filter(([, value]) => value))
}
function fail(caught: unknown, output: { value: string }) {
  if (caught instanceof ApiError && caught.status === 401) emit('sessionExpired')
  else output.value = describeError(caught)
}
function categoryLabel(category: string) { return assetCategoryLabels[category] || ({ unknown: '待分类', general: '一般数据', important: '重要数据', core: '核心数据' }[category]) || postgresSensitivityLabel(category) }
function snapshotOrigin(origin: string) { return origin === 'manual' ? '手动留存' : origin === 'automatic' ? '自动留存' : ({postgresql:'PostgreSQL 采集',openapi:'契约扫描',runtime:'运行时观测',classification:'分类确认'}[origin] || '采集留存') }
function snapshotLabel(snapshot: DataFlowSnapshotSummary) { return `#${snapshot.id} · ${formatDate(snapshot.createdAt)} · ${snapshotOrigin(snapshot.origin)}` }
function evidenceKind(kind: string) { return dataFlowEvidenceLabels[kind] || '采集记录' }
function initialNode(value: DataFlowGraph) { return value.edges.find(edge => edge.relation === 'service_call')?.from ?? value.nodes.find(node => node.evidence?.length)?.id ?? value.nodes[0]?.id ?? '' }
async function selectNode(id: string) {
  selectedNodeId.value = id
  await nextTick()
  nodeHeading.value?.focus({ preventScroll: true })
  nodeHeading.value?.scrollIntoView({ block: 'nearest' })
}
function focusNode() {
  if (!selectedNode.value) return
  draft.focus = selectedNode.value.id
  void loadGraph()
}
async function loadGraph() {
  graphRequest?.abort()
  const pending = new AbortController()
  graphRequest = pending
  loading.value = true
  error.value = ''
  const filter = currentFilter()
  try {
    const result = await request<DataFlowGraph>(base + dataFlowQuery(filter), { signal: pending.signal })
    if (pending.signal.aborted) return
    graph.value = result
    applied.value = filter
    selectedSnapshot.value = ''
    currentSnapshot.value = null
    selectedNodeId.value = initialNode(result)
    nodeLimit.value = edgeLimit.value = 25
  } catch (caught) { if (!pending.signal.aborted) fail(caught, error) }
  finally { if (!pending.signal.aborted) loading.value = false }
}
async function loadSnapshots() {
  snapshotsError.value = ''
  try {
    const result = await request<{ items: DataFlowSnapshotSummary[]; limit: number }>(base + '/snapshots', { signal: lifetime.signal })
    snapshots.value = result.items ?? []
    if (!compareTo.value && snapshots.value[0]) compareTo.value = String(snapshots.value[0].id)
    if (!compareFrom.value && snapshots.value[1]) compareFrom.value = String(snapshots.value[1].id)
  } catch (caught) { if (!lifetime.signal.aborted) fail(caught, snapshotsError) }
}
async function chooseSnapshot() {
  if (!selectedSnapshot.value) { await loadGraph(); return }
  graphRequest?.abort()
  const pending = new AbortController()
  graphRequest = pending
  loading.value = true
  error.value = ''
  try {
    const result = await request<DataFlowSnapshot>(base + '/snapshots/' + selectedSnapshot.value, { signal: pending.signal })
    if (pending.signal.aborted) return
    currentSnapshot.value = result
    graph.value = result.graph
    applied.value = result.scope
    Object.assign(draft, { service: '', category: '', subjectRef: '', traceRef: '', focus: '' }, result.scope)
    selectedNodeId.value = initialNode(result.graph)
    nodeLimit.value = edgeLimit.value = 25
  } catch (caught) { if (!pending.signal.aborted) fail(caught, error) }
  finally { if (!pending.signal.aborted) loading.value = false }
}
async function createSnapshot() {
  if (!canSnapshot.value || creating.value || loading.value || !graph.value || currentSnapshot.value) return
  creating.value = true
  notice.value = ''
  error.value = ''
  try {
    const result = await request<DataFlowSnapshot>(base + '/snapshots', { method: 'POST', body: JSON.stringify(applied.value) })
    notice.value = `已留存快照 #${result.id}，可在快照比较中查看。`
    await loadSnapshots()
  } catch (caught) { fail(caught, error) }
  finally { creating.value = false }
}
async function compareSnapshots() {
  if (!compareFrom.value || !compareTo.value || compareFrom.value === compareTo.value || comparing.value) return
  comparisonRequest?.abort()
  const pending = new AbortController()
  comparisonRequest = pending
  comparing.value = true
  compareError.value = ''
  comparison.value = null
  try {
    const options = { signal: pending.signal }
    const [result, from, to] = await Promise.all([
      request<DataFlowComparison>(base + '/compare?' + new URLSearchParams({ from: compareFrom.value, to: compareTo.value }), options),
      request<DataFlowSnapshot>(base + '/snapshots/' + compareFrom.value, options),
      request<DataFlowSnapshot>(base + '/snapshots/' + compareTo.value, options),
    ])
    if (pending.signal.aborted) return
    comparison.value = result
    comparisonFromGraph.value = from.graph
    comparisonToGraph.value = to.graph
    comparisonSelected.value = initialNode(from.graph)
    comparisonNodeNames.value = new Map([...from.graph.nodes, ...to.graph.nodes].map(node => [node.id, node.label]))
  }
  catch (caught) { if (!pending.signal.aborted) fail(caught, compareError) }
  finally { if (!pending.signal.aborted) comparing.value = false }
}
async function investigate() {
  investigationRequest?.abort()
  const pending = new AbortController()
  investigationRequest = pending
  investigating.value = true
  investigationError.value = ''
  try {
    investigation.value = await request<DataFlowInvestigation>(base + '/investigation' + dataFlowQuery(currentFilter()), { signal: pending.signal })
    requestLimit.value = 25
  } catch (caught) { if (!pending.signal.aborted) fail(caught, investigationError) }
  finally { if (!pending.signal.aborted) investigating.value = false }
}
function openTab(next: typeof tab.value) {
  tab.value = next
  if (next === 'investigation' && !investigation.value && !investigating.value) void investigate()
}
function resetFilter() {
  Object.assign(draft, { service: '', category: '', subjectRef: '', traceRef: '', focus: '' })
  if (tab.value === 'investigation') void investigate()
  else void loadGraph()
}
onMounted(() => { void Promise.all([loadGraph(), loadSnapshots()]) })
onBeforeUnmount(() => { lifetime.abort(); graphRequest?.abort(); comparisonRequest?.abort(); investigationRequest?.abort() })
</script>

<template>
  <header class="page-header">
    <div><p class="eyebrow">数据治理 / 数据流转</p><h1>{{ tab === 'compare' ? '图谱历史与差异' : tab === 'investigation' ? '请求与数据关系调查' : '数据流转图谱' }}</h1><p>关联资产目录、接口契约与运行时观测，按来源保留关系依据。</p></div>
    <a v-if="graph" class="button button-primary" :href="exportPath" :download="currentSnapshot ? 'scarlet-data-flow-snapshot-' + currentSnapshot.id + '.json' : undefined"><Icon name="download" />{{ currentSnapshot ? '导出快照' : '导出当前关系' }}</a>
  </header>
  <nav class="flow-tabs" aria-label="数据流转视图">
    <button v-for="item in ([{id:'graph',label:'关系图谱'},{id:'compare',label:'快照比较'},{id:'investigation',label:'请求调查'}] as const)" :key="item.id" type="button" class="button" :class="tab === item.id ? 'button-primary' : 'button-secondary'" :aria-pressed="tab === item.id" @click="openTab(item.id)">{{ item.label }}</button>
  </nav>
  <p v-if="notice" class="message message-success page-feedback" role="status">{{ notice }}</p>
  <form v-if="tab !== 'compare'" class="panel flow-filters" @submit.prevent="tab === 'investigation' ? investigate() : loadGraph()">
    <div class="field"><label for="flow-service">服务别名</label><input id="flow-service" v-model="draft.service" maxlength="80" placeholder="全部服务"></div>
    <div class="field"><label for="flow-category">识别类别</label><select id="flow-category" v-model="draft.category"><option value="">全部类别</option><option v-for="category in categories" :key="category" :value="category">{{ categoryLabel(category) }}</option></select></div>
    <div class="field"><label for="flow-subject">脱敏主体引用</label><input id="flow-subject" v-model="draft.subjectRef" maxlength="64" pattern="[0-9a-f]{64}" autocomplete="off" placeholder="已采集的主体引用"></div>
    <div class="field"><label for="flow-trace">Trace 引用</label><input id="flow-trace" v-model="draft.traceRef" maxlength="32" pattern="[0-9a-f]{32}" autocomplete="off" placeholder="已采集的 Trace 引用"></div>
    <div class="flow-filter-actions"><button type="submit" class="button button-primary" :disabled="loading || investigating">{{ loading || investigating ? '查询中…' : '查询' }}</button><button type="button" class="button button-secondary" :disabled="loading || investigating" @click="resetFilter">重置</button></div>
    <p v-if="draft.focus" class="field-hint flow-filter-note">当前围绕节点 {{ draft.focus }} 查询；重置可恢复全部范围。</p>
  </form>

  <template v-if="tab === 'graph'">
    <p v-if="error" class="message message-error page-feedback" role="alert">{{ error }} <button type="button" class="button button-secondary" @click="loadGraph">重新读取</button></p>
    <div class="workbench-grid flow-workbench"><section class="panel flow-graph-panel" aria-labelledby="flow-graph-title" :aria-busy="loading">
      <div class="panel-heading"><div><h2 id="flow-graph-title">{{ currentSnapshot ? '历史快照 #' + currentSnapshot.id : '当前关系图谱' }}</h2><p v-if="graph">生成于 {{ formatDate(graph.generatedAt) }} · 观测窗口自 {{ formatDate(graph.windowStart) }} 起</p></div><div class="flow-snapshot-controls"><label for="flow-snapshot">查看快照</label><select id="flow-snapshot" v-model="selectedSnapshot" :disabled="loading" @change="chooseSnapshot"><option value="">当前关系</option><option v-for="snapshot in snapshots" :key="snapshot.id" :value="String(snapshot.id)">{{ snapshotLabel(snapshot) }}</option></select><button v-if="canSnapshot && !currentSnapshot" type="button" class="button button-secondary" :disabled="creating || loading || !graph" @click="createSnapshot">{{ creating ? '留存中…' : '留存快照' }}</button></div></div>
      <p v-if="snapshotsError" class="message message-warning" role="alert">快照列表：{{ snapshotsError }}</p>
      <p v-if="loading && !graph" class="empty-state" role="status">正在关联目录、契约与运行时观测…</p>
      <template v-if="graph">
        <dl class="flow-summary" aria-label="图谱规模"><div><dt>业务应用</dt><dd>{{ graph.summary.applications }}</dd></div><div><dt>接口</dt><dd>{{ graph.summary.apis }}</dd></div><div><dt>数据库表</dt><dd>{{ graph.summary.tables }}</dd></div><div><dt>脱敏主体</dt><dd>{{ graph.summary.subjects }}</dd></div><div><dt>已定位数据库目标</dt><dd>{{ graph.summary.tracedDatabaseTargets }}</dd></div><div><dt>关系</dt><dd>{{ graph.summary.edges }}</dd></div></dl>
        <FeatherGraph :graph="graph" :loading="loading" :selected="selectedNodeId" @select="selectNode($event.id)" />
        <details class="flow-coverage"><summary>查看关系类型与采集范围</summary>        <div class="flow-scope-note"><p>目录结构与契约声明代表登记关系；运行时观测代表采集窗口内的实际记录。数据库类型与已定位目标分开呈现，关系不推断未采集的业务血缘。</p><p v-if="graph.warnings?.length">采集范围提示：{{ graph.warnings.map(dataFlowWarning).join('；') }}</p><p v-if="graph.snapshotStatus?.error">最近快照留存未完成（{{ formatDate(graph.snapshotStatus.lastAttemptAt) }}）：{{ dataFlowWarning(graph.snapshotStatus.error) }}</p><p v-if="graph.nodes.length > 60">图示展示前 60 个节点及其内部关系；完整结果请查看下方台账或导出 JSON。</p></div>
        <ul class="flow-legend" aria-label="关系图例"><li v-for="(label, relation) in dataFlowRelationLabels" :key="relation" :data-relation="relation">{{ label }}</li></ul>
</details>
      </template>
    </section>
    <aside class="context-rail flow-context"><section class="panel flow-node-detail" aria-labelledby="flow-node-title"><div class="panel-heading"><h2 id="flow-node-title" ref="nodeHeading" tabindex="-1">关系与来源依据</h2></div><template v-if="selectedNode"><div class="flow-context-body"><h3>{{ selectedNode.label }}</h3><p class="muted">{{ dataFlowNodeLabels[selectedNode.kind] }}<span v-if="selectedNode.service"> · {{ selectedNode.service }}</span></p><p class="flow-node-categories"><span v-for="category in selectedNode.categories" :key="category" class="badge">{{ categoryLabel(category) }}</span><span v-if="!selectedNode.categories?.length" class="badge">数据类别未知</span></p><div class="page-actions"><button type="button" class="button button-secondary" :disabled="loading" @click="focusNode">聚焦相邻关系</button><a v-if="selectedNode.assetId" class="button button-secondary" :href="home + '/classification/' + selectedNode.assetId">查看资产分类</a></div></div><ol class="flow-proof-spine"><li v-for="(evidence,index) in selectedNode.evidence" :key="index"><strong>{{ evidenceKind(evidence.kind) }}</strong><dl><div><dt>来源</dt><dd>{{ evidence.source }}<span v-if="evidence.sourceId"> · #{{ evidence.sourceId }}</span></dd></div><div><dt>记录</dt><dd class="mono">{{ evidence.record }}</dd></div><div><dt>观测</dt><dd>{{ evidence.observedAt ? formatDate(evidence.observedAt) : '未提供' }}</dd></div><div v-if="evidence.requests !== undefined"><dt>来源内请求</dt><dd>{{ evidence.requests }}</dd></div></dl><p v-if="evidence.traceRef" class="mono">Trace {{ evidence.traceRef }}</p><p v-if="evidence.spanRef" class="mono">Span {{ evidence.spanRef }}</p><p v-if="evidence.warnings?.length" class="muted">{{ evidence.warnings.map(dataFlowWarning).join('；') }}</p><details v-if="evidence.details" class="flow-capture-details"><summary>采用标准、字段与范围</summary><pre class="flow-source-details">{{ JSON.stringify(evidence.details,null,2) }}</pre></details></li></ol><p v-if="!selectedNode.evidence?.length" class="empty-state">该节点暂无附加取证记录。</p></template><p v-else class="empty-state">选择图中节点，查看实际来源与关系依据。</p></section><section class="panel"><div class="panel-heading"><h3>采用范围</h3></div><div class="flow-context-body"><p>仅展示已接入来源与观测窗口。目录声明、实际请求、精确数据库目标分别留证；缺失的关系不会补造。</p><p v-if="currentSnapshot">历史快照 #{{ currentSnapshot.id }} · {{ formatDate(currentSnapshot.createdAt) }}</p><p v-else-if="graph">{{ formatDate(graph.windowStart) }} 至 {{ formatDate(graph.generatedAt) }}</p></div></section></aside></div>
    <section v-if="graph?.nodes.length" class="panel flow-records" aria-labelledby="flow-nodes-title"><div class="panel-heading"><h2 id="flow-nodes-title">节点台账</h2><span>{{ graph.nodes.length }} 项</span></div><div class="table-scroll" role="region" tabindex="0" aria-labelledby="flow-nodes-title"><table class="data-table"><thead><tr><th scope="col">节点</th><th scope="col">类型</th><th scope="col">服务</th><th scope="col">数据类别</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="node in graph.nodes.slice(0, nodeLimit)" :key="node.id"><th scope="row">{{ node.label }}</th><td>{{ dataFlowNodeLabels[node.kind] }}</td><td>{{ node.service || '—' }}</td><td>{{ node.categories?.map(categoryLabel).join('、') || '未识别' }}</td><td><button type="button" class="button button-secondary" @click="selectNode(node.id)">查看依据</button></td></tr></tbody></table></div><div v-if="graph.nodes.length > nodeLimit" class="panel-footer"><button type="button" class="button button-secondary" @click="nodeLimit += 25">再显示 25 项</button></div></section>
    <section v-if="graph?.edges.length" class="panel flow-records" aria-labelledby="flow-edges-title"><div class="panel-heading"><h2 id="flow-edges-title">关系与取证</h2><span>{{ graph.edges.length }} 项</span></div><div class="table-scroll" role="region" tabindex="0" aria-labelledby="flow-edges-title"><table class="data-table"><thead><tr><th scope="col">起点 → 终点</th><th scope="col">关系依据</th><th scope="col">取证记录</th></tr></thead><tbody><tr v-for="edge in graph.edges.slice(0, edgeLimit)" :key="edge.id"><th scope="row">{{ nodeNames.get(edge.from) || edge.from }} <Icon name="arrow" /> {{ nodeNames.get(edge.to) || edge.to }}</th><td>{{ dataFlowRelationLabels[edge.relation] }}</td><td><details><summary>{{ edge.evidence?.length || 0 }} 项记录</summary><ul class="flow-evidence-list"><li v-for="(evidence, index) in edge.evidence" :key="index"><strong>{{ evidenceKind(evidence.kind) }} · {{ evidence.source }}</strong><span>记录 {{ evidence.record }}<span v-if="evidence.requests !== undefined"> · 来源内 {{ evidence.requests }} 次请求</span></span><time v-if="evidence.observedAt" :datetime="evidence.observedAt">{{ formatDate(evidence.observedAt) }}</time><span v-if="evidence.traceRef" class="mono">Trace {{ evidence.traceRef }}</span><span v-if="evidence.spanRef" class="mono">Span {{ evidence.spanRef }}</span><span v-if="evidence.warnings?.length">{{ evidence.warnings.map(dataFlowWarning).join('；') }}</span><details v-if="evidence.details" class="flow-capture-details"><summary>关联证明与采集范围</summary><pre class="flow-source-details">{{ JSON.stringify(evidence.details, null, 2) }}</pre></details></li></ul></details></td></tr></tbody></table></div><div v-if="graph.edges.length > edgeLimit" class="panel-footer"><button type="button" class="button button-secondary" @click="edgeLimit += 25">再显示 25 项</button></div></section>
  </template>

  <section v-if="tab === 'compare'" class="panel flow-compare" aria-labelledby="flow-compare-title" :aria-busy="comparing"><div class="panel-heading"><div><h2 id="flow-compare-title">快照比较</h2><p>比较两次留存的节点、关系与取证信息；“未再观察”不等于数据已删除。</p></div><button type="button" class="button button-secondary" @click="loadSnapshots">刷新快照列表</button></div><p v-if="snapshotsError" class="message message-error" role="alert">{{ snapshotsError }}</p><form class="flow-compare-form" @submit.prevent="compareSnapshots"><div class="field"><label for="flow-compare-from">较早快照</label><select id="flow-compare-from" v-model="compareFrom" required><option value="">请选择快照</option><option v-for="snapshot in snapshots" :key="snapshot.id" :value="String(snapshot.id)">{{ snapshotLabel(snapshot) }}</option></select></div><div class="field"><label for="flow-compare-to">较新快照</label><select id="flow-compare-to" v-model="compareTo" required><option value="">请选择快照</option><option v-for="snapshot in snapshots" :key="snapshot.id" :value="String(snapshot.id)">{{ snapshotLabel(snapshot) }}</option></select></div><button type="submit" class="button button-primary" :disabled="comparing || !compareFrom || !compareTo || compareFrom === compareTo">{{ comparing ? '比较中…' : '比较快照' }}</button></form><p v-if="compareError" class="message message-error" role="alert">{{ compareError }}</p><p v-if="snapshots.length < 2" class="empty-state">至少需要两次快照。采集自动留存或手动留存后，可在此比较变化。</p><div v-if="comparison" class="flow-comparison-results"><div class="flow-comparison-canvases"><section><h3>较早快照 #{{ comparison.from.id }}</h3><FeatherGraph :graph="comparisonFromGraph" :selected="comparisonSelected" compact @select="comparisonSelected = $event.id" /></section><section><h3>较新快照 #{{ comparison.to.id }}</h3><FeatherGraph :graph="comparisonToGraph" :selected="comparisonSelected" compact @select="comparisonSelected = $event.id" /></section></div><p class="flow-comparison-context">#{{ comparison.from.id }}（{{ formatDate(comparison.from.createdAt) }}）→ #{{ comparison.to.id }}（{{ formatDate(comparison.to.createdAt) }}）</p><p v-if="JSON.stringify(comparison.from.scope) !== JSON.stringify(comparison.to.scope)" class="message message-warning">两次快照的筛选范围不同，变化包含范围差异，请先核对服务和类别等条件。</p><details v-for="group in compareGroups" :key="group.title" class="flow-difference" :open="group.nodes.length + group.edges.length > 0"><summary>{{ group.title }} <strong>{{ group.nodes.length + group.edges.length }}</strong></summary><ul v-if="group.nodes.length"><li v-for="node in group.nodes" :key="node.id"><strong>{{ node.label }}</strong><span>{{ dataFlowNodeLabels[node.kind] }} · {{ node.categories?.map(categoryLabel).join('、') || '未识别类别' }}</span></li></ul><ul v-else-if="group.edges.length"><li v-for="edge in group.edges" :key="edge.id"><strong>{{ comparisonNodeNames.get(edge.from) || edge.from }} → {{ comparisonNodeNames.get(edge.to) || edge.to }}</strong><span>{{ dataFlowRelationLabels[edge.relation] }} · {{ edge.evidence?.length || 0 }} 项取证记录</span></li></ul><p v-else>此项没有变化。</p></details></div></section>

  <div v-if="tab === 'investigation' && investigation?.items.length" class="flow-investigation-workbench"><section class="panel"><div class="panel-heading"><div><h2>同一观测引用下的请求</h2><p>按已采集 Trace 引用并列展示；只有双端父子 Span 证据才能形成调用关系。</p></div><span>{{ relatedRequests.length }} 条</span></div><div class="flow-request-context"><span class="badge">{{ selectedRequest?.service }}</span><code v-if="selectedRequest?.traceRef">Trace {{ selectedRequest.traceRef }}</code><span v-else class="muted">未采集 Trace，仅展示所选记录。</span></div><ol class="flow-request-spine"><li v-for="item in relatedRequests" :key="item.id" :class="{'flow-request-selected': item.id === selectedRequest?.id}"><button type="button" :aria-pressed="item.id === selectedRequest?.id" @click="selectedRequestId = item.id"><span class="flow-request-source">{{ evidenceKind(item.kind) }} · {{ item.source }}</span><span class="flow-request-title"><strong>{{ item.service }}</strong><code>{{ item.method }} {{ item.route }}</code><span class="badge">HTTP {{ item.status }}</span></span><span class="flow-request-meta">{{ formatDate(item.observedAt) }} · 来源 #{{ item.sourceId }} · 记录 #{{ item.id }}</span><span class="flow-request-meta">{{ item.databaseTargets?.length || 0 }} 项数据库目标 · {{ item.dataFields?.length || 0 }} 项字段元数据<span v-if="item.serviceCalls?.length"> · {{ item.serviceCalls.length }} 项跨服务候选</span></span></button></li></ol><p class="flow-investigation-note">轴上仅排列真实记录；缺失父子 Span、另一端来源或精确数据库目标时保留待核实状态，不补造链路。</p></section><aside class="context-rail"><section class="panel"><div class="panel-heading"><h2>请求与采集证明</h2></div><div v-if="selectedRequest" class="flow-selected-request"><h3><code>{{ selectedRequest.method }} {{ selectedRequest.route }}</code></h3><dl><div><dt>来源</dt><dd>{{ selectedRequest.source }} · #{{ selectedRequest.sourceId }}</dd></div><div><dt>状态</dt><dd>HTTP {{ selectedRequest.status }}</dd></div><div><dt>观测时间</dt><dd>{{ formatDate(selectedRequest.observedAt) }}</dd></div><div><dt>Span</dt><dd class="mono">{{ selectedRequest.spanRef || '未采集' }}</dd></div><div><dt>父 Span</dt><dd class="mono">{{ selectedRequest.parentSpanRef || '未采集' }}</dd></div><div><dt>脱敏主体</dt><dd class="mono">{{ selectedRequest.subjectRef || '未采集' }}</dd></div></dl><h4>字段与观察分母</h4><ul v-if="selectedRequest.dataFields?.length" class="flow-field-proof"><li v-for="(field,index) in selectedRequest.dataFields" :key="index"><strong><code>{{ field.name }}</code></strong><span>{{ ({request:'请求',response:'响应',database:'数据库'}[field.location]) || field.location }} · {{ categoryLabel(field.category) }}</span><span>{{ field.method === 'format' ? '格式命中' : '字段元数据候选' }} {{ field.count }} / 观察 {{ field.observed }}</span></li></ul><p v-else class="muted">未采集字段元数据，不能据此认定无敏感数据。</p><h4>数据库目标</h4><ul v-if="selectedRequest.databaseTargets?.length" class="flow-field-proof"><li v-for="(target,index) in selectedRequest.databaseTargets" :key="index"><strong>{{ target.system }} · {{ target.operation }}</strong><span>{{ target.source }} / {{ target.database }}.{{ target.schema ? target.schema + '.' : '' }}{{ target.table }}</span></li></ul><p v-else class="muted">无可定位的数据库目标。</p><h4 v-if="selectedRequest.captureWarnings?.length">采集缺口</h4><ul v-if="selectedRequest.captureWarnings?.length" class="flow-request-warnings"><li v-for="warning in selectedRequest.captureWarnings" :key="warning">{{ dataFlowWarning(warning) }}</li></ul><p class="flow-investigation-note">保留 {{ investigation.retentionDays }} 天；状态码和字段候选不代表漏洞确认。</p></div></section></aside></div>
  <section v-if="tab === 'investigation'" class="panel flow-investigation" aria-labelledby="flow-investigation-title" :aria-busy="investigating"><div class="panel-heading"><div><h2 id="flow-investigation-title">请求调查</h2><p>按服务、已脱敏的主体或 Trace 引用核对请求元数据；不展示业务请求原文。</p></div><span v-if="investigation">最多 {{ investigation.limit }} 条 · 保留 {{ investigation.retentionDays }} 天</span></div><p v-if="investigationError" class="message message-error" role="alert">{{ investigationError }}</p><p v-if="investigating && !investigation" class="empty-state" role="status">正在读取请求关联记录…</p><p v-if="investigation?.truncated" class="message message-warning">结果已达到 {{ investigation.limit }} 条上限，请缩小服务、主体或 Trace 范围。</p><div v-if="investigation?.items.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="flow-investigation-title"><table class="data-table flow-investigation-table"><thead><tr><th scope="col">接口</th><th scope="col">服务与来源</th><th scope="col">状态码</th><th scope="col">关联引用</th><th scope="col">采集范围</th><th scope="col">观测时间</th></tr></thead><tbody><tr v-for="item in investigation.items.slice(0, requestLimit)" :key="item.id"><th scope="row"><button type="button" class="table-link" :aria-pressed="selectedRequest?.id === item.id" @click="selectedRequestId = item.id"><span class="mono">{{ item.method }} {{ item.route }}</span></button></th><td>{{ item.service }}<span class="table-secondary">{{ item.source }} · {{ evidenceKind(item.kind) }}</span></td><td>{{ item.status }}</td><td class="mono"><span v-if="item.subjectRef">主体 {{ item.subjectRef }}</span><span v-if="item.traceRef" class="table-secondary">Trace {{ item.traceRef }}</span><span v-if="item.spanRef" class="table-secondary">Span {{ item.spanRef }}</span><span v-if="item.parentSpanRef" class="table-secondary">父 Span {{ item.parentSpanRef }}</span><span v-if="!item.subjectRef && !item.traceRef && !item.spanRef">未采集</span></td><td><span v-if="item.serviceCalls?.length">{{ item.serviceCalls.length }} 项调用候选（须双端证据才能连边）</span>{{ item.databaseTargets?.length || 0 }} 项数据库关联<span class="table-secondary">{{ item.dataFields?.length || 0 }} 项字段元数据</span><details v-if="item.databaseTargets?.length || item.dataFields?.length || item.serviceCalls?.length" class="flow-capture-details"><summary>查看关联元数据</summary><ul><li v-for="call in item.serviceCalls" :key="call.targetService + call.spanRef"><strong>目标服务 {{ call.targetService }}</strong><span>HTTP Client Span {{ call.spanRef }}</span></li><li v-for="(target, index) in item.databaseTargets" :key="'db-' + index"><strong>{{ target.system }} · {{ target.operation }}</strong><span>{{ target.source }} / {{ target.database }}.{{ target.schema ? target.schema + '.' : '' }}{{ target.table }}</span></li><li v-for="(field, index) in item.dataFields" :key="'field-' + index"><strong>{{ ({request:'请求',response:'响应',database:'数据库'}[field.location]) || field.location }} · {{ field.name }}</strong><span>{{ categoryLabel(field.category) }} · {{ field.method === 'format' ? '格式命中' : '字段元数据候选' }} {{ field.count }} / 观察 {{ field.observed }}（受采集范围限制）</span></li></ul></details><span v-if="item.captureWarnings?.length" class="table-secondary">{{ item.captureWarnings.map(dataFlowWarning).join('；') }}</span></td><td>{{ formatDate(item.observedAt) }}</td></tr></tbody></table></div><p v-else-if="investigation && !investigating" class="empty-state">当前筛选范围内暂无请求记录；只有来源实际采到的脱敏主体、Trace 和数据库关联可用于调查。</p><div v-if="investigation && investigation.items.length > requestLimit" class="panel-footer"><button type="button" class="button button-secondary" @click="requestLimit += 25">再显示 25 条</button></div></section>
</template>

<style scoped>
.flow-tabs { display: flex; flex-wrap: wrap; gap: .5rem; margin-bottom: 1rem; }
.flow-filters { display: grid; grid-template-columns: minmax(8rem, 1fr) minmax(7rem, .8fr) minmax(9rem, 1.2fr) minmax(9rem, 1.2fr) auto; align-items: end; gap: .8rem; margin-bottom: 1rem; padding: 1rem; }
.flow-filters .field, .flow-compare-form .field { min-width: 0; margin: 0; }
.flow-filter-actions { display: flex; flex-wrap: wrap; gap: .5rem; }
.flow-filter-note { grid-column: 1 / -1; }
.flow-graph-panel, .flow-records, .flow-node-detail { margin-bottom: 1rem; }
.flow-snapshot-controls { display: flex; flex-wrap: wrap; align-items: center; gap: .5rem; }
.flow-snapshot-controls label { color: var(--secondary); font-size: .78rem; }
.flow-snapshot-controls select { min-width: 0; max-width: 21rem; min-height: 2.75rem; padding: .4rem .6rem; border: 1px solid var(--input-line); border-radius: .4rem; color: var(--ink); background: var(--surface); font: inherit; font-size: .8rem; }
.flow-summary { display: grid; grid-template-columns: repeat(6, minmax(0, 1fr)); gap: 1rem; padding: 1rem; border-bottom: 1px solid var(--line); }
.flow-summary dt { color: var(--secondary); font-size: .75rem; }
.flow-summary dd { margin-top: .2rem; font-size: 1.35rem; font-weight: 700; font-variant-numeric: tabular-nums; }
.flow-scope-note { padding: .8rem 1rem; color: var(--secondary); background: var(--surface-subtle); font-size: .78rem; }
.flow-scope-note p + p { margin-top: .4rem; }
.flow-legend { display: flex; flex-wrap: wrap; gap: .5rem 1.3rem; margin: 0; padding: .8rem 1rem; list-style: none; font-size: .75rem; }
.flow-legend li { display: flex; align-items: center; gap: .45rem; }
.flow-legend li::before { width: 1.5rem; height: 0; border-top: 2px solid var(--accent); content: ''; }
.flow-legend [data-relation="declared"]::before, .flow-legend [data-relation="catalog"]::before { border-top-style: dashed; border-color: var(--secondary); }
.flow-legend [data-relation="database_type"]::before { border-color: var(--violet); border-top-style: dotted; }
.flow-legend [data-relation="database_target"]::before { border-color: var(--teal); }
.flow-legend [data-relation="service_call"]::before { border-color: var(--accent); }
.flow-legend [data-relation="subject_access"]::before { border-color: var(--violet); }
.flow-map-scroll { max-width: 100%; max-height: 40rem; overflow: auto; border-top: 1px solid var(--line); }
.flow-map { position: relative; min-width: 810px; }
.flow-connectors { position: absolute; inset: 0; pointer-events: none; }
.flow-connectors > path { fill: none; stroke: var(--accent); stroke-width: 1.7; }
.flow-connectors [data-relation="declared"], .flow-connectors [data-relation="catalog"] { stroke: var(--secondary); stroke-dasharray: 5 4; }
.flow-connectors [data-relation="database_type"] { stroke: var(--violet); stroke-dasharray: 2 4; }
.flow-connectors [data-relation="database_target"] { stroke: var(--teal); }
.flow-connectors [data-relation="service_call"] { stroke: var(--accent); stroke-width: 2.5; }
.flow-connectors [data-relation="subject_access"] { stroke: var(--violet); }
.flow-node { position: absolute; display: flex; align-items: center; gap: .65rem; width: 194px; min-height: 68px; padding: .65rem .75rem; border: 1px solid var(--input-line); border-radius: .55rem; color: var(--ink); background: var(--surface); text-align: left; }
.flow-node:hover, .flow-node[aria-pressed="true"] { border-color: var(--accent); background: var(--accent-soft); }
.flow-node > .icon { flex: none; color: var(--accent); width: 1.35rem; height: 1.35rem; }
.flow-node[data-kind="subject"] > .icon { color: var(--violet); }
.flow-node[data-kind="table"] > .icon, .flow-node[data-kind="database_type"] > .icon { color: var(--teal); }
.flow-node > span { display: grid; min-width: 0; gap: .2rem; }
.flow-node strong { display: -webkit-box; overflow: hidden; -webkit-line-clamp: 2; -webkit-box-orient: vertical; font-size: .8rem; overflow-wrap: anywhere; }
.flow-node small { color: var(--secondary); font-size: .75rem; }
.flow-node-categories { padding: .8rem 1rem; color: var(--secondary); font-size: .82rem; }
.flow-evidence-list { display: grid; gap: .65rem; min-width: 14rem; margin: .6rem 0; padding-left: 1rem; }
.flow-evidence-list li > span, .flow-evidence-list time { display: block; margin-top: .15rem; color: var(--secondary); font-size: .76rem; }
.flow-records .data-table { min-width: 40rem; }
.flow-records details > summary { min-height: 2.75rem; display: flex; align-items: center; color: var(--accent); cursor: pointer; }
.flow-compare-form { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) auto; align-items: end; gap: .8rem; padding: 1rem; }
.flow-comparison-results { padding: 0 1rem 1rem; }
.flow-comparison-context { margin-bottom: .8rem; color: var(--secondary); font-size: .8rem; }
.flow-difference { border-top: 1px solid var(--line); }
.flow-difference summary { display: flex; align-items: center; gap: .6rem; min-height: 2.75rem; cursor: pointer; }
.flow-difference summary strong { color: var(--accent); font-variant-numeric: tabular-nums; }
.flow-difference > p { padding: 0 0 .75rem; color: var(--secondary); font-size: .8rem; }
.flow-difference ul { display: grid; gap: .65rem; margin: 0; padding: 0 0 1rem 1.3rem; }
.flow-difference li span { display: block; margin-top: .1rem; color: var(--secondary); font-size: .78rem; }
.flow-capture-details { min-width: 14rem; }
.flow-capture-details summary { display: flex; align-items: center; min-height: 2.75rem; color: var(--accent); cursor: pointer; }
.flow-capture-details ul { display: grid; gap: .6rem; margin: .5rem 0; padding-left: 1rem; }
.flow-capture-details li > span { display: block; color: var(--secondary); font-size: .75rem; }
.flow-investigation-table { min-width: 66rem; }
.flow-investigation-table td:nth-child(4) { max-width: 17rem; overflow-wrap: anywhere; }
@media (max-width: 76rem) { .flow-filters { grid-template-columns: repeat(2, minmax(0, 1fr)); } .flow-filter-actions { grid-column: 1 / -1; } .flow-summary { grid-template-columns: repeat(3, minmax(0, 1fr)); } }
@media (max-width: 43.99rem) { .flow-filters, .flow-compare-form { grid-template-columns: minmax(0, 1fr); } .flow-snapshot-controls { width: 100%; } .flow-snapshot-controls select { max-width: 100%; flex: 1; } .flow-summary { gap: .75rem; padding: .8rem; } .flow-summary dd { font-size: 1.2rem; } .flow-map-scroll { max-height: 30rem; } .flow-node-detail .panel-heading, .flow-graph-panel .panel-heading { flex-wrap: wrap; } }
.flow-workbench{display:grid;grid-template-columns:minmax(0,1fr) 300px;gap:16px;align-items:start;margin-top:16px}.flow-graph-panel{min-width:0}.flow-graph-panel>.panel-heading{align-items:flex-start}.flow-snapshot-controls{justify-content:flex-end}.flow-snapshot-controls select{max-width:245px;font-size:12px;min-height:34px}.flow-snapshot-controls label{font-size:12px}.flow-snapshot-controls .button{font-size:12px}.flow-summary{gap:8px;padding:14px 18px}.flow-summary dt{font-size:12px}.flow-summary dd{font-size:20px}.flow-context{display:grid;gap:16px;min-width:0}.flow-context .panel-heading{padding:16px 18px}.flow-context-body{padding:18px;font-size:12px;line-height:1.7}.flow-context-body h3{font-size:14px;margin-bottom:6px;overflow-wrap:anywhere}.flow-context-body .page-actions{margin-top:16px}.flow-context-body .button{min-height:34px;font-size:12px}.flow-node-categories{display:flex;flex-wrap:wrap;gap:5px;padding:12px 0 0}.flow-proof-spine{margin:0 18px 18px;padding:0 0 0 18px;list-style:none;border-left:1px solid var(--line)}.flow-proof-spine>li{position:relative;padding:0 0 20px;font-size:12px;min-width:0;overflow-wrap:anywhere}.flow-proof-spine>li::before{content:'';position:absolute;left:-23px;top:4px;width:9px;height:9px;border-radius:50%;background:var(--teal);box-shadow:0 0 0 3px var(--teal-soft)}.flow-proof-spine strong{font-size:12px}.flow-proof-spine dl{margin:8px 0;display:grid;gap:5px}.flow-proof-spine dl>div{display:grid;grid-template-columns:58px minmax(0,1fr);gap:8px}.flow-proof-spine dt{color:var(--secondary)}.flow-proof-spine dd{margin:0}.flow-proof-spine p{font-size:12px;line-height:1.7;margin-top:6px}.flow-proof-spine .flow-source-details{max-width:100%;max-height:260px;font-size:12px}.flow-coverage{font-size:12px;border-top:1px solid var(--line)}.flow-coverage>summary{padding:12px 18px;color:var(--accent);cursor:pointer}.flow-coverage .flow-scope-note{padding:4px 18px 12px;font-size:12px;background:var(--surface)}.flow-coverage .flow-legend{font-size:12px;padding:0 18px 15px}.flow-tabs{gap:0;border-bottom:1px solid var(--line);padding:0 10px;background:var(--surface);border-radius:10px 10px 0 0}.flow-tabs .button{border:0;border-radius:0;background:transparent;color:var(--secondary);border-bottom:2px solid transparent;min-height:43px;padding:10px 15px}.flow-tabs .button-primary{color:var(--accent);border-bottom-color:var(--accent)}.flow-filters{margin:0;display:grid;grid-template-columns:1.1fr 1fr 1.2fr 1.2fr auto;gap:12px;padding:14px 18px;border-radius:0 0 10px 10px;border-top:0}.flow-filters .field{margin:0}.flow-filters label{font-size:12px}.flow-filters input,.flow-filters select{min-height:34px;font-size:12px}.flow-filter-actions{align-self:end;display:flex;gap:7px}.flow-comparison-canvases{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px;margin-bottom:20px}.flow-comparison-canvases>section{min-width:0;overflow:hidden;border-radius:10px;background:var(--brand-gradient)}.flow-comparison-canvases h3{color:var(--nav-text);background:var(--brand-node-surface);font-size:12px;padding:12px 16px}.flow-compare{margin-top:16px}.flow-difference summary{font-size:12px}.flow-difference li{font-size:12px}.flow-records{margin-top:16px}.flow-investigation .flow-investigation-table{font-size:12px}.flow-investigation .flow-investigation-table th,.flow-investigation .flow-investigation-table td{padding:12px}.flow-investigation .flow-capture-details{min-width:0}.flow-investigation .flow-capture-details>summary{min-height:34px}.flow-investigation .flow-evidence-list{font-size:12px}.flow-investigation-table td:nth-child(4){max-width:170px}@media(min-width:1100px) and (max-width:1400px){.flow-workbench{grid-template-columns:minmax(0,1fr) 280px}.flow-filters{grid-template-columns:repeat(4,minmax(0,1fr))}.flow-filter-actions{grid-column:1/-1;justify-content:flex-end}.flow-graph-panel>.panel-heading{flex-wrap:wrap}.flow-snapshot-controls{width:100%;justify-content:flex-start}.flow-summary{grid-template-columns:repeat(3,minmax(0,1fr))}}
.flow-investigation-workbench{display:grid;grid-template-columns:minmax(0,1fr) 300px;gap:16px;align-items:start;margin-top:16px}.flow-investigation-workbench .panel-heading p{font-size:12px}.flow-request-context{display:flex;align-items:center;gap:12px;padding:18px;font-size:12px;flex-wrap:wrap;border-bottom:1px solid var(--line)}.flow-request-context code{overflow-wrap:anywhere}.flow-request-spine{list-style:none;margin:22px 24px 16px 34px;padding:0 0 0 26px;border-left:1px solid var(--line)}.flow-request-spine>li{position:relative;padding:0 0 16px}.flow-request-spine>li::before{content:'';position:absolute;left:-32px;top:22px;width:10px;height:10px;border:2px solid white;border-radius:50%;background:var(--teal);box-shadow:0 0 0 1px var(--line)}.flow-request-spine button{display:grid;gap:10px;width:100%;text-align:left;background:var(--surface-subtle);border:1px solid var(--line);padding:16px;border-radius:10px;cursor:pointer;color:var(--primary)}.flow-request-spine button:hover{border-color:var(--accent)}.flow-request-selected button{background:var(--accent-soft);border-color:var(--accent);box-shadow:0 0 0 2px var(--accent-soft)}.flow-request-source{font-size:12px;color:var(--secondary)}.flow-request-title{display:flex;gap:10px;flex-wrap:wrap;align-items:center;font-size:12px}.flow-request-title strong{font-size:13px}.flow-request-title code{overflow-wrap:anywhere}.flow-request-meta{font-size:12px;color:var(--secondary);line-height:1.6}.flow-investigation-note{font-size:12px;color:var(--secondary);line-height:1.8;padding:16px 18px;border-top:1px solid var(--line);margin:0}.flow-selected-request{padding:18px;font-size:12px;line-height:1.7;overflow-wrap:anywhere}.flow-selected-request h3{font-size:13px;margin-bottom:18px}.flow-selected-request h4{font-size:12px;margin:20px 0 10px}.flow-selected-request dl{margin:0;display:grid;gap:12px}.flow-selected-request dl>div{display:grid;grid-template-columns:60px minmax(0,1fr);gap:8px}.flow-selected-request dd{margin:0}.flow-selected-request dt{color:var(--secondary)}.flow-field-proof{padding:0;list-style:none;margin:0}.flow-field-proof>li{display:grid;gap:4px;padding:12px 0;border-bottom:1px solid var(--line)}.flow-field-proof span{color:var(--secondary)}.flow-request-warnings{padding:12px 12px 12px 24px;background:var(--warning-soft);border:1px solid var(--line);border-radius:8px;color:var(--warning)}.flow-selected-request .flow-investigation-note{padding:14px 0 0;margin-top:18px}.flow-investigation{margin-top:16px}@media(min-width:1100px) and (max-width:1400px){.flow-investigation-workbench{grid-template-columns:minmax(0,1fr) 280px}}
.flow-investigation-table button.table-link{background:transparent;border:0;padding:0;color:var(--accent);font-size:12px;text-align:left;cursor:pointer}.flow-investigation-table button.table-link:hover{text-decoration:underline}.flow-tabs .button-primary{background:transparent!important;color:var(--accent)!important;border-bottom-color:var(--accent)!important}
.flow-source-details{max-height:30rem;max-width:56rem;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;font-size:.76rem}
</style>
