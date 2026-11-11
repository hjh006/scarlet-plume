<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { lifecycleCategoryLabels } from './api-lifecycle'
import { runtimeDetectionDescriptions, runtimeDetectionLabels, type RuntimeDetectionPolicy, type RuntimePolicyPage } from './runtime-policies'

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const endpoint = workspacePath(props.workspace.slug) + '/api-security/runtime/policies'
const active = new AbortController()
const page = ref<RuntimePolicyPage | null>(null)
const selected = ref('sensitive_response')
const form = ref<RuntimeDetectionPolicy | null>(null)
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const saved = ref('')
const history = ref<RuntimeDetectionPolicy[]>([])
const historyOpen = ref(false)
const historyLoading = ref(false)
const historyError = ref('')
let historyRequest = 0
const canEdit = computed(() => props.workspace.role === 'owner')
const windowRule = computed(() => ['subject_bulk_access', 'error_spike', 'off_hours'].includes(selected.value))
const home = `#/${encodeURIComponent(props.workspace.slug)}`

function failureText(failure: unknown) {
  if (active.signal.aborted) return ''
  if (failure instanceof ApiError && failure.status === 401) { emit('sessionExpired'); return '' }
  return describeError(failure)
}
function choose() {
  const policy = page.value?.items.find(item => item.rule === selected.value)
  form.value = policy ? { ...policy, categories: [...policy.categories] } : null
  error.value = ''; saved.value = ''
  history.value = []; historyError.value = ''
  if (historyOpen.value) void loadHistory()
}
async function loadHistory() {
  const sequence = ++historyRequest
  const rule = selected.value
  historyLoading.value = true; historyError.value = ''
  try {
    const result = await request<{ items: RuntimeDetectionPolicy[] }>(endpoint + '/history?rule=' + encodeURIComponent(rule), { signal: active.signal })
    if (sequence === historyRequest && rule === selected.value) history.value = result.items
  } catch (failure) { if (sequence === historyRequest) historyError.value = failureText(failure) }
  finally { if (sequence === historyRequest) historyLoading.value = false }
}
async function load() {
  if (loading.value || saving.value) return
  loading.value = true; error.value = ''
  try { page.value = await request<RuntimePolicyPage>(endpoint, { signal: active.signal }); choose() }
  catch (failure) { error.value = failureText(failure) }
  finally { loading.value = false }
}
async function save() {
  if (!form.value || !canEdit.value || saving.value) return
  saving.value = true; error.value = ''; saved.value = ''
  try {
    const result = await request<RuntimeDetectionPolicy>(endpoint, { method: 'PUT', body: JSON.stringify(form.value), signal: active.signal })
    if (page.value) page.value.items = page.value.items.map(item => item.rule === result.rule ? result : item)
    form.value = { ...result, categories: [...result.categories] }
    saved.value = `已保存版本 ${result.revision}，后续观测使用此版本。历史命中和证据保留原依据。`
    if (historyOpen.value) void loadHistory()
  } catch (failure) { error.value = failureText(failure) }
  finally { saving.value = false }
}
onMounted(() => { void load() })
onBeforeUnmount(() => active.abort())
</script>

