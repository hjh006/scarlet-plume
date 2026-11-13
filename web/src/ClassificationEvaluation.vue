<script setup lang="ts">
import { computed, ref } from 'vue'
import { assetBusinessLevelLabels, assetCategoryLabels, documentationUrl, formatDate } from './api'
import {
  classificationComparisonLabels, classificationEvaluationLabels, type AutomaticClassification, type ClassificationFact,
} from './classification-standards'
import { postgresProfileStatusLabels, postgresProfileWarning, postgresSensitivityLabel } from './postgresql'

const props = defineProps<{ automatic: AutomaticClassification; compact?: boolean }>()
const evaluation = computed(() => props.automatic.evaluation)
const evidenceTab = ref('rules')
const factLabels: Record<string, string> = {
  column_count: '字段数量',
  constraint_count: '约束数量',
  estimated_row_count: '优化器估计行数',
  row_security: '行级安全',
  metadata_candidate: '元数据识别线索',
  content_scope: '内容聚合范围',
  sampled_row_count: '已观察样本行数',
  format_match: '格式匹配样本行数',
}
const sourceLabels: Record<string, string> = {
  catalog: '数据库系统目录', source_aggregate: '源库聚合', column_name: '字段名称', column_comment: '字段注释',
}
const statutoryLabels: Record<string, string> = { unknown: '待认定', general: '一般数据', important: '重要数据', core: '核心数据' }

function describeCondition(condition: string) {
  const prefix = ['元数据范围缺口：', '内容范围缺口：'].find(value => condition.startsWith(value))
  if (!prefix) return condition
  const warnings = condition.slice(prefix.length).split(',').filter(Boolean).map(postgresProfileWarning)
  return prefix.slice(0, -1) + ' · ' + warnings.join('、')
}

function describeFact(fact: ClassificationFact) {
  const note = fact.note
  if (fact.kind === 'content_scope') {
    const [status = '', method = '', warnings = ''] = fact.note?.split(';') ?? []
    return [
      postgresProfileStatusLabels[status] || status,
      method === 'bounded_prefix_aggregate' ? '有界前缀样本聚合' : method,
      ...warnings.split(',').filter(Boolean).map(postgresProfileWarning),
    ].filter(Boolean).join(' · ')
  }
  if (fact.kind === 'row_security' && note && /^enabled=(true|false);forced=(true|false);active=(true|false)$/.test(note)) {
    const states = note.split(';').map(part => part.endsWith('=true'))
    return [states[0] ? '已启用行级安全' : '行级安全关闭', states[1] ? '强制执行' : '由表级配置控制', states[2] ? '当前采集角色受行级策略限制' : '当前采集角色无行级策略过滤'].join(' · ')
  }
  return fact.note
}
</script>

