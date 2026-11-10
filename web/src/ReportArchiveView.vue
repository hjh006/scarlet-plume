<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { archiveFailureLabels, type ReportArchivePage } from './reports'
import Icon from './Icon.vue'

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const archivePath = home + '/reports/archives'
const apiUrl = workspacePath(props.workspace.slug) + '/reports/archives'
const active = new AbortController()
const page = ref<ReportArchivePage | null>(null)
const loading = ref(true)
const loadError = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    page.value = await request<ReportArchivePage>(apiUrl, { signal: active.signal })
  } catch (error) {
    if (active.signal.aborted) return
    if (error instanceof ApiError && error.status === 401) emit('sessionExpired')
    else loadError.value = describeError(error)
  } finally {
    loading.value = false
  }
}

onMounted(() => { void load() })
onBeforeUnmount(() => active.abort())
</script>

<template>
  <a class="back-link" :href="home + '/reports'"><Icon name="back" />返回合规报告</a>
  <header class="page-header">
    <div>
      <h1>自动报告归档</h1>
      <p>追溯每次 PostgreSQL 采集后的数据识别和检查快照。</p>
    </div>
    <div class="page-actions">
      <a class="button button-secondary" :href="home + '/automation'">查看采集任务</a>
      <button type="button" class="button button-secondary" :disabled="loading" @click="load">刷新归档</button>
    </div>
  </header>
  <section class="panel" aria-labelledby="report-archives-title" :aria-busy="loading">
    <div class="panel-heading">
      <div>
        <h2 id="report-archives-title">最近归档</h2>
        <p v-if="page">展示工作区最近 100 条归档，每个任务保留最近 {{ page.retentionPerJob }} 条。</p>
      </div>
    </div>
    <p v-if="loadError" class="message message-error report-feedback" role="alert">{{ loadError }}</p>
    <p v-else-if="loading && !page" class="empty-state" role="status">正在读取归档…</p>
    <div v-else-if="page?.items.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="report-archives-title">
      <table class="data-table report-table report-archive-table">
        <thead><tr><th scope="col">采集来源</th><th scope="col">结果</th><th scope="col">识别与检查</th><th scope="col">生成时间</th></tr></thead>
        <tbody>
          <tr v-for="item in page.items" :key="item.id">
            <th scope="row">
              <a v-if="item.status === 'ready'" class="table-link" :href="archivePath + '/' + item.id">{{ item.source }}</a>
              <span v-else>{{ item.source }}</span>
              <span class="table-secondary">任务 #{{ item.jobId }} · <span class="mono">{{ item.runId }}</span></span>
            </th>
            <td>
              <span class="badge" :class="item.status === 'ready' ? 'status-verified' : 'status-needs_work'">
                {{ item.status === 'ready' ? '已归档' : '生成失败' }}
              </span>
              <span v-if="item.status === 'failed'" class="table-secondary">{{ archiveFailureLabels[item.code || ''] || '本次未生成报告。' }}</span>
            </td>
            <td>
              <template v-if="item.identification && item.counts">
                {{ item.identification.tables }} 张表 · {{ item.identification.columns }} 个字段
                <span class="table-secondary">{{ item.identification.sensitiveCandidates }} 个疑似敏感 · {{ item.counts.total }} 项检查</span>
              </template>
              <span v-else>—</span>
            </td>
            <td><time :datetime="item.generatedAt">{{ formatDate(item.generatedAt) }}</time></td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-else class="empty-state">
      <strong>暂无自动报告</strong>
      <p>启用 PostgreSQL 任务的自动归档，下一次采集成功后可查看报告。</p>
    </div>
  </section>
</template>
