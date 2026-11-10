<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, request, workspacePath, type Asset, type AssetPage, type Workspace } from './api'
import { canManageChecks, type Check, type Requirement } from './workflow'
import Icon from './Icon.vue'

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const apiUrl = workspacePath(props.workspace.slug)
const pageUrl = `#/${encodeURIComponent(props.workspace.slug)}/checks`
const assets = ref<Asset[]>([])
const assetCursor = ref<number | null>(null)
const requirements = ref<Requirement[]>([])
const loading = ref(true)
const saving = ref(false)
const loadError = ref('')
const formError = ref('')
const assetId = ref('')
const requirementCode = ref('')
const ownerTeam = ref('')
const note = ref('')
const feishuTargetDigest = ref('')
const active = new AbortController()

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
  const query = append && assetCursor.value !== null ? '?after=' + assetCursor.value : ''
  const options = { signal: active.signal }
  try {
    const [page, rules] = await Promise.all([
      request<AssetPage>(apiUrl + '/assets' + query, options),
      append ? undefined : request<{ items: Requirement[] }>(apiUrl + '/requirements', options),
    ])
    if (active.signal.aborted) return
    assets.value = append ? [...assets.value, ...page.items] : page.items
    assetCursor.value = page.nextCursor
    if (rules) requirements.value = rules.items
  } catch (error) {
    loadError.value = errorText(error)
  } finally {
    loading.value = false
  }
}

async function createCheck() {
  if (saving.value || loading.value || !canManageChecks(props.workspace.role)) return
  saving.value = true
  formError.value = ''
  try {
    const check = await request<Check>(apiUrl + '/checks', {
      method: 'POST',
      body: JSON.stringify({
        assetId: Number(assetId.value), requirementCode: requirementCode.value,
        ownerTeam: ownerTeam.value.trim(), note: note.value.trim(),
        feishuTargetDigest: requirementCode.value === 'G03' ? feishuTargetDigest.value.trim() || undefined : undefined,
      }),
    })
    if (!active.signal.aborted) window.location.hash = pageUrl + '/' + check.id
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
  <a class="back-link" :href="pageUrl"><Icon name="back" />返回监管检查</a>
  <header class="page-header">
    <div><h1>新建监管检查</h1><p>选定监管要求和资产，写明本次检查的范围与通过标准。</p></div>
  </header>
  <p v-if="!canManageChecks(workspace.role)" class="message message-warning" role="alert">当前账号无权创建检查。</p>
  <p v-else-if="loading && !assets.length" class="message" role="status">正在读取监管要求和资产…</p>
  <div v-else-if="loadError && !assets.length" class="message message-error page-feedback">
    <p role="alert">{{ loadError }}</p>
    <button type="button" class="button button-secondary" @click="load()">重新读取</button>
  </div>
  <section v-else-if="!assets.length" class="panel empty-state">
    <h2>请先登记数据资产</h2>
    <p>每项检查需要关联一项本工作区的数据资产。</p>
    <a class="button button-primary" :href="`#/${encodeURIComponent(workspace.slug)}/assets`">前往数据资产</a>
  </section>
  <p v-else-if="!requirements.length" class="message message-warning" role="status">当前暂无可关联的监管要求。</p>
  <form v-else class="panel form-panel" @submit.prevent="createCheck">
    <p v-if="formError" class="message message-error page-feedback" role="alert">{{ formError }}</p>
    <div class="field">
      <label for="check-requirement">监管要求</label>
      <select id="check-requirement" v-model="requirementCode" required>
        <option value="" disabled>选择监管要求</option>
        <option v-for="rule in requirements" :key="rule.code" :value="rule.code">{{ rule.code }} · {{ rule.title }}</option>
      </select>
    </div>
    <div class="field">
      <label for="check-asset">关联资产</label>
      <select id="check-asset" v-model="assetId" required>
        <option value="" disabled>选择本工作区资产</option>
        <option v-for="asset in assets" :key="asset.id" :value="String(asset.id)">{{ asset.name }} · {{ asset.system }}</option>
      </select>
      <button v-if="assetCursor !== null" type="button" class="button button-secondary more-assets" :disabled="loading" @click="load(true)">
        {{ loading ? '正在读取…' : '加载更多资产' }}
      </button>
      <p v-if="loadError" class="field-hint" role="alert">{{ loadError }}</p>
    </div>
    <div class="field"><label for="check-owner">整改责任团队</label><input id="check-owner" v-model="ownerTeam" maxlength="80" required></div>
    <div class="field"><label for="check-note">检查范围与标准</label><textarea id="check-note" v-model="note" maxlength="1000" required></textarea></div>
    <div v-if="requirementCode === 'G03'" class="field">
      <label for="check-feishu-target">飞书核验目标摘要（可选）</label>
      <input id="check-feishu-target" v-model="feishuTargetDigest" maxlength="64" pattern="[a-f0-9]{64}"
             spellcheck="false" autocomplete="off" aria-describedby="check-feishu-hint">
      <p id="check-feishu-hint" class="field-hint">绑定客户侧采集清单中的 targetDigest，使整改前和复测清单对应同一文档与协作者。填写 64 位小写十六进制摘要。</p>
    </div>
    <div class="form-actions">
      <button type="submit" class="button button-primary" :disabled="saving || loading">{{ saving ? '正在创建…' : '创建检查' }}</button>
      <a class="button button-secondary" :href="pageUrl">取消</a>
    </div>
  </form>
</template>
