<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { activityKindLabels, type Activity, type ActivityPage } from './activity'
import { ApiError, describeError, downloadFile, formatDate, request, workspacePath, type Workspace } from './api'
import { checkStatusLabels, eventLabels, type Check, type CheckEvent, type Evidence } from './workflow'
import { postgresAssessmentLabels, postgresAssessmentReasons, postgresPublicSelect, type PostgresEvidence } from './postgresql'
import type { ReportCounts, ReportDataFlows, ReportIdentification, ReportSource, ReportCatalogDiscovery, ReportOperations, ReportAssessmentReferences } from './reports'
import Icon from './Icon.vue'
import CatalogAssessmentEvidence from './CatalogAssessmentEvidence.vue'
import { assessmentKinds, assessmentStatuses } from './assessments'
import RuntimeDetectionFacts from './RuntimeDetectionFacts.vue'
import { controlKindLabels, controlStatusLabels, controlResultLabel, controlEventLabels, securityEventCategoryLabels, securityEventOutcomeLabels, operationsSeverityLabels } from './security-operations'
import { runtimeRuleLabels, type RuntimeFinding } from './runtime-findings'
import ClassificationEvaluation from './ClassificationEvaluation.vue'
import type { ClassificationRecord } from './classification-standards'
import { dataFlowWarning } from './data-flows'

interface ComplianceReport {
  generatedAt: string
  scope: { activityId?: number; activityName?: string; source?: ReportSource }
  counts: ReportCounts
  identification?: ReportIdentification
  dataFlows?: ReportDataFlows
  catalogDiscovery?: ReportCatalogDiscovery
  operations?: ReportOperations
  assessmentReferences?: ReportAssessmentReferences
  checks: { check: Check; evidence: Evidence[]; events: CheckEvent[]; automaticEvaluation?: PostgresEvidence; classification?: ClassificationRecord; runtimeFinding?: RuntimeFinding }[]
}

const props = defineProps<{ workspace: Workspace; activityContext?: string; archiveId?: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const apiUrl = workspacePath(props.workspace.slug)
const reportUrl = props.archiveId
  ? apiUrl + '/reports/archives/' + encodeURIComponent(props.archiveId)
  : apiUrl + '/reports/compliance'
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
const catalogLimit=ref(20)
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
  evaluation: props.archiveId ? entry.automaticEvaluation : entry.evidence.find(evidence => evidence.postgres?.assessment)?.postgres,
  automaticSnapshot: entry.automaticEvaluation?.classification ?? entry.classification?.automatic ??
    entry.evidence.find(evidence => evidence.postgres?.classification)?.postgres?.classification,
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
    const scope = props.archiveId ? '-archive-' + props.archiveId : activityId.value ? '-activity-' + activityId.value : ''
    await downloadFile(reportUrl + '/export?' + params, 'scarlet-plume-compliance' + scope + '.' + format, active.signal)
  } catch (error) {
    exportError.value = errorText(error)
  } finally {
    exporting.value = ''
  }
}

onMounted(() => {
  if (!props.archiveId) void loadActivities()
  void loadReport()
})
onBeforeUnmount(() => active.abort())
</script>

