<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { activityKindLabels, type Activity, type ActivityPage, type ActivityHistory, type ActivityHistoryPage } from './activity'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { canManageChecks } from './workflow'
import Icon from './Icon.vue'
import GovernanceSteps from './GovernanceSteps.vue'
import './governance-workspace.css'
import { useScenarioContext } from './scenario-context'

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const { context, href } = useScenarioContext(props.workspace.slug)
const apiUrl = workspacePath(props.workspace.slug) + '/activities'
const items = ref<Activity[]>([])
const cursor = ref<number | null>(null)
const kind = ref('')
const loading = ref(true)
const errorMessage = ref('')
const selectedId = ref<number | null>(Number(context.value.activityId)||null)
const selected = computed(() => items.value.find(item => item.id === selectedId.value) ?? items.value[0] ?? null)
const history = ref<ActivityHistory[]>([])
const historyLoading = ref(false), historyError = ref('')
let historyRequest: AbortController | undefined
const latestChange = computed(() => history.value[0])
const factFields = [{key:'purpose',label:'用途'},{key:'recipient',label:'接收方'},{key:'region',label:'地域'},{key:'retentionPolicy',label:'保存安排'},{key:'transferPath',label:'传输路径'},{key:'safeguards',label:'保护措施'}] as const
function changes(entry:ActivityHistory){return factFields.filter(field=>String(entry.before?.[field.key]??'')!==String(entry.after[field.key]??'')).map(field=>field.label).join('、')||'无所列事实差异'}
const stages = computed(() => selected.value ? [
  {label:'收集',icon:'assets',value:selected.value.assetName||'尚未关联资产',status:'关联资产登记'},
  {label:'处理',icon:'classification',value:selected.value.purpose||'用途未记录',status:selected.value.purpose?'已登记 · 必要性另核':'待补充用途'},
  {label:'提供',icon:'data-flows',value:selected.value.recipient||'接收方未记录',status:selected.value.recipient?'已登记 · 实际关系另核':'提供范围待核实'},
  {label:'保存',icon:'assets',value:selected.value.retentionPolicy||'保存安排未记录',status:'源端执行待取证'},
] : [])
async function loadHistory(){historyRequest?.abort();history.value=[];historyError.value='';historyLoading.value=false;if(!selected.value)return;const operation=new AbortController();historyRequest=operation;historyLoading.value=true;try{const result=await request<ActivityHistoryPage>(apiUrl+'/'+selected.value.id+'/history',{signal:operation.signal});if(!operation.signal.aborted)history.value=result.items}catch(error){if(!operation.signal.aborted){if(error instanceof ApiError&&error.status===401)emit('sessionExpired');else historyError.value=describeError(error)}}finally{if(!operation.signal.aborted)historyLoading.value=false}}
watch(()=>[selected.value?.id,selected.value?.revision],()=>void loadHistory())
let active: AbortController | undefined

async function load(append = false) {
  active?.abort()
  const operation = new AbortController()
  active = operation
  loading.value = true
  errorMessage.value = ''
  if (!append) {
    items.value = []
    cursor.value = null
  }
  const params = new URLSearchParams()
  if (context.value.assetId) params.set('assetId', context.value.assetId)
  if (kind.value) params.set('kind', kind.value)
  if (append && cursor.value !== null) params.set('before', String(cursor.value))
  try {
    const result = await request<ActivityPage>(apiUrl + '?' + params, { signal: operation.signal })
    if (operation.signal.aborted) return
    items.value = append ? [...items.value, ...result.items] : result.items
    cursor.value = result.nextCursor ?? null
  } catch (error) {
    if (operation.signal.aborted) return
    if (error instanceof ApiError && error.status === 401) emit('sessionExpired')
    else errorMessage.value = describeError(error)
  } finally {
    if (!operation.signal.aborted) loading.value = false
  }
}

watch(() => context.value.assetId, () => { void load() })
onMounted(() => { void load() })
onBeforeUnmount(() => {active?.abort();historyRequest?.abort()})
</script>

