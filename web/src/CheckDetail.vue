<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, documentationUrl, formatDate, request, workspacePath, type Workspace } from './api'
import { activityCheckHints, activityKindLabels } from './activity'
import { postgresAssessmentLabels, postgresAssessmentReasons, postgresPermissionScope, postgresPublicSelect, postgresSensitivityLabel } from './postgresql'
import { runtimeRuleLabels } from './runtime-findings'
import { checkStatusLabels, evidenceKindLabels, eventLabels, type CheckDetail } from './workflow'
import CheckEvidenceForm from './CheckEvidenceForm.vue'
import CatalogAssessmentEvidence from './CatalogAssessmentEvidence.vue'
import Icon from './Icon.vue'
import TemplateCriteria from './TemplateCriteria.vue'
import ClassificationEvaluation from './ClassificationEvaluation.vue'
import RuntimeDetectionFacts from './RuntimeDetectionFacts.vue'

const props = defineProps<{ workspace: Workspace; checkId: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const apiUrl = workspacePath(props.workspace.slug) + '/checks/' + encodeURIComponent(props.checkId)
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const pageUrl = home + '/checks'
const record = ref<CheckDetail | null>(null)
const loading = ref(true)
const loadError = ref('')
const actionError = ref('')
const notice = ref('')
const actionPending = ref(false)
const actionConflict = ref(false)
const formVersion = ref(0)
const decision = ref<'verified' | 'changes' | ''>('')
const reviewNote = ref('')
const detailTab = ref<'evidence' | 'basis' | 'history'>('evidence')
const evidenceCount = (kind: string) => record.value?.evidence.filter(entry => entry.kind === kind).length ?? 0
const active = new AbortController()
const runtimeRetestLabels = {
  awaiting_remediation: '等待整改记录', awaiting_observations: '等待来源自动复测',
  passed: '自动复测通过 · 待独立复核', invalidated: '复测依据已失效', verified: '已完成独立复核',
}

const changes = computed(() => record.value?.status === 'needs_work'
  ? record.value.events.find(event => event.action === 'changes') : undefined)
const activityHint = computed(() => record.value?.activity ? activityCheckHints[record.value.activity.kind] : undefined)
const requirementUrl = computed(() => documentationUrl(record.value?.requirement.sourceUrl || ''))

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
    const updated = await request<CheckDetail>(apiUrl, { signal: active.signal })
    if (updated.reviewRevision !== record.value?.reviewRevision) {
      decision.value = ''
      reviewNote.value = ''
    }
    record.value = updated
    actionConflict.value = false
    actionError.value = ''
  } catch (error) {
    loadError.value = errorText(error)
  } finally {
    loading.value = false
  }
}

async function saveAction(action: 'submit' | 'review') {
  if (!record.value || actionPending.value || loading.value || actionConflict.value) return
  actionPending.value = true
  actionError.value = ''
  notice.value = ''
  try {
    const payload = action === 'submit' ? {} : {
      reviewRevision: record.value.reviewRevision, decision: decision.value, note: reviewNote.value.trim(),
    }
    await request(apiUrl + '/' + action, { method: 'POST', body: JSON.stringify(payload) })
    if (active.signal.aborted) return
    notice.value = action === 'submit' ? '已提交，等待独立复核。' : '已记录复核结论。'
    decision.value = ''
    reviewNote.value = ''
    await load()
  } catch (error) {
    actionConflict.value = error instanceof ApiError && error.status === 409
    actionError.value = errorText(error)
  } finally {
    actionPending.value = false
  }
}

async function bindPostgres() {
  const jobId = record.value?.availablePostgresJobId
  if (!jobId || loading.value || actionPending.value) return
  actionPending.value = true
  notice.value = ''
  actionError.value = ''
  try {
    await request(apiUrl + '/postgresql', {
      method: 'POST', body: JSON.stringify({ jobId }), signal: active.signal,
    })
    if (active.signal.aborted) return
    notice.value = '已关联 PostgreSQL 采集任务。下次采集成功后可查看证据。'
    await load()
  } catch (error) {
    actionConflict.value = error instanceof ApiError && error.status === 409
    actionError.value = errorText(error)
  } finally {
    actionPending.value = false
  }
}

