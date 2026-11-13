<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { ApiError, describeError, downloadFile, formatDate, request, workspacePath, type Workspace } from './api'
import { scenarioContextHref } from './scenario-context'
import {
  endpointCapability, endpointCategoryLabels, endpointChannelCoverage, endpointChannelLabels,
  endpointDeploymentLabels, endpointModeLabels, endpointPolicyStatusLabels, endpointReceiptLabels, endpointScopeLabels,
  endpointNativeActionLabels, endpointNativeBoundaries, endpointNativeTargetScopes, endpointNativePhaseLabels, endpointNativePhaseTitles, endpointNativeStateLabels, endpointPrivilegeLabels,
  endpointSessionDeploymentLabels, endpointSessionResultLabels, endpointSessionCurrentReasons, endpointSessionCodeLabels,
  type EndpointChannel, type EndpointDevice, type EndpointEnforcementScope, type EndpointEventScope,
  type EndpointMode, type EndpointPolicy, type EndpointPolicyExport, type EndpointReceipt, type EndpointSessionEvent,
} from './endpoint-channels'

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ sessionExpired: []; inspectEvents: [scope: EndpointEventScope] }>()
const editor = computed(() => ['owner', 'operator'].includes(props.workspace.role))
const owner = computed(() => props.workspace.role === 'owner')
const reviewer = computed(() => ['owner', 'reviewer'].includes(props.workspace.role))
const channels = Object.keys(endpointChannelLabels) as EndpointChannel[]
const devices = ref<EndpointDevice[]>([]), policies = ref<EndpointPolicy[]>([])
const deviceLimit = ref(100), policyLimit = ref(100), sourceId = ref(0), channelFilter = ref('')
const deviceKey = ref(''), selected = ref<EndpointPolicy>(), exportSnapshot = ref<EndpointPolicyExport>()
const loading = ref(false), busy = ref(false), error = ref(''), notice = ref(''), conflict = ref(false)
const showForm = ref(false), decision = ref<'reject' | 'revoke' | ''>(''), decisionNote = ref('')
const form = reactive({ channel: 'usb' as EndpointChannel, scope: 'controlled_operations' as EndpointEnforcementScope, mode: 'monitor' as EndpointMode, categories: Object.keys(endpointCategoryLabels), reason: '', durationMinutes: 60 })
let active = new AbortController(), generation = 0
type RequestScope = { base: string; signal: AbortSignal; generation: number }
const capture = (): RequestScope => ({ base: workspacePath(props.workspace.slug) + '/endpoint', signal: active.signal, generation })
const current = (scope: RequestScope) => !scope.signal.aborted && scope.generation === generation
const key = (device: EndpointDevice) => device.sourceId + ':' + device.deviceRef
const selectedDevice = computed(() => devices.value.find(device => key(device) === deviceKey.value))
const visibleDevices = computed(() => devices.value.filter(device => !sourceId.value || device.sourceId === Number(sourceId.value)))
const sourceChoices = computed(() => [...new Map(devices.value.map(device => [device.sourceId, device.sourceName])).entries()])
const capability = computed(() => endpointCapability(selectedDevice.value, form.channel, form.scope))
const nativeAvailable = computed(() => {
  const cap = endpointCapability(selectedDevice.value, form.channel, 'native_control')
  return selectedDevice.value?.os === 'windows' && !!cap?.supported && cap.modes.includes('block_all') && ['administrator','system'].includes(cap.privilege || '') && cap.targetScope === endpointNativeTargetScopes[form.channel] && !!cap.targetRefs?.length && cap.targetRefs.length <= 20 && cap.targetRefs.every(ref => /^[0-9a-f]{64}$/.test(ref)) && /^[0-9a-f]{64}$/.test(cap.configDigest || '')
})
const sessionAvailable = computed(() => {
  const cap = endpointCapability(selectedDevice.value, 'clipboard', 'session_response')
  return selectedDevice.value?.os === 'windows' && !!cap?.supported && cap.executor === 'windows_clipboard_session_guard' && cap.privilege === 'interactive_user' && cap.targetScope === 'interactive_session_clipboard' && cap.targetRefs?.length === 1 && /^[0-9a-f]{64}$/.test(cap.targetRefs[0]) && /^[0-9a-f]{64}$/.test(cap.configDigest || '')
})
const modeChoices = computed(() => (form.scope === 'native_control' ? ['block_all'] : form.scope === 'session_response' ? ['monitor','clear_sensitive','clear_all'] : ['monitor','block_sensitive','block_all']).map(mode => ({mode:mode as EndpointMode,label:form.scope === 'native_control' ? endpointNativeActionLabels[form.channel] : form.scope === 'session_response' && mode === 'monitor' ? '仅观察，不清除' : endpointModeLabels[mode as EndpointMode]})))
const needsCategories = computed(() => ['block_sensitive','clear_sensitive'].includes(form.mode))
const formAvailable = computed(() => !!selectedDevice.value?.current && !!capability.value?.supported && capability.value.modes.includes(form.mode) && modeChoices.value.some(choice => choice.mode === form.mode) && (form.scope !== 'native_control' || nativeAvailable.value && form.mode === 'block_all') && (form.scope !== 'session_response' || form.channel === 'clipboard' && sessionAvailable.value))
const canCreate = computed(() => editor.value && !conflict.value && formAvailable.value && !!form.reason.trim() && form.reason.trim().length <= 400 && (!needsCategories.value || form.categories.length > 0) && Number.isInteger(form.durationMinutes) && form.durationMinutes >= 1 && form.durationMinutes <= 1440)
const canReview = computed(() => reviewer.value && !!selected.value?.canReview && !conflict.value)
const canRevoke = computed(() => owner.value && !!selected.value && ['draft', 'approved'].includes(selected.value.status) && !conflict.value)
const receipts = computed(() => selected.value?.receipts ?? (selected.value?.latestReceipt ? [selected.value.latestReceipt] : []))
const sessionEvents = computed(() => selected.value?.sessionEvents ?? [])
const nativeProgress = computed(() => Object.entries(endpointNativePhaseLabels).map(([phase,label]) => {
  const matching = receipts.value.filter(receipt => receipt.scope === 'native_control' && receipt.phase === phase && (!selected.value?.nativeLease || receipt.leaseRef === selected.value.nativeLease.leaseRef))
  const receipt = matching.sort((left,right) => (right.id || 0) - (left.id || 0) || Date.parse(right.observedAt) - Date.parse(left.observedAt))[0]
  return {phase,title:endpointNativePhaseTitles[phase],label,receipt}
}))