<template>
<div class="activities-workbench governance-page">
<header class="page-header"><div><p class="eyebrow">处理活动 · 业务事实登记与修订评审</p><h1>把业务事实讲清楚，检查才能复用。</h1><p>对照活动登记与修订变化，按明确资产和活动进入复查。</p></div><a v-if="canManageChecks(workspace.role)" class="button button-primary" :href="href('activities/new',{assetId:context.assetId,activityId:''})"><Icon name="plus"/>新建活动</a></header>
<GovernanceSteps :labels="['选择活动','核对业务事实','修订评审','关联复查','记录与留痕']" :current="1"/>
<p v-if="context.assetId" class="message">实际台账查询：资产 #{{context.assetId}}。场景上下文用于返回导航，活动事实以选中记录为准。</p>
<div v-if="selected" class="governance-activity-layout"><section class="panel governance-activity-main"><div class="panel-heading"><div><h2><Icon name="classification"/>{{selected.name}} · v{{selected.revision}}</h2><p>处理活动的登记事实，运行效果分别取证。</p></div><span class="badge status-draft">{{activityKindLabels[selected.kind]}}</span></div><div class="governance-activity-stages"><article v-for="(stage,index) in stages" :key="stage.label"><h3><Icon :name="stage.icon"/>{{stage.label}}</h3><p :title="stage.value">{{stage.value}}</p><span class="badge" :class="index===3?'status-needs_work':'status-draft'">{{stage.status}}</span></article></div><div class="panel-heading"><h3>处理活动事实详情</h3><a class="table-link" :href="href('activities/'+selected.id,{assetId:String(selected.assetId),activityId:String(selected.id)})">编辑事实 / 查看完整历史 <Icon name="chevron"/></a></div><div class="table-scroll" role="region" tabindex="0" aria-label="选中活动登记事实"><table class="data-table governance-fact-table"><tbody><tr><th scope="row">关联资产</th><td>{{selected.assetName}} · {{selected.assetSystem}}</td><td><a class="table-link" :href="href('classification/'+selected.assetId,{assetId:String(selected.assetId),activityId:String(selected.id)})">分类与来源</a></td></tr><tr v-for="field in factFields" :key="field.key"><th scope="row">{{field.label}}</th><td>{{selected[field.key]||'未记录 · 待核实'}}</td><td><span class="badge status-draft">业务登记</span></td></tr></tbody></table></div><p class="governance-inline-note"><Icon name="info"/>登记地域、保存或保护安排不等于实际执行；需要在相应场景核对来源证据。</p></section><aside class="panel governance-activity-impact"><div class="panel-heading"><div><h2><Icon name="history"/>修订影响</h2><p>采用真实修订前后记录，旧检查保留原时点。</p></div></div><p v-if="historyLoading" class="empty-state" role="status">正在读取修订…</p><p v-else-if="historyError" class="message message-error" role="alert">{{historyError}}</p><div v-else-if="latestChange?.before" class="governance-version-pair"><section><h3>v{{latestChange.before.revision}} · 修改前</h3><p>{{latestChange.before.recipient||'接收方未记录'}}</p><small>{{latestChange.before.region||'地域未记录'}}</small></section><Icon name="arrow"/><section><h3>v{{latestChange.after.revision}} · 本次修订</h3><p>{{latestChange.after.recipient||'接收方未记录'}}</p><small>{{latestChange.after.region||'地域未记录'}}</small></section><p class="governance-version-changes">所列变化：{{changes(latestChange)}}</p></div><p v-else class="empty-state">尚无包含前后版本的修订记录，不推断接收方或处理范围变化。</p><h3 class="governance-section-label">继续核对相关范围</h3><a v-for="scene in [{id:'personal',label:'个人信息复查',icon:'user',desc:'用途、必要性与保护依据'},{id:'crossborder',label:'跨境复查',icon:'data-flows',desc:'实际去向与适用途径事实'},{id:'sharing',label:'共享复查',icon:'integrations',desc:'提供范围与接收方控制'}]" :key="scene.id" class="governance-recheck-link" :href="href('scenarios/'+scene.id,{assetId:String(selected.assetId),activityId:String(selected.id)})"><strong><Icon :name="scene.icon"/>{{scene.label}}</strong><span>{{scene.desc}}</span><Icon name="chevron"/></a><a class="button button-secondary" :href="href('reports/'+selected.id,{assetId:String(selected.assetId),activityId:String(selected.id)})">查看活动采用范围报告</a></aside></div>
<section v-if="selected" class="panel governance-activity-history"><div class="panel-heading"><h2><Icon name="history"/>修订记录</h2><a class="table-link" :href="href('activities/'+selected.id,{assetId:String(selected.assetId),activityId:String(selected.id)})">查看全部版本与逐项差异</a></div><div v-if="history.length" class="table-scroll" role="region" tabindex="0" aria-label="活动修订摘要"><table class="data-table"><thead><tr><th>版本</th><th>修订时间</th><th>操作人</th><th>事实变化</th></tr></thead><tbody><tr v-for="entry in history.slice(0,3)" :key="entry.id"><th scope="row">v{{entry.revision}}</th><td>{{formatDate(entry.createdAt)}}</td><td>{{entry.actor}}</td><td>{{changes(entry)}}</td></tr></tbody></table></div><p v-else class="empty-state">{{historyLoading?'正在读取修订记录…':'尚无可展示的修订记录。'}}</p></section>
<section class="panel" aria-labelledby="activities-title" :aria-busy="loading"><div class="panel-heading"><h2 id="activities-title">处理活动台账</h2><button type="button" class="button button-secondary" :disabled="loading" @click="load()">刷新</button></div><nav class="activity-kind-tabs" aria-label="活动场景"><button type="button" :class="{active:!kind}" :aria-pressed="!kind" @click="kind='';load()">全部</button><button v-for="(label,value) in activityKindLabels" :key="value" type="button" :class="{active:kind===value}" :aria-pressed="kind===value" @click="kind=String(value);load()">{{label}}</button></nav>
      <p v-if="loading&&!items.length" class="empty-state" role="status">正在读取处理活动…</p><div v-if="errorMessage" class="empty-state"><p role="alert">{{errorMessage}}</p><button class="button button-secondary" type="button" @click="load()">重新读取</button></div>
      <div v-if="items.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="activities-title"><table class="data-table activity-table"><thead><tr><th scope="col">活动名称 / 关联资产</th><th scope="col">场景</th><th scope="col">接收方 / 区域</th><th scope="col">最近修订</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="activity in items" :key="activity.id" :class="{selected:selected?.id===activity.id}"><th scope="row"><button class="activity-select" type="button" :aria-pressed="selected?.id===activity.id" @click="selectedId=activity.id">{{activity.name}}</button><span class="table-secondary">{{activity.assetName}}</span></th><td><span class="badge" :class="activity.kind==='ai'?'status-in_review':'status-draft'">{{activityKindLabels[activity.kind]}}</span></td><td>{{activity.recipient||'未记录'}}<span class="table-secondary">{{activity.region||'地域未记录'}}</span></td><td>v{{activity.revision}}<time class="table-secondary" :datetime="activity.updatedAt||activity.createdAt">{{formatDate(activity.updatedAt||activity.createdAt)}}</time></td><td><a class="table-link" :href="href('activities/'+activity.id, { assetId: String(activity.assetId), activityId: String(activity.id) })">查看详情</a></td></tr></tbody></table></div>
      <div v-if="!loading&&!errorMessage&&!items.length" class="empty-state"><strong>{{kind?'没有符合条件的活动':'尚未登记处理活动'}}</strong><p>{{kind?'选择其他活动场景查看记录。':'按资产登记内部处理、对外共享、数据出境或 AI 使用。'}}</p></div><div class="panel-footer activity-list-footer"><span class="field-hint">已加载 {{items.length}} 项，按当前场景筛选</span><button v-if="cursor!==null&&!errorMessage" class="button button-secondary" type="button" :disabled="loading" @click="load(true)">{{loading?'正在读取…':'加载更多活动'}}</button></div>
    </section>
