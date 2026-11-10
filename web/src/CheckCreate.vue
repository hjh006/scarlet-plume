<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { activityCheckHints, activityKindLabels, type Activity, type ActivityPage } from './activity'
import { ApiError, describeError, request, workspacePath, type Asset, type AssetPage, type Workspace } from './api'
import type { CheckTemplate } from './template'
import TemplateCriteria from './TemplateCriteria.vue'
import { canManageChecks, type Check, type Requirement } from './workflow'
import Icon from './Icon.vue'

const props = defineProps<{ workspace: Workspace; activityContext?: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const apiUrl = workspacePath(props.workspace.slug)
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const pageUrl = home + '/checks'
const cancelUrl = props.activityContext ? home + '/activities/' + encodeURIComponent(props.activityContext) : pageUrl
const assets = ref<Asset[]>([])
const assetCursor = ref<number | null>(null)
const requirements = ref<Requirement[]>([])
const linkedActivity = ref<Activity | null>(null)
const activityOptions = ref<Activity[]>([])
const activityCursor = ref<number | null>(null)
const activityLoading = ref(false)
const activityError = ref('')
const activityId = ref('')
let activityOperation: AbortController | undefined
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
const templates = ref<CheckTemplate[]>([])
const templateRevisionId = ref('')
const templateItemKey = ref('')
const templateLoading = ref(false)
const templateError = ref('')
let templateOperation: AbortController | undefined
const selectedTemplate = computed(() => templates.value.find(item => String(item.id) === templateRevisionId.value))
const selectedTemplateItem = computed(() => selectedTemplate.value?.items.find(item => item.key === templateItemKey.value))
const effectiveRequirementCode = computed(() => selectedTemplateItem.value?.requirementCode || requirementCode.value)
const selectedActivity = computed(() => linkedActivity.value ?? activityOptions.value.find(item => String(item.id) === activityId.value))
const activityHint = computed(() => selectedActivity.value ? activityCheckHints[selectedActivity.value.kind] : undefined)

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
    const [page, rules, activity] = await Promise.all([
      props.activityContext ? undefined : request<AssetPage>(apiUrl + '/assets' + query, options),
      append ? undefined : request<{ items: Requirement[] }>(apiUrl + '/requirements', options),
      props.activityContext
        ? request<Activity>(apiUrl + '/activities/' + encodeURIComponent(props.activityContext), options)
        : undefined,
    ])
    if (active.signal.aborted) return
    if (page) {
      assets.value = append ? [...assets.value, ...page.items] : page.items
      assetCursor.value = page.nextCursor
    }
    if (rules) requirements.value = rules.items
    if (activity) linkedActivity.value = activity
  } catch (error) {
    loadError.value = errorText(error)
  } finally {
    loading.value = false
  }
}

async function loadActivities(append = false) {
  activityOperation?.abort()
  activityError.value = ''
  if (!append) {
    activityId.value = ''
    activityOptions.value = []
    activityCursor.value = null
  }
  if (!assetId.value || props.activityContext) {
    activityLoading.value = false
    return
  }
  const operation = new AbortController()
  activityOperation = operation
  activityLoading.value = true
  const params = new URLSearchParams({ assetId: assetId.value })
  if (append && activityCursor.value !== null) params.set('before', String(activityCursor.value))
  try {
    const result = await request<ActivityPage>(apiUrl + '/activities?' + params, { signal: operation.signal })
    if (operation.signal.aborted) return
    activityOptions.value = append ? [...activityOptions.value, ...result.items] : result.items
    activityCursor.value = result.nextCursor ?? null
  } catch (error) {
    if (!operation.signal.aborted) activityError.value = errorText(error)
  } finally {
    if (!operation.signal.aborted) activityLoading.value = false
  }
}

async function loadTemplates() {
  templateOperation?.abort()
  templateRevisionId.value = ''
  templateItemKey.value = ''
  templates.value = []
  templateError.value = ''
  const kind = selectedActivity.value?.kind
  if (!kind || kind === 'internal') {
    templateLoading.value = false
    return
  }
  const operation = new AbortController()
  templateOperation = operation
  templateLoading.value = true
  try {
    const result = await request<{ items: CheckTemplate[] }>(
      apiUrl + '/templates?kind=' + encodeURIComponent(kind), { signal: operation.signal },
    )
    if (!operation.signal.aborted) templates.value = result.items
  } catch (error) {
    if (!operation.signal.aborted) templateError.value = errorText(error)
  } finally {
    if (!operation.signal.aborted) templateLoading.value = false
  }
}

