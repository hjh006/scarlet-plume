<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import {
  automationProviders, automationRunClass, automationRunDetail, automationRunStatuses,
  type AutomationJob, type AutomationProvider, type AutomationRun, type IssuedAutomationJob,
} from './automation'
import Icon from './Icon.vue'
import ManagedPostgresForm from './ManagedPostgresForm.vue'

const props = defineProps<{ workspace: Workspace; checkContext?: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const automationPath = workspacePath(props.workspace.slug) + '/automation'
const jobsPath = automationPath + '/jobs'
const managedPath = automationPath + '/managed'
const owner = computed(() => props.workspace.role === 'owner')
const pageOrigin = window.location.origin
const lifecycle = new AbortController()
const jobs = ref<AutomationJob[]>([])
const loaded = ref(false)
const loading = ref(false)
const loadError = ref('')
const notice = ref('')
const formError = ref('')
const showForm = ref(Boolean(props.checkContext))
const managedAvailable = ref(false)
const managedHosts = ref<string[]>([])
const managedDraft = ref<{ job?: AutomationJob } | null>(null)
const queueing = ref<number | null>(null)
const runError = ref('')
const awaitingRun = ref<{ jobId: number; previousRunId?: number; startedAt: number } | null>(null)
const provider = ref<AutomationProvider>(props.checkContext ? 'feishu' : 'postgresql')
const source = ref('')
const checkId = ref(props.checkContext || '')
const saving = ref(false)
const issued = ref<IssuedAutomationJob | null>(null)
const tokenHeading = ref<HTMLElement | null>(null)
const tokenInput = ref<HTMLInputElement | null>(null)
const providerInput = ref<HTMLSelectElement | null>(null)
const copyNotice = ref('')
const historyHeading = ref<HTMLElement | null>(null)
const selectedJob = ref<AutomationJob | null>(null)
const runs = ref<AutomationRun[]>([])
const focusedJob = computed(() => selectedJob.value ?? jobs.value[0])
const selectedRunId = ref<number>()
const focusedRun = computed(() => runs.value.find(run => run.id === selectedRunId.value) ?? runs.value[0])
const showIssuedToken = ref(false)
const historyLoading = ref(false)
const historyError = ref('')
const revokeTarget = ref<AutomationJob | null>(null)
const revokeHeading = ref<HTMLElement | null>(null)
const revoking = ref(false)
const revokeError = ref('')
let refreshTimer: number
let runRefreshTimer: number | undefined
let listRequest: AbortController | undefined
let historyRequest: AbortController | undefined

const setupOptions: Record<AutomationProvider, string> = {
  postgresql: "-source '任务中的来源名称' -schema '已授权的 schema'",
  openmetadata: "-source '任务中的来源名称' -service '目录服务名称'",
  feishu: '',
}
const sourceSettings: Record<AutomationProvider, string> = {
  postgresql: 'SCARLET_PG_DSN',
  openmetadata: 'SCARLET_OPENMETADATA_URL、SCARLET_OPENMETADATA_TOKEN',
  feishu: 'SCARLET_FEISHU_APP_ID、SCARLET_FEISHU_APP_SECRET、SCARLET_FEISHU_DOC_TOKEN、SCARLET_FEISHU_MEMBER_OPEN_ID',
}
const setupCommand = computed(() => {
  if (!issued.value) return ''
  const connector = issued.value.job.provider
  const loopback = /^http:\/\/(127\.0\.0\.1|\[::1\])(?::[0-9]+)?$/.test(pageOrigin)
  return [
    'go run ./cmd/agent', '-provider ' + connector, '-interval 15m', setupOptions[connector],
    loopback ? '-allow-insecure-loopback' : '',
  ].filter(Boolean).join(' ')
})

function errorText(error: unknown) {
  if (lifecycle.signal.aborted) return ''
  if (error instanceof ApiError && error.status === 401) {
    emit('sessionExpired')
    return ''
  }
  return describeError(error)
}

function jobLabel(job: AutomationJob) {
  return job.source || `检查 #${job.checkId}`
}

function jobState(job: AutomationJob) {
  if (job.revokedAt) return '已撤销'
  if (job.managed?.running) return '采集中'
  if (job.managed?.queued) return '已排队'
  if (job.managed && !job.managed.enabled) return '已暂停'
  return '已启用'
}

async function loadManagedAvailability() {
  try {
    const result = await request<{ enabled: boolean; allowedHosts: string[] }>(managedPath, { signal: lifecycle.signal })
    managedAvailable.value = result.enabled
    managedHosts.value = result.allowedHosts ?? []
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) emit('sessionExpired')
  }
}

