<script setup lang="ts">
import { computed } from 'vue'
import { aiMediaVersionLabel, aiText, type AIRecord } from './ai-security-editor'
import { aiDecision, aiDecisionLabel, aiString } from './ai-security'

const props=defineProps<{record:AIRecord}>()
const decision=computed(()=>aiDecision(props.record))
const hasDecision=computed(()=>typeof decision.value.outcome==='string')
const mediaDigests=computed(()=>Array.isArray(decision.value.mediaDigests)?decision.value.mediaDigests.filter((value):value is string=>typeof value==='string' && /^[a-f0-9]{64}$/.test(value)):[])
const sourceDigests=computed(()=>Array.isArray(decision.value.sourceDigests)?decision.value.sourceDigests.filter((value):value is string=>typeof value==='string' && /^[a-f0-9]{64}$/.test(value)):[])
const mediaVersion=computed(()=>aiMediaVersionLabel(decision.value.mediaInspectionVersion))
const cases=computed(()=>Array.isArray(props.record.cases)?props.record.cases.filter((item):item is AIRecord=>typeof item==='object' && item!==null):[])
function outcome(value:unknown) {
  if(typeof value==='object' && value!==null && !Array.isArray(value)) return aiDecisionLabel(value as AIRecord)
  return ({allow:'允许',deny:'拒绝',passed:'通过',failed:'未通过',configured:'按配置可用范围与审批门槛',not_applicable:'当前范围不适用'} as Record<string,string>)[String(value)] || aiText(value) || '未提供'
}
</script>

<template>
  <div class="ai-frozen-evidence">
    <p class="ai-evidence-context">当前选中证据记录 {{aiString(record,'id')?'#'+aiString(record,'id'):'（本次返回）'}}；独立于上方当前分页。核对时以此记录实际保存的配置和绑定为准。</p>
    <dl v-if="hasDecision" class="ai-security-facts">
      <div><dt>实际决策</dt><dd>{{ aiDecisionLabel(record) }}</dd></div>
      <div><dt>决策代码</dt><dd>{{ aiString(decision,'code') || '未提供' }}</dd></div>
      <div><dt>采用目录版本</dt><dd>{{ aiString(decision,'catalogVersion') || '未提供' }}</dd></div>
      <div v-if="decision.catalogDigest"><dt>采用目录摘要</dt><dd class="mono">{{ aiString(decision,'catalogDigest') }}</dd></div>
      <div v-if="decision.requestDigest"><dt>绑定请求摘要</dt><dd class="mono">{{ aiString(decision,'requestDigest') }}</dd></div>
      <div><dt>主体与应用</dt><dd>{{ aiString(decision,'principalId') }} / {{ aiString(decision,'applicationId') }}</dd></div>
      <div><dt>允许资料</dt><dd>{{ aiText(decision.allowedDocumentIds) || '无允许资料引用' }}</dd></div>
      <div><dt>采用类别</dt><dd>{{ aiText(decision.categories) || '本次未列出类别' }}</dd></div><div v-if="decision.taskId"><dt>绑定任务</dt><dd class="mono">{{ aiString(decision,'taskId') }}</dd></div><div v-if="decision.taskPurpose"><dt>可信任务用途</dt><dd>{{ aiString(decision,'taskPurpose') }}</dd></div>
      <div v-if="decision.taskScopeDigest"><dt>任务范围摘要</dt><dd class="mono">{{ aiString(decision,'taskScopeDigest') }}</dd></div>
      <div v-if="decision.taskId"><dt>执行租约协议</dt><dd class="mono">{{ aiString(decision,'leaseProtocolVersion') || '未提供，不能据此证明当前租约有效' }}</dd></div>
      <div v-if="decision.taskId"><dt>完整祖先租约摘要</dt><dd class="mono">{{ aiString(decision,'leaseChainDigest') || '未提供，不能以当前租约回填历史决策' }}</dd></div>
      <div v-if="sourceDigests.length"><dt>实际来源正文摘要</dt><dd><span v-for="digest in sourceDigests" :key="digest" class="table-secondary mono">{{ digest }}</span></dd></div>
      <div><dt>采用规则</dt><dd>{{ aiText(decision.rules) || '未列出' }}</dd></div>
      <div v-if="mediaDigests.length"><dt>媒体检测绑定摘要</dt><dd><span v-for="digest in mediaDigests" :key="digest" class="table-secondary mono">{{ digest }}</span></dd></div>
      <div v-if="mediaDigests.length"><dt>媒体检测版本</dt><dd>{{ mediaVersion }}</dd></div>
      <div><dt>独立批准</dt><dd>{{ decision.requiresApproval===true?'需要批准':decision.requiresApproval===false?'本次未要求':'未提供' }}</dd></div>
    </dl>
    <div v-if="cases.length" class="table-scroll" role="region" aria-label="冻结用例的预期及实际结果" tabindex="0">
      <table class="data-table"><thead><tr><th scope="col">用例</th><th scope="col">预期</th><th scope="col">实际</th><th scope="col">核对结果</th></tr></thead><tbody><tr v-for="(item,index) in cases" :key="index"><th scope="row">{{ aiString(item,'name') || '用例 '+(index+1) }}<span v-if="item.catalogScope==='derived_engine_contract'" class="table-secondary">派生目录契约，不调用真实上游</span></th><td>{{ outcome(item.expected) }}</td><td>{{ outcome(item.actual) }}<span v-if="item.code" class="table-secondary">{{ aiString(item,'code') }}</span></td><td><span class="badge" :class="item.status==='not_applicable'?'status-draft':item.passed===true?'status-verified':item.passed===false?'status-needs-work':'status-draft'">{{ item.status==='not_applicable'?'当前范围不适用':item.passed===true?'通过':item.passed===false?'未通过':'待核对' }}</span></td></tr></tbody></table>
    </div>
    <p v-if="Array.isArray(decision.coverage) && decision.coverage.length" class="ai-security-note">{{ aiText(decision.coverage) }}</p>
    <details class="ai-security-evidence-json"><summary>查看完整冻结证据 JSON</summary><pre class="ai-security-code" tabindex="0" role="region" aria-label="当前选中记录的完整冻结证据 JSON">{{ JSON.stringify(record,null,2) }}</pre></details>
  </div>
</template>

<style scoped>
.ai-frozen-evidence{min-width:0}.ai-evidence-context{margin:0 0 16px;color:var(--secondary);font-size:13px;line-height:1.8}.ai-frozen-evidence .data-table{min-width:700px;table-layout:fixed}.ai-frozen-evidence .data-table th:first-child{width:30%}.ai-frozen-evidence .data-table :is(th,td){overflow-wrap:anywhere}.ai-frozen-evidence .ai-security-facts dd{white-space:pre-wrap}.ai-frozen-evidence pre:focus-visible{outline:3px solid var(--accent);outline-offset:3px}
</style>
