<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ApiError, describeError, documentationUrl, downloadFile, formatDate, request, workspacePath, type Workspace } from './api'
import { assessmentFactSourceLabel } from './assessments'
import { useScenarioContext } from './scenario-context'
import { scenarios } from './scenario-navigation'
import { scenarioCheckApplicabilityLabels, scenarioCheckOutcomeLabels, scenarioCheckReportLabels, scenarioCheckRetestLabels, type ScenarioCheckFinding, type ScenarioCheckRun } from './scenario-checks'
import { controlKindLabels, controlResultLabel, controlStatusLabels, type ControlAction } from './security-operations'
import Icon from './Icon.vue'
import RuntimeEvidenceScope from './RuntimeEvidenceScope.vue'
import type { RuntimeEvidenceScope as RuntimeScope } from './runtime-evidence-scope'

const props = defineProps<{ workspace: Workspace; scenarioId: string; taskId?: string; assetId: string; activityId: string; scopeReady: boolean }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const { href } = useScenarioContext(props.workspace.slug)
const base = workspacePath(props.workspace.slug) + '/scenario-check-runs'
const lifetime = new AbortController()
const editor = computed(() => ['owner', 'operator'].includes(props.workspace.role))
const selected = ref<ScenarioCheckRun>()
const history = ref<ScenarioCheckRun[]>([])
const historyLimit = ref(50)
const historyLoading = ref(false)
const detailLoading = ref(false)
const running = ref(false)
const exporting = ref(false)
const error = ref('')
const notice = ref('')
const view = ref('findings')
const views = [{ key: 'findings', label: '检查发现' }, { key: 'evidence', label: '证据清单' }, { key: 'requirements', label: '监管报告要求' }, { key: 'history', label: '历史结果' }]
const outcomeFilter = ref('')
const selectedFindingId = ref('')
const findings = computed(() => selected.value?.findings?.filter(item => !outcomeFilter.value || item.outcome === outcomeFilter.value) ?? [])
const finding = computed(() => findings.value.find(item => item.id === selectedFindingId.value) ?? findings.value[0])
const runtimeScope = ref<RuntimeScope>(); const runtimeScopeValid=ref(true); const runtimeEnabled=ref(false)
function runtimeChanged(scope:RuntimeScope|undefined,valid:boolean,enabled:boolean){runtimeScope.value=scope;runtimeScopeValid.value=valid;runtimeEnabled.value=enabled;resetScope()}
const canRun = computed(() => editor.value && (runtimeEnabled.value ? runtimeScopeValid.value && !!runtimeScope.value : props.scopeReady && !!props.assetId) && !running.value && !detailLoading.value && !actionBusy.value)
const selectedEvidence = computed(() => selected.value?.evidence?.filter(item => finding.value?.evidenceRefs.includes(item.id)) ?? [])
const findingActions = computed(() => selected.value?.actions?.filter(item => item.findingId === finding.value?.id) ?? [])
const actionFinding = ref<ScenarioCheckFinding>()
const actionDraft = reactive({ reason: '', durationMinutes: 60 })
const actionBusy = ref(false)
const actionHeading = ref<HTMLElement>()
let historyController: AbortController | undefined
let detailController: AbortController | undefined
let runController: AbortController | undefined
let actionController: AbortController | undefined

