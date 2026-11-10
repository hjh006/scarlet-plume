<script setup lang="ts">
import { computed, ref } from 'vue'
import { formatDate, type Workspace } from './api'
import { runtimeRuleLabels, runtimeRiskSeverityLabels, type RuntimeFinding, type RuntimeRisk } from './runtime-findings'
import { checkStatusLabels } from './workflow'
import Icon from './Icon.vue'
import RuntimeRiskEditor from './RuntimeRiskEditor.vue'

const props = defineProps<{ findings: RuntimeFinding[]; home: string; loading: boolean; error: string; workspace: Workspace }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const localRisk = ref<Record<number, RuntimeRisk>>({})
const severity = ref<RuntimeRisk['severity'] | 'pending' | ''>('')
const overdueOnly = ref(false)
const today = new Intl.DateTimeFormat('sv-SE').format(new Date())
function riskFor(finding: RuntimeFinding) {
  const saved = localRisk.value[finding.id]
  return saved && saved.revision > (finding.risk?.revision ?? 0) ? saved : finding.risk
}
function isOverdue(finding: RuntimeFinding) {
  const due = riskFor(finding)?.dueDate
  return Boolean(due && due < today && finding.checkStatus !== 'verified')
}
const rule = ref<RuntimeFinding['rule'] | ''>('')
const status = ref<RuntimeFinding['checkStatus'] | 'open' | ''>('')
const limit = ref(25)
const filtered = computed(() => props.findings.filter(finding =>
  (!rule.value || finding.rule === rule.value) &&
  (!status.value || (status.value === 'open' ? finding.checkStatus !== 'verified' : finding.checkStatus === status.value)) &&
  (!severity.value || (severity.value === 'pending' ? !riskFor(finding) : riskFor(finding)?.severity === severity.value)) &&
  (!overdueOnly.value || isOverdue(finding)),
))
</script>

