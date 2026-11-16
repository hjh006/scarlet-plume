<script setup lang="ts">
import { computed } from 'vue'
import { assessmentFactLabels, assessmentFactDisplayValue, assessmentFactSourceLabel, assessmentFactStateLabel, structuredAssessmentFact, type AssessmentFact } from './assessments'
import { formatDate } from './api'
import Icon from './Icon.vue'
const props = defineProps<{ title: string; factKeys: string[]; facts?: AssessmentFact[]; note?: string; icon?: string }>()
const items = computed(() => props.facts?.filter(f => props.factKeys.includes(f.key)) ?? [])
</script>
<template>
  <section class="scenario-fact-card">
    <h3><Icon :name="icon || 'reports'"/>{{ title }}</h3>
    <p v-if="note" class="fact-card-note">{{ note }}</p>
    <ul v-if="items.length"><li v-for="(fact,index) in items" :key="fact.key + ':' + fact.source + ':' + index">
      <div><strong>{{ assessmentFactLabels[fact.key] || fact.key }}</strong><span class="badge" :class="fact.state === 'known' ? 'status-in_progress' : 'status-needs_work'">{{ assessmentFactStateLabel(fact) }}</span></div>
      <template v-if="structuredAssessmentFact(fact.value)"><p>已返回结构化来源记录，范围与限制需单独核对。</p><details><summary>查看此项来源记录</summary><pre>{{ structuredAssessmentFact(fact.value) }}</pre></details></template>
      <p v-else>{{ fact.state === 'known' ? assessmentFactDisplayValue(fact) || '来源未返回具体内容' : '尚待核实' }}</p>
      <small>{{ assessmentFactSourceLabel(fact.source) }}<template v-if="fact.record"> · {{ fact.record }}</template><br>{{ fact.observedAt ? formatDate(fact.observedAt) : '观察时点未提供' }}</small>
    </li></ul><p v-else class="fact-card-missing"><Icon name="warning"/>未取得此项事实或执行证据</p>
  </section>
</template>
<style scoped>
.scenario-fact-card{min-width:0;border:1px solid #cbddea;border-radius:9px;background:#f7fbff;padding:16px}.scenario-fact-card h3{display:flex;align-items:center;gap:10px;font-size:17px;margin:0 0 12px;color:var(--ink)}.scenario-fact-card h3>.icon{width:25px;height:25px;color:var(--accent);flex:none}.scenario-fact-card ul{list-style:none;padding:0;margin:0}.scenario-fact-card li+li{border-top:1px solid var(--line);margin-top:12px;padding-top:12px}.scenario-fact-card li>div{display:flex;flex-wrap:wrap;align-items:center;gap:7px}.scenario-fact-card strong{font-size:13px}.scenario-fact-card .badge{font-size:12px}.scenario-fact-card p{font-size:13px;line-height:1.8;margin:7px 0;overflow-wrap:anywhere}.scenario-fact-card small{font-size:12px;line-height:1.7;color:var(--secondary);overflow-wrap:anywhere;display:block}.fact-card-note{color:var(--secondary)}.fact-card-missing{color:var(--warning);display:flex;gap:8px}.fact-card-missing .icon{width:18px;height:18px;flex:none;margin-top:3px}.scenario-fact-card summary{color:var(--accent);cursor:pointer;font-size:12px;padding:7px 0}.scenario-fact-card pre{max-height:240px;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;font-size:12px;line-height:1.7;background:white;padding:10px;border-radius:5px}
</style>