function failure(cause: unknown) {
  if (lifetime.signal.aborted) return
  if (cause instanceof ApiError && cause.status === 401) { emit('sessionExpired'); return }
  error.value = describeError(cause)
}
function outcomeClass(outcome: string) { return outcome === 'compliant' ? 'status-verified' : outcome === 'non_compliant' ? 'status-needs_work' : 'status-draft' }
function outcomeLabel(outcome: string) { return scenarioCheckOutcomeLabels[outcome] || outcome }
function runTitle(value: ScenarioCheckRun) { return value.task ? scenarios.find(scene => scene.id === value.scenario)?.tasks.find(task => task.id === value.task)?.label || value.task : '场景综合检查' }
function selectResult(value: ScenarioCheckRun) { selected.value = value; selectedFindingId.value = ''; outcomeFilter.value = ''; actionFinding.value = undefined }
async function loadHistory() {
  historyController?.abort()
  history.value = []
  if (!runtimeEnabled.value && !props.assetId && !props.activityId) { historyLoading.value = false; return }
  const operation = new AbortController(); historyController = operation; historyLoading.value = true
  const query = new URLSearchParams({ scenario: props.scenarioId, task: props.taskId ?? '' })
  if (runtimeEnabled.value) {if(!runtimeScope.value){historyLoading.value=false;return};for(const [key,value] of Object.entries(runtimeScope.value))query.set(key,String(value))}
  else {if (props.assetId) query.set('assetId', props.assetId);if (props.activityId) query.set('activityId', props.activityId)}
  try {
    const value = await request<{ items: ScenarioCheckRun[]; limit: number }>(base + '?' + query, { signal: operation.signal })
    if (!operation.signal.aborted) {
      history.value = value.items; historyLimit.value = value.limit ?? 50
      if (!selected.value && value.items[0]) await loadDetail(value.items[0].id)
    }
  } catch (cause) { if (!operation.signal.aborted) failure(cause) }
  finally { if (!operation.signal.aborted) historyLoading.value = false }
}
async function loadDetail(id: number) {
  detailController?.abort()
  const operation = new AbortController(); detailController = operation; detailLoading.value = true; error.value = ''
  try {
    const value = await request<ScenarioCheckRun>(base + '/' + id, { signal: operation.signal })
    if (!operation.signal.aborted) selectResult(value)
  } catch (cause) { if (!operation.signal.aborted) failure(cause) }
  finally { if (!operation.signal.aborted) detailLoading.value = false }
}
async function run(previous?: ScenarioCheckRun) {
  if (!canRun.value) return
  const chosenScope=previous?previous.scope:runtimeScope.value
  const operation = new AbortController(); runController = operation
  running.value = true; error.value = ''; notice.value = ''
  try {
    const value = await request<ScenarioCheckRun>(base, { method: 'POST', signal: operation.signal, body: JSON.stringify({ scenario: previous?.scenario ?? props.scenarioId, task: previous?.task ?? props.taskId ?? '', assetId: previous?.assetId ?? (runtimeEnabled.value ? 0 : Number(props.assetId) || 0), activityId: previous ? previous.activityId ?? 0 : runtimeEnabled.value ? 0 : Number(props.activityId) || 0, ...(chosenScope ? {scope:chosenScope}:{}), ...(previous ? { previousRunId: previous.id } : {}) }) })
    if (!operation.signal.aborted) {
      selectResult(value); view.value = 'findings'; await loadHistory()
      if (operation.signal.aborted) return
      notice.value = previous ? '已保存本次复查结果，是否取得新来源观察请核对复测状态。' : '检查结果已保存，判定基于本次可用来源事实。'
    }
  } catch (cause) { if (!operation.signal.aborted) failure(cause) }
  finally { if (!operation.signal.aborted) running.value = false }
}
async function exportRun(format: 'html' | 'json' | 'csv' | 'zip') {
  if (!selected.value || exporting.value) return
  exporting.value = true; error.value = ''
  try { await downloadFile(base + '/' + selected.value.id + '/export?' + new URLSearchParams({ format }), `scenario-check-${selected.value.id}.${format}`, lifetime.signal) }
  catch (cause) { failure(cause) }
  finally { exporting.value = false }
}
async function proposeRemediation(value: ScenarioCheckFinding) {
  if (!editor.value || !value.remediation.executable || actionBusy.value) return
  actionFinding.value = value; actionDraft.reason = Array.from(value.remediation.advice).slice(0, 300).join(''); actionDraft.durationMinutes = 60
  await nextTick(); actionHeading.value?.focus({ preventScroll: true }); actionHeading.value?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
}
async function submitRemediation() {
  if (!selected.value || !actionFinding.value || !editor.value || actionBusy.value) return
  const reason = actionDraft.reason.trim()
  if (!reason || Array.from(reason).length > 300) { error.value = '请填写 1 至 300 字的整改申请原因。'; return }
  if (!Number.isInteger(actionDraft.durationMinutes) || actionDraft.durationMinutes < 1 || actionDraft.durationMinutes > 1440) { error.value = '授权有效期应为 1 至 1440 分钟的整数。'; return }
  const operation = new AbortController(); actionController = operation; actionBusy.value = true; error.value = ''; notice.value = ''
  try {
    const value = await request<ControlAction>(base + '/' + selected.value.id + '/findings/' + encodeURIComponent(actionFinding.value.id) + '/remediation', { method: 'POST', signal: operation.signal, body: JSON.stringify({ reason, durationMinutes: actionDraft.durationMinutes }) })
    if (!operation.signal.aborted) { actionFinding.value = undefined; await loadDetail(selected.value.id); notice.value = `受控整改申请 #${value.id} 已创建，等待独立审批及来源执行。执行回执与新来源复测分别核对。` }
  } catch (cause) { if (!operation.signal.aborted) failure(cause) }
  finally { if (!operation.signal.aborted) actionBusy.value = false }
}
function resetScope() {
  historyController?.abort(); detailController?.abort(); runController?.abort(); actionController?.abort()
  running.value = false; detailLoading.value = false; actionBusy.value = false
  selected.value = undefined; selectedFindingId.value = ''; actionFinding.value = undefined; error.value = ''; notice.value = ''; outcomeFilter.value = ''
  void loadHistory()
}
watch(() => [props.scenarioId, props.taskId, props.assetId, props.activityId], resetScope)
onMounted(() => { void loadHistory() })
onBeforeUnmount(() => { lifetime.abort(); historyController?.abort(); detailController?.abort(); runController?.abort(); actionController?.abort() })
defineExpose({ run, canRun, running })
</script>

