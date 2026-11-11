<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, downloadFile, formatDate, request, workspacePath, type Workspace } from './api'
import { postgresSensitivityLabel as sensitivityLabel } from './postgresql'
import { canManageChecks } from './workflow'
import Icon from './Icon.vue'
import './api-security-view.css'

interface ScanSummary {
  id: number
  source: string
  createdAt: string
  version: string
  operationCount: number
  sensitiveFieldCount: number
  findingCount: number
  checkCount: number
  unresolvedReferences: number
}

interface ScanDetail extends ScanSummary {
  operations: {
    method: string
    path: string
    auth: string
    deprecated: boolean
    sensitiveFields: { location: string; name: string; category: string }[] | null
    findings: { code: string; title: string; severity: string }[] | null
    assetId?: number
    checkId?: number
  }[]
}

const props = defineProps<{ workspace: Workspace; scanId?: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const apiPath = workspacePath(props.workspace.slug) + '/api-security/scans'
const scanPath = home + '/api-security'
const detailPath = props.scanId ? apiPath + '/' + encodeURIComponent(props.scanId) : ''
const canScan = computed(() => canManageChecks(props.workspace.role))
const source = ref('')
const file = ref<File | null>(null)
const scanning = ref(false)
const loading = ref(true)
const scanError = ref('')
const loadError = ref('')
const exportError = ref('')
const exporting = ref(false)
const scans = ref<ScanSummary[]>([])
const detail = ref<ScanDetail | null>(null)
const selectedOperationKey = ref('')
const detailTab = ref<'operations'|'findings'|'fields'>('operations')
const visibleOperations = computed(() => detail.value?.operations.filter(operation => detailTab.value === 'findings' ? operation.findings?.length : detailTab.value === 'fields' ? operation.sensitiveFields?.length : true) ?? [])
const selectedOperation = computed(() => visibleOperations.value.find(operation => operation.method + ' ' + operation.path === selectedOperationKey.value) ?? visibleOperations.value[0])
const active = new AbortController()
const maxFileBytes = 480 * 1024
const authLabels: Record<string, string> = {
  required: '声明鉴权',
  anonymous: '允许匿名',
  unspecified: '未声明鉴权',
  invalid: '鉴权声明无效',
}
const severityLabels: Record<string, string> = {
  critical: '严重', high: '高', medium: '中', low: '低', info: '提示',
}
const locationLabels: Record<string, string> = {
  parameter: '参数', request: '请求体', response: '响应体',
}

function errorText(error: unknown) {
  if (active.signal.aborted) return ''
  if (error instanceof ApiError && error.status === 401) {
    emit('sessionExpired')
    return ''
  }
  return describeError(error)
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const options = { signal: active.signal }
    if (detailPath) detail.value = await request<ScanDetail>(detailPath, options)
    else scans.value = (await request<{ items: ScanSummary[] }>(apiPath, options)).items ?? []
  } catch (error) {
    loadError.value = errorText(error)
  } finally {
    loading.value = false
  }
}

function selectFile(event: Event) {
  file.value = (event.target as HTMLInputElement).files?.[0] ?? null
  scanError.value = file.value && file.value.size > maxFileBytes ? '文件不能超过 480 KiB。' : ''
}

async function scan() {
  if (!canScan.value || scanning.value || !file.value || file.value.size > maxFileBytes) return
  const label = source.value.trim()
  if (!label) {
    scanError.value = '请输入来源名称。'
    return
  }
  scanning.value = true
  scanError.value = ''
  try {
    let document: unknown
    try {
      document = JSON.parse(await file.value.text())
    } catch {
      scanError.value = '无法读取 JSON，请选择 OpenAPI JSON 文件。'
      return
    }
    if (!document || typeof document !== 'object' || Array.isArray(document)) {
      scanError.value = 'OpenAPI 文件的顶层必须是 JSON 对象。'
      return
    }
    const result = await request<ScanDetail>(apiPath, {
      method: 'POST', body: JSON.stringify({ source: label, document }), signal: active.signal,
    })
    if (!active.signal.aborted) window.location.hash = scanPath + '/' + result.id
  } catch (error) {
    scanError.value = errorText(error)
  } finally {
    scanning.value = false
  }
}