async function loadJobs(background = false) {
  if (background && loading.value) return
  listRequest?.abort()
  const pending = new AbortController()
  listRequest = pending
  loading.value = true
  if (!background) loadError.value = ''
  try {
    const result = await request<{ items: AutomationJob[] }>(jobsPath, { signal: pending.signal })
    if (pending.signal.aborted) return
    jobs.value = result.items
    loaded.value = true
    loadError.value = ''
  } catch (error) {
    if (!pending.signal.aborted) loadError.value = errorText(error)
  } finally {
    if (!pending.signal.aborted) loading.value = false
  }
}

function openManagedForm(job?: AutomationJob) {
  showForm.value = false
  managedDraft.value = { job }
  notice.value = ''
  runError.value = ''
}

async function managedSaved(job: AutomationJob) {
  managedDraft.value = null
  notice.value = job.managed?.enabled
    ? '任务已保存，定期采集已启用。'
    : '任务已保存，定期采集已暂停。仍可立即采集。'
  await loadJobs()
}

function trackRun() {
  window.clearTimeout(runRefreshTimer)
  runRefreshTimer = window.setTimeout(async () => {
    const active = awaitingRun.value
    if (!active || lifecycle.signal.aborted) return
    await loadJobs(true)
    if (awaitingRun.value !== active || lifecycle.signal.aborted) return
    const job = jobs.value.find(item => item.id === active.jobId)
    if (job?.latestRun && job.latestRun.id !== active.previousRunId && !job.managed?.queued && !job.managed?.running) {
      awaitingRun.value = null
      const detail = automationRunDetail(job.latestRun, true)
      if (job.latestRun.status === 'error') {
        notice.value = ''
        runError.value = detail
      } else {
        notice.value = '本次采集已完成。' + detail
      }
      if (selectedJob.value?.id === job.id) await loadRuns(job)
      return
    }
    if (Date.now() - active.startedAt < 45_000) trackRun()
    else {
      awaitingRun.value = null
      notice.value = '采集任务仍在处理，可刷新任务查看进度。'
    }
  }, 1_000)
}

async function runNow(job: AutomationJob) {
  if (queueing.value !== null || !job.managed) return
  queueing.value = job.id
  runError.value = ''
  notice.value = ''
  try {
    await request(`${managedPath}/jobs/${job.id}/run`, { method: 'POST', signal: lifecycle.signal })
    if (lifecycle.signal.aborted) return
    awaitingRun.value = { jobId: job.id, previousRunId: job.latestRun?.id, startedAt: Date.now() }
    notice.value = '采集任务已排队，运行结果将自动刷新。'
    trackRun()
  } catch (error) {
    runError.value = errorText(error)
  } finally {
    queueing.value = null
  }
}

async function openForm() {
  formError.value = ''
  managedDraft.value = null
  showForm.value = true
  await nextTick()
  providerInput.value?.focus()
}

async function createJob() {
  if (!owner.value || saving.value || issued.value) return
  const body = provider.value === 'feishu'
    ? { provider: provider.value, checkId: Number(checkId.value) }
    : { provider: provider.value, source: source.value.trim() }
  if (('checkId' in body && (!Number.isSafeInteger(body.checkId) || Number(body.checkId) < 1)) ||
      ('source' in body && !body.source)) {
    formError.value = '请填写有效的来源名称或检查编号。'
    return
  }

  formError.value = ''
  notice.value = ''
  saving.value = true
  try {
    const result = await request<IssuedAutomationJob>(jobsPath, {
      method: 'POST', body: JSON.stringify(body), signal: lifecycle.signal,
    })
    if (lifecycle.signal.aborted) return
    showIssuedToken.value = false
    issued.value = result
    copyNotice.value = ''
    showForm.value = false
    source.value = ''
    await nextTick()
    tokenHeading.value?.focus()
    await loadJobs()
  } catch (error) {
    formError.value = errorText(error)
  } finally {
    saving.value = false
  }
}