<template>
  <div class="classification-evaluation" :class="{compact}">
    <div class="classification-evaluation-heading">
      <span class="classification-evaluation-mark" aria-hidden="true">↗</span><strong>本次自动判定</strong>
      <span class="badge" :class="evaluation.status === 'detected' ? 'status-in_review' : 'status-draft'">{{ classificationEvaluationLabels[evaluation.status] }}</span>
      <span v-if="automatic.stale" class="badge status-needs_work">快照需更新</span>
    </div>
    <div class="classification-evaluation-version"><span>{{evaluation.standard.code}} · {{evaluation.version}}</span><time :datetime="automatic.observedAt">{{formatDate(automatic.observedAt)}}</time></div>
    <details class="classification-snapshot-meta"><summary>标准、观察范围与批次详情</summary><dl class="classification-evaluation-summary">
      <div><dt>观察时间</dt><dd><time :datetime="automatic.observedAt">{{ formatDate(automatic.observedAt) }}</time></dd></div>
      <div><dt>来源批次</dt><dd>任务 #{{ automatic.jobId }} · <span class="mono">{{ automatic.runId }}</span></dd></div>
      <div class="classification-adopted-standard"><dt>采用标准</dt><dd>
        <a v-if="documentationUrl(evaluation.standard.url)" class="table-link" :href="documentationUrl(evaluation.standard.url)" target="_blank" rel="noopener noreferrer">
          {{ evaluation.standard.code }} · {{ evaluation.standard.title }}
        </a>
        <span v-else>{{ evaluation.standard.code }} · {{ evaluation.standard.title }}</span>
      </dd></div>
      <div><dt>数据属性</dt><dd>{{ assetCategoryLabels[evaluation.category] || '待判定' }} · {{ assetBusinessLevelLabels[evaluation.businessLevel] || '敏感级别待判定' }}</dd></div>
      <div><dt>标准等级</dt><dd>{{ evaluation.standard.levels?.find(level => level.code === evaluation.levelCode)?.label || '待判定' }}</dd></div>
      <div><dt>法定类别</dt><dd>{{ statutoryLabels[evaluation.statutoryCategory || 'unknown'] || evaluation.statutoryCategory }}</dd></div>
      <div><dt>结果比对</dt><dd>{{ classificationComparisonLabels[automatic.comparison] }} · 对照分类版本 {{ automatic.classificationRevision }}</dd></div>
      <div><dt>规则版本</dt><dd class="mono">{{ evaluation.version }}</dd></div>
    </dl></details>
    <p class="classification-evaluation-scope" :class="{ applicable: evaluation.autoApplicable }">{{ evaluation.autoApplicable ? '本次事实支持自动应用数据属性分类。' : '本次结果需结合待补条件复核。' }}</p>
    <ul v-if="automatic.comparisonReasons?.length" class="classification-evaluation-reasons">
      <li v-for="reason in automatic.comparisonReasons" :key="reason">{{ reason }}</li>
    </ul>
    <nav class="classification-evidence-tabs" aria-label="自动判定依据"><button type="button" :class="{active:evidenceTab==='rules'}" :aria-pressed="evidenceTab==='rules'" @click="evidenceTab='rules'">条款判定 <span>{{evaluation.rules?.length??0}}</span></button><button type="button" :class="{active:evidenceTab==='facts'}" :aria-pressed="evidenceTab==='facts'" @click="evidenceTab='facts'">采集事实 <span>{{evaluation.facts?.length??0}}</span></button><button type="button" :class="{active:evidenceTab==='batch'}" :aria-pressed="evidenceTab==='batch'" @click="evidenceTab='batch'">证据批次</button><button type="button" :class="{active:evidenceTab==='missing'}" :aria-pressed="evidenceTab==='missing'" @click="evidenceTab='missing'">缺失条件 <span>{{evaluation.missingConditions?.length??0}}</span></button></nav>
    <dl v-if="evidenceTab==='batch'" class="classification-evaluation-summary"><div><dt>采集任务</dt><dd>#{{automatic.jobId}}</dd></div><div><dt>观察时间</dt><dd>{{formatDate(automatic.observedAt)}}</dd></div><div class="classification-adopted-standard"><dt>来源运行批次</dt><dd class="mono">{{automatic.runId}}</dd></div><div><dt>规则版本</dt><dd class="mono">{{evaluation.version}}</dd></div><div><dt>对照分类版本</dt><dd>v{{automatic.classificationRevision}}</dd></div><div><dt>快照状态</dt><dd>{{automatic.stale?'需更新':'按本次观察范围展示'}}</dd></div></dl>
    <details v-show="evidenceTab==='facts'" class="classification-evaluation-details" open>
      <summary>采集事实 · {{ evaluation.facts?.length ?? 0 }} 项</summary>
      <div v-if="evaluation.facts?.length" class="table-scroll" role="region" tabindex="0" aria-label="自动判定所用采集事实">
        <table class="data-table classification-facts-table">
          <thead><tr><th scope="col">事实</th><th scope="col">字段与识别类型</th><th scope="col">计数</th><th scope="col">来源与范围</th></tr></thead>
          <tbody>
            <tr v-for="(fact, index) in evaluation.facts" :key="index">
              <th scope="row">{{ factLabels[fact.kind] || fact.kind }}</th>
              <td><span v-if="fact.column" class="mono">{{ fact.column }}</span><span v-if="fact.category" class="table-secondary">{{ postgresSensitivityLabel(fact.category) }}</span><span v-if="!fact.column && !fact.category">—</span></td>
              <td>{{ fact.count ?? '—' }}</td>
              <td>{{ sourceLabels[fact.source] || fact.source }}<span v-if="fact.note" class="table-secondary">{{ describeFact(fact) }}</span></td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="field-hint">此快照暂无可用采集事实。</p>
    </details>
    <details v-show="evidenceTab==='rules'" class="classification-evaluation-details" open>
      <summary>条款判定 · {{ evaluation.rules?.length ?? 0 }} 条</summary>
      <ol v-if="evaluation.rules?.length" class="classification-evaluation-rules">
        <li v-for="entry in evaluation.rules" :key="entry.rule.id">
          <div class="classification-evaluation-heading">
            <strong>{{ entry.rule.clause }} · {{ entry.rule.title }}</strong>
            <span class="badge" :class="entry.status === 'detected' ? 'status-in_review' : 'status-draft'">{{ classificationEvaluationLabels[entry.status] }}</span>
          </div>
          <p>{{ entry.rule.criterion }}</p>
          <p v-if="entry.rule.evidence" class="field-hint">取证要求 {{ entry.rule.evidence }}</p>
          <ul v-if="entry.facts?.length">
            <li v-for="(fact, index) in entry.facts" :key="index">
              {{ factLabels[fact.kind] || fact.kind }}<span v-if="fact.column"> · <span class="mono">{{ fact.column }}</span></span><span v-if="fact.category"> · {{ postgresSensitivityLabel(fact.category) }}</span><span v-if="fact.count !== undefined"> · {{ fact.count }} 行</span>
            </li>
          </ul>
          <ul v-if="entry.missingConditions?.length" class="classification-evaluation-missing">
            <li v-for="condition in entry.missingConditions" :key="condition">{{ describeCondition(condition) }}</li>
          </ul>
        </li>
      </ol>
      <p v-else class="field-hint">所选标准暂无已配置的自动判定条款。</p>
    </details>
    <div v-if="evidenceTab==='missing' && evaluation.missingConditions?.length" class="classification-evaluation-limitations">
      <strong>仍需补充的条件</strong>
      <ul><li v-for="condition in evaluation.missingConditions" :key="condition">{{ describeCondition(condition) }}</li></ul>
    </div>
    <p v-else-if="evidenceTab==='missing'" class="field-hint classification-no-missing">本次规则结果未列出缺失条件；适用范围仍以采集快照为准。</p>
  </div>
