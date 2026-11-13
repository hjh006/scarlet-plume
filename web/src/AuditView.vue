<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, downloadFile, formatDate, request, workspacePath, type Workspace } from './api'
import Icon from './Icon.vue'

interface AuditPage<T> { items: T[]; limit: number; truncated?: boolean }
interface RequestAudit {
  id: number; observedAt: string; sourceId: number; source: string; service: string
  kind: 'gateway' | 'otlp'; method: string; route: string; status: number
}
interface ActionAudit { at: string; actor: string; action: string; targetType: string; targetId: number; automated?: boolean }
interface RequestAuditPage extends AuditPage<RequestAudit> { retentionDays: number; retentionLimitPerSource?: number }
const props = defineProps<{ workspace: Workspace; tab: 'runtime' | 'actions' }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const base = workspacePath(props.workspace.slug)
const apiPath = base + '/audit/' + props.tab
const actionsTab = props.tab === 'actions'
const active = new AbortController()
const hours = ref(24)
const sourceId = ref('')
const method = ref('')
const routeTemplate = ref('')
const status = ref<number | ''>('')
const sources = ref<{ id: number; name: string; service: string }[]>([])
const sourceError = ref('')
const runtime = ref<RequestAuditPage | null>(null)
const actions = ref<AuditPage<ActionAudit> | null>(null)
const loading = ref(false)
const exporting = ref(false)
const error = ref('')
const exportError = ref('')
const appliedQuery = ref('')
const selectedIndex = ref(0)
const selectedRequest = computed(() => runtime.value?.items[selectedIndex.value] ?? runtime.value?.items[0])
const selectedAction = computed(() => actions.value?.items[selectedIndex.value] ?? actions.value?.items[0])
const methods = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS', 'TRACE']
const actionLabels: Record<string, string> = {
  'check.created': '创建检查',
  'check.evidence': '登记证据',
  'check.submitted': '提交复核',
  'check.changes': '要求整改',
  'check.verified': '独立复核通过',
  'check.postgres_binding': '绑定 PostgreSQL 来源',
  'classification.reviewed': '确认数据分类',
  'classification.detected': '自动识别数据分类',
  'risk.triaged': '更新风险研判',
  'api.scanned': '扫描 API 契约',
  'runtime.source_created': '创建观测来源',
  'runtime.source_revoked': '撤销观测来源',
  'runtime.policy_updated': '保存运行时检测策略版本',
}
const targetLabels: Record<string, string> = {
  check: '检查', asset: '资产', runtime_finding: '运行时发现', api_scan: '契约扫描', runtime_source: '观测来源', runtime_policy: '检测策略版本',
}
const targetPaths: Record<string, string> = { check: '/checks/', asset: '/classification/', api_scan: '/api-security/' }
const kindLabels = { gateway: '独立采集 Agent / 网关日志', otlp: '应用探针（OTLP）' }

function errorText(failure: unknown) {
  if (active.signal.aborted) return ''
  if (failure instanceof ApiError && failure.status === 401) {
    emit('sessionExpired')
    return ''
  }
  return describeError(failure)
}

async function loadSources() {
  try {
    const result = await request<{ items: typeof sources.value }>(base + '/api-security/runtime/sources', { signal: active.signal })
    sources.value = result.items ?? []
  } catch (failure) {
    sourceError.value = errorText(failure)
  }
}

async function load() {
  if (loading.value || exporting.value) return
  loading.value = true
  error.value = ''
  exportError.value = ''
  const query = new URLSearchParams({ hours: String(hours.value) })
  if (!actionsTab) {
    if (sourceId.value) query.set('sourceId', sourceId.value)
    if (method.value) query.set('method', method.value)
    if (status.value !== '') query.set('status', String(status.value))
    if (routeTemplate.value.trim()) query.set('route', routeTemplate.value.trim())
  }
  try {
    if (actionsTab) actions.value = await request<AuditPage<ActionAudit>>(apiPath + '?' + query, { signal: active.signal })
    else runtime.value = await request<RequestAuditPage>(apiPath + '?' + query, { signal: active.signal })
    appliedQuery.value = query.toString()
  } catch (failure) {
    error.value = errorText(failure)
  } finally {
    loading.value = false
  }
}

