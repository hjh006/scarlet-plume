<script setup lang="ts">
import { computed } from 'vue'
import { formatDate } from './api'
import { aiString } from './ai-security'
import type { AIRecord } from './ai-security-editor'
import type { RuntimeEvent, RuntimeFilters } from './ai-security-runtime'
import { mediaDeclaration, mediaVersionState, mediaV2, runtimeMediaEvidence } from './ai-security-media'

const props=defineProps<{models:AIRecord[];events:RuntimeEvent[];filters:RuntimeFilters;loading:boolean;error:string;readAt:string}>()
defineEmits<{configuration:[];evidence:[];refresh:[]}>()
const models=computed(()=>props.models.flatMap(model=>{const media=mediaDeclaration(model);return media?[{model,media}]:[]}))
const events=computed(()=>props.events.flatMap(event=>{const media=runtimeMediaEvidence(event);return media?[{event,media}]:[]}))
const executions:Record<string,string>={not_attempted:'未发起上游动作',succeeded:'来源报告成功',failed:'来源报告失败',unknown:'上游结果未知',blocked_output:'上游已返回，输出被阻止'}
const stages:Record<string,string>={decision:'策略决策',prepared:'执行前准备',completed:'执行后回执'}
const kinds:Record<string,string>={model:'模型输入',output:'模型输出',rag:'知识检索',tool:'工具动作'}
const outcomes:Record<string,string>={allow:'允许',deny:'拒绝',unknown:'未知',approval_required:'等待独立批准'}
</script>

<template>
  <section class="panel media-rollout" aria-labelledby="media-rollout-title">
    <div class="ai-security-heading"><div><h2 id="media-rollout-title">媒体 v2 升级与运行证据</h2><p>网关与扫描器需同步采用 v2。配置声明、就绪探测、实际媒体回执和独立效果验收分别核对。</p></div><button type="button" class="button button-secondary" @click="$emit('configuration')">核对模型媒体声明</button></div>
    <div class="ai-security-body"><h3>已保存的模型声明</h3><p class="table-secondary">这里读取已保存配置；尚未发布的修订与编辑草稿不能证明客户节点已经升级。历史 v1 目录可配合新网关和 v2 扫描器使用，实际检测回执必须明确采用 v2。</p></div>
    <div v-if="models.length" class="table-scroll" role="region" aria-label="已保存的媒体模型声明" tabindex="0"><table class="data-table media-models"><thead><tr><th scope="col">模型与状态</th><th scope="col">声明版本</th><th scope="col">允许格式与单体上限</th><th scope="col">升级核对</th></tr></thead><tbody><tr v-for="{model,media} in models" :key="aiString(model,'id')"><th scope="row">{{ aiString(model,'name','id') }}<span class="table-secondary mono"><template v-if="model.name && model.name!==model.id">{{ aiString(model,'id') }} · </template>{{ model.enabled===true?'已启用声明':'未启用声明' }}</span></th><td>{{ mediaVersionState(media.version) }}<span class="table-secondary mono">{{ media.version || '未记录' }}</span></td><td><span v-for="mime in media.mimeTypes" :key="mime" class="table-secondary mono">{{ mime }}</span><span v-if="!media.mimeTypes.length">未记录允许格式</span><span class="table-secondary">单体 {{ media.maxAssetBytes?media.maxAssetBytes.toLocaleString('zh-CN')+' 字节':'上限未记录' }}</span></td><td>{{ media.version===mediaV2?'声明已采用 v2，仍需验证实际节点':'保留原声明；显式迁移需保存、回归及独立发布' }}</td></tr></tbody></table></div><p v-else class="empty-state">已保存配置未声明媒体模型。此状态不证明其他调用路径具备媒体检测。</p>
    <div class="ai-security-heading"><div><h3>当前查询页的媒体回执</h3><p>来源 {{ filters.sourceId || '全部' }} · 类型 {{ kinds[filters.kind] || filters.kind || '全部' }} · 决策 {{ outcomes[filters.outcome] || filters.outcome || '全部' }}。只展示已加载页，缺少记录不能推断检测未发生或升级已完成。</p></div><button type="button" class="button button-secondary" :disabled="loading" @click="$emit('refresh')">刷新媒体回执</button></div>
    <p v-if="error" class="message message-error" role="alert">{{ error }}{{ readAt?' 下方保留上次成功读取的回执。':' 本次尚未取得运行回执，请重试。' }}</p><p v-if="loading" class="ai-security-body" role="status">正在读取媒体运行证据…</p>
    <div v-if="events.length" class="table-scroll" role="region" aria-label="媒体版本与原始绑定回执" tabindex="0"><table class="data-table media-events"><thead><tr><th scope="col">回执与来源</th><th scope="col">媒体检测版本</th><th scope="col">阶段、结果与范围</th><th scope="col">原始绑定</th></tr></thead><tbody><tr v-for="{event,media} in events" :key="event.id"><th scope="row">#{{ event.id }} · 来源 #{{ event.sourceId }}<span class="table-secondary">{{ event.applicationId }} · {{ media.principalId || '未记录主体' }}</span><time class="table-secondary" :datetime="event.receivedAt" :title="event.receivedAt">接收 {{ formatDate(event.receivedAt,true) }}</time></th><td>{{ mediaVersionState(media.version) }}<span class="table-secondary mono">{{ media.version || '未记录' }}</span><span v-if="media.version!==mediaV2" class="table-secondary">不能作为当前 v2 检测已生效的证据。</span></td><td>{{ stages[event.stage] || event.stage }}<span class="table-secondary">{{ outcomes[event.outcome] || event.outcome }} · {{ executions[event.executionStatus] || '执行结果未记录' }}</span><span class="table-secondary">{{ event.current?'当前目录绑定的来源记录':'历史来源或历史目录记录' }}</span><span class="table-secondary">{{ event.stage==='prepared'?'准备记录不表示已执行成功':'来源报告不替代独立效果验收' }}</span></td><td><strong v-if="!media.bound">缺少完整绑定，不能采用</strong><details v-else><summary>核对 {{ media.digests.length }} 个媒体摘要</summary><p class="mono">目录 {{ event.catalogDigest }}</p><p class="mono">请求 {{ event.requestDigest }}</p><p v-for="value in media.digests" :key="value" class="mono">媒体 {{ value }}</p></details></td></tr></tbody></table></div><p v-else-if="!loading && !error" class="empty-state">当前查询页没有带媒体版本或摘要的回执。请核对来源、筛选、分页与客户侧部署，不能按节点能力声明确认升级完成。</p>
    <div class="ai-security-body"><p class="table-secondary">{{ readAt?'本页读取于 '+formatDate(readAt)+'。':'尚未成功读取运行回执。' }}网关的版本匹配与就绪检查也不等于识别准确率或全链路安全已验收。升级后应分别验证允许、输入拒绝、输出阻断、低置信媒体及扫描器故障；旧 v1 记录不回填为 v2。</p><button type="button" class="button button-secondary" @click="$emit('evidence')">按来源与分页核对完整回执</button></div>
  </section>
</template>

<style scoped>
.media-rollout{grid-column:1/-1;min-width:0}.media-rollout h3{margin:0 0 8px;font-size:15px;line-height:1.6}.media-rollout .ai-security-body>p{margin:0 0 12px;line-height:1.8}
.media-models{min-width:790px}.media-events{min-width:880px}.media-rollout th:first-child{width:23%}.media-rollout td{vertical-align:top;overflow-wrap:anywhere}.media-rollout details p{max-width:56ch;overflow-wrap:anywhere;line-height:1.7}
</style>