async function createCheck() {
  if (saving.value || loading.value || !canManageChecks(props.workspace.role) ||
      (props.activityContext && !linkedActivity.value) ||
      (templateRevisionId.value && !selectedTemplateItem.value)) return
  saving.value = true
  formError.value = ''
  try {
    const check = await request<Check>(apiUrl + '/checks', {
      method: 'POST',
      body: JSON.stringify({
        assetId: linkedActivity.value?.assetId ?? Number(assetId.value),
        activityId: selectedActivity.value?.id, requirementCode: effectiveRequirementCode.value,
        templateRevisionId: selectedTemplateItem.value ? selectedTemplate.value?.id : undefined,
        templateItemKey: selectedTemplateItem.value?.key,
        ownerTeam: ownerTeam.value.trim(), note: note.value.trim(),
        feishuTargetDigest: effectiveRequirementCode.value === 'G03' ? feishuTargetDigest.value.trim() || undefined : undefined,
      }),
    })
    if (!active.signal.aborted) window.location.hash = pageUrl + '/' + check.id
  } catch (error) {
    formError.value = errorText(error)
  } finally {
    saving.value = false
  }
}

watch(assetId, () => { void loadActivities() })
watch(selectedActivity, () => { void loadTemplates() })
watch(templateRevisionId, () => { templateItemKey.value = '' })
watch(activityHint, (hint, previous) => {
  if (hint && (!requirementCode.value || requirementCode.value === previous?.code) &&
      requirements.value.some(rule => rule.code === hint.code)) {
    requirementCode.value = hint.code
  }
})

onMounted(() => { void load() })
onBeforeUnmount(() => {
  active.abort()
  activityOperation?.abort()
  templateOperation?.abort()
})
</script>

