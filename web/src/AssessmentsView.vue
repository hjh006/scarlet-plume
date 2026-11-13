<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import ScenarioDataPanel from './ScenarioDataPanel.vue'
import { useScenarioContext } from './scenario-context'
import type { Activity, ActivityPage } from './activity'
import { checkStatusLabels } from './workflow'
import { assessmentFactLabels, assessmentFactDisplayValue, assessmentFactSourceLabel, assessmentFactStateLabel, structuredAssessmentFact, assessmentKinds, assessmentOptions, assessmentStatuses, type Assessment, type AssessmentAnswer, type AssessmentDetail, type AssessmentQuestion, type AssessmentTemplate } from './assessments'

const props = defineProps<{ workspace: Workspace; assessmentId?: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const { context, href, setScope } = useScenarioContext(props.workspace.slug)
const scopeError = ref('')
const materialsScene = computed(() => ['personal', 'domestic', 'crossborder'].includes(context.value.scene))
const base = workspacePath(props.workspace.slug) + '/assessments'
const active = new AbortController()
const items = ref<Assessment[]>([])
const templates = ref<AssessmentTemplate[]>([])
const activities = ref<Activity[]>([])
const activityCursor = ref<number | null>(null)
const activityLoading = ref(false)
const detail = ref<AssessmentDetail>()
const answers = reactive<Record<string, AssessmentAnswer>>({})
const error = ref('')
const notice = ref('')
const busy = ref(false)
const loading = ref(false)
const showCreate = ref(new URLSearchParams(window.location.hash.split('?')[1] ?? '').get('create') === '1')
const showTemplates = ref(false)
const kindFilter = ref(new URLSearchParams(window.location.hash.split('?')[1] ?? '').get('kind') || '')
const statusFilter = ref('')
const reviewNote = ref('')
const checkIds = ref('')
const canCreate = computed(() => ['owner', 'operator'].includes(props.workspace.role))
const published = computed(() => templates.value.filter(t => t.state === 'published' && t.revisionId))
const due = new Date(Date.now() + 14 * 86400000).toISOString().slice(0, 10)
const create = reactive({ title: '', activityId: context.value.activityId, templateRevisionId: '', dueAt: due, refreshMinutes: 1440 })
const templateDraft = ref<AssessmentTemplate>()
const templateForm = reactive({ key: '', title: '', kind: 'pia', baseRevisionId: undefined as number | undefined, questions: [] as AssessmentQuestion[] })
const editingTemplate = ref(false)
const editingQuestion = ref(0)
const previewQuestion = computed(() => templateForm.questions[editingQuestion.value] ?? templateForm.questions[0])
const factChoices = Object.entries(assessmentFactLabels)

function scrollTo(id: string) { document.getElementById(id)?.scrollIntoView({ block: 'start', behavior: 'smooth' }) }
function showError(value: unknown) {
  if (active.signal.aborted) return
  if (value instanceof ApiError && value.status === 401) emit('sessionExpired')
  else error.value = describeError(value)
}
function applyDetail(value: AssessmentDetail) {
  detail.value = value
  for (const key of Object.keys(answers)) delete answers[key]
  for (const question of value.template.questions) {
    answers[question.key] = structuredClone(value.answers.find(a => a.key === question.key) ?? { key: question.key, values: [], evidenceIds: [] })
  }
  checkIds.value = value.checks.map(c => c.id).join(', ')
  reviewNote.value = ''
}
async function loadActivities(append = false) {
  activityLoading.value = true
  const query = new URLSearchParams()
  if (context.value.assetId) query.set('assetId', context.value.assetId)
  if (append && activityCursor.value !== null) query.set('before', String(activityCursor.value))
  try {
    const result = await request<ActivityPage>(workspacePath(props.workspace.slug) + '/activities?' + query, { signal: active.signal })
    activities.value = append ? [...activities.value, ...result.items] : result.items
    activityCursor.value = result.nextCursor ?? null
    if (context.value.activityId && !activities.value.some(item => String(item.id) === context.value.activityId)) activities.value.push(await request<Activity>(workspacePath(props.workspace.slug) + '/activities/' + encodeURIComponent(context.value.activityId), { signal: active.signal }))
    const selected = activities.value.find(item => String(item.id) === (context.value.activityId || create.activityId))
    if (selected && context.value.assetId && selected.assetId !== Number(context.value.assetId)) throw new Error('所选活动与链接中的资产不一致，请返回场景重新选择。')
    if (selected && !context.value.assetId) setScope({ assetId: String(selected.assetId), activityId: String(selected.id) })
  } finally { activityLoading.value = false }
}
async function load() {
  loading.value = true; error.value = ''; scopeError.value = ''
  try {
    if (props.assessmentId) {
      const value = await request<AssessmentDetail>(base + '/' + encodeURIComponent(props.assessmentId), { signal: active.signal })
      applyDetail(value)
      if (context.value.assetId && Number(context.value.assetId) !== value.activity.assetId || context.value.activityId && Number(context.value.activityId) !== value.activity.id) scopeError.value = '链接中的资产或活动范围与本评估不一致，请返回场景重新选择。'
      else setScope({ assetId: String(value.activity.assetId), activityId: String(value.activity.id) })
    } else {
      const query = new URLSearchParams({ kind: kindFilter.value, status: statusFilter.value })
      if (context.value.assetId) query.set('assetId', context.value.assetId)
      if (context.value.activityId) query.set('activityId', context.value.activityId)
      const [records, catalog] = await Promise.all([
        request<{ items: Assessment[] }>(base + '?' + query, { signal: active.signal }),
        request<{ items: AssessmentTemplate[] }>(base + '/templates', { signal: active.signal }),
        loadActivities(),
      ])
      items.value = records.items; templates.value = catalog.items
      if (context.value.activityId) create.activityId = context.value.activityId
      const requestedKind = new URLSearchParams(window.location.hash.split('?')[1] ?? '').get('kind')
      if (!create.templateRevisionId && requestedKind && assessmentKinds[requestedKind]) create.templateRevisionId = String(published.value.find(item => item.kind === requestedKind)?.revisionId ?? '')
    }
  } catch (e) { showError(e) } finally { loading.value = false }
}
function selectCreateActivity() {
  const activity = activities.value.find(item => String(item.id) === create.activityId)
  if (activity) setScope({ assetId: String(activity.assetId), activityId: String(activity.id) })
}
watch(context, () => {
  if (props.assessmentId || loading.value) return
  const query = new URLSearchParams(window.location.hash.split('?')[1] ?? '')
  if (query.get('kind') && assessmentKinds[query.get('kind')!]) {
    if (kindFilter.value !== query.get('kind')) create.templateRevisionId = ''
    kindFilter.value = query.get('kind')!
  }
  create.activityId = context.value.activityId
  if (query.get('create') === '1') showCreate.value = true
  void load()
})

async function perform(action: () => Promise<void>) {
  if (busy.value || scopeError.value) return
  busy.value = true; error.value = ''; notice.value = ''
  try { await action() } catch (e) { showError(e) } finally { busy.value = false }
}
async function createAssessment() {
  const selected = activities.value.find(item => String(item.id) === create.activityId)
  if (!selected || context.value.assetId && selected.assetId !== Number(context.value.assetId)) { error.value = '请核对所选活动及其关联资产范围。'; return }
  await perform(async () => {
    const value = await request<AssessmentDetail>(base, { method: 'POST', body: JSON.stringify({ ...create, activityId: Number(create.activityId), templateRevisionId: Number(create.templateRevisionId), dueAt: new Date(create.dueAt + 'T23:59:00').toISOString() }) })
    window.location.hash = href('assessments/' + value.id, { activityId: String(value.activity.id), assetId: String(value.activity.assetId) })
  })
}
function answerInput() {
  const ids = checkIds.value.split(/[，,\s]+/).filter(Boolean)
  if (ids.some(id => !/^\d+$/.test(id) || Number(id) < 1)) throw new Error('请填写有效的整改检查编号，以逗号分隔。')
  return { revision: detail.value!.revision, answers: Object.values(answers), checkIds: ids.map(Number) }
}
async function saveAnswers() {
  if (!detail.value) return
  await perform(async () => {
    applyDetail(await request<AssessmentDetail>(base + '/' + detail.value!.id + '/answers', { method: 'PUT', body: JSON.stringify(answerInput()) }))
    notice.value = '答复已保存。'
  })
}
async function review(action: string) {
  if (!detail.value) return
  await perform(async () => {
    const note = reviewNote.value
    if (action === 'submit') applyDetail(await request<AssessmentDetail>(base + '/' + detail.value!.id + '/answers', { method: 'PUT', body: JSON.stringify(answerInput()) }))
    applyDetail(await request<AssessmentDetail>(base + '/' + detail.value!.id + '/review', { method: 'POST', body: JSON.stringify({ revision: detail.value!.revision, action, note }) }))
  })
}
async function refreshFacts() {
  if (!detail.value) return
  await perform(async () => {
    applyDetail(await request<AssessmentDetail>(base + '/' + detail.value!.id + '/refresh', { method: 'POST', body: '{}' }))
    notice.value = '已刷新并采用当前事实。活动范围变化时，旧答复已清除。'
  })
}
function setNumber(key: string, event: Event) {
  const value = (event.target as HTMLInputElement).value
  answers[key]!.number = value === '' ? undefined : Number(value)
}
function optionLabel(value: string) { return assessmentOptions[value] ?? value }
function factsFor(question: AssessmentQuestion) { return detail.value?.facts.filter(f => f.key === question.factKey) ?? [] }
function factFor(question: AssessmentQuestion) { const facts = factsFor(question); return facts.length === 1 ? facts[0] : undefined }
function editTemplate(value?: AssessmentTemplate, newVersion = false) {
  templateDraft.value = value?.state === 'draft' ? value : undefined
  Object.assign(templateForm, { key: value && (value.state === 'draft' || newVersion) ? value.key : '', title: value ? value.title + (value.state === 'draft' || newVersion ? '' : '副本') : '', kind: value?.kind ?? 'pia', baseRevisionId: value?.revisionId, questions: value ? structuredClone(value.questions) : [] })
  editingTemplate.value = true
}
function addQuestion() {
  templateForm.questions.push({ key: 'question_' + (templateForm.questions.length + 1), title: '', kind: 'text', basis: '', basisUrl: '', applicability: '', evidence: '', acceptance: '', requirementCode: 'P04', obligation: 'conditional_law', required: true })
}
async function saveTemplate() {
  await perform(async () => {
    const suffix = templateDraft.value ? '/' + templateDraft.value.id + '/draft' : ''
    const value = await request<AssessmentTemplate>(base + '/templates' + suffix, { method: templateDraft.value ? 'PUT' : 'POST', body: JSON.stringify({ ...templateForm, revision: templateDraft.value?.revision ?? 0 }) })
    templateDraft.value = value
    notice.value = '草稿已保存，发布后可创建评估。'
    await load()
  })
}
async function publishTemplate(value: AssessmentTemplate) {
  await perform(async () => { await request(base + '/templates/' + value.id + '/publish', { method: 'POST', body: JSON.stringify({ revision: value.revision }) }); editingTemplate.value = false; await load() })
}
async function disableTemplate(value: AssessmentTemplate) {
  await perform(async () => { await request(base + '/templates/' + value.revisionId + '/disable', { method: 'POST', body: '{}' }); await load() })
}
onMounted(load)
onBeforeUnmount(() => active.abort())
</script>

<template>
  <div class="assessments-page">
    <header class="page-header">
      <div><a v-if="assessmentId" :href="href('assessments')">返回专项评估</a><h1>{{ detail?.title ?? '专项评估' }}</h1><p>围绕实际处理活动核验条件、留存依据并完成独立复核。</p></div>
      <div class="actions">
        <button type="button" class="button secondary" :disabled="loading || busy" @click="load">刷新</button>
        <template v-if="!assessmentId"><button type="button" class="button secondary" @click="showTemplates = !showTemplates">{{ showTemplates ? '评估记录' : '模板管理' }}</button><button v-if="canCreate" type="button" class="button primary" @click="showCreate = !showCreate">新建评估</button></template>
        <template v-if="detail"><a class="button secondary" :href="base + '/' + detail.id + '/export?format=json'">导出 JSON</a><a class="button secondary" :href="base + '/' + detail.id + '/export?format=html'">导出报告</a></template>
      </div>
    </header>
    <p v-if="scopeError" class="message message-error" role="alert">{{ scopeError }}</p>
    <p v-if="context.assetId || context.activityId" class="assessment-scope-caption">本次办理范围：资产 #{{ context.assetId || '待选' }}<template v-if="context.activityId"> · 活动 #{{ context.activityId }}</template>；评估列表按此范围读取。<a :href="href('activities/new', { assetId: context.assetId, activityId: '' })">为所选资产登记活动</a></p>
    <ScenarioDataPanel v-if="materialsScene && !scopeError && (detail?.activity.assetId || context.assetId || context.activityId)" :workspace="workspace" :asset-id="detail ? String(detail.activity.assetId) : context.assetId" :activity-id="detail ? String(detail.activity.id) : create.activityId || context.activityId" compact @session-expired="emit('sessionExpired')" />
    <p v-if="error" class="error-message" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <p v-if="loading" role="status">正在读取评估与采用的来源事实…</p>



    <template v-if="!assessmentId && !showTemplates"><nav class="assessment-scenarios" aria-label="评估类型"><button :class="{active: !kindFilter}" @click="kindFilter = ''; load()">全部评估</button><button v-for="(label,key) in assessmentKinds" :key="key" :class="{active:kindFilter === key}" @click="kindFilter = String(key); load()">{{ label }}</button></nav>
      <form class="filter-bar" @submit.prevent="load"><label class="field">评估类型<select v-model="kindFilter"><option value="">全部类型</option><option v-for="(label, key) in assessmentKinds" :key="key" :value="key">{{ label }}</option></select></label><label class="field">状态<select v-model="statusFilter"><option value="">全部状态</option><option v-for="(label, key) in assessmentStatuses" :key="key" :value="key">{{ label }}</option></select></label><button class="button secondary" :disabled="loading">查询</button></form>
<div class="assessment-list-workbench">      <section class="panel"><h2>评估记录</h2><p v-if="!loading && !items.length">当前范围内尚无评估。</p><div v-else class="table-scroll"><table class="data-table"><thead><tr><th>评估</th><th>类型</th><th>当前状态</th><th>期限</th><th>更新时间</th></tr></thead><tbody><tr v-for="item in items" :key="item.id"><td><a :href="href('assessments/' + item.id, { activityId: String(item.activityId) })">{{ item.title }}</a></td><td>{{ assessmentKinds[item.kind] }}</td><td>{{ assessmentStatuses[item.status] }}<span v-if="item.stale" class="badge">待重新评估</span></td><td>{{ formatDate(item.dueAt) }}</td><td>{{ formatDate(item.updatedAt) }}</td></tr></tbody></table></div><p class="muted">展示最近100条评估，业务条件缺失会保留为待核验。</p></section><aside class="assessment-list-rail">    <section v-if="showCreate && canCreate" class="panel assessment-create-panel">
      <h2>创建专项评估</h2>
      <form class="assessment-form" @submit.prevent="createAssessment">
        <label class="field">评估名称<input v-model="create.title" required maxlength="100"></label>
        <label class="field">处理活动<select v-model="create.activityId" required @change="selectCreateActivity"><option value="">选择已登记活动</option><option v-for="a in activities" :key="a.id" :value="String(a.id)">{{ a.name }}</option></select></label>
        <button v-if="activityCursor !== null" class="button secondary" type="button" :disabled="activityLoading" @click="loadActivities(true).catch(showError)">加载更多范围内活动</button>
        <label class="field">已发布模板<select v-model="create.templateRevisionId" required><option value="">选择模板版本</option><option v-for="t in published" :key="t.revisionId" :value="String(t.revisionId)">{{ assessmentKinds[t.kind] }} · {{ t.title }} · v{{ t.revision }}</option></select></label>
        <label class="field">评估期限<input v-model="create.dueAt" type="date" required></label>
        <label class="field">机器事实刷新间隔（分钟）<input v-model.number="create.refreshMinutes" type="number" min="5" max="43200" required></label>
        <button class="button primary" :disabled="busy || !activities.length || !published.length">创建并采集事实</button>
      </form>
      <p v-if="!activities.length">请先在<a :href="href('activities/new', { assetId: context.assetId, activityId: '' })">处理活动</a>登记评估范围。</p>
    </section><section v-if="!showCreate || !canCreate" class="panel"><p class="eyebrow">评估采用范围</p><h2>从实际处理活动开始</h2><p>按已发布模板采集来源事实，逐项核实适用条件；未知信息保留为待取证。</p><dl><div><dt>本次记录</dt><dd>{{ items.length }} 项</dd></div><div><dt>已发布模板</dt><dd>{{ published.length }} 个版本</dd></div><div><dt>可关联活动</dt><dd>{{ activities.length }} 项</dd></div></dl><button v-if="canCreate" class="button primary" @click="showCreate=true">创建专项评估</button></section><section class="panel"><h2>独立复核要求</h2><p>答复完成并不代表合规通过。缺失业务条件、过期来源和未完成整改会阻止通过。</p><p>完成后的历史评估保留已采用的事实与版本，后续变化单独标记。</p></section></aside></div>
    </template>

    <section v-if="!assessmentId && showTemplates" class="panel">
      <div class="panel-heading"><h2>版本化评估模板</h2><button v-if="workspace.role === 'owner'" class="button secondary" @click="editTemplate()">创建模板草稿</button></div>
      <div class="table-scroll"><table class="data-table"><thead><tr><th>模板</th><th>类型与版本</th><th>状态</th><th>题目</th><th>操作</th></tr></thead><tbody><tr v-for="t in templates" :key="t.state + ':' + t.id"><td>{{ t.title }}<small>{{ t.key }}</small></td><td>{{ assessmentKinds[t.kind] }} · v{{ t.revision }}</td><td>{{ { draft: '草稿', published: '已发布', disabled: '已停用' }[t.state] ?? t.state }}</td><td>{{ t.questions.length }}</td><td class="actions"><template v-if="workspace.role === 'owner'"><button class="button secondary" @click="editTemplate(t)">{{ t.state === 'draft' ? '编辑' : '复制为草稿' }}</button><button v-if="t.state !== 'draft' && !t.builtIn" class="button secondary" @click="editTemplate(t, true)">创建新版本</button><button v-if="t.state === 'draft'" class="button secondary" :disabled="busy" @click="publishTemplate(t)">发布</button><button v-else-if="t.state === 'published' && !t.builtIn" class="button secondary" :disabled="busy" @click="disableTemplate(t)">停用</button></template></td></tr></tbody></table></div>
      <div v-if="editingTemplate" class="assessment-template-workbench"><nav class="panel assessment-template-outline" aria-label="模板题目目录"><h3>题目目录</h3><button v-for="(q,index) in templateForm.questions" :key="index" type="button" :class="{active:editingQuestion===index}" @click="editingQuestion=index;scrollTo('template-question-' + index)">{{ String(index+1).padStart(2,'0') }} · {{ q.title || '新题目' }}</button><p v-if="!templateForm.questions.length">从添加题目开始配置。</p></nav><form class="assessment-template-editor" @submit.prevent="saveTemplate">
        <div class="template-editor-heading"><h3>模板草稿</h3><p>将适用条件、来源事实、取证要求和通过标准逐题配置；已发布版本保留原始内容。</p></div><nav class="template-question-tabs" aria-label="草稿题目"><button v-for="(q,index) in templateForm.questions" :key="index" type="button" @click="scrollTo('template-question-' + index)">{{ index+1 }} · {{ q.title || '新题目' }}</button></nav><div class="assessment-form"><label class="field">模板标识<input v-model="templateForm.key" required pattern="[a-z0-9][a-z0-9_-]{0,59}" :disabled="!!templateDraft"></label><label class="field">名称<input v-model="templateForm.title" required maxlength="100"></label><label class="field">评估类型<select v-model="templateForm.kind" :disabled="!!templateDraft"><option v-for="(label, key) in assessmentKinds" :key="key" :value="key">{{ label }}</option></select></label></div>
        <fieldset v-for="(q, index) in templateForm.questions" :id="'template-question-' + index" :key="index" class="assessment-question" @focusin="editingQuestion=index"><legend>{{ q.title || '新题目' }}</legend><div class="assessment-form"><label class="field">题目标识<input v-model="q.key" required pattern="[a-z0-9][a-z0-9_-]{0,59}"></label><label class="field">题目<input v-model="q.title" required maxlength="100"></label><label class="field">题型<select v-model="q.kind"><option value="single">单选</option><option value="multi">多选</option><option value="text">文本</option><option value="number">数字</option><option value="evidence">证据引用</option></select></label><label class="field">依据性质<select v-model="q.obligation"><option value="conditional_law">依条件适用的法定义务</option><option value="recommended">组织治理建议</option></select></label><label class="field">关联监管要求<input v-model="q.requirementCode" required maxlength="12"></label><label class="field">自动事实来源<select v-model="q.factKey"><option value="">由评估人核实</option><option v-for="[key, label] in factChoices" :key="key" :value="key">{{ label }}</option></select></label></div><label v-if="q.kind === 'single' || q.kind === 'multi'" class="field">选项（每行一个）<textarea :value="q.options?.join('\n')" required @input="q.options = ($event.target as HTMLTextAreaElement).value.split('\n').map(v => v.trim()).filter(Boolean)"></textarea></label><label class="field">法规或治理依据<input v-model="q.basis" required maxlength="400"></label><label class="field">官方依据链接<input v-model="q.basisUrl" type="url" required></label><label class="field">适用条件<textarea v-model="q.applicability" required maxlength="1000"></textarea></label><label class="field">取证要求<textarea v-model="q.evidence" required maxlength="1000"></textarea></label><label class="field">验收标准<textarea v-model="q.acceptance" required maxlength="1000"></textarea></label><label><input v-model="q.required" type="checkbox">独立通过前须完成</label><button class="button secondary" type="button" @click="templateForm.questions.splice(index, 1)">移除此题</button></fieldset>
        <div class="actions"><button type="button" class="button secondary" :disabled="templateForm.questions.length >= 25" @click="addQuestion">添加题目</button><button class="button primary" :disabled="busy || !templateForm.questions.length">保存草稿</button><button type="button" class="button secondary" @click="editingTemplate = false">收起</button></div>
      </form><aside class="panel assessment-template-preview"><p class="eyebrow">当前题目预览</p><h3>{{ previewQuestion?.title || '待填写题目' }}</h3><template v-if="previewQuestion"><span class="badge">{{ previewQuestion.obligation==='recommended'?'组织治理建议':'依条件适用的法定义务' }}</span><dl><div><dt>适用条件</dt><dd>{{ previewQuestion.applicability || '待填写' }}</dd></div><div><dt>取证要求</dt><dd>{{ previewQuestion.evidence || '待填写' }}</dd></div><div><dt>验收标准</dt><dd>{{ previewQuestion.acceptance || '待填写' }}</dd></div><div><dt>自动事实</dt><dd>{{ assessmentFactLabels[previewQuestion.factKey || ''] || '评估人核实' }}</dd></div></dl></template><p>发布后固定本版本题目及依据。复制内置模板生成独立草稿，既有评估继续保留采用版本。</p></aside></div>
    </section>

    <template v-if="detail">
      <div class="assessment-workflow"><div><span>01</span><strong>评估范围</strong><small>{{ detail.activity.name }}</small></div><div><span>02</span><strong>来源事实</strong><small>{{ detail.facts.filter(f => f.state === 'known').length }} / {{ detail.facts.length }} 项已有来源记录</small></div><div><span>03</span><strong>答复与整改</strong><small>{{ detail.gaps.length }} 项取证缺口 · {{ detail.checks.length }} 项关联检查</small></div><div><span>04</span><strong>独立复核</strong><small>{{ assessmentStatuses[detail.status] }}</small></div></div>
      <p v-if="detail.stale" class="error-message" role="status">活动或来源事实已变化。{{ detail.canEdit ? '请刷新并核对当前范围，业务范围变化后需要重新答题。' : '当前采用快照已过期，请建立新评估并补齐取证。' }}</p>
      <div class="assessment-detail-workbench"><nav class="panel assessment-directory" aria-label="评估答题目录"><h2>评估目录</h2><button @click="scrollTo('assessment-facts')">来源事实</button><button v-for="(q,index) in detail.template.questions" :key="q.key" @click="scrollTo('answer-' + q.key)"><span>{{ String(index+1).padStart(2,'0') }}</span>{{ q.title }}</button><button @click="scrollTo('assessment-history')">评估历史</button></nav><main class="assessment-main">      <section id="assessment-facts" class="panel assessment-facts"><div class="panel-heading"><h2>来源事实</h2><button v-if="detail.canEdit" class="button secondary" :disabled="busy" @click="refreshFacts">刷新并采用当前事实</button></div><p>登记信息与自动采集分别保留来源，尚未核实的业务条件继续取证。</p><p v-if="detail.automaticApplicability" class="assessment-auto-fact">范围自动判定 · {{ detail.automaticApplicability.state === 'applicable' ? '登记活动符合评估范围' : '待核实触发条件' }} · {{ detail.automaticApplicability.reason }}</p><div class="table-scroll"><table class="data-table"><thead><tr><th>事实</th><th>观察结果</th><th>来源与记录</th></tr></thead><tbody><tr v-for="fact in detail.facts" :key="fact.key + ':' + fact.source + ':' + (fact.record || '')"><td>{{ assessmentFactLabels[fact.key] ?? fact.key }}</td><td><span class="assessment-fact-state">{{ assessmentFactStateLabel(fact) }}</span><details v-if="structuredAssessmentFact(fact.value)" class="assessment-fact-detail"><summary>查看来源明细与范围</summary><pre>{{ structuredAssessmentFact(fact.value) }}</pre></details><span v-else>{{ fact.state === 'known' ? assessmentFactDisplayValue(fact) || '来源未返回结果内容' : fact.value ? '待核实 · ' + fact.value : '待核实' }}</span></td><td>{{ assessmentFactSourceLabel(fact.source) }}<small>{{ fact.record }}<template v-if="fact.observedAt"> · {{ formatDate(fact.observedAt) }}</template></small></td></tr></tbody></table></div></section>      <form class="panel assessment-answers" @submit.prevent="saveAnswers">
        <h2>评估答复</h2><p>{{ detail.basis }}</p>
        <fieldset v-for="q in detail.template.questions" :key="q.key" :id="'answer-' + q.key" class="assessment-question" :disabled="!detail.canEdit || detail.stale || busy"><legend>{{ q.title }} <small v-if="q.required">须完成</small></legend><p><span class="badge">{{ q.obligation === 'recommended' ? '组织治理建议' : '依条件适用' }}</span> {{ q.applicability }}</p><p><a :href="q.basisUrl" target="_blank" rel="noopener noreferrer">{{ q.basis }}</a></p><p v-if="factsFor(q).length > 1" class="assessment-auto-fact">同一事实有多份来源记录。请在来源事实中逐一核对范围、时点与差异后填写答复。</p><p v-else-if="factFor(q)" class="assessment-auto-fact">来源事实 · {{ assessmentFactSourceLabel(factFor(q)!.source) }} · {{ structuredAssessmentFact(factFor(q)?.value) ? '结构化来源记录，请在来源事实中展开核对。' : factFor(q)?.state === 'known' ? factFor(q)?.value : '待核实' }}<button v-if="q.kind === 'text' && factFor(q)?.state === 'known' && !structuredAssessmentFact(factFor(q)?.value) && detail.canEdit && !detail.stale" class="button secondary" type="button" @click="answers[q.key]!.value = factFor(q)?.value">采用此事实</button></p>
          <label v-if="q.kind === 'single'" class="field">核验结论<select v-model="answers[q.key]!.value"><option value="">请选择</option><option v-for="option in q.options" :key="option" :value="option">{{ optionLabel(option) }}</option></select></label>
          <div v-else-if="q.kind === 'multi'" class="assessment-options"><label v-for="option in q.options" :key="option"><input v-model="answers[q.key]!.values" type="checkbox" :value="option">{{ optionLabel(option) }}</label></div>
          <label v-else-if="q.kind === 'text'" class="field">答复<textarea v-model="answers[q.key]!.value" maxlength="2000" rows="4"></textarea></label>
          <label v-else-if="q.kind === 'number'" class="field">经核实的数值<input :value="answers[q.key]!.number ?? ''" type="number" min="0" step="1" @input="setNumber(q.key, $event)"></label>
          <label v-else class="field">已登记证据（可多选）<select v-model="answers[q.key]!.evidenceIds" multiple size="5"><option v-for="e in detail.evidence" :key="e.id" :value="e.id">#{{ e.id }} · {{ e.source }} · {{ e.summary }}</option></select><span v-if="!detail.evidence.length">请先在相关资产或活动的检查中登记证据。</span></label>
          <label class="field">理由与范围说明<textarea v-model="answers[q.key]!.note" maxlength="1000" rows="2"></textarea></label><details><summary>取证要求与验收标准</summary><p>{{ q.evidence }}</p><p>{{ q.acceptance }}</p></details>
        </fieldset>
        <label class="field">关联整改检查编号（逗号分隔）<input v-model="checkIds" :disabled="!detail.canEdit || detail.stale || busy" inputmode="numeric"></label>
        <div v-if="detail.canEdit" class="actions"><button class="button primary" :disabled="busy || detail.stale">保存答复</button><button class="button secondary" type="button" :disabled="busy || detail.stale" @click="review('submit')">提交独立复核</button></div>
      </form>      <section id="assessment-history" class="panel"><h2>评估历史</h2><ol class="assessment-history"><li v-for="event in detail.events" :key="event.id"><strong>{{ { created: '创建评估', answers_saved: '保存答复', submit: '提交复核', return: '退回补充', approve: '独立通过', stale: '标记过期' }[event.action] ?? event.action }}</strong> · {{ event.actor }} · {{ formatDate(event.createdAt) }}<p v-if="event.note">{{ event.note }}</p></li></ol></section>
</main><aside class="assessment-review-rail">      <section class="panel assessment-summary"><div><span class="badge">{{ assessmentKinds[detail.kind] }}</span> <span class="badge">{{ assessmentStatuses[detail.status] }}</span><p>{{ detail.activity.name }} · {{ detail.template.title }} v{{ detail.template.revision }}</p><p>创建人 {{ detail.creator }}<span v-if="detail.reviewer"> · 复核人 {{ detail.reviewer }}</span></p></div><div><p>评估期限 {{ formatDate(detail.dueAt) }}</p><p>事实刷新 {{ formatDate(detail.factsRefreshedAt) }}</p><p>每 {{ detail.refreshMinutes }} 分钟核对机器事实</p></div></section>      <section class="panel"><h2>取证缺口与整改</h2><p v-if="!detail.gaps.length && !detail.risks.length">当前答复已覆盖模板要求，结论由独立人员核验。</p><ul><li v-for="gap in detail.gaps" :key="gap.key">{{ gap.title }} · {{ gap.reason }}</li><li v-for="risk in detail.risks" :key="'risk:' + risk.key">{{ risk.title }} · {{ risk.reason }}</li></ul><p v-for="check in detail.checks" :key="check.id"><a :href="home + '/checks/' + check.id">检查 #{{ check.id }}</a> · {{ check.requirementCode }} · {{ check.status }} · {{ check.ownerTeam }}</p><p v-if="detail.evidenceTruncated">候选证据列表已达到展示上限，请按实际范围核对采用证据编号。</p></section>      <section v-if="detail.canReview" class="panel"><h2>独立复核</h2><label class="field">复核意见<textarea v-model="reviewNote" maxlength="1000" rows="3"></textarea></label><div class="actions"><button class="button secondary" :disabled="busy || !reviewNote.trim()" @click="review('return')">退回补充</button><button class="button primary" :disabled="busy || !reviewNote.trim() || !detail.canApprove" @click="review('approve')">独立通过</button></div><p v-if="!detail.canApprove">活动过期、未完成答复和关联整改检查会阻止通过。</p></section><section v-if="!detail.canReview" class="panel"><h2>当前权限</h2><p>{{ detail.canEdit ? '完成答复后提交独立人员复核。' : '当前评估以只读方式展示采用的事实和历史答复。' }}</p><p>{{ detail.status === 'approved' ? '已通过记录保留采用快照；新来源事实变化后需要重新评估。' : detail.status === 'submitted' ? '当前采用范围随送审版本留存，结论等待独立人员核验。' : '当前事实与答复保留版本；缺失条件补齐后再提交独立复核。' }}</p></section></aside></div>
    </template>
  </div>
</template>

<style scoped>
.assessments-page > .filter-bar { align-items: flex-end; }
.assessments-page > .filter-bar > .field { display: grid; gap: 7px; margin: 0; }
.assessments-page > .filter-bar > .button { align-self: flex-end; }
.assessments-page { display: grid; gap: 24px; }
.assessment-summary, form.panel { padding: 18px; }
.assessments-page > section.panel > h2,
.assessments-page > section.panel > p { margin-left: 18px; margin-right: 18px; }
.assessments-page > section.panel > .assessment-form,
.assessments-page > section.panel > .assessment-template-editor { margin: 18px; }
.assessments-page > section.panel > .assessment-history { margin-right: 18px; margin-left: 18px; }
.assessment-form { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 230px), 1fr)); gap: 18px; align-items: end; }
.assessment-summary { display: flex; justify-content: space-between; gap: 24px; flex-wrap: wrap; }
.assessment-question { margin: 24px 0; padding: 22px; border: 1px solid var(--line, #dbe5ed); border-radius: 12px; min-width: 0; }
.assessment-question legend { padding: 0 8px; font-weight: 600; }
.assessment-question p { max-width: 80ch; }
.assessment-question .field { margin: 16px 0; }
.assessment-question:disabled { opacity: 1; }
.assessment-question textarea, .assessment-question select, .assessment-question input { max-width: 100%; }
.assessment-options { display: flex; gap: 16px; flex-wrap: wrap; margin: 18px 0; }
.assessment-options label { display: flex; gap: 8px; align-items: center; min-height: 44px; }
.assessment-auto-fact { padding: 14px; background: var(--surface-muted, #f5f8fe); border-radius: 8px; }
.assessment-template-editor { margin-top: 28px; }
.assessment-history { padding-left: 24px; }
.assessment-history li { padding: 10px 0; }
small { display: block; margin-top: 5px; color: var(--muted, #53667d); overflow-wrap: anywhere; }
.actions { display: flex; gap: 10px; flex-wrap: wrap; align-items: center; }
@media (max-width: 600px) { .assessment-question { padding: 14px; } }
</style>

<style scoped>
.assessments-page{gap:16px}.assessment-workflow{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));background:#fff;border:1px solid var(--line);border-radius:12px;padding:18px}.assessment-workflow>div{padding:0 16px;border-right:1px solid var(--line)}.assessment-workflow>div:last-child{border:0}.assessment-workflow span{color:var(--blue);font-size:12px;margin-right:8px}.assessment-workflow strong{font-size:13px}.assessment-detail-workbench{display:grid;grid-template-columns:170px minmax(0,1fr) 300px;gap:16px;align-items:start}.assessment-directory{position:sticky;top:84px;padding:18px 10px}.assessment-directory h2{font-size:13px;margin:0 8px 12px}.assessment-directory button{display:flex;gap:8px;align-items:start;width:100%;text-align:left;background:none;border:0;padding:11px 8px;color:var(--muted);line-height:1.6;cursor:pointer;font:inherit;font-size:12px}.assessment-directory button:hover{background:var(--accent-soft);color:var(--blue)}.assessment-directory button span{color:var(--blue);font-size:12px;margin-top:2px}.assessment-main,.assessment-review-rail{display:grid;gap:16px;min-width:0}.assessment-main>.panel,.assessment-review-rail>.panel{padding:20px}.assessment-summary{display:grid;gap:4px}.assessment-review-rail h2{font-size:14px;margin:0 0 14px}.assessment-review-rail p,.assessment-review-rail li{font-size:12px;line-height:1.8;overflow-wrap:anywhere}.assessment-review-rail ul{padding-left:17px}.assessment-review-rail .actions{display:grid;grid-template-columns:1fr 1fr}.assessment-review-rail .button{padding:7px 9px}.assessment-question{padding:18px;margin:18px 0;scroll-margin-top:90px;background:var(--surface-subtle)}.assessment-question legend{color:var(--ink)}.assessment-facts{scroll-margin-top:90px}.assessment-scenarios,.template-question-tabs{display:flex;gap:16px;flex-wrap:wrap;border-bottom:1px solid var(--line)}.assessment-scenarios button,.template-question-tabs button{font:inherit;padding:12px 3px;border:0;border-bottom:2px solid transparent;background:none;color:var(--muted);cursor:pointer}.assessment-scenarios button.active{color:var(--blue);border-color:var(--blue)}.template-editor-heading{padding-bottom:12px}.template-editor-heading h3{font-size:18px}.template-editor-heading p{color:var(--muted);font-size:12px}.assessment-template-editor{padding-top:22px;border-top:1px solid var(--line)}@media(max-width:1400px){.assessment-detail-workbench{grid-template-columns:145px minmax(0,1fr) 270px;gap:12px}.assessment-main>.panel,.assessment-review-rail>.panel{padding:16px}.assessment-workflow>div{padding:0 10px}}
</style>

<style scoped>.assessment-list-workbench{display:grid;grid-template-columns:minmax(0,1fr) 300px;gap:16px;align-items:start}.assessment-list-rail{display:grid;gap:16px}.assessment-list-rail>.panel{padding:20px}.assessment-list-rail h2{font-size:14px;margin:0 0 16px}.assessment-list-rail p{font-size:12px;line-height:1.9;color:var(--muted)}.assessment-list-rail dl{display:grid;gap:15px;margin:20px 0}.assessment-list-rail dl>div{font-size:12px;display:grid;grid-template-columns:90px 1fr}.assessment-list-rail dd{margin:0}.assessment-list-rail dt{color:var(--muted)}.assessment-create-panel .assessment-form{display:grid;grid-template-columns:minmax(0,1fr);gap:14px}.assessment-create-panel .field{font-size:12px}.assessment-template-workbench{display:grid;grid-template-columns:160px minmax(0,1fr) 260px;gap:16px;padding:20px;border-top:1px solid var(--line);align-items:start}.assessment-template-editor{margin:0!important;padding:0;border:0;min-width:0}.assessment-template-editor .assessment-form{grid-template-columns:1fr 1fr;gap:12px}.assessment-template-editor .assessment-question{padding:16px}.assessment-template-outline{padding:18px 10px;position:sticky;top:84px}.assessment-template-outline h3{font-size:13px;padding:0 8px}.assessment-template-outline button{display:block;text-align:left;background:none;border:0;padding:12px 8px;width:100%;font:inherit;font-size:12px;line-height:1.8;color:var(--muted);cursor:pointer}.assessment-template-outline button.active{background:var(--accent-soft);color:var(--blue);border-radius:6px}.assessment-template-outline p{font-size:12px;color:var(--muted);padding:0 8px}.assessment-template-preview{padding:20px;position:sticky;top:84px}.assessment-template-preview h3{font-size:14px;margin-bottom:16px}.assessment-template-preview dl{display:grid;gap:18px}.assessment-template-preview dt{font-size:12px;color:var(--blue);margin-bottom:7px}.assessment-template-preview dd{margin:0;font-size:12px;line-height:1.8;white-space:pre-wrap;overflow-wrap:anywhere}.assessment-template-preview p{font-size:12px;line-height:1.9;color:var(--muted)}.template-question-tabs{display:none}@media(max-width:1400px){.assessment-list-workbench{grid-template-columns:minmax(0,1fr) 270px}.assessment-template-workbench{grid-template-columns:140px minmax(0,1fr) 240px;gap:12px;padding:16px}.assessment-template-editor .assessment-form{grid-template-columns:1fr}}</style>

<style scoped>.assessment-list-workbench>section.panel>h2{padding:18px 20px;margin:0;font-size:15px}.assessment-list-workbench>section.panel>p{margin:16px 20px;color:var(--muted);font-size:12px}.assessment-template-workbench .field{margin:0}.assessment-template-workbench .assessment-question>.field{margin-top:14px}.assessment-create-panel .field{margin:0}</style>

<style scoped>
.assessment-fact-state{display:block;margin-bottom:6px;color:var(--secondary);font-size:12px}.assessment-fact-detail summary{min-height:32px;color:var(--accent);cursor:pointer}.assessment-fact-detail pre{max-height:260px;max-width:100%;overflow:auto;margin-top:8px;padding:12px;border:1px solid var(--line);border-radius:6px;background:var(--surface-subtle);font-size:12px;line-height:1.8;white-space:pre-wrap;overflow-wrap:anywhere}
</style>

<style scoped>.assessment-scope-caption{padding:12px 16px;margin:0 0 16px;border:1px solid var(--line);border-radius:8px;background:var(--surface-subtle);font-size:12px;line-height:1.8}.assessment-scope-caption a{margin-left:12px;color:var(--accent)}.assessments-page>.scenario-data{margin-bottom:18px}</style>
