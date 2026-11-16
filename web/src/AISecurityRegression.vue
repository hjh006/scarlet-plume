<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ApiError, describeError, formatDate, request } from './api'
import { aiDecisionLabel, aiString, type AISecurityBundle } from './ai-security'
import { aiText, type AIRecord } from './ai-security-editor'
import AISecurityEvidence from './AISecurityEvidence.vue'

const props = defineProps<{ base: string; bundle: AISecurityBundle; canWrite: boolean; canReview: boolean; draftChanged: boolean }>()
const emit = defineEmits<{ refresh: []; sessionExpired: [] }>()
const controller = new AbortController()
const packs = ref<AIRecord[]>([]), requiredDimensions = ref<string[]>([])
const currentEngine = ref(''), currentInspection = ref('')
const loading = ref(false), busy = ref(''), error = ref(''), feedback = ref('')
const selectedPack = ref(''), packageJSON = ref(''), preview = ref<AIRecord | null>(null)
const selectedEvidence = ref<AIRecord | null>(null), comparison = ref<AIRecord | null>(null)
const selectedPackEvidence = ref<AIRecord | null>(null)
const fromEvaluation = ref(''), toEvaluation = ref(''), publishEvaluation = ref(''), publishNote = ref('')
const previewInput = ref('')
const canImport = computed(() => preview.value !== null && previewInput.value === packageJSON.value)
const eligible = computed(() => props.bundle.evaluations.filter(item => item.gateEligible === true && Number(item.configurationRevision) === props.bundle.revision && currentEngine.value && item.engineVersion===currentEngine.value && item.inspectionVersion===currentInspection.value && currentPack(item)))
watch(packageJSON, () => { preview.value = null; previewInput.value = '' })
watch(() => props.bundle.revision, () => { publishEvaluation.value = ''; comparison.value = null })
onUnmounted(() => controller.abort())
onMounted(loadPacks)

