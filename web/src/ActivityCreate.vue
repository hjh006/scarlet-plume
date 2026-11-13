<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { activityKindLabels, type Activity, type ActivityKind } from './activity'
import { ApiError, describeError, request, workspacePath, type Asset, type AssetPage, type Workspace } from './api'
import { canManageChecks } from './workflow'
import Icon from './Icon.vue'
import ScenarioDataPanel from './ScenarioDataPanel.vue'
import { useScenarioContext } from './scenario-context'

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const apiUrl = workspacePath(props.workspace.slug)
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const { context, href, setScope } = useScenarioContext(props.workspace.slug)
const pageUrl = computed(() => href('activities'))
const materialsScene = computed(() => ['personal', 'domestic', 'crossborder'].includes(context.value.scene))
const assets = ref<Asset[]>([])
const cursor = ref<number | null>(null)
const loading = ref(true)
const saving = ref(false)
const loadError = ref('')
const formError = ref('')
const active = new AbortController()
const draft = reactive({
  assetId: context.value.assetId,
  name: '',
  kind: (context.value.scene === 'crossborder' ? 'cross_border' : 'internal') as ActivityKind,
  purpose: '',
  recipient: '',
  region: '',
  retentionPolicy: '',
  transferPath: '',
  safeguards: '',
})
const selectedAsset = computed(() => assets.value.find(asset => String(asset.id) === draft.assetId))
const isCrossBorder = computed(() => draft.kind === 'cross_border')
const isAI = computed(() => draft.kind === 'ai')
const isExternal = computed(() => draft.kind !== 'internal')

function focusField(id: string) { const field=document.getElementById(id); field?.scrollIntoView({block:'center',behavior:'smooth'}); field?.focus({preventScroll:true}) }

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
    if (draft.assetId && !assets.value.some(asset => String(asset.id) === draft.assetId)) {
      const selected = await request<{ asset: Asset }>(apiUrl + '/classification/assets/' + encodeURIComponent(draft.assetId), { signal: active.signal })
      assets.value.push(selected.asset)
    }
  } catch (error) {
    loadError.value = errorText(error)
  } finally {
    loading.value = false
  }
}

async function createActivity() {
  if (saving.value || loading.value || !canManageChecks(props.workspace.role)) return
  if (!selectedAsset.value) { formError.value = '请核对本工作区已读取的关联资产。'; return }
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
    if (!active.signal.aborted) window.location.hash = href('activities/' + activity.id, { assetId: String(activity.assetId), activityId: String(activity.id) })
  } catch (error) {
    formError.value = errorText(error)
  } finally {
    saving.value = false
  }
}

watch(() => context.value.assetId, value => { if (value !== draft.assetId) { draft.assetId = value; void load() } })
function selectAsset() { setScope({ assetId: draft.assetId, activityId: '' }) }
onMounted(() => { void load() })
onBeforeUnmount(() => active.abort())
</script>