async function exportAudit() {
  if (!appliedQuery.value || exporting.value || loading.value) return
  exporting.value = true
  exportError.value = ''
  try {
    await downloadFile(apiPath + '/export?' + appliedQuery.value, 'scarlet-' + props.tab + '-audit.json', active.signal)
  } catch (failure) {
    exportError.value = errorText(failure)
  } finally {
    exporting.value = false
  }
}

onMounted(() => {
  void load()
  if (!actionsTab) void loadSources()
})
onBeforeUnmount(() => active.abort())
</script>

<template>
  <div class="audit-view">
    <header class="page-header">
      <div>
        <p class="eyebrow">安全与集成</p>
        <h1>审计中心</h1>
        <p>按条件查询运行时请求和平台关键操作，按最近一次查询条件导出。</p>
      </div>
      <div class="page-actions">
        <button type="button" class="button button-primary" :disabled="!appliedQuery || loading || exporting" @click="exportAudit">
          <Icon name="download" />{{ exporting ? '正在导出…' : '导出 JSON' }}
        </button>
      </div>
    </header>
    <nav class="audit-tabs" aria-label="审计范围">
      <a class="button" :class="actionsTab ? 'button-secondary' : 'button-primary'" :href="home + '/audit'"
         :aria-current="actionsTab ? undefined : 'page'">请求审计</a>
      <a class="button" :class="actionsTab ? 'button-primary' : 'button-secondary'" :href="home + '/audit/actions'"
         :aria-current="actionsTab ? 'page' : undefined">平台操作</a>
    </nav>
    <p v-if="exportError" class="message message-error page-feedback" role="alert">{{ exportError }}</p>
    <div class="audit-workbench"><section class="panel" aria-labelledby="audit-title" :aria-busy="loading">
      <div class="panel-heading">
        <div>
          <h2 id="audit-title">{{ actionsTab ? '平台操作记录' : '请求审计记录' }}</h2>
          <p v-if="actionsTab">记录经办人、时间、操作与对象标识。</p>
          <p v-else>查询已采集的请求元数据。记录包含路由模板、HTTP 方法、状态码、来源和观测时间。</p>
        </div>
      </div>
      <form class="audit-filter" @submit.prevent="load">
        <div class="field">
          <label for="audit-hours">时间范围</label>
          <select id="audit-hours" v-model.number="hours">
            <option v-if="!actionsTab" :value="1">最近 1 小时</option><option v-if="!actionsTab" :value="6">最近 6 小时</option>
            <option :value="24">最近 24 小时</option><option :value="168">最近 7 天</option>
            <option v-if="actionsTab" :value="720">最近 30 天</option>
          </select>
        </div>
        <template v-if="!actionsTab">
          <div class="field">
            <label for="audit-source">观测来源</label>
            <select id="audit-source" v-model="sourceId">
              <option value="">全部来源</option>
              <option v-for="source in sources" :key="source.id" :value="String(source.id)">{{ source.name }} · {{ source.service }}</option>
            </select>
          </div>
          <div class="field">
            <label for="audit-method">HTTP 方法</label>
            <select id="audit-method" v-model="method">
              <option value="">全部方法</option><option v-for="value in methods" :key="value" :value="value">{{ value }}</option>
            </select>
          </div>
          <div class="field">
            <label for="audit-status">HTTP 状态码</label>
            <input id="audit-status" v-model.number="status" type="number" min="100" max="599" step="1" placeholder="例如 410">
          </div>
          <div class="field">
            <label for="audit-route">路由模板</label>
            <input id="audit-route" v-model="routeTemplate" maxlength="240" placeholder="/v1/items/{id}" title="支持按路由模板片段查询"
                   spellcheck="false" autocapitalize="none">
          </div>
        </template>
        <button type="submit" class="button button-secondary" :disabled="loading || exporting">{{ loading ? '正在查询…' : '查询' }}</button>
      </form>
      <p v-if="sourceError" class="field-hint audit-note">观测来源列表暂不可用，可继续按其他条件查询。</p>
      <p v-if="error" class="message message-error audit-note" role="alert">{{ error }}</p>
      <p v-else-if="loading" class="empty-state" role="status">正在读取审计记录…</p>
      <template v-else-if="runtime || actions">
        <p v-if="runtime" class="field-hint audit-note">
          保留最近 {{ runtime.retentionDays }} 天的记录<span v-if="runtime.retentionLimitPerSource">，每个来源最多 {{ runtime.retentionLimitPerSource }} 条</span>。
        </p>
        <p v-if="runtime?.truncated || actions?.truncated" class="message message-warning audit-note" role="status">
          当前查询仅展示最近 {{ runtime?.limit ?? actions?.limit }} 条，导出采用相同上限。可缩小时间范围继续查询。
        </p>
        <div v-if="runtime?.items.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="audit-title">
          <table class="data-table audit-table">
            <thead><tr><th scope="col">观测时间</th><th scope="col">来源与服务</th><th scope="col">方法</th><th scope="col">路由模板</th><th scope="col">状态码</th></tr></thead>
            <tbody>
              <tr v-for="(entry,index) in runtime.items" :key="entry.id" :class="{selected: selectedRequest?.id === entry.id}">
                <td><time :datetime="entry.observedAt">{{ formatDate(entry.observedAt) }}</time></td>
                <th scope="row">{{ entry.source }}<span class="table-secondary">{{ kindLabels[entry.kind] }} · {{ entry.service }}</span></th>
                <td class="mono">{{ entry.method }}</td>
                <td class="mono"><button class="audit-record-link" @click="selectedIndex = index">{{ entry.route }}</button></td>
                <td><span class="badge" :class="entry.status >= 500 ? 'status-needs_work' : entry.status >= 400 ? 'status-in_review' : 'status-draft'">{{ entry.status }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else-if="actions?.items.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="audit-title">
          <table class="data-table audit-table audit-actions-table">
            <thead><tr><th scope="col">操作时间</th><th scope="col">经办人</th><th scope="col">操作</th><th scope="col">对象</th></tr></thead>
            <tbody>
              <tr v-for="(entry, index) in actions.items" :key="index" :class="{selected: selectedAction === entry}">
                <td><time :datetime="entry.at">{{ formatDate(entry.at) }}</time></td>
                <th scope="row">{{ entry.actor }}</th>
                <td>
                  <button class="audit-record-link" @click="selectedIndex = index">{{ actionLabels[entry.action] || entry.action }}</button>
                  <span v-if="entry.automated" class="table-secondary">自动登记</span>
                </td>
                <td>
                  <a v-if="targetPaths[entry.targetType]" class="table-link" :href="home + targetPaths[entry.targetType] + entry.targetId">
                    {{ targetLabels[entry.targetType] }} #{{ entry.targetId }}
                  </a>
                  <span v-else>{{ targetLabels[entry.targetType] || entry.targetType }} #{{ entry.targetId }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">所选范围内暂无审计记录。</p>
      </template>
    </section><aside class="audit-detail-rail"><section class="panel"><p class="eyebrow">{{ actionsTab ? '操作详情' : '请求详情' }}</p><template v-if="selectedRequest && !actionsTab"><h2><span class="audit-method">{{ selectedRequest.method }}</span>{{ selectedRequest.route }}</h2><dl><div><dt>观察时间</dt><dd>{{ formatDate(selectedRequest.observedAt) }}</dd></div><div><dt>来源</dt><dd>{{ selectedRequest.source }} · #{{ selectedRequest.sourceId }}</dd></div><div><dt>服务</dt><dd>{{ selectedRequest.service }}</dd></div><div><dt>采集方式</dt><dd>{{ kindLabels[selectedRequest.kind] }}</dd></div><div><dt>状态码</dt><dd><span class="badge">{{ selectedRequest.status }}</span></dd></div><div><dt>审计编号</dt><dd>#{{ selectedRequest.id }}</dd></div></dl></template><template v-else-if="selectedAction"><h2>{{ actionLabels[selectedAction.action] || selectedAction.action }}</h2><dl><div><dt>操作时间</dt><dd>{{ formatDate(selectedAction.at) }}</dd></div><div><dt>经办人</dt><dd>{{ selectedAction.actor }}</dd></div><div><dt>对象</dt><dd>{{ targetLabels[selectedAction.targetType] || selectedAction.targetType }} #{{ selectedAction.targetId }}</dd></div><div><dt>登记方式</dt><dd>{{ selectedAction.automated ? '自动登记' : '平台操作' }}</dd></div></dl><a v-if="targetPaths[selectedAction.targetType]" class="button button-secondary" :href="home + targetPaths[selectedAction.targetType] + selectedAction.targetId">打开关联对象</a></template><p v-else>选择记录查看来源与审计标识。</p></section><section class="panel"><h2>查询与留存</h2><p>当前列表 {{ runtime?.items.length ?? actions?.items.length ?? 0 }} 条。{{ runtime?.truncated || actions?.truncated ? '已达到本次查询上限，请缩小查询范围。' : '数量仅代表本次查询返回的记录。' }}</p><p v-if="runtime">保留最近 {{ runtime.retentionDays }} 天<span v-if="runtime.retentionLimitPerSource">，每个来源最多 {{ runtime.retentionLimitPerSource }} 条</span>。</p><p>导出沿用最近一次成功查询的条件和展示上限。</p></section></aside></div>
  </div>
</template>

<style scoped>
.audit-tabs { display: flex; flex-wrap: wrap; gap: .55rem; margin-bottom: 1rem; }
.audit-filter { display: flex; flex-wrap: wrap; align-items: end; gap: .65rem; padding: .85rem 1.1rem; border-bottom: 1px solid var(--line); }
.audit-filter .field { flex: 1 1 8.5rem; min-width: 0; max-width: 16rem; margin: 0; }
.audit-note { margin: .75rem 1.1rem; }
.audit-table { min-width: 39rem; }
.audit-actions-table { min-width: 29rem; }
.audit-table th, .audit-table td { overflow-wrap: anywhere; }
@media (max-width: 43.99rem) {
  .audit-filter .field { flex-basis: 7rem; }
}
</style>

<style scoped>.audit-workbench{display:grid;grid-template-columns:minmax(0,1fr) 300px;gap:16px;align-items:start}.audit-tabs{border-bottom:1px solid var(--line);gap:24px;padding:0 0 10px}.audit-tabs .button{background:none;border:0;color:var(--muted);padding-left:0;padding-right:0;border-radius:0}.audit-tabs .button-primary{color:var(--blue);box-shadow:0 11px 0 -9px var(--blue)}.audit-filter{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:14px;padding:20px}.audit-filter .field{max-width:none;width:100%;font-size:12px}.audit-filter>.button{align-self:end}.audit-table{min-width:640px}.audit-table tr.selected{background:var(--accent-soft)}.audit-record-link{border:0;background:none;color:var(--blue);font:inherit;text-align:left;cursor:pointer;padding:0;overflow-wrap:anywhere}.audit-detail-rail{display:grid;gap:16px;position:sticky;top:84px}.audit-detail-rail .panel{padding:20px}.audit-detail-rail h2{font-size:14px;line-height:1.8;overflow-wrap:anywhere;margin:0 0 16px}.audit-detail-rail p{font-size:12px;color:var(--muted);line-height:1.9}.audit-detail-rail dl{display:grid;gap:16px;margin:20px 0}.audit-detail-rail dl>div{display:grid;grid-template-columns:70px minmax(0,1fr);gap:9px;font-size:12px}.audit-detail-rail dt{color:var(--muted)}.audit-detail-rail dd{margin:0;overflow-wrap:anywhere}.audit-method{display:inline-block;color:var(--blue);font-size:12px;margin-right:8px}.audit-actions-table{min-width:560px}@media(max-width:1400px){.audit-workbench{grid-template-columns:minmax(0,1fr) 270px}.audit-filter{gap:12px;padding:16px}}</style>