function failure(caught: unknown, scope: RequestScope) {
  if (!current(scope)) return
  if (caught instanceof ApiError && caught.status === 401) { emit('sessionExpired'); return }
  if (caught instanceof ApiError && caught.status === 409) conflict.value = true
  error.value = describeError(caught)
}
async function readPolicies(scope: RequestScope) {
  const query = new URLSearchParams()
  if (sourceId.value) query.set('sourceId', String(sourceId.value))
  if (channelFilter.value) query.set('channel', channelFilter.value)
  const result = await request<{ items: EndpointPolicy[]; limit: number }>(scope.base + '/policies?' + query, { signal: scope.signal })
  if (current(scope)) { policies.value = result.items; policyLimit.value = result.limit }
}
async function readDevices(scope: RequestScope) {
  const result = await request<{ items: EndpointDevice[]; limit: number }>(scope.base + '/devices', { signal: scope.signal })
  if (current(scope)) { devices.value = result.items; deviceLimit.value = result.limit }
}
async function refresh() {
  if (busy.value || loading.value) return
  const scope = capture(); loading.value = true; error.value = ''
  try {
    await Promise.all([readDevices(scope), readPolicies(scope)])
    if (current(scope) && selected.value) {
      const detail = await request<EndpointPolicy>(scope.base + '/policies/' + selected.value.id, { signal: scope.signal })
      if (current(scope)) selected.value = detail
    }
    if (current(scope)) conflict.value = false
  } catch (caught) { failure(caught, scope) }
  finally { if (current(scope)) loading.value = false }
}
async function filterPolicies() {
  if (busy.value || loading.value) return
  const scope = capture(); loading.value = true; error.value = ''; selected.value = undefined; exportSnapshot.value = undefined
  if (selectedDevice.value && sourceId.value && selectedDevice.value.sourceId !== Number(sourceId.value)) { deviceKey.value = ''; showForm.value = false }
  try { await readPolicies(scope) } catch (caught) { failure(caught, scope) }
  finally { if (current(scope)) loading.value = false }
}
async function chooseDevice(device: EndpointDevice) {
  if (busy.value || loading.value) return
  deviceKey.value = key(device); sourceId.value = device.sourceId; showForm.value = false; notice.value = ''
  await filterPolicies()
}
function openForm() {
  if (!editor.value || !selectedDevice.value || busy.value || loading.value) return
  Object.assign(form, { channel: (channelFilter.value || 'usb') as EndpointChannel, scope: 'controlled_operations', mode: 'monitor', categories: Object.keys(endpointCategoryLabels), reason: '', durationMinutes: 60 })
  showForm.value = true; error.value = ''; notice.value = ''; conflict.value = false
}
function resetMode() { form.mode = form.scope === 'native_control' ? 'block_all' : 'monitor' }
function openSessionForm() {
  if (!editor.value || !selectedDevice.value?.current || !sessionAvailable.value || busy.value || loading.value) return
  openForm(); form.channel = 'clipboard'; form.scope = 'session_response'; form.mode = 'monitor'
}
async function create() {
  if (!canCreate.value || busy.value || loading.value) return
  const scope = capture(), device = selectedDevice.value!
  busy.value = true; error.value = ''; notice.value = ''
  try {
    const family = await request<{ items: EndpointPolicy[] }>(scope.base + '/policies?sourceId=' + device.sourceId + '&channel=' + form.channel, { signal: scope.signal })
    if (!current(scope)) return
    const revision = Math.max(0, ...family.items.filter(policy => policy.scope === form.scope).map(policy => policy.revision))
    const boundTargets = ['native_control','session_response'].includes(form.scope) ? {targetScope:capability.value?.targetScope,targetRefs:capability.value?.targetRefs,configDigest:capability.value?.configDigest} : {}
    const policy = await request<EndpointPolicy>(scope.base + '/policies', { method: 'POST', body: JSON.stringify({ sourceId: device.sourceId, deviceRef: device.deviceRef, channel: form.channel, scope: form.scope, mode: form.mode, categories: needsCategories.value ? form.categories : [], reason: form.reason.trim(), durationMinutes: Number(form.durationMinutes), revision, ...boundTargets }), signal: scope.signal })
    if (!current(scope)) return
    selected.value = policy; showForm.value = false; exportSnapshot.value = undefined; conflict.value = false
    await readPolicies(scope)
    if (current(scope)) notice.value = '策略申请已登记，等待另一位有权限的人员独立审批。尚未证明设备已应用或发生阻断。'
  } catch (caught) { failure(caught, scope) }
  finally { if (current(scope)) busy.value = false }
}
async function choosePolicy(id: number) {
  if (busy.value || loading.value) return
  const scope = capture(); busy.value = true; error.value = ''; notice.value = ''; decision.value = ''; decisionNote.value = ''
  try {
    const result = await request<EndpointPolicy>(scope.base + '/policies/' + id, { signal: scope.signal })
    if (current(scope)) { selected.value = result; exportSnapshot.value = undefined; conflict.value = false }
  } catch (caught) { failure(caught, scope) }
  finally { if (current(scope)) busy.value = false }
}
async function changePolicy(action: 'approve' | 'reject' | 'revoke') {
  if (!selected.value || busy.value || loading.value || conflict.value || (action === 'revoke' ? !canRevoke.value : !canReview.value) || (action !== 'approve' && !decisionNote.value.trim())) return
  const scope = capture(), policy = selected.value; busy.value = true; error.value = ''; notice.value = ''
  try {
    await request(scope.base + '/policies/' + policy.id + '/' + action, { method: 'POST', body: JSON.stringify({ revision: policy.revision, ...(action === 'reject' ? { note: decisionNote.value.trim() } : action === 'revoke' ? { reason: decisionNote.value.trim() } : {}) }), signal: scope.signal })
    const updated = await request<EndpointPolicy>(scope.base + '/policies/' + policy.id, { signal: scope.signal })
    if (!current(scope)) return
    selected.value = updated; exportSnapshot.value = undefined; decision.value = ''; decisionNote.value = ''
    await readPolicies(scope)
    if (current(scope)) notice.value = action === 'approve' ? '已独立批准此版本。请继续核对客户端的实际应用回执与事件。' : action === 'revoke' ? policy.scope === 'session_response' ? '已登记撤销，不再批准后续会话清除；请核对源端状态。已经清除的内容不会恢复。' : policy.scope === 'native_control' ? '已登记撤销。原生控制仍须核对源端回滚回执；策略撤销或过期不等于实际恢复已完成。' : '已登记撤销。平台状态与客户端实际停止结果须分别核对。' : '已拒绝此申请。原申请与证据继续留存。'
  } catch (caught) { failure(caught, scope) }
  finally { if (current(scope)) busy.value = false }
}
async function exportPolicy(format: 'html' | 'json') {
  if (!selected.value || busy.value || loading.value) return
  const scope = capture(), policy = selected.value; busy.value = true; error.value = ''
  try {
    const frozen = await request<EndpointPolicyExport>(scope.base + '/policies/' + policy.id + '/exports', { method: 'POST', body: '{}', signal: scope.signal })
    if (!current(scope)) return
    exportSnapshot.value = frozen
    await downloadFile(scope.base + '/exports/' + frozen.id + '/export?format=' + format, 'endpoint-policy-' + policy.id + '-snapshot-' + frozen.id + '.' + format, scope.signal)
    if (current(scope)) notice.value = '已导出冻结的策略与回执证据。导出不代表实际阻断或监管通过。'
  } catch (caught) { failure(caught, scope) }
  finally { if (current(scope)) busy.value = false }
}
function capabilitySummary(device: EndpointDevice, channel: EndpointChannel) {
  const scopes = device.capabilities.filter(cap => cap.channel === channel && cap.supported).map(cap => cap.scope === 'observation' ? '可观察' : cap.scope === 'native_control' ? endpointNativeActionLabels[channel] : endpointScopeLabels[cap.scope])
  return [...new Set(scopes)].join(' / ') || '尚未报告可用能力'
}
function deploymentLabel(policy: EndpointPolicy) {
  if (policy.deploymentState === 'unavailable' && policy.status === 'draft') return '尚未批准下发'
  if (policy.scope === 'native_control') return endpointNativeStateLabels[policy.deploymentState] || '尚无可核对的原生控制阶段'
  if (policy.scope === 'session_response') return policy.status === 'rejected' ? '申请未获批准，不能启动监听' : endpointSessionDeploymentLabels[policy.deploymentState] || '尚无可核对的会话监听状态'
  if (['rejected', 'revoked', 'superseded'].includes(policy.status)) return '不再是可下发策略；历史回执分别留存'
  return endpointDeploymentLabels[policy.deploymentState] || policy.deploymentState || '尚无源端应用回执'
}
function policyActionLabel(policy: EndpointPolicy) {
  if (policy.scope === 'session_response' && policy.mode === 'monitor') return '仅观察，不清除'
  return policy.scope === 'native_control' ? endpointNativeActionLabels[policy.channel] : endpointModeLabels[policy.mode]
}
function nativeCapability(device: EndpointDevice, channel: EndpointChannel) { return endpointCapability(device, channel, 'native_control') }
function sessionCapability(device: EndpointDevice) { return endpointCapability(device, 'clipboard', 'session_response') }
function receiptLabel(receipt: EndpointReceipt) {
  if (receipt.scope === 'session_response') return receipt.status === 'loaded' ? '来源报告监听已加载（不代表已经清除）' : receipt.status === 'stopped' ? '来源报告监听已停止（不恢复内容）' : receipt.status === 'failed' ? '来源报告监听加载失败' : receipt.status === 'unsupported' ? '来源报告不支持会话响应' : '会话监听状态未知'
  if (receipt.scope !== 'native_control') return endpointReceiptLabels[receipt.status] || receipt.status
  if (endpointNativePhaseLabels[receipt.phase || '']) return endpointNativePhaseLabels[receipt.phase || '']
  return ['failed','unsupported'].includes(receipt.status) ? endpointReceiptLabels[receipt.status] + '（尚无原生阶段证明）' : '尚无可核对的原生控制阶段'
}
function currentPolicyLabel(policy: EndpointPolicy) {
  if (policy.current) return policy.scope === 'session_response' ? '仅限采用时的登录会话，须核对逐次响应' : '当前批准版本与采用能力相符'
  if (policy.scope === 'session_response' && ['draft','approved'].includes(policy.status) && endpointSessionCurrentReasons[policy.currentReason || '']) return endpointSessionCurrentReasons[policy.currentReason || '']
  if (policy.deploymentState === 'expired') return '有效期已结束'
  if (policy.status === 'draft') return '尚未批准生效'
  if (policy.status !== 'approved') return '此版本不再批准下发'
  return policy.scope === 'session_response' ? '当前来源、登录会话或配置不可用' : '来源、能力或版本当前不可用'
}
function sessionEventMatches(event: EndpointSessionEvent, policy: EndpointPolicy) {
  return event.scope === 'session_response' && event.sourceId === policy.sourceId && event.deviceRef === policy.deviceRef && event.sessionRef === policy.targetRefs?.[0] && event.policyId === policy.id && event.policyRevision === policy.revision && event.policyDigest === policy.digest && event.configDigest === policy.configDigest
}
function sessionCaptureLabel(capture: string) {
  return capture === 'complete' ? '来源声明本次文本范围完整' : capture === 'partial' ? '捕获不完整，其他表示仍未知' : '捕获范围未知'
}
function sessionResultLabel(event: EndpointSessionEvent) {
  return event.code === 'session_changed' ? '登录会话已变化，本次未清除' : endpointSessionResultLabels[event.status] || '未识别的来源结果，不能认定安全'
}
function inspect(scope: EndpointEventScope) { emit('inspectEvents', scope) }
watch(() => props.workspace.slug, () => {
  active.abort(); active = new AbortController(); generation++
  devices.value = []; policies.value = []; sourceId.value = 0; channelFilter.value = ''; deviceKey.value = ''; selected.value = undefined; exportSnapshot.value = undefined
  showForm.value = false; decision.value = ''; decisionNote.value = ''; error.value = ''; notice.value = ''; conflict.value = false; loading.value = false; busy.value = false
  void refresh()
}, { immediate: true })
onBeforeUnmount(() => active.abort())
</script>