async function exportScan() {
  if (!detail.value || exporting.value) return
  exporting.value = true
  exportError.value = ''
  try {
    await downloadFile(detailPath + '/export', 'scarlet-plume-api-scan-' + detail.value.id + '.json', active.signal)
  } catch (error) {
    exportError.value = errorText(error)
  } finally {
    exporting.value = false
  }
}

onMounted(() => { void load() })
onBeforeUnmount(() => active.abort())
</script>

<template>
  <div class="api-workbench" :class="{'api-workbench--list': !scanId }">
    <a v-if="scanId" class="back-link api-back" :href="scanPath"><Icon name="back" />返回 API 安全</a>
    <header class="api-masthead" :class="{ 'api-masthead--detail': scanId }">
      <div class="api-masthead-copy">
        <p v-if="scanId" class="api-scan-index">API 安全 · 扫描 #{{ scanId }}</p>
        <h1>{{ scanId ? (detail?.source ? detail.source + ' · 契约扫描详情' : 'API 契约扫描详情') : 'API 契约安全与扫描' }}</h1>
        <p class="api-scan-context">
          {{ scanId ? '依据接口契约分析，运行时行为待验证。' : '盘点接口、鉴权声明与疑似敏感字段，将契约发现登记到监管检查。' }}
        </p>
      </div>
      <div class="page-actions">
        <a class="button button-secondary" :href="scanPath + '/runtime'"><Icon name="automation" />运行时观测</a>
        <button v-if="detail" type="button" class="button button-primary api-export" :disabled="exporting" @click="exportScan">
          <Icon name="download" />{{ exporting ? '正在导出…' : '导出 JSON' }}
        </button>
      </div>
    </header>
    <p v-if="exportError" class="message message-error page-feedback" role="alert">{{ exportError }}</p>

    <nav class="api-context-tabs" aria-label="API安全视图"><a :href="scanPath" aria-current="page">契约扫描</a><a :href="scanPath + '/runtime'">运行时观测</a><a :href="home + '/api-lifecycle'">接口生命周期</a></nav>
    <dl v-if="!scanId && scans.length" class="api-metrics" aria-label="最近扫描范围与最新契约"><div class="api-metric"><dt>已显示扫描（最多25次）</dt><dd>{{ scans.length }}</dd></div><div class="api-metric"><dt>最新契约 · 接口操作</dt><dd>{{ scans[0]?.operationCount }}</dd></div><div class="api-metric"><dt>最新契约 · 字段候选</dt><dd>{{ scans[0]?.sensitiveFieldCount }}</dd></div><div class="api-metric"><dt>最新契约 · 待核查信号</dt><dd>{{ scans[0]?.findingCount }}</dd></div></dl>
    <div v-if="!scanId" class="api-scan-layout">    <section  class="panel api-panel" aria-labelledby="api-history-title" :aria-busy="loading">
      <div class="api-panel-heading">
        <div><h2 id="api-history-title">最近扫描</h2><p>展示最近 25 次扫描，可查看接口明细与关联检查。</p></div>
        <button type="button" class="button button-secondary" :disabled="loading" @click="load">刷新扫描</button>
      </div>
      <p v-if="loadError" class="message message-error report-feedback" role="alert">{{ loadError }}</p>
      <p v-else-if="loading && !scans.length" class="empty-state" role="status">正在读取扫描记录…</p>
      <div v-else-if="scans.length" class="api-table-frame" role="region" tabindex="0" aria-labelledby="api-history-title">
        <table class="api-table api-history-table" role="table">
          <thead role="rowgroup"><tr role="row"><th scope="col" role="columnheader">来源</th><th scope="col" role="columnheader">接口</th><th scope="col" role="columnheader">疑似敏感字段</th><th scope="col" role="columnheader">契约发现</th><th scope="col" role="columnheader">扫描时间</th></tr></thead>
          <tbody role="rowgroup">
            <tr v-for="entry in scans" :key="entry.id" role="row">
              <th scope="row" role="rowheader">
                <a class="table-link" :href="scanPath + '/' + entry.id">{{ entry.source }}</a>
                <span class="api-secondary">OpenAPI {{ entry.version }}</span>
              </th>
              <td role="cell"><span class="api-cell-label" aria-hidden="true">接口</span>{{ entry.operationCount }}</td>
              <td role="cell"><span class="api-cell-label" aria-hidden="true">疑似敏感字段</span>{{ entry.sensitiveFieldCount }}</td>
              <td role="cell"><span class="api-cell-label" aria-hidden="true">契约发现</span>{{ entry.findingCount }}<span class="api-secondary">{{ entry.checkCount }} 项关联检查</span></td>
              <td role="cell"><span class="api-cell-label" aria-hidden="true">扫描时间</span><time :datetime="entry.createdAt">{{ formatDate(entry.createdAt) }}</time></td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-else class="empty-state"><strong>暂无 API 扫描</strong><p>导入 OpenAPI 文档后，这里会显示扫描结果。</p></div>
    </section>

