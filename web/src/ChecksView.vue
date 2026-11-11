<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { canManageChecks, checkStatusLabels, type Check, type CheckPage } from './workflow'
import Icon from './Icon.vue'

const props = defineProps<{ workspace: Workspace; activityId?: number }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const pageUrl = `#/${encodeURIComponent(props.workspace.slug)}/checks`
const apiUrl = workspacePath(props.workspace.slug) + '/checks'
const items = ref<Check[]>([])
const cursor = ref<number | null>(null)
const status = ref('')
const query = ref('')
const selectedId = ref<number | null>(null)
const visibleItems = computed(() => { const term = query.value.trim().toLowerCase(); return term ? items.value.filter(item => [item.template?.item.title, item.requirement.title, item.requirement.code, item.assetName, item.activity?.name, item.ownerTeam].some(value => value?.toLowerCase().includes(term))) : items.value })
const selected = computed(() => items.value.find(item => item.id === selectedId.value) ?? visibleItems.value[0])
const statusCounts = computed(() => Object.entries(checkStatusLabels).map(([value, label]) => ({ value, label, count: items.value.filter(item => item.status === value).length })))
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
  if (props.activityId) params.set('activityId', String(props.activityId))
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

onMounted(() => { void load() })
onBeforeUnmount(() => active?.abort())
</script>

<template>
  <header v-if="!activityId" class="page-header">
    <div>
      <p class="eyebrow">治理工作区 / 监管检查</p>
      <h1>监管检查</h1>
      <p>关联监管要求和数据资产，记录取证、整改与独立复核。</p>
    </div>
    <a v-if="canManageChecks(workspace.role)" class="button button-primary" :href="pageUrl + '/new'">
      <Icon name="plus" />新建检查
    </a>
  </header>

  <div v-if="!activityId" class="check-status-strip" aria-label="本次载入检查状态">
    <div v-for="entry in statusCounts" :key="entry.value"><span>{{ entry.label }}</span><strong>{{ entry.count }}</strong></div>
    <p>本次载入 {{ items.length }} 项<span v-if="cursor !== null"> · 仍有更多记录</span></p>
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
          <tr><th scope="col">监管要求与资产</th><th scope="col">责任团队</th><th scope="col">当前阶段</th><th scope="col" class="wide-date">更新时间</th></tr>
        </thead>
        <tbody>
          <tr v-for="check in visibleItems" :key="check.id" :class="{ selected: selected?.id === check.id }">
            <th scope="row">
              <a class="table-link" :href="pageUrl + '/' + check.id">{{ check.template?.item.title || check.requirement.title }}</a>
              <span class="table-secondary">{{ check.requirement.code }} · {{ check.assetName }}</span>
              <span v-if="!activityId && check.activity" class="table-secondary">{{ check.activity.name }}</span>
              <time class="table-secondary compact-date" :datetime="check.updatedAt">{{ formatDate(check.updatedAt) }}</time>
            </th>
            <td>{{ check.ownerTeam }}</td>
            <td><span class="badge status" :class="'status-' + check.status">{{ checkStatusLabels[check.status] }}</span></td>
            <td class="wide-date"><time :datetime="check.updatedAt">{{ formatDate(check.updatedAt) }}</time><button class="check-select" type="button" :aria-pressed="selected?.id === check.id" @click="selectedId = check.id">查看摘要</button></td>
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
      <a class="button button-primary" :href="pageUrl + '/' + selected.id">查看证据与复核</a>
    </div><p v-else class="empty-state">选择检查后查看采用范围。</p></section>
    <section class="panel"><div class="panel-heading"><h2>核验顺序</h2></div><ol class="checks-process"><li>核对适用要求与来源</li><li>登记观察和整改</li><li>取得同范围新复测</li><li>交由独立人员复核</li></ol></section>
  </aside>
  </div>
</template>

<style scoped>
.check-status-strip{display:grid;grid-template-columns:repeat(4,minmax(0,1fr)) auto;align-items:center;border:1px solid var(--line);border-radius:12px;background:var(--surface);margin:0 0 18px;padding:18px 20px;gap:20px}.check-status-strip>div{display:grid;gap:4px;border-right:1px solid var(--line)}.check-status-strip span{font-size:12px;color:var(--secondary)}.check-status-strip strong{font-size:27px;font-variant-numeric:tabular-nums;line-height:1.3}.check-status-strip p{font-size:11px;color:var(--secondary)}.checks-workbench{display:grid;grid-template-columns:minmax(0,1fr) 300px;gap:16px;align-items:start}.checks-embedded{display:block}.checks-search{margin-left:auto;display:flex;align-items:center;gap:8px}.checks-search input{min-width:185px;max-width:240px}.check-select{display:block;margin-top:5px;padding:0;background:none;border:0;color:var(--accent);font-size:11px}.check-table tr.selected{background:#edf9fc}.check-table tr.selected>th{box-shadow:inset 3px 0 #00889d}.checks-context{display:grid;gap:16px}.checks-context-body{padding:18px;display:grid;gap:14px}.checks-context-body h3{line-height:1.6}.checks-context-body dl{margin:0;font-size:12px}.checks-context-body dl>div{display:grid;grid-template-columns:72px minmax(0,1fr);gap:10px;padding:10px 0;border-bottom:1px solid var(--line)}.checks-context-body dt{color:var(--secondary)}.checks-context-body dd{margin:0;overflow-wrap:anywhere}.checks-process{list-style:none;margin:0;padding:18px 22px;display:grid;gap:15px;counter-reset:step}.checks-process li{counter-increment:step;font-size:12px}.checks-process li::before{content:counter(step);display:inline-grid;place-items:center;width:21px;height:21px;border:1px solid #a6d8e0;border-radius:50%;color:var(--teal);margin-right:9px}.checks-workbench .panel{min-width:0}@media(max-width:1320px){.checks-workbench{grid-template-columns:minmax(0,1fr) 270px}.checks-search{margin-left:0}.check-status-strip{gap:12px;padding:15px}.check-status-strip p{max-width:90px}}
</style>