function records(value: unknown): AIRecord[] {
  return Array.isArray(value) ? value.filter((item): item is AIRecord => item !== null && typeof item === 'object' && !Array.isArray(item)) : []
}
const dimensionLabels:Record<string,string>={identity:'身份边界',request_lifetime:'请求时效',rag:'知识权限',model:'模型外发',tool:'工具授权',deny_all:'明确停用'}
function strings(value:unknown):string[]{return Array.isArray(value)?value.filter((item):item is string=>typeof item==='string'):[]}
function dimensions(value:unknown){return strings(value).map(item=>dimensionLabels[item] || item).join('、')}
function packLabel(value:unknown){const p=value as AIRecord|null;return p&&typeof p==='object'?aiString(p,'packId')+' · '+aiString(p,'version'):'未采用版本化测试包'}
function caseResult(value:unknown){const c=value as AIRecord|null;if(!c||typeof c!=='object')return '未提供';return (c.status==='not_applicable'?'不适用':c.passed===true?'通过':'未通过')+' · '+aiString(c,'actual')+' · '+aiString(c,'code')}
function fail(cause: unknown) {
  if (controller.signal.aborted) return
  if (cause instanceof ApiError && cause.status === 401) emit('sessionExpired')
  error.value = describeError(cause)
}
async function loadPacks() {
  loading.value = true; error.value = ''
  try {
    const out = await request<AIRecord>(props.base + '/regression-packs', { signal: controller.signal })
    packs.value = records(out.items)
    currentEngine.value = aiString(out,'engineVersion'); currentInspection.value = aiString(out,'inspectionVersion')
    requiredDimensions.value = Array.isArray(out.requiredDimensions) ? out.requiredDimensions as string[] : []
    if (selectedPack.value && !packs.value.some(item => String(item.id) === selectedPack.value)) selectedPack.value = ''
  } catch (cause) { fail(cause) } finally { loading.value = false }
}
async function action(name: string, run: () => Promise<void>) {
  if (busy.value) return
  busy.value = name; error.value = ''; feedback.value = ''
  try { await run() } catch (cause) { fail(cause) } finally { busy.value = '' }
}
async function baseline() {
  await action('baseline', async () => {
    const out = await request<AIRecord>(props.base + '/regression-packs/baseline', { signal: controller.signal })
    packageJSON.value = JSON.stringify(out, null, 2)
    feedback.value = '基线已载入待核对区，尚未导入或发布。'
  })
}
async function viewPack(id: unknown) {
  await action('view-pack', async () => {
    selectedPackEvidence.value = await request<AIRecord>(props.base + '/regression-packs/' + encodeURIComponent(String(id)), { signal: controller.signal })
  })
}
async function validatePack() {
  if (!props.canWrite) return
  await action('validate', async () => {
    if (new TextEncoder().encode(packageJSON.value).length > 262144) throw new Error('测试包超过 256 KiB，请缩小范围。')
    const pack: unknown = JSON.parse(packageJSON.value)
    if (pack === null || typeof pack !== 'object' || Array.isArray(pack)) throw new Error('测试包必须为 JSON 对象。')
    const input = packageJSON.value
    const out = await request<AIRecord>(props.base + '/regression-packs/validate', { method: 'POST', body: JSON.stringify({ pack }), signal: controller.signal })
    if (packageJSON.value !== input) return
    preview.value = out; previewInput.value = input
    feedback.value = '校验预览已生成。请核对覆盖与摘要，再明确导入；校验不会执行上游动作。'
  })
}
async function importPack() {
  if (!props.canWrite || !canImport.value || !preview.value) return
  await action('import', async () => {
    const validated = preview.value!.pack as AIRecord
    if (!validated || !aiString(validated, 'digest')) throw new Error('尚未取得服务端校验后的测试包摘要。')
    const latest = packs.value.filter(item => item.packId === validated.packId).sort((a, b) => Number(b.id) - Number(a.id))[0]
    const out = await request<AIRecord>(props.base + '/regression-packs', { method: 'POST', body: JSON.stringify({ expectedLatestVersion: latest ? aiString(latest, 'version') : '', pack: validated }), signal: controller.signal })
    selectedPack.value = aiString(out, 'id'); preview.value = null; previewInput.value = ''
    await loadPacks(); feedback.value = '测试包版本已冻结导入；尚未运行回归或发布配置。'
  })
}
async function runEvaluation(legacy = false) {
  if (!props.canWrite || props.draftChanged || !legacy && !selectedPack.value) return
  await action('evaluate', async () => {
    selectedEvidence.value = await request<AIRecord>(props.base + '/evaluations', { method: 'POST', body: JSON.stringify(legacy ? {} : { packId: Number(selectedPack.value) }), signal: controller.signal })
    emit('refresh'); feedback.value = '回归已完成。请核对失败项、覆盖缺口与发布资格，随后由独立账号复核。'
  })
}
async function compareEvaluations() {
  if (!fromEvaluation.value || !toEvaluation.value || fromEvaluation.value === toEvaluation.value) return
  await action('compare', async () => {
    comparison.value = await request<AIRecord>(props.base + '/evaluations/' + toEvaluation.value + '/compare?against=' + encodeURIComponent(fromEvaluation.value), { signal: controller.signal })
  })
}
async function publish() {
  if (!props.canReview || props.draftChanged || !publishEvaluation.value) return
  await action('publish', async () => {
    if (new TextEncoder().encode(publishNote.value).length > 1000) throw new Error('复核意见不能超过 1000 字节。')
    await request(props.base + '/publish', { method: 'POST', body: JSON.stringify({ expectedRevision: props.bundle.revision, evaluationId: Number(publishEvaluation.value), note: publishNote.value }), signal: controller.signal })
    publishEvaluation.value = ''; publishNote.value = ''; emit('refresh')
    feedback.value = '已独立复核并发布此配置修订。客户侧仍需采用版本并验证实际执行。'
  })
}
function gateLabel(item: AIRecord) {
  if (item.gateEligible === true && !currentPack(item)) return '测试包已有新版本或超出当前列表，需要核对并重新回归'
  if (item.gateEligible === true && currentEngine.value && (item.engineVersion!==currentEngine.value || item.inspectionVersion!==currentInspection.value)) return '引擎或检测版本已变化，需要重新回归'
  if (item.gateEligible === true) return Number(item.configurationRevision) === props.bundle.revision ? '满足本次发布门禁' : '历史修订通过，不能发布当前修订'
  return item.gateVersion === 'legacy' || !item.gateVersion ? '旧诊断记录，不满足新发布门禁' : '未满足发布门禁'
}
function currentPack(item:AIRecord) {
  const pack=item.pack as AIRecord|undefined
  if(!pack)return false
  const latest=packs.value.filter(value=>value.packId===pack.packId).sort((a,b)=>Number(b.id)-Number(a.id))[0]
  return latest?.id===pack.id && latest?.digest===pack.digest
}
</script>

