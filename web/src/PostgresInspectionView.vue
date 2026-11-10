<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { postgresPermissionScope, postgresPublicSelect, postgresSensitivityLabel, type PostgresInspection } from './postgresql'
import Icon from './Icon.vue'

const props = defineProps<{ workspace: Workspace; assetId: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const apiUrl = workspacePath(props.workspace.slug) + '/assets/' + encodeURIComponent(props.assetId) + '/postgresql'
const active = new AbortController()
const record = ref<PostgresInspection | null>(null)
const loading = ref(true)
const missing = ref(false)
const loadError = ref('')

const table = computed(() => record.value?.table)
const sensitiveCount = computed(() => table.value?.columns.filter(column => column.sensitivity).length ?? 0)
const publicColumnCount = computed(() => table.value?.publicSelectColumns?.length ?? 0)
const hasPublicSelect = computed(() => table.value?.publicSelect || publicColumnCount.value > 0)

async function load() {
  loading.value = true
  missing.value = false
  loadError.value = ''
  try {
    record.value = await request<PostgresInspection>(apiUrl, { signal: active.signal })
  } catch (error) {
    if (active.signal.aborted) return
    if (error instanceof ApiError && error.status === 401) emit('sessionExpired')
    else if (error instanceof ApiError && error.status === 404) {
      record.value = null
      missing.value = true
    } else loadError.value = describeError(error)
  } finally {
    loading.value = false
  }
}

onMounted(() => { void load() })
onBeforeUnmount(() => active.abort())
</script>

<template>
  <div class="postgres-record">
    <a class="back-link" :href="home + '/assets'"><Icon name="back" />返回数据资产</a>
    <header class="page-header">
      <div>
        <p class="eyebrow">PostgreSQL · 资产 #{{ assetId }}</p>
        <h1>{{ table?.name || '字段与权限' }}</h1>
        <p v-if="table" class="mono postgres-source-ref">{{ table.sourceRef }}</p>
        <p v-else>查看自动采集的字段、疑似敏感标记和授权。</p>
      </div>
      <div class="page-actions">
        <button type="button" class="button button-secondary" :disabled="loading" @click="load">刷新采集结果</button>
      </div>
    </header>
    <p v-if="loading && !record" class="message" role="status">正在读取 PostgreSQL 采集结果…</p>
    <p v-else-if="loadError" class="message message-error page-feedback" role="alert">{{ loadError }}</p>
    <section v-else-if="missing" class="panel empty-state">
      <h2>暂无 PostgreSQL 采集结果</h2>
      <p>托管 PostgreSQL 任务成功采集此表后，字段和授权会在这里显示。</p>
      <a class="button button-secondary" :href="home + '/automation'">查看自动采集</a>
    </section>
    <template v-else-if="record && table">
      <section class="panel postgres-panel" aria-labelledby="postgres-observation-title" :aria-busy="loading">
        <div class="panel-heading">
          <div><h2 id="postgres-observation-title">采集概况</h2><p>字段和权限来自本次 PostgreSQL 系统目录快照。</p></div>
          <span v-if="hasPublicSelect" class="badge status-needs_work">PUBLIC SELECT 授权</span>
        </div>
        <dl class="detail-list">
          <div><dt>表所有者</dt><dd class="mono">{{ table.owner || '未记录' }}</dd></div>
          <div><dt>字段</dt><dd>{{ table.columns.length }} 个 · {{ sensitiveCount }} 个疑似敏感</dd></div>
          <div><dt>采集时间</dt><dd><time :datetime="record.observedAt">{{ formatDate(record.observedAt) }}</time></dd></div>
          <div>
            <dt>运行来源</dt>
            <dd><a class="table-link" :href="home + '/automation'">任务 #{{ record.jobId }}</a>
              <span class="table-secondary mono">{{ record.runId }}</span></dd>
          </div>
          <div class="detail-wide">
            <dt>PUBLIC 权限</dt>
            <dd>{{ postgresPublicSelect(table.publicSelect, publicColumnCount) }} ·
              {{ table.publicSchemaUsage ? 'PUBLIC 具备 schema USAGE' : 'PUBLIC 未获 schema USAGE' }}
            </dd>
          </div>
        </dl>
      </section>
      <section class="panel postgres-panel" aria-labelledby="postgres-columns-title">
        <div class="panel-heading">
          <div><h2 id="postgres-columns-title">字段结构</h2><p>疑似敏感标记来自字段名称和类型，供人工核对。</p></div>
        </div>
        <div v-if="table.columns.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="postgres-columns-title">
          <table class="data-table postgres-table">
            <thead>
              <tr><th scope="col">字段</th><th scope="col">数据类型</th><th scope="col">可为空</th><th scope="col">疑似敏感</th></tr>
            </thead>
            <tbody>
              <tr v-for="column in table.columns" :key="column.ordinal">
                <th scope="row" class="mono">{{ column.name }}</th>
                <td class="mono">{{ column.type }}</td>
                <td>{{ column.nullable ? '是' : '否' }}</td>
                <td><span v-if="column.sensitivity" class="badge status-needs_work">{{ postgresSensitivityLabel(column.sensitivity) }}</span><span v-else>—</span></td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">此表暂无字段。</p>
      </section>
      <section class="panel postgres-panel" aria-labelledby="postgres-grants-title">
        <div class="panel-heading">
          <div><h2 id="postgres-grants-title">直接授权</h2><p>{{ postgresPermissionScope }}</p></div>
        </div>
        <div v-if="table.grants.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="postgres-grants-title">
          <table class="data-table postgres-table postgres-grants">
            <thead>
              <tr>
                <th scope="col">被授权角色</th><th scope="col">对象</th><th scope="col">权限</th>
                <th scope="col">可转授权</th><th scope="col">Schema USAGE</th><th scope="col">授权方</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="grant in table.grants" :key="JSON.stringify(grant)">
                <th scope="row">
                  <span v-if="grant.public" class="badge status-needs_work">PUBLIC</span>
                  <span v-else class="mono">{{ grant.grantee }}</span>
                </th>
                <td v-if="grant.column" class="mono">{{ grant.column }}<span class="table-secondary">字段</span></td>
                <td v-else>整表</td>
                <td class="mono">{{ grant.privilege }}</td>
                <td>{{ grant.grantable ? '是' : '否' }}</td>
                <td>{{ grant.schemaUsage ? '有' : '未检出' }}</td>
                <td class="mono">{{ grant.grantor || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">此快照未检出直接授权。</p>
      </section>
    </template>
  </div>
</template>
