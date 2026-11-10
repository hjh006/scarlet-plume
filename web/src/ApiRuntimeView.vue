<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import Icon from './Icon.vue'
import RuntimeFindingsPanel from './RuntimeFindingsPanel.vue'
import type { RuntimeFinding } from './runtime-findings'
import './api-security-view.css'

const kinds = { gateway: '独立采集 Agent / 网关日志', otlp: '应用探针（OTLP）' }
type SourceKind = keyof typeof kinds
const postures = {
  documented: '契约已记录',
  deprecated_active: '已弃用 · 仍有流量',
  observed_only: '契约未记录',
  no_baseline: '无匹配契约',
}
type RoutePosture = keyof typeof postures
const findingsWarnings = {
  scope_limited: '该批次达到发现处理上限',
  evaluation_failed: '该批次规则检查未完成',
}

interface RuntimeSource {
  id: number
  name: string
  service: string
  kind: SourceKind
  createdAt: string
  revokedAt?: string
  lastSeen?: string
  findingsWarning?: keyof typeof findingsWarnings
  findingsWarningAt?: string
}

interface RuntimeRoute {
  sourceId: number
  source: string
  service: string
  kind: SourceKind
  method: string
  route: string
  posture: RoutePosture
  requests: number
  responses4xx: number
  responses5xx: number
  lastSeen: string
  databaseSystems: string[] | null
}

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const scanPath = home + '/api-security'
const runtimePath = workspacePath(props.workspace.slug) + '/api-security/runtime'
const owner = computed(() => props.workspace.role === 'owner')
const sources = ref<RuntimeSource[]>([])
const routes = ref<RuntimeRoute[]>([])
const findings = ref<RuntimeFinding[]>([])
const findingsLoaded = ref(false)
const findingsError = ref('')
const loading = ref(false)
const loaded = ref(false)
const loadError = ref('')
const actionError = ref('')
const notice = ref('')
const pending = ref<'create' | 'revoke' | ''>('')
const formVisible = ref(false)
const name = ref('')
const service = ref('')
const kind = ref<SourceKind>('gateway')
const issued = ref<{ source: RuntimeSource; token: string } | null>(null)
const revokeTarget = ref<RuntimeSource | null>(null)
const createButton = ref<HTMLButtonElement>()
const nameInput = ref<HTMLInputElement>()
const tokenHeading = ref<HTMLElement>()
const tokenInput = ref<HTMLInputElement>()
const revokeHeading = ref<HTMLElement>()
const copyNotice = ref('')
let revokeButton: HTMLButtonElement | undefined
const active = new AbortController()
const kindFilter = ref<SourceKind | ''>('')
const postureFilter = ref<RoutePosture | ''>('')
const visibleCount = ref(50)
const enabledSources = computed(() => sources.value.filter(source => !source.revokedAt))
const reportingSources = computed(() => enabledSources.value.filter(source => source.lastSeen).length)
const openFindings = computed(() => findings.value.filter(finding => finding.checkStatus !== 'verified').length)
const filteredRoutes = computed(() => routes.value.filter(route =>
  (!kindFilter.value || route.kind === kindFilter.value) && (!postureFilter.value || route.posture === postureFilter.value),
))

function errorText(error: unknown) {
  if (active.signal.aborted) return ''
  if (error instanceof ApiError && error.status === 401) {
    issued.value = null
    emit('sessionExpired')
    return ''
  }
  return describeError(error)
}

async function load() {
  if (loading.value) return
  loading.value = true
  loadError.value = ''
  findingsError.value = ''
  try {
    const [sourcePage, routePage, findingPage] = await Promise.all([
      request<{ items: RuntimeSource[] }>(runtimePath + '/sources', { signal: active.signal }),
      request<{ items: RuntimeRoute[] }>(runtimePath + '/routes', { signal: active.signal }),
      request<{ items: RuntimeFinding[] }>(runtimePath + '/findings', { signal: active.signal }).catch(error => {
        findingsError.value = errorText(error)
      }),
    ])
    sources.value = sourcePage.items ?? []
    routes.value = routePage.items ?? []
    if (findingPage) {
      findings.value = findingPage.items ?? []
      findingsLoaded.value = true
    }
    loaded.value = true
  } catch (error) {
    loadError.value = errorText(error)
  } finally {
    loading.value = false
  }
}