<template>
  <div>
    <header class="page-header">
      <div><p class="eyebrow">安全运营</p><h1>运行时检测策略</h1><p>配置真实观测信号的阈值，将命中送入检查、整改和独立复核。</p></div>
      <div class="page-actions"><a class="button button-secondary" :href="home + '/api-security/runtime'">运行时发现</a><button class="button button-secondary" type="button" :disabled="loading || saving" @click="load">刷新策略</button></div>
    </header>
    <p v-if="error" class="message message-error" role="alert">{{ error }}<span v-if="error.includes('冲突')"> 请刷新当前策略后重新修改。</span></p>
    <p v-if="saved" class="message message-success" role="status">{{ saved }}</p>
    <section class="panel" aria-labelledby="detection-list-title" :aria-busy="loading">
      <div class="panel-heading"><div><h2 id="detection-list-title">检测规则</h2><p>策略按工作区隔离；窗口按来源分别计数，避免多个探针重复累计。未知或未采集不视为安全通过。</p></div></div>
      <p v-if="loading && !page" class="empty-state" role="status">正在读取检测策略…</p>
      <div v-else-if="page" class="table-scroll" tabindex="0" role="region" aria-labelledby="detection-list-title">
        <table class="data-table"><thead><tr><th scope="col">规则</th><th scope="col">状态</th><th scope="col">当前版本</th><th scope="col">窗口与阈值</th><th scope="col">操作</th></tr></thead><tbody>
          <tr v-for="policy in page.items" :key="policy.rule"><th scope="row">{{ runtimeDetectionLabels[policy.rule] }}<code class="table-secondary">{{ policy.rule }}</code></th><td><span class="badge">{{ policy.enabled ? '已启用' : '未启用' }}</span></td><td>{{ policy.revision ? `v${policy.revision}` : '系统默认' }}<span v-if="policy.createdAt" class="table-secondary">{{ formatDate(policy.createdAt) }}</span></td><td>{{ policy.windowMinutes }} 分钟 · 命中 ≥ {{ policy.threshold }}<span class="table-secondary">至少 {{ policy.minRequests }} 次请求{{ policy.rule === 'error_spike' ? ` · 错误 ≥ ${policy.errorPercent}%` : '' }}</span></td><td><button class="button button-secondary" type="button" :disabled="saving" :aria-pressed="selected === policy.rule" @click="selected = policy.rule; choose()">{{ canEdit ? '配置' : '查看' }}</button></td></tr>
        </tbody></table>
      </div>
    </section>
    <section v-if="form" class="panel policy-editor" aria-labelledby="detection-edit-title">
      <div class="panel-heading"><div><h2 id="detection-edit-title">{{ runtimeDetectionLabels[selected] }}</h2><p>{{ runtimeDetectionDescriptions[selected] }}</p></div></div>
      <p v-if="!canEdit" class="message">只有工作区负责人可以修改策略。当前显示已生效配置。</p>
      <form @submit.prevent="save">
        <fieldset :disabled="!canEdit || saving" class="policy-fields">
          <legend class="sr-only">{{ runtimeDetectionLabels[selected] }}配置</legend>
          <label class="policy-toggle"><input v-model="form.enabled" type="checkbox">启用此规则</label>
          <div class="field"><label for="policy-window">统计窗口（分钟）</label><input id="policy-window" v-model.number="form.windowMinutes" type="number" min="1" max="60" required></div>
          <div class="field"><label for="policy-threshold">最少命中计数</label><input id="policy-threshold" v-model.number="form.threshold" type="number" min="1" max="10000" required></div>
          <div class="field"><label for="policy-minimum">窗口最少请求数</label><input id="policy-minimum" v-model.number="form.minRequests" type="number" min="1" max="10000" required></div>
          <div v-if="selected === 'error_spike'" class="field"><label for="policy-error">错误比例（%）</label><input id="policy-error" v-model.number="form.errorPercent" type="number" min="1" max="100" required></div>
          <template v-if="selected === 'off_hours'"><div class="field"><label for="policy-start">监测时段开始（小时）</label><input id="policy-start" v-model.number="form.startHour" type="number" min="0" max="23" required></div><div class="field"><label for="policy-end">监测时段结束（小时）</label><input id="policy-end" v-model.number="form.endHour" type="number" min="0" max="23" required></div><div class="field"><label for="policy-timezone">时区偏移（分钟，东八区为 480）</label><input id="policy-timezone" v-model.number="form.timezoneOffsetMinutes" type="number" min="-720" max="840" required></div></template>
          <fieldset v-if="selected === 'sensitive_response'" class="policy-categories"><legend>监测类别</legend><label v-for="(label, category) in lifecycleCategoryLabels" :key="category"><input v-model="form.categories" type="checkbox" :value="category">{{ label }}</label></fieldset>
        </fieldset>
        <p class="muted policy-note">{{ windowRule ? '窗口规则须在整改后经过完整配置窗口并获得足够新观测，才能自动复测。' : '整改复测须有同来源的新鲜明确观测；无数据、检查不完整或认证未知均不能替代复测。' }} 关闭或改宽阈值不会自动将检查标记为通过。</p>
        <div v-if="canEdit" class="page-actions"><button class="button button-primary" type="submit" :disabled="saving || loading">{{ saving ? '正在保存…' : '保存新版本' }}</button><span class="muted">当前 {{ form.revision ? `v${form.revision}` : '系统默认' }}，保存后生成独立版本。</span></div>
      </form>
      <div class="policy-history">
        <button class="button button-secondary" type="button" :aria-expanded="historyOpen" aria-controls="policy-version-history" @click="historyOpen = !historyOpen; historyOpen && loadHistory()">{{ historyOpen ? '收起版本历史' : '查看版本历史' }}</button>
        <div v-if="historyOpen" id="policy-version-history" :aria-busy="historyLoading">
          <p v-if="historyError" class="message message-error" role="alert">{{ historyError }}</p>
          <p v-else-if="historyLoading" class="empty-state" role="status">正在读取版本历史…</p>
          <div v-else-if="history.length" class="table-scroll" tabindex="0" role="region" aria-label="最近 30 个策略版本"><table class="data-table"><thead><tr><th scope="col">版本</th><th scope="col">保存时间</th><th scope="col">状态</th><th scope="col">保存的阈值</th></tr></thead><tbody><tr v-for="version in history" :key="version.id"><th scope="row">v{{ version.revision }}</th><td>{{ version.createdAt ? formatDate(version.createdAt) : '系统默认' }}</td><td>{{ version.enabled ? '启用' : '停用' }}</td><td>{{ version.windowMinutes }} 分钟 · 命中 ≥ {{ version.threshold }} · 至少 {{ version.minRequests }} 次请求<span v-if="version.rule === 'error_spike'" class="table-secondary">错误比例 ≥ {{ version.errorPercent }}%</span><span v-if="version.rule === 'off_hours'" class="table-secondary">{{ version.startHour }} 时至 {{ version.endHour }} 时，UTC 偏移 {{ version.timezoneOffsetMinutes }} 分钟</span><span v-if="version.rule === 'sensitive_response'" class="table-secondary">{{ version.categories.map(category => lifecycleCategoryLabels[category] || category).join('、') }}</span></td></tr></tbody></table></div>
          <p v-else class="empty-state">尚无人工保存版本，当前使用系统默认策略。</p>
        </div>
      </div>
    </section>
    <p v-if="page" class="muted policy-note">检测样本保留 {{ page.retentionHours }} 小时，每来源最多 {{ page.sampleLimitPerSource.toLocaleString() }} 条；窗口最长 {{ page.windowLimitMinutes }} 分钟。原始请求、响应、SQL 和凭据不进入平台。疑似攻击模式不能作为漏洞已验证的结论。</p>
  </div>
</template>

<style scoped>
.policy-editor{margin-top:1.25rem}.policy-fields{border:0;padding:0;display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:1.25rem}.policy-toggle,.policy-categories{grid-column:1/-1}.policy-toggle,.policy-categories label{display:flex;align-items:center;gap:.65rem;min-height:44px}.policy-categories{display:flex;flex-wrap:wrap;gap:.5rem 1.25rem;border:1px solid var(--line);border-radius:12px;padding:1rem}.policy-categories legend{padding:0 .4rem}.policy-note{margin:1rem 0;line-height:1.7}.policy-fields input[type=checkbox]{width:18px;height:18px}.policy-editor .page-actions{align-items:center}.policy-history{margin-top:1.5rem}.policy-history .table-scroll{margin-top:1rem}@media(max-width:760px){.policy-fields{grid-template-columns:1fr}.policy-categories{gap:.25rem 1rem}}
</style>