async function evidenceSaved(message: string) {
  if (active.signal.aborted) return
  notice.value = message
  formVersion.value++
  await load()
}

onMounted(() => { void load() })
onBeforeUnmount(() => active.abort())
</script>

<template>
  <div class="check-record">
    <a class="back-link" :href="pageUrl"><Icon name="back" />返回监管检查</a>
    <p v-if="loading && !record" class="message" role="status">正在读取检查详情…</p>
    <div v-if="loadError" class="message message-error page-feedback">
      <p role="alert">{{ loadError }}</p>
      <button type="button" class="button button-secondary" @click="load">重新读取</button>
    </div>

    <template v-if="record">
      <header class="page-header">
        <div>
          <p class="eyebrow">检查与独立复核 <span class="mono">#{{ record.id }}</span></p>
          <h1>{{ record.template?.item.title || record.requirement.title }}</h1>
          <p>{{ record.assetName }} · {{ record.ownerTeam }}</p>
        </div>
        <div class="page-actions">
          <a v-if="record.feishuTargetDigest && workspace.role === 'owner'" class="button button-secondary"
             :href="home + '/automation/new/' + record.id"><Icon name="automation" />配置自动采集</a>
          <a v-if="record.runtimeFinding" class="button button-secondary" :href="home + '/api-security/runtime'">
            <Icon name="automation" />运行时观测
          </a>
          <a class="button button-secondary" :href="apiUrl + '/export'"><Icon name="download" />导出 JSON</a>
          <button v-if="record.canSubmit" type="button" class="button button-primary"
                  :disabled="actionPending || loading || actionConflict || Boolean(loadError)" @click="saveAction('submit')">提交复核</button>
        </div>
      </header>
      <p v-if="notice" class="message message-success page-feedback" role="status">{{ notice }}</p>
      <div v-if="actionError" class="message message-error page-feedback">
        <p role="alert">{{ actionError }}</p>
        <button v-if="actionConflict" type="button" class="button button-secondary" :disabled="loading" @click="load">刷新检查记录</button>
      </div>

      <ol class="workflow-spine check-workflow" aria-label="检查进度">
        <li class="is-recorded"><span>1</span><strong>发现</strong><small>检查 #{{ record.id }} · {{ formatDate(record.updatedAt) }}</small></li>
        <li :class="{ 'is-recorded': record.evidence.length }"><span>2</span><strong>取证</strong><small>{{ record.evidence.length ? record.evidence.length + ' 条记录已留存' : '尚未取证' }}</small></li>
        <li :class="{ 'is-recorded': evidenceCount('remediation') }"><span>3</span><strong>整改与复测</strong><small>{{ evidenceCount('remediation') }} 条整改 · {{ evidenceCount('retest') }} 条复测</small></li>
        <li :class="{ 'is-recorded': record.status === 'verified', 'is-current': record.status === 'in_review' }"><span>4</span><strong>独立复核</strong><small>{{ checkStatusLabels[record.status] }}</small></li>
      </ol>
      <div class="check-detail-tabs" role="tablist" aria-label="检查详情内容">
        <button v-for="tab in ([['evidence','证据记录'],['basis','检查依据'],['history','复核历史']] as const)" :key="tab[0]" type="button" role="tab" :aria-selected="detailTab === tab[0]" :class="{active:detailTab === tab[0]}" @click="detailTab = tab[0]">{{ tab[1] }}</button>
      </div>
      <section v-show="detailTab === 'basis'" class="panel" aria-labelledby="check-summary-title">
        <div class="panel-heading">
          <div><h2 id="check-summary-title">检查信息</h2><p>提交后由具备权限且未参与经办的账号独立复核。</p></div>
          <span class="badge status" :class="'status-' + record.status">{{ checkStatusLabels[record.status] }}</span>
        </div>
        <dl class="detail-list">
          <div><dt>关联资产</dt><dd>{{ record.assetName }}</dd></div>
          <div v-if="record.djangoId"><dt>Django 原检查编号</dt><dd>#{{ record.djangoId }}</dd></div>
          <div v-if="record.activity">
            <dt>关联处理活动</dt>
            <dd>
              <a class="table-link" :href="home + '/activities/' + record.activity.id">{{ record.activity.name }}</a>
              <span class="table-secondary">{{ activityKindLabels[record.activity.kind] }}</span>
            </dd>
          </div>
          <div><dt>整改责任团队</dt><dd>{{ record.ownerTeam }}</dd></div>
          <div><dt>更新时间</dt><dd class="date-text"><time :datetime="record.updatedAt">{{ formatDate(record.updatedAt) }}</time></dd></div>
          <div>
            <dt>监管要求来源</dt>
            <dd>
              <a v-if="requirementUrl" class="source-link" :href="requirementUrl" target="_blank" rel="noopener noreferrer">
                {{ record.requirement.citation || record.requirement.code }} <Icon name="external" />
              </a>
              <span v-else>{{ record.requirement.citation || record.requirement.code }}</span>
            </dd>
          </div>
          <div v-if="record.requirement.summary" class="detail-wide"><dt>要求说明</dt><dd class="text-block">{{ record.requirement.summary }}</dd></div>
          <div v-if="record.feishuTargetDigest" class="detail-wide">
            <dt>飞书采集范围</dt>
            <dd class="text-block">核对指定协作者的文档直接授权。链接、继承和群组授权等访问路径需单独核验。</dd>
          </div>
          <div class="detail-wide"><dt>检查范围与标准</dt><dd class="text-block">{{ record.note }}</dd></div>
          <div v-if="record.template" class="detail-wide">
            <dt>检查模板</dt>
            <dd>
              <a class="table-link" :href="home + '/templates/' + record.template.revisionId">{{ record.template.title }}</a>
              <span class="table-secondary">版本 {{ record.template.version }} · {{ record.template.item.title }}</span>
              <TemplateCriteria :item="record.template.item" />
            </dd>
          </div>
        </dl>
      </section>

      <section v-if="record.classification" v-show="detailTab === 'basis'" class="panel current-classification" aria-labelledby="check-classification-title">
        <div class="panel-heading">
          <div><h2 id="check-classification-title">当前分类与自动判定</h2><p>查看资产当前记录，并在证据中追溯各次采集快照。</p></div>
          <a class="button button-secondary" :href="home + '/classification/' + record.assetId">查看资产分类</a>
        </div>
        <div class="check-classification-body">
          <p v-if="record.classification.revision" class="field-hint">
            {{ record.classification.origin === 'automatic' ? '自动识别' : '人工确认' }} · 分类版本 {{ record.classification.revision }}
          </p>
          <ClassificationEvaluation v-if="record.classification.automatic" :automatic="record.classification.automatic" compact />
          <p v-else class="field-hint">暂无自动判定快照。</p>
        </div>
      </section>

      <div v-show="detailTab === 'evidence'" class="detail-grid check-evidence-workbench">
        <section class="panel" aria-labelledby="check-evidence-title">
          <div class="panel-heading">
            <div><h2 id="check-evidence-title">证据记录</h2><p>按来源标识和观察时间追溯核验依据。</p></div>
            <button v-if="record.feishuTargetDigest || record.postgresJobId || record.runtimeFinding" type="button" class="button button-secondary"
                    :disabled="loading || actionPending" @click="load">刷新证据</button>
          </div>
          <div v-if="record.runtimeFinding" class="runtime-check-binding">
            <strong>API 运行时自动取证</strong>
            <p class="text-block">{{ runtimeRuleLabels[record.runtimeFinding.rule] }} · <span class="mono">{{ record.runtimeFinding.method }} {{ record.runtimeFinding.route }}</span></p>
            <p class="field-hint">{{ record.runtimeFinding.service }} · 原观测来源 {{ record.runtimeFinding.source }} · #{{ record.runtimeFinding.sourceId }}</p>
            <p class="field-hint">
              最近触发 <time :datetime="record.runtimeFinding.lastSeen">{{ formatDate(record.runtimeFinding.lastSeen) }}</time>
              <template v-if="record.runtimeFinding.baselineScanId"> · <a class="table-link" :href="home + '/api-security/' + record.runtimeFinding.baselineScanId">触发时的契约扫描</a></template>
            </p>
            <p v-if="record.runtimeFinding.retestAt" class="field-hint">
              最近自动复测 <time :datetime="record.runtimeFinding.retestAt">{{ formatDate(record.runtimeFinding.retestAt) }}</time>
            </p>
            <RuntimeDetectionFacts v-if="record.runtimeFinding.detection" :detection="record.runtimeFinding.detection" />
            <section v-if="record.runtimeRetest" class="evidence-guidance" aria-label="自动复测状态">
              <strong>{{ runtimeRetestLabels[record.runtimeRetest.state] }}</strong>
              <p>{{ record.runtimeRetest.reason }}</p>
              <p v-if="record.runtimeRetest.nextEligibleAt && !['passed', 'verified'].includes(record.runtimeRetest.state)" class="field-hint">完整复测窗口最早可用时间：{{ formatDate(record.runtimeRetest.nextEligibleAt) }}。仍需同范围的完整新观察，等待本身不会产生通过记录。</p>
              <p class="field-hint">整改记录或受控执行成功回执启动自动复测。通过后系统提交独立复核；新风险、策略变化或来源失效会使原复测依据失效。</p>
            </section>
            <p class="field-hint">证据用于核对实际响应、发布范围和访问策略。复测须来自正信号绑定的同一来源，新规则还须符合当前已启用策略及明确可观察范围。</p>
          </div>
          <div v-if="record.postgresJobId || record.availablePostgresJobId" class="postgres-binding">
            <strong>PostgreSQL 自动取证</strong>
            <p v-if="record.postgresJobId" class="field-hint">
              已关联任务 #{{ record.postgresJobId }}。检查待取证或待整改时，后续快照可自动登记证据。
            </p>
            <p v-else class="field-hint">关联此资产的 PostgreSQL 任务后，可在后续采集中自动登记目录证据。</p>
            <button v-if="record.availablePostgresJobId" type="button" class="button button-secondary"
                    :disabled="loading || actionPending || actionConflict || Boolean(loadError)" @click="bindPostgres">
              {{ record.postgresJobId ? '更新 PostgreSQL 采集任务' : '关联 PostgreSQL 采集任务' }}
            </button>
            <a class="table-link" :href="home + '/assets/' + record.assetId">查看字段与权限</a>
          </div>
          <div v-if="activityHint" class="activity-guidance">
            <strong>建议核对的证据</strong>
            <p>{{ activityHint.scope }}</p>
            <p v-if="changes">{{ activityHint.retest }}</p>
            <p>仅登记记录编号或脱敏摘要，原始业务数据和提示词留在源系统。</p>
          </div>
          <div v-if="changes" class="evidence-guidance">
            <strong>本轮整改要求</strong>
            <p class="text-block">{{ changes.note }}</p>
            <ol v-if="record.runtimeFinding" class="remediation-steps">
              <li>完成变更并登记「整改记录」，关联审批或变更编号。</li>
              <li v-if="record.runtimeFinding.rule === 'unregistered_success'">
                导入同一服务的新 OpenAPI 契约，登记该接口并将 <code>deprecated</code> 设为 <code>false</code>。再通过原观测来源发起获授权的复测请求，取得新的 HTTP 2xx。
              </li>
              <li v-else-if="record.runtimeFinding.rule === 'deprecated_success'">在最新契约中保留该接口的弃用标记或移除该接口。通过原观测来源复测，同一批次需观测到该接口的 HTTP 410，且无 HTTP 2xx。</li>
              <li v-else>通过最近正信号的同一来源取得整改后的新鲜观测。敏感响应需明确完成响应检查；认证、请求模式需有明确结果；频次、错误和时段规则需经过完整窗口且有足够请求。缺失或未知数据不能替代复测，修改策略后需按新版本重新采证。</li>
              <li>{{ record.runtimeRetest ? '系统在取得有效的完整新窗口后自动复测，通过后进入独立复核；刷新证据核对具体依据。' : '刷新证据，核对系统登记的复测后提交独立复核。' }}</li>
            </ol>
            <ol v-else-if="record.feishuTargetDigest" class="remediation-steps">
              <li>在飞书移除指定协作者的直接授权。</li>
              <li>手工登记「整改记录」，关联变更记录。</li>
              <li>复测权限。已启用的采集任务会在下次运行时登记符合本轮条件的权限证据。</li>
              <li>提交独立复核。</li>
            </ol>
            <ol v-else-if="record.postgresJobId" class="remediation-steps">
              <li>完成源端整改后登记「整改记录」。</li>
              <li>等待下一轮 PostgreSQL 采集或从「自动采集」发起立即采集。</li>
              <li>刷新证据，核对复测结果后提交独立复核。</li>
            </ol>
            <p v-else-if="record.canAddEvidence && !record.canSubmit">登记整改后新取得的复测证据，再提交独立复核。</p>
          </div>
          <CheckEvidenceForm v-if="record.canAddEvidence && !loadError" :key="formVersion" :api-url="apiUrl"
                             :retest="record.status === 'needs_work'" :target-digest="record.feishuTargetDigest"
                             :automatic-retest="Boolean(record.runtimeFinding)"
                             @saved="evidenceSaved" @session-expired="emit('sessionExpired')" />

          <article v-for="entry in record.evidence" :key="entry.id" class="evidence">
            <div class="evidence-heading">
              <h3><span class="evidence-kind-label">{{ evidenceKindLabels[entry.kind] }}</span>{{ entry.sourceSystem }}</h3>
              <span v-if="entry.automationJobId" class="badge status-verified">自动采集</span>
              <span class="badge">{{ evidenceKindLabels[entry.kind] }}</span>
            </div>
            <p class="evidence-ref">记录标识 <span class="mono">{{ entry.sourceRecord }}</span></p>
            <p v-if="entry.automationJobId" class="field-hint">
              采集任务 #{{ entry.automationJobId }} · 运行批次 <span class="mono">{{ entry.automationRunId }}</span>
            </p>
            <p v-if="entry.feishu" class="field-hint">{{ entry.feishu.memberPresent ? '检出目标协作者的直接授权' : '未检出目标协作者的直接授权' }}</p>
            <p class="text-block">{{ entry.summary }}</p>
            <CatalogAssessmentEvidence v-if="entry.catalog" :assessment="entry.catalog"/>
            <div v-if="entry.postgres" class="postgres-evidence-meta">
              <div v-if="entry.postgres.assessment" class="postgres-assessment">
                <span class="badge" :class="entry.postgres.assessment === 'review_required' ? 'status-needs_work' : 'status-draft'">
                  自动检查 · {{ postgresAssessmentLabels[entry.postgres.assessment] }}
                </span>
                <p v-if="entry.postgres.assessmentReasons?.length" class="field-hint">{{ postgresAssessmentReasons(entry.postgres.assessmentReasons) }}</p>
              </div>
              <p class="field-hint">
                <span v-if="entry.postgres.database" class="mono">{{ entry.postgres.database }} · </span>
                <span class="mono">{{ entry.postgres.sourceRef }}</span>
                · {{ entry.postgres.columnCount }} 个字段 · {{ entry.postgres.sensitiveCandidateCount }} 个疑似敏感 · {{ entry.postgres.grantCount }} 条直接授权
              </p>
              <p class="field-hint">
                {{ postgresPublicSelect(entry.postgres.publicSelect, entry.postgres.publicSelectColumnCount) }}
                · {{ entry.postgres.publicSchemaUsage ? 'PUBLIC 具备 schema USAGE' : 'PUBLIC 未获 schema USAGE' }}
              </p>
              <p class="field-hint">{{ postgresPermissionScope }}</p>
              <details v-if="entry.postgres.sensitiveFields?.length || entry.postgres.selectGrants?.length"
                       class="asset-metadata postgres-evidence-details">
                <summary>查看疑似字段和 SELECT 授权</summary>
                <div v-if="entry.postgres.sensitiveFields?.length">
                  <strong>疑似敏感字段</strong>
                  <ul>
                    <li v-for="field in entry.postgres.sensitiveFields" :key="field.name">
                      <span class="mono">{{ field.name }}</span> · {{ postgresSensitivityLabel(field.sensitivity) }}
                    </li>
                  </ul>
                  <p v-if="entry.postgres.sensitiveFieldsOmitted" class="field-hint">另有 {{ entry.postgres.sensitiveFieldsOmitted }} 个字段，可在资产详情查看。</p>
                </div>
                <div v-if="entry.postgres.selectGrants?.length">
                  <strong>表所有者之外的直接 SELECT 授权</strong>
                  <ul>
                    <li v-for="(grant, index) in entry.postgres.selectGrants" :key="index">
                      <span class="mono">{{ grant.public ? 'PUBLIC' : grant.grantee }}</span> ·
                      {{ grant.column || '整表' }} · {{ grant.schemaUsage ? '有 schema USAGE' : '未检出 schema USAGE' }}
                    </li>
                  </ul>
                  <p v-if="entry.postgres.selectGrantsOmitted" class="field-hint">另有 {{ entry.postgres.selectGrantsOmitted }} 条授权，可在资产详情查看。</p>
                </div>
              </details>
              <details v-if="entry.postgres.classification" class="check-classification-snapshot">
                <summary class="table-link">查看本条证据的自动判定快照</summary>
                <ClassificationEvaluation :automatic="entry.postgres.classification" compact />
              </details>
            </div>
            <div class="evidence-meta">
              <span>观察于 <time :datetime="entry.observedAt">{{ formatDate(entry.observedAt) }}</time></span>
              <span>{{ entry.automationJobId ? entry.recordedBy + ' 授权的任务登记于' : entry.recordedBy + ' 登记于' }}
                <time :datetime="entry.createdAt">{{ formatDate(entry.createdAt) }}</time>
              </span>
            </div>
          </article>
          <div v-if="!record.evidence.length" class="empty-state">
            <strong>尚未登记证据</strong>
            <p>登记源系统记录的标识和脱敏摘要，以便复核时定位原始记录。</p>
          </div>
        </section>

        <aside class="check-review-rail">
          <section class="panel"><div class="panel-heading"><h2>复核要点</h2></div><ul class="review-scope-list"><li>同一来源、目标和规则范围</li><li>新复测晚于本轮整改记录</li><li>原始、整改与复测分别保留</li><li>复核人回避经办与证据登记</li></ul><p class="review-callout">执行回执、来源复测与独立通过分别记录，不能自动等同。</p></section>
      <section v-if="record.canReview" class="panel review-panel" aria-labelledby="check-review-title">
        <div class="panel-heading">
          <div><h2 id="check-review-title">独立复核</h2><p>核对本次提交的证据，说明核验结果或待整改事项。</p></div>
        </div>
        <form class="evidence-form" @submit.prevent="saveAction('review')">
          <p v-if="record.feishuTargetDigest" class="field-hint page-feedback">
            请对照飞书源端权限，并单独核验链接、继承和群组等访问路径。
          </p>
          <p v-if="record.runtimeFinding" class="field-hint page-feedback">
            请核对变更记录、原来源自动复测和实际访问范围。
          </p>
          <p v-if="!record.canVerify && record.runtimeFinding" class="message message-warning page-feedback">
            本轮尚缺符合条件的自动复测。请记录整改要求。
          </p>
          <p v-else-if="!record.canVerify && record.feishuTargetDigest" class="message message-warning page-feedback">
            当前飞书清单仍检出目标协作者的直接授权。请记录整改要求。
          </p>
          <div class="field">
            <label for="check-review-decision">复核决定</label>
            <select id="check-review-decision" v-model="decision" required>
              <option value="" disabled>选择复核决定</option>
              <option value="changes">要求整改</option>
              <option v-if="record.canVerify" value="verified">复核通过</option>
            </select>
          </div>
          <div class="field">
            <label for="check-review-note">复核意见</label>
            <textarea id="check-review-note" v-model="reviewNote" maxlength="600" required></textarea>
          </div>
          <div class="form-actions">
            <button type="submit" class="button button-primary" :disabled="actionPending || loading || actionConflict || Boolean(loadError)">
              {{ actionPending ? '正在保存…' : '保存复核结果' }}
            </button>
          </div>
        </form>
      </section>
          <section v-if="!record.canReview" class="panel"><div class="panel-heading"><h2>当前复核状态</h2></div><div class="review-readonly"><span class="badge status" :class="'status-' + record.status">{{ checkStatusLabels[record.status] }}</span><p>{{ record.status === 'verified' ? '当前检查已完成独立复核，原采用证据保持留存。' : record.status === 'in_review' ? '当前账号不能独立复核本次提交，请由符合回避要求的人员核验。' : '完成来源取证和必要整改后，再提交独立复核。' }}</p></div></section>
        </aside>
      </div>


        <section v-show="detailTab !== 'basis'" class="panel check-audit-trail" aria-labelledby="check-events-title">
          <div class="panel-heading"><div><h2 id="check-events-title">操作历史</h2><p>查看登记、提交和复核记录。</p></div></div>
          <ol v-if="record.events.length" class="timeline">
            <li v-for="event in record.events" :key="event.id">
              <strong>{{ eventLabels[event.action] }}</strong>
              <div class="event-meta">{{ event.actor }} · <time :datetime="event.createdAt">{{ formatDate(event.createdAt) }}</time></div>
              <p v-if="event.note" class="text-block">{{ event.note }}</p>
            </li>
          </ol>
          <div v-else class="empty-state"><p>尚无操作记录。</p></div>
        </section>
    </template>
  </div>
