<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ApiError, assetBusinessLevelLabels, assetCategoryLabels, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import type { WorkbenchItem } from './classification-workbench'

interface AIProfile {
  id: number; name: string; baseUrl: string; protocol: 'chat_completions' | 'responses'; model: string
  enabled: boolean; allowMetadata: boolean; revision: number; hasApiKey: boolean
}
interface EngineState {
  local: { version: string; ruleKinds: string[]; formats: string[] }
  settings: { mode: 'local' | 'hybrid'; defaultProfileId: number | null; revision: number }
  profiles: AIProfile[]
  encryptionReady: boolean
}
interface AISuggestion {
  id: number; assetId: number; assetRevision: number; profileId: number; profileRevision: number; model: string
  category: string; businessLevel: string; rationale: string; createdAt: string
}
const props = defineProps<{ workspace: Workspace; asset?: WorkbenchItem }>()
const emit = defineEmits<{ sessionExpired: []; selectAsset: [] }>()
const base = workspacePath(props.workspace.slug) + '/classification'
const active = new AbortController()
const owner = computed(() => props.workspace.role === 'owner')
const editor = computed(() => ['owner', 'operator'].includes(props.workspace.role))
const state = ref<EngineState>()
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const notice = ref('')
const settings = reactive({ mode: 'local' as 'local' | 'hybrid', defaultProfileId: '' })
const profileId = ref('')
const usableProfiles = computed(() => state.value?.profiles.filter(profile => profile.enabled && profile.allowMetadata) ?? [])
const chosenProfile = computed(() => state.value?.profiles.find(profile => String(profile.id) === profileId.value))
const ruleKindLabels: Record<string, string> = { keyword: '关键词', regexp: 'RE2 正则', dictionary: '精确词典' }
const formatLabels: Record<string, string> = { email: '邮箱', phone: '手机号', national_id: '身份证校验位', financial_account: '银行卡 Luhn', bank_luhn: '银行卡 Luhn', id_checksum: '身份证校验位' }
function failed(cause: unknown) {
  if (active.signal.aborted) return
  if (cause instanceof ApiError && cause.status === 401) { emit('sessionExpired'); return }
  error.value = describeError(cause)
}
async function load() {
  if (loading.value) return
  loading.value = true; error.value = ''
  try {
    state.value = await request<EngineState>(base + '/engine', { signal: active.signal })
    settings.mode = state.value.settings.mode
    settings.defaultProfileId = state.value.settings.defaultProfileId ? String(state.value.settings.defaultProfileId) : ''
    if (!usableProfiles.value.some(profile => String(profile.id) === profileId.value)) profileId.value = usableProfiles.value.find(profile => profile.id === state.value?.settings.defaultProfileId)?.id.toString() ?? usableProfiles.value[0]?.id.toString() ?? ''
  } catch (cause) { failed(cause) }
  finally { loading.value = false }
}
async function saveSettings() {
  if (!state.value || !owner.value || busy.value || generating.value) return
  busy.value = true; error.value = ''; notice.value = ''
  try {
    await request(base + '/engine', { method: 'PUT', signal: active.signal, body: JSON.stringify({ mode: settings.mode, defaultProfileId: settings.mode === 'hybrid' && settings.defaultProfileId ? Number(settings.defaultProfileId) : null, revision: state.value.settings.revision }) })
    await load(); notice.value = '识别引擎设置已保存。AI 建议仅在明确点击生成后调用。'
  } catch (cause) { failed(cause) }
  finally { busy.value = false }
}

