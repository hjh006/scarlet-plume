<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { canManageChecks, checkStatusLabels, type Check, type CheckPage, type CheckDetail } from './workflow'
import Icon from './Icon.vue'
import EvidencePath from './EvidencePath.vue'
import { useScenarioContext } from './scenario-context'

const props = defineProps<{ workspace: Workspace; activityId?: number }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const { context, href } = useScenarioContext(props.workspace.slug)
const pageUrl = computed(() => href('checks'))
const scopeActivity = computed(() => props.activityId ? String(props.activityId) : context.value.activityId)
const apiUrl = workspacePath(props.workspace.slug) + '/checks'
const items = ref<Check[]>([])
const cursor = ref<number | null>(null)
const status = ref('')
const query = ref('')
const selectedId = ref<number | null>(null)
const visibleItems = computed(() => { const term = query.value.trim().toLowerCase(); return term ? items.value.filter(item => [item.template?.item.title, item.requirement.title, item.requirement.code, item.assetName, item.activity?.name, item.ownerTeam].some(value => value?.toLowerCase().includes(term))) : items.value })
const selected = computed(() => items.value.find(item => item.id === selectedId.value) ?? visibleItems.value[0])
const statusCounts = computed(() => Object.entries(checkStatusLabels).map(([value, label]) => ({ value, label, count: items.value.filter(item => item.status === value).length })))
const selectedDetail = ref<CheckDetail>()
const detailLoading = ref(false)
const detailError = ref('')
let detailRequest: AbortController | undefined
const observation = computed(() => selectedDetail.value?.evidence.filter(e => e.kind === 'observation').sort((a,b) => Date.parse(b.observedAt)-Date.parse(a.observedAt))[0])
const evidenceStages = computed(() => {
  const d=selectedDetail.value
  return [
    {label:'采用范围',value:d?.assetName || '选择检查',note:d?.requirement.code,tone:d?'known':'pending'},
    {label:'原始观察',value:observation.value ? formatDate(observation.value.observedAt) : '尚未登记',tone:observation.value?'known':'pending'},
    {label:'整改记录',value:d?.evidence.some(e=>e.kind==='remediation')?'已有整改依据':'尚未登记',tone:d?.evidence.some(e=>e.kind==='remediation')?'known':'pending'},
    {label:'新来源复测',value:d?.evidence.some(e=>e.kind==='retest')?'已有复测记录':'尚未取得',tone:d?.evidence.some(e=>e.kind==='retest')?'known':'pending'},
    {label:'独立复核',value:d?checkStatusLabels[d.status]:'尚无记录',tone:d?.status==='verified'?'known':'pending'},
  ] as {label:string;value:string;note?:string;tone:'known'|'pending'}[]
})
watch(() => selected.value?.id, async id => {
  detailRequest?.abort();selectedDetail.value=undefined;detailError.value='';detailLoading.value=false
  if(!id || props.activityId) return
  const op=new AbortController();detailRequest=op;detailLoading.value=true
  try {const value=await request<CheckDetail>(apiUrl+'/'+id,{signal:op.signal});if(!op.signal.aborted)selectedDetail.value=value}
  catch(e){if(!op.signal.aborted){if(e instanceof ApiError&&e.status===401)emit('sessionExpired');else detailError.value=describeError(e)}}
  finally{if(!op.signal.aborted)detailLoading.value=false}
})
const loading = ref(true)
const errorMessage = ref('')
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
  if (scopeActivity.value) params.set('activityId', scopeActivity.value)
  if (context.value.assetId) params.set('assetId', context.value.assetId)
  if (status.value) params.set('status', status.value)
  if (append && cursor.value !== null) params.set('before', String(cursor.value))
  try {
    const result = await request<CheckPage>(apiUrl + '?' + params, { signal: operation.signal })
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

watch(() => [context.value.assetId, context.value.activityId], () => { void load() })
onMounted(() => { void load() })
onBeforeUnmount(() => {active?.abort();detailRequest?.abort()})
</script>

<template>
  <header v-if="!activityId" class="page-header">
    <div>
      <p class="eyebrow">治理工作区 / 监管检查</p>
      <h1>监管检查与整改复核</h1>
      <p>关联监管要求和数据资产，记录取证、整改与独立复核。</p>
    </div>
    <a v-if="canManageChecks(workspace.role)" class="button button-primary" :href="href(scopeActivity ? 'checks/new/' + scopeActivity : 'checks/new')">
      <Icon name="plus" />新建检查
    </a>
  </header>

  <p v-if="!activityId && (context.assetId || scopeActivity)" class="message">本次检查范围：资产 #{{ context.assetId || '按活动确定' }}<template v-if="scopeActivity"> · 活动 #{{ scopeActivity }}</template>。下列记录按服务端范围读取。</p>
  <section v-if="!activityId" class="check-evidence-workspace" aria-label="选中检查的实际证据">
    <EvidencePath :items="evidenceStages" label="当前检查的取证与复核记录" />
    <p v-if="detailLoading" class="message" role="status">正在读取所选检查的证据…</p><p v-if="detailError" class="message message-error" role="alert">{{ detailError }}</p>
    <div v-if="selectedDetail && !detailLoading" class="check-evidence-grid">
      <section class="panel"><div class="panel-heading"><div><h2>{{ selectedDetail.template?.item.title || selectedDetail.requirement.title }}</h2><p>{{ selectedDetail.assetName }} · {{ selectedDetail.requirement.code }}</p></div><span class="badge">{{ checkStatusLabels[selectedDetail.status] }}</span></div>
        <div class="check-evidence-body"><div class="check-comparison"><section><span>采用要求与基线</span><h3>{{ selectedDetail.requirement.citation }}</h3><p>{{ selectedDetail.template?.item.acceptanceCriteria || selectedDetail.requirement.summary }}</p><a :href="selectedDetail.requirement.sourceUrl" target="_blank" rel="noopener noreferrer">查看采用依据</a></section><span class="comparison-vs">VS</span><section><span>实际来源观察</span><h3>{{ observation?.sourceSystem || '尚无原始观察' }}</h3><p>{{ observation?.summary || '先登记实际观察；缺少来源时保留未知。' }}</p><small v-if="observation">{{ observation.sourceRecord }} · {{ formatDate(observation.observedAt) }}</small></section></div>
          <div class="check-evidence-ledger"><h3>采用证据台账</h3><div v-if="selectedDetail.evidence.length" class="table-scroll" role="region" tabindex="0" aria-label="采用证据台账"><table class="data-table"><thead><tr><th scope="col">证据阶段</th><th scope="col">来源与摘要</th><th scope="col" class="table-date">观察时间</th></tr></thead><tbody><tr v-for="entry in selectedDetail.evidence" :key="entry.id"><th scope="row">{{ {observation:'原始观察',remediation:'整改记录',retest:'复测记录'}[entry.kind] }}</th><td>{{ entry.summary }}<small>{{ entry.sourceSystem }} · {{ entry.sourceRecord }}</small></td><td class="table-date">{{ formatDate(entry.observedAt) }}</td></tr></tbody></table></div><p v-else class="field-hint">尚未登记可采用证据；未观察不能视为符合。</p></div>
        </div>
      </section><aside class="panel check-dossier"><h2><Icon name="reports" />证据档案</h2><dl><div><dt>检查记录</dt><dd>#{{ selectedDetail.id }}</dd></div><div><dt>实际对象</dt><dd>{{ selectedDetail.assetName }}</dd></div><div><dt>模板采用</dt><dd>{{ selectedDetail.template ? selectedDetail.template.title + ' · v' + selectedDetail.template.version : '采用监管要求登记' }}</dd></div><div><dt>来源记录</dt><dd>{{ observation?.sourceRecord || '尚未取得' }}</dd></div><div><dt>更新时间</dt><dd>{{ formatDate(selectedDetail.updatedAt) }}</dd></div></dl><p>整改依据、复测记录与独立复核分别留存；措施效果和未覆盖条件以详情中的实际证据为准。</p><a class="button button-primary" :href="href('checks/'+selectedDetail.id)">进入取证与独立复核</a></aside>
    </div>
  </section>
  <div v-if="!activityId" class="check-status-strip" aria-label="本次载入检查状态">
    <div v-for="entry in statusCounts" :key="entry.value"><span>{{ entry.label }}</span><strong>{{ !items.length && (loading || errorMessage) ? '—' : entry.count }}</strong></div>
    <p v-if="!items.length && loading" role="status">正在读取检查状态…</p>
    <p v-else-if="!items.length && errorMessage">读取失败，检查状态未知</p>
    <p v-else>已载入 {{ items.length }} 项<span v-if="cursor !== null"> · 仍有更多记录</span></p>
  </div>
  <div class="checks-workbench" :class="{ 'checks-embedded': activityId }">
  <section class="panel" :class="{ 'activity-related': activityId }" aria-labelledby="checks-title" :aria-busy="loading">
    <div class="panel-heading"><h2 id="checks-title">{{ activityId ? '关联检查' : '检查台账' }}</h2></div>
    <div class="filter-bar">
      <label for="check-status">检查状态</label>
      <select id="check-status" v-model="status" @change="load()">
        <option value="">全部状态</option>
        <option v-for="(label, value) in checkStatusLabels" :key="value" :value="value">{{ label }}</option>
      </select>
      <label class="checks-search" for="check-query">检索已载入记录<input id="check-query" v-model="query" type="search" placeholder="检查、资产或责任团队"></label>
    </div>
    <p v-if="loading && !items.length" class="empty-state" role="status">正在读取检查记录…</p>
    <div v-if="errorMessage" class="empty-state">
      <p role="alert">{{ errorMessage }}</p>
      <button class="button button-secondary" type="button" @click="load()">重新读取</button>
    </div>
    <div v-if="visibleItems.length" class="table-scroll" tabindex="0" role="region" aria-label="检查台账">
      <table class="data-table check-table">
        <thead>
          <tr><th scope="col">监管要求与资产</th><th scope="col">责任团队</th><th scope="col">当前阶段</th><th scope="col" class="wide-date table-date">更新时间</th></tr>
        </thead>
        <tbody>
          <tr v-for="check in visibleItems" :key="check.id" :class="{ selected: selected?.id === check.id }">
            <th scope="row">
              <a class="table-link" :href="href('checks/' + check.id)">{{ check.template?.item.title || check.requirement.title }}</a>
              <span class="table-secondary">{{ check.requirement.code }} · {{ check.assetName }}</span>
              <span v-if="!activityId && check.activity" class="table-secondary">{{ check.activity.name }}</span>
              <div class="compact-date"><time class="table-secondary" :datetime="check.updatedAt">{{ formatDate(check.updatedAt) }}</time><button class="check-select" type="button" :aria-pressed="selected?.id === check.id" @click="selectedId = check.id">查看摘要</button></div>
            </th>
            <td>{{ check.ownerTeam }}</td>
            <td><span class="badge status" :class="'status-' + check.status">{{ checkStatusLabels[check.status] }}</span></td>
            <td class="wide-date table-date"><time :datetime="check.updatedAt">{{ formatDate(check.updatedAt) }}</time><button class="check-select" type="button" :aria-pressed="selected?.id === check.id" @click="selectedId = check.id">查看摘要</button></td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-if="!loading && !errorMessage && !visibleItems.length" class="empty-state">
      <strong>{{ status || query ? '没有符合条件的检查' : '尚未登记监管检查' }}</strong>
      <p v-if="status || query">调整状态或检索条件可查看相应记录。</p>
      <p v-else-if="activityId">关联后可在检查中登记批准依据、实际配置和整改复测证据。</p>
      <p v-else>检查可关联已登记的数据资产及处理活动。</p>
    </div>
    <div v-if="cursor !== null && !errorMessage" class="panel-footer">
      <button class="button button-secondary" type="button" :disabled="loading" @click="load(true)">
        {{ loading ? '正在读取…' : '加载更多检查' }}
      </button>
    </div>
  </section>
  <aside v-if="!activityId" class="context-rail checks-context">
    <section class="panel"><div class="panel-heading"><h2>检查摘要</h2></div><div v-if="selected" class="checks-context-body">
      <span class="badge status" :class="'status-' + selected.status">{{ checkStatusLabels[selected.status] }}</span>
      <h3>{{ selected.template?.item.title || selected.requirement.title }}</h3>
      <dl><div><dt>监管要求</dt><dd>{{ selected.requirement.code }}</dd></div><div><dt>关联资产</dt><dd>{{ selected.assetName }}</dd></div><div><dt>责任团队</dt><dd>{{ selected.ownerTeam }}</dd></div><div><dt>更新记录</dt><dd>{{ formatDate(selected.updatedAt) }}</dd></div></dl>
      <p class="field-hint">证据新鲜度、来源覆盖与下一步操作以检查详情中的实际取证为准。</p>
      <a class="button button-primary" :href="href('checks/' + selected.id)">查看证据与复核</a>
    </div><p v-else class="empty-state">选择检查后查看采用范围。</p></section>
    <section class="panel"><div class="panel-heading"><h2>核验顺序</h2></div><ol class="checks-process"><li>核对适用要求与来源</li><li>登记观察和整改</li><li>取得同范围新复测</li><li>交由独立人员复核</li></ol></section>
  </aside>
  </div>
</template>

<style scoped>
.check-status-strip{display:grid;grid-template-columns:repeat(4,minmax(0,1fr)) auto;align-items:center;border:1px solid var(--line);border-radius:12px;background:var(--surface);margin:0 0 18px;padding:18px 20px;gap:20px}.check-status-strip>div{display:grid;gap:4px;border-right:1px solid var(--line)}.check-status-strip span{font-size:12px;color:var(--secondary)}.check-status-strip strong{font-size:27px;font-variant-numeric:tabular-nums;line-height:1.3}.check-status-strip p{font-size:12px;color:var(--secondary)}.checks-workbench{display:grid;grid-template-columns:minmax(0,1fr) 300px;gap:16px;align-items:start}.checks-embedded{display:block}.checks-search{margin-left:auto;display:flex;align-items:center;gap:8px}.checks-search input{min-width:185px;max-width:240px}.check-select{display:block;margin-top:5px;padding:0;background:none;border:0;color:var(--accent);font-size:12px}.check-table tr.selected{background:var(--accent-soft)}.check-table tr.selected>th{box-shadow:inset 2px 0 var(--accent)}.checks-context{display:grid;gap:16px}.checks-context-body{padding:18px;display:grid;gap:14px}.checks-context-body h3{line-height:1.6}.checks-context-body dl{margin:0;font-size:12px}.checks-context-body dl>div{display:grid;grid-template-columns:72px minmax(0,1fr);gap:10px;padding:10px 0;border-bottom:1px solid var(--line)}.checks-context-body dt{color:var(--secondary)}.checks-context-body dd{margin:0;overflow-wrap:anywhere}.checks-process{list-style:none;margin:0;padding:18px 22px;display:grid;gap:15px;counter-reset:step}.checks-process li{counter-increment:step;font-size:12px}.checks-process li::before{content:counter(step);display:inline-grid;place-items:center;width:21px;height:21px;border:1px solid var(--line);border-radius:50%;color:var(--teal);margin-right:9px}.checks-workbench .panel{min-width:0}@media(max-width:1320px){.checks-workbench{grid-template-columns:minmax(0,1fr) 270px}.checks-search{margin-left:0}.check-status-strip{gap:12px;padding:15px}.check-status-strip p{max-width:90px}}
</style>

<style scoped>.check-evidence-workspace{margin-bottom:20px}.check-evidence-grid{display:grid;grid-template-columns:minmax(0,1fr) 310px;gap:16px}.check-evidence-grid>.panel{min-width:0}.check-evidence-body{padding:20px}.check-comparison{display:grid;grid-template-columns:1fr 38px 1fr;align-items:stretch;gap:8px}.check-comparison>section{border:1px solid #cadfed;border-radius:8px;background:#f5faff;padding:18px;min-width:0}.check-comparison>section:first-child{background:#f0f9f4;border-color:#c3e3d0}.check-comparison h3{font-size:16px;line-height:1.7;margin:9px 0}.check-comparison p{font-size:13px;line-height:1.9;white-space:pre-wrap;overflow-wrap:anywhere}.check-comparison span,.check-comparison small{font-size:12px;color:var(--secondary);overflow-wrap:anywhere}.comparison-vs{align-self:center;justify-self:center;font-weight:700}.check-evidence-ledger{margin-top:20px}.check-evidence-ledger h3{font-size:16px;margin-bottom:14px}.check-evidence-ledger small{display:block;font-size:12px;margin-top:6px;color:var(--secondary);overflow-wrap:anywhere}.check-dossier{padding:20px}.check-dossier h2{display:flex;align-items:center;gap:9px;font-size:18px}.check-dossier dl{margin:20px 0}.check-dossier dl>div{display:grid;grid-template-columns:74px 1fr;gap:12px;padding:11px 0;border-bottom:1px solid var(--line);font-size:13px}.check-dossier dt{color:var(--secondary)}.check-dossier dd{margin:0;overflow-wrap:anywhere}.check-dossier p{font-size:12px;line-height:1.9;color:var(--secondary);margin-bottom:18px}.check-status-strip{display:flex;flex-wrap:wrap;gap:18px}.check-status-strip>div{display:flex;gap:10px;align-items:center;padding-right:18px}.check-status-strip strong{font-size:20px}@media(max-width:1200px){.check-evidence-grid{grid-template-columns:minmax(0,1fr) 270px}.check-evidence-body{padding:16px}.check-comparison>section{padding:13px}}@media(max-width:850px){.check-evidence-grid{grid-template-columns:1fr}.check-comparison{grid-template-columns:1fr}.comparison-vs{padding:6px}.checks-workbench{grid-template-columns:1fr}}</style>
