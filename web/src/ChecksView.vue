<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { canManageChecks, checkStatusLabels, type Check, type CheckPage } from './workflow'
import Icon from './Icon.vue'

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const pageUrl = `#/${encodeURIComponent(props.workspace.slug)}/checks`
const apiUrl = workspacePath(props.workspace.slug) + '/checks'
const items = ref<Check[]>([])
const cursor = ref<number | null>(null)
const status = ref('')
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
  <header class="page-header">
    <div>
      <p class="eyebrow">治理工作区 / 监管检查</p>
      <h1>监管检查</h1>
      <p>关联监管要求和数据资产，记录取证、整改与独立复核。</p>
    </div>
    <a v-if="canManageChecks(workspace.role)" class="button button-primary" :href="pageUrl + '/new'">
      <Icon name="plus" />新建检查
    </a>
  </header>

  <section class="panel" aria-labelledby="checks-title" :aria-busy="loading">
    <div class="panel-heading"><h2 id="checks-title">检查台账</h2></div>
    <div class="filter-bar">
      <label for="check-status">检查状态</label>
      <select id="check-status" v-model="status" @change="load()">
        <option value="">全部状态</option>
        <option v-for="(label, value) in checkStatusLabels" :key="value" :value="value">{{ label }}</option>
      </select>
    </div>
    <p v-if="loading && !items.length" class="empty-state" role="status">正在读取检查记录…</p>
    <div v-if="errorMessage" class="empty-state">
      <p role="alert">{{ errorMessage }}</p>
      <button class="button button-secondary" type="button" @click="load()">重新读取</button>
    </div>
    <div v-if="items.length" class="table-scroll" tabindex="0" role="region" aria-label="检查台账">
      <table class="data-table check-table">
        <thead>
          <tr><th scope="col">监管要求与资产</th><th scope="col">责任团队</th><th scope="col">状态</th><th scope="col" class="wide-date">更新时间</th></tr>
        </thead>
        <tbody>
          <tr v-for="check in items" :key="check.id">
            <th scope="row">
              <a class="table-link" :href="pageUrl + '/' + check.id">{{ check.requirement.title }}</a>
              <span class="table-secondary">{{ check.requirement.code }} · {{ check.assetName }}</span>
              <time class="table-secondary compact-date" :datetime="check.updatedAt">{{ formatDate(check.updatedAt) }}</time>
            </th>
            <td>{{ check.ownerTeam }}</td>
            <td><span class="badge status" :class="'status-' + check.status">{{ checkStatusLabels[check.status] }}</span></td>
            <td class="wide-date"><time :datetime="check.updatedAt">{{ formatDate(check.updatedAt) }}</time></td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-if="!loading && !errorMessage && !items.length" class="empty-state">
      <strong>{{ status ? '没有符合条件的检查' : '尚未登记监管检查' }}</strong>
      <p v-if="status">选择其他状态可查看相应记录。</p>
      <p v-else>检查记录按工作区管理，可关联已登记的数据资产。</p>
    </div>
    <div v-if="cursor !== null && !errorMessage" class="panel-footer">
      <button class="button button-secondary" type="button" :disabled="loading" @click="load(true)">
        {{ loading ? '正在读取…' : '加载更多检查' }}
      </button>
    </div>
  </section>
</template>
