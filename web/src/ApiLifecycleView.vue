<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, downloadFile, formatDate, request, workspacePath, type Workspace } from './api'
import { lifecycleAuthLabels, lifecycleCategoryLabels, lifecycleChangeLabels, lifecycleLocationLabels, lifecycleStateLabels, type ApiLifecycle } from './api-lifecycle'
import Icon from './Icon.vue'

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const endpoint = workspacePath(props.workspace.slug) + '/api-security/lifecycle'
const active = new AbortController()
const page = ref<ApiLifecycle | null>(null)
const sources = ref<ApiLifecycle['sources']>([])
const service = ref('')
const sourceId = ref('')
const loading = ref(false)
const exporting = ref(false)
const error = ref('')
const exportError = ref('')
const appliedQuery = ref('')
const visibleCount = ref(50)
const changed = computed(() => page.value?.comparisons.reduce((count, item) => count + item.changes.length, 0) ?? 0)
const observed = computed(() => page.value?.items.filter(item => item.observed).length ?? 0)

function errorText(failure: unknown) {
  if (active.signal.aborted) return ''
  if (failure instanceof ApiError && failure.status === 401) {
    emit('sessionExpired')
    return ''
  }
  return describeError(failure)
}

function chooseSource() {
  const selected = sources.value.find(item => String(item.id) === sourceId.value)
  if (selected) service.value = selected.service
}

async function load() {
  if (loading.value || exporting.value) return
  loading.value = true
  error.value = ''
  exportError.value = ''
  const query = new URLSearchParams()
  if (service.value.trim()) query.set('service', service.value.trim())
  if (sourceId.value) query.set('sourceId', sourceId.value)
  try {
    page.value = await request<ApiLifecycle>(endpoint + (query.size ? '?' + query : ''), { signal: active.signal })
    sources.value = page.value.sources
    appliedQuery.value = query.toString()
    visibleCount.value = 50
  } catch (failure) {
    page.value = null
    error.value = errorText(failure)
  } finally {
    loading.value = false
  }
}

async function exportDirectory() {
  if (!page.value || loading.value || exporting.value) return
  exporting.value = true
  exportError.value = ''
  try {
    await downloadFile(endpoint + '/export' + (appliedQuery.value ? '?' + appliedQuery.value : ''), 'scarlet-api-lifecycle.json', active.signal)
  } catch (failure) {
    exportError.value = errorText(failure)
  } finally {
    exporting.value = false
  }
}

onMounted(() => { void load() })
onBeforeUnmount(() => active.abort())
</script>