async function copyToken() {
  if (!issued.value) return
  try {
    await navigator.clipboard.writeText(issued.value.token)
    copyNotice.value = '已复制接入令牌。'
  } catch {
    tokenInput.value?.focus()
    tokenInput.value?.select()
    copyNotice.value = '已选中接入令牌，可使用系统快捷键复制。'
  }
}

function dismissToken() {
  showIssuedToken.value = false
  issued.value = null
  copyNotice.value = ''
  notice.value = '采集任务已创建。连接采集端后，可在列表查看运行结果。'
}

async function loadRuns(job: AutomationJob, focus = false) {
  historyRequest?.abort()
  const pending = new AbortController()
  historyRequest = pending
  if (selectedJob.value?.id !== job.id) runs.value = []
  selectedJob.value = job
  historyLoading.value = true
  historyError.value = ''
  if (focus) {
    await nextTick()
    historyHeading.value?.focus()
  }
  try {
    const result = await request<{ items: AutomationRun[] }>(`${jobsPath}/${job.id}/runs`, { signal: pending.signal })
    if (!pending.signal.aborted) runs.value = result.items
  } catch (error) {
    if (!pending.signal.aborted) historyError.value = errorText(error)
  } finally {
    if (!pending.signal.aborted) historyLoading.value = false
  }
}

function closeHistory() {
  historyRequest?.abort()
  selectedJob.value = null
  runs.value = []
}

async function askRevoke(job: AutomationJob) {
  revokeTarget.value = job
  revokeError.value = ''
  await nextTick()
  revokeHeading.value?.focus()
}

async function revokeJob() {
  const job = revokeTarget.value
  if (!job || !owner.value || revoking.value) return
  revoking.value = true
  revokeError.value = ''
  try {
    await request(`${jobsPath}/${job.id}`, { method: 'DELETE', signal: lifecycle.signal })
    if (lifecycle.signal.aborted) return
    if (issued.value?.job.id === job.id) issued.value = null
    revokeTarget.value = null
    if (awaitingRun.value?.jobId === job.id) awaitingRun.value = null
    notice.value = '采集任务已撤销，运行记录仍可查看。'
    await loadJobs()
  } catch (error) {
    revokeError.value = errorText(error)
  } finally {
    revoking.value = false
  }
}

onMounted(() => {
  void loadJobs()
  void loadManagedAvailability()
  refreshTimer = window.setInterval(() => {
    if (document.visibilityState !== 'visible' || saving.value || revoking.value) return
    void loadJobs(true)
  }, 20_000)
})

onBeforeUnmount(() => {
  lifecycle.abort()
  listRequest?.abort()
  historyRequest?.abort()
  window.clearInterval(refreshTimer)
  window.clearTimeout(runRefreshTimer)
  issued.value = null
})
</script>