<template>
  <a v-if="archiveId" class="back-link" :href="home + '/reports/archives'"><Icon name="back" />返回自动报告归档</a>
  <header class="page-header">
    <div>
      <h1>{{ archiveId ? '自动归档报告' : '合规报告' }}</h1>
      <p>{{ archiveId ? '查看采集时保存的数据识别、自动检查和工作流记录。' : '按工作区或处理活动汇总检查、证据、整改复测与复核结果。' }}</p>
    </div>
    <div class="page-actions">
      <a v-if="!archiveId" class="button button-secondary" :href="home + '/reports/archives'">自动报告归档</a>
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
      <div>
        <h2 id="report-scope-title">报告范围</h2>
        <p>{{ archiveId ? '汇总此来源在本次采集范围内关联的检查。已验证表示该检查已通过独立复核。' : '仅汇总已创建的检查。未选用的模板项未评估，已验证表示该检查通过了独立复核。' }}</p>
      </div>
    </div>
    <div v-if="!archiveId" class="filter-bar">
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
      <dl v-if="report.scope.source" class="detail-list">
        <div><dt>采集来源</dt><dd>{{ report.scope.source.source }} · 任务 #{{ report.scope.source.jobId }}</dd></div>
        <div><dt>采集批次</dt><dd class="mono">{{ report.scope.source.runId }}</dd></div>
        <div><dt>观察时间</dt><dd><time :datetime="report.scope.source.observedAt">{{ formatDate(report.scope.source.observedAt) }}</time></dd></div>
      </dl>
      <dl class="report-counts">
        <div v-for="entry in counts" :key="entry.key"><dt>{{ entry.label }}</dt><dd>{{ report.counts[entry.key] }}</dd></div>
      </dl>
      <p class="report-timestamp">
        {{ report.counts.templated }} 项关联模板 · {{ report.counts.evidence }} 条证据 · {{ report.counts.remediations }} 条整改 ·
        {{ report.counts.retests }} 条复测 · {{ report.counts.reviews }} 次复核
      </p>
      <p class="report-timestamp">
        {{ archiveId ? '自动归档于' : '预览生成于' }} <time :datetime="report.generatedAt">{{ formatDate(report.generatedAt) }}</time>。
        {{ archiveId ? '导出使用本次归档的数据，可用浏览器打印 HTML。' : '导出时重新汇总，可用浏览器打印 HTML。' }}
      </p>
    </template>
  </section>
  <section v-if="report?.identification" class="panel report-results" aria-labelledby="report-identification-title">
    <div class="panel-heading">
      <div><h2 id="report-identification-title">数据自动识别</h2><p>汇总元数据及授权范围内的受限格式统计，具体方法与覆盖范围见证据。</p></div>
    </div>
    <dl class="report-counts">
      <div><dt>采集表数</dt><dd>{{ report.identification.tables }}</dd></div>
      <div><dt>字段总数</dt><dd>{{ report.identification.columns }}</dd></div>
      <div><dt>疑似敏感字段</dt><dd>{{ report.identification.sensitiveCandidates }}</dd></div>
      <div><dt>含疑似敏感字段的表</dt><dd>{{ report.identification.tablesWithCandidates }}</dd></div>
      <div><dt>PUBLIC SELECT 授权表数</dt><dd>{{ report.identification.publicTableSelect }}</dd></div>
      <div><dt>PUBLIC SELECT 授权字段数</dt><dd>{{ report.identification.publicColumnSelect }}</dd></div>
    </dl>
  </section>

  <section v-if="report?.assessmentReferences" class="panel report-results" aria-labelledby="report-assessment-refs-title"><div class="panel-heading"><div><h2 id="report-assessment-refs-title">关联活动的专项评估引用</h2><p>{{report.assessmentReferences.basis}}</p></div></div><p v-if="report.assessmentReferences.truncated">最多展示200项引用，请在专项评估中核对完整范围。</p><ul><li v-for="assessment in report.assessmentReferences.items" :key="assessment.id"><a :href="home+'/assessments/'+assessment.id">#{{assessment.id}} · {{assessment.title}}</a> · {{assessmentKinds[assessment.kind]||assessment.kind}} · {{assessmentStatuses[assessment.status]||assessment.status}} · 模板#{{assessment.templateRevisionId}} · 修订v{{assessment.revision}} · {{formatDate(assessment.updatedAt)}}<span v-if="assessment.stale||assessment.reassessmentPending"> · 已标记需要重评</span></li></ul></section>

  <section v-if="report?.operations" class="panel report-results" aria-labelledby="report-operations-title">
    <div class="panel-heading"><div><h2 id="report-operations-title">关联安全运营与处置回执</h2><p>{{report.operations.scope}}</p></div><span>{{formatDate(report.operations.capturedAt)}}</span></div>
    <article v-for="action in report.operations.controls" :key="action.id"><h3>处置 #{{action.id}} · {{controlKindLabels[action.kind]}} · {{controlStatusLabels[action.status]||action.status}}</h3><p>检查 #{{action.checkId}} · 修订 v{{action.revision}} · 申请人 {{action.actor}} · 审批人 {{action.approvedBy||'尚未审批'}} · 到期 {{formatDate(action.expiresAt)}}</p><p>{{action.spec.service}} {{action.spec.method}} {{action.spec.route}}<span v-if="action.spec.database">{{action.spec.database}} / {{action.spec.schema}} / {{action.spec.table}} / {{action.spec.role}}</span></p><p>{{action.reason}}</p><p v-if="action.result">执行回执：{{controlResultLabel(action.result.code)}} · {{action.result.success?'来源报告成功':'来源报告失败'}} · {{formatDate(action.result.observedAt)}}</p><ul v-if="action.events?.length"><li v-for="(event,index) in action.events" :key="index">{{controlEventLabels[event.action]||event.action}} · {{event.actor}} · {{formatDate(event.createdAt)}} · {{event.note}}</li></ul></article>
    <div v-if="report.operations.securityEvents.length" class="table-scroll"><table class="data-table"><thead><tr><th>关联检查 / 来源</th><th>类别 / 规则</th><th>来源结果</th><th>来源窗口统计</th></tr></thead><tbody><tr v-for="event in report.operations.securityEvents" :key="event.id"><td>检查 #{{event.checkId}} · {{event.source}}</td><td>{{securityEventCategoryLabels[event.category]||event.category}} · {{event.rule}} · {{operationsSeverityLabels[event.severity]||event.severity}}</td><td>{{securityEventOutcomeLabels[event.outcome]||event.outcome}} · {{formatDate(event.observedAt)}}</td><td>{{event.count}} 次 / {{event.windowSeconds}} 秒（来源统计口径）</td></tr></tbody></table></div>
  </section>

  <section v-if="reportChecks.some(entry=>entry.runtimeFinding?.detection)" class="panel report-results" aria-labelledby="report-runtime-facts-title">
    <div class="panel-heading"><div><h2 id="report-runtime-facts-title">运行时规则与实际观测依据</h2><p>策略版本、来源内统计窗口与可观察分母随本报告冻结；模式命中需结合独立核验。</p></div></div>
    <template v-for="entry in reportChecks" :key="'runtime-'+entry.check.id"><article v-if="entry.runtimeFinding?.detection"><h3>检查 #{{entry.check.id}} · {{runtimeRuleLabels[entry.runtimeFinding.rule]}}</h3><p>{{entry.runtimeFinding.service}} · {{entry.runtimeFinding.method}} {{entry.runtimeFinding.route}}</p><RuntimeDetectionFacts :detection="entry.runtimeFinding.detection"/></article></template>
  </section>

  <section v-if="report?.catalogDiscovery" class="panel report-results" aria-labelledby="report-catalog-title">
    <div class="panel-heading"><div><h2 id="report-catalog-title">多源目录与分类证据</h2><p>{{report.catalogDiscovery.basis}}</p></div></div>
    <dl class="report-metrics"><div><dt>目录表 / 集合</dt><dd>{{report.catalogDiscovery.tables}}</dd></div><div><dt>字段 / 路径</dt><dd>{{report.catalogDiscovery.fields}}</dd></div><div><dt>最新来源规则证据</dt><dd>{{report.catalogDiscovery.ruleHits}}</dd></div></dl>
    <p v-if="report.catalogDiscovery.truncated" class="message message-warning">来源或评估达到报告展示上限，需缩小范围核对。</p>
    <div class="table-scroll"><table class="data-table"><thead><tr><th>来源 / 系统</th><th>配置 / 运行</th><th>来源状态</th><th>观察时间</th></tr></thead><tbody><tr v-for="source in report.catalogDiscovery.sources" :key="source.id"><td>{{source.source}} · {{source.system}}<span class="table-secondary">{{source.provider==='openmetadata'?'OpenMetadata 深目录':'原生采集'}}</span></td><td>v{{source.revision}} · {{source.runId?'运行 #'+source.runId:'尚无运行'}}</td><td>{{source.current?'当前有效':source.status==='error'?'最新采集失败':'未观察、暂停或已过期'}}</td><td>{{source.observedAt?formatDate(source.observedAt):'—'}}</td></tr></tbody></table></div>
    <article v-for="item in report.catalogDiscovery.assessments.slice(0,catalogLimit)" :key="item.evidenceId"><p>检查 #{{item.checkId}} · 证据 #{{item.evidenceId}} · {{item.kind==='retest'?'来源自动复测':'来源观察'}} · {{item.current?'当前事实仍相同':'历史证据，当前事实需重新核对'}}</p><CatalogAssessmentEvidence :assessment="item.assessment"/></article>
    <button v-if="report.catalogDiscovery.assessments.length>catalogLimit" class="button button-secondary" @click="catalogLimit+=20">再显示20项评估</button>
  </section>

  <section v-if="report?.dataFlows" class="panel report-results" aria-labelledby="report-flows-title">
    <div class="panel-heading">
      <div><h2 id="report-flows-title">工作区数据流快照摘要</h2><p>快照 #{{ report.dataFlows.snapshotId }} · {{ formatDate(report.dataFlows.capturedAt) }}</p></div>
      <a class="text-link" :href="home + '/data-flows'">查看数据流转<Icon name="arrow" /></a>
    </div>
    <dl class="report-counts">
      <div><dt>应用</dt><dd>{{ report.dataFlows.summary.applications }}</dd></div>
      <div><dt>接口</dt><dd>{{ report.dataFlows.summary.apis }}</dd></div>
      <div><dt>库表</dt><dd>{{ report.dataFlows.summary.tables }}</dd></div>
      <div><dt>去标识主体</dt><dd>{{ report.dataFlows.summary.subjects }}</dd></div>
      <div><dt>节点</dt><dd>{{ report.dataFlows.summary.nodes }}</dd></div>
      <div><dt>关系</dt><dd>{{ report.dataFlows.summary.edges }}</dd></div>
    </dl>
    <p class="muted">{{ report.dataFlows.basis }}</p>
    <ul v-if="report.dataFlows.warnings.length"><li v-for="warning in report.dataFlows.warnings" :key="warning">{{ dataFlowWarning(warning) }}</li></ul>
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
            <td>
              <span class="badge status" :class="'status-' + entry.check.status">{{ checkStatusLabels[entry.check.status] }}</span>
              <template v-if="entry.evaluation?.assessment">
                <span class="table-secondary">自动检查 · {{ postgresAssessmentLabels[entry.evaluation.assessment] }}</span>
                <span v-if="entry.evaluation.assessmentReasons?.length" class="table-secondary">
                  {{ postgresAssessmentReasons(entry.evaluation.assessmentReasons) }}
                </span>
              </template>
            </td>
            <td>
              {{ entry.evidence.length }}
              <template v-if="entry.postgresEvidence">
                <span class="table-secondary">PostgreSQL 自动采集</span>
                <span class="table-secondary">{{ entry.postgresEvidence.columnCount }} 个字段 · {{ entry.postgresEvidence.sensitiveCandidateCount }} 个疑似敏感</span>
                <span class="table-secondary">{{ postgresPublicSelect(entry.postgresEvidence.publicSelect, entry.postgresEvidence.publicSelectColumnCount) }}</span>
              </template>
              <span v-if="entry.classification?.revision" class="table-secondary">
                {{ entry.classification.origin === 'automatic' ? '自动识别' : '人工确认' }} · 分类版本 {{ entry.classification.revision }}
              </span>
              <details v-if="entry.automaticSnapshot" class="report-classification-snapshot">
                <summary class="table-link">查看自动判定快照</summary>
                <ClassificationEvaluation :automatic="entry.automaticSnapshot" compact />
              </details>
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
    <div v-else class="empty-state"><strong>此范围暂无检查</strong><p>{{ archiveId ? '此归档保留了本次采集的识别汇总。' : '新建检查后，报告将纳入该记录和后续操作。' }}</p></div>
  </section>
</template>

<style scoped>
.report-classification-snapshot { min-width: 0; }
.report-classification-snapshot > summary { min-height: 2.5rem; padding-block: .5rem; cursor: pointer; }
.report-classification-snapshot[open] { min-width: min(37rem, 100%); }
</style>