async function openForm() {
  actionError.value = ''
  notice.value = ''
  formVisible.value = true
  await nextTick()
  nameInput.value?.focus()
}

async function dismissForm() {
  formVisible.value = false
  actionError.value = ''
  name.value = ''
  service.value = ''
  await nextTick()
  createButton.value?.focus()
}

async function createSource() {
  if (!owner.value || loading.value || pending.value) return
  const values = { name: name.value.trim(), service: service.value.trim(), kind: kind.value }
  actionError.value = ''
  if (!values.name || !values.service) {
    actionError.value = '请填写来源名称和服务别名。'
    return
  }
  pending.value = 'create'
  try {
    const result = await request<{ source: RuntimeSource; token: string }>(runtimePath + '/sources', {
      method: 'POST', body: JSON.stringify(values), signal: active.signal,
    })
    if (active.signal.aborted) return
    sources.value = [result.source, ...sources.value]
    issued.value = result
    formVisible.value = false
    name.value = ''
    service.value = ''
    await nextTick()
    tokenHeading.value?.focus()
  } catch (error) {
    actionError.value = errorText(error)
  } finally {
    pending.value = ''
  }
}

async function copyToken() {
  const token = issued.value?.token
  if (!token) return
  try {
    await navigator.clipboard.writeText(token)
    copyNotice.value = '已复制接入令牌。'
  } catch {
    tokenInput.value?.focus()
    tokenInput.value?.select()
    copyNotice.value = '已选中令牌，可使用系统快捷键复制。'
  }
}

async function dismissToken() {
  issued.value = null
  copyNotice.value = ''
  notice.value = '来源已创建，连接后可在列表查看最近上报时间。'
  await nextTick()
  createButton.value?.focus()
}

async function askRevoke(source: RuntimeSource, event: MouseEvent) {
  revokeButton = event.currentTarget as HTMLButtonElement
  revokeTarget.value = source
  actionError.value = ''
  notice.value = ''
  await nextTick()
  revokeHeading.value?.focus()
}

async function dismissRevoke() {
  revokeTarget.value = null
  actionError.value = ''
  await nextTick()
  revokeButton?.focus()
  revokeButton = undefined
}

async function revoke() {
  const source = revokeTarget.value
  if (!source || !owner.value || loading.value || pending.value) return
  pending.value = 'revoke'
  actionError.value = ''
  try {
    await request(runtimePath + '/sources/' + source.id, { method: 'DELETE', signal: active.signal })
    if (active.signal.aborted) return
    revokeTarget.value = null
    revokeButton = undefined
    notice.value = '接入令牌已撤销，历史观测仍可查看。'
    await load()
    await nextTick()
    createButton.value?.focus()
  } catch (error) {
    actionError.value = errorText(error)
  } finally {
    pending.value = ''
  }
}

onMounted(() => { void load() })
onBeforeUnmount(() => {
  issued.value = null
  active.abort()
})
</script>