const profileForm = ref(false)
const editingProfile = ref<AIProfile>()
const profileDraft = reactive({ name: '', baseUrl: '', protocol: 'chat_completions' as 'chat_completions' | 'responses', model: '', enabled: true, allowMetadata: false })
const apiKey = ref('')
const clearApiKey = ref(false)
const models = ref<string[]>([])
const testedProfileId = ref<number>()
const editorModels = computed(() => editingProfile.value?.id === testedProfileId.value ? models.value : [])
const profileConflict = ref(false)
const changedKeyDestination = computed(() => !!editingProfile.value?.hasApiKey && profileDraft.baseUrl.trim() !== editingProfile.value.baseUrl && !apiKey.value.trim() && !clearApiKey.value)
function editProfile(profile?: AIProfile) {
  if (!owner.value || busy.value || generating.value) return
  editingProfile.value = profile
  Object.assign(profileDraft, profile ? { name: profile.name, baseUrl: profile.baseUrl, protocol: profile.protocol, model: profile.model, enabled: profile.enabled, allowMetadata: profile.allowMetadata } : { name: '', baseUrl: '', protocol: 'chat_completions', model: '', enabled: true, allowMetadata: false })
  apiKey.value = ''; clearApiKey.value = false; profileConflict.value = false
  profileForm.value = true; error.value = ''; notice.value = ''
}
function closeProfile() { apiKey.value = ''; clearApiKey.value = false; profileForm.value = false }
watch(clearApiKey, value => { if (value) apiKey.value = '' })
watch(owner, value => { if (!value) closeProfile() })
async function saveProfile() {
  if (!owner.value || busy.value || generating.value || changedKeyDestination.value || profileConflict.value) return
  busy.value = true; error.value = ''; notice.value = ''
  try {
    const profile = editingProfile.value
    await request(base + '/ai/profiles' + (profile ? '/' + profile.id : ''), { method: profile ? 'PUT' : 'POST', signal: active.signal, body: JSON.stringify({ ...profileDraft, name: profileDraft.name.trim(), baseUrl: profileDraft.baseUrl.trim(), model: profileDraft.model.trim(), ...(profile ? { revision: profile.revision } : {}), ...(apiKey.value ? { apiKey: apiKey.value } : {}), ...(clearApiKey.value ? { clearApiKey: true } : {}) }) })
    closeProfile(); await load(); notice.value = '模型配置已保存。密钥只写入服务端受保护配置，不返回页面。'
  } catch (cause) { profileConflict.value = cause instanceof ApiError && cause.status === 409; failed(cause) }
  finally { apiKey.value = ''; busy.value = false }
}
async function reloadProfile() {
  const id = editingProfile.value?.id
  await load()
  const profile = state.value?.profiles.find(item => item.id === id)
  if (profile) editProfile(profile)
}
async function testProfile(profile: AIProfile) {
  if (!owner.value || busy.value || generating.value) return
  busy.value = true; error.value = ''; notice.value = ''; models.value = []; testedProfileId.value = profile.id
  try {
    const result = await request<{ models: string[] }>(base + '/ai/profiles/' + profile.id + '/test', { method: 'POST', signal: active.signal })
    models.value = result.models ?? []
    notice.value = models.value.length ? `配置连接已测试，取得 ${models.value.length} 个模型标识。模型仍可手动填写。` : '连接测试已完成，未返回模型列表；可继续手动填写模型标识。'
  } catch (cause) { failed(cause) }
  finally { busy.value = false }
}

const suggestions = ref<AISuggestion[]>([])
const suggestionsLoading = ref(false)
const generating = ref(false)
let suggestionController: AbortController | undefined
const inferenceReady = computed(() => !!inputMetadata.value && !inputLoading.value && editor.value && state.value?.settings.mode === 'hybrid' && !!props.asset && !!chosenProfile.value?.enabled && !!chosenProfile.value.allowMetadata)
const inputMetadata = ref<Record<string, unknown>>()
const inputLoading = ref(false)
const inputError = ref('')
let inputController: AbortController | undefined
let inferenceController: AbortController | undefined
const payloadPreview = computed(() => inputMetadata.value ? JSON.stringify(inputMetadata.value, null, 2) : '')
async function loadInput() {
  inputController?.abort(); inferenceController?.abort(); generating.value = false
  inputMetadata.value = undefined; inputError.value = ''; inputLoading.value = false
  if (!editor.value || !props.asset) return
  const operation = new AbortController()
  inputController = operation; inputLoading.value = true
  try {
    const result = await request<{ assetId: number; revision: number; metadata: Record<string, unknown> }>(base + '/ai/input?' + new URLSearchParams({ assetId: String(props.asset.asset.id), revision: String(props.asset.revision) }), { signal: operation.signal })
    if (!operation.signal.aborted) inputMetadata.value = result.metadata
  } catch (cause) { if (!operation.signal.aborted) { inputError.value = describeError(cause); if (cause instanceof ApiError && cause.status === 401) emit('sessionExpired') } }
  finally { if (!operation.signal.aborted) inputLoading.value = false }
}
async function loadSuggestions() {
  suggestionController?.abort()
  suggestions.value = []; suggestionsLoading.value = false
  if (!props.asset) return
  const operation = new AbortController()
  suggestionController = operation; suggestionsLoading.value = true
  try {
    const result = await request<{ items: AISuggestion[] }>(base + '/ai/suggestions?' + new URLSearchParams({ assetId: String(props.asset.asset.id) }), { signal: operation.signal })
    if (!operation.signal.aborted) suggestions.value = result.items
  } catch (cause) { if (!operation.signal.aborted) failed(cause) }
  finally { if (!operation.signal.aborted) suggestionsLoading.value = false }
}
async function generateSuggestion() {
  if (!inferenceReady.value || !props.asset || generating.value) return
  const operation = new AbortController()
  inferenceController = operation
  generating.value = true; error.value = ''; notice.value = ''
  try {
    await request(base + '/ai/suggestions', { method: 'POST', signal: operation.signal, body: JSON.stringify({ profileId: Number(profileId.value), assetId: Number(props.asset.asset.id), revision: props.asset.revision }) })
    if (!operation.signal.aborted) { await loadSuggestions(); notice.value = 'AI 参考建议已记录。请核对依据，再在资产分类确认中作出人工决定。' }
  } catch (cause) { if (!operation.signal.aborted) failed(cause) }
  finally { if (!operation.signal.aborted) generating.value = false }
}
function profileName(id: number) { return state.value?.profiles.find(profile => profile.id === id)?.name ?? '配置 #' + id }
watch(() => [props.asset?.asset.id, props.asset?.revision], () => { void loadSuggestions() })
watch(() => [props.asset?.asset.id, props.asset?.revision, profileId.value, chosenProfile.value?.revision], () => { void loadInput() })
onMounted(() => { void load(); void loadSuggestions(); void loadInput() })
onBeforeUnmount(() => { apiKey.value = ''; active.abort(); suggestionController?.abort(); inputController?.abort(); inferenceController?.abort() })
</script>