<template>
  <a v-if="checkContext" class="back-link" :href="home + '/checks/' + encodeURIComponent(checkContext)">
    <Icon name="back" />返回检查 #{{ checkContext }}
  </a>

  <header class="page-header">
    <div>
      <p class="eyebrow">治理工作区 / 连接器</p>
      <h1>自动采集</h1>
      <p>定期读取已授权的数据来源，自动登记资产目录或检查证据，并保留每次运行结果。</p>
    </div>
    <div v-if="owner && !showForm && !issued && !managedDraft" class="automation-actions">
      <button v-if="managedAvailable && !checkContext" type="button" class="button button-primary" @click="openManagedForm()">
        <Icon name="plus" />新建 PostgreSQL 采集
      </button>
      <button type="button" class="button" :class="managedAvailable && !checkContext ? 'button-secondary' : 'button-primary'" @click="openForm">
        <Icon v-if="!managedAvailable || checkContext" name="plus" />{{ managedAvailable && !checkContext ? '接入外部采集器' : '新建采集任务' }}
      </button>
    </div>
  </header>

  <p v-if="notice" class="message message-success page-feedback" role="status">{{ notice }}</p>
  <p v-if="runError" class="message message-error page-feedback" role="alert">{{ runError }}</p>

  <ManagedPostgresForm v-if="owner && managedDraft" :key="managedDraft.job?.id ?? 'new'"
    :managed-path="managedPath" :allowed-hosts="managedHosts" :job="managedDraft.job"
    @cancel="managedDraft = null" @saved="managedSaved" @session-expired="emit('sessionExpired')" />

  <section v-if="owner && showForm" class="panel form-panel automation-form" aria-labelledby="automation-form-heading">
    <h2 id="automation-form-heading">新建采集任务</h2>
    <form @submit.prevent="createJob">
      <div class="field">
        <label for="automation-provider">采集来源</label>
        <select id="automation-provider" ref="providerInput" v-model="provider" :disabled="saving || Boolean(checkContext)">
          <option v-for="(label, key) in automationProviders" :key="key" :value="key">{{ label }}</option>
        </select>
      </div>
      <div v-if="provider === 'feishu'" class="field">
        <label for="automation-check">关联检查编号</label>
        <input id="automation-check" v-model="checkId" inputmode="numeric" pattern="[1-9][0-9]*" required
               :readonly="Boolean(checkContext)" aria-describedby="automation-check-hint" :disabled="saving">
        <p id="automation-check-hint" class="field-hint">绑定已配置飞书核验目标的检查，采集指定协作者的直接授权。</p>
      </div>
      <div v-else class="field">
        <label for="automation-source">来源名称</label>
        <input id="automation-source" v-model="source" maxlength="80" required
               aria-describedby="automation-source-hint" :disabled="saving">
        <p id="automation-source-hint" class="field-hint">用业务库或目录名称区分来源。采集端使用同一个名称，资产台账将据此追溯来源。</p>
      </div>
      <p class="field-hint">采集频率和源系统凭据在采集端配置。</p>
      <p v-if="formError" class="message message-error page-feedback" role="alert">{{ formError }}</p>
      <div class="form-actions">
        <button type="submit" class="button button-primary" :disabled="saving">{{ saving ? '正在创建…' : '创建并生成令牌' }}</button>
        <button type="button" class="button button-secondary" :disabled="saving" @click="showForm = false">取消</button>
      </div>
    </form>
  </section>

  <section v-if="issued" class="panel automation-form automation-token" aria-labelledby="automation-token-heading">
    <div class="panel-heading">
      <div>
        <h2 id="automation-token-heading" ref="tokenHeading" tabindex="-1">采集任务已创建</h2>
        <p>{{ automationProviders[issued.job.provider] }} · {{ jobLabel(issued.job) }}</p>
      </div>
    </div>
    <div class="automation-panel-body">
      <div class="field">
        <label for="automation-issued-token">接入令牌（仅显示一次）</label>
        <input id="automation-issued-token" ref="tokenInput" :type="showIssuedToken ? 'text' : 'password'" :value="issued.token" class="mono" readonly
               autocomplete="off" spellcheck="false" aria-describedby="automation-token-hint">
        <p id="automation-token-hint" class="field-hint">将令牌保存到采集端。关闭此面板后需要新建任务才能生成新令牌。</p>
      </div>
      <dl class="automation-connect">
        <div><dt>采集端平台地址</dt><dd><code>SCARLET_AGENT_URL={{ pageOrigin }}</code></dd></div>
        <div><dt>采集端令牌变量</dt><dd><code>SCARLET_AGENT_TOKEN</code></dd></div>
        <div v-if="issued.job.source"><dt>采集端来源名称</dt><dd>{{ issued.job.source }}</dd></div>
      </dl>
      <details class="automation-setup">
        <summary>采集端运行方式</summary>
        <p>在可访问来源系统的环境中配置平台地址、接入令牌和 <code>{{ sourceSettings[issued.job.provider] }}</code>，再从项目的 <code>backend</code> 目录运行。</p>
        <pre>{{ setupCommand }}</pre>
      </details>
      <div class="form-actions">
        <button type="button" class="button button-primary" @click="copyToken">复制令牌</button><button type="button" class="button button-secondary" @click="showIssuedToken = !showIssuedToken">{{ showIssuedToken ? '隐藏令牌' : '临时显示' }}</button>
        <button type="button" class="button button-secondary" @click="dismissToken">完成</button>
      </div>
      <p v-if="copyNotice" class="field-hint" role="status">{{ copyNotice }}</p>
    </div>
  </section>

  <section v-if="revokeTarget" class="panel automation-form" aria-labelledby="automation-revoke-heading">
    <div class="panel-heading"><h2 id="automation-revoke-heading" ref="revokeHeading" tabindex="-1">撤销采集任务</h2></div>
    <div class="automation-panel-body">
      <p>撤销「{{ jobLabel(revokeTarget) }}」后，{{ revokeTarget.managed ? '定期采集将停止。' : '采集端的接入令牌立即失效。' }}</p>
      <p v-if="revokeError" class="message message-error page-feedback" role="alert">{{ revokeError }}</p>
      <div class="form-actions">
        <button type="button" class="button button-primary" :disabled="revoking" @click="revokeJob">{{ revoking ? '正在撤销…' : '确认撤销' }}</button>
        <button type="button" class="button button-secondary" :disabled="revoking" @click="revokeTarget = null; revokeError = ''">取消</button>
      </div>
    </div>
  </section>

  <div class="automation-workbench"><section class="panel" aria-labelledby="automation-jobs-heading" :aria-busy="loading">
    <div class="panel-heading">
      <div><h2 id="automation-jobs-heading">采集任务</h2><p>查看任务授权和最近一次运行。</p></div>
      <div class="automation-actions">
        <a class="button button-secondary" :href="home + '/assets'">查看资产台账</a>
        <a class="button button-secondary" :href="home + '/reports/archives'">自动报告归档</a>
        <button type="button" class="button button-secondary" :disabled="loading" @click="loadJobs()">刷新任务</button>
      </div>
    </div>
    <p v-if="loadError" class="message message-error automation-feedback" role="alert">{{ loadError }}</p>
    <p v-if="!loaded && loading" class="empty-state" role="status">正在读取采集任务…</p>
    <div v-else-if="loaded && jobs.length" class="table-scroll" role="region" aria-labelledby="automation-jobs-heading" tabindex="0">
      <table class="data-table automation-table">
        <thead><tr><th scope="col">来源</th><th scope="col">接入状态</th><th scope="col">最近运行</th><th scope="col">操作</th></tr></thead>
        <tbody>
          <tr v-for="job in jobs" :key="job.id" :class="{selected:focusedJob?.id === job.id}">
            <th scope="row">
              <a v-if="job.checkId" class="table-link" :href="home + '/checks/' + job.checkId">{{ jobLabel(job) }}</a>
              <button v-else class="automation-record-link" @click="loadRuns(job, true)">{{ jobLabel(job) }}</button>
              <span class="table-secondary">{{ automationProviders[job.provider] }} · 任务 #{{ job.id }}</span>
              <template v-if="job.managed">
                <span class="table-secondary">平台采集 · 每 {{ job.managed.intervalMinutes }} 分钟</span>
                <span class="table-secondary">字段识别 · 自动执行</span>
                <span class="table-secondary">自动检查 · {{ job.managed.autoChecks ? '开启' : '关闭' }}</span>
                <span class="table-secondary">报告归档 · {{ job.managed.autoReports ? '开启' : '关闭' }}</span>
              </template>
            </th>
            <td>
              <span class="badge" :class="job.revokedAt || job.managed?.enabled === false ? 'status-draft' : 'status-verified'">{{ jobState(job) }}</span>
              <span v-if="!job.revokedAt && job.managed?.enabled && job.managed.nextRunAt && !job.managed.queued && !job.managed.running" class="table-secondary">
                下次 <time :datetime="job.managed.nextRunAt">{{ formatDate(job.managed.nextRunAt) }}</time>
              </span>
            </td>
            <td v-if="job.latestRun">
              <span class="badge" :class="automationRunClass(job.latestRun.status)">{{ automationRunStatuses[job.latestRun.status] }}</span>
              <span class="table-secondary">{{ automationRunDetail(job.latestRun, Boolean(job.managed)) }}</span>
              <time class="table-secondary" :datetime="job.latestRun.observedAt">{{ formatDate(job.latestRun.observedAt) }}</time>
            </td>
            <td v-else class="table-secondary">等待首次运行</td>
            <td>
              <div class="automation-actions">
                <button type="button" class="button button-secondary" @click="loadRuns(job, true)">运行记录</button>
                <template v-if="owner && !job.revokedAt">
                  <template v-if="job.managed && managedAvailable">
                    <button type="button" class="button button-secondary" :disabled="Boolean(issued) || Boolean(managedDraft)" @click="openManagedForm(job)">任务配置</button>
                    <button type="button" class="button button-secondary"
                            :disabled="queueing !== null || Boolean(awaitingRun) || Boolean(managedDraft) || job.managed.running || job.managed.queued"
                            @click="runNow(job)">{{ queueing === job.id ? '正在排队…' : '立即采集' }}</button>
                  </template>
                  <button type="button" class="button button-secondary" :disabled="revoking || Boolean(managedDraft)" @click="askRevoke(job)">撤销</button>
                </template>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-else-if="loaded" class="empty-state">
      <strong>暂无采集任务</strong>
      <p v-if="owner && managedAvailable">新建 PostgreSQL 任务后可测试连接、设置定期采集，或立即采集一次。</p>
      <p v-else>{{ owner ? '新建任务后，在连接源系统的采集端配置接入令牌和运行频率。' : '工作区负责人创建任务后，可在这里查看采集状态。' }}</p>
    </div>
  </section><aside class="automation-context-rail"><section v-if="focusedJob" class="panel"><p class="eyebrow">当前采集任务</p><h2>{{ jobLabel(focusedJob) }}</h2><span class="badge">{{ jobState(focusedJob) }}</span><dl><div><dt>任务来源</dt><dd>{{ automationProviders[focusedJob.provider] }}</dd></div><div><dt>任务编号</dt><dd>#{{ focusedJob.id }}</dd></div><template v-if="focusedJob.managed"><div><dt>采集频率</dt><dd>每 {{ focusedJob.managed.intervalMinutes }} 分钟</dd></div><div><dt>自动检查</dt><dd>{{ focusedJob.managed.autoChecks ? '已开启' : '未开启' }}</dd></div><div><dt>报告归档</dt><dd>{{ focusedJob.managed.autoReports ? '已开启' : '未开启' }}</dd></div></template><div><dt>最近运行</dt><dd>{{ focusedJob.latestRun ? automationRunStatuses[focusedJob.latestRun.status] : '等待首次运行' }}</dd></div></dl><button class="button button-secondary" @click="loadRuns(focusedJob, true)">查看运行依据</button></section><section class="panel"><h2>自动化处理链</h2><ol><li>按授权范围采集来源事实</li><li>识别目录、字段与权限</li><li>将可验证结果关联检查</li><li>保留复测与报告快照</li></ol><p>来源状态和运行成功不能代替检查的独立复核结论。</p></section></aside></div>

  <div v-if="selectedJob" class="automation-workbench"><section class="panel automation-history" aria-labelledby="automation-history-heading" :aria-busy="historyLoading">
    <div class="panel-heading">
      <div>
        <h2 id="automation-history-heading" ref="historyHeading" tabindex="-1">运行记录</h2>
        <p>{{ automationProviders[selectedJob.provider] }} · {{ jobLabel(selectedJob) }}</p>
      </div>
      <div class="automation-actions">
        <button type="button" class="button button-secondary" :disabled="historyLoading" @click="loadRuns(selectedJob)">刷新记录</button>
        <button type="button" class="button button-secondary" @click="closeHistory">关闭记录</button>
      </div>
    </div>
    <p v-if="historyError" class="message message-error automation-feedback" role="alert">{{ historyError }}</p>
    <p v-if="historyLoading && !runs.length" class="empty-state" role="status">正在读取运行记录…</p>
    <div v-else-if="runs.length" class="table-scroll" role="region" aria-labelledby="automation-history-heading" tabindex="0">
      <table class="data-table automation-table">
        <thead><tr><th scope="col">批次</th><th scope="col">采集结果</th><th scope="col">观察时间</th><th scope="col">登记时间</th></tr></thead>
        <tbody>
          <tr v-for="run in runs" :key="run.id" :class="{selected: focusedRun?.id === run.id}">
            <th scope="row" class="mono"><button class="automation-record-link" @click="selectedRunId = run.id">{{ run.runId }}</button></th>
            <td>
              <span class="badge" :class="automationRunClass(run.status)">{{ automationRunStatuses[run.status] }}</span>
              <span class="table-secondary">{{ automationRunDetail(run, Boolean(selectedJob.managed)) }}</span>
            </td>
            <td><time :datetime="run.observedAt">{{ formatDate(run.observedAt) }}</time></td>
            <td><time :datetime="run.createdAt">{{ formatDate(run.createdAt) }}</time></td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-else-if="!historyError" class="empty-state">{{ selectedJob.managed ? '等待任务首次运行。' : '等待采集端首次运行。' }}</p>
  </section><aside class="automation-context-rail"><section v-if="focusedRun" class="panel"><p class="eyebrow">运行记录</p><h2 class="mono">{{ focusedRun.runId }}</h2><span class="badge" :class="automationRunClass(focusedRun.status)">{{ automationRunStatuses[focusedRun.status] }}</span><dl><div><dt>运行编号</dt><dd>#{{ focusedRun.id }}</dd></div><div><dt>观察时间</dt><dd>{{ formatDate(focusedRun.observedAt) }}</dd></div><div><dt>登记时间</dt><dd>{{ formatDate(focusedRun.createdAt) }}</dd></div></dl><p>{{ automationRunDetail(focusedRun, Boolean(selectedJob.managed)) }}</p><a class="button button-secondary" :href="home + '/reports/archives'">查看报告归档</a></section></aside></div>