<template>
  <div class="api-workbench api-runtime">
    <a class="back-link api-back" :href="scanPath"><Icon name="back" />返回契约扫描</a>
    <header class="api-masthead api-masthead--detail">
      <div class="api-masthead-copy">
        <p class="api-scan-index">API 安全</p>
        <h1>运行时观测</h1>
        <p class="api-scan-context">通过独立采集 Agent 观测服务入口，也可接入既有网关日志和 OTLP 应用追踪。按服务别名与最新 OpenAPI 契约对照。</p>
      </div>
      <div class="page-actions api-runtime-actions">
        <button type="button" class="button button-secondary" :disabled="loading || Boolean(pending)" @click="load">刷新观测</button>
        <button v-if="owner" ref="createButton" type="button" class="button button-primary"
                :aria-expanded="formVisible" aria-controls="runtime-source-form"
                :disabled="!loaded || formVisible || Boolean(issued) || Boolean(revokeTarget) || Boolean(pending)" @click="openForm">
          <Icon name="plus" />新增观测来源
        </button>
      </div>
    </header>
    <p v-if="notice" class="message message-success" role="status">{{ notice }}</p>
    <p v-if="loadError" class="message message-error" role="alert">{{ loadError }}</p>
    <p v-else-if="loading && !loaded" class="message" role="status">正在读取运行时观测…</p>

    <section v-if="formVisible" id="runtime-source-form" class="panel api-panel" aria-labelledby="runtime-source-form-title">
      <div class="api-panel-heading"><div><h2 id="runtime-source-form-title">新增观测来源</h2><p>为单个网关或应用签发专用接入令牌。</p></div></div>
      <form @submit.prevent="createSource">
        <fieldset class="runtime-source-fields" :disabled="Boolean(pending)">
          <div class="field">
            <label for="runtime-source-kind">接入类型</label>
            <select id="runtime-source-kind" v-model="kind">
              <option v-for="(label, value) in kinds" :key="value" :value="value">{{ label }}</option>
            </select>
          </div>
          <div class="field">
            <label for="runtime-source-name">来源名称</label>
            <input id="runtime-source-name" ref="nameInput" v-model="name" maxlength="80" placeholder="例如，生产入口网关" required>
          </div>
          <div class="field">
            <label for="runtime-source-service">服务别名</label>
            <input id="runtime-source-service" v-model="service" maxlength="80" aria-describedby="runtime-service-hint" required>
          </div>
        </fieldset>
        <p id="runtime-service-hint" class="field-hint">与契约扫描的来源名称一致时，平台按该来源的最新契约对照。</p>
        <p v-if="actionError" class="message message-error runtime-action-error" role="alert">{{ actionError }}</p>
        <div class="form-actions">
          <button type="submit" class="button button-primary" :disabled="loading || Boolean(pending)">{{ pending ? '正在创建…' : '创建并签发令牌' }}</button>
          <button type="button" class="button button-secondary" :disabled="Boolean(pending)" @click="dismissForm">取消</button>
        </div>
      </form>
    </section>

    <section v-if="issued" class="panel api-panel" aria-labelledby="runtime-issued-title">
      <div class="api-panel-heading">
        <div><h2 id="runtime-issued-title" ref="tokenHeading" tabindex="-1">接入令牌已签发</h2><p>{{ issued.source.name }} · {{ kinds[issued.source.kind] }}</p></div>
      </div>
      <div class="field">
        <label for="runtime-issued-token">接入令牌</label>
        <input id="runtime-issued-token" ref="tokenInput" class="mono" :value="issued.token" aria-describedby="runtime-token-hint"
               autocomplete="off" autocapitalize="none" spellcheck="false" readonly>
        <p id="runtime-token-hint" class="field-hint">令牌仅展示一次。复制后存入接入端的凭据配置。</p>
      </div>
      <div class="form-actions">
        <button type="button" class="button button-primary" @click="copyToken">复制令牌</button>
        <button type="button" class="button button-secondary" @click="dismissToken">完成并隐藏令牌</button>
      </div>
      <p v-if="copyNotice" class="field-hint" role="status">{{ copyNotice }}</p>
    </section>

    <section v-if="revokeTarget" class="panel api-panel" aria-labelledby="runtime-revoke-title">
      <div class="api-panel-heading">
        <div>
          <h2 id="runtime-revoke-title" ref="revokeHeading" tabindex="-1">撤销观测来源</h2>
          <p>撤销「{{ revokeTarget.name }}」的接入令牌后，该来源将停止上报。历史路由记录可继续查看。</p>
        </div>
      </div>
      <p v-if="actionError" class="message message-error runtime-action-error" role="alert">{{ actionError }}</p>
      <div class="form-actions">
        <button type="button" class="button button-primary" :disabled="loading || Boolean(pending)" @click="revoke">{{ pending ? '正在撤销…' : '确认撤销' }}</button>
        <button type="button" class="button button-secondary" :disabled="Boolean(pending)" @click="dismissRevoke">取消</button>
      </div>
    </section>

    <template v-if="loaded">
      <dl class="api-metrics" aria-label="运行时观测汇总">
        <div class="api-metric"><dt><span class="api-metric-icon api-metric-icon--blue"><Icon name="integrations" /></span>启用来源</dt><dd>{{ enabledSources.length }}</dd></div>
        <div class="api-metric"><dt><span class="api-metric-icon api-metric-icon--teal"><Icon name="automation" /></span>已上报来源</dt><dd>{{ reportingSources }}</dd></div>
        <div class="api-metric"><dt><span class="api-metric-icon api-metric-icon--violet"><Icon name="api-security" /></span>观测路由</dt><dd>{{ routes.length }}</dd></div>
        <div class="api-metric"><dt><span class="api-metric-icon api-metric-icon--coral"><Icon name="checks" /></span>未闭环发现</dt><dd>{{ findingsLoaded ? openFindings : '—' }}</dd></div>
      </dl>

      <RuntimeFindingsPanel :findings="findings" :home="home" :loading="loading" :error="findingsError" />

      <section class="panel api-panel" aria-labelledby="runtime-routes-title" :aria-busy="loading">
        <div class="api-panel-heading">
          <div><h2 id="runtime-routes-title">观测路由</h2><p>展示最近的 {{ routes.length }} 项，按来源和路由分别统计。同一次请求可能由网关和应用分别观测。</p></div>
        </div>
        <div v-if="routes.length" class="runtime-route-filters">
          <div class="field">
            <label for="runtime-filter-kind">来源类型</label>
            <select id="runtime-filter-kind" v-model="kindFilter" @change="visibleCount = 50">
              <option value="">全部类型</option>
              <option v-for="(label, value) in kinds" :key="value" :value="value">{{ label }}</option>
            </select>
          </div>
          <div class="field">
            <label for="runtime-filter-posture">契约对照</label>
            <select id="runtime-filter-posture" v-model="postureFilter" @change="visibleCount = 50">
              <option value="">全部结果</option>
              <option v-for="(label, value) in postures" :key="value" :value="value">{{ label }}</option>
            </select>
          </div>
        </div>
        <details class="runtime-help">
          <summary>如何理解契约对照结果</summary>
          <p>平台按服务别名匹配最新 OpenAPI 契约。已记录表示契约含该方法和路由模板。未记录表示已匹配的契约缺少对应接口。这里统计各类 HTTP 响应，运行时发现以 HTTP 2xx 成功响应为触发条件。没有匹配的契约时标为「无匹配契约」。</p>
        </details>
        <div v-if="filteredRoutes.length" class="api-table-frame" role="region" tabindex="0" aria-labelledby="runtime-routes-title">
          <table class="api-table runtime-routes-table" role="table">
            <thead role="rowgroup"><tr role="row">
              <th scope="col" role="columnheader">路由模板</th><th scope="col" role="columnheader">观测来源</th>
              <th scope="col" role="columnheader">契约对照</th><th scope="col" role="columnheader">观测请求</th>
              <th scope="col" role="columnheader">数据库系统</th><th scope="col" role="columnheader">最近观测</th>
            </tr></thead>
            <tbody role="rowgroup">
              <tr v-for="route in filteredRoutes.slice(0, visibleCount)" :key="JSON.stringify([route.sourceId, route.method, route.route])" role="row">
                <th scope="row" role="rowheader">
                  <span class="api-endpoint">
                    <span class="api-method mono" :class="{ 'api-method--write': route.method !== 'GET' && route.method !== 'HEAD' }">{{ route.method }}</span>
                    <code class="api-path mono">{{ route.route }}</code>
                  </span>
                </th>
                <td role="cell"><span class="api-cell-label" aria-hidden="true">观测来源</span>{{ route.source }}<span class="api-secondary">{{ kinds[route.kind] }} · {{ route.service }}</span></td>
                <td role="cell"><span class="api-cell-label" aria-hidden="true">契约对照</span><span class="badge" :data-posture="route.posture">{{ postures[route.posture] }}</span></td>
                <td role="cell"><span class="api-cell-label" aria-hidden="true">观测请求</span>{{ route.requests }}<span class="api-secondary">4xx {{ route.responses4xx }} · 5xx {{ route.responses5xx }}</span></td>
                <td role="cell"><span class="api-cell-label" aria-hidden="true">数据库系统</span>{{ route.databaseSystems?.join('、') || '—' }}</td>
                <td role="cell"><span class="api-cell-label" aria-hidden="true">最近观测</span><time :datetime="route.lastSeen">{{ formatDate(route.lastSeen) }}</time></td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">{{ routes.length ? '暂无符合筛选条件的路由。' : '暂无运行时观测。接入来源上报后，路由会在此显示。' }}</p>
        <button v-if="filteredRoutes.length > visibleCount" type="button" class="button button-secondary runtime-more"
                @click="visibleCount += 50">显示更多路由</button>
      </section>

      <section class="panel api-panel" aria-labelledby="runtime-sources-title" :aria-busy="loading">
        <div class="api-panel-heading">
          <div><h2 id="runtime-sources-title">观测来源</h2><p>根据最近上报时间追踪接入状态。</p></div>
        </div>
        <div v-if="sources.length" class="api-table-frame" role="region" tabindex="0" aria-labelledby="runtime-sources-title">
          <table class="api-table runtime-sources-table" role="table">
            <thead role="rowgroup"><tr role="row">
              <th scope="col" role="columnheader">来源</th><th scope="col" role="columnheader">服务别名</th>
              <th scope="col" role="columnheader">状态</th><th scope="col" role="columnheader">最近上报</th>
              <th v-if="owner" scope="col" role="columnheader">操作</th>
            </tr></thead>
            <tbody role="rowgroup">
              <tr v-for="source in sources" :key="source.id" role="row">
                <th scope="row" role="rowheader">{{ source.name }}<span class="api-secondary">{{ kinds[source.kind] }} · #{{ source.id }}</span></th>
                <td role="cell"><span class="api-cell-label" aria-hidden="true">服务别名</span>{{ source.service }}</td>
                <td role="cell">
                  <span class="api-cell-label" aria-hidden="true">状态</span>
                  <span class="badge" :class="!source.revokedAt && source.lastSeen ? 'status-verified' : 'status-draft'">
                    {{ source.revokedAt ? '已撤销' : source.lastSeen ? '已收到观测' : '等待上报' }}
                  </span>
                </td>
                <td role="cell">
                  <span class="api-cell-label" aria-hidden="true">最近上报</span>
                  <time v-if="source.lastSeen" :datetime="source.lastSeen">{{ formatDate(source.lastSeen) }}</time>
                  <span v-else>暂无</span>
                  <span v-if="source.findingsWarning" class="api-secondary runtime-source-warning">
                    {{ findingsWarnings[source.findingsWarning] }}
                    <time v-if="source.findingsWarningAt" :datetime="source.findingsWarningAt">{{ formatDate(source.findingsWarningAt) }}</time>
                  </span>
                </td>
                <td v-if="owner" role="cell">
                  <button v-if="!source.revokedAt" type="button" class="button button-secondary" :aria-label="`撤销 ${source.name}`"
                          :disabled="loading || formVisible || Boolean(issued) || Boolean(revokeTarget) || Boolean(pending)" @click="askRevoke(source, $event)">撤销</button>
                  <span v-else class="api-muted">—</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">暂无观测来源。{{ owner ? '新增来源后即可配置接入。' : '由工作区负责人配置接入。' }}</p>
        <details class="runtime-help runtime-setup">
          <summary>接入方式</summary>
          <p><strong>独立采集 Agent</strong> · 在服务入口部署 Go 反向代理 <code>runtime-agent</code>，配置固定上游与受信任路由模板。令牌通过 <code>SCARLET_RUNTIME_GATEWAY_TOKEN</code> 注入接入端。</p>
          <p>HTTPS 入口可在获授权后配置 <code>-tls-cert</code> 与 <code>-tls-key</code> 终止 TLS，再观测 HTTP 路由和状态码。HTTPS 透传时，这些信息保持加密。</p>
          <p><strong>既有网关日志（可选）</strong> · 将日志转换为约定的 JSON 格式，向 <code>POST /api/v1/runtime/gateway</code> 上报。通过 <code>Authorization: Bearer TOKEN</code> 认证，仅发送方法、路由模板、状态码和观测时间。</p>
          <p><strong>应用探针（OTLP）</strong> · 需要应用内部的路由和关联数据库系统时，按语言配置 OpenTelemetry SDK 或自动探针，例如 Java Agent、Go、Python、Node.js、.NET。</p>
          <p>使用 <code>http/protobuf</code> 上报追踪，将 <code>OTEL_EXPORTER_OTLP_TRACES_ENDPOINT</code> 设为赤翎服务上 <code>/api/v1/runtime/otlp/v1/traces</code> 的完整接入地址，并在 <code>OTEL_EXPORTER_OTLP_TRACES_HEADERS</code> 中用 <code>Authorization=Bearer%20TOKEN</code> 的格式配置令牌。日志和指标可停用或继续上报到现有观测系统。</p>
          <p>平台仅保留路由统计与数据库系统类型。将令牌存入接入端的凭据配置。部署示例见项目文档 <code>docs/runtime-security.md</code>。</p>
        </details>
      </section>
    </template>
  </div>
</template>

<style scoped src="./api-runtime-view.css"></style>