<template>
  <section class="engine-settings" :aria-busy="loading || busy || generating">
    <p v-if="error" class="message message-error engine-feedback" role="alert">{{ error }}<button v-if="!busy && !loading" type="button" class="table-link" @click="load">重新读取设置</button></p>
    <p v-if="notice" class="message message-success" role="status">{{ notice }}</p>
    <p v-if="loading && !state" class="empty-state" role="status">正在读取识别引擎…</p>
    <template v-if="state">
      <section class="panel"><div class="panel-heading"><div><h2>自有规则识别引擎</h2><p>本地执行可追溯的格式与规则识别，人工分类确认保留优先权。</p></div><span class="badge status-verified">{{ state.local.version }}</span></div><div class="engine-local"><dl><div><dt>版本化规则</dt><dd><span v-for="kind in state.local.ruleKinds" :key="kind">{{ ruleKindLabels[kind] || kind }}</span></dd></div><div><dt>内置格式</dt><dd><span v-for="format in state.local.formats" :key="format">{{ formatLabels[format] || format }}</span></dd></div><div><dt>判定依据</dt><dd>按已采用行业标准保留事实、条款与缺失条件；结合合成正反例校准、人工确认及独立发布复核。</dd></div></dl><p class="field-hint">当前是规则和格式识别能力。AI 为可选外部参考建议，法定类别与最终分类由实际依据和人工确认决定。</p></div></section>

      <div class="engine-layout"><section class="panel"><div class="panel-heading"><div><h2>引擎模式</h2><p>设置版本 v{{ state.settings.revision }}</p></div><span class="badge" :class="state.settings.mode === 'hybrid' ? 'status-in_review' : 'status-verified'">{{ state.settings.mode === 'hybrid' ? '本地 + 可选 AI 建议' : '本地规则' }}</span></div><form class="engine-form" @submit.prevent="saveSettings"><label>识别模式<select v-model="settings.mode" :disabled="!owner || busy || generating"><option value="local">本地规则识别</option><option value="hybrid">本地规则 + 可选 AI 参考建议</option></select></label><label>默认 AI 配置<select v-model="settings.defaultProfileId" :disabled="!owner || busy || generating"><option value="">未指定</option><option v-for="profile in usableProfiles" :key="profile.id" :value="String(profile.id)">{{ profile.name }} · {{ profile.model }}</option></select></label><p class="field-hint">本地模式不发起 AI 推理。混合模式需选择已启用且允许发送元数据的模型配置，仍由使用者逐次点击生成。</p><div v-if="owner" class="engine-actions"><button class="button button-primary" :disabled="busy || generating || settings.mode === 'hybrid' && !settings.defaultProfileId">{{ busy ? '保存中…' : '保存引擎设置' }}</button></div><p v-else class="field-hint">当前角色可查看，工作区负责人可修改设置。</p></form></section>
        <section class="panel"><div class="panel-heading"><div><h2>自由选择模型服务</h2><p>HTTPS 地址、协议与模型标识由负责人配置。</p></div><button v-if="owner" class="button button-secondary" type="button" :disabled="busy || generating" @click="editProfile()">新增配置</button></div><p v-if="!state.encryptionReady" class="engine-encryption-note">服务端尚未启用 API 密钥加密。需要管理员配置加密密钥后才能保存密钥；无需密钥的服务可按服务端允许范围配置。</p><div class="engine-profiles"><article v-for="profile in state.profiles" :key="profile.id"><div><strong>{{ profile.name }}</strong><span class="badge" :class="profile.enabled ? 'status-verified' : 'status-draft'">{{ profile.enabled ? '已启用' : '已停用' }}</span></div><p>{{ profile.model }} · {{ profile.protocol === 'responses' ? 'Responses' : 'Chat Completions' }}</p><p class="engine-url">{{ profile.baseUrl }}</p><p>配置 v{{ profile.revision }} · {{ profile.hasApiKey ? '已保存密钥' : '无已保存密钥' }} · {{ profile.allowMetadata ? '允许受限元数据' : '未允许发送元数据' }}</p><div v-if="owner" class="engine-actions"><button class="button button-secondary" type="button" :disabled="busy || generating" @click="editProfile(profile)">编辑配置</button><button class="button button-secondary" type="button" :disabled="busy || generating" @click="testProfile(profile)">测试连接 / 获取模型</button></div></article><p v-if="!state.profiles.length" class="empty-state">尚无 AI 模型配置。本地规则识别可继续使用。</p></div><details v-if="models.length" class="engine-model-results engine-model-list"><summary>{{ profileName(testedProfileId || 0) }}返回的模型（{{ models.length }}）</summary><p>{{ models.join('、') }}</p></details></section></div>

      <section v-if="profileForm && owner" class="panel"><div class="panel-heading"><h2>{{ editingProfile ? '编辑模型配置' : '新增模型配置' }}</h2><button class="button button-secondary" type="button" :disabled="busy || generating" @click="closeProfile">取消</button></div><form class="engine-form" @submit.prevent="saveProfile"><div class="engine-form-grid"><label>配置名称<input v-model="profileDraft.name" required maxlength="80" :disabled="busy || generating"></label><label>请求协议<select v-model="profileDraft.protocol" :disabled="busy || generating"><option value="chat_completions">OpenAI 兼容 Chat Completions</option><option value="responses">OpenAI 兼容 Responses</option></select></label><label class="engine-wide">HTTPS API 基础地址<input v-model="profileDraft.baseUrl" type="url" required pattern="https://.*" maxlength="2048" :disabled="busy || generating" placeholder="https://api.example.com/v1"></label><label>模型标识<input v-model="profileDraft.model" required maxlength="160" list="classification-profile-models" :disabled="busy || generating" placeholder="填写服务支持的实际模型 ID"><datalist id="classification-profile-models"><option v-for="model in editorModels" :key="model" :value="model"/></datalist></label><label>API 密钥<input v-model="apiKey" type="password" autocomplete="new-password" autocapitalize="off" spellcheck="false" :disabled="busy || generating || clearApiKey || !state.encryptionReady" :placeholder="editingProfile?.hasApiKey ? '留空保留已保存密钥' : '可选，按服务要求填写'"></label></div><label v-if="editingProfile?.hasApiKey" class="engine-checkbox"><input v-model="clearApiKey" type="checkbox" :disabled="busy || generating">清除已保存密钥</label><p v-if="changedKeyDestination" class="message message-warning">地址已更改。请填写新密钥或明确清除旧密钥，再保存配置。</p><label class="engine-checkbox"><input v-model="profileDraft.enabled" type="checkbox" :disabled="busy || generating">启用此配置</label><label class="engine-checkbox"><input v-model="profileDraft.allowMetadata" type="checkbox" :disabled="busy || generating">允许发送所选资产名称、系统、字段名与类型（不含原始记录或注释）</label><p class="field-hint">密钥不会回显。未填写新密钥时保留已保存值；请求返回或离开页面后清空本次输入。连接测试只读取模型列表，不发送资产信息；不支持模型列表时仍可手动填写模型标识。</p><div v-if="editorModels.length" class="engine-model-results"><strong>服务返回的模型</strong><p>{{ editorModels.join('、') }}</p></div><div class="engine-actions"><button v-if="profileConflict" class="button button-secondary" type="button" :disabled="busy || generating" @click="reloadProfile">重新读取并替换草稿</button><button class="button button-primary" :disabled="busy || generating || changedKeyDestination || profileConflict">{{ busy ? '保存中…' : '保存模型配置' }}</button></div></form></section>

      <section class="panel" aria-labelledby="ai-asset-title"><div class="panel-heading"><div><h2 id="ai-asset-title">资产 AI 参考建议</h2><p>独立保存建议，核对后再进行人工分类确认。</p></div><button class="button button-secondary" type="button" :disabled="generating" @click="emit('selectAsset')">选择台账资产</button></div><div v-if="asset" class="engine-assistance"><div class="engine-asset-scope"><strong>{{ asset.asset.name }}</strong><span>资产 #{{ asset.asset.id }} · 分类 v{{ asset.revision }} · {{ asset.asset.system }}</span></div><div class="engine-inference-grid"><div><label>本次采用模型<select v-model="profileId" :disabled="!editor || generating || state.settings.mode !== 'hybrid'"><option value="">请选择启用且已授权的配置</option><option v-for="profile in usableProfiles" :key="profile.id" :value="String(profile.id)">{{ profile.name }} · {{ profile.model }}</option></select></label><p v-if="state.settings.mode === 'local'" class="field-hint">当前为本地规则模式。负责人启用混合模式并允许元数据发送后，可逐次生成 AI 参考建议。</p><p v-else-if="!usableProfiles.length" class="field-hint">暂无已启用且允许发送元数据的模型配置。</p><p class="field-hint">调用所选服务可能产生费用。建议只包含数据属性、内部级别和理由，不自动采用，也不认定重要或核心数据。</p><button v-if="editor" class="button button-primary" type="button" :disabled="!inferenceReady || generating || busy" @click="generateSuggestion">{{ generating ? '正在生成建议…' : '生成 AI 参考建议' }}</button><p v-else class="field-hint">当前角色可查看建议；负责人或经办人可发起生成。</p></div><details class="engine-payload"><summary>所选资产的可发送元数据预览</summary><p v-if="inputLoading" class="field-hint" role="status">正在读取可发送元数据…</p><p v-else-if="inputError" class="message message-error" role="alert">{{ inputError }}<button class="table-link" type="button" @click="loadInput">重新读取</button></p><pre v-else-if="inputMetadata">{{ payloadPreview }}</pre><p v-else-if="!editor" class="field-hint">负责人或经办人可读取待发送元数据预览。</p><p class="field-hint">只允许资产名称、系统及字段名称和类型，不包含原始记录、字段注释或凭据。</p></details></div><div class="engine-suggestion-heading"><h3>该资产的参考建议</h3><button class="table-link" type="button" :disabled="suggestionsLoading || generating" @click="loadSuggestions">刷新建议</button></div><p v-if="suggestionsLoading" class="field-hint" role="status">正在读取建议…</p><ol v-else-if="suggestions.length" class="engine-suggestions"><li v-for="suggestion in suggestions" :key="suggestion.id"><div><strong>{{ assetCategoryLabels[suggestion.category] || suggestion.category }} · {{ assetBusinessLevelLabels[suggestion.businessLevel] || suggestion.businessLevel }}</strong><span class="badge" :class="suggestion.assetRevision === asset.revision ? 'status-in_review' : 'status-draft'">{{ suggestion.assetRevision === asset.revision ? '参考建议，待人工确认' : '历史建议，资产版本已变化' }}</span></div><p>{{ suggestion.rationale }}</p><p class="engine-suggestion-meta">{{ profileName(suggestion.profileId) }} · {{ suggestion.model }} · 配置 v{{ suggestion.profileRevision }} · 资产分类 v{{ suggestion.assetRevision }} · {{ formatDate(suggestion.createdAt) }}</p></li></ol><p v-else class="field-hint">该资产尚无 AI 参考建议。</p></div><p v-else class="empty-state">先在资产台账选择一项资产，核对其身份和当前分类版本。</p></section>
    </template>
  </section>
