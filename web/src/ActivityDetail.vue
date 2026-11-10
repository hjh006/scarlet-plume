<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { activityCheckHints, activityKindLabels, type Activity } from './activity'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { canManageChecks } from './workflow'
import ChecksView from './ChecksView.vue'
import Icon from './Icon.vue'

const props = defineProps<{ workspace: Workspace; activityId: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const apiUrl = workspacePath(props.workspace.slug) + '/activities/' + encodeURIComponent(props.activityId)
const activity = ref<Activity | null>(null)
const loading = ref(true)
const loadError = ref('')
const active = new AbortController()

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    activity.value = await request<Activity>(apiUrl, { signal: active.signal })
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
  <a class="back-link" :href="home + '/activities'"><Icon name="back" />返回处理活动</a>
  <p v-if="loading" class="message" role="status">正在读取活动详情…</p>
  <div v-else-if="loadError" class="message message-error page-feedback">
    <p role="alert">{{ loadError }}</p>
    <button type="button" class="button button-secondary" @click="load">重新读取</button>
  </div>
  <template v-else-if="activity">
    <header class="page-header">
      <div>
        <p class="eyebrow">处理活动 <span class="mono">#{{ activity.id }}</span></p>
        <h1>{{ activity.name }}</h1>
        <p>{{ activityKindLabels[activity.kind] }} · {{ activity.assetName }}</p>
      </div>
      <div class="page-actions">
        <a v-if="canManageChecks(workspace.role)" class="button button-primary" :href="home + '/checks/new/' + activity.id">
          <Icon name="plus" />发起关联检查
        </a>
        <a class="button button-secondary" :href="home + '/reports/' + activity.id"><Icon name="reports" />查看活动报告</a>
      </div>
    </header>
    <section class="panel activity-record" aria-labelledby="activity-record-title">
      <div class="panel-heading">
        <div><h2 id="activity-record-title">活动信息</h2><p>按活动记录处理事实，在关联检查中核对适用要求和证据。</p></div>
        <span class="badge">{{ activityKindLabels[activity.kind] }}</span>
      </div>
      <dl class="detail-list">
        <div><dt>关联资产</dt><dd>{{ activity.assetName }} · {{ activity.assetSystem }}</dd></div>
        <div><dt>登记时间</dt><dd class="date-text"><time :datetime="activity.createdAt">{{ formatDate(activity.createdAt) }}</time></dd></div>
        <div><dt>{{ activity.kind === 'ai' ? '模型服务或供应商' : '接收方' }}</dt><dd>{{ activity.recipient || '未记录' }}</dd></div>
        <div><dt>{{ activity.kind === 'cross_border' ? '出境目的地' : '处理地域' }}</dt><dd>{{ activity.region || '未记录' }}</dd></div>
        <div class="detail-wide"><dt>处理目的</dt><dd class="text-block">{{ activity.purpose }}</dd></div>
        <div class="detail-wide"><dt>留存和训练条款</dt><dd class="text-block">{{ activity.retentionPolicy || '未记录' }}</dd></div>
        <div v-if="activity.kind === 'cross_border'" class="detail-wide">
          <dt>拟采用的出境路径</dt><dd class="text-block">{{ activity.transferPath || '未记录' }}</dd>
        </div>
        <div v-if="activity.safeguards" class="detail-wide"><dt>保护措施</dt><dd class="text-block">{{ activity.safeguards }}</dd></div>
      </dl>
      <div v-if="activityCheckHints[activity.kind]" class="activity-guidance">
        <strong>候选检查范围</strong>
        <p>{{ activityCheckHints[activity.kind]?.scope }}</p>
      </div>
    </section>
    <ChecksView :workspace="workspace" :activity-id="activity.id" @session-expired="emit('sessionExpired')" />
  </template>
</template>