<template>
  <p v-if="error" class="message message-error" role="alert">{{ error }}</p>
  <p v-if="feedback" class="message message-success" role="status">{{ feedback }}</p>
  <section class="panel">
    <div class="ai-security-heading"><div><h2>让升级经过同一套安全回归</h2><p>测试包、配置、引擎与风险覆盖分别留版本。探针只运行策略引擎，实际模型和工具执行仍需客户侧验收。</p></div><button class="button button-secondary" :disabled="loading || !!busy" @click="loadPacks">刷新测试包</button></div>
    <div class="ai-security-body">
      <p v-if="loading" role="status">正在读取版本化测试包…</p>
      <p>基线可验证维度：{{ requiredDimensions.length ? dimensions(requiredDimensions) : '等待服务端提供维度' }}。发布时按实际启用范围核对必测探针。</p>
      <div class="ai-security-actions"><button class="button button-secondary" :disabled="!!busy" @click="baseline">载入平台基线</button></div>
      <details v-if="canWrite || packageJSON" :open="!!packageJSON" class="ai-regression-import">
        <summary>导入或更新合成探针测试包</summary>
        <p class="table-secondary">仅支持服务端登记的合成探针，不能写自由提示词、真实业务正文或密钥。新增测试内容不会自动修改客户策略。修改包后需使用新版本；重新计算摘要时先移除旧 digest，保留声明摘要时服务端将核对它。</p>
        <form @submit.prevent="validatePack">
          <label class="field">测试包 JSON<textarea v-model="packageJSON" :readonly="!canWrite" class="mono" rows="12" spellcheck="false" maxlength="262144" required></textarea></label>
          <div class="ai-security-actions"><button v-if="canWrite" class="button button-secondary" :disabled="!!busy">校验与预览</button></div>
        </form>
        <div v-if="preview" class="ai-security-result">
          <h3>服务端校验预览</h3>
          <p>基线维度缺口：{{ aiText(preview.coverageGaps) || '无维度缺口；实际发布还会核对当前配置的必测探针' }}</p>
          <pre class="ai-security-code">{{ JSON.stringify(preview,null,2) }}</pre>
          <button v-if="canWrite" class="button button-primary" :disabled="!!busy || !canImport" @click="importPack">冻结并导入此测试包版本</button>
        </div>
      </details>
    </div>
    <div v-if="packs.length" class="table-scroll" role="region" aria-label="版本化安全测试包" tabindex="0"><table class="data-table ai-regression-table"><thead><tr><th scope="col">测试包与版本</th><th scope="col">风险与必测维度</th><th scope="col">摘要与来源时点</th><th scope="col">核对</th></tr></thead><tbody><tr v-for="item in packs" :key="aiString(item,'id')"><th scope="row">{{ aiString(item,'packId') }}<span class="table-secondary">{{ aiString(item,'version') }} · {{ aiString(item,'caseCount') }} 个探针</span></th><td>{{ aiText(item.riskTags) }}<span class="table-secondary">{{ dimensions(item.requiredDimensions) }}</span></td><td><span class="table-secondary mono">{{ aiString(item,'digest') }}</span><span class="table-secondary">{{ formatDate(aiString(item,'createdAt')) }}</span></td><td><button class="button button-secondary" :disabled="!!busy" @click="viewPack(item.id)">查看冻结包</button></td></tr></tbody></table></div>
    <p v-else-if="!loading" class="empty-state">尚未导入版本化测试包。载入平台基线并校验后，再明确导入。</p>
    <div v-if="selectedPackEvidence" class="ai-security-body"><h3>冻结包内容，尚未运行</h3><p>{{ aiString(selectedPackEvidence,'packId') }} · {{ aiString(selectedPackEvidence,'version') }}</p><div class="table-scroll" role="region" aria-label="冻结测试包探针" tabindex="0"><table class="data-table ai-regression-table"><thead><tr><th scope="col">用例与标识</th><th scope="col">合成探针</th><th scope="col">风险维度</th><th scope="col">预期</th></tr></thead><tbody><tr v-for="item in records(selectedPackEvidence.cases)" :key="aiString(item,'id')"><th scope="row">{{ aiString(item,'name') }}<span class="table-secondary">{{ aiString(item,'id') }}</span></th><td>{{ aiString(item,'probe') }}</td><td>{{ aiString(item,'dimension') }}</td><td>{{ item.expectedOutcome==='configured'?'按配置门槛':aiDecisionLabel({outcome:item.expectedOutcome}) }}</td></tr></tbody></table></div><details><summary>查看完整冻结测试包 JSON</summary><pre class="ai-security-code">{{ JSON.stringify(selectedPackEvidence,null,2) }}</pre></details></div>
  </section>
  <section class="panel">
    <div class="ai-security-heading"><div><h2>运行并保存当前修订的回归</h2><p>回归结果绑定配置修订和测试包摘要；失败、探针不适用和缺少必测维度都会影响发布资格。</p></div></div>
    <form class="ai-security-body" @submit.prevent="runEvaluation(false)"><fieldset class="ai-security-fields" :disabled="!canWrite || !!busy || draftChanged"><label class="field field-wide">采用测试包<select v-model="selectedPack" required><option value="">选择已冻结测试包版本</option><option v-for="item in packs" :key="aiString(item,'id')" :value="aiString(item,'id')">{{ aiString(item,'packId') }} · {{ aiString(item,'version') }}</option></select></label></fieldset><p v-if="draftChanged" class="message message-warning">当前草稿未保存或基于旧修订，请先保存或重新载入。</p><div class="ai-security-actions"><button class="button button-primary" :disabled="!canWrite || !!busy || draftChanged || !selectedPack">{{ busy==='evaluate'?'正在运行…':'运行版本化安全回归' }}</button><button class="button button-secondary" type="button" :disabled="!canWrite || !!busy || draftChanged" @click="runEvaluation(true)">仅运行旧版诊断</button></div></form>
    <div v-if="bundle.evaluations.length" class="table-scroll" role="region" aria-label="安全回归与发布资格" tabindex="0"><table class="data-table ai-regression-table"><thead><tr><th scope="col">回归记录</th><th scope="col">采用版本</th><th scope="col">结果与缺口</th><th scope="col">证据</th></tr></thead><tbody><tr v-for="item in bundle.evaluations" :key="aiString(item,'id')"><th scope="row">#{{ aiString(item,'id') }}<span class="table-secondary">{{ formatDate(aiString(item,'createdAt')) }}</span></th><td>配置修订 {{ aiString(item,'configurationRevision') }}<span class="table-secondary">{{ packLabel(item.pack) }}</span><span class="table-secondary">{{ aiString(item,'engineVersion') }}</span></td><td><strong>{{ aiDecisionLabel(item) }} · {{ gateLabel(item) }}</strong><span v-if="strings(item.coverageGaps).length" class="table-secondary">{{ strings(item.coverageGaps).length }} 项覆盖缺口</span><details v-if="strings(item.coverageGaps).length"><summary>核对缺口代码</summary><p class="table-secondary mono">{{ aiText(item.coverageGaps) }}</p></details></td><td><button class="button button-secondary" @click="selectedEvidence=item">展开用例结果</button></td></tr></tbody></table></div>
    <p v-else class="empty-state">暂无已保存回归。</p>
    <div v-if="selectedEvidence" class="ai-security-body"><AISecurityEvidence :record="selectedEvidence" /></div>
  </section>
  <section class="panel">
    <div class="ai-security-heading"><div><h2>比较配置或测试包升级的影响</h2><p>用例增删和内容变化单独列示；相同标识但探针或预期变化，不能当成同一测试结果提升。</p></div></div>
    <form class="ai-security-body" @submit.prevent="compareEvaluations"><fieldset class="ai-security-fields" :disabled="!!busy"><label class="field">基准回归<select v-model="fromEvaluation" required><option value="">选择基准记录</option><option v-for="item in bundle.evaluations" :key="aiString(item,'id')" :value="aiString(item,'id')">#{{ aiString(item,'id') }} · 修订 {{ aiString(item,'configurationRevision') }}</option></select></label><label class="field">待比较回归<select v-model="toEvaluation" required><option value="">选择比较记录</option><option v-for="item in bundle.evaluations" :key="aiString(item,'id')" :value="aiString(item,'id')">#{{ aiString(item,'id') }} · 修订 {{ aiString(item,'configurationRevision') }}</option></select></label></fieldset><div class="ai-security-actions"><button class="button button-secondary" :disabled="!!busy || !fromEvaluation || !toEvaluation || fromEvaluation===toEvaluation">比较冻结结果</button></div></form>
    <div v-if="comparison" class="ai-security-body"><p>新增用例：{{ aiText(comparison.addedCaseIds) || '无' }}；移除用例：{{ aiText(comparison.removedCaseIds) || '无' }}</p><p>同标识发生变化：{{ records(comparison.changedCases).length }} 项；回归退化：{{ records(comparison.regressions).length }} 项。</p><div v-if="records(comparison.changedCases).length" class="table-scroll" role="region" aria-label="升级前后用例差异" tabindex="0"><table class="data-table ai-regression-table"><thead><tr><th scope="col">用例与变化类型</th><th scope="col">基准结果</th><th scope="col">当前结果</th></tr></thead><tbody><tr v-for="change in records(comparison.changedCases)" :key="aiString(change,'id')"><th scope="row">{{ aiString(change,'id') }}<span class="table-secondary">{{ change.definitionChanged?'用例定义变化':'同一定义的结果变化' }}</span></th><td>{{ caseResult(change.before) }}</td><td>{{ caseResult(change.after) }}</td></tr></tbody></table></div><details><summary>查看完整冻结比较 JSON</summary><pre class="ai-security-code">{{ JSON.stringify(comparison,null,2) }}</pre></details></div>
  </section>
  <section class="panel">
    <div class="ai-security-heading"><div><h2>独立复核并发布配置</h2><p>采用当前修订、完整必测覆盖且全部通过的版本化回归。配置修改人不能自行完成发布复核。</p></div></div>
    <form class="ai-security-body" @submit.prevent="publish"><fieldset class="ai-security-fields" :disabled="!canReview || !!busy || draftChanged"><label class="field field-wide">采用符合门禁的回归<select v-model="publishEvaluation" required><option value="">选择当前修订符合门禁的记录</option><option v-for="item in eligible" :key="aiString(item,'id')" :value="aiString(item,'id')">#{{ aiString(item,'id') }} · 修订 {{ aiString(item,'configurationRevision') }}</option></select></label><label class="field field-wide">独立复核意见<textarea v-model.trim="publishNote" required rows="3" maxlength="1000"></textarea></label></fieldset><div class="ai-security-actions"><button class="button button-primary" :disabled="!canReview || !!busy || draftChanged || !publishEvaluation">独立复核并发布此修订</button><p v-if="!canReview">当前账号可查看，发布需要负责人或复核人角色。</p></div></form>
  </section>
</template>

<style scoped>
.ai-regression-import { margin-top: 1rem; }
.ai-regression-import summary { cursor: pointer; padding-block: .6rem; font-weight: 600; }
.ai-regression-import form { margin-top: .8rem; }
.ai-regression-table { min-width: 760px; }
.ai-regression-table :is(th,td) { min-width: 140px; overflow-wrap: anywhere; }
</style>