</div>
</template>
<style scoped>
.activities-layout{align-items:start}.activities-layout>*{min-width:0}.activities-layout>.panel>.table-scroll{max-height:calc(100vh - 370px);overflow:auto}.activity-kind-tabs{display:flex;flex-wrap:wrap;gap:4px;padding:0 16px;border-bottom:1px solid var(--line)}.activity-kind-tabs button{padding:13px 12px;border:0;border-bottom:2px solid transparent;background:transparent;color:var(--secondary);font-size:12px;cursor:pointer}.activity-kind-tabs button.active{border-bottom-color:var(--accent);color:var(--accent);font-weight:700}.activity-table{min-width:680px;table-layout:fixed;font-size:13px}.activity-table th:first-child{width:32%}.activity-table th:nth-child(2){width:18%}.activity-table th:nth-child(3){width:25%}.activity-table th:nth-child(4){width:15%}.activity-table th:last-child{width:10%}.activity-table :is(th,td){overflow-wrap:anywhere;vertical-align:top}.activity-table .table-secondary{font-size:12px}.activity-table tr.selected{background:var(--accent-soft)}.activity-table tr.selected th{box-shadow:inset 1px 0 var(--accent)}.activity-select{padding:0;border:0;background:none;text-align:left;font:inherit;font-weight:700;color:var(--ink);cursor:pointer;overflow-wrap:anywhere}.activity-context{padding:18px;display:grid;gap:14px;overflow-wrap:anywhere}.activity-context h3{font-size:15px;line-height:1.6}.activity-context .badge{justify-self:start}.activity-context dl{margin:0;font-size:12px}.activity-context dl>div{display:grid;grid-template-columns:74px minmax(0,1fr);gap:10px;border-bottom:1px solid var(--line);padding:10px 0}.activity-context dt{color:var(--secondary)}.activity-context dd{margin:0}.activity-list-footer{display:flex;justify-content:space-between;align-items:center;gap:12px}.activities-layout{grid-template-columns:minmax(0,1fr) 290px}
.activity-table td:last-child a{white-space:nowrap}
</style>
