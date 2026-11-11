<script setup lang="ts">
import { computed } from 'vue'
import { formatDate } from './api'
import type { CatalogClassificationAssessment } from './discovery'
import { classificationComparisonLabels } from './classification-standards'
import ClassificationEvaluation from './ClassificationEvaluation.vue'
const props=defineProps<{assessment:CatalogClassificationAssessment}>()
const automatic=computed(()=>props.assessment.evaluation?{jobId:props.assessment.sourceId,runId:String(props.assessment.runId),observedAt:props.assessment.observedAt,classificationRevision:props.assessment.classificationRevision,comparison:props.assessment.comparison,comparisonReasons:props.assessment.reasons,evaluation:props.assessment.evaluation}:undefined)
const candidates=computed(()=>props.assessment.fields.filter(f=>f.sensitivity||f.hits?.length))
const labels:Record<string,string>={fresh_source_after_manual_confirmation_required:'人工确认后需重新领取任务并采集正向来源事实',manual_classification_confirmation_required:'需要人工确认分类，并取得确认后的新来源统计',current_positive_content_scope_required:'需要当前授权范围内可用的正向内容统计',adopted_standard_unavailable:'采用标准的自动规则不可用'}
</script>
<template>
  <details class="catalog-assessment"><summary><span class="evidence-proof-mark">目录取证</span> #{{assessment.id}} · {{classificationComparisonLabels[assessment.comparison]}} · 来源配置 v{{assessment.sourceRevision}}</summary>
    <div class="catalog-proof-body"><div class="catalog-proof-heading"><strong>{{assessment.system}}</strong><span>{{[assessment.database,assessment.schema,assessment.table].filter(Boolean).join(' / ')}}</span></div>
      <dl class="catalog-proof-metadata"><div><dt>来源与运行</dt><dd>{{assessment.source}} #{{assessment.sourceId}} · 运行 #{{assessment.runId}}</dd></div><div><dt>分类版本</dt><dd>{{assessment.classificationRevision}}</dd></div><div><dt>观察时间</dt><dd>{{formatDate(assessment.observedAt)}}</dd></div></dl>
      <p class="catalog-snapshot-note">采集时冻结的证据。当前复核还需核验来源状态、人工分类、规则版本与整改周期。</p>
      <ClassificationEvaluation v-if="automatic" :automatic="automatic" compact/>
      <div v-if="assessment.missingConditions.length" class="catalog-proof-gaps"><h4>尚缺条件</h4><ul><li v-for="condition in assessment.missingConditions" :key="condition">{{labels[condition]||condition}}</li></ul></div>
      <details class="catalog-proof-section"><summary>客户规则与受限命中 <span>{{candidates.length}} 个候选字段</span></summary><ul class="catalog-rule-list"><li v-for="rule in assessment.appliedRules" :key="rule.key"><strong>{{rule.name}}</strong><span>{{rule.key}} v{{rule.version}} · {{rule.target}}</span></li></ul><div class="catalog-field-proof" v-for="field in candidates" :key="field.name"><h4>{{field.name}} <small>{{field.type}}</small></h4><dl v-for="hit in field.hits" :key="hit.ruleKey+hit.ruleVersion+hit.method"><dt>{{hit.ruleKey}} v{{hit.ruleVersion}} · {{hit.category}}</dt><dd>{{hit.method}} · 命中 {{hit.matchCount}} / 已观察 {{hit.scannedCount}}</dd></dl></div></details>
      <details class="catalog-proof-section"><summary>采集覆盖与限制</summary><pre>{{JSON.stringify(assessment.coverage,null,2)}}</pre></details>
    </div>
  </details>
</template>
<style scoped>
.catalog-assessment{margin:14px 0;border:1px solid var(--line,#dbe5ed);border-radius:9px;background:#fff}.catalog-assessment>summary{font-size:12px;padding:14px;cursor:pointer;color:#23445f;line-height:1.8}.evidence-proof-mark{color:#087f94;font-size:10px;padding:4px 6px;background:#ebf7f8;border-radius:4px;margin-right:8px}.catalog-proof-body{padding:0 18px 18px;border-top:1px solid #edf1f6}.catalog-proof-heading{display:flex;flex-wrap:wrap;gap:10px;margin:16px 0;font-size:12px}.catalog-proof-heading span{color:var(--muted)}.catalog-proof-metadata{display:grid;gap:10px}.catalog-proof-metadata>div{display:grid;grid-template-columns:85px minmax(0,1fr);gap:10px;font-size:11px}.catalog-proof-metadata dt{color:var(--muted)}.catalog-proof-metadata dd{margin:0;overflow-wrap:anywhere}.catalog-snapshot-note{font-size:11px;color:var(--muted);line-height:1.9;padding:12px;background:#f6f9fc;border-radius:6px}.catalog-proof-gaps{border-left:2px solid #ff654b;padding:2px 12px;margin:15px 0;background:#fff8f5}.catalog-proof-gaps h4{font-size:12px;margin:8px 0}.catalog-proof-gaps ul{margin:8px 0;padding-left:16px;font-size:11px;line-height:1.8}.catalog-proof-section{padding-top:12px;border-top:1px solid #edf1f6;margin-top:12px}.catalog-proof-section>summary{font-size:12px;cursor:pointer}.catalog-proof-section>summary span{font-size:10px;color:var(--muted);margin-left:8px}.catalog-rule-list{list-style:none;padding:0;display:grid;gap:10px}.catalog-rule-list li{font-size:11px;display:grid;gap:5px}.catalog-rule-list span{color:var(--muted)}.catalog-field-proof{padding:12px;background:#f8fbfd;margin-top:10px;border-radius:6px}.catalog-field-proof h4{font-size:12px;margin:0 0 10px}.catalog-field-proof small{color:var(--muted);font-weight:400}.catalog-field-proof dl{display:grid;gap:6px;font-size:11px}.catalog-field-proof dd{margin:0;color:var(--muted)}pre{max-height:22rem;overflow:auto;white-space:pre-wrap;font-size:11px;overflow-wrap:anywhere}
</style>
