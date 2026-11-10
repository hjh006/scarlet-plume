<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, downloadFile, formatDate, request, workspacePath, type Workspace } from './api'
import { postgresSensitivityLabel as sensitivityLabel } from './postgresql'
import { canManageChecks } from './workflow'
import Icon from './Icon.vue'

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
  <a v-if="scanId" class="back-link" :href="scanPath"><Icon name="back" />返回 API 安全</a>
  <header class="page-header">
    <div>
      <p v-if="scanId" class="eyebrow">API 安全 · 扫描 #{{ scanId }}</p>
      <h1>{{ scanId ? detail?.source || 'API 契约扫描' : 'API 安全' }}</h1>
      <p>扫描 OpenAPI 契约中的接口、鉴权声明和疑似敏感字段。结论基于文档，运行时行为需另行验证。</p>
    </div>
    <div class="page-actions">
      <button v-if="detail" type="button" class="button button-primary" :disabled="exporting" @click="exportScan">
        <Icon name="download" />{{ exporting ? '正在导出…' : '导出 JSON' }}
      </button>
    </div>
  </header>
  <p v-if="exportError" class="message message-error page-feedback" role="alert">{{ exportError }}</p>

  <section v-if="!scanId && canScan" class="panel automation-form" aria-labelledby="api-scan-title">
    <div class="panel-heading">
      <div><h2 id="api-scan-title">扫描 OpenAPI 文档</h2><p>提交 JSON 后自动分析契约，并将相关发现登记到检查流程。</p></div>
    </div>
    <div class="import-panel-body">
      <form @submit.prevent="scan">
        <div class="field">
          <label for="api-scan-source">来源名称</label>
          <input id="api-scan-source" v-model="source" maxlength="80" required :disabled="scanning" placeholder="例如，客户服务 API">
        </div>
        <div class="field">
          <label for="api-scan-file">OpenAPI JSON</label>
          <input id="api-scan-file" type="file" accept=".json,application/json"
                 aria-describedby="api-scan-file-hint" :disabled="scanning" required @change="selectFile">
          <p id="api-scan-file-hint" class="field-hint">支持 OpenAPI 3.0 和 3.1，文件上限 480 KiB，每份文档最多扫描 200 个接口操作。</p>
        </div>
        <p v-if="scanError" class="message message-error page-feedback" role="alert">{{ scanError }}</p>
        <button type="submit" class="button button-primary" :disabled="scanning || !file || file.size > maxFileBytes">
          <Icon name="upload" />{{ scanning ? '扫描中…' : '开始扫描' }}
        </button>
      </form>
    </div>
  </section>

  <section v-if="!scanId" class="panel" aria-labelledby="api-history-title" :aria-busy="loading">
    <div class="panel-heading">
      <div><h2 id="api-history-title">最近扫描</h2><p>展示最近 25 次扫描，可查看接口明细与关联检查。</p></div>
      <button type="button" class="button button-secondary" :disabled="loading" @click="load">刷新扫描</button>
    </div>
    <p v-if="loadError" class="message message-error report-feedback" role="alert">{{ loadError }}</p>
    <p v-else-if="loading && !scans.length" class="empty-state" role="status">正在读取扫描记录…</p>
    <div v-else-if="scans.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="api-history-title">
      <table class="data-table report-table">
        <thead><tr><th scope="col">来源</th><th scope="col">接口</th><th scope="col">疑似敏感字段</th><th scope="col">契约发现</th><th scope="col">扫描时间</th></tr></thead>
        <tbody>
          <tr v-for="entry in scans" :key="entry.id">
            <th scope="row">
              <a class="table-link" :href="scanPath + '/' + entry.id">{{ entry.source }}</a>
              <span class="table-secondary">OpenAPI {{ entry.version }}</span>
            </th>
            <td>{{ entry.operationCount }}</td>
            <td>{{ entry.sensitiveFieldCount }}</td>
            <td>{{ entry.findingCount }}<span class="table-secondary">{{ entry.checkCount }} 项关联检查</span></td>
            <td><time :datetime="entry.createdAt">{{ formatDate(entry.createdAt) }}</time></td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-else class="empty-state"><strong>暂无 API 扫描</strong><p>导入 OpenAPI 文档后，这里会显示扫描结果。</p></div>
  </section>

  <p v-else-if="loading" class="message" role="status">正在读取 API 扫描结果…</p>
  <p v-else-if="loadError" class="message message-error page-feedback" role="alert">{{ loadError }}</p>
  <template v-else-if="detail">
    <section class="panel" aria-labelledby="api-results-title">
      <div class="panel-heading">
        <div>
          <h2 id="api-results-title">契约扫描结果</h2>
          <p>OpenAPI {{ detail.version }} · <time :datetime="detail.createdAt">{{ formatDate(detail.createdAt) }}</time></p>
        </div>
      </div>
      <dl class="report-counts">
        <div><dt>接口数</dt><dd>{{ detail.operationCount }}</dd></div>
        <div><dt>疑似敏感字段</dt><dd>{{ detail.sensitiveFieldCount }}</dd></div>
        <div><dt>契约发现</dt><dd>{{ detail.findingCount }}</dd></div>
        <div><dt>关联检查</dt><dd>{{ detail.checkCount }}</dd></div>
      </dl>
      <p v-if="detail.unresolvedReferences" class="report-timestamp">有 {{ detail.unresolvedReferences }} 处引用未解析，对应内容未纳入扫描。</p>
    </section>
    <section class="panel report-results" aria-labelledby="api-operations-title">
      <div class="panel-heading"><div><h2 id="api-operations-title">接口明细</h2></div></div>
      <div v-if="detail.operations.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="api-operations-title">
        <table class="data-table check-table report-table">
          <thead><tr><th scope="col">接口</th><th scope="col">鉴权声明</th><th scope="col">疑似敏感字段</th><th scope="col">契约发现</th><th scope="col">检查</th></tr></thead>
          <tbody>
            <tr v-for="operation in detail.operations" :key="operation.method + ' ' + operation.path">
              <th scope="row"><span class="mono">{{ operation.method.toUpperCase() }} {{ operation.path }}</span><span v-if="operation.deprecated" class="table-secondary">已标记弃用</span></th>
              <td>{{ authLabels[operation.auth] || operation.auth }}</td>
              <td>
                <details v-if="operation.sensitiveFields?.length" class="asset-metadata">
                  <summary>{{ operation.sensitiveFields.length }} 个字段</summary>
                  <ul class="api-operation-list">
                    <li v-for="(field, index) in operation.sensitiveFields" :key="index">
                      <span class="mono">{{ field.name }}</span> · {{ locationLabels[field.location] || field.location }} · {{ sensitivityLabel(field.category) }}
                    </li>
                  </ul>
                </details>
                <span v-else>未识别到</span>
              </td>
              <td>
                <ul v-if="operation.findings?.length" class="api-operation-list">
                  <li v-for="finding in operation.findings" :key="finding.code">
                    <strong>{{ severityLabels[finding.severity] || finding.severity }}</strong> · {{ finding.title }}
                  </li>
                </ul>
                <span v-else>未检出</span>
              </td>
              <td><a v-if="operation.checkId" class="table-link" :href="home + '/checks/' + operation.checkId">检查 #{{ operation.checkId }}</a><span v-else>—</span></td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="empty-state">此文档没有可扫描的接口。</p>
    </section>
  </template>
</template>
