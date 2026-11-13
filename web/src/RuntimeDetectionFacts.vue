<script setup lang="ts">
import { formatDate } from './api'
import { lifecycleCategoryLabels } from './api-lifecycle'
import type { RuntimeDetection } from './runtime-findings'
defineProps<{ detection: RuntimeDetection }>()
const gapLabels: Record<string, string> = {
  configured_technical_baseline_not_automatic_legal_conclusion: '采用的是已配置技术基线，命中不自动认定违反法律要求',
  source_labels_not_automatically_verified: '主体、地域或接收方等来源声明仍需核实实际业务事实',
  connected_source_scope_only: '只覆盖已接入来源的观测范围', pattern_or_policy_signal_not_proven_vulnerability: '规则或模式命中，尚未证明存在漏洞',
  credential_field_name_signal_requires_validation: '凭据字段名信号，实际有效性待核实', source_sample_limit_20000: '达到来源样本上限，统计范围不完整',
  auth_state_unknown: '部分请求认证状态未知', source_declared_auth_state: '认证状态由获授权来源明确上报',
  recipient_authorization_not_verified: '接收者授权情况仍需核实', external_destination_not_observed: '尚未观察外部目的地，不能认定外传',
  format_not_real_identity: '格式匹配不代表真实身份或账号', requests_not_rows_or_people: '请求次数不代表数据行数或人数', subject_scoped_to_source: '主体标识仅在同一来源内比较',
  absolute_window_threshold_not_historical_baseline: '按窗口绝对阈值判断，未推断历史基线变化', fixed_offset_not_business_calendar: '使用固定时区，未包含节假日和业务日历',
  json_size_limit: 'JSON 超过有界检查大小', json_depth_limit: 'JSON 深度超过检查范围', json_field_limit: '字段路径达到检查上限',
  json_dynamic_keys: '动态键已屏蔽，字段路径精度受限', json_invalid: '正文不是完整可识别 JSON', json_read_error: '正文未完整读到',
  json_encoding_unsupported: '正文编码不在当前检查范围', invalid_data_fields: '字段投影无效，相关范围未知', invalid_security_context: '安全信号声明无效或不唯一',
}
</script>

<template>
  <details class="detection-facts" open>
    <summary>查看策略与观测依据</summary>
    <dl>
      <div><dt>实际统计来源</dt><dd>{{ detection.source }} · #{{ detection.sourceId }}</dd></div>
      <div><dt>命中时策略</dt><dd>{{ detection.policy.revision ? `v${detection.policy.revision} · #${detection.policy.id}` : '系统默认' }} · {{ detection.policy.windowMinutes }} 分钟 · 命中阈值 {{ detection.policy.threshold }} · 最少请求 {{ detection.policy.minRequests }}</dd></div>
      <div><dt>统计窗口</dt><dd>{{ formatDate(detection.windowStart) }} 至 {{ formatDate(detection.windowEnd) }}</dd></div>
      <div><dt>请求与信号</dt><dd>{{ detection.requests }} 次请求；信号命中 {{ detection.matched }} / 可观察分母 {{ detection.observed }}。字段统计可能包含同一响应多个字段，不是唯一记录数。</dd></div>
      <div v-if="detection.policy.rule === 'error_spike'"><dt>错误比例阈值</dt><dd>{{ detection.policy.errorPercent }}%</dd></div>
      <div v-if="detection.policy.rule === 'off_hours'"><dt>监测时段</dt><dd>{{ detection.policy.startHour }} 时至 {{ detection.policy.endHour }} 时，UTC 偏移 {{ detection.policy.timezoneOffsetMinutes }} 分钟</dd></div>
      <div v-if="detection.subjectRef"><dt>去标识主体</dt><dd><code>{{ detection.subjectRef.slice(0, 12) }}…</code>（来源内标识）</dd></div>
      <div v-if="detection.categories.length"><dt>类别信号</dt><dd>{{ detection.categories.map(category => lifecycleCategoryLabels[category] || category).join('、') }}</dd></div>
    </dl>
    <ul v-if="detection.warnings.length"><li v-for="warning in detection.warnings" :key="warning">{{ gapLabels[warning] || warning }}</li></ul>
  </details>
</template>

<style scoped>
.detection-facts{margin-top:.65rem;max-width:62ch;font-size:.875rem;line-height:1.65}.detection-facts summary{cursor:pointer;min-height:44px;display:flex;align-items:center;color:var(--primary);font-weight:600}.detection-facts dl{margin:.25rem 0}.detection-facts dl>div{margin:.6rem 0}.detection-facts dt{font-weight:600}.detection-facts dd{margin:0;overflow-wrap:anywhere}.detection-facts ul{padding-left:1.2rem;margin:.65rem 0}
.detection-facts{max-width:none;margin-top:16px;padding-top:12px;border-top:1px solid var(--line);font-size:12px}.detection-facts summary{min-height:36px;font-size:12px;color:var(--accent);font-weight:600}.detection-facts dl{margin:12px 0;display:grid;gap:14px}.detection-facts dl>div{margin:0}.detection-facts dt{font-size:12px;color:var(--secondary);font-weight:400;margin-bottom:5px}.detection-facts dd{font-size:12px;line-height:1.7}.detection-facts ul{padding:12px 12px 12px 28px;background:var(--warning-soft);border:1px solid var(--line);border-radius:8px;font-size:12px;color:var(--warning)}.detection-facts li+li{margin-top:6px}
</style>