<template>
  <a class="back-link" :href="cancelUrl"><Icon name="back" />{{ activityContext ? '返回处理活动' : '返回监管检查' }}</a>
  <header class="page-header">
    <div><h1>新建监管检查</h1><p>选定监管要求和资产，如涉及特定处理活动，可将审批、证据与整改记录关联到该活动。</p></div>
  </header>
  <p v-if="!canManageChecks(workspace.role)" class="message message-warning" role="alert">当前账号无权创建检查。</p>
  <p v-else-if="loading && !linkedActivity && !assets.length" class="message" role="status">正在读取监管要求与关联记录…</p>
  <div v-else-if="loadError && !linkedActivity && !assets.length" class="message message-error page-feedback">
    <p role="alert">{{ loadError }}</p>
    <button type="button" class="button button-secondary" @click="load()">重新读取</button>
  </div>
  <section v-else-if="!linkedActivity && !assets.length" class="panel empty-state">
    <h2>请先登记数据资产</h2>
    <p>每项检查需要关联一项本工作区的数据资产。</p>
    <a class="button button-primary" :href="`#/${encodeURIComponent(workspace.slug)}/assets`">前往数据资产</a>
  </section>
  <p v-else-if="!requirements.length" class="message message-warning" role="status">当前暂无可关联的监管要求。</p>
  <form v-else class="panel form-panel" @submit.prevent="createCheck">
    <p v-if="formError" class="message message-error page-feedback" role="alert">{{ formError }}</p>
    <div v-if="linkedActivity" class="field">
      <strong>{{ linkedActivity.name }}</strong>
      <p class="field-hint">{{ activityKindLabels[linkedActivity.kind] }} · 关联资产 {{ linkedActivity.assetName }} · {{ linkedActivity.assetSystem }}</p>
    </div>
    <div v-if="!linkedActivity" class="field">
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
    <div v-if="!linkedActivity && assetId" class="field">
      <label for="check-activity">关联处理活动（可选）</label>
      <select id="check-activity" v-model="activityId" :disabled="activityLoading || saving">
        <option value="">不关联处理活动</option>
        <option v-for="activity in activityOptions" :key="activity.id" :value="String(activity.id)">
          {{ activity.name }} · {{ activityKindLabels[activity.kind] }}
        </option>
      </select>
      <p v-if="activityLoading" class="field-hint" role="status">正在读取此资产的处理活动…</p>
      <div v-if="activityError" class="field-hint" role="alert">
        <p>{{ activityError }}</p>
        <button type="button" class="button button-secondary more-assets" @click="loadActivities()">重新读取活动</button>
      </div>
      <p v-else-if="!activityLoading && !activityOptions.length" class="field-hint">
        此资产暂无处理活动。可到<a :href="home + '/activities'">处理活动</a>登记。
      </p>
      <button v-if="activityCursor !== null" type="button" class="button button-secondary more-assets"
              :disabled="activityLoading" @click="loadActivities(true)">加载更多活动</button>
    </div>
    <div v-if="selectedActivity && selectedActivity.kind !== 'internal'" class="field">
      <label for="check-template">检查模板（可选）</label>
      <select id="check-template" v-model="templateRevisionId" :disabled="templateLoading || saving" aria-describedby="check-template-hint">
        <option value="">手动选择监管要求</option>
        <option v-for="entry in templates" :key="entry.id" :value="String(entry.id)">
          {{ entry.title }} · 版本 {{ entry.version }}{{ entry.builtIn ? ' · 内置' : ' · 工作区' }}
        </option>
      </select>
      <p id="check-template-hint" class="field-hint">核对适用条件后选用一项。检查记录将保留该项和模板版本。</p>
      <p v-if="templateLoading" class="field-hint" role="status">正在读取匹配的检查模板…</p>
      <div v-else-if="templateError" class="field-hint" role="alert">
        <p>{{ templateError }}</p>
        <button type="button" class="button button-secondary more-assets" @click="loadTemplates">重新读取模板</button>
      </div>
    </div>
    <div v-if="selectedTemplate" class="field">
      <label for="check-template-item">模板检查项</label>
      <select id="check-template-item" v-model="templateItemKey" required>
        <option value="" disabled>选择检查项</option>
        <option v-for="item in selectedTemplate.items" :key="item.key" :value="item.key">
          {{ item.requirementCode }} · {{ item.title }}
        </option>
      </select>
      <TemplateCriteria v-if="selectedTemplateItem" :item="selectedTemplateItem" />
    </div>
    <div v-else class="field">
      <label for="check-requirement">监管要求</label>
      <select id="check-requirement" v-model="requirementCode" :aria-describedby="activityHint ? 'check-rule-hint' : undefined" required>
        <option value="" disabled>选择监管要求</option>
        <option v-for="rule in requirements" :key="rule.code" :value="rule.code">{{ rule.code }} · {{ rule.title }}</option>
      </select>
      <p v-if="activityHint" id="check-rule-hint" class="field-hint">候选规则 {{ activityHint.code }}。请结合实际场景选择适用要求。</p>
    </div>
    <div class="field"><label for="check-owner">整改责任团队</label><input id="check-owner" v-model="ownerTeam" maxlength="80" required></div>
    <div class="field">
      <label for="check-note">检查范围与标准</label>
      <textarea id="check-note" v-model="note" maxlength="1000"
                :aria-describedby="activityHint ? 'check-scope-hint' : undefined" required></textarea>
      <p v-if="activityHint" id="check-scope-hint" class="field-hint">{{ activityHint.scope }}</p>
    </div>
    <div v-if="effectiveRequirementCode === 'G03'" class="field">
      <label for="check-feishu-target">飞书核验目标摘要（可选）</label>
      <input id="check-feishu-target" v-model="feishuTargetDigest" maxlength="64" pattern="[a-f0-9]{64}"
             spellcheck="false" autocomplete="off" aria-describedby="check-feishu-hint">
      <p id="check-feishu-hint" class="field-hint">绑定客户侧采集清单中的 targetDigest，使整改前和复测清单对应同一文档与协作者。填写 64 位小写十六进制摘要。</p>
    </div>
    <div class="form-actions">
      <button type="submit" class="button button-primary" :disabled="saving || loading">{{ saving ? '正在创建…' : '创建检查' }}</button>
      <a class="button button-secondary" :href="cancelUrl">取消</a>
    </div>
  </form>
</template>