<template>
  <section class="endpoint-channels" aria-label="终端通道设备与策略">
    <div class="endpoint-heading"><div><h3>已接入设备与通道策略</h3><p>先核对设备能力，再申请有期限的策略。观察记录、独立批准、监听加载与逐次动作结果分别留痕。</p></div><div class="endpoint-actions"><a class="button" href="/downloads/endpoint-channel-enforcement.md" download>接入与验收说明</a><a class="button" href="/downloads/scarlet-endpoint-agent-windows-amd64.exe" download>下载 Windows x64 执行器</a><a class="button" href="/downloads/scarlet-endpoint-agent-windows-arm64.exe" download>下载 Windows ARM64 执行器</a><button class="button" type="button" :disabled="loading || busy" @click="refresh">{{ loading ? '正在刷新…' : '刷新设备与回执' }}</button></div></div>
    <p>执行器须由负责人在授权设备显式安装或运行，下载不代表已部署。本轮已完成 Windows 交叉编译与本机合成验证，Windows 实机兼容性仍待验收。</p>
    <details class="endpoint-enterprise"><summary>企业部署与验收 · 系统服务与登录会话响应分开</summary><div class="endpoint-body"><p>系统服务负责已批准的三项固定原生控制及到期恢复；每用户观察进程负责该登录会话中的剪贴板新变化。System 服务不能因此读取所有用户桌面剪贴板。</p><p>本地剪贴板响应须由当前登录用户单独运行会话监听器，仅对其观测的变化通知进行识别和条件清除。新登录使旧会话策略永久失效，需要重新注册并审批；这不是粘贴前阻断。</p><p>域策略或 MDM 已管理、权限不足或管理归属未知时，须由企业管理员配置和确认；执行器保守拒绝接管，不会自动覆盖其他管理设置。</p><a class="button" href="/downloads/windows-enterprise-deployment.zip" download>下载企业部署与验收包（ZIP）</a><a class="button" href="/downloads/windows-enterprise-manifest.json" download>下载发布校验清单</a><p>安装、卸载与功能验收须在授权 Windows 环境显式运行。本轮没有 Windows 实机验收结果，配置或回读回执不表示实际阻断已通过。</p><p>本地会话优先验收 Office、Edge/Chrome、记事本中的复制与粘贴场景。实机状态为 not_run；这些是待验收项目，不是已兼容应用清单。</p></div></details>
    <p v-if="error" class="message message-error" role="alert">{{ error }}</p><p v-if="notice" class="message message-success" role="status">{{ notice }}</p>
    <p v-if="conflict" class="message" role="status">策略或能力版本已变化。请刷新并核对最新范围后再操作；当前输入保留。</p>
    <section class="panel">
      <div class="panel-heading"><div><h4>设备与能力 · 已返回 {{ devices.length }} 台</h4><p>最多返回 {{ deviceLimit }} 台；来源未接入或近期无心跳不表示通道未使用。</p></div><label class="endpoint-filter">安全事件来源<select v-model.number="sourceId" :disabled="loading || busy" @change="filterPolicies"><option :value="0">全部已返回来源</option><option v-for="[id,name] in sourceChoices" :key="id" :value="id">{{ name }} · #{{ id }}</option></select></label></div>
      <p v-if="loading && !devices.length" class="message" role="status">正在读取已接入设备及其来源能力…</p>
      <div v-else-if="!visibleDevices.length" class="endpoint-empty"><p>当前尚未取得可核对的设备记录。接入后才能配置策略；没有设备记录不代表USB、打印或剪贴板已经受控。</p><ol><li>在<a class="table-link" :href="scenarioContextHref(workspace.slug,'operations')">运营与处置为每台设备登记独立安全事件来源</a>。</li><li>下载对应 Windows 执行器，在授权设备配置来源令牌和本地匿名引用密钥（HMAC）；密钥不填入本页面。</li><li>显式运行客户端，收到心跳注册后刷新设备与能力。</li></ol></div>
      <div v-else class="table-wrap"><table><thead><tr><th>设备 / 来源</th><th>系统与联络状态</th><th>来源报告的通道能力</th><th>操作</th></tr></thead><tbody><tr v-for="device in visibleDevices" :key="key(device)" :class="{ 'is-selected': deviceKey === key(device) }"><th scope="row">设备 {{ device.deviceRef.slice(0,12) }}<small>{{ device.sourceName }} · 来源 #{{ device.sourceId }}</small><small>{{ device.current ? '来源当前有效' : '来源已失效，保留历史记录' }}</small></th><td>{{ ({windows:'Windows',linux:'Linux',darwin:'macOS'} as Record<string,string>)[device.os] || device.os }}<small>{{ device.online ? '近期在线' : '近期未联络' }} · 客户端 {{ device.agentVersion }}</small><small>最近心跳 {{ formatDate(device.lastSeenAt) }}</small></td><td><span v-for="channel in channels" :key="channel" class="channel-summary">{{ endpointChannelLabels[channel] }}：{{ capabilitySummary(device,channel) }}</span></td><td><button class="button" type="button" :aria-pressed="deviceKey === key(device)" :disabled="loading || busy" @click="chooseDevice(device)">查看设备与策略</button></td></tr></tbody></table></div>
    </section>

    <section v-if="selectedDevice" class="panel">
      <div class="panel-heading"><div><h4>{{ selectedDevice.sourceName }} · 设备 {{ selectedDevice.deviceRef.slice(0,12) }}</h4><p>来源报告的应用能力须与具体执行器核对；不能把观察或加载回执当作已发生阻断。</p></div><div class="endpoint-actions"><button class="button" type="button" :disabled="busy || loading" @click="inspect({sourceId:selectedDevice.sourceId,deviceRef:selectedDevice.deviceRef})">核对设备来源事件</button><button v-if="editor" class="button button-primary" type="button" :disabled="busy || loading || !selectedDevice.current" @click="openForm">申请通道策略</button></div></div>
      <div class="endpoint-body endpoint-coverage"><p v-for="channel in channels" :key="channel"><strong>{{ endpointChannelLabels[channel] }}</strong> {{ endpointChannelCoverage[channel] }}</p><section v-if="selectedDevice.os==='windows'" class="endpoint-native-coverage"><h4>Windows 企业原生控制 · 固定范围</h4><div class="table-wrap"><table><thead><tr><th>可配置动作</th><th>来源前提与目标</th><th>适用边界</th></tr></thead><tbody><tr v-for="channel in channels" :key="channel"><th scope="row">{{ endpointNativeActionLabels[channel] }}</th><td>{{ nativeCapability(selectedDevice,channel)?.supported ? '来源报告配置前提就绪' : '当前不支持，待企业管理员核对' }}<small>{{ endpointPrivilegeLabels[nativeCapability(selectedDevice,channel)?.privilege || 'unknown'] }} · 已报告目标 {{ nativeCapability(selectedDevice,channel)?.targetRefs?.length || 0 }} 项</small><small>{{ nativeCapability(selectedDevice,channel)?.reason || '尚无进一步原生能力声明' }}</small></td><td>{{ endpointNativeBoundaries[channel] }}</td></tr></tbody></table></div></section><section v-if="selectedDevice.os==='windows'" class="endpoint-session-coverage"><h4>本地剪贴板 · 当前登录会话响应</h4><p>{{ sessionCapability(selectedDevice)?.supported ? '来源报告当前登录会话响应可用' : '当前来源未提供登录会话响应能力，请在对应用户会话接入监听器。' }}</p><p>{{ endpointPrivilegeLabels[sessionCapability(selectedDevice)?.privilege || 'unknown'] }} · 会话 {{ sessionCapability(selectedDevice)?.targetRefs?.[0]?.slice(0,12) || '尚未取得可采用引用' }}。System 服务和禁用 RDP 重定向均不能替代该能力。</p><p>变化通知发生后才识别并按条件清除，存在竞态；清除成功不等于粘贴前阻断，也不证明覆盖所有应用。</p><button v-if="editor" class="button button-primary" type="button" :disabled="busy || loading || !selectedDevice.current || !sessionAvailable" @click="openSessionForm">申请此登录会话响应策略</button></section><details><summary>核对来源能力与执行器</summary><ul><li v-for="(cap,index) in selectedDevice.capabilities" :key="index">{{ endpointChannelLabels[cap.channel] }} · {{ cap.scope === 'observation' ? '仅观察' : endpointScopeLabels[cap.scope] }} · {{ cap.supported ? '来源报告可用' : '未提供此能力' }}<small>{{ cap.reason || '未提供进一步覆盖说明' }}<template v-if="cap.executor"> · 执行器 {{ cap.executor }}</template></small></li></ul><small>设备引用 {{ selectedDevice.deviceRef }} · 来源观察 {{ formatDate(selectedDevice.observedAt) }}</small></details></div>
    </section>

    <form v-if="showForm && selectedDevice && editor" class="panel endpoint-form" @submit.prevent="create">
      <div><h4>申请临时通道策略</h4><p>有效期从提交申请时开始计算，审批等待也占用有效期。新版本需重新独立审批。</p></div>
      <div class="endpoint-fields"><label>数据通道<select v-model="form.channel" :disabled="busy || loading" @change="resetMode"><option v-for="channel in channels" :key="channel" :value="channel">{{ endpointChannelLabels[channel] }}</option></select></label><label>应用范围<select v-model="form.scope" :disabled="busy || loading" @change="resetMode"><option value="controlled_operations">指定的受控动作</option><option value="native_control" :disabled="!nativeAvailable">设备原生控制{{ nativeAvailable ? '' : '（设备未提供）' }}</option><option value="session_response" :disabled="!sessionAvailable || form.channel!=='clipboard'">当前登录会话变化后响应{{ sessionAvailable ? '' : '（需剪贴板会话能力）' }}</option></select></label><label>策略动作<select v-model="form.mode" :disabled="busy || loading"><option v-for="choice in modeChoices" :key="choice.mode" :value="choice.mode" :disabled="!capability?.supported || !capability.modes.includes(choice.mode)">{{ choice.label }}</option></select></label><label>有效期（分钟，从提交起计）<input v-model.number="form.durationMinutes" type="number" min="1" max="1440" step="1" required :disabled="busy || loading"></label></div>
      <p v-if="form.scope === 'controlled_operations'">仅覆盖此客户端明确执行的受控动作，不覆盖资源管理器、浏览器或其他应用的任意操作。</p><template v-else-if="form.scope==='native_control'"><p><strong>{{ endpointNativeActionLabels[form.channel] }}</strong> · 仅采用这项固定控制，不按敏感内容选择或扩展到其他目标。</p><p>{{ endpointNativeBoundaries[form.channel] }}</p><p>来源权限：{{ endpointPrivilegeLabels[capability?.privilege || 'unknown'] }} · 已配置目标 {{ capability?.targetRefs?.length || 0 }} 项。目标由客户端预先配置，页面不接收打印机名称或任意目标。</p><details><summary>核对本次只读采用的匿名目标</summary><small v-for="target in capability?.targetRefs || []" :key="target">{{ target }}</small><small>配置摘要 {{ capability?.configDigest || '未提供' }}</small></details></template><template v-else><p><strong>当前登录会话 · 剪贴板变化后的条件响应</strong></p><p>只处理该匿名登录会话中监听器观测到的新变化。存在识别与清除之间的竞态，清除成功也不能证明内容未被其他应用粘贴。</p><p>采用会话 {{ capability?.targetRefs?.[0]?.slice(0,12) || '未提供' }} · {{ endpointPrivilegeLabels[capability?.privilege || 'unknown'] }}。不由 System 服务跨会话清除，也不等同于禁用 RDP 重定向。</p><p v-if="form.mode==='clear_sensitive'">命中任一选定格式后，尝试清除当前剪贴板的整体内容，不是局部脱敏；只在观察条件仍匹配时执行。</p><p v-if="form.mode==='clear_all'">该模式会在内容尚未变更、条件仍匹配时尝试清除变化后的当前全部内容。撤销或到期只停止后续清除，不恢复已清除内容。</p><details><summary>核对此次只读采用的登录会话</summary><small>{{ capability?.targetRefs?.[0] || '未提供会话引用' }}</small><small>配置摘要 {{ capability?.configDigest || '未提供' }}</small></details></template>
      <p v-if="!formAvailable" class="message">设备尚未报告支持所选通道、范围与动作。{{ capability?.reason || '可先核对观察事件，或由负责人接入具有对应能力的执行器。' }}</p>
      <fieldset v-if="needsCategories"><legend>{{ form.scope==='session_response' ? '用于变化后条件清除的内容格式' : '需要阻止的内容格式' }}</legend><label v-for="(label,category) in endpointCategoryLabels" :key="category" class="endpoint-check"><input v-model="form.categories" type="checkbox" :value="category" :disabled="busy || loading">{{ label }}</label><p v-if="form.scope==='session_response'">部分文本中出现明确格式命中时也可采用清除策略；未命中且捕获不完整时仍为未知，不能解释成内容安全。</p><p v-else>仅采用支持的本地格式检查。没有内容检查能力的通道不能因观察到任务或变化就认定内容已检查。</p></fieldset>
      <label>申请依据<textarea v-model="form.reason" maxlength="400" rows="3" required :disabled="busy || loading" placeholder="说明业务范围、影响和本次采用此策略的依据"></textarea></label>
      <div class="endpoint-actions"><button class="button" type="button" :disabled="busy" @click="showForm=false">取消</button><button class="button button-primary" type="submit" :disabled="busy || loading || !canCreate">{{ busy ? '正在提交…' : '提交独立审批' }}</button></div>
    </form>

    <section class="panel"><div class="panel-heading"><div><h4>策略版本 · {{ policies.length }} 条</h4><p>当前来源与通道筛选下最近 {{ policyLimit }} 条。批准不等于客户端已经加载，加载也不等于已发生阻断。</p></div><label class="endpoint-filter">通道<select v-model="channelFilter" :disabled="busy || loading" @change="filterPolicies"><option value="">全部通道</option><option v-for="channel in channels" :key="channel" :value="channel">{{ endpointChannelLabels[channel] }}</option></select></label></div><p v-if="!policies.length" class="message">当前范围尚无策略记录。选择已接入设备后，可由有权限的人员提出申请。</p><div v-else class="table-wrap"><table><thead><tr><th>策略 / 来源</th><th>范围与动作</th><th>审批 / 应用回执</th><th>有效期</th><th>核对</th></tr></thead><tbody><tr v-for="policy in policies" :key="policy.id"><th scope="row">{{ endpointChannelLabels[policy.channel] }} · v{{ policy.revision }}<small>{{ policy.sourceName }} · #{{ policy.id }}</small></th><td>{{ endpointScopeLabels[policy.scope] }}<small>{{ policyActionLabel(policy) }}</small></td><td>{{ endpointPolicyStatusLabels[policy.status] || policy.status }}<small>{{ deploymentLabel(policy) }}</small></td><td>{{ formatDate(policy.expiresAt) }}<small>从 {{ formatDate(policy.createdAt) }} 起 {{ policy.durationMinutes }} 分钟</small></td><td><button class="button" type="button" :disabled="busy || loading" :aria-pressed="selected?.id === policy.id" @click="choosePolicy(policy.id)">查看版本与回执</button></td></tr></tbody></table></div></section>

    <section v-if="selected" class="panel" aria-label="策略版本与来源回执">
      <div class="panel-heading"><div><h4>{{ endpointChannelLabels[selected.channel] }}策略 #{{ selected.id }} · v{{ selected.revision }}</h4><p>{{ endpointPolicyStatusLabels[selected.status] || selected.status }} · {{ selected.sourceName }}</p></div><div class="endpoint-actions"><button class="button" type="button" :disabled="busy || loading" @click="exportPolicy('html')">冻结并导出 HTML</button><button class="button" type="button" :disabled="busy || loading" @click="exportPolicy('json')">冻结并导出 JSON</button></div></div>
      <div class="endpoint-body"><dl class="endpoint-facts"><div><dt>采用范围</dt><dd>{{ endpointScopeLabels[selected.scope] }} · {{ policyActionLabel(selected) }}<small>设备 {{ selected.deviceRef.slice(0,12) }} · {{ selected.os }}</small></dd></div><div><dt>申请与审批</dt><dd>{{ selected.creator }}<small>{{ selected.approver ? '独立审批：'+selected.approver+' · '+formatDate(selected.approvedAt || '') : '尚无独立批准记录' }}</small></dd></div><div><dt>有效期</dt><dd>{{ formatDate(selected.expiresAt) }}<small>提交 {{ formatDate(selected.createdAt) }} · {{ selected.durationMinutes }} 分钟</small></dd></div><div><dt>来源状态</dt><dd>{{ currentPolicyLabel(selected) }}<small>{{ deploymentLabel(selected) }}</small></dd></div></dl><p class="endpoint-reason"><strong>申请依据</strong> {{ selected.reason }}</p><p v-if="selected.decisionNote" class="endpoint-reason"><strong>拒绝或撤销依据</strong> {{ selected.decisionNote }}</p><p v-if="selected.categories.length">采用的内容格式：{{ selected.categories.map(category=>endpointCategoryLabels[category] || category).join('、') }}</p>
        <section v-if="selected.scope==='native_control'" class="endpoint-native-evidence"><h4>{{ endpointNativeActionLabels[selected.channel] }} · 配置与验证分别核对</h4><p>{{ endpointNativeBoundaries[selected.channel] }}</p><dl class="endpoint-facts"><div><dt>采用权限与范围</dt><dd>{{ endpointPrivilegeLabels[selected.capability?.privilege || 'unknown'] }}<small>冻结目标 {{ selected.targetRefs?.length || 0 }} 项；不接受远程任意目标</small></dd></div><div><dt>执行租约</dt><dd>{{ selected.nativeLease ? endpointNativeStateLabels[selected.nativeLease.state] || '来源租约阶段待核对' : '尚未签发执行租约' }}<small v-if="selected.nativeLease">租约有效至 {{ formatDate(selected.nativeLease.expiresAt) }}</small></dd></div></dl><p>以下仅展示源端分阶段回执。配置、回读、功能验证和恢复互不替代；本轮 Windows 实机功能仍待验收。</p><dl class="endpoint-native-phases"><div v-for="entry in nativeProgress" :key="entry.phase"><dt>{{ entry.title }}</dt><dd>{{ entry.receipt ? entry.label+' · '+formatDate(entry.receipt.observedAt) : '未收到该阶段回执' }}<small v-if="entry.receipt?.verificationMethod">功能验证方法：{{ entry.receipt.verificationMethod }}</small><small v-if="entry.receipt?.verificationRef">功能验证引用：{{ entry.receipt.verificationRef }}</small></dd></div></dl><p>撤销、到期或能力变化后需等待源端恢复；发现其他管理设置已改变时保留冲突，不会强行覆盖。</p><details><summary>核对冻结目标、租约与状态摘要</summary><small>目标范围 {{ selected.targetScope || '未提供' }}</small><small v-for="target in selected.targetRefs || []" :key="target">目标 {{ target }}</small><small>配置摘要 {{ selected.configDigest || '未提供' }}</small><template v-if="selected.nativeLease"><small>租约 {{ selected.nativeLease.leaseRef }}</small><small>采用前状态 {{ selected.nativeLease.priorStateDigest || '尚无回执' }}</small><small>计划状态（尚不表示已写入） {{ selected.nativeLease.plannedStateDigest || '尚无准备回执' }}</small><small v-if="selected.nativeLease.preparedAt">原状态保全 {{ formatDate(selected.nativeLease.preparedAt) }}</small><small>实际采用后状态 {{ selected.nativeLease.appliedStateDigest || '尚无配置写入回执' }}</small><small>最近来源更新 {{ formatDate(selected.nativeLease.updatedAt) }}</small></template></details></section>
        <section v-if="selected.scope==='session_response'" class="endpoint-session-evidence"><h4>当前登录会话 · 变化后响应证据</h4><p>此版本仅适用于冻结的匿名登录会话。新登录使旧策略永久失效，不能将旧版本自动采用到新会话；需要重新注册与审批。</p><p v-if="selected.sessionCurrent===false">来源报告当前登录会话已不同，此冻结旧版本不可再次采用。</p><p v-if="selected.endedAt">此版本已于 {{ formatDate(selected.endedAt) }} 结束；不恢复已清除内容。</p><p>撤销或到期只停止后续清除，不恢复已经清除的内容。监听加载、格式命中、尝试清除和响应后回读均不证明粘贴前阻断。</p><dl class="endpoint-facts"><div><dt>采用会话</dt><dd>{{ selected.targetRefs?.[0]?.slice(0,12) || '未提供' }}<small>{{ endpointPrivilegeLabels[selected.capability?.privilege || 'unknown'] }}</small></dd></div><div><dt>响应证据记录</dt><dd>当前返回 {{ sessionEvents.length }} 条<template v-if="selected.sessionEventsTotal!=null"> / 共 {{ selected.sessionEventsTotal }} 条</template><small>{{ selected.sessionEventsTruncated ? '已达到采用预算，未返回部分仍未知' : '仅核对本版本已收到的来源记录' }}</small></dd></div></dl><ul v-if="selected.sessionCoverageMissing?.length" class="endpoint-session-gaps"><li v-for="gap in selected.sessionCoverageMissing" :key="gap">{{ gap }}</li></ul><p v-if="!sessionEvents.length" class="message">尚未取得本版本的剪贴板变化响应。已批准、监听 loaded 或没有事件都不证明本地应用已受控，也不证明不存在风险。</p><details v-else class="endpoint-session-history"><summary>逐次变化与响应记录 · {{ sessionEvents.length }} 条</summary><div class="table-wrap"><table class="endpoint-session-table"><thead><tr><th>观察 / 响应时间</th><th>来源结果与覆盖</th><th>清除与条件核验</th><th>会话 / 版本关联</th><th>证据引用</th></tr></thead><tbody><tr v-for="event in sessionEvents" :key="event.id"><td>{{ formatDate(event.observedAt) }}<small>响应 {{ formatDate(event.responseAt) }}</small><small>平台接收 {{ formatDate(event.receivedAt) }}</small></td><td>{{ sessionResultLabel(event) }}<small v-if="endpointSessionCodeLabels[event.code]">{{ endpointSessionCodeLabels[event.code] }}</small><small v-if="event.historical">接收时属于历史或迟到记录，不作为当前响应效果</small><small v-else-if="event.currentResponse">接收时作为当次有效来源响应留存，不代表当前仍生效或粘贴前阻断</small><small>{{ sessionCaptureLabel(event.capture) }}</small><small>格式命中：{{ (event.categories || []).map(category=>endpointCategoryLabels[category] || category).join('、') || '未取得明确格式命中' }}</small><small v-if="event.capture!=='complete' && event.status==='unknown'">未完整检查且无明确命中时保持未知。</small><small v-if="event.capture!=='complete' && event.status==='cleared_after_change' && selected.mode==='clear_sensitive'">部分文本明确命中触发响应，未观察表示仍未知。</small></td><td>{{ event.clearAttempted ? '来源报告已尝试清除' : '来源报告未尝试清除' }}<small>{{ event.sequenceMatched ? '来源报告条件序列匹配' : event.code==='session_sequence_changed' ? '内容序列已变化，本次未清除' : '未取得条件序列匹配证据' }}</small><small>{{ event.clipboardEmpty ? '源端响应后回读为空；仍非粘贴前阻断' : '未取得响应后为空的回读' }}</small></td><td>策略 #{{ event.policyId }} · v{{ event.policyRevision }}<small>会话 {{ event.sessionRef.slice(0,12) }}</small><small>{{ sessionEventMatches(event,selected) ? '对应此冻结会话与策略版本' : '关联字段不一致，不能作为此版本证明' }}</small></td><td>#{{ event.id }}<details><summary>核对匿名证据与缺口</summary><small>来源结果代码 {{ event.code }}</small><small>事件 {{ event.eventRef }}</small><small>操作 {{ event.operationRef }}</small><small>观察序列 {{ event.sequenceRef || '未提供' }}</small><small>响应时序列 {{ event.currentSequenceRef || '未提供' }}</small><small>内容引用 {{ event.contentRef || '未提供' }}</small><small>证据摘要 {{ event.snapshotDigest }}</small></details></td></tr></tbody></table></div></details><p>实机验收：尚未运行（not_run）。请按部署包验收说明优先核对 Office、Edge/Chrome、记事本；不据这些来源声明生成应用兼容或全覆盖结论。</p><details><summary>核对冻结会话与配置摘要</summary><small>{{ selected.targetRefs?.[0] || '未提供会话引用' }}</small><small>配置摘要 {{ selected.configDigest || '未提供' }}</small></details></section>
        <div class="endpoint-actions endpoint-review"><button v-if="canReview" class="button button-primary" type="button" :disabled="busy || loading" @click="changePolicy('approve')">独立批准此版本</button><button v-if="canReview" class="button" type="button" :disabled="busy || loading" @click="decision='reject';decisionNote=''">拒绝申请</button><button v-if="canRevoke" class="button" type="button" :disabled="busy || loading" @click="decision='revoke';decisionNote=''">{{ selected.scope==='native_control' ? '撤销并请求回滚' : '撤销此策略' }}</button><button v-if="selected.scope!=='session_response'" class="button" type="button" :disabled="busy || loading" @click="inspect({sourceId:selected.sourceId,deviceRef:selected.deviceRef,channel:selected.channel})">{{ selected.scope==='native_control' ? '查看同设备与通道事件' : '核对此范围实际事件' }}</button></div>
        <p v-if="selected.scope==='native_control'">事件查询按来源、设备、通道和当前时间范围筛选，可能包含观察或受控动作记录，不作为这项原生配置的功能验证。</p><p v-if="!canReview && selected.status === 'draft'">此版本须由具备审批权限且未提出本次申请的人员独立核对。</p>
        <form v-if="decision" class="endpoint-decision" @submit.prevent="changePolicy(decision || 'reject')"><label>{{ decision === 'revoke' ? '撤销依据' : '拒绝依据' }}<textarea v-model="decisionNote" maxlength="400" rows="3" required :disabled="busy || loading"></textarea></label><div class="endpoint-actions"><button class="button" type="button" :disabled="busy" @click="decision=''">取消</button><button class="button" type="submit" :disabled="busy || loading || !decisionNote.trim() || conflict">{{ decision === 'revoke' ? '确认撤销申请' : '保存拒绝结论' }}</button></div></form>
        <h4>来源部署回执 · {{ receipts.length }} 条</h4><p v-if="selected.scope==='native_control'">原生配置与回读仅证明采用的设置或队列位已被来源回读，不能认定实际 I/O、打印停止或 RDP 重定向效果；功能与回滚仍须分别验证。</p><p v-else-if="selected.scope==='session_response'">loaded 只表示来源报告当前会话监听已加载，不表示收到过变化、执行过清除或阻断粘贴。实际结果须核对本版本的会话响应事件。</p><p v-else>“来源报告已加载”只证明对应执行器报告采用了该版本。实际拦截须核对同来源、设备、通道、版本和策略摘要的阻断事件。</p><p v-if="!receipts.length" class="message">尚无部署回执。客户端离线、未拉取、未批准或不支持时，不能显示为已应用或已拦截。</p><div v-else class="table-wrap"><table><thead><tr><th>来源声明</th><th>版本 / 执行器</th><th>时间与引用</th></tr></thead><tbody><tr v-for="receipt in receipts" :key="receipt.receiptRef"><td>{{ receiptLabel(receipt) }}<small>{{ receipt.code }}</small><template v-if="receipt.scope==='native_control'"><small>{{ receipt.action==='rollback' ? '恢复采用前状态' : '采用原生配置' }} · {{ receipt.targetRefs?.length || 0 }} 个冻结目标</small><small v-if="receipt.verificationMethod">功能验证方法：{{ receipt.verificationMethod }}</small><small v-if="receipt.verificationRef">功能验证引用：{{ receipt.verificationRef }}</small></template></td><td>v{{ receipt.policyRevision }} · {{ receipt.executor }}<small>{{ receipt.policyRevision === selected.revision && receipt.policyDigest === selected.digest ? '对应此策略版本与摘要' : '历史或不同版本回执，请核对' }}</small></td><td>{{ formatDate(receipt.observedAt) }}<small>回执 {{ receipt.receiptRef }}</small></td></tr></tbody></table></div>
        <details class="endpoint-digests"><summary>版本与证据摘要</summary><small>策略 {{ selected.digest }}</small><small>采用能力 {{ selected.capabilityDigest }}</small><small v-if="exportSnapshot">最近冻结 #{{ exportSnapshot.id }} · {{ exportSnapshot.snapshotDigest }} · {{ formatDate(exportSnapshot.createdAt) }}</small></details>
      </div>
    </section>
  </section>
