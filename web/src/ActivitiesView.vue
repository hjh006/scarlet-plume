<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { activityKindLabels, type Activity, type ActivityPage } from './activity'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { canManageChecks } from './workflow'
import Icon from './Icon.vue'
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
const selectedId = ref<number | null>(null)
const selected = computed(() => items.value.find(item => item.id === selectedId.value) ?? items.value[0] ?? null)
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
onBeforeUnmount(() => active?.abort())
</script>

<template>
  <div class="activities-workbench">
    <header class="page-header"><div><p class="eyebrow">数据治理 / 处理活动</p><h1>处理活动</h1><p>记录数据使用目的与流向，按真实修订追溯检查、评估和变更。</p></div><a v-if="canManageChecks(workspace.role)" class="button button-primary" :href="href('activities/new', { assetId: context.assetId, activityId: '' })"><Icon name="plus"/>新建活动</a></header>
    <p v-if="context.assetId" class="message">本次资产范围：#{{ context.assetId }}。按该资产读取已登记活动，活动类型筛选继续有效。</p>
    <div class="workbench-grid activities-layout"><section class="panel" aria-labelledby="activities-title" :aria-busy="loading"><div class="panel-heading"><h2 id="activities-title">处理活动台账</h2><button type="button" class="button button-secondary" :disabled="loading" @click="load()">刷新</button></div><nav class="activity-kind-tabs" aria-label="活动场景"><button type="button" :class="{active:!kind}" :aria-pressed="!kind" @click="kind='';load()">全部</button><button v-for="(label,value) in activityKindLabels" :key="value" type="button" :class="{active:kind===value}" :aria-pressed="kind===value" @click="kind=String(value);load()">{{label}}</button></nav>
      <p v-if="loading&&!items.length" class="empty-state" role="status">正在读取处理活动…</p><div v-if="errorMessage" class="empty-state"><p role="alert">{{errorMessage}}</p><button class="button button-secondary" type="button" @click="load()">重新读取</button></div>
      <div v-if="items.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="activities-title"><table class="data-table activity-table"><thead><tr><th scope="col">活动名称 / 关联资产</th><th scope="col">场景</th><th scope="col">接收方 / 区域</th><th scope="col">最近修订</th><th scope="col">操作</th></tr></thead><tbody><tr v-for="activity in items" :key="activity.id" :class="{selected:selected?.id===activity.id}"><th scope="row"><button class="activity-select" type="button" :aria-pressed="selected?.id===activity.id" @click="selectedId=activity.id">{{activity.name}}</button><span class="table-secondary">{{activity.assetName}}</span></th><td><span class="badge" :class="activity.kind==='ai'?'status-in_review':'status-draft'">{{activityKindLabels[activity.kind]}}</span></td><td>{{activity.recipient||'未记录'}}<span class="table-secondary">{{activity.region||'地域未记录'}}</span></td><td>v{{activity.revision}}<time class="table-secondary" :datetime="activity.updatedAt||activity.createdAt">{{formatDate(activity.updatedAt||activity.createdAt)}}</time></td><td><a class="table-link" :href="href('activities/'+activity.id, { assetId: String(activity.assetId), activityId: String(activity.id) })">查看详情</a></td></tr></tbody></table></div>
      <div v-if="!loading&&!errorMessage&&!items.length" class="empty-state"><strong>{{kind?'没有符合条件的活动':'尚未登记处理活动'}}</strong><p>{{kind?'选择其他活动场景查看记录。':'按资产登记内部处理、对外共享、数据出境或 AI 使用。'}}</p></div><div class="panel-footer activity-list-footer"><span class="field-hint">已加载 {{items.length}} 项，按当前场景筛选</span><button v-if="cursor!==null&&!errorMessage" class="button button-secondary" type="button" :disabled="loading" @click="load(true)">{{loading?'正在读取…':'加载更多活动'}}</button></div>
    </section><aside class="context-rail section-stack"><section class="panel"><div class="panel-heading"><h2>活动详情</h2></div><div v-if="selected" class="activity-context"><h3>{{selected.name}}</h3><span class="badge">{{activityKindLabels[selected.kind]}} · v{{selected.revision}}</span><dl><div><dt>关联资产</dt><dd>{{selected.assetName}}</dd></div><div><dt>处理目的</dt><dd>{{selected.purpose}}</dd></div><div><dt>接收方</dt><dd>{{selected.recipient||'未记录'}}</dd></div><div><dt>处理地域</dt><dd>{{selected.region||'未记录'}}</dd></div><div><dt>最近变更</dt><dd>{{formatDate(selected.updatedAt||selected.createdAt)}}</dd></div></dl><a class="button button-primary" :href="href('activities/'+selected.id, { assetId: String(selected.assetId), activityId: String(selected.id) })">查看活动与历史</a><a class="table-link" :href="href('reports/'+selected.id, { assetId: String(selected.assetId), activityId: String(selected.id) })">查看活动范围报告</a></div><p v-else class="empty-state">选择活动后查看当前修订与处理范围。</p></section><section class="panel"><div class="panel-heading"><h2>变更与重评</h2></div><p class="activity-context field-hint">活动变化会影响采用该范围的专项评估。当前活动、评估采用时点和独立复核结论分别保留。</p></section></aside></div>
  </div>
</template>
<style scoped>
.activities-layout{align-items:start}.activities-layout>*{min-width:0}.activities-layout>.panel>.table-scroll{max-height:calc(100vh - 370px);overflow:auto}.activity-kind-tabs{display:flex;flex-wrap:wrap;gap:4px;padding:0 16px;border-bottom:1px solid var(--line)}.activity-kind-tabs button{padding:13px 12px;border:0;border-bottom:2px solid transparent;background:transparent;color:var(--secondary);font-size:12px;cursor:pointer}.activity-kind-tabs button.active{border-bottom-color:var(--accent);color:var(--accent);font-weight:700}.activity-table{min-width:680px;table-layout:fixed;font-size:13px}.activity-table th:first-child{width:32%}.activity-table th:nth-child(2){width:18%}.activity-table th:nth-child(3){width:25%}.activity-table th:nth-child(4){width:15%}.activity-table th:last-child{width:10%}.activity-table :is(th,td){overflow-wrap:anywhere;vertical-align:top}.activity-table .table-secondary{font-size:12px}.activity-table tr.selected{background:var(--accent-soft)}.activity-table tr.selected th{box-shadow:inset 1px 0 var(--accent)}.activity-select{padding:0;border:0;background:none;text-align:left;font:inherit;font-weight:700;color:var(--ink);cursor:pointer;overflow-wrap:anywhere}.activity-context{padding:18px;display:grid;gap:14px;overflow-wrap:anywhere}.activity-context h3{font-size:15px;line-height:1.6}.activity-context .badge{justify-self:start}.activity-context dl{margin:0;font-size:12px}.activity-context dl>div{display:grid;grid-template-columns:74px minmax(0,1fr);gap:10px;border-bottom:1px solid var(--line);padding:10px 0}.activity-context dt{color:var(--secondary)}.activity-context dd{margin:0}.activity-list-footer{display:flex;justify-content:space-between;align-items:center;gap:12px}.activities-layout{grid-template-columns:minmax(0,1fr) 290px}
.activity-table td:last-child a{white-space:nowrap}
</style>