<template>
  <div class="lifecycle-view">
    <header class="page-header">
      <div>
        <p class="eyebrow">API 安全</p>
        <h1>API 生命周期</h1>
        <p>对照已登记的契约与实际路由观测，追踪接口和敏感字段声明的版本变化。</p>
      </div>
      <div class="page-actions">
        <a class="button button-secondary" :href="home + '/api-security'">契约扫描</a>
        <button type="button" class="button button-primary" :disabled="!page || loading || exporting" @click="exportDirectory">
          <Icon name="download" />{{ exporting ? '正在导出…' : '导出 JSON' }}
        </button>
      </div>
    </header>
    <p v-if="exportError" class="message message-error page-feedback" role="alert">{{ exportError }}</p>
    <section class="panel" aria-labelledby="lifecycle-scope-title" :aria-busy="loading">
      <div class="panel-heading">
        <div><h2 id="lifecycle-scope-title">目录范围</h2><p>按准确服务别名关联契约与观测；选择来源后，仅展示该来源的运行时记录。</p></div>
      </div>
      <form class="lifecycle-filters" @submit.prevent="load">
        <div class="field">
          <label for="lifecycle-service">服务别名</label>
          <input id="lifecycle-service" v-model="service" list="lifecycle-services" maxlength="80" placeholder="全部服务" @input="sourceId = ''">
          <datalist id="lifecycle-services"><option v-for="alias in page?.services ?? []" :key="alias" :value="alias" /></datalist>
        </div>
        <div class="field">
          <label for="lifecycle-source">运行时来源</label>
          <select id="lifecycle-source" v-model="sourceId" @change="chooseSource">
            <option value="">全部来源</option>
            <option v-for="source in sources" :key="source.id" :value="String(source.id)">{{ source.name }} · {{ source.service }}{{ source.revokedAt ? '（已撤销）' : '' }}</option>
          </select>
        </div>
        <button type="submit" class="button button-secondary" :disabled="loading || exporting">{{ loading ? '正在查询…' : '查询目录' }}</button>
      </form>
      <p v-if="error" class="message message-error lifecycle-feedback" role="alert">{{ error }}</p>
      <p v-else-if="loading" class="empty-state" role="status">正在对照契约与运行时观测…</p>
      <div v-else-if="page" class="lifecycle-summary">
        <p><strong>{{ page.items.length }}</strong> 项接口 · <strong>{{ observed }}</strong> 项有历史观测 · <strong>{{ changed }}</strong> 项版本变更</p>
        <p class="muted">生成于 {{ formatDate(page.generatedAt) }}。未观测接口的实际使用情况待核实；下线待核实项需结合契约时间与最后观测时间复测。</p>
      </div>
    </section>
    <template v-if="page && !loading">
      <section class="panel" aria-labelledby="lifecycle-directory-title">
        <div class="panel-heading"><div><h2 id="lifecycle-directory-title">统一接口目录</h2><p>最新契约与运行时路由的并集。计数按来源分别保留，包含已撤销来源的历史观测。</p></div></div>
        <div v-if="page.items.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="lifecycle-directory-title">
          <table class="data-table lifecycle-table">
            <thead><tr><th scope="col">服务与接口</th><th scope="col">生命周期状态</th><th scope="col">声明与字段候选</th><th scope="col">观测来源</th><th scope="col">相关记录</th></tr></thead>
            <tbody>
              <tr v-for="item in page.items.slice(0, visibleCount)" :key="item.service + item.method + item.routeKey">
                <th scope="row"><span class="table-secondary">{{ item.service }}</span><code>{{ item.method }} {{ item.route }}</code><span v-if="item.paths.length > 1" class="table-secondary">归一化合并 {{ item.paths.length }} 个参数命名变体</span></th>
                <td>{{ lifecycleStateLabels[item.state] }}<span v-if="item.lastSeen" class="table-secondary">最后观测 {{ formatDate(item.lastSeen) }}</span></td>
                <td>
                  <span>{{ item.auth.map(value => lifecycleAuthLabels[value] || value).join(' / ') || '无匹配契约声明' }}</span>
                  <span v-if="item.deprecated || item.deprecationMixed" class="table-secondary">{{ item.deprecationMixed ? '部分参数变体声明弃用' : '已声明弃用' }}</span>
                  <details v-if="item.sensitiveFields.length"><summary>{{ item.sensitiveFields.length }} 项敏感字段声明候选</summary><ul class="lifecycle-records"><li v-for="field in item.sensitiveFields" :key="field.location + field.name + field.category">{{ lifecycleLocationLabels[field.location] || field.location }} · <code>{{ field.name }}</code> · {{ lifecycleCategoryLabels[field.category] || field.category }}</li></ul></details>
                </td>
                <td>
                  <ul v-if="item.sources.length" class="lifecycle-records"><li v-for="source in item.sources" :key="source.id"><strong>{{ source.name }}</strong>{{ source.revokedAt ? '（已撤销）' : '' }}<span class="table-secondary">{{ source.kind === 'otlp' ? '应用探针' : '网关 / Agent' }} · 已采集 {{ source.requests }} 次 · 4xx {{ source.responses4xx }} · 5xx {{ source.responses5xx }}</span></li></ul>
                  <span v-else class="muted">尚无匹配观测</span>
                </td>
                <td class="lifecycle-links">
                  <a v-if="item.scanId" class="table-link" :href="home + '/api-security/' + item.scanId">契约 #{{ item.scanId }}</a>
                  <a v-for="id in item.assetIds" :key="'asset' + id" class="table-link" :href="home + '/classification/' + id">资产 #{{ id }}</a>
                  <a v-for="id in item.checkIds" :key="'check' + id" class="table-link" :href="home + '/checks/' + id">检查 #{{ id }}</a>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">所选范围暂无接口。可先导入 OpenAPI 契约或连接运行时来源。</p>
        <div v-if="page.items.length > visibleCount" class="lifecycle-more"><button type="button" class="button button-secondary" @click="visibleCount += 50">再显示 50 项（共 {{ page.items.length }} 项）</button></div>
      </section>
      <section class="panel" aria-labelledby="lifecycle-changes-title">
        <div class="panel-heading"><div><h2 id="lifecycle-changes-title">最近版本变更</h2><p>每个服务仅比较最近前一版与当前契约；参数名称变体归一化后匹配。字段候选来自契约声明。</p></div></div>
        <div v-for="comparison in page.comparisons" :key="comparison.service" class="lifecycle-comparison">
          <h3>{{ comparison.service }}</h3>
          <p class="muted"><a v-if="comparison.previousScanId" class="table-link" :href="home + '/api-security/' + comparison.previousScanId">上一版 #{{ comparison.previousScanId }}</a>{{ comparison.previousScanId ? ' → ' : '' }}<a class="table-link" :href="home + '/api-security/' + comparison.scanId">当前版 #{{ comparison.scanId }}</a> · {{ formatDate(comparison.createdAt) }}</p>
          <p v-if="comparison.initial" class="muted">首个契约版本，尚无前版可比较。</p>
          <p v-else-if="!comparison.changes.length" class="muted">本次对比未发现所支持维度的变化。</p>
          <details v-for="change in comparison.changes" :key="change.method + change.routeKey" class="lifecycle-change">
            <summary><code>{{ change.method }} {{ change.route }}</code><span>{{ change.kinds.map(kind => lifecycleChangeLabels[kind]).join(' · ') }}</span></summary>
            <div class="lifecycle-versions">
              <div v-for="version in [{ title: '变更前', operation: change.before }, { title: '变更后', operation: change.after }]" :key="version.title">
                <h4>{{ version.title }}</h4>
                <template v-if="version.operation">
                  <p>{{ version.operation.auth.map(value => lifecycleAuthLabels[value] || value).join(' / ') }} · {{ version.operation.deprecationMixed ? '部分变体弃用' : version.operation.deprecated ? '声明弃用' : '未声明弃用' }}</p>
                  <ul v-if="version.operation.sensitiveFields.length" class="lifecycle-records"><li v-for="field in version.operation.sensitiveFields" :key="field.location + field.name + field.category">{{ lifecycleLocationLabels[field.location] || field.location }} · <code>{{ field.name }}</code> · {{ lifecycleCategoryLabels[field.category] || field.category }}</li></ul>
                  <p v-else class="muted">未识别敏感字段声明候选</p>
                  <p class="lifecycle-inline-links"><a v-for="id in version.operation.assetIds" :key="'asset' + id" class="table-link" :href="home + '/classification/' + id">资产 #{{ id }}</a><a v-for="id in version.operation.checkIds" :key="'check' + id" class="table-link" :href="home + '/checks/' + id">检查 #{{ id }}</a></p>
                </template>
                <p v-else class="muted">该版本无此接口</p>
              </div>
            </div>
          </details>
        </div>
        <p v-if="!page.comparisons.length" class="empty-state">所选服务暂无契约版本，可先导入 OpenAPI。</p>
      </section>
    </template>
  </div>
