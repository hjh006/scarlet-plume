<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ApiError, describeError, request, workspacePath, type Workspace } from './api'
import type { DiscoveryRule } from './discovery'

interface Outcome { index: number; matched: boolean; expected?: boolean; skipped?: boolean }
interface Calibration { matched: number; scanned: number; truePositive: number; falsePositive: number; trueNegative: number; falseNegative: number; precision: number | null; recall: number | null; accuracy: number | null; outcomes: Outcome[]; warnings: string[] }
const props = defineProps<{ workspace: Workspace; rule?: DiscoveryRule }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const positive = ref('')
const negative = ref('')
const result = ref<Calibration | null>(null)
const busy = ref(false)
const error = ref('')
const editor = computed(() => ['owner', 'operator'].includes(props.workspace.role))
let controller: AbortController | undefined
const lines = (text: string) => text.split(/\r?\n/).map(value => value.trim()).filter(Boolean)
const sampleCount = computed(() => lines(positive.value).length + lines(negative.value).length)
const percent = (value: number | null) => value === null ? '无法计算' : (value * 100).toFixed(1) + '%'
function reset() { controller?.abort(); result.value = null; error.value = ''; busy.value = false }
watch(() => [JSON.stringify(props.rule), positive.value, negative.value], reset)
function loadExamples() {
  positive.value = props.rule?.target === 'content' ? 'demo@example.invalid' : 'customer_email 电子邮箱 varchar(128)'
  negative.value = props.rule?.target === 'content' ? 'synthetic-no-match' : 'product_title 商品名称 varchar(100)'
}
async function test() {
  if (!props.rule || !editor.value || busy.value) return
  controller?.abort()
  const operation = new AbortController()
  controller = operation
  busy.value = true; error.value = ''; result.value = null
  try {
    const samples = [...lines(positive.value).map(value => ({ value, expected: true })), ...lines(negative.value).map(value => ({ value, expected: false }))]
    const value = await request<Calibration>(workspacePath(props.workspace.slug) + '/discovery/rules/test', { method: 'POST', signal: operation.signal, body: JSON.stringify({ rule: props.rule, samples }) })
    if (!operation.signal.aborted) result.value = value
  } catch (cause) {
    if (!operation.signal.aborted) {
      if (cause instanceof ApiError && cause.status === 401) emit('sessionExpired')
      error.value = describeError(cause)
    }
  } finally { if (!operation.signal.aborted) busy.value = false }
}
onBeforeUnmount(() => controller?.abort())
</script>

<template>
  <section class="rule-calibration" aria-labelledby="rule-calibration-title" :aria-busy="busy">
    <div class="calibration-heading"><div><h3 id="rule-calibration-title">规则测试与校准</h3><p>用合成正反例核对当前规则的命中、漏报和误报。编辑规则或样本后，旧结果自动清除。</p></div></div>
    <p v-if="!editor" class="field-hint">当前身份可查看规则；测试和校准由负责人或经办人执行。</p>
    <form v-else @submit.prevent="test">
      <div class="calibration-examples">
        <label class="field">预期命中的合成正例<textarea v-model="positive" rows="4" maxlength="60000" placeholder="每行一条，不要填写真实客户数据或凭据。"></textarea></label>
        <label class="field">预期不命中的合成反例<textarea v-model="negative" rows="4" maxlength="60000" placeholder="每行一条，可留空。"></textarea></label>
      </div>
      <div class="calibration-actions"><button class="button button-secondary" type="button" @click="loadExamples">载入邮箱合成示例</button><button class="button button-primary" type="submit" :disabled="busy || !rule || sampleCount < 1 || sampleCount > 100">{{ busy ? '正在校准…' : '测试当前规则' }}</button><span>{{ sampleCount }} / 100 条</span></div>
      <p class="field-hint">示例用于演示格式，请按实际规则调整样本和预期。单条最多 1,000 字符，总请求最多 64 KiB；内容采样按采集器限制跳过超过 256 字节的值。</p>
    </form>
    <p v-if="error" class="message message-error" role="alert">{{ error }}</p>
    <div v-if="result" class="calibration-result" aria-live="polite">
      <dl class="calibration-metrics"><div><dt>命中 / 已测试</dt><dd>{{ result.matched }} / {{ result.scanned }}</dd></div><div><dt>精确率</dt><dd>{{ percent(result.precision) }}</dd></div><div><dt>召回率</dt><dd>{{ percent(result.recall) }}</dd></div><div><dt>准确率</dt><dd>{{ percent(result.accuracy) }}</dd></div></dl>
      <p>正例命中 {{ result.truePositive }} · 误报 {{ result.falsePositive }} · 漏报 {{ result.falseNegative }} · 反例未命中 {{ result.trueNegative }}</p>
      <p class="field-hint">以上统计仅适用于本次合成样本，不代表生产数据识别准确率。测试不会修改或发布规则。</p>
      <div class="table-scroll" role="region" aria-label="合成样本逐项测试结果" tabindex="0"><table class="data-table"><thead><tr><th scope="col">样本序号</th><th scope="col">预期</th><th scope="col">实际结果</th></tr></thead><tbody><tr v-for="outcome in result.outcomes" :key="outcome.index"><th scope="row">{{ outcome.index + 1 }}</th><td>{{ outcome.expected === true ? '应命中' : outcome.expected === false ? '不应命中' : '未设置' }}</td><td>{{ outcome.skipped ? '超过内容采样长度，已跳过' : outcome.matched ? '命中' : '未命中' }}</td></tr></tbody></table></div>
    </div>
  </section>
</template>

<style scoped>
.rule-calibration { padding: 22px; border-top: 1px solid var(--line); }
.calibration-heading h3 { margin: 0 0 8px; font-size: 15px; }
.calibration-heading p, .rule-calibration .field-hint { color: var(--secondary); font-size: 12px; line-height: 1.8; }
.calibration-examples { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; margin: 18px 0; }
.calibration-examples .field { display: grid; gap: 8px; margin: 0; font-size: 12px; }
.calibration-examples textarea { width: 100%; resize: vertical; }
.calibration-actions { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; }
.calibration-actions span { color: var(--secondary); font-size: 12px; }
.calibration-metrics { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; margin: 20px 0; }
.calibration-metrics dt { color: var(--secondary); font-size: 12px; }
.calibration-metrics dd { margin: 5px 0 0; font-size: 18px; font-weight: 700; font-variant-numeric: tabular-nums; }
.calibration-result > p { font-size: 12px; line-height: 1.8; }
@media (max-width: 1100px) { .calibration-examples { grid-template-columns: minmax(0, 1fr); }.calibration-metrics { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
</style>