<template>
  <a class="back-link" :href="pageUrl"><Icon name="back" />返回处理活动</a>
  <header class="page-header">
    <div><p class="eyebrow">数据治理 / 处理活动</p><h1>新建处理活动</h1><p>说明关联资产、处理目的和数据流向。审批、取证与整改在关联检查中记录。</p></div>
  </header>
  <ScenarioDataPanel v-if="materialsScene && draft.assetId" :workspace="workspace" :asset-id="draft.assetId" activity-id="" compact @session-expired="emit('sessionExpired')" />
  <p v-if="!canManageChecks(workspace.role)" class="message message-warning" role="alert">当前账号无权登记活动。</p>
  <p v-else-if="loading && !assets.length" class="message" role="status">正在读取数据资产…</p>
  <div v-else-if="loadError && !assets.length" class="message message-error page-feedback">
    <p role="alert">{{ loadError }}</p>
    <button type="button" class="button button-secondary" @click="load()">重新读取</button>
  </div>
  <section v-else-if="!assets.length" class="panel empty-state">
    <h2>请先登记数据资产</h2>
    <p>每项处理活动关联本工作区的一项资产。</p>
    <a class="button button-primary" :href="href('assets')">前往数据资产</a>
  </section>
  <div v-else class="form-workbench activity-create-layout"><aside class="panel activity-form-outline"><div class="panel-heading"><h2>配置步骤</h2></div><ol class="workflow-spine"><li><button type="button" @click="focusField('activity-name')">关联范围</button></li><li><button type="button" @click="focusField('activity-purpose')">处理目的</button></li><li><button type="button" @click="focusField('activity-recipient')">接收与区域</button></li><li><button type="button" @click="focusField('activity-retention')">保留与保障</button></li></ol></aside><form class="panel form-main activity-create-form" @submit.prevent="createActivity"><div class="panel-heading"><h2>新建处理活动</h2></div><div class="activity-create-fields">
    <p v-if="formError" class="message message-error page-feedback" role="alert">{{ formError }}</p>
    <div class="field">
      <label for="activity-name">活动名称</label>
      <input id="activity-name" v-model="draft.name" maxlength="100" required>
    </div>
    <div class="field">
      <label for="activity-asset">关联资产</label>
      <select id="activity-asset" v-model="draft.assetId" required @change="selectAsset">
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
      <label for="activity-retention">{{ isAI ? '留存和训练条款（可选）' : '保存期限与安排（可选）' }}</label>
      <textarea id="activity-retention" v-model="draft.retentionPolicy" maxlength="400"></textarea>
    </div>
    <div v-if="isExternal" class="field">
      <label for="activity-safeguards">保护措施（可选）</label>
      <textarea id="activity-safeguards" v-model="draft.safeguards" maxlength="400"
                aria-describedby="activity-safeguards-hint"></textarea>
      <p id="activity-safeguards-hint" class="field-hint">记录范围限制、脱敏、权限或到期处理。源系统配置与批准记录可在检查中按引用登记。</p>
    </div>
    </div><div class="form-actions">
      <button type="submit" class="button button-primary" :disabled="saving || loading">{{ saving ? '正在保存…' : '保存活动' }}</button>
      <a class="button button-secondary" :href="pageUrl">取消</a>
    </div>
  </form><aside class="form-aside context-rail section-stack"><section class="panel"><div class="panel-heading"><h2>关联资产</h2></div><dl class="activity-create-context"><div><dt>当前资产</dt><dd>{{selectedAsset?.name||'尚未选择'}}</dd></div><div><dt>所属系统</dt><dd>{{selectedAsset?.system||'未记录'}}</dd></div><div><dt>责任团队</dt><dd>{{selectedAsset?.ownerTeam||'待确认'}}</dd></div><div><dt>来源</dt><dd>{{selectedAsset?.source||'手工登记 / 未记录'}}</dd></div></dl></section><section class="panel"><div class="panel-heading"><h2>核验提示</h2></div><p class="activity-create-note">采集可以提供资产与识别事实；处理依据、实际用途、接收方保障和留存条款仍需明确取证。</p></section><section class="panel"><div class="panel-heading"><h2>修订与范围</h2></div><p class="activity-create-note">首次保存后记录活动修订。后续变化将单独留存，已采用的评估和历史报告保持各自时点。</p></section></aside></div>
</template>
<style scoped>
.activity-create-layout>*{min-width:0}.activity-create-layout{display:grid;grid-template-columns:190px minmax(0,1fr) 290px;gap:16px;align-items:start}.activity-form-outline ol{margin:0;padding:20px 20px 22px 36px;display:grid;gap:22px;font-size:12px}.activity-form-outline button{min-height:32px;border:0;padding:4px 0;background:none;color:var(--secondary);font:inherit;text-align:left;cursor:pointer}.activity-create-fields{scroll-margin-top:90px;display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:20px;padding:20px}.activity-create-fields>.field{margin:0;min-width:0}.activity-create-fields>.field:has(textarea){grid-column:1/-1}.activity-create-fields textarea{min-height:100px}.activity-create-form>.form-actions{position:sticky;bottom:0;display:flex;justify-content:flex-end;margin:0;padding:16px 20px;border-top:1px solid var(--line);background:var(--surface-subtle);z-index:1}.activity-create-context{margin:0;padding:12px 18px;display:grid;font-size:12px}.activity-create-context>div{display:grid;grid-template-columns:75px minmax(0,1fr);gap:10px;padding:12px 0;border-bottom:1px solid var(--line)}.activity-create-context>div:last-child{border:0}.activity-create-context dt{color:var(--secondary)}.activity-create-context dd{margin:0;overflow-wrap:anywhere}.activity-create-note{padding:18px;font-size:12px;color:var(--secondary);line-height:1.8}.activity-create-form .message{margin:16px 20px 0}.activity-create-fields .more-assets{margin-top:10px}
@media(max-width:1400px){.activity-create-layout{grid-template-columns:160px minmax(0,1fr) 250px}}
.activity-form-outline .workflow-spine>li+li::before{left:-15px;top:-17px;width:1px;height:20px}
</style>