</template>

<style scoped>
.lifecycle-view { display: grid; gap: 1rem; min-width: 0; }
.lifecycle-filters { display: flex; flex-wrap: wrap; align-items: end; gap: .8rem; padding: 1rem 1.1rem; }
.lifecycle-filters .field { flex: 1 1 14rem; min-width: 0; max-width: 24rem; margin: 0; }
.lifecycle-summary, .lifecycle-feedback { margin: 0; padding: .8rem 1.1rem; }
.lifecycle-summary p { margin: .25rem 0; }
.lifecycle-table { min-width: 58rem; }
.lifecycle-table th, .lifecycle-table td { vertical-align: top; }
.lifecycle-table code, .lifecycle-change code { overflow-wrap: anywhere; white-space: normal; }
.lifecycle-links .table-link { display: block; margin-bottom: .3rem; }
.lifecycle-records { margin: .35rem 0; padding-inline-start: 1.1rem; }
.lifecycle-records li + li { margin-top: .5rem; }
.lifecycle-table summary, .lifecycle-change summary { cursor: pointer; }
.lifecycle-table summary { margin-top: .4rem; color: var(--accent); }
.lifecycle-comparison { padding: 1rem 1.1rem; border-top: 1px solid var(--line); }
.lifecycle-comparison h3, .lifecycle-versions h4 { margin: 0 0 .5rem; }
.lifecycle-change { padding: .85rem 0; border-bottom: 1px solid var(--line); }
.lifecycle-change summary { display: list-item; }
.lifecycle-change summary span { display: block; margin: .4rem 0 0 1rem; color: var(--secondary); }
.lifecycle-versions { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1rem; margin-top: 1rem; }
.lifecycle-inline-links { display: flex; flex-wrap: wrap; gap: .6rem; }
.lifecycle-more { padding: 1rem 1.1rem; }
@media (max-width: 640px) { .lifecycle-versions { grid-template-columns: 1fr; } .lifecycle-filters .field { max-width: none; } }
</style>