</template>

<style scoped>
.endpoint-channels{display:grid;gap:18px;min-width:0}.endpoint-heading{display:flex;align-items:flex-start;justify-content:space-between;gap:16px}.endpoint-channels h3{font-size:16px;margin:0}.endpoint-channels h4{font-size:14px;margin:0}.endpoint-channels p{font-size:13px;line-height:1.75;color:var(--secondary);margin:7px 0 0;max-width:80ch}.endpoint-channels .panel-heading{gap:16px;flex-wrap:wrap}.endpoint-channels .message{margin:0}.endpoint-filter,.endpoint-form label,.endpoint-decision label{display:grid;gap:7px;font-size:13px}.endpoint-channels select,.endpoint-channels input:not([type=checkbox]),.endpoint-channels textarea{font:inherit;color:var(--ink);background:var(--surface);padding:10px;border:1px solid var(--input-line);border-radius:6px;min-width:0;max-width:100%}.endpoint-actions{display:flex;align-items:center;gap:8px;flex-wrap:wrap}.endpoint-body,.endpoint-form,.endpoint-empty{padding:18px 20px}.endpoint-form{display:grid;gap:16px}.endpoint-fields{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px}.endpoint-form fieldset{border:1px solid var(--line);border-radius:6px;padding:14px;display:flex;gap:16px;flex-wrap:wrap;min-width:0}.endpoint-form legend{font-size:13px;padding:0 5px}.endpoint-form fieldset .endpoint-check{display:flex;align-items:center;gap:7px}.endpoint-form fieldset p{flex-basis:100%}.endpoint-channels table{width:100%;min-width:720px;border-collapse:collapse}.endpoint-channels th,.endpoint-channels td{font-size:13px;text-align:left;padding:13px;border-bottom:1px solid var(--line);vertical-align:top}.endpoint-channels small{display:block;color:var(--secondary);font-size:12px;line-height:1.7;margin-top:5px;overflow-wrap:anywhere;font-weight:400}.channel-summary{display:block;font-size:12px;line-height:1.8}.endpoint-channels .is-selected{background:var(--surface-muted,var(--surface))}.endpoint-channels details{margin-top:16px}.endpoint-channels summary{cursor:pointer;font-size:13px;color:var(--accent)}.endpoint-coverage ul{margin:12px 0;padding-left:18px;display:grid;gap:12px;font-size:13px}.endpoint-coverage strong{color:var(--ink)}.endpoint-facts{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:18px;margin:0}.endpoint-facts dt{font-size:12px;color:var(--secondary);margin-bottom:5px}.endpoint-facts dd{font-size:13px;margin:0}.endpoint-reason{white-space:pre-wrap;overflow-wrap:anywhere}.endpoint-review{margin:18px 0}.endpoint-decision{display:grid;gap:14px;margin:16px 0 22px}.endpoint-body>h4{margin-top:24px}.endpoint-digests{overflow-wrap:anywhere}.endpoint-enterprise{border-top:1px solid var(--line);padding-top:14px}.endpoint-enterprise>.endpoint-body{padding:12px 0 0;display:grid;gap:12px}.endpoint-enterprise .button{justify-self:start}.endpoint-native-coverage,.endpoint-session-coverage{margin-top:20px}.endpoint-native-coverage h4{margin-bottom:10px}.endpoint-native-evidence,.endpoint-session-evidence{margin-top:22px;padding-top:18px;border-top:1px solid var(--line)}.endpoint-native-evidence>.endpoint-facts,.endpoint-session-evidence>.endpoint-facts{margin-top:16px}.endpoint-session-gaps{padding-left:20px;font-size:13px;line-height:1.8;color:var(--secondary)}.endpoint-channels .endpoint-session-table{min-width:1000px}.endpoint-native-phases{margin:16px 0 0;display:grid;gap:12px}.endpoint-native-phases>div{display:grid;grid-template-columns:minmax(180px,1fr) minmax(0,2fr);gap:14px;padding-bottom:12px;border-bottom:1px solid var(--line)}.endpoint-native-phases dt,.endpoint-native-phases dd{font-size:13px;margin:0;overflow-wrap:anywhere}@media(max-width:800px){.endpoint-heading{flex-direction:column}.endpoint-fields,.endpoint-facts,.endpoint-native-phases>div{grid-template-columns:1fr}.endpoint-filter{width:100%}}
</style>
