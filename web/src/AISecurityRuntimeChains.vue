<script setup lang="ts">
import { aiString } from './ai-security'
import { aiText, type AIRecord } from './ai-security-editor'
import { formatDate } from './api'

defineProps<{ requests: AIRecord[]; limit: number }>()

const statuses: Record<string, string> = {
  reported_succeeded: '来源报告完成，阶段齐全',
  reported_failed: '来源报告执行失败',
  reported_unknown: '上游执行结果未知',
  reported_blocked_output: '上游返回后输出被阻止',
  reported_not_attempted: '准备后未执行',
  policy_deny: '在策略决策阶段拒绝',
  policy_approval_required: '等待独立批准，未进入执行',
  policy_unknown: '策略决策未知，未进入执行',
  incomplete: '阶段不完整，结果待核对',
  inconsistent: '回执相互矛盾，需调查',
  attempts_ambiguous: '旧回执无法区分多次尝试，需核对',
}
const compatibility: Record<string, string> = {
  supported_profile: '来源声明匹配当前支持版本',
  unsupported: '来源声明的版本尚不受支持',
  unversioned: '缺少执行器或检测版本',
}
const stages: Record<string, string> = { decision: '策略决策', prepared: '执行前准备', completed: '执行后回执' }
const issues: Record<string, string> = {
  identity_or_kind_conflict: '同一请求的身份或动作类型不一致',
  runtime_profile_conflict: '请求阶段采用的执行器或检测版本不一致',
  phase_result_conflict: '同一阶段存在矛盾的决策或执行结果',
  invalid_phase_claim: '阶段与执行结果不一致',
  execution_after_denial: '拒绝决策之后出现准备或执行报告',
  source_timestamps_out_of_order: '来源报告的阶段时点顺序异常',
}
function list(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : []
}
</script>

<template>
  <div class="ai-security-heading">
    <div>
      <h2>按请求核对执行链</h2>
      <p>关联同一来源、请求引用、请求摘要和目录摘要。仅使用本次最近 {{ limit }} 条回执，阶段缺失保留为待核对。</p>
    </div>
  </div>
  <div v-if="requests.length" class="table-scroll" role="region" aria-label="AI 请求执行链" tabindex="0">
    <table class="data-table ai-runtime-chains">
      <thead><tr><th scope="col">来源与请求</th><th scope="col">阶段与结果</th><th scope="col">执行器与检测版本</th><th scope="col">证据范围</th></tr></thead>
      <tbody>
        <tr v-for="item in requests" :key="aiString(item,'sourceId')+aiString(item,'requestId')+aiString(item,'requestDigest')+aiString(item,'catalogDigest')+aiString(item,'attemptId')">
          <th scope="row">
            {{ aiString(item,'requestId') }}
            <span class="table-secondary">来源 #{{ aiString(item,'sourceId') }} · {{ aiString(item,'applicationId') }}</span>
            <span class="table-secondary">主体 {{ aiString(item,'principalId') }}</span>
            <span class="table-secondary mono">{{ item.attemptId?'尝试 '+aiString(item,'attemptId'):'旧回执未提供尝试引用' }}</span>
          </th>
          <td>
            <strong>{{ statuses[aiString(item,'status')] || aiString(item,'status') }}</strong>
            <span v-if="aiString(item,'status').startsWith('policy_')" class="table-secondary">决策阶段结束，无须出现准备和执行回执。</span>
            <span v-else-if="list(item.missingStages).length" class="table-secondary">未收到：{{ list(item.missingStages).map(stage=>stages[stage] || stage).join('、') }}</span>
            <ul v-if="list(item.issues).length" class="ai-runtime-issues"><li v-for="issue in list(item.issues)" :key="issue">{{ issues[issue] || issue }}</li></ul>
          </td>
          <td>
            {{ compatibility[aiString(item,'runtimeCompatibility')] || '版本待核对' }}
            <span class="table-secondary mono">{{ aiString(item,'runtimeProtocolVersion') || '执行器版本未提供' }}</span>
            <span class="table-secondary mono">{{ aiString(item,'inspectionVersion') || '检测版本未提供' }}</span>
            <span v-if="item.mcpProtocolVersion" class="table-secondary mono">MCP {{ aiString(item,'mcpProtocolVersion') }}</span>
          </td>
          <td>
            {{ item.current?'当前目录绑定':'历史目录或历史来源' }}
            <span class="table-secondary">{{ formatDate(aiString(item,'lastObservedAt')) }}</span>
            <details>
              <summary>核对关联证据</summary>
              <p class="table-secondary mono">回执 {{ aiText(item.eventIds) }}</p>
              <p class="table-secondary mono">目录 {{ aiString(item,'catalogDigest') }}</p>
              <p class="table-secondary mono">请求 {{ aiString(item,'requestDigest') }}</p>
            </details>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
  <p v-else class="empty-state">尚无可关联的客户侧回执。单次控制台评估不会形成实际执行链。</p>
  <p class="ai-security-body table-secondary">“阶段齐全”和版本匹配均来自来源报告，仍需独立效果复核；缺少完成回执时，不能推断上游没有产生副作用。</p>
</template>

<style scoped>
.ai-runtime-chains { min-width: 860px; }
.ai-runtime-chains th { min-width: 180px; }
.ai-runtime-chains td { min-width: 190px; overflow-wrap: anywhere; }
.ai-runtime-issues { margin: .4rem 0 0; padding-left: 1.2rem; }
summary { cursor: pointer; padding-block: .35rem; }
</style>