<aside class="context-rail api-scan-rail">    <section v-if="canScan" class="panel api-panel api-import" aria-labelledby="api-scan-title">
      <div class="api-panel-heading">
        <div><h2 id="api-scan-title">扫描 OpenAPI 文档</h2><p>提交 JSON 后自动分析契约，并将相关发现登记到检查流程。</p></div>
      </div>
      <form class="api-import-form" @submit.prevent="scan">
        <div class="api-import-fields">
          <div class="field">
            <label for="api-scan-source">来源名称</label>
            <input id="api-scan-source" v-model="source" maxlength="80" required :disabled="scanning" placeholder="例如，客户服务 API">
          </div>
          <div class="field">
            <label for="api-scan-file">OpenAPI JSON</label>
            <input id="api-scan-file" type="file" accept=".json,application/json"
                   aria-describedby="api-scan-file-hint" :disabled="scanning" required @change="selectFile">
          </div>
        </div>
        <p id="api-scan-file-hint" class="field-hint">支持 OpenAPI 3.0 和 3.1，文件上限 480 KiB，每份文档最多扫描 200 个接口操作。</p>
        <p v-if="scanError" class="message message-error api-scan-error" role="alert">{{ scanError }}</p>
        <button type="submit" class="button button-primary" :disabled="scanning || !file || file.size > maxFileBytes">
          <Icon name="upload" />{{ scanning ? '扫描中…' : '开始扫描' }}
        </button>
      </form>
    </section>

