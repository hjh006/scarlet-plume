<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
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
const selectedId = ref<number | null>(null)
const selected = computed(()=>page.value?.items.find(item=>item.id===selectedId.value)??page.value?.items[0])

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
      <h1>历史报告归档</h1>
      <p>追溯每次 PostgreSQL 采集后的数据识别和检查快照。</p>
    </div>
    <div class="page-actions">
      <a class="button button-secondary" :href="home + '/automation'">查看采集任务</a>
      <button type="button" class="button button-secondary" :disabled="loading" @click="load">刷新归档</button>
    </div>
  </header>
  <div class="archive-workbench"><section class="panel" aria-labelledby="report-archives-title" :aria-busy="loading">
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
          <tr v-for="item in page.items" :key="item.id" :class="{selected:selected?.id===item.id}">
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
            <td><time :datetime="item.generatedAt">{{ formatDate(item.generatedAt) }}</time><button class="archive-select" type="button" @click="selectedId=item.id">查看采用范围</button></td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-else class="empty-state">
      <strong>暂无自动报告</strong>
      <p>启用 PostgreSQL 任务的自动归档，下一次采集成功后可查看报告。</p>
    </div>
  </section><aside class="archive-context"><section class="panel"><div class="panel-heading"><h2>历史采用快照</h2></div><div v-if="selected" class="archive-context-body"><span class="badge" :class="selected.status==='ready'?'status-verified':'status-needs_work'">{{selected.status==='ready'?'已归档':'生成失败'}}</span><h3>{{selected.source}}</h3><dl><div><dt>归档时间</dt><dd>{{formatDate(selected.generatedAt)}}</dd></div><div><dt>采集任务</dt><dd>#{{selected.jobId}}</dd></div><div><dt>运行引用</dt><dd class="mono">{{selected.runId}}</dd></div><div><dt>采用范围</dt><dd>本次PostgreSQL来源关联检查</dd></div></dl><template v-if="selected.status==='ready'"><a class="button button-primary" :href="archivePath+'/'+selected.id">预览采用报告</a><a class="button button-secondary" :href="apiUrl+'/'+selected.id+'/export?format=html'">导出 HTML</a><a class="button button-secondary" :href="apiUrl+'/'+selected.id+'/export?format=json'">导出 JSON</a></template><p v-else class="message message-warning">{{archiveFailureLabels[selected.code||'']||'本次未生成报告。可核对采集范围与来源记录。'}}</p></div><p v-else class="empty-state">暂无可查看的历史快照。</p></section><section class="panel"><div class="panel-heading"><h2>留存口径</h2></div><p class="archive-note">归档固定生成时的范围和证据，不随当前来源或检查状态改写。每个任务保留最近 {{page?.retentionPerJob??'限定数量'}} 条。</p></section></aside></div>
</template>
<style scoped>
.archive-workbench{display:grid;grid-template-columns:minmax(0,1fr) 300px;gap:16px;align-items:start}.archive-workbench>.panel{min-width:0}.archive-context{display:grid;gap:16px;position:sticky;top:16px}.archive-context-body{padding:18px;display:grid;gap:12px}.archive-context h3{font-size:14px}.archive-context dl{margin:0;font-size:11px}.archive-context dl>div{display:grid;grid-template-columns:65px minmax(0,1fr);gap:12px;padding:11px 0;border-bottom:1px solid var(--line)}.archive-context dt{color:var(--secondary)}.archive-context dd{margin:0;overflow-wrap:anywhere}.archive-select{display:block;border:0;background:none;padding:0;margin-top:6px;color:var(--accent);font-size:11px}.report-archive-table tr.selected{background:#edf9fc}.report-archive-table tr.selected>th{box-shadow:inset 3px 0 var(--teal)}.archive-note{padding:18px;color:var(--secondary);font-size:12px}@media(max-width:1320px){.archive-workbench{grid-template-columns:minmax(0,1fr) 270px}}
</style>