</template>

<style scoped>
.engine-settings{display:grid;gap:16px;min-width:0}.engine-feedback{display:flex;gap:16px;justify-content:space-between}.engine-local{padding:18px 20px}.engine-local dl{display:grid;gap:16px;margin:0 0 14px;font-size:12px}.engine-local dl>div{display:grid;grid-template-columns:90px minmax(0,1fr);gap:14px}.engine-local dt{color:var(--secondary)}.engine-local dd{margin:0;display:flex;flex-wrap:wrap;gap:7px;line-height:1.8}.engine-local dd>span{padding:3px 8px;background:var(--accent-soft);color:var(--accent);border-radius:4px}.engine-layout{display:grid;grid-template-columns:330px minmax(0,1fr);gap:16px;align-items:start}.engine-layout>*{min-width:0}.engine-form{padding:20px;display:grid;gap:16px}.engine-settings label:not(.engine-checkbox){display:grid;gap:7px;min-width:0;font-size:12px;color:var(--secondary)}.engine-settings :is(input:not([type=checkbox]),select,textarea){min-width:0;width:100%;min-height:40px;padding:8px 10px;border:1px solid var(--input-line);border-radius:6px;background:var(--surface);color:var(--ink);font:inherit;font-size:12px}.engine-checkbox{display:flex;align-items:flex-start;gap:8px;font-size:12px;line-height:1.8}.engine-checkbox input{margin-top:4px;flex:none;accent-color:var(--accent)}.engine-actions{display:flex;flex-wrap:wrap;gap:8px}.engine-profiles{padding:0 20px}.engine-profiles article{padding:18px 0;border-top:1px solid var(--line);font-size:12px}.engine-profiles article:first-child{border-top:0}.engine-profiles article>div:first-child{display:flex;justify-content:space-between;align-items:center;gap:10px}.engine-profiles strong{font-size:14px}.engine-profiles p{color:var(--secondary);margin:5px 0;overflow-wrap:anywhere}.engine-profiles .engine-actions{margin-top:12px}.engine-profiles .button{min-height:36px;font-size:12px;padding:6px 10px}.engine-encryption-note{padding:12px 16px;margin:0 20px;background:var(--warning-soft);color:var(--warning);border-radius:6px;font-size:12px;line-height:1.8}.engine-form-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px}.engine-wide{grid-column:1/-1}.engine-model-results{padding:12px 14px;background:var(--surface-subtle);border-radius:6px;font-size:12px;overflow-wrap:anywhere}.engine-model-list{margin:0 20px 18px}.engine-model-list summary{cursor:pointer;color:var(--accent)}.engine-model-results p{margin:8px 0 0;color:var(--secondary)}.engine-assistance{padding:20px;display:grid;gap:18px}.engine-asset-scope{display:flex;flex-wrap:wrap;align-items:center;gap:12px;padding:12px 14px;border:1px solid var(--line);border-radius:6px;background:var(--surface-subtle);font-size:12px}.engine-asset-scope strong{font-size:14px;overflow-wrap:anywhere}.engine-asset-scope span{color:var(--secondary)}.engine-inference-grid{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:24px}.engine-inference-grid>div>.field-hint{margin:10px 0;line-height:1.8}.engine-payload{padding:14px;border:1px solid var(--line);border-radius:6px;font-size:12px;min-width:0;align-self:start}.engine-payload summary{cursor:pointer;color:var(--accent)}.engine-payload pre{margin:14px 0;white-space:pre-wrap;overflow-wrap:anywhere;max-height:250px;overflow:auto;color:var(--ink);font-size:12px}.engine-suggestion-heading{display:flex;justify-content:space-between;align-items:center;gap:12px;padding-top:16px;border-top:1px solid var(--line)}.engine-suggestion-heading h3{font-size:14px}.engine-suggestions{margin:0;padding:0;list-style:none;display:grid;gap:16px}.engine-suggestions li{padding:16px;border:1px solid var(--line);border-radius:8px;font-size:12px}.engine-suggestions li>div{display:flex;flex-wrap:wrap;justify-content:space-between;gap:10px}.engine-suggestions li>p{margin:10px 0 0;line-height:1.8;white-space:pre-wrap;overflow-wrap:anywhere}.engine-suggestion-meta{color:var(--secondary)}.engine-settings .field-hint{font-size:12px;line-height:1.8}.engine-settings button.table-link{border:0;background:none;padding:0;font:inherit;font-size:12px;color:var(--accent);cursor:pointer}.engine-settings .panel-heading p{font-size:12px}.engine-settings .message{margin:0}.engine-settings button:disabled{cursor:default}
@media(max-width:1099px){.engine-layout{grid-template-columns:280px minmax(0,1fr)}}
@media(max-width:767px){.engine-layout,.engine-inference-grid,.engine-form-grid{grid-template-columns:minmax(0,1fr)}.engine-wide{grid-column:auto}.engine-local dl>div{grid-template-columns:minmax(0,1fr);gap:6px}.engine-feedback{flex-wrap:wrap}.engine-form,.engine-assistance{padding:16px}}
</style>