<section class="panel api-panel"><div class="api-panel-heading"><h2>范围与依据</h2></div><div class="api-basis"><p>采用实际上传的OpenAPI声明及版本。契约发现需要结合运行时观测、来源取证与独立复核。</p><dl><div><dt>输入范围</dt><dd>OpenAPI 3.0 / 3.1 JSON</dd></div><div><dt>当前权限</dt><dd>{{ canScan ? '可提交契约扫描' : '只读查看已有扫描' }}</dd></div><div><dt>行为结论</dt><dd>待运行时验证</dd></div></dl></div></section></aside></div>

    <p v-else-if="loading" class="message" role="status">正在读取 API 扫描结果…</p>
    <p v-else-if="loadError" class="message message-error page-feedback" role="alert">{{ loadError }}</p>
    <template v-else-if="detail">
      <dl class="api-metrics" aria-label="契约扫描结果">
        <div class="api-metric">
          <dt>
            <span class="api-metric-icon api-metric-icon--blue" aria-hidden="true">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m7 7-5 5 5 5m10-10 5 5-5 5M14.5 5l-5 14" /></svg>
            </span>
            接口操作
          </dt>
          <dd>{{ detail.operationCount }}</dd>
        </div>
        <div class="api-metric">
          <dt><span class="api-metric-icon api-metric-icon--teal" aria-hidden="true"><Icon name="assets" /></span>疑似敏感字段</dt>
          <dd>{{ detail.sensitiveFieldCount }}</dd>
        </div>
        <div class="api-metric">
          <dt>
            <span class="api-metric-icon api-metric-icon--coral" aria-hidden="true">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M10.4 4.6 2.8 18a1.7 1.7 0 0 0 1.5 2.5h15.4a1.7 1.7 0 0 0 1.5-2.5L13.6 4.6a1.85 1.85 0 0 0-3.2 0ZM12 9v4.5m0 3h.01" /></svg>
            </span>
            契约风险
          </dt>
          <dd>{{ detail.findingCount }}</dd>
        </div>
        <div class="api-metric">
          <dt><span class="api-metric-icon api-metric-icon--violet" aria-hidden="true"><Icon name="checks" /></span>关联检查</dt>
          <dd>{{ detail.checkCount }}</dd>
        </div>
      </dl>

      <div class="api-detail-layout"><section class="panel api-panel api-operations" aria-labelledby="api-operations-title">
        <div class="api-panel-heading">
          <h2 id="api-operations-title">接口与发现</h2>
          <p class="api-scan-date">OpenAPI {{ detail.version }} · <time :datetime="detail.createdAt">{{ formatDate(detail.createdAt) }}</time></p>
        </div>
        <nav class="api-detail-tabs" aria-label="扫描结果视图"><button v-for="item in ([{id:'operations',label:'接口'},{id:'findings',label:'发现'},{id:'fields',label:'字段'}] as const)" :key="item.id" type="button" :aria-pressed="detailTab === item.id" @click="detailTab = item.id">{{ item.label }}</button></nav><div v-if="visibleOperations.length" class="api-table-frame" role="region" tabindex="0" aria-labelledby="api-operations-title">
          <table class="api-table api-operations-table" role="table">
            <thead role="rowgroup">
              <tr role="row">
                <th scope="col" role="columnheader">接口操作</th><th scope="col" role="columnheader">鉴权声明</th>
                <th scope="col" role="columnheader">疑似敏感字段</th><th scope="col" role="columnheader">契约发现</th>
                <th scope="col" role="columnheader">关联记录</th>
              </tr>
            </thead>
            <tbody role="rowgroup">
              <tr v-for="operation in visibleOperations" :key="operation.method + ' ' + operation.path" role="row" :class="{ 'api-row-selected': selectedOperation === operation }">
                <th scope="row" role="rowheader">
                  <span class="api-endpoint">
                    <span class="api-method mono" :class="{ 'api-method--write': operation.method !== 'GET' && operation.method !== 'HEAD' }">{{ operation.method.toUpperCase() }}</span>
                    <button type="button" class="api-operation-select mono" :aria-pressed="selectedOperation === operation" @click="selectedOperationKey = operation.method + ' ' + operation.path">{{ operation.path }}</button>
                  </span>
                  <span v-if="operation.deprecated" class="api-secondary">已标记弃用</span>
                </th>
                <td role="cell">
                  <span class="api-cell-label" aria-hidden="true">鉴权声明</span>
                  <span class="api-auth" :class="'api-auth--' + operation.auth">{{ authLabels[operation.auth] || operation.auth }}</span>
                </td>
                <td role="cell">
                  <span class="api-cell-label" aria-hidden="true">疑似敏感字段</span>
                  <details v-if="operation.sensitiveFields?.length" class="api-fields">
                    <summary :aria-label="`查看 ${operation.sensitiveFields.length} 个疑似敏感字段详情`">
                      <span v-for="field in operation.sensitiveFields.slice(0, 2)" :key="`${field.location}/${field.name}/${field.category}`" class="api-field-chip mono">{{ field.name }}</span>
                      <span class="api-field-more">
                        <template v-if="operation.sensitiveFields.length > 2">+{{ operation.sensitiveFields.length - 2 }}</template>
                        <Icon name="chevron" />
                      </span>
                    </summary>
                    <ul class="api-field-list">
                      <li v-for="field in operation.sensitiveFields" :key="`${field.location}/${field.name}/${field.category}`">
                        <span class="mono">{{ field.name }}</span>
                        <span class="api-secondary">{{ locationLabels[field.location] || field.location }} · {{ sensitivityLabel(field.category) }}</span>
                      </li>
                    </ul>
                  </details>
                  <span v-else class="api-muted">未识别到</span>
                </td>
                <td role="cell">
                  <span class="api-cell-label" aria-hidden="true">契约发现</span>
                  <ul v-if="operation.findings?.length" class="api-findings">
                    <li v-for="finding in operation.findings" :key="finding.code" class="api-finding" :class="'api-finding--' + finding.severity">
                      <span>
                        <strong class="api-finding-title">{{ finding.title.split('，', 1)[0] }}</strong>
                        <span v-if="finding.title.includes('，')" class="api-finding-detail">{{ finding.title.slice(finding.title.indexOf('，') + 1) }}</span>
                      </span>
                      <span class="api-severity">{{ severityLabels[finding.severity] || finding.severity }}</span>
                    </li>
                  </ul>
                  <span v-else class="api-muted">未检出</span>
                </td>
                <td role="cell">
                  <span class="api-cell-label" aria-hidden="true">关联记录</span>
                  <a v-if="operation.checkId" class="api-check-link table-link" :href="home + '/checks/' + operation.checkId" :aria-label="`查看检查 #${operation.checkId}`">查看检查<Icon name="arrow" /></a>
                  <span v-else class="api-muted">—</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">{{ detail.operations.length ? '当前视图暂无匹配接口。' : '此文档没有可扫描的接口。' }}</p>
        <p class="api-scan-note">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="12" r="9" /><path d="M12 11v5m0-8h.01" /></svg>
          <span>结果基于 OpenAPI 定义。<template v-if="detail.unresolvedReferences"> 有 {{ detail.unresolvedReferences }} 处引用未解析，对应内容未纳入扫描。</template></span>
        </p>
      </section><aside v-if="selectedOperation" class="context-rail"><section class="panel api-panel"><div class="api-panel-heading"><h2>接口采用依据</h2></div><div class="api-basis"><span class="api-method mono">{{ selectedOperation.method }}</span><h3 class="mono">{{ selectedOperation.path }}</h3><span class="badge">契约声明 · 待运行时验证</span><dl><div><dt>鉴权声明</dt><dd>{{ authLabels[selectedOperation.auth] || selectedOperation.auth }}</dd></div><div><dt>采用版本</dt><dd>OpenAPI {{ detail.version }}</dd></div><div><dt>扫描来源</dt><dd>{{ detail.source }}</dd></div><div><dt>采用时点</dt><dd>{{ formatDate(detail.createdAt) }}</dd></div><div><dt>生命周期</dt><dd>{{ selectedOperation.deprecated ? '契约已标记弃用' : '未声明弃用' }}</dd></div></dl><a v-if="selectedOperation.checkId" class="button button-primary" :href="home + '/checks/' + selectedOperation.checkId">查看关联检查</a><p v-else class="muted">当前接口暂无关联检查。</p></div></section><section class="panel api-panel"><div class="api-panel-heading"><h3>字段候选</h3></div><ul v-if="selectedOperation.sensitiveFields?.length" class="api-proof-fields"><li v-for="field in selectedOperation.sensitiveFields" :key="field.location + field.name"><strong class="mono">{{ field.name }}</strong><small>{{ locationLabels[field.location] || field.location }} · {{ sensitivityLabel(field.category) }}</small></li></ul><p v-else class="empty-state">契约范围内未识别到字段候选。</p></section></aside></div>
    </template>
  </div>
</template>
