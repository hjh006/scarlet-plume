<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { activityCheckHints, activityKindLabels, type Activity, type ActivityHistory, type ActivityHistoryPage, type ActivityInput } from './activity'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { canManageChecks } from './workflow'
import ChecksView from './ChecksView.vue'
import Icon from './Icon.vue'

const props = defineProps<{ workspace: Workspace; activityId: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const apiUrl = workspacePath(props.workspace.slug) + '/activities/' + encodeURIComponent(props.activityId)
const activity = ref<Activity | null>(null)
const loading = ref(true)
const loadError = ref('')
const editing = ref(false)
const saving = ref(false)
const conflict = ref(false)
const formError = ref('')
const notice = ref('')
const editHeading = ref<HTMLElement | null>(null)
const draftRevision = ref(0)
const draft = reactive<ActivityInput>({ assetId: 0, name: '', kind: 'internal', purpose: '', recipient: '', region: '', retentionPolicy: '', transferPath: '', safeguards: '' })
const history = ref<ActivityHistory[]>([])
const historyCursor = ref<number | null>(null)
const historyLoading = ref(false)
const historyError = ref('')
const active = new AbortController()
let historyRequest: AbortController | undefined
const canEdit = computed(() => ['owner', 'operator'].includes(props.workspace.role))
const fields: { key: keyof ActivityInput; label: string }[] = [
  { key: 'assetId', label: '关联资产' }, { key: 'name', label: '活动名称' }, { key: 'kind', label: '活动类型' },
  { key: 'purpose', label: '处理目的' }, { key: 'recipient', label: '接收方 / 模型服务' }, { key: 'region', label: '处理地域' },
  { key: 'retentionPolicy', label: '留存和训练条款' }, { key: 'transferPath', label: '出境路径记录' }, { key: 'safeguards', label: '保护措施' },
]
function errorText(error: unknown) {
  if (active.signal.aborted) return ''
  if (error instanceof ApiError && error.status === 401) { emit('sessionExpired'); return '' }
  return describeError(error)
}
function applyDraft(value: Activity) {
  for (const { key } of fields) Object.assign(draft, { [key]: value[key] ?? '' })
  draftRevision.value = value.revision
}
async function load(replaceDraft = false) {
  if ((loading.value && activity.value) || saving.value) return
  loading.value = true
  if (replaceDraft) formError.value = ''
  else loadError.value = ''
  try {
    const value = await request<Activity>(apiUrl, { signal: active.signal })
    if (active.signal.aborted) return
    activity.value = value
    if (replaceDraft) { applyDraft(value); conflict.value = false; formError.value = ''; notice.value = '已重新读取当前修订，未保存的输入已替换。' }
  } catch (error) {
    if (replaceDraft) formError.value = errorText(error)
    else loadError.value = errorText(error)
  } finally { loading.value = false }
}
async function beginEdit() {
  if (!canEdit.value || !activity.value?.revision || loading.value || saving.value) return
  applyDraft(activity.value); editing.value = true; conflict.value = false; formError.value = ''; notice.value = ''
  await nextTick(); editHeading.value?.focus({ preventScroll: true }); editHeading.value?.scrollIntoView({ block: 'nearest' })
}
async function save() {
  if (!editing.value || !canEdit.value || !activity.value || saving.value || loading.value || conflict.value) return
  saving.value = true; formError.value = ''; notice.value = ''
  const input = { ...draft, assetId: activity.value.assetId, revision: draftRevision.value }
  for (const key of ['name', 'purpose', 'recipient', 'region', 'retentionPolicy', 'transferPath', 'safeguards'] as const) input[key] = input[key].trim()
  try {
    const value = await request<Activity>(apiUrl, { method: 'PUT', body: JSON.stringify(input), signal: active.signal })
    if (active.signal.aborted) return
    notice.value = value.revision === draftRevision.value ? '活动内容未变化，保留当前修订。' : `活动已保存为修订 v${value.revision}。`
    activity.value = value; editing.value = false; conflict.value = false
    await loadHistory()
  } catch (error) {
    conflict.value = error instanceof ApiError && error.status === 409
    formError.value = conflict.value ? '活动已被其他人更新。未保存的输入已保留，请核对后显式重新读取当前修订。' : errorText(error)
  } finally { saving.value = false }
}
function cancelEdit() { const refresh = conflict.value; editing.value = false; conflict.value = false; formError.value = ''; notice.value = ''; if (refresh) void load() }
async function loadHistory(append = false) {
  historyRequest?.abort()
  const pending = new AbortController(); historyRequest = pending
  historyLoading.value = true; historyError.value = ''
  const query = new URLSearchParams({ limit: '50' })
  if (append && historyCursor.value !== null) query.set('before', String(historyCursor.value))
  try {
    const page = await request<ActivityHistoryPage>(apiUrl + '/history?' + query, { signal: pending.signal })
    if (pending.signal.aborted) return
    history.value = append ? [...history.value, ...page.items] : page.items
    historyCursor.value = page.nextCursor ?? null
  } catch (error) { if (!pending.signal.aborted) historyError.value = errorText(error) }
  finally { if (!pending.signal.aborted) historyLoading.value = false }
}
function changes(entry: ActivityHistory) { return fields.filter(field => String(entry.before?.[field.key] ?? '') !== String(entry.after[field.key] ?? '')) }
function display(value: Activity | null, key: keyof ActivityInput) {
  if (!value) return '未记录'
  if (key === 'kind') return activityKindLabels[value.kind] || value.kind
  if (key === 'assetId') return (value.assetName ? value.assetName + ' · ' : '') + '#' + value.assetId
  return String(value[key] || '未记录')
}
onMounted(async () => { await load(); if (activity.value) void loadHistory() })
onBeforeUnmount(() => { active.abort(); historyRequest?.abort() })
</script>

<template>
  <a class="back-link" :href="home + '/activities'"><Icon name="back" />返回处理活动</a>
  <p v-if="loading && !activity" class="message" role="status">正在读取活动详情…</p>
  <div v-if="loadError" class="message message-error page-feedback"><p role="alert">{{ loadError }}</p><button type="button" class="button button-secondary" :disabled="loading" @click="load()">重新读取</button></div>
  <template v-if="activity">
    <header class="page-header"><div><p class="eyebrow">处理活动 <span class="mono">#{{ activity.id }}</span></p><h1>{{ activity.name }}</h1><p>{{ activityKindLabels[activity.kind] }} · {{ activity.assetName }}</p></div><div class="page-actions"><button v-if="canEdit && !editing" class="button button-secondary" type="button" :disabled="loading || !activity.revision" @click="beginEdit">编辑活动</button><a v-if="canManageChecks(workspace.role)" class="button button-primary" :href="home + '/checks/new/' + activity.id"><Icon name="plus" />发起关联检查</a><a class="button button-secondary" :href="home + '/reports/' + activity.id"><Icon name="reports" />查看活动报告</a></div></header>
    <p v-if="notice" class="message message-success page-feedback" role="status">{{ notice }}</p>
    <section v-if="editing && canEdit" class="panel activity-edit" aria-labelledby="activity-edit-title" :aria-busy="saving || loading"><div class="panel-heading"><div><h2 id="activity-edit-title" ref="editHeading" tabindex="-1">编辑活动 · 基于修订 v{{ draftRevision }}</h2><p>关联资产保持不变，保存全部已登记属性。</p></div></div><form class="activity-edit-form" @submit.prevent="save"><div v-if="formError" class="message message-error activity-edit-wide" role="alert"><p>{{ formError }}</p><button v-if="conflict" class="button button-secondary" type="button" :disabled="loading || saving" @click="load(true)">{{ loading ? '读取中…' : '重新读取并替换未保存输入' }}</button></div><div class="field"><label for="activity-edit-name">活动名称</label><input id="activity-edit-name" v-model="draft.name" maxlength="100" required></div><div class="field"><label for="activity-edit-kind">活动类型</label><select id="activity-edit-kind" v-model="draft.kind"><option v-for="(label, key) in activityKindLabels" :key="key" :value="key">{{ label }}</option></select></div><div class="activity-edit-asset activity-edit-wide">关联资产：{{ activity.assetName }} · {{ activity.assetSystem }} · #{{ activity.assetId }}</div><div class="field activity-edit-wide"><label for="activity-edit-purpose">处理目的</label><textarea id="activity-edit-purpose" v-model="draft.purpose" maxlength="400" required></textarea></div><div class="field"><label for="activity-edit-recipient">{{ draft.kind === 'ai' ? '模型服务或供应商' : '接收方' }}</label><input id="activity-edit-recipient" v-model="draft.recipient" maxlength="100" :required="draft.kind === 'cross_border'"></div><div class="field"><label for="activity-edit-region">{{ draft.kind === 'cross_border' ? '出境目的地' : '处理地域' }}</label><input id="activity-edit-region" v-model="draft.region" maxlength="100" :required="draft.kind === 'cross_border'"></div><div class="field activity-edit-wide"><label for="activity-edit-retention">留存和训练条款</label><textarea id="activity-edit-retention" v-model="draft.retentionPolicy" maxlength="400"></textarea></div><div class="field activity-edit-wide"><label for="activity-edit-transfer">出境路径记录</label><textarea id="activity-edit-transfer" v-model="draft.transferPath" maxlength="400"></textarea><p class="field-hint">变更活动类型时保留已有路径；需要删除时请明确清空。</p></div><div class="field activity-edit-wide"><label for="activity-edit-safeguards">保护措施</label><textarea id="activity-edit-safeguards" v-model="draft.safeguards" maxlength="400"></textarea></div><div class="form-actions activity-edit-wide"><button class="button button-primary" type="submit" :disabled="saving || loading || conflict">{{ saving ? '保存中…' : '保存活动修订' }}</button><button class="button button-secondary" type="button" :disabled="saving || loading" @click="cancelEdit">取消编辑</button></div></form></section>
    <section class="panel activity-record" aria-labelledby="activity-record-title"><div class="panel-heading"><div><h2 id="activity-record-title">活动信息</h2><p>按活动记录处理事实，在关联检查中核对适用要求和证据。</p></div><span class="badge">{{ activityKindLabels[activity.kind] }}<span v-if="activity.revision"> · v{{ activity.revision }}</span></span></div><dl class="detail-list"><div><dt>关联资产</dt><dd>{{ activity.assetName }} · {{ activity.assetSystem }}</dd></div><div><dt>登记时间</dt><dd class="date-text"><time :datetime="activity.createdAt">{{ formatDate(activity.createdAt) }}</time></dd></div><div v-if="activity.updatedAt"><dt>最近修订</dt><dd><time :datetime="activity.updatedAt">{{ formatDate(activity.updatedAt) }}</time></dd></div><div><dt>{{ activity.kind === 'ai' ? '模型服务或供应商' : '接收方' }}</dt><dd>{{ activity.recipient || '未记录' }}</dd></div><div><dt>{{ activity.kind === 'cross_border' ? '出境目的地' : '处理地域' }}</dt><dd>{{ activity.region || '未记录' }}</dd></div><div class="detail-wide"><dt>处理目的</dt><dd class="text-block">{{ activity.purpose }}</dd></div><div class="detail-wide"><dt>留存和训练条款</dt><dd class="text-block">{{ activity.retentionPolicy || '未记录' }}</dd></div><div v-if="activity.kind === 'cross_border' || activity.transferPath" class="detail-wide"><dt>{{ activity.kind === 'cross_border' ? '拟采用的出境路径' : '既有出境路径记录' }}</dt><dd class="text-block">{{ activity.transferPath || '未记录' }}</dd></div><div v-if="activity.safeguards" class="detail-wide"><dt>保护措施</dt><dd class="text-block">{{ activity.safeguards }}</dd></div></dl><div v-if="activityCheckHints[activity.kind]" class="activity-guidance"><strong>候选检查范围</strong><p>{{ activityCheckHints[activity.kind]?.scope }}</p></div></section>
    <section class="panel activity-history" aria-labelledby="activity-history-title" :aria-busy="historyLoading"><div class="panel-heading"><div><h2 id="activity-history-title">修订历史</h2><p>逐次保留修改人、修订版本和前后变化。</p></div><div class="page-actions"><button class="button button-secondary" type="button" :disabled="historyLoading" @click="loadHistory()">刷新历史</button><a class="button button-secondary" :href="apiUrl + '/history/export'"><Icon name="download" />导出修订历史</a></div></div><p v-if="historyError" class="message message-error" role="alert">{{ historyError }}</p><p v-if="historyLoading && !history.length" class="empty-state" role="status">正在读取修订历史…</p><div v-if="history.length" class="activity-history-list"><details v-for="(entry, index) in history" :key="entry.id" :open="index === 0"><summary><strong>修订 v{{ entry.revision }}</strong><span>{{ entry.actor }}</span><time :datetime="entry.createdAt">{{ formatDate(entry.createdAt) }}</time><span>{{ changes(entry).length }} 项属性变化</span></summary><div v-if="changes(entry).length" class="table-scroll" role="region" tabindex="0" :aria-label="'修订v' + entry.revision + '的前后变化'"><table class="data-table activity-history-table"><thead><tr><th scope="col">属性</th><th scope="col">修改前</th><th scope="col">修改后</th></tr></thead><tbody><tr v-for="field in changes(entry)" :key="field.key"><th scope="row">{{ field.label }}</th><td class="text-block">{{ display(entry.before, field.key) }}</td><td class="text-block">{{ display(entry.after, field.key) }}</td></tr></tbody></table></div><p v-else class="empty-state">该记录未包含属性差异。</p></details></div><p v-else-if="!historyLoading && !historyError" class="empty-state">尚无修订历史。保存实际变化后，会在这里保留前后版本。</p><div v-if="historyCursor !== null" class="panel-footer"><button class="button button-secondary" type="button" :disabled="historyLoading" @click="loadHistory(true)">{{ historyLoading ? '读取中…' : '读取更早修订' }}</button></div></section>
    <ChecksView :workspace="workspace" :activity-id="activity.id" @session-expired="emit('sessionExpired')" />
  </template>
</template>

<style scoped>
.activity-edit, .activity-history { margin: 1.2rem 0; }
.activity-edit-form { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: .9rem 1rem; padding: 1rem; }
.activity-edit-form .field { min-width: 0; margin: 0; }
.activity-edit-wide { grid-column: 1 / -1; }
.activity-edit-form .form-actions { margin-top: 0; }
.activity-edit-form .message .button { margin-top: .65rem; }
.activity-edit-asset { padding: .6rem .75rem; background: var(--surface-subtle); border-radius: .4rem; color: var(--secondary); font-size: .8rem; }
.activity-history-list > details + details { border-top: 1px solid var(--line); }
.activity-history-list summary { display: flex; flex-wrap: wrap; align-items: center; gap: .4rem 1rem; min-height: 3rem; padding: .8rem 1rem; cursor: pointer; }
.activity-history-list summary > span, .activity-history-list summary > time { color: var(--secondary); font-size: .78rem; }
.activity-history-table { min-width: 34rem; table-layout: fixed; }
.activity-history-table th:first-child { width: 18%; }
@media (max-width: 43.99rem) { .activity-edit-form { grid-template-columns: minmax(0, 1fr); } }
</style>
