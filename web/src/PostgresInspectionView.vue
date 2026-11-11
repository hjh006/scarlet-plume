<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { postgresPermissionScope, postgresProfileStatusLabels, postgresProfileWarning, postgresPublicSelect, postgresSensitivityLabel, type PostgresInspection } from './postgresql'
import Icon from './Icon.vue'
import ClassificationEvaluation from './ClassificationEvaluation.vue'

const props = defineProps<{ workspace: Workspace; assetId: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const apiUrl = workspacePath(props.workspace.slug) + '/assets/' + encodeURIComponent(props.assetId) + '/postgresql'
const active = new AbortController()
const record = ref<PostgresInspection | null>(null)
const loading = ref(true)
const missing = ref(false)
const loadError = ref('')
const candidatesOnly = ref(false)
const selectedTab = ref('columns')
const tabs = computed(() => [
  { id: 'columns', label: '字段与识别', count: table.value?.columns.length },
  { id: 'content', label: '内容聚合', count: analysis.value?.columns?.length, available: Boolean(analysis.value) },
  { id: 'grants', label: '直接授权', count: table.value?.grants.length },
  { id: 'constraints', label: '表约束', count: table.value?.constraints?.length, available: Boolean(table.value?.constraints?.length) },
  { id: 'evaluation', label: '自动判定', available: Boolean(record.value?.classification) },
].filter(tab => tab.available !== false))

const table = computed(() => record.value?.table)
const candidates = computed(() => table.value?.columns.filter(column => column.sensitivity) ?? [])
const visibleColumns = computed(() => candidatesOnly.value ? candidates.value : table.value?.columns ?? [])
const publicColumnCount = computed(() => table.value?.publicSelectColumns?.length ?? 0)
const hasPublicSelect = computed(() => table.value?.publicSelect || publicColumnCount.value > 0)
const analysis = computed(() => table.value?.contentAnalysis)
const constraintLabels: Record<string, string> = { p: '主键', f: '外键', u: '唯一约束', c: '检查约束', x: '排他约束', n: '非空约束' }

async function load() {
  loading.value = true
  missing.value = false
  loadError.value = ''
  try {
    record.value = await request<PostgresInspection>(apiUrl, { signal: active.signal })
  } catch (error) {
    if (active.signal.aborted) return
    if (error instanceof ApiError && error.status === 401) emit('sessionExpired')
    else if (error instanceof ApiError && error.status === 404) {
      record.value = null
      missing.value = true
    } else loadError.value = describeError(error)
  } finally {
    loading.value = false
  }
}

onMounted(() => { void load() })
onBeforeUnmount(() => active.abort())
</script>

<template>
  <div class="postgres-record">
    <a class="back-link" :href="home + '/assets'"><Icon name="back" />返回数据资产</a>
    <header class="page-header">
      <div>
        <p class="eyebrow">PostgreSQL · 资产 #{{ assetId }}</p>
        <h1>{{ table?.name || '字段与权限' }}</h1>
        <p v-if="table" class="mono postgres-source-ref">{{ table.sourceRef }}</p>
        <p v-else>查看自动采集的字段、疑似敏感标记和授权。</p>
      </div>
      <div class="page-actions">
        <a class="button button-secondary" :href="home + '/classification/' + assetId">分类与自动判定</a>
        <button type="button" class="button button-secondary" :disabled="loading" @click="load">刷新采集结果</button>
      </div>
    </header>
    <p v-if="loading && !record" class="message" role="status">正在读取 PostgreSQL 采集结果…</p>
    <p v-else-if="loadError" class="message message-error page-feedback" role="alert">{{ loadError }}</p>
    <section v-else-if="missing" class="panel empty-state">
      <h2>暂无 PostgreSQL 采集结果</h2>
      <p>托管 PostgreSQL 任务成功采集此表后，字段和授权会在这里显示。</p>
      <a class="button button-secondary" :href="home + '/automation'">查看自动采集</a>
    </section>
    <template v-else-if="record && table">
      <dl class="scope-strip postgres-counts" aria-label="本次采集范围"><div><dt>字段</dt><dd>{{ table.columns.length }}</dd></div><div><dt>疑似敏感字段</dt><dd>{{ candidates.length }}</dd></div><div><dt>直接授权记录</dt><dd>{{ table.grants.length }}</dd></div><div><dt>采集时间</dt><dd class="postgres-count-date"><time :datetime="record.observedAt">{{ formatDate(record.observedAt) }}</time></dd></div></dl>
      <div class="postgres-workbench"><div class="section-stack postgres-main"><nav class="postgres-tabs" aria-label="查看采集证据"><button v-for="tab in tabs" :key="tab.id" :class="{ active: selectedTab === tab.id }" :aria-pressed="selectedTab === tab.id" type="button" @click="selectedTab = tab.id">{{ tab.label }}<span v-if="tab.count !== undefined">{{ tab.count }}</span></button></nav>
<section v-show="selectedTab === 'columns'" class="panel postgres-panel" aria-labelledby="postgres-columns-title">
        <div class="panel-heading">
          <div><h2 id="postgres-columns-title">字段与自动识别</h2><p>按字段名称和类型识别疑似敏感字段，供分级分类时核对。</p></div>
          <button v-if="table.columns.length" type="button" class="button"
                  :class="candidatesOnly ? 'button-primary' : 'button-secondary'" :aria-pressed="candidatesOnly"
                  @click="candidatesOnly = !candidatesOnly">仅看疑似敏感</button>
        </div>
        <div v-if="visibleColumns.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="postgres-columns-title">
          <table class="data-table postgres-table">
            <thead>
              <tr><th scope="col">字段</th><th scope="col">数据类型</th><th scope="col">可为空</th><th scope="col">疑似敏感</th><th scope="col">采集角色读取权限</th></tr>
            </thead>
            <tbody>
              <tr v-for="column in visibleColumns" :key="column.ordinal">
                <th scope="row"><span class="mono">{{ column.name }}</span><span v-if="column.comment" class="table-secondary">{{ column.comment }}</span></th>
                <td class="mono">{{ column.type }}</td>
                <td>{{ column.nullable ? '是' : '否' }}</td>
                <td><span v-if="column.sensitivity" class="badge status-needs_work">{{ postgresSensitivityLabel(column.sensitivity) }}</span><span v-else>—</span></td>
                <td>{{ column.canSelect === undefined ? '未记录' : column.canSelect ? '有 SELECT 权限' : '缺少 SELECT 权限' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">{{ candidatesOnly ? '此快照未识别到疑似敏感字段。' : '此表暂无字段。' }}</p>
      </section>
<section v-if="analysis" v-show="selectedTab === 'content'" class="panel postgres-panel" aria-labelledby="postgres-content-title">
        <div class="panel-heading">
          <div><h2 id="postgres-content-title">源库格式聚合</h2><p>展示源库返回的计数，业务值保留在来源系统。</p></div>
          <span class="badge status-draft">{{ postgresProfileStatusLabels[analysis.status] || analysis.status }}</span>
        </div>
        <dl class="detail-list">
          <div><dt>样本可见行数</dt><dd>{{ analysis.rowsScanned }} 行</dd></div>
          <div><dt>本轮行数上限</dt><dd>{{ analysis.rowLimit }} 行</dd></div>
        </dl>
        <p class="postgres-profile-hint">计数来自有界前缀样本，规模和人数需另行核验。</p>
        <ul v-if="analysis.warnings?.length" class="postgres-profile-warnings"><li v-for="warning in analysis.warnings" :key="warning">{{ postgresProfileWarning(warning) }}</li></ul>
        <div v-if="analysis.columns?.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="postgres-content-title">
          <table class="data-table postgres-table">
            <thead><tr><th scope="col">字段</th><th scope="col">可见非空行数</th><th scope="col">格式命中行数</th></tr></thead>
            <tbody>
              <tr v-for="column in analysis.columns" :key="column.column">
                <th scope="row" class="mono">{{ column.column }}</th>
                <td>{{ column.nonNullRows }}<span v-if="column.limitedRows" class="table-secondary">{{ column.limitedRows }} 行超出格式分析长度</span></td>
                <td>
                  <span v-for="match in column.matches" :key="match.category" class="table-secondary">{{ postgresSensitivityLabel(match.category) }} · {{ match.count }} 行</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">本次没有可用的字段聚合结果。</p>
      </section>
<section v-show="selectedTab === 'grants'" class="panel postgres-panel" aria-labelledby="postgres-grants-title">
        <div class="panel-heading">
          <div><h2 id="postgres-grants-title">直接授权</h2><p>{{ postgresPermissionScope }}</p></div>
        </div>
        <div v-if="table.grants.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="postgres-grants-title">
          <table class="data-table postgres-table postgres-grants">
            <thead>
              <tr>
                <th scope="col">被授权角色</th><th scope="col">对象</th><th scope="col">权限</th>
                <th scope="col">可转授权</th><th scope="col">Schema USAGE</th><th scope="col">授权方</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="grant in table.grants" :key="JSON.stringify(grant)">
                <th scope="row">
                  <span v-if="grant.public" class="badge status-needs_work">PUBLIC</span>
                  <span v-else class="mono">{{ grant.grantee }}</span>
                </th>
                <td v-if="grant.column" class="mono">{{ grant.column }}<span class="table-secondary">字段</span></td>
                <td v-else>整表</td>
                <td class="mono">{{ grant.privilege }}</td>
                <td>{{ grant.grantable ? '是' : '否' }}</td>
                <td>{{ grant.schemaUsage ? '有' : '未检出' }}</td>
                <td class="mono">{{ grant.grantor || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">此快照未检出直接授权。</p>
      </section>
<section v-if="table.constraints?.length" v-show="selectedTab === 'constraints'" class="panel postgres-panel" aria-labelledby="postgres-constraints-title">
        <div class="panel-heading"><h2 id="postgres-constraints-title">表约束</h2></div>
        <div class="table-scroll" role="region" tabindex="0" aria-labelledby="postgres-constraints-title">
          <table class="data-table postgres-table">
            <thead><tr><th scope="col">约束</th><th scope="col">类型</th><th scope="col">字段</th><th scope="col">验证状态</th></tr></thead>
            <tbody><tr v-for="constraint in table.constraints" :key="constraint.name">
              <th scope="row" class="mono">{{ constraint.name }}</th><td>{{ constraintLabels[constraint.type] || constraint.type }}</td>
              <td class="mono">{{ constraint.columns?.join('、') || '—' }}</td><td>{{ constraint.validated ? '已验证' : '待验证' }}</td>
            </tr></tbody>
          </table>
        </div>
      </section>
<section v-if="record.classification" v-show="selectedTab === 'evaluation'" class="panel postgres-panel" aria-labelledby="postgres-classification-title">
        <div class="panel-heading"><h2 id="postgres-classification-title">本次采集的自动判定</h2></div>
        <div class="postgres-profile-body"><ClassificationEvaluation :automatic="record.classification" compact /></div>
      </section></div><aside class="context-rail postgres-context"><section class="panel postgres-panel" aria-labelledby="postgres-observation-title" :aria-busy="loading">
        <div class="panel-heading">
          <div><h2 id="postgres-observation-title">采集概况</h2><p>字段和权限来自本次 PostgreSQL 系统目录快照。</p></div>
          <span v-if="hasPublicSelect" class="badge status-needs_work">PUBLIC SELECT 授权</span>
        </div>
        <dl class="detail-list">
          <div><dt>表所有者</dt><dd class="mono">{{ table.owner || '未记录' }}</dd></div>
          <div><dt>字段自动识别</dt><dd>{{ table.columns.length }} 个字段 · {{ candidates.length }} 个疑似敏感</dd></div>
          <div v-if="table.metadataStatus"><dt>元数据范围</dt><dd>{{ postgresProfileStatusLabels[table.metadataStatus] || table.metadataStatus }}</dd></div>
          <div v-if="table.estimatedRows !== undefined"><dt>优化器估计行数</dt><dd>{{ table.estimatedRows }}<span class="table-secondary">估计可能陈旧，实际规模待核验。</span></dd></div>
          <div v-if="table.rowSecurity"><dt>行级策略</dt><dd>{{ table.rowSecurity.active ? '当前角色受行级策略限制' : '当前角色无行级策略过滤' }}</dd></div>
          <div v-if="table.comment" class="detail-wide"><dt>表注释</dt><dd class="text-block">{{ table.comment }}</dd></div>
          <div><dt>采集时间</dt><dd><time :datetime="record.observedAt">{{ formatDate(record.observedAt) }}</time></dd></div>
          <div>
            <dt>运行来源</dt>
            <dd><a class="table-link" :href="home + '/automation'">任务 #{{ record.jobId }}</a>
              <span class="table-secondary mono">{{ record.runId }}</span></dd>
          </div>
          <div class="detail-wide">
            <dt>PUBLIC 权限</dt>
            <dd>{{ postgresPublicSelect(table.publicSelect, publicColumnCount) }} ·
              {{ table.publicSchemaUsage ? 'PUBLIC 具备 schema USAGE' : 'PUBLIC 未获 schema USAGE' }}
            </dd>
          </div>
        </dl>
        <ul v-if="table.warnings?.length" class="postgres-profile-warnings"><li v-for="warning in table.warnings" :key="warning">{{ postgresProfileWarning(warning) }}</li></ul>
      </section><section class="panel postgres-boundary"><h3>证据边界</h3><p>字段名称与格式命中属于识别线索。权限来自目录中的直接授权快照；角色继承、会话设置和业务访问需结合来源系统核验。</p><a class="table-link" :href="home + '/classification/' + assetId">查看分类判定依据 <Icon name="chevron" /></a></section></aside></div>
    </template>
  </div>
</template>

<style scoped>
.postgres-profile-body { padding: 16px 20px; }
.postgres-workbench{display:grid;grid-template-columns:minmax(0,1fr) 300px;align-items:start;gap:16px}.postgres-workbench>*{min-width:0}.postgres-counts{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));padding:18px 20px;margin-bottom:16px;gap:20px}.postgres-counts dt{font-size:12px;color:var(--secondary)}.postgres-counts dd{font-size:26px;font-weight:650;margin-top:8px;letter-spacing:-.03em}.postgres-counts .postgres-count-date{font-size:12px;letter-spacing:normal}.postgres-tabs{display:flex;gap:22px;padding:0 20px;background:var(--surface);border:1px solid var(--line);border-radius:12px;overflow-x:auto}.postgres-tabs>button{display:flex;align-items:center;gap:7px;flex-shrink:0;border:0;border-bottom:2px solid transparent;padding:15px 0;background:none;color:var(--secondary);font:inherit;cursor:pointer}.postgres-tabs>button.active{border-color:var(--accent);color:var(--accent);font-weight:650}.postgres-tabs>button>span{border-radius:4px;padding:1px 5px;background:var(--surface-subtle);font-size:11px}.postgres-context .detail-list{grid-template-columns:minmax(0,1fr)}.postgres-context .panel-heading{align-items:flex-start;flex-direction:column;gap:10px}.postgres-context dd{overflow-wrap:anywhere}.postgres-context .detail-list .detail-wide{grid-column:auto}.postgres-boundary{padding:20px}.postgres-boundary h3{font-size:14px}.postgres-boundary p{margin:10px 0;font-size:12px;line-height:1.75;color:var(--secondary)}.postgres-boundary .table-link{display:inline-flex;align-items:center;gap:8px}.postgres-main .postgres-panel{margin:0}.postgres-main .postgres-table{min-width:630px}@media(max-width:1399px){.postgres-workbench{grid-template-columns:minmax(0,1fr) 270px}.postgres-tabs{gap:16px}}
.postgres-profile-hint { margin: 0 1.1rem .65rem; color: var(--secondary); font-size: .83rem; }
.postgres-profile-warnings { margin: .45rem 1.1rem .8rem; padding-left: 1.1rem; color: var(--warning); font-size: .83rem; }
.postgres-context{display:grid;gap:16px}.postgres-counts dd{color:var(--ink)}
</style>