<template>
  <section class="panel api-panel runtime-findings" aria-labelledby="runtime-findings-title" :aria-busy="loading">
    <div class="api-panel-heading">
      <div>
        <h2 id="runtime-findings-title">运行时发现与风险管理</h2>
        <p>自动记录成功响应与契约的差异，并登记证据到 G03 监管检查。</p>
      </div>
      <span v-if="findings.length" class="api-secondary">已登记 {{ findings.length }} 项</span>
    </div>
    <div v-if="findings.length" class="filter-bar runtime-findings-filters">
      <label for="runtime-finding-rule">触发规则</label>
      <select id="runtime-finding-rule" v-model="rule" @change="limit = 25">
        <option value="">全部规则</option>
        <option v-for="(label, value) in runtimeRuleLabels" :key="value" :value="value">{{ label }}</option>
      </select>
      <label for="runtime-finding-status">检查状态</label>
      <select id="runtime-finding-status" v-model="status" @change="limit = 25">
        <option value="">全部状态</option>
        <option value="open">未闭环</option>
        <option v-for="(label, value) in checkStatusLabels" :key="value" :value="value">{{ label }}</option>
      </select>
      <label for="runtime-risk-filter">风险等级</label>
      <select id="runtime-risk-filter" v-model="severity" @change="limit = 25">
        <option value="">全部等级</option>
        <option value="pending">待研判</option>
        <option v-for="(label, value) in runtimeRiskSeverityLabels" :key="value" :value="value">{{ label }}</option>
      </select>
      <label class="runtime-overdue-filter"><input v-model="overdueOnly" type="checkbox" @change="limit = 25">仅逾期</label>
    </div>
    <details class="runtime-findings-help">
      <summary>触发规则与取证范围</summary>
      <p>平台先按服务别名匹配最新 OpenAPI 契约。契约缺少对应接口，或接口已标记弃用时，实际观测到 HTTP 2xx 才触发发现。自动证据记录路由模板、状态码、来源和契约扫描编号，用于核对发布范围与访问控制。</p>
      <p>同一服务、方法、路由和规则的重复触发并入当前检查。独立复核通过后再出现新的触发记录时，平台新建检查并保留上轮链接。</p>
    </details>
    <p v-if="error" class="message message-error" role="alert">{{ error }}</p>
    <p v-else-if="loading && !findings.length" class="empty-state" role="status">正在读取运行时发现…</p>
    <div v-if="filtered.length" class="api-table-frame" role="region" tabindex="0" aria-labelledby="runtime-findings-title">
      <table class="api-table runtime-findings-table" role="table">
        <thead role="rowgroup"><tr role="row">
          <th scope="col" role="columnheader">接口与服务</th><th scope="col" role="columnheader">触发与依据</th>
          <th scope="col" role="columnheader">触发时间</th><th scope="col" role="columnheader">风险研判</th><th scope="col" role="columnheader">复测与复核</th>
          <th scope="col" role="columnheader">关联检查</th>
        </tr></thead>
        <tbody role="rowgroup">
          <tr v-for="finding in filtered.slice(0, limit)" :key="finding.id" role="row">
            <th scope="row" role="rowheader">
              <span class="api-endpoint">
                <span class="api-method mono" :class="{ 'api-method--write': !['GET', 'HEAD'].includes(finding.method) }">{{ finding.method }}</span>
                <code class="api-path mono">{{ finding.route }}</code>
              </span>
              <span class="api-secondary">{{ finding.service }}</span>
            </th>
            <td role="cell">
              <span class="api-cell-label" aria-hidden="true">触发与依据</span>
              <strong>{{ runtimeRuleLabels[finding.rule] }}</strong>
              <span class="api-secondary">首次观测来源 {{ finding.source }} · #{{ finding.sourceId }}</span>
              <a class="table-link runtime-findings-link" :href="home + '/api-security/' + finding.baselineScanId">契约扫描 #{{ finding.baselineScanId }}</a>
            </td>
            <td role="cell">
              <span class="api-cell-label" aria-hidden="true">触发时间</span>
              最近 <time :datetime="finding.lastSeen">{{ formatDate(finding.lastSeen) }}</time>
              <span class="api-secondary">首次 <time :datetime="finding.firstSeen">{{ formatDate(finding.firstSeen) }}</time></span>
            </td>
            <td role="cell">
              <span class="api-cell-label" aria-hidden="true">风险研判</span>
              <RuntimeRiskEditor :workspace="workspace" :finding="finding" :risk="riskFor(finding)" :overdue="isOverdue(finding)"
                                 @updated="localRisk[finding.id] = $event" @session-expired="emit('sessionExpired')" />
            </td>
            <td role="cell">
              <span class="api-cell-label" aria-hidden="true">复测与复核</span>
              <span class="badge status" :class="'status-' + finding.checkStatus">{{ checkStatusLabels[finding.checkStatus] }}</span>
              <span v-if="finding.retestAt" class="api-secondary">
                自动复测 <time :datetime="finding.retestAt">{{ formatDate(finding.retestAt) }}</time>
              </span>
              <span v-else-if="finding.checkStatus === 'needs_work'" class="api-secondary">等待自动复测</span>
            </td>
            <td role="cell">
              <span class="api-cell-label" aria-hidden="true">关联检查</span>
              <a class="api-check-link table-link" :href="home + '/checks/' + finding.checkId">G03 · #{{ finding.checkId }}<Icon name="arrow" /></a>
              <span v-if="finding.previousCheckId" class="api-secondary">
                复发 · <a class="table-link" :href="home + '/checks/' + finding.previousCheckId">上轮检查 #{{ finding.previousCheckId }}</a>
              </span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-else-if="!error && !loading" class="empty-state">
      {{ findings.length ? '暂无符合筛选条件的发现。' : '尚未登记运行时发现。匹配到契约并观测到满足规则的 HTTP 2xx 后，结果会在此显示。' }}
    </p>
    <button v-if="filtered.length > limit" type="button" class="button button-secondary runtime-findings-more" @click="limit += 25">显示更多发现</button>
  </section>
</template>

<style scoped>
.runtime-findings-filters { padding: 0 0 .65rem; border: 0; }
.runtime-findings-filters select { min-width: 0; }
.runtime-overdue-filter { display: inline-flex; align-items: center; gap: .35rem; }
.runtime-findings-help { margin: .25rem 0 .75rem; padding: .55rem .75rem; border-radius: .4rem; background: var(--surface-subtle); }
.runtime-findings-help summary { min-height: 1.8rem; color: var(--api-blue-deep); cursor: pointer; font-weight: 700; }
.runtime-findings-help p { margin-top: .45rem; color: var(--api-quiet); font-size: .875rem; }
.runtime-findings-table th:first-child { width: 25%; }
.runtime-findings-table .api-check-link { min-height: 2rem; }
.runtime-findings-link { display: inline-block; padding-block: .2rem; font-size: .8rem; }
.runtime-findings-more { margin-top: .75rem; }
@media (max-width: 58rem) {
  .runtime-findings-table tbody th:first-child { width: auto; }
  .runtime-findings-table tbody td:last-child { grid-column: 1 / -1; }
}
</style>
