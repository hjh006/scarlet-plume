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
  } catch (failure) { error.value = failure instanceof ApiError && failure.status === 409 ? '策略版本已变化。未保存的草稿仍已保留，请核对最新版本后重试。' : failureText(failure) }
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
    <div class="policy-workbench"><aside class="panel policy-catalog" aria-labelledby="detection-list-title" :aria-busy="loading"><div class="panel-heading"><h2 id="detection-list-title">检测规则</h2></div><p v-if="loading && !page" class="empty-state" role="status">正在读取检测策略…</p><nav v-else-if="page" aria-label="选择检测规则"><button v-for="policy in page.items" :key="policy.rule" type="button" class="policy-catalog-item" :aria-pressed="selected === policy.rule" :disabled="saving" @click="selected = policy.rule; choose()"><span>{{ runtimeDetectionLabels[policy.rule] }}</span><small :class="policy.enabled ? 'enabled' : ''">{{ policy.enabled ? '已启用' : '未启用' }}</small><code>{{ policy.revision ? 'v' + policy.revision : '系统默认' }}</code></button></nav></aside>
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
    <aside class="context-rail policy-context"><section class="panel"><div class="panel-heading"><h2>采用范围</h2></div><dl class="policy-adoption"><div><dt>当前规则</dt><dd>{{ form ? runtimeDetectionLabels[form.rule] : '正在读取' }}</dd></div><div><dt>配置修订</dt><dd>{{ form?.revision ? 'v' + form.revision : '系统默认' }}</dd></div><div><dt>工作区</dt><dd>{{ workspace.name }}</dd></div><div><dt>编辑权限</dt><dd>{{ canEdit ? '负责人可保存新版本' : '只读，不能修改策略' }}</dd></div></dl></section><section class="panel"><div class="panel-heading"><h3>核验提示</h3></div><div class="policy-context-body"><p>检测按来源分别计算窗口与分母，策略变更不会改写历史采用版本。</p><p>模式信号不能证明漏洞利用成功；未知或未采集不作为安全通过。</p></div></section><section v-if="page" class="panel"><div class="panel-heading"><h3>采集预算</h3></div><dl class="policy-adoption"><div><dt>保留窗口</dt><dd>{{ page.retentionHours }} 小时</dd></div><div><dt>每来源样本</dt><dd>最多 {{ page.sampleLimitPerSource.toLocaleString() }} 条</dd></div><div><dt>检测窗口</dt><dd>最长 {{ page.windowLimitMinutes }} 分钟</dd></div></dl><p class="policy-context-body">原始请求、响应、SQL 与凭据保留在来源侧。</p></section></aside></div>

  </div>
</template>

<style scoped>
.policy-workbench{display:grid;grid-template-columns:210px minmax(0,1fr) 280px;gap:16px;align-items:start}.policy-catalog nav{display:grid}.policy-catalog-item{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:5px 9px;align-items:center;width:100%;min-height:67px;padding:14px 16px;border:0;border-bottom:1px solid var(--line);background:white;color:var(--ink);font-size:12px;text-align:left;cursor:pointer}.policy-catalog-item:last-child{border-bottom:0}.policy-catalog-item[aria-pressed=true]{background:#edf9fc;box-shadow:inset 3px 0 #20b3ce}.policy-catalog-item:hover{background:#f0f7fc}.policy-catalog-item small{font-size:10px;color:var(--secondary);background:#edf1f6;padding:3px 5px;border-radius:4px}.policy-catalog-item small.enabled{background:#e2f5ef;color:#14785c}.policy-catalog-item code{grid-column:1/-1;color:var(--secondary);font-size:10px}.policy-adoption{padding:6px 18px 12px}.policy-adoption>div{display:grid;grid-template-columns:75px minmax(0,1fr);gap:10px;padding:12px 0;border-bottom:1px solid var(--line);font-size:11px}.policy-adoption>div:last-child{border-bottom:0}.policy-adoption dt{color:var(--secondary)}.policy-adoption dd{margin:0;overflow-wrap:anywhere}.policy-context-body{padding:16px 18px;font-size:11px;line-height:1.8;color:var(--secondary)}.policy-context-body p+p{margin-top:12px}.policy-context{display:grid;gap:16px}.policy-editor>form{padding:18px}.policy-editor .panel-heading p{font-size:11px}.policy-history{padding:16px 18px;border-top:1px solid var(--line)}@media(min-width:1100px) and (max-width:1400px){.policy-workbench{grid-template-columns:180px minmax(0,1fr) 240px}.policy-catalog-item{padding:12px;font-size:11px}.policy-fields{gap:12px!important}}

.policy-editor{margin-top:0;min-width:0}.policy-fields{border:0;padding:0;display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:1.25rem}.policy-toggle,.policy-categories{grid-column:1/-1}.policy-toggle,.policy-categories label{display:flex;align-items:center;gap:.65rem;min-height:44px}.policy-categories{display:flex;flex-wrap:wrap;gap:.5rem 1.25rem;border:1px solid var(--line);border-radius:12px;padding:1rem}.policy-categories legend{padding:0 .4rem}.policy-note{margin:1rem 0;line-height:1.7}.policy-fields input[type=checkbox]{width:18px;height:18px}.policy-editor .page-actions{align-items:center}.policy-history{margin-top:1.5rem}.policy-history .table-scroll{margin-top:1rem}@media(max-width:760px){.policy-fields{grid-template-columns:1fr}.policy-categories{gap:.25rem 1rem}}
.policy-fields .field{margin:0;align-self:start}.policy-note{font-size:11px;color:var(--secondary)}.policy-toggle,.policy-categories label{font-size:12px;min-height:36px}.policy-fields .field label,.policy-categories legend{font-size:11px}.policy-editor .page-actions>span{font-size:11px}.policy-history{margin-top:0}.policy-catalog{overflow:hidden}
</style>