</template>

<style scoped>
.runtime-check-binding {
  display: grid;
  gap: .25rem;
  padding: .75rem 1.1rem;
  border-bottom: 1px solid var(--line);
  background: var(--surface-subtle);
}
.current-classification { margin-top: 1rem; }
.check-classification-body { padding: .8rem 1.1rem; }
.check-classification-snapshot { margin-block: .65rem; }
.check-classification-snapshot > summary { min-height: 2.5rem; padding-block: .5rem; cursor: pointer; }

.check-workflow{list-style:none;margin:28px 0 24px;padding:0 20px;display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:12px}.check-workflow li{position:relative;display:grid;justify-items:center;gap:6px;font-size:12px;text-align:center}.check-workflow li::before{content:"";position:absolute;top:14px;height:1px;background:var(--line);left:-50%;right:50%}.check-workflow li:first-child::before{display:none}.check-workflow li>span{z-index:1;display:grid;place-items:center;width:29px;height:29px;border:1px solid var(--input-line);border-radius:50%;background:white;color:var(--secondary)}.check-workflow .is-recorded>span{background:var(--teal);border-color:var(--teal);color:white}.check-workflow .is-current>span{border-color:var(--accent);color:var(--accent);box-shadow:0 0 0 4px var(--accent-soft)}.check-workflow small{color:var(--secondary);font-size:12px;max-width:230px}.check-detail-tabs{display:flex;flex-wrap:wrap;gap:4px;margin-bottom:16px;border-bottom:1px solid var(--line)}.check-detail-tabs button{min-height:44px;cursor:pointer;border:0;border-bottom:2px solid transparent;background:none;padding:12px 17px;color:var(--secondary);font-size:12px}.check-detail-tabs button.active{color:var(--accent);border-bottom-color:var(--accent);font-weight:700}.check-record .check-evidence-workbench{display:grid;grid-template-columns:minmax(0,1fr) 340px;gap:16px;align-items:start}.check-review-rail{display:grid;gap:16px;min-width:0;position:sticky;top:16px}.check-review-rail .review-panel{margin:0}.review-scope-list{list-style:none;padding:18px;margin:0;display:grid;gap:17px;font-size:12px}.review-scope-list li::before{content:"";display:inline-block;width:10px;height:10px;border:1px solid var(--input-line);margin-right:10px;vertical-align:middle}.review-callout{font-size:12px;background:var(--surface-subtle);margin:0 18px 18px;padding:12px;color:var(--secondary);border-radius:6px}.review-readonly{padding:18px;display:grid;gap:12px;font-size:12px}.check-record .evidence{margin:16px;padding:17px;border:1px solid var(--line);border-radius:8px;background:white}.evidence-kind-label{display:inline-block;color:var(--secondary);font-size:12px;margin-right:10px}.evidence-heading h3{font-size:13px}.check-audit-trail{margin-top:16px}.check-audit-trail .timeline{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:20px;padding:20px;max-height:400px;overflow:auto}.check-audit-trail .timeline li{margin:0;padding-bottom:0}.check-review-rail .evidence-form{padding:18px}@media(max-width:1320px){.check-record .check-evidence-workbench{grid-template-columns:minmax(0,1fr) 300px}}
</style>