</template>

<style scoped>.automation-workbench{display:grid;grid-template-columns:minmax(0,1fr) 300px;gap:16px;align-items:start;margin-bottom:18px}.automation-context-rail{display:grid;gap:16px}.automation-context-rail>.panel{padding:20px}.automation-context-rail h2{font-size:14px;line-height:1.8;margin:0 0 14px;overflow-wrap:anywhere}.automation-context-rail p,.automation-context-rail li{font-size:12px;line-height:1.9;color:var(--muted)}.automation-context-rail ol{padding-left:20px}.automation-context-rail dl{display:grid;gap:15px;margin:20px 0}.automation-context-rail dl>div{display:grid;grid-template-columns:74px minmax(0,1fr);gap:8px;font-size:12px}.automation-context-rail dt{color:var(--muted)}.automation-context-rail dd{margin:0;overflow-wrap:anywhere}.automation-record-link{border:0;background:none;color:var(--blue);padding:0;font:inherit;cursor:pointer;text-align:left;overflow-wrap:anywhere}.automation-table{min-width:650px}.automation-table .automation-actions{gap:6px}.automation-table .automation-actions .button{font-size:12px;min-height:32px;padding:6px 9px}.automation-table tr.selected{background:var(--surface-subtle)}.automation-history{margin-top:0}.automation-table td,.automation-table th{vertical-align:top}.automation-form{max-width:none}.automation-form>form,.automation-panel-body{padding:22px}.automation-token{border-color:var(--input-line);margin-bottom:18px}.automation-connect{display:grid;grid-template-columns:1fr 1fr;gap:16px}.automation-connect dd{overflow-wrap:anywhere}.automation-setup pre{padding:14px;border:1px solid var(--line);border-radius:8px;background:var(--surface-subtle);white-space:pre-wrap;font-size:12px}@media(max-width:1400px){.automation-workbench{grid-template-columns:minmax(0,1fr) 270px;gap:12px}}</style>
