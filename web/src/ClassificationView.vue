<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import {
  ApiError, assetBusinessLevelLabels, assetCategoryLabels, describeError, documentationUrl, formatDate, providerLabel, request,
  workspacePath, type Workspace,
} from './api'
import { postgresSensitivityLabel } from './postgresql'
import { classificationOriginLabels, isReferencePublication, standardKindLabels, standardsByIndustry, type ClassificationStandards, type ClassificationStandardSnapshot, type ClassificationRecord } from './classification-standards'
import { automationRunDetail, type AutomationJob } from './automation'
import ClassificationEvaluation from './ClassificationEvaluation.vue'
import Icon from './Icon.vue'
import ClassificationWorkbench from './ClassificationWorkbench.vue'
import { useScenarioContext } from './scenario-context'
import { classificationCandidateMethodLabel, taxonomyPath, type ClassificationCandidate } from './classification-workbench'

interface Classification extends ClassificationRecord {
  candidates: ClassificationCandidate[]; candidateCount: number
  suggestedCategory?: string; suggestedBusinessLevel?: string
  standard?: ClassificationStandardSnapshot
}
interface Values { category: string; businessLevel: string; statutoryCategory: string; basis: string }
interface History extends Values {
  id: number; revision: number; actor: string; createdAt: string
  previousCategory: string; previousBusinessLevel: string; previousStatutoryCategory: string; previousBasis: string
  standard?: ClassificationStandardSnapshot
  origin?: 'manual' | 'automatic'
  taxonomy?: ClassificationRecord['taxonomy']
}
interface Detail extends Classification { history: History[]; historyTruncated?: boolean }