<template>
  <section class="scenario-checks" aria-labelledby="scenario-check-results-title" :aria-busy="running || detailLoading || actionBusy">
    <div class="scenario-check-runbar"><div><h2 id="scenario-check-results-title">技术检查与发现</h2><p>分析当前已采集事实，保存规则、证据和范围；新来源采集与结果复查分别记录。</p></div><div class="scenario-check-actions"><button class="button button-secondary" type="button" :disabled="historyLoading || running" @click="view = 'history'; loadHistory()">查看历史结果</button><button v-if="editor" class="button button-primary" type="button" :disabled="!canRun" @click="run()"><Icon name="checks"/>{{ running ? '正在运行检查…' : '运行检查' }}</button></div></div>
    <RuntimeEvidenceScope :workspace="workspace" @change="runtimeChanged" @session-expired="emit('sessionExpired')"/><p v-if="runtimeEnabled" class="scenario-check-message">本次按独立运行时来源范围检查，不将该来源伪关联到上方数据库或业务资产。来源观察不完整的项目保持待核实。</p>
    <p v-if="!runtimeEnabled && !assetId && !activityId" class="scenario-check-message">先选择已授权资产或处理活动，再运行检查或查看该范围的历史结果。</p><p v-else-if="!taskId" class="scenario-check-message">当前为场景综合技术检查，使用本页列明的技术规则；选择专项项可进一步核对适用说明和报告要求。</p><p v-else-if="!editor" class="scenario-check-message">当前角色可查看检查结果、证据与报告；负责人或经办人可运行检查及申请受控整改。</p>
    <p class="scenario-check-message">资产范围核对分类一致性、数据库直接授权与保护配置覆盖；运行时范围核对实际用户/IP、敏感数据、共享及目的地观察与已配置策略。未提供事实或许可基线时保持待核实。</p>
    <p v-if="error" class="message message-error" role="alert">{{ error }}</p><p v-if="notice" class="message message-success" role="status">{{ notice }}</p>
    <nav class="scenario-check-tabs" aria-label="技术检查结果视图"><button v-for="item in views" :key="item.key" type="button" :aria-pressed="view === item.key" :class="{active:view === item.key}" @click="view = item.key">{{ item.label }}</button></nav>
    <template v-if="selected && view !== 'history'">
      <div class="scenario-check-result-head"><div><span class="badge" :class="outcomeClass(selected.outcome)">{{ outcomeLabel(selected.outcome) }}</span><strong>{{ runTitle(selected) }} #{{ selected.id }} · {{ selected.assetName || selected.scope?.service || '运行时来源' }}</strong><span>{{ formatDate(selected.createdAt) }} · {{ selected.actor }}</span></div><div class="scenario-check-actions"><button class="button button-secondary" type="button" :disabled="detailLoading || running" @click="loadDetail(selected.id)">刷新结果</button><button v-if="editor" class="button button-secondary" type="button" :disabled="!canRun" @click="run(selected)">按本次范围复查</button></div></div>
      <dl class="scenario-check-counts"><div><dt>符合</dt><dd>{{ selected.counts.compliant }}</dd></div><div><dt>不符合</dt><dd>{{ selected.counts.nonCompliant }}</dd></div><div><dt>待核实</dt><dd>{{ selected.counts.needsVerification }}</dd></div><div><dt>复测状态</dt><dd class="scenario-check-retest">{{ scenarioCheckRetestLabels[selected.retestState] || selected.retestState }}</dd></div></dl>
      <p class="scenario-check-projection">{{ selected.engineVersion }}<template v-if="selected.assetId"> · 资产分类 v{{ selected.assetRevision }}</template><template v-if="selected.scope"> · 来源 #{{selected.scope.sourceId}} / {{selected.scope.service}} · {{formatDate(selected.scope.from)}} 至 {{formatDate(selected.scope.to)}}<template v-if="selected.scope.subjectRef"> · 主体 {{selected.scope.subjectRef}}</template><template v-if="selected.scope.clientIp"> · IP {{selected.scope.clientIp}}</template></template><template v-if="selected.activityId"> · 活动 #{{ selected.activityId }} / v{{ selected.activityRevision }}</template><template v-if="selected.previousRunId"> · 关联前次检查 #{{ selected.previousRunId }}</template>。本次依据已存来源事实完成检查；无新来源观察时不会标记源端复测完成。</p>
      <ul v-if="selected.warnings?.length" class="scenario-check-warnings"><li v-for="warning in selected.warnings" :key="warning">{{ warning }}</li></ul>
      <div class="scenario-check-exportbar"><span>本次检查报告与证据</span><button class="table-link" type="button" :disabled="exporting" @click="exportRun('html')">导出 HTML 报告</button><button class="table-link" type="button" :disabled="exporting" @click="exportRun('json')">导出 JSON 快照</button><button class="table-link" type="button" :disabled="exporting" @click="exportRun('csv')">导出发现 CSV</button><button class="table-link" type="button" :disabled="exporting" @click="exportRun('zip')">下载证据包 ZIP</button></div>

      <div v-if="view === 'findings'" class="scenario-findings-layout"><div class="scenario-findings-main"><div class="scenario-findings-filter"><label>检查结果<select v-model="outcomeFilter"><option value="">全部结果</option><option v-for="(label,key) in scenarioCheckOutcomeLabels" :key="key" :value="key">{{ label }}</option></select></label><span>本次 {{ findings.length }} 项</span></div><div class="table-scroll" role="region" tabindex="0" aria-label="本次技术检查发现"><table class="data-table scenario-findings-table"><thead><tr><th scope="col">检查项与要求</th><th scope="col">结果</th><th scope="col">观察与范围</th><th scope="col">证据</th></tr></thead><tbody><tr v-for="item in findings" :key="item.id" :class="{selected:finding?.id === item.id}"><th scope="row"><button class="scenario-finding-select" type="button" :aria-pressed="finding?.id === item.id" @click="selectedFindingId = item.id">{{ item.title }}</button><span class="table-secondary">{{ item.requirementCode }} · {{ item.ruleVersion }}</span></th><td><span class="badge" :class="outcomeClass(item.outcome)">{{ outcomeLabel(item.outcome) }}</span></td><td>{{ item.observed }}<span class="table-secondary">{{ item.coverage }}</span></td><td>{{ item.evidenceRefs.length }} 项<span v-if="item.missingConditions.length" class="table-secondary">{{ item.missingConditions.length }} 项待核实条件</span></td></tr></tbody></table><p v-if="!findings.length" class="empty-state">当前筛选下没有检查项。</p></div></div>
        <aside v-if="finding" class="scenario-finding-detail"><h3>{{ finding.title }}</h3><span class="badge" :class="outcomeClass(finding.outcome)">{{ outcomeLabel(finding.outcome) }}</span><dl><div><dt>核查要求</dt><dd>{{ finding.expected }}</dd></div><div><dt>本次观察</dt><dd>{{ finding.observed }}</dd></div><div><dt>采用依据</dt><dd><a v-if="documentationUrl(finding.basis.url)" class="table-link" :href="documentationUrl(finding.basis.url)" target="_blank" rel="noopener noreferrer">{{ finding.basis.title }}</a><span v-else>{{ finding.basis.title }}</span><span class="table-secondary">{{ finding.basis.clause }} · {{ finding.basis.version }}</span></dd></div><div><dt>观察覆盖</dt><dd>{{ finding.coverage }}</dd></div><div><dt>整改建议</dt><dd>{{ finding.remediation.advice }}</dd></div></dl><section v-if="finding.missingConditions.length" class="scenario-finding-gaps"><h4>尚待核实</h4><ul><li v-for="condition in finding.missingConditions" :key="condition">{{ condition }}</li></ul></section><details v-if="selectedEvidence.length" class="scenario-finding-evidence"><summary>采用证据（{{ selectedEvidence.length }}）</summary><ul><li v-for="evidence in selectedEvidence" :key="evidence.id"><strong>{{ assessmentFactSourceLabel(evidence.source) }}</strong><span>{{ evidence.record }}</span><time v-if="evidence.observedAt">{{ formatDate(evidence.observedAt) }}</time><span>{{ evidence.scope }}</span></li></ul></details><div class="scenario-finding-next"><a v-if="finding.checkId" class="button button-secondary" :href="href('checks/' + finding.checkId)">查看关联检查与复测</a><template v-for="action in findingActions" :key="action.actionId"><a class="button button-secondary" :href="href('operations/' + action.actionId)">处置 #{{ action.actionId }} · {{ action.status === 'applied' ? '来源回执已执行，待复测/复核' : controlStatusLabels[action.status] || action.status }}</a><p v-if="action.result">{{ controlResultLabel(action.result.code) }}；来源执行回执与新来源复测分别核对。</p></template><a v-if="finding.actionId && !findingActions.some(action => action.actionId === finding?.actionId)" class="button button-secondary" :href="href('operations/' + finding.actionId)">查看处置 #{{ finding.actionId }}</a><button v-if="editor && finding.remediation.executable && !findingActions.length && !finding.actionId" class="button button-primary" type="button" :disabled="actionBusy" @click="proposeRemediation(finding)">申请受控整改</button><p v-if="finding.outcome === 'compliant'" class="field-hint">本项技术检查符合，无需发起修复。</p><p v-else-if="!finding.remediation.executable" class="field-hint">{{ finding.remediation.reason || '此项暂无可执行的自动处置，按建议整改后取得同范围新来源事实再复查。' }}</p></div></aside>
      </div>

      <section v-else-if="view === 'evidence'" class="scenario-check-evidence"><div class="scenario-check-section-title"><h3>本次采用的技术证据</h3><span>{{ selected.evidence?.length ?? 0 }} 项 · 按本次范围冻结</span></div><article v-for="evidence in selected.evidence" :key="evidence.id"><div><strong>{{ assessmentFactSourceLabel(evidence.source) }}</strong><span class="mono">{{ evidence.id }}</span></div><dl><div><dt>来源记录</dt><dd>{{ evidence.record }}</dd></div><div><dt>观察时点</dt><dd>{{ evidence.observedAt ? formatDate(evidence.observedAt) : '未提供' }}</dd></div><div><dt>实际范围</dt><dd>{{ evidence.scope }}</dd></div><div><dt>证据摘要</dt><dd class="mono">{{ evidence.digest }}</dd></div></dl><details><summary>查看冻结证据内容</summary><pre>{{ JSON.stringify(evidence.snapshot,null,2) }}</pre></details></article><p v-if="!selected.evidence?.length" class="empty-state">本次没有可采用的来源证据，检查项保持待核实并列出缺口。</p></section>

      <section v-else-if="view === 'requirements'" class="scenario-report-requirements"><div class="scenario-check-section-title"><h3>监管报告要求与证据覆盖</h3><p>对照本次采用的要求版本列示可用内容、缺口与需另行确认的正式程序。</p></div><article v-for="requirement in selected.reportRequirements" :key="requirement.key"><div class="scenario-report-heading"><h4>{{ requirement.title }}</h4><span class="badge" :class="requirement.status === 'available' ? 'status-verified' : 'status-needs_work'">{{ requirement.key === 'technical_check_report' && requirement.status === 'available' ? '内部检查报告可导出' : scenarioCheckReportLabels[requirement.status] || requirement.status }}</span></div><p>{{ scenarioCheckApplicabilityLabels[requirement.applicability] || requirement.applicability }}</p><dl><div><dt>要求版本</dt><dd>{{ requirement.basis.title }} · {{ requirement.basis.version }} · {{ requirement.basis.clause }}<a v-if="documentationUrl(requirement.basis.url)" class="table-link" :href="documentationUrl(requirement.basis.url)" target="_blank" rel="noopener noreferrer">公开依据</a></dd></div><div><dt>实际覆盖</dt><dd>{{ requirement.coverage }}</dd></div><div><dt>关联检查</dt><dd>{{ requirement.findingIds.length }} 项检查 · {{ requirement.evidenceRefs.length }} 项证据引用</dd></div></dl><h5>报告所需内容</h5><ul><li v-for="item in requirement.requiredDeliverables" :key="item">{{ item }}</li></ul><template v-if="requirement.missingConditions.length"><h5>未满足或待补条件</h5><ul class="scenario-finding-gaps"><li v-for="item in requirement.missingConditions" :key="item">{{ item }}</li></ul></template><template v-if="requirement.externalSteps.length"><h5>需另行核实的正式程序</h5><ul><li v-for="item in requirement.externalSteps" :key="item">{{ item }}</li></ul></template><p v-if="requirement.retentionNote" class="field-hint">{{ requirement.retentionNote }}</p></article><p v-if="!selected.reportRequirements?.length" class="empty-state">本次检查未返回额外报告要求，可先核对检查依据与缺失条件。</p><div class="scenario-check-report-note"><p>导出报告记录本次技术判定与证据缺口。当前处置回执可随导出附列，原检查结论与冻结证据保持原时点。</p><p>证据覆盖不等于正式报送、备案或主管部门批准；未核实内容按缺项列入报告。</p><a class="table-link" :href="href('reports/archives')">核对已有冻结报告</a></div></section>
    </template>

    <section v-if="view === 'history'" class="scenario-check-history"><div class="scenario-check-section-title"><h3>当前范围历史检查</h3><span>当前筛选最近 {{ historyLimit }} 次 · {{ history.length }} 条</span></div><p v-if="historyLoading" class="empty-state" role="status">正在读取历史结果…</p><div v-else-if="history.length" class="table-scroll" role="region" tabindex="0" aria-label="历史检查结果"><table class="data-table"><thead><tr><th scope="col">检查与资产</th><th scope="col">本次判定</th><th scope="col">观察与复查</th><th scope="col">执行人 / 时间</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="item in history" :key="item.id"><th scope="row">#{{ item.id }} · {{ item.assetName }}<span class="table-secondary">{{ runTitle(item) }} · {{ item.engineVersion }}</span></th><td><span class="badge" :class="outcomeClass(item.outcome)">{{ outcomeLabel(item.outcome) }}</span><span class="table-secondary">符合 {{ item.counts.compliant }} / 不符合 {{ item.counts.nonCompliant }} / 待核实 {{ item.counts.needsVerification }}</span></td><td>{{ scenarioCheckRetestLabels[item.retestState] || item.retestState }}<span v-if="item.previousRunId" class="table-secondary">前次 #{{ item.previousRunId }}</span></td><td>{{ item.actor }}<span class="table-secondary">{{ formatDate(item.createdAt) }}</span></td><td><button class="table-link" type="button" :disabled="detailLoading" @click="loadDetail(item.id); view = 'findings'">查看结果与证据</button></td></tr></tbody></table></div><p v-else class="empty-state">当前范围暂无历史检查。运行检查后，将保留逐项发现、依据与证据快照。</p></section>
    <p v-else-if="!selected && !historyLoading && !detailLoading" class="empty-state">暂无检查结果。选择范围并运行检查，或打开已有历史结果。</p><p v-if="detailLoading" class="scenario-check-message" role="status">正在读取本次结果与证据…</p>

    <section v-if="actionFinding" class="scenario-check-remediation"><h3 ref="actionHeading" tabindex="-1">申请受控整改 · {{ actionFinding.title }}</h3><p>{{ controlKindLabels[actionFinding.remediation.kind as keyof typeof controlKindLabels] || actionFinding.remediation.kind }} · {{ actionFinding.remediation.reason }}</p><dl v-if="actionFinding.remediation.target" class="scenario-remediation-target"><div v-if="actionFinding.remediation.target.assetId"><dt>拟操作资产</dt><dd>#{{ actionFinding.remediation.target.assetId }}</dd></div><div v-if="actionFinding.remediation.target.operationsSourceId"><dt>来源执行器</dt><dd>#{{ actionFinding.remediation.target.operationsSourceId }}</dd></div><div v-if="actionFinding.remediation.target.role"><dt>账号 / 角色</dt><dd>{{ actionFinding.remediation.target.role }}</dd></div><div v-if="actionFinding.remediation.target.method || actionFinding.remediation.target.route"><dt>请求范围</dt><dd>{{ actionFinding.remediation.target.method }} {{ actionFinding.remediation.target.route }}</dd></div></dl><form @submit.prevent="submitRemediation"><label>整改申请原因<textarea v-model="actionDraft.reason" required maxlength="300" rows="3" :disabled="actionBusy"></textarea></label><label>授权有效期（分钟）<input v-model.number="actionDraft.durationMinutes" type="number" min="1" max="1440" required :disabled="actionBusy"></label><p class="field-hint">仅生成服务端列出的受控处置申请，独立审批后由已登记来源执行；执行回执、新来源复查和回滚分别留存。</p><div class="scenario-check-actions"><button class="button button-secondary" type="button" :disabled="actionBusy" @click="actionFinding = undefined">取消</button><button class="button button-primary" :disabled="actionBusy || !actionDraft.reason.trim()">{{ actionBusy ? '提交中…' : '提交独立审批' }}</button></div></form></section>
  </section>
