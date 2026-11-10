<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { activityKindLabels, type Activity, type ActivityPage } from './activity'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { canManageChecks } from './workflow'
import Icon from './Icon.vue'

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const pageUrl = `#/${encodeURIComponent(props.workspace.slug)}/activities`
const apiUrl = workspacePath(props.workspace.slug) + '/activities'
const items = ref<Activity[]>([])
const cursor = ref<number | null>(null)
const kind = ref('')
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

onMounted(() => { void load() })
onBeforeUnmount(() => active?.abort())
</script>

<template>
  <header class="page-header">
    <div>
      <p class="eyebrow">治理工作区 / 处理活动</p>
      <h1>处理活动</h1>
      <p>登记数据如何使用、流向何处，并关联审批依据、证据与整改记录。</p>
    </div>
    <a v-if="canManageChecks(workspace.role)" class="button button-primary" :href="pageUrl + '/new'">
      <Icon name="plus" />登记活动
    </a>
  </header>

  <section class="panel" aria-labelledby="activities-title" :aria-busy="loading">
    <div class="panel-heading"><h2 id="activities-title">处理活动台账</h2></div>
    <div class="filter-bar">
      <label for="activity-kind-filter">活动类型</label>
      <select id="activity-kind-filter" v-model="kind" @change="load()">
        <option value="">全部类型</option>
        <option v-for="(label, value) in activityKindLabels" :key="value" :value="value">{{ label }}</option>
      </select>
    </div>
    <p v-if="loading && !items.length" class="empty-state" role="status">正在读取处理活动…</p>
    <div v-if="errorMessage" class="empty-state">
      <p role="alert">{{ errorMessage }}</p>
      <button class="button button-secondary" type="button" @click="load()">重新读取</button>
    </div>
    <div v-if="items.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="activities-title">
      <table class="data-table activity-table">
        <thead>
          <tr>
            <th scope="col">活动与资产</th><th scope="col" class="activity-purpose">处理目的</th>
            <th scope="col">接收方与地域</th><th scope="col" class="wide-date">登记时间</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="activity in items" :key="activity.id">
            <th scope="row">
              <a class="table-link" :href="pageUrl + '/' + activity.id">{{ activity.name }}</a>
              <span class="table-secondary">{{ activityKindLabels[activity.kind] }} · {{ activity.assetName }}</span>
              <time class="table-secondary compact-date" :datetime="activity.createdAt">{{ formatDate(activity.createdAt) }}</time>
            </th>
            <td class="activity-purpose"><div class="activity-purpose-preview">{{ activity.purpose }}</div></td>
            <td>{{ activity.recipient || '—' }}<span v-if="activity.region" class="table-secondary">{{ activity.region }}</span></td>
            <td class="date-text wide-date"><time :datetime="activity.createdAt">{{ formatDate(activity.createdAt) }}</time></td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-if="!loading && !errorMessage && !items.length" class="empty-state">
      <strong>{{ kind ? '没有符合条件的活动' : '尚未登记处理活动' }}</strong>
      <p>{{ kind ? '选择其他活动类型查看记录。' : '可按资产登记内部处理、对外共享、数据出境或 AI 使用。' }}</p>
    </div>
    <div v-if="cursor !== null && !errorMessage" class="panel-footer">
      <button class="button button-secondary" type="button" :disabled="loading" @click="load(true)">
        {{ loading ? '正在读取…' : '加载更多活动' }}
      </button>
    </div>
  </section>
</template>
