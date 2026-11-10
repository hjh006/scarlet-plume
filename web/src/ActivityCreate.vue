<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { activityKindLabels, type Activity, type ActivityKind } from './activity'
import { ApiError, describeError, request, workspacePath, type Asset, type AssetPage, type Workspace } from './api'
import { canManageChecks } from './workflow'
import Icon from './Icon.vue'

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const apiUrl = workspacePath(props.workspace.slug)
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const pageUrl = home + '/activities'
const assets = ref<Asset[]>([])
const cursor = ref<number | null>(null)
const loading = ref(true)
const saving = ref(false)
const loadError = ref('')
const formError = ref('')
const active = new AbortController()
const draft = reactive({
  assetId: '',
  name: '',
  kind: 'internal' as ActivityKind,
  purpose: '',
  recipient: '',
  region: '',
  retentionPolicy: '',
  transferPath: '',
  safeguards: '',
})
const isCrossBorder = computed(() => draft.kind === 'cross_border')
const isAI = computed(() => draft.kind === 'ai')
const isExternal = computed(() => draft.kind !== 'internal')

function errorText(error: unknown) {
  if (active.signal.aborted) return ''
  if (error instanceof ApiError && error.status === 401) {
    emit('sessionExpired')
    return ''
  }
  return describeError(error)
}

async function load(append = false) {
  if (!canManageChecks(props.workspace.role)) {
    loading.value = false
    return
  }
  loading.value = true
  loadError.value = ''
  const query = append && cursor.value !== null ? '?after=' + cursor.value : ''
  try {
    const page = await request<AssetPage>(apiUrl + '/assets' + query, { signal: active.signal })
    if (active.signal.aborted) return
    assets.value = append ? [...assets.value, ...page.items] : page.items
    cursor.value = page.nextCursor
  } catch (error) {
    loadError.value = errorText(error)
  } finally {
    loading.value = false
  }
}

async function createActivity() {
  if (saving.value || loading.value || !canManageChecks(props.workspace.role)) return
  saving.value = true
  formError.value = ''
  try {
    const activity = await request<Activity>(apiUrl + '/activities', {
      method: 'POST',
      body: JSON.stringify({
        assetId: Number(draft.assetId), kind: draft.kind, name: draft.name.trim(), purpose: draft.purpose.trim(),
        recipient: draft.recipient.trim(), region: draft.region.trim(), retentionPolicy: draft.retentionPolicy.trim(),
        transferPath: isCrossBorder.value ? draft.transferPath.trim() : '',
        safeguards: isExternal.value ? draft.safeguards.trim() : '',
      }),
    })
    if (!active.signal.aborted) window.location.hash = pageUrl + '/' + activity.id
  } catch (error) {
    formError.value = errorText(error)
  } finally {
    saving.value = false
  }
}

onMounted(() => { void load() })
onBeforeUnmount(() => active.abort())
</script>

<template>
  <a class="back-link" :href="pageUrl"><Icon name="back" />返回处理活动</a>
  <header class="page-header">
    <div><h1>登记处理活动</h1><p>说明关联资产、处理目的和数据流向。审批、取证与整改在关联检查中记录。</p></div>
  </header>
  <p v-if="!canManageChecks(workspace.role)" class="message message-warning" role="alert">当前账号无权登记活动。</p>
  <p v-else-if="loading && !assets.length" class="message" role="status">正在读取数据资产…</p>
  <div v-else-if="loadError && !assets.length" class="message message-error page-feedback">
    <p role="alert">{{ loadError }}</p>
    <button type="button" class="button button-secondary" @click="load()">重新读取</button>
  </div>
  <section v-else-if="!assets.length" class="panel empty-state">
    <h2>请先登记数据资产</h2>
    <p>每项处理活动关联本工作区的一项资产。</p>
    <a class="button button-primary" :href="home + '/assets'">前往数据资产</a>
  </section>
  <form v-else class="panel form-panel" @submit.prevent="createActivity">
    <p v-if="formError" class="message message-error page-feedback" role="alert">{{ formError }}</p>
    <div class="field">
      <label for="activity-name">活动名称</label>
      <input id="activity-name" v-model="draft.name" maxlength="100" required>
    </div>
    <div class="field">
      <label for="activity-asset">关联资产</label>
      <select id="activity-asset" v-model="draft.assetId" required>
        <option value="" disabled>选择本工作区资产</option>
        <option v-for="asset in assets" :key="asset.id" :value="String(asset.id)">{{ asset.name }} · {{ asset.system }}</option>
      </select>
      <button v-if="cursor !== null" type="button" class="button button-secondary more-assets" :disabled="loading" @click="load(true)">
        {{ loading ? '正在读取…' : '加载更多资产' }}
      </button>
      <p v-if="loadError" class="field-hint" role="alert">{{ loadError }}</p>
    </div>
    <div class="field">
      <label for="activity-kind">活动类型</label>
      <select id="activity-kind" v-model="draft.kind" required>
        <option v-for="(label, value) in activityKindLabels" :key="value" :value="value">{{ label }}</option>
      </select>
    </div>
    <div class="field">
      <label for="activity-purpose">处理目的</label>
      <textarea id="activity-purpose" v-model="draft.purpose" maxlength="400" required></textarea>
    </div>
    <div class="field">
      <label for="activity-recipient">{{ isAI ? '模型服务或供应商' : '接收方' }}{{ isCrossBorder ? '' : '（可选）' }}</label>
      <input id="activity-recipient" v-model="draft.recipient" maxlength="100" :required="isCrossBorder">
    </div>
    <div class="field">
      <label for="activity-region">{{ isCrossBorder ? '出境目的地' : '处理地域（可选）' }}</label>
      <input id="activity-region" v-model="draft.region" maxlength="100" :required="isCrossBorder"
             :aria-describedby="isCrossBorder ? 'activity-region-hint' : undefined">
      <p v-if="isCrossBorder" id="activity-region-hint" class="field-hint">记录接收方所在地和实际处理地域，便于后续核对数据流向。</p>
    </div>
    <div v-if="isCrossBorder" class="field">
      <label for="activity-transfer">拟采用的出境路径（可选）</label>
      <input id="activity-transfer" v-model="draft.transferPath" maxlength="400" aria-describedby="activity-transfer-hint">
      <p id="activity-transfer-hint" class="field-hint">记录已识别的路径或待核实事项，并在检查中登记判断依据。</p>
    </div>
    <div class="field">
      <label for="activity-retention">{{ isAI ? '留存和训练条款（可选）' : '留存条款（可选）' }}</label>
      <textarea id="activity-retention" v-model="draft.retentionPolicy" maxlength="400"></textarea>
    </div>
    <div v-if="isExternal" class="field">
      <label for="activity-safeguards">保护措施（可选）</label>
      <textarea id="activity-safeguards" v-model="draft.safeguards" maxlength="400"
                aria-describedby="activity-safeguards-hint"></textarea>
      <p id="activity-safeguards-hint" class="field-hint">记录范围限制、脱敏、权限或到期处理。源系统配置与批准记录可在检查中按引用登记。</p>
    </div>
    <div class="form-actions">
      <button type="submit" class="button button-primary" :disabled="saving || loading">{{ saving ? '正在保存…' : '保存活动' }}</button>
      <a class="button button-secondary" :href="pageUrl">取消</a>
    </div>
  </form>
</template>
