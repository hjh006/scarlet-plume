<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import Icon from './Icon.vue'
import SecurityPostureExplorer from './SecurityPostureExplorer.vue'
import SecurityNetworkOperations from './SecurityNetworkOperations.vue'
import './ai-security.css'

const props = defineProps<{ workspace: Workspace; initialTab?:string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
interface Capability { id:string; name:string; stage:string; records:number; href:string; coverage:string; warnings:string[] }
interface Platform { generatedAt:string; counts:Record<string,number>; capabilities:Capability[]; truncated:boolean; coverage?:string[] }
const base = workspacePath(props.workspace.slug) + '/security-platform'
const home = '#/' + encodeURIComponent(props.workspace.slug)
const data = ref<Platform | null>(null)
const loading = ref(true), error = ref('')
const tab = ref(['coverage','risks','network','changes','snapshots'].includes(props.initialTab || '')?props.initialTab!:'coverage')
const controller = new AbortController()
let generation = 0
const stageLabels:Record<string,string> = { implemented:'已有功能', needs_source:'等待来源接入', needs_configuration:'等待配置', not_reported:'尚未收到状态' }
function handleError(cause:unknown) {
  if (controller.signal.aborted) return
  if (cause instanceof ApiError && cause.status === 401) emit('sessionExpired')
  error.value = cause instanceof ApiError && cause.status === 409 ? '记录已被其他成员更新，请刷新后核对最新版本再提交。' : describeError(cause)
}
async function load() {
  const current = ++generation
  loading.value = true; error.value = ''
  try { const result=await request<Platform>(base,{signal:controller.signal});if(current===generation)data.value=result }
  catch(cause) { if(current===generation)handleError(cause) }
  finally { if(current===generation)loading.value = false }
}
function href(value:string) { return value==='security-platform'?home+'/security-platform?tab=risks':value.startsWith('#/') ? value : home+'/'+value.replace(/^\//,'') }
onMounted(load)
onUnmounted(() => controller.abort())
</script>

<template>
  <section class="ai-security-workspace" :aria-busy="loading">
    <header class="page-header"><div><p class="eyebrow">数据与 AI 安全 / 能力与覆盖</p><h1>把数据、AI 与业务连接起来</h1><p>核对八个方向的实际来源、可用功能和待补证据，围绕真实业务关联开展变更复核。</p></div><div class="page-actions"><a class="button button-primary" :href="home+'/ai-security'"><Icon name="shield"/>AI 运行时安全</a><button class="button button-secondary" :disabled="loading" @click="load">刷新</button></div></header>
    <p v-if="error" class="message message-error" role="alert">{{ error }}{{ data?' 下方保留上次成功读取的汇总，本次刷新未更新。':'' }}</p>
    <p v-if="loading && !data" class="message" role="status">正在核对工作区能力与来源…</p><section v-if="!loading && !data" class="panel ai-security-empty"><h2>尚未取得能力与来源汇总</h2><p>当前无法确认资产、AI 来源与业务关联覆盖。重新读取后再判断可用能力和待补证据。</p><button class="button button-secondary" @click="load">重新读取汇总</button></section>
    <template v-if="data">
      <div class="panel ai-security-config-source"><span>汇总时间 {{ formatDate(data.generatedAt) }}</span><span>资产 {{ data.counts.assets ?? 0 }}</span><span>已分类 {{ data.counts.classifiedAssets ?? 0 }}</span><span>活动 {{ data.counts.activities ?? 0 }}</span><span>有效来源 {{ data.counts.activeSources ?? 0 }}</span><span>未关闭发现 {{ data.counts.openFindings ?? 0 }}</span></div>
      <nav class="ai-security-tabs" aria-label="安全能力工作台"><button type="button" :aria-pressed="tab==='coverage'" @click="tab='coverage'">八方向覆盖</button><button type="button" :aria-pressed="tab==='risks'" @click="tab='risks'">数据风险与证据</button><button type="button" :aria-pressed="tab==='network'" @click="tab='network'">网络与业务关联</button><button type="button" :aria-pressed="tab==='changes'" @click="tab='changes'">变更影响与复核</button><button type="button" :aria-pressed="tab==='snapshots'" @click="tab='snapshots'">冻结与导出</button></nav>
      <section v-if="tab==='coverage'" class="panel"><div class="ai-security-heading"><div><h2>八个方向，共用真实数据与证据</h2><p>“已有功能”说明实现入口可用；接入、运行结果和控制效果仍需分别核对。</p></div></div><div v-if="data.capabilities.length" class="table-scroll" role="region" aria-label="八方向功能与覆盖" tabindex="0"><table class="data-table ai-security-coverage"><thead><tr><th scope="col">能力方向</th><th scope="col">当前状态</th><th scope="col">实际范围与缺口</th><th scope="col">进入能力</th></tr></thead><tbody><tr v-for="item in data.capabilities" :key="item.id"><th scope="row">{{ item.name }}<span class="table-secondary">{{ item.records }} 条相关记录</span></th><td><span class="badge" :class="item.stage==='implemented'?'status-verified':'status-draft'">{{ stageLabels[item.stage] || item.stage }}</span></td><td>{{ item.coverage }}<span v-for="warning in item.warnings" :key="warning" class="table-secondary">{{ warning }}</span></td><td><a :href="href(item.href)">查看功能与来源</a></td></tr></tbody></table></div><p v-else class="empty-state">尚未收到能力覆盖状态，请刷新重试。</p></section>
      <KeepAlive>
        <SecurityPostureExplorer v-if="tab==='risks'" :workspace="workspace" @session-expired="emit('sessionExpired')" />
        <SecurityNetworkOperations v-else-if="tab==='network' || tab==='changes' || tab==='snapshots'" :workspace="workspace" :tab="tab" @session-expired="emit('sessionExpired')" @select-tab="tab=$event" @changed="load" />
      </KeepAlive>
      <details v-if="data.coverage?.length" class="panel ai-security-body"><summary>本次汇总的来源与覆盖说明</summary><ul><li v-for="item in data.coverage" :key="item">{{ item }}</li></ul></details>
    </template>
  </section>
</template>