</template>

<style scoped>
.classification-evaluation { min-width:0;container-type:inline-size; }.classification-evidence-tabs{display:flex;gap:18px;overflow-x:auto;border-bottom:1px solid var(--line);margin-top:16px}.classification-evidence-tabs>button{display:flex;gap:6px;align-items:center;flex-shrink:0;background:none;border:0;border-bottom:2px solid transparent;padding:11px 0;font:inherit;font-size:12px;color:var(--secondary);cursor:pointer}.classification-evidence-tabs>button.active{color:var(--accent);border-color:var(--accent);font-weight:650}.classification-evidence-tabs>button>span{padding:1px 5px;border-radius:4px;background:var(--surface-subtle);font-size:12px}.classification-no-missing{padding:16px 0}
.classification-evaluation-mark{display:grid;place-items:center;width:28px;height:28px;background:var(--accent-soft);border-radius:7px;color:var(--accent);font-size:18px}.classification-adopted-standard{grid-column:1/-1}.classification-evaluation-summary>div{min-width:0;padding:12px 14px;border:1px solid var(--line);border-radius:8px;background:var(--surface)}.classification-evaluation-summary>div.classification-adopted-standard{border-left:1px solid var(--accent);background:var(--surface-subtle)}
.classification-evaluation-heading { display: flex; flex-wrap: wrap; align-items: center; gap: .4rem .65rem; }
.classification-evaluation-summary { display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px;margin:16px 0; }
.classification-evaluation-summary dt { color: var(--secondary); font-size: 12px; font-weight: 700; }
.classification-evaluation-summary dd { margin-top: .15rem; overflow-wrap: anywhere; }
.classification-evaluation-scope { color: var(--secondary); font-size:12px;padding:10px 14px;background:var(--surface-subtle);border-left:1px solid var(--violet);border-radius:0 6px 6px 0; }.classification-evaluation-scope.applicable{border-left-color:var(--success)}
.classification-evaluation-reasons { margin: .5rem 0; padding-left: 1.15rem; color: var(--warning); font-size: .85rem; }
.classification-evaluation-details { margin-top:12px;padding-top:6px;border-top:1px solid var(--line); }
.classification-evaluation-details > summary { min-height: 2.5rem; padding-block: .5rem; color: var(--accent); font-weight: 700; cursor: pointer; }
.classification-facts-table { min-width: 32rem; }
.classification-evaluation-rules { margin: 0; padding: 0; list-style: none; }
.classification-evaluation-rules > li { padding:16px 0;border-bottom:1px solid var(--line);background:var(--surface); }.classification-evaluation-rules > li:last-child{border-bottom:0}
.classification-evaluation-rules > li + li { margin-top: .55rem; }
.classification-evaluation-rules p { margin-top: .35rem; }
.classification-evaluation-rules ul { margin: .4rem 0 0; padding-left: 1.15rem; font-size: .83rem; }
.classification-evaluation-missing { color: var(--secondary); }
.classification-evaluation-limitations { margin-top: .75rem; padding: .7rem .8rem; border-radius: .4rem; background: var(--warning-soft); font-size: .85rem; }
.classification-evaluation-limitations ul { margin: .3rem 0 0; padding-left: 1.15rem; }
@container (min-width: 650px){.classification-evaluation-summary{grid-template-columns:repeat(3,minmax(0,1fr))}.classification-adopted-standard{grid-column:1/-1}}
@media (max-width: 43.99rem) {
  .classification-evaluation-summary { grid-template-columns: minmax(0, 1fr); }
}
.classification-evaluation-version{display:flex;justify-content:space-between;gap:12px;flex-wrap:wrap;margin:12px 0 8px;padding:10px 12px;border:1px solid var(--line);border-left:1px solid var(--accent);border-radius:6px;font-size:12px;color:var(--secondary);background:var(--surface-subtle)}.classification-snapshot-meta{margin-bottom:12px}.classification-snapshot-meta>summary{font-size:12px;color:var(--accent);cursor:pointer;padding:8px 0}.classification-evaluation.compact .classification-evaluation-version{font-size:12px}
</style>