</template>

<style scoped>
.scenario-checks{min-width:0;padding:20px 22px;background:var(--surface);display:grid;gap:16px}.scenario-check-runbar{display:flex;justify-content:space-between;gap:16px;align-items:center}.scenario-check-runbar h2{font-size:16px;margin:0 0 6px}.scenario-check-runbar p{font-size:12px;color:var(--secondary);line-height:1.8;max-width:64ch}.scenario-check-actions{display:flex;align-items:center;flex-wrap:wrap;gap:8px}.scenario-check-actions .button{font-size:12px;white-space:nowrap}.scenario-check-message{font-size:12px;color:var(--secondary);line-height:1.8;margin:0}.scenario-checks .message{margin:0}.scenario-check-tabs{display:flex;gap:22px;overflow:auto;border-bottom:1px solid var(--line)}.scenario-check-tabs button{flex-shrink:0;min-height:42px;padding:9px 0;border:0;border-bottom:2px solid transparent;background:none;font:inherit;font-size:13px;color:var(--secondary);cursor:pointer}.scenario-check-tabs button.active{color:var(--accent);border-bottom-color:var(--accent);font-weight:650}.scenario-check-result-head{display:flex;justify-content:space-between;align-items:center;gap:12px}.scenario-check-result-head>div:first-child{display:flex;align-items:center;gap:10px;flex-wrap:wrap;min-width:0;font-size:12px}.scenario-check-result-head strong{overflow-wrap:anywhere}.scenario-check-result-head>div>span:last-child{color:var(--secondary)}.scenario-check-counts{display:grid;grid-template-columns:repeat(3,minmax(0,1fr)) minmax(200px,1.8fr);margin:0;border:1px solid var(--line);border-radius:8px;background:var(--surface-subtle)}.scenario-check-counts>div{padding:12px 16px}.scenario-check-counts>div+div{border-left:1px solid var(--line)}.scenario-check-counts dt{font-size:12px;color:var(--secondary)}.scenario-check-counts dd{margin:4px 0 0;font-size:23px;font-weight:650;font-variant-numeric:tabular-nums}.scenario-check-counts>div:first-child dd{color:var(--teal)}.scenario-check-counts>div:nth-child(2) dd{color:var(--danger)}.scenario-check-counts dd.scenario-check-retest{font-size:12px;line-height:1.8;font-weight:600}.scenario-check-projection{font-size:12px;color:var(--secondary);line-height:1.8}.scenario-check-warnings{margin:0;padding:12px 14px 12px 30px;border-radius:6px;background:var(--warning-soft);color:var(--warning);font-size:12px;line-height:1.8}.scenario-check-exportbar{display:flex;align-items:center;gap:10px 16px;flex-wrap:wrap;font-size:12px;padding:12px 0;border-block:1px solid var(--line)}.scenario-check-exportbar>span{font-weight:600;color:var(--secondary)}.scenario-findings-layout{display:grid;grid-template-columns:minmax(0,1fr) 320px;gap:18px;align-items:start}.scenario-findings-main{min-width:0}.scenario-findings-filter{display:flex;align-items:center;justify-content:space-between;gap:12px;font-size:12px;color:var(--secondary);padding:0 0 12px}.scenario-findings-filter label{display:flex;align-items:center;gap:8px}.scenario-checks select,.scenario-checks input,.scenario-checks textarea{min-width:0;max-width:100%;border:1px solid var(--input-line);border-radius:6px;padding:7px 9px;background:var(--surface);font:inherit;font-size:12px;color:var(--ink)}.scenario-findings-table{min-width:590px;table-layout:fixed}.scenario-findings-table th:first-child{width:33%}.scenario-findings-table th:nth-child(2){width:16%}.scenario-findings-table th:nth-child(3){width:38%}.scenario-findings-table th:last-child{width:13%}.scenario-findings-table :is(th,td){overflow-wrap:anywhere;vertical-align:top;font-size:12px}.scenario-findings-table .selected{background:var(--accent-soft)}.scenario-finding-select{padding:0;border:0;background:none;text-align:left;color:var(--ink);font:inherit;font-weight:650;cursor:pointer;overflow-wrap:anywhere}.scenario-finding-select:hover{color:var(--accent)}.scenario-finding-detail{padding:16px;background:var(--surface-subtle);border:1px solid var(--line);border-radius:8px;min-width:0;font-size:12px;line-height:1.8}.scenario-finding-detail h3{font-size:14px;margin:0 0 10px;overflow-wrap:anywhere}.scenario-finding-detail dl{display:grid;margin:12px 0}.scenario-finding-detail dl>div{display:grid;grid-template-columns:62px minmax(0,1fr);gap:10px;padding:10px 0;border-bottom:1px solid var(--line)}.scenario-finding-detail dt{color:var(--secondary)}.scenario-finding-detail dd{margin:0;overflow-wrap:anywhere}.scenario-finding-gaps{color:var(--warning);line-height:1.8}.scenario-finding-gaps h4{font-size:12px}.scenario-finding-gaps ul{padding-left:17px;margin:8px 0}.scenario-finding-evidence{margin-top:14px}.scenario-finding-evidence summary{color:var(--accent);cursor:pointer}.scenario-finding-evidence ul{list-style:none;padding:0;margin:10px 0;display:grid;gap:12px}.scenario-finding-evidence li{display:grid;gap:3px;overflow-wrap:anywhere}.scenario-finding-evidence li span,.scenario-finding-evidence time{color:var(--secondary)}.scenario-finding-next{display:grid;gap:10px;margin-top:16px}.scenario-finding-next .button{font-size:12px;white-space:normal}.scenario-finding-next p{font-size:12px;color:var(--secondary);line-height:1.8}.scenario-check-section-title{display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:10px;margin-bottom:16px}.scenario-check-section-title h3{font-size:15px}.scenario-check-section-title>span,.scenario-check-section-title>p{font-size:12px;color:var(--secondary);line-height:1.8}.scenario-check-evidence>article,.scenario-report-requirements>article{padding:18px 0;border-top:1px solid var(--line);font-size:12px;line-height:1.8;min-width:0}.scenario-check-evidence>article>div:first-child,.scenario-report-heading{display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:10px}.scenario-check-evidence dl,.scenario-report-requirements dl{display:grid;margin:12px 0}.scenario-check-evidence dl>div,.scenario-report-requirements dl>div{display:grid;grid-template-columns:78px minmax(0,1fr);gap:12px;padding:8px 0}.scenario-check-evidence dt,.scenario-report-requirements dt{color:var(--secondary)}.scenario-check-evidence dd,.scenario-report-requirements dd{margin:0;overflow-wrap:anywhere}.scenario-check-evidence summary{cursor:pointer;color:var(--accent)}.scenario-check-evidence pre,.scenario-check-remediation pre{max-height:340px;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;font-size:12px;padding:12px;background:var(--surface-subtle);border-radius:6px}.scenario-report-requirements h4{font-size:14px}.scenario-report-requirements h5{font-size:12px;margin:14px 0 6px}.scenario-report-requirements ul{padding-left:18px}.scenario-report-requirements dd a{margin-left:8px}.scenario-check-report-note{margin-top:16px;padding:14px;background:var(--surface-subtle);border-radius:6px;font-size:12px;color:var(--secondary);line-height:1.8}.scenario-check-report-note p+p{margin-top:8px}.scenario-check-history table{min-width:710px;font-size:12px;table-layout:fixed}.scenario-check-history :is(th,td){overflow-wrap:anywhere;vertical-align:top}.scenario-check-history th:first-child{width:25%}.scenario-check-history th:nth-child(2){width:26%}.scenario-check-history th:nth-child(3){width:23%}.scenario-check-history th:nth-child(4){width:17%}.scenario-check-history th:last-child{width:13%}.scenario-check-remediation{padding:18px;border:1px solid var(--line);border-radius:8px;background:var(--surface-subtle);font-size:12px}.scenario-check-remediation h3{font-size:14px;margin-bottom:8px}.scenario-check-remediation form{display:grid;gap:14px;margin-top:16px}.scenario-check-remediation label{display:grid;gap:6px;color:var(--secondary)}.scenario-check-remediation textarea{width:100%;resize:vertical}.scenario-check-remediation input{max-width:170px}.scenario-check-remediation .field-hint{font-size:12px;line-height:1.8}.scenario-checks button.table-link{border:0;background:none;color:var(--accent);padding:0;font:inherit;font-size:12px;cursor:pointer;text-align:left}.scenario-checks .table-secondary{font-size:12px}
@media(max-width:1399px){.scenario-findings-layout{grid-template-columns:minmax(0,1fr) 280px}.scenario-check-runbar{align-items:flex-start}.scenario-check-runbar>.scenario-check-actions{justify-content:flex-end}}
@media(max-width:1099px){.scenario-findings-layout{grid-template-columns:minmax(0,1fr)}.scenario-finding-detail dl{grid-template-columns:repeat(2,minmax(0,1fr));gap:0 16px}.scenario-check-result-head{align-items:flex-start}}
@media(max-width:767px){.scenario-checks{padding:16px}.scenario-check-runbar,.scenario-check-result-head{flex-direction:column}.scenario-check-runbar>.scenario-check-actions{justify-content:flex-start}.scenario-check-counts{grid-template-columns:repeat(3,minmax(0,1fr))}.scenario-check-counts>div:last-child{grid-column:1/-1;border-left:0;border-top:1px solid var(--line)}.scenario-finding-detail dl{grid-template-columns:minmax(0,1fr)}.scenario-check-tabs{gap:18px}}
</style>

<style scoped>.scenario-remediation-target{display:flex;flex-wrap:wrap;gap:14px 24px;margin:16px 0}.scenario-remediation-target>div{display:grid;gap:5px}.scenario-remediation-target dt{color:var(--secondary)}.scenario-remediation-target dd{margin:0;overflow-wrap:anywhere}</style>