const props = defineProps<{ workspace: Workspace; assetId?: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const { context: scenarioContext, href: link } = useScenarioContext(props.workspace.slug)
const connectedScenarios = [{ id: 'personal', label: '个人信息保护' }, { id: 'domestic', label: '国内合规与迎检' }, { id: 'crossborder', label: '数据跨境合规' }]

const classificationPath = workspacePath(props.workspace.slug) + '/classification'
const apiPath = classificationPath + '/assets'
const detailPath = props.assetId ? apiPath + '/' + encodeURIComponent(props.assetId) : ''
const canEdit = computed(() => props.workspace.role === 'owner' || props.workspace.role === 'operator')
const active = new AbortController()
const detail = ref<Detail | null>(null)
const loading = ref(false)
const saving = ref(false)
const conflict = ref(false)
const loadError = ref('')
const saveError = ref('')
const notice = ref('')
const collecting = ref(false)
const collectionError = ref('')
const collectionNotice = ref('')
let collectionTimer: number | undefined
const standardCatalog = ref<ClassificationStandards>()
const standardsLoading = ref(false)
const standardsError = ref('')
const standardId = ref('')
const levelCode = ref('')
const ruleId = ref('')
const draft = reactive<Values>({ category: 'unknown', businessLevel: 'unknown', statutoryCategory: 'unknown', basis: '' })
const categories: Record<string, string> = { unknown: '待判定', ...assetCategoryLabels }
const levels: Record<string, string> = { unknown: '待判定', ...assetBusinessLevelLabels }
const statutory: Record<string, string> = { unknown: '待认定', general: '一般数据', important: '重要数据', core: '核心数据' }
const locations: Record<string, string> = { parameter: '参数', request: '请求体', response: '响应体' }
const standardOptions = computed(() => standardCatalog.value?.items.filter(item => item.status === 'effective' || isReferencePublication(item)) ?? [])
const standardOptionGroups = computed(() => standardsByIndustry(standardOptions.value))
const selectedStandard = computed(() => standardOptions.value.find(item => item.id === standardId.value))
const selectedLevel = computed(() => selectedStandard.value?.levels?.find(level => level.code === levelCode.value))
const ruleOptions = computed(() => (selectedStandard.value?.rules ?? []).filter(rule =>
  !rule.levelCodes?.length || rule.levelCodes.includes(levelCode.value),
))
const selectedRule = computed(() => ruleOptions.value.find(rule => rule.id === ruleId.value))
const historicalStandard = computed(() => detail.value?.standard?.id === standardId.value && !selectedStandard.value
  ? detail.value?.standard
  : undefined)
const statutorySelection = computed({
  get: () => selectedLevel.value?.statutoryCategory ?? draft.statutoryCategory,
  set: (value: string) => { draft.statutoryCategory = value },
})
const validStandard = computed(() => selectedStandard.value &&
  (!selectedStandard.value.levels?.length || selectedLevel.value || !levelCode.value && statutorySelection.value === 'unknown') &&
  (!selectedStandard.value.rules?.length || selectedRule.value),
)

function errorText(error: unknown) {
  if (active.signal.aborted) return ''
  if (error instanceof ApiError && error.status === 401) {
    emit('sessionExpired')
    return ''
  }
  return describeError(error)
}

function setDetail(value: Detail) {
  detail.value = value
  standardId.value = value.standard?.id ?? standardCatalog.value?.defaultStandardId ?? ''
  levelCode.value = value.standard?.levelCode ?? ''
  ruleId.value = value.standard?.ruleId ?? ''
  Object.assign(draft, {
    category: value.asset.category || 'unknown',
    businessLevel: value.asset.businessLevel || 'unknown',
    statutoryCategory: value.asset.statutoryCategory || 'unknown',
    basis: value.asset.classificationBasis || '',
  })
}

async function loadStandards() {
  standardsLoading.value = true
  standardsError.value = ''
  try {
    const catalog = await request<ClassificationStandards>(classificationPath + '/standards', { signal: active.signal })
    standardCatalog.value = catalog
    if (!standardId.value) standardId.value = catalog.defaultStandardId
  } catch (error) {
    standardsError.value = errorText(error)
  } finally {
    standardsLoading.value = false
  }
}

function changeStandard() {
  levelCode.value = ''
  ruleId.value = ''
}

function changeLevel() {
  if (!selectedRule.value) ruleId.value = ''
}

function trackCollection(jobId: number, previousRunId: string, startedAt: number) {
  collectionTimer = window.setTimeout(async () => {
    if (active.signal.aborted) return
    try {
      const page = await request<{ items: AutomationJob[] }>(workspacePath(props.workspace.slug) + '/automation/jobs', { signal: active.signal })
      const job = page.items.find(item => item.id === jobId)
      if (!job || job.revokedAt) {
        collecting.value = false
        collectionError.value = '采集任务已撤销或不可访问，请在自动采集中检查任务。'
        return
      }
      if (job.latestRun && job.latestRun.runId !== previousRunId && !job.managed?.running && !job.managed?.queued) {
        collecting.value = false
        if (job.latestRun.status === 'error') collectionError.value = automationRunDetail(job.latestRun, true)
        await load()
        if (!collectionError.value) collectionNotice.value = '本次采集已完成，分类记录已刷新。'
        return
      }
      if (Date.now() - startedAt < 45_000) trackCollection(jobId, previousRunId, startedAt)
      else {
        collecting.value = false
        collectionNotice.value = '采集仍在处理中，可在自动采集页面查看进度。'
      }
    } catch (error) {
      collecting.value = false
      collectionError.value = errorText(error)
    }
  }, 1000)
}

async function collect() {
  const automatic = detail.value?.automatic
  if (props.workspace.role !== 'owner' || !automatic || collecting.value || loading.value || saving.value) return
  collecting.value = true
  collectionError.value = ''
  collectionNotice.value = ''
  notice.value = ''
  try {
    await request(workspacePath(props.workspace.slug) + '/automation/managed/jobs/' + automatic.jobId + '/run', {
      method: 'POST', signal: active.signal,
    })
    if (!active.signal.aborted) trackCollection(automatic.jobId, automatic.runId, Date.now())
  } catch (error) {
    collecting.value = false
    collectionError.value = errorText(error)
  }
}

async function load() {
  if (!detailPath || loading.value || saving.value) return
  loading.value = true
  loadError.value = ''
  try {
    setDetail(await request<Detail>(detailPath, { signal: active.signal }))
    saveError.value = ''
    conflict.value = false
  } catch (error) {
    loadError.value = errorText(error)
  } finally {
    loading.value = false
  }
}

function fillSuggestions() {
  if (!detail.value) return
  if (detail.value.suggestedCategory) draft.category = detail.value.suggestedCategory
  if (detail.value.suggestedBusinessLevel) draft.businessLevel = detail.value.suggestedBusinessLevel
  notice.value = '已填入识别建议。请核对分类并补充判定依据。'
}

async function save() {
  if (!detail.value || !canEdit.value || loading.value || saving.value || collecting.value || conflict.value) return
  saveError.value = ''
  notice.value = ''
  if (!validStandard.value) {
    saveError.value = '请选择标准或参考资料，并填写适用的标准等级和规则。'
    return
  }
  const basis = draft.basis.trim()
  if (!basis || basis.length > 400) {
    saveError.value = '请填写 1 到 400 字的分类判定依据。'
    return
  }
  saving.value = true
  try {
    const updated = await request<Detail>(detailPath, {
      method: 'PUT',
      body: JSON.stringify({
        revision: detail.value.revision, ...draft, basis, statutoryCategory: statutorySelection.value,
        standardId: standardId.value,
        levelCode: selectedLevel.value?.code,
        ruleId: selectedRule.value?.id,
      }),
      signal: active.signal,
    })
    setDetail(updated)
    notice.value = '分类已确认，变更已登记到历史和平台审计。'
  } catch (error) {
    if (error instanceof ApiError && error.status === 409) {
      conflict.value = true
      saveError.value = '此资产已有更新。读取最新版本后可重新确认。'
    } else saveError.value = errorText(error)
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  if (detailPath) void load()
  if (detailPath && canEdit.value) void loadStandards()
})
onBeforeUnmount(() => {
  window.clearTimeout(collectionTimer)
  active.abort()
})
</script>

<template>
  <div class="classification-view">
    <a v-if="assetId" class="back-link" :href="link('classification')"><Icon name="back" />返回分类台账</a>
    <header class="page-header">
      <div>
        <p class="eyebrow">数据分类分级</p>
        <h1>{{ assetId ? '分类确认与判定依据' : '分类分级' }}</h1>
        <p>{{ assetId ? '核对自动识别线索，记录分类决定和判定依据。' : '查看采集器的字段识别线索、已确认分类和变更历史。' }}</p>
      </div>
      <div class="page-actions">
        <button v-if="assetId && detail" type="button" class="button button-secondary" :disabled="loading || saving || collecting" @click="load()">刷新结果</button>
        <a class="button button-secondary" :href="link('classification/standards')">标准库</a>
        <a v-if="assetId && detail?.asset.sourceProvider === 'postgresql'" class="button button-secondary" :href="link('assets/' + assetId)">查看字段与权限</a>
        <a v-if="!assetId" class="button button-secondary" :href="link('assets')">查看数据资产</a>
      </div>
    </header>
    <div v-if="assetId && detail" class="classification-scene-actions"><span>按本资产进入场景检查</span><a v-for="scene in connectedScenarios" :key="scene.id" class="table-link" :href="link('scenarios/' + scene.id, { assetId: String(detail.asset.id), activityId: scenarioContext.assetId === String(detail.asset.id) ? scenarioContext.activityId : '' })">{{ scene.label }}<Icon name="chevron"/></a></div>
    <p v-if="loadError" class="message message-error page-feedback" role="alert">{{ loadError }}</p>
    <p v-if="assetId && loading && !detail" class="empty-state" role="status">正在读取资产分类…</p>
    <button v-if="assetId && loadError" type="button" class="button button-secondary" :disabled="loading" @click="load()">重新读取</button>
    <p v-if="collectionError" class="message message-error page-feedback" role="alert">{{ collectionError }}</p>
    <p v-if="collectionNotice" class="message message-success page-feedback" role="status">{{ collectionNotice }}</p>

    <ClassificationWorkbench v-if="!assetId" :workspace="workspace" @session-expired="emit('sessionExpired')" />

    <div v-else-if="detail" class="classification-detail">
      <div class="classification-detail-main section-stack"><div class="scope-strip classification-asset-scope"><div><span class="eyebrow">当前资产</span><strong>{{ detail.asset.name }}</strong><span>{{ detail.asset.system }} · 分类修订 v{{ detail.revision }}</span></div><span class="badge status-draft">{{ detail.automatic ? '有自动判定快照' : '暂无自动判定快照' }}</span></div>
<section class="panel" aria-labelledby="classification-current-title">
        <div class="panel-heading">
          <div><h2 id="classification-current-title">当前分类</h2><p>{{ detail.asset.system }}<template v-if="detail.asset.sourceProvider"> · {{ providerLabel(detail.asset.sourceProvider) }}</template></p></div>
          <span class="badge" :class="!detail.revision ? 'status-draft' : detail.origin === 'automatic' ? 'status-in_review' : 'status-verified'">
            {{ detail.revision ? classificationOriginLabels[detail.origin || 'manual'] + ' · 版本 ' + detail.revision : '待确认' }}
          </span>
        </div>
        <dl class="detail-list">
          <div><dt>数据属性</dt><dd>{{ categories[detail.asset.category || 'unknown'] }}</dd></div>
          <div><dt>企业内部敏感级别</dt><dd>{{ levels[detail.asset.businessLevel || 'unknown'] }}</dd></div>
          <div><dt>法定类别</dt><dd>{{ statutory[detail.asset.statutoryCategory || 'unknown'] }}</dd></div>
          <div v-if="detail.standard"><dt>采用标准</dt><dd>
            <a v-if="documentationUrl(detail.standard.url)" class="table-link" :href="documentationUrl(detail.standard.url)"
               target="_blank" rel="noopener noreferrer">{{ detail.standard.code }} · {{ detail.standard.title }}</a>
            <span v-else>{{ detail.standard.code }} · {{ detail.standard.title }}</span>
            <span class="table-secondary">{{ detail.standard.industry }} · {{ standardKindLabels[detail.standard.kind] }}</span>
          </dd></div>
          <div v-if="detail.standard?.levelLabel"><dt>标准等级</dt><dd>{{ detail.standard.levelLabel }}</dd></div>
          <div v-if="detail.standard?.ruleTitle"><dt>采用规则</dt><dd>{{ detail.standard.ruleClause }} · {{ detail.standard.ruleTitle }}</dd></div>
          <div v-if="detail.taxonomy" class="detail-wide"><dt>采用业务分类</dt><dd>{{ taxonomyPath(detail.taxonomy) }} · 目录 v{{ detail.taxonomy.revision }}<span v-if="detail.taxonomyStale" class="table-secondary">目录已变更，需核对采用版本。</span></dd></div>
          <div class="detail-wide"><dt>判定依据</dt><dd class="text-block">{{ detail.asset.classificationBasis || '—' }}</dd></div>
        </dl>
        <details v-if="detail.standard?.ruleCriterion || detail.standard?.ruleEvidence" class="classification-current-rule"><summary>查看采用规则的判定条件与取证要求</summary><dl class="detail-list">          <div v-if="detail.standard?.ruleCriterion" class="detail-wide"><dt>规则判定条件</dt><dd class="text-block">{{ detail.standard.ruleCriterion }}</dd></div>
          <div v-if="detail.standard?.ruleEvidence" class="detail-wide"><dt>规则取证要求</dt><dd class="text-block">{{ detail.standard.ruleEvidence }}</dd></div>
</dl></details>
        <p v-if="!detail.revision" class="field-hint classification-note">已有分类值可来自资产历史记录。完成本页确认后，平台开始记录分类版本。</p>
      </section>
<section class="panel" aria-labelledby="classification-automatic-title">
        <div class="panel-heading">
          <div><h2 id="classification-automatic-title">自动判定快照</h2><p>查看本次采集事实与标准条款的对应结果。</p></div>
          <div class="page-actions">
            <button v-if="workspace.role === 'owner' && detail.automatic" type="button" class="button button-secondary"
                    :disabled="loading || saving || collecting" @click="collect">{{ collecting ? '正在采集…' : '重新采集' }}</button>
            <a v-if="detail.asset.sourceProvider === 'postgresql'" class="button button-secondary" :href="home + '/automation'">查看采集任务</a>
          </div>
        </div>
        <div v-if="detail.automatic" class="classification-automatic-body"><ClassificationEvaluation :automatic="detail.automatic" /></div>
        <p v-else class="empty-state">暂无自动判定快照。支持的来源完成采集后，这里会显示事实、条款结果和待补条件。</p>
      </section>
<section class="panel" aria-labelledby="classification-candidates-title">
        <div class="panel-heading">
          <div>
            <h2 id="classification-candidates-title">自动识别线索</h2>
            <p>展示已采集数据库、目录及 OpenAPI 的识别线索，按实际来源保留规则、范围和批次。当前共有 {{ detail.candidateCount }} 项线索。</p>
          </div>
        </div>
        <dl v-if="detail.suggestedCategory || detail.suggestedBusinessLevel" class="detail-list">
          <div v-if="detail.suggestedCategory"><dt>建议数据属性</dt><dd>{{ categories[detail.suggestedCategory] }}</dd></div>
          <div v-if="detail.suggestedBusinessLevel"><dt>建议内部敏感级别</dt><dd>{{ levels[detail.suggestedBusinessLevel] }}</dd></div>
        </dl>
        <p v-if="detail.candidateCount > detail.candidates.length" class="field-hint classification-note">当前展示 {{ detail.candidates.length }} 项，可到对应来源核对其余线索。</p>
        <div v-if="detail.candidates.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="classification-candidates-title">
          <table class="data-table classification-candidates">
            <thead><tr><th scope="col">字段或参数</th><th scope="col">识别线索</th><th scope="col">来源</th><th scope="col">观测时间</th></tr></thead>
            <tbody>
              <tr v-for="(candidate, index) in detail.candidates" :key="index">
                <th scope="row" class="mono">{{ candidate.name }}</th>
                <td>{{ postgresSensitivityLabel(candidate.category) }}
                  <span v-if="candidate.ruleKey" class="table-secondary">{{ candidate.ruleKey }}<template v-if="candidate.ruleVersion != null"> · v{{ candidate.ruleVersion }}</template></span>
                  <span v-if="candidate.method" class="table-secondary">{{ classificationCandidateMethodLabel(candidate.method) }}</span>
                  <span v-if="candidate.matchCount != null || candidate.scannedCount != null" class="table-secondary">命中 {{ candidate.matchCount ?? '—' }} / 已观察 {{ candidate.scannedCount ?? '—' }}</span>
                  <span v-if="candidate.current === false" class="table-secondary">历史线索，需重新采集</span>
                </td>
                <td>
                  <a v-if="candidate.scanId" class="table-link" :href="link('api-security/' + candidate.scanId)">OpenAPI 扫描 #{{ candidate.scanId }}</a>
                  <span v-else>{{ providerLabel(candidate.source) }}</span>
                  <span v-if="candidate.sourceId != null" class="table-secondary">来源 #{{ candidate.sourceId }}</span>
                  <span v-if="candidate.location" class="table-secondary">{{ locations[candidate.location] || candidate.location }}</span>
                  <span v-if="candidate.runId" class="table-secondary mono">{{ candidate.runId }}</span>
                </td>
                <td><time :datetime="candidate.observedAt">{{ formatDate(candidate.observedAt) }}</time></td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">此资产暂无自动识别线索，可结合实际业务数据和分类依据确认。</p>
      </section>
<section class="panel" aria-labelledby="classification-history-title">
        <div class="panel-heading"><div><h2 id="classification-history-title">分类变更历史</h2><p>按版本追溯经办人和判定依据。</p></div></div>
        <p v-if="detail.historyTruncated" class="field-hint classification-note">这里展示最近的分类版本。</p>
        <ol v-if="detail.history.length" class="timeline">
          <li v-for="entry in detail.history" :key="entry.id">
            <strong>版本 {{ entry.revision }}</strong>
            <span class="badge" :class="entry.origin === 'automatic' ? 'status-in_review' : 'status-verified'">{{ classificationOriginLabels[entry.origin || 'manual'] }}</span>
            <div class="event-meta">{{ entry.actor }} · <time :datetime="entry.createdAt">{{ formatDate(entry.createdAt) }}</time></div>
            <p>{{ categories[entry.category] }} · {{ levels[entry.businessLevel] }} · {{ statutory[entry.statutoryCategory] }}</p>
            <p v-if="entry.standard" class="classification-history-standard">
              <a v-if="documentationUrl(entry.standard.url)" class="table-link" :href="documentationUrl(entry.standard.url)"
                 target="_blank" rel="noopener noreferrer">{{ entry.standard.code }} · {{ entry.standard.title }}</a>
              <span v-else>{{ entry.standard.code }} · {{ entry.standard.title }}</span>
              <span v-if="entry.standard.levelLabel"> · {{ entry.standard.levelLabel }}</span>
              <span v-if="entry.standard.ruleTitle"> · {{ entry.standard.ruleClause }} {{ entry.standard.ruleTitle }}</span>
            </p>
            <details v-if="entry.standard?.ruleCriterion || entry.standard?.ruleEvidence" class="classification-history-rule">
              <summary class="table-link">查看当时采用的规则内容</summary>
              <dl>
                <div v-if="entry.standard.ruleCriterion"><dt>判定条件</dt><dd class="text-block">{{ entry.standard.ruleCriterion }}</dd></div>
                <div v-if="entry.standard.ruleEvidence"><dt>取证要求</dt><dd class="text-block">{{ entry.standard.ruleEvidence }}</dd></div>
              </dl>
            </details>
            <p v-if="entry.taxonomy" class="classification-history-standard">采用业务分类：{{ taxonomyPath(entry.taxonomy) }} · 目录 v{{ entry.taxonomy.revision }}</p>
            <p class="text-block">{{ entry.basis }}</p>
            <details v-if="entry.previousCategory || entry.previousBusinessLevel || entry.previousStatutoryCategory || entry.previousBasis">
              <summary class="table-link">查看变更前记录</summary>
              <p>{{ categories[entry.previousCategory || 'unknown'] }} · {{ levels[entry.previousBusinessLevel || 'unknown'] }} · {{ statutory[entry.previousStatutoryCategory || 'unknown'] }}</p>
              <p class="text-block">{{ entry.previousBasis || '—' }}</p>
            </details>
          </li>
        </ol>
        <p v-else class="empty-state">尚未登记分类确认版本。</p>
      </section></div><aside class="context-rail classification-confirm-rail"><section v-if="canEdit" class="panel classification-confirm-panel" aria-labelledby="classification-form-title">
        <div class="panel-heading">
          <div><h2 id="classification-form-title">确认分类</h2><p>依据组织采用的分类规则填写。保存后会记录经办人、依据和版本。</p></div>
          <button v-if="detail.suggestedCategory || detail.suggestedBusinessLevel" type="button" class="button button-secondary"
                  :disabled="loading || saving || collecting || conflict" @click="fillSuggestions">填入识别建议</button>
        </div>
        <form class="classification-form" @submit.prevent="save">
          <p v-if="standardsLoading" class="field-hint" role="status">正在读取可选标准…</p>
          <div v-if="standardsError" class="message message-error classification-feedback">
            <p role="alert">{{ standardsError }}</p>
            <button type="button" class="button button-secondary" @click="loadStandards">重新读取标准</button>
          </div>
          <fieldset v-if="standardCatalog" class="classification-fields classification-standard-fields"
                    :disabled="loading || saving || collecting || conflict || standardsLoading">
            <div class="field">
              <label for="classification-standard">采用标准</label>
              <select id="classification-standard" v-model="standardId" required @change="changeStandard">
                <option value="" disabled>请选择标准或参考资料</option>
                <option v-if="historicalStandard" :value="historicalStandard.id" disabled>{{ historicalStandard.code }} · 历史版本</option>
                <optgroup v-for="[industry, standards] in standardOptionGroups" :key="industry" :label="industry">
                  <option v-for="standard in standards" :key="standard.id" :value="standard.id">
                    {{ standard.code }} · {{ standard.title }}{{ standard.id === standardCatalog.defaultStandardId ? '（工作区默认）' : '' }}{{ isReferencePublication(standard) ? '（发布资料，仅供参考）' : '' }}
                  </option>
                </optgroup>
              </select>
              <p v-if="historicalStandard" class="field-hint">确认新版本时请选择现行标准或可选参考资料。</p>
            </div>
            <div v-if="selectedStandard?.levels?.length" class="field">
              <label for="classification-standard-level">标准等级</label>
              <select id="classification-standard-level" v-model="levelCode" @change="changeLevel">
                <option value="">等级待评定（先确认数据属性）</option>
                <option v-for="level in selectedStandard.levels" :key="level.code" :value="level.code">{{ level.label }}</option>
              </select>
              <p v-if="!selectedLevel" class="field-hint">等级待评定时，法定类别请选择待认定。</p>
            </div>
            <div v-if="selectedStandard?.rules?.length" class="field">
              <label for="classification-standard-rule">采用规则</label>
              <select id="classification-standard-rule" v-model="ruleId" required>
                <option value="" disabled>请选择适用规则</option>
                <option v-for="rule in ruleOptions" :key="rule.id" :value="rule.id">{{ rule.clause }} · {{ rule.title }}</option>
              </select>
            </div>
          </fieldset>
          <dl v-if="selectedRule" class="classification-rule-preview">
            <div><dt>判定要点</dt><dd>{{ selectedRule.criterion }}</dd></div>
            <div v-if="selectedRule.evidence"><dt>取证要求</dt><dd>{{ selectedRule.evidence }}</dd></div>
          </dl>
          <details v-if="selectedStandard" class="classification-standard-scope">
            <summary>{{ selectedStandard.levels?.length || selectedStandard.rules?.length ? '标准适用范围' : '参考标准适用范围' }}</summary>
            <p>{{ selectedStandard.scope }}</p>
            <a v-if="documentationUrl(selectedStandard.url)" class="table-link" :href="documentationUrl(selectedStandard.url)"
               target="_blank" rel="noopener noreferrer">查看官方来源</a>
          </details>
          <fieldset class="classification-fields" :disabled="loading || saving || collecting || conflict">
            <div class="field">
              <label for="classification-category">数据属性</label>
              <select id="classification-category" v-model="draft.category"><option v-for="(label, value) in categories" :key="value" :value="value">{{ label }}</option></select>
            </div>
            <div class="field">
              <label for="classification-level">企业内部敏感级别</label>
              <select id="classification-level" v-model="draft.businessLevel"><option v-for="(label, value) in levels" :key="value" :value="value">{{ label }}</option></select>
            </div>
            <div class="field">
              <label for="classification-statutory">法定类别</label>
              <select id="classification-statutory" v-model="statutorySelection" :disabled="Boolean(selectedLevel?.statutoryCategory)">
                <option v-for="(label, value) in statutory" :key="value" :value="value">{{ label }}</option>
              </select>
              <p v-if="selectedLevel?.statutoryCategory" class="field-hint">由所选标准等级确定。</p>
            </div>
          </fieldset>
          <div class="field">
            <label for="classification-basis">判定依据</label>
            <textarea id="classification-basis" v-model="draft.basis" required maxlength="400" :disabled="loading || saving || collecting || conflict" aria-describedby="classification-basis-hint"></textarea>
            <p id="classification-basis-hint" class="field-hint">填写规则编号、审批记录或脱敏判定摘要。</p>
          </div>
          <p v-if="notice" class="message message-success classification-feedback" role="status">{{ notice }}</p>
          <p v-if="saveError" class="message message-error classification-feedback" role="alert">{{ saveError }}</p>
          <div class="form-actions">
            <button type="submit" class="button button-primary" :disabled="loading || saving || collecting || conflict || !validStandard">{{ saving ? '正在保存…' : '保存分类' }}</button>
            <button v-if="conflict" type="button" class="button button-secondary" :disabled="loading" @click="load()">读取最新版本</button>
          </div>
        </form>
      </section><section v-if="!canEdit" class="panel classification-explainer"><h2>分类确认</h2><p>当前角色可查看判定依据和变更历史。所有者或操作员可保存分类确认。</p></section><section class="panel classification-explainer"><h3>版本与取证</h3><p>每次确认保留采用标准、条款、依据及经办人。重新采集形成新的来源快照，历史确认内容保持可追溯。</p><a class="table-link" :href="link('classification/standards')">查看标准库 <Icon name="chevron" /></a></section></aside>
    </div>
  </div>
</template>

<style scoped>
.classification-counts { margin-bottom: 1rem; }
.classification-counts div { border-bottom: 0; }
.classification-filter select { min-width: 0; }
.classification-detail { display:grid;grid-template-columns:minmax(0,1fr) 320px;align-items:start;gap:16px; }
.classification-detail-main,.classification-confirm-rail{min-width:0}.classification-list-workbench{display:grid;grid-template-columns:minmax(0,1fr) 290px;gap:16px;align-items:start}.classification-list-workbench>*{min-width:0}.classification-list-context .detail-list{grid-template-columns:minmax(0,1fr)}.classification-asset-name{border:0;background:none;padding:0;text-align:left;color:var(--accent);font:inherit;font-weight:650;cursor:pointer;overflow-wrap:anywhere}.classification-row-selected{background:var(--accent-soft)}.classification-row-selected>th{box-shadow:inset 1px 0 var(--accent)}.classification-list-context dd{overflow-wrap:anywhere}.classification-rail-actions{padding:0 20px 20px;display:flex;flex-direction:column}.classification-explainer{padding:20px}.classification-explainer h3,.classification-missing-summary h3{font-size:14px}.classification-explainer p{margin:10px 0;color:var(--secondary);font-size:12px;line-height:1.75}.classification-explainer .table-link{display:inline-flex;gap:8px;align-items:center}.classification-missing-summary{padding:0 20px 20px;color:var(--secondary);font-size:12px}.classification-missing-summary ul{padding-left:18px;margin-top:8px}.classification-asset-scope{padding:16px 20px;display:flex;align-items:center;justify-content:space-between;gap:12px}.classification-asset-scope>div{display:grid;gap:4px;min-width:0}.classification-asset-scope>div>span:last-child{font-size:12px;color:var(--secondary)}.classification-asset-scope strong{overflow-wrap:anywhere}.classification-confirm-panel .panel-heading{align-items:flex-start;flex-direction:column}.classification-confirm-panel .classification-fields{grid-template-columns:minmax(0,1fr)}.classification-confirm-panel .classification-fields .field+.field{margin:0}.classification-confirm-panel .classification-fields{margin-bottom:14px}.classification-confirm-panel .form-actions{align-items:stretch;flex-direction:column}.classification-confirm-panel select{font-size:12px}.classification-confirm-panel textarea{min-height:120px}.classification-confirm-panel .panel-heading p{line-height:1.7}.classification-confirm-panel .classification-rule-preview{font-size:12px;line-height:1.7}.classification-confirm-rail{position:sticky;top:84px}.classification-detail .detail-list{grid-template-columns:repeat(3,minmax(0,1fr))}@media(max-width:1399px){.classification-detail{grid-template-columns:minmax(0,1fr) 300px}.classification-list-workbench{grid-template-columns:minmax(0,1fr) 270px}.classification-detail .detail-list{grid-template-columns:repeat(2,minmax(0,1fr))}}
.classification-automatic-body { padding: .85rem 1.1rem; }
.classification-note { margin: .7rem 1.1rem; }
.classification-table { min-width: 850px; table-layout:fixed; }.classification-table th:first-child{width:26%}.classification-table th:nth-child(2){width:19%}.classification-table th:nth-child(3),.classification-table th:nth-child(4){width:12%}.classification-table th:nth-child(5){width:18%}.classification-table th:nth-child(6){width:13%}
.classification-candidates { min-width: 33rem; }
.classification-table :is(th, td), .classification-candidates :is(th, td), .classification-view .text-block { overflow-wrap: anywhere; }
.classification-form { padding: .9rem 1.1rem 1.1rem; }
.classification-fields { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: .75rem; min-width: 0; margin: 0; padding: 0; border: 0; }
.classification-fields .field { min-width: 0; margin: 0; }
.classification-standard-fields { margin-bottom: .8rem; }
.classification-rule-preview { margin-bottom: .8rem; padding: .55rem .75rem; border-radius: .4rem; background: var(--surface-subtle); }
.classification-rule-preview > div + div { margin-top: .45rem; }
.classification-rule-preview dt { font-weight: 700; }
.classification-standard-scope { margin-bottom: .8rem; color: var(--secondary); font-size: .84rem; }
.classification-standard-scope summary { color: var(--accent); font-weight: 700; }
.classification-standard-scope p { margin-block: .25rem; }
.classification-history-standard { margin-top: .35rem; }
.classification-history-rule { margin-top: .4rem; }
.classification-history-rule dl { padding: .4rem .65rem; border-radius: .35rem; background: var(--surface-subtle); }
.classification-history-rule dt { font-weight: 700; }
.classification-history-rule dl > div + div { margin-top: .4rem; }
.classification-feedback { margin-top: .75rem; }
.classification-detail summary { cursor: pointer; }
@media (max-width: 43.99rem) {
  .classification-fields { grid-template-columns: minmax(0, 1fr); }
}
.classification-list-context,.classification-confirm-rail{display:grid;gap:16px}.classification-list-workbench>section>.table-scroll{max-height:calc(100vh - 450px);min-height:300px;overflow:auto}.classification-list-workbench .data-table th:first-child{max-width:220px}.classification-list-workbench .classification-asset-name{max-width:220px}.classification-detail-main>.scope-strip{border:1px solid var(--line);border-radius:12px;background:var(--surface)}
.classification-current-rule{margin:0 18px 18px}.classification-current-rule>summary{cursor:pointer;color:var(--accent);font-size:12px}.classification-current-rule .detail-list{padding:14px 0 0;border:0}
</style>

<style scoped>.classification-scene-actions{display:flex;gap:16px;align-items:center;flex-wrap:wrap;padding:13px 16px;border:1px solid var(--line);border-radius:8px;background:var(--surface);font-size:12px;margin-bottom:16px}.classification-scene-actions>span{color:var(--secondary)}.classification-scene-actions a{display:inline-flex;align-items:center;gap:6px}.classification-scene-actions .icon{width:14px;height:14px}</style>
