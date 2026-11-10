<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { activityKindLabels, type Activity, type ActivityPage } from './activity'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { checkStatusLabels, eventLabels, type Check, type CheckEvent, type Evidence } from './workflow'
import { postgresPublicSelect } from './postgresql'
import Icon from './Icon.vue'

interface ComplianceReport {
  generatedAt: string
  scope: { activityId?: number; activityName?: string }
  counts: {
    draft: number; inReview: number; needsWork: number; verified: number; total: number
    templated: number; evidence: number; remediations: number; retests: number; reviews: number
  }
  checks: { check: Check; evidence: Evidence[]; events: CheckEvent[] }[]
}

const props = defineProps<{ workspace: Workspace; activityContext?: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const apiUrl = workspacePath(props.workspace.slug)
const reportUrl = apiUrl + '/reports/compliance'
const activityId = ref(props.activityContext ?? '')
const activities = ref<Activity[]>([])
const activityCursor = ref<number | null>(null)
const activityLoading = ref(false)
const activityError = ref('')
const report = ref<ComplianceReport | null>(null)
const loading = ref(true)
const errorMessage = ref('')
const exportError = ref('')
const exporting = ref<'html' | 'json' | ''>('')
const active = new AbortController()
const counts = [
  { key: 'total', label: '检查总数' },
  { key: 'draft', label: checkStatusLabels.draft },
  { key: 'inReview', label: checkStatusLabels.in_review },
  { key: 'needsWork', label: checkStatusLabels.needs_work },
  { key: 'verified', label: checkStatusLabels.verified },
] as const

const reportQuery = computed(() => activityId.value ? '?activityId=' + encodeURIComponent(activityId.value) : '')
const activityMissing = computed(() => activityId.value && !activities.value.some(item => String(item.id) === activityId.value))
const reportChecks = computed(() => report.value?.checks.map(entry => ({
  ...entry, postgresEvidence: entry.evidence.find(evidence => evidence.postgres)?.postgres,
})) ?? [])

function errorText(error: unknown) {
  if (active.signal.aborted) return ''
  if (error instanceof ApiError && error.status === 401) {
    emit('sessionExpired')
    return ''
  }
  return describeError(error)
}

async function loadActivities(append = false) {
  if (activityLoading.value) return
  activityLoading.value = true
  activityError.value = ''
  try {
    const query = append && activityCursor.value !== null ? '?before=' + activityCursor.value : ''
    const page = await request<ActivityPage>(apiUrl + '/activities' + query, { signal: active.signal })
    if (active.signal.aborted) return
    activities.value = append ? [...activities.value, ...page.items] : page.items
    activityCursor.value = page.nextCursor ?? null
  } catch (error) {
    activityError.value = errorText(error)
  } finally {
    activityLoading.value = false
  }
}

async function loadReport() {
  loading.value = true
  errorMessage.value = ''
  exportError.value = ''
  report.value = null
  try {
    report.value = await request<ComplianceReport>(reportUrl + reportQuery.value, { signal: active.signal })
  } catch (error) {
    errorMessage.value = errorText(error)
  } finally {
    loading.value = false
  }
}

function setScope() {
  window.location.hash = home + '/reports' + (activityId.value ? '/' + encodeURIComponent(activityId.value) : '')
}

function latestDecision(events: CheckEvent[]) {
  return events.find(event => event.action === 'verified' || event.action === 'changes')
}

async function download(format: 'html' | 'json') {
  if (exporting.value || !report.value || loading.value) return
  exporting.value = format
  exportError.value = ''
  try {
    const params = new URLSearchParams({ format })
    if (activityId.value) params.set('activityId', activityId.value)
    const response = await fetch(reportUrl + '/export?' + params, { signal: active.signal, credentials: 'same-origin' })
    if (!response.ok) {
      const body = await response.json().catch(() => null)
      throw new ApiError(response.status, typeof body?.error === 'string' ? body.error.slice(0, 300) : '')
    }
    const blob = await response.blob()
    if (active.signal.aborted) return
    const fileUrl = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = fileUrl
    const scope = activityId.value ? '-activity-' + activityId.value : ''
    link.download = 'scarlet-plume-compliance' + scope + '.' + format
    document.body.append(link)
    link.click()
    link.remove()
    window.setTimeout(() => URL.revokeObjectURL(fileUrl), 1000)
  } catch (error) {
    exportError.value = errorText(error)
  } finally {
    exporting.value = ''
  }
}

onMounted(() => { void loadActivities(); void loadReport() })
onBeforeUnmount(() => active.abort())
</script>

<template>
  <header class="page-header">
    <div>
      <h1>合规报告</h1>
      <p>按工作区或处理活动汇总检查、证据、整改复测与复核结果。</p>
    </div>
    <div class="page-actions">
      <button type="button" class="button button-primary" :disabled="!report || loading || Boolean(exporting)" @click="download('html')">
        <Icon name="download" />{{ exporting === 'html' ? '正在导出…' : '导出 HTML' }}
      </button>
      <button type="button" class="button button-secondary" :disabled="!report || loading || Boolean(exporting)" @click="download('json')">
        {{ exporting === 'json' ? '正在导出…' : '导出 JSON' }}
      </button>
    </div>
  </header>
  <p v-if="exportError" class="message message-error page-feedback" role="alert">{{ exportError }}</p>
  <section class="panel report-scope" aria-labelledby="report-scope-title" :aria-busy="loading">
    <div class="panel-heading">
      <div><h2 id="report-scope-title">报告范围</h2><p>仅汇总已创建的检查。未选用的模板项未评估，已验证表示该检查通过了独立复核。</p></div>
    </div>
    <div class="filter-bar">
      <label for="report-activity">处理活动</label>
      <select id="report-activity" v-model="activityId" :disabled="loading || Boolean(exporting)" @change="setScope">
        <option value="">整个工作区</option>
        <option v-if="activityMissing" :value="activityId">{{ report?.scope.activityName || '活动 #' + activityId }}</option>
        <option v-for="activity in activities" :key="activity.id" :value="String(activity.id)">
          {{ activity.name }} · {{ activityKindLabels[activity.kind] }}
        </option>
      </select>
      <button v-if="activityCursor !== null" type="button" class="button button-secondary"
              :disabled="activityLoading || Boolean(exporting)" @click="loadActivities(true)">加载更多活动</button>
      <button type="button" class="button button-secondary" :disabled="loading || Boolean(exporting)" @click="loadReport">
        刷新预览
      </button>
    </div>
    <div v-if="activityError" class="empty-state">
      <p role="alert">{{ activityError }}</p>
      <button type="button" class="button button-secondary" @click="loadActivities()">重新读取活动</button>
    </div>
    <p v-if="errorMessage" class="message message-error report-feedback" role="alert">{{ errorMessage }}</p>
    <p v-else-if="loading" class="empty-state" role="status">正在汇总检查结果…</p>
    <template v-else-if="report">
      <dl class="report-counts">
        <div v-for="entry in counts" :key="entry.key"><dt>{{ entry.label }}</dt><dd>{{ report.counts[entry.key] }}</dd></div>
      </dl>
      <p class="report-timestamp">
        {{ report.counts.templated }} 项关联模板 · {{ report.counts.evidence }} 条证据 · {{ report.counts.remediations }} 条整改 ·
        {{ report.counts.retests }} 条复测 · {{ report.counts.reviews }} 次复核
      </p>
      <p class="report-timestamp">
        预览生成于 <time :datetime="report.generatedAt">{{ formatDate(report.generatedAt) }}</time>。导出时重新汇总，可用浏览器打印 HTML。
      </p>
    </template>
  </section>
  <section v-if="report" class="panel report-results" aria-labelledby="report-results-title">
    <div class="panel-heading">
      <div><h2 id="report-results-title">检查明细</h2><p>导出包含全部证据记录和操作时间线。</p></div>
    </div>
    <div v-if="report.checks.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="report-results-title">
      <table class="data-table check-table report-table">
        <thead>
          <tr><th scope="col">检查与资产</th><th scope="col">状态</th><th scope="col">证据数</th><th scope="col">最近复核</th></tr>
        </thead>
        <tbody>
          <tr v-for="entry in reportChecks" :key="entry.check.id">
            <th scope="row">
              <a class="table-link" :href="home + '/checks/' + entry.check.id">{{ entry.check.template?.item.title || entry.check.requirement.title }}</a>
              <span class="table-secondary">{{ entry.check.requirement.code }} · {{ entry.check.assetName }}</span>
              <span v-if="entry.check.template" class="table-secondary">{{ entry.check.template.title }} · 版本 {{ entry.check.template.version }}</span>
            </th>
            <td><span class="badge status" :class="'status-' + entry.check.status">{{ checkStatusLabels[entry.check.status] }}</span></td>
            <td>
              {{ entry.evidence.length }}
              <template v-if="entry.postgresEvidence">
                <span class="table-secondary">PostgreSQL 自动采集</span>
                <span class="table-secondary">{{ entry.postgresEvidence.columnCount }} 个字段 · {{ entry.postgresEvidence.sensitiveCandidateCount }} 个疑似敏感</span>
                <span class="table-secondary">{{ postgresPublicSelect(entry.postgresEvidence.publicSelect, entry.postgresEvidence.publicSelectColumnCount) }}</span>
              </template>
            </td>
            <td>
              <template v-if="latestDecision(entry.events)">
                {{ eventLabels[latestDecision(entry.events)!.action] }}
                <span class="table-secondary">{{ latestDecision(entry.events)!.actor }}</span>
              </template>
              <template v-else>暂无复核结果</template>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-else class="empty-state"><strong>此范围暂无检查</strong><p>新建检查后，报告将纳入该记录和后续操作。</p></div>
  </section>
</template>
