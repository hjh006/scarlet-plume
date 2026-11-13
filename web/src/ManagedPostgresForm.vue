<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ApiError, describeError, request } from './api'
import type { AutomationJob } from './automation'

const props = defineProps<{
  managedPath: string
  allowedHosts: string[]
  job?: AutomationJob
}>()
const emit = defineEmits<{ cancel: []; saved: [job: AutomationJob]; sessionExpired: [] }>()
const original = props.job?.managed
const source = ref(props.job?.source ?? '')
const host = ref(original?.host ?? props.allowedHosts[0] ?? '')
const port = ref<number | ''>(original?.port ?? 5432)
const database = ref(original?.database ?? '')
const user = ref(original?.user ?? '')
const password = ref('')
const schemaText = ref(original?.schemas.join(', ') ?? 'public')
const intervalMinutes = ref<number | ''>(original?.intervalMinutes ?? 15)
const enabled = ref(original?.enabled ?? true)
const autoChecks = ref(original?.autoChecks ?? !original)
const autoReports = ref(original?.autoReports ?? !original)
const heading = ref<HTMLElement | null>(null)
const pending = ref<'test' | 'save' | ''>('')
const feedback = ref<{ kind: 'success' | 'error'; message: string }>()
const lifecycle = new AbortController()
const needsPassword = computed(() => !original ||
  host.value.trim() !== original.host || port.value !== original.port ||
  database.value.trim() !== original.database || user.value.trim() !== original.user,
)

watch([host, port, database, user, password, schemaText], () => { feedback.value = undefined })

function connection() {
  const schemas = [...new Set(schemaText.value.split(/[\n,，]/).map(schema => schema.trim()).filter(Boolean))]
  if (!host.value.trim() || !database.value.trim() || !user.value.trim() || !schemas.length ||
      port.value === '' || !Number.isInteger(port.value) || port.value < 1 || port.value > 65535) {
    feedback.value = { kind: 'error', message: '请填写主机、端口、数据库、用户和至少一个 schema。' }
    return
  }
  if (needsPassword.value && !password.value) {
    feedback.value = { kind: 'error', message: '请输入此连接的密码。' }
    return
  }
  return {
    host: host.value.trim(), port: port.value, database: database.value.trim(),
    user: user.value.trim(), password: password.value || undefined, schemas,
  }
}

function showError(error: unknown) {
  if (lifecycle.signal.aborted) return
  if (error instanceof ApiError && error.status === 401) emit('sessionExpired')
  else feedback.value = { kind: 'error', message: describeError(error) }
}

async function testConnection() {
  if (pending.value) return
  const body = connection()
  if (!body) return
  pending.value = 'test'
  feedback.value = undefined
  try {
    const { tableCount } = await request<{ tableCount: number }>(props.managedPath + '/test', {
      method: 'POST', body: JSON.stringify({ ...body, jobId: props.job?.id }), signal: lifecycle.signal,
    })
    feedback.value = { kind: 'success', message: `连接成功，可读取 ${tableCount} 张表的元数据。` }
  } catch (error) {
    showError(error)
  } finally {
    pending.value = ''
  }
}

async function save() {
  if (pending.value) return
  const body = connection()
  if (!body) return
  if (!source.value.trim() || intervalMinutes.value === '' || !Number.isInteger(intervalMinutes.value) ||
      intervalMinutes.value < 1 || intervalMinutes.value > 1440) {
    feedback.value = { kind: 'error', message: '请填写来源名称，并将采集频率设为 1 到 1440 分钟。' }
    return
  }
  pending.value = 'save'
  feedback.value = undefined
  try {
    const job = await request<AutomationJob>(props.managedPath + '/jobs' + (props.job ? '/' + props.job.id : ''), {
      method: props.job ? 'PUT' : 'POST',
      body: JSON.stringify({
        ...body, source: props.job ? undefined : source.value.trim(),
        revision: original?.revision, intervalMinutes: intervalMinutes.value, enabled: enabled.value,
        autoChecks: autoChecks.value, autoReports: autoReports.value,
      }),
      signal: lifecycle.signal,
    })
    password.value = ''
    emit('saved', job)
  } catch (error) {
    showError(error)
  } finally {
    pending.value = ''
  }
}

onMounted(() => { heading.value?.focus() })
onBeforeUnmount(() => {
  password.value = ''
  lifecycle.abort()
})
function scrollField(id: string) { document.getElementById(id)?.scrollIntoView({ block: 'center', behavior: 'smooth' }) }
</script>

<template>
  <section class="automation-form managed-pg-form" aria-labelledby="managed-pg-heading">
    <h2 id="managed-pg-heading" ref="heading" tabindex="-1">{{ job ? '配置 PostgreSQL 任务' : '新建 PostgreSQL 采集任务' }}</h2>
    <div class="managed-workbench"><nav class="panel managed-outline" aria-label="PostgreSQL配置步骤"><h3>配置步骤</h3><button type="button" @click="scrollField('managed-pg-source')">1. 连接信息</button><button type="button" @click="scrollField('managed-pg-schemas')">2. 授权范围</button><button type="button" @click="scrollField('managed-pg-interval')">3. 频率与自动化</button></nav><form class="panel managed-connection-form" autocomplete="off" @submit.prevent="save">
      <fieldset class="managed-pg-fields" :disabled="Boolean(pending)">
        <div class="field">
          <label for="managed-pg-source">来源名称</label>
          <input id="managed-pg-source" v-model="source" maxlength="80" required :readonly="Boolean(job)">
        </div>
        <div class="managed-pg-pair">
          <div class="field">
            <label for="managed-pg-host">主机</label>
            <input id="managed-pg-host" v-model="host" maxlength="253" required list="managed-pg-hosts"
                   spellcheck="false" autocapitalize="none">
            <datalist id="managed-pg-hosts"><option v-for="allowedHost in allowedHosts" :key="allowedHost" :value="allowedHost" /></datalist>
          </div>
          <div class="field">
            <label for="managed-pg-port">端口</label>
            <input id="managed-pg-port" v-model="port" type="number" min="1" max="65535" step="1" required>
          </div>
        </div>
        <div class="managed-pg-pair">
          <div class="field">
            <label for="managed-pg-database">数据库</label>
            <input id="managed-pg-database" v-model="database" maxlength="63" required spellcheck="false" autocapitalize="none">
          </div>
          <div class="field">
            <label for="managed-pg-user">数据库用户</label>
            <input id="managed-pg-user" v-model="user" maxlength="63" required spellcheck="false" autocapitalize="none">
          </div>
        </div>
        <div class="field">
          <label for="managed-pg-password">数据库密码</label>
          <input id="managed-pg-password" v-model="password" type="password" autocomplete="new-password"
                 aria-describedby="managed-pg-password-hint" :required="needsPassword">
          <p id="managed-pg-password-hint" class="field-hint">{{ needsPassword ? '请输入只读采集用户的密码。' : '留空继续使用已保存的密码。' }}</p>
        </div>
        <div class="field">
          <label for="managed-pg-schemas">采集 schema</label>
          <input id="managed-pg-schemas" v-model="schemaText" required aria-describedby="managed-pg-schemas-hint"
                 spellcheck="false" autocapitalize="none">
          <p id="managed-pg-schemas-hint" class="field-hint">多个 schema 用逗号分隔。采集表目录、字段结构和直接授权。</p>
        </div>
        <div class="managed-pg-pair managed-pg-schedule">
          <div class="field">
            <label for="managed-pg-interval">采集频率（分钟）</label>
            <input id="managed-pg-interval" v-model="intervalMinutes" type="number" min="1" max="1440" step="1" required>
          </div>
          <div class="field">
            <label class="managed-pg-check" for="managed-pg-enabled">
              <input id="managed-pg-enabled" v-model="enabled" type="checkbox"> 启用定期采集
            </label>
          </div>
        </div>
        <fieldset class="template-fieldset managed-pg-automation">
          <legend>采集后的自动化</legend>
          <p class="field-hint">每次采集都会按字段名称和类型识别疑似敏感字段。</p>
          <div class="field">
            <label class="managed-pg-check" for="managed-pg-auto-checks">
              <input id="managed-pg-auto-checks" v-model="autoChecks" type="checkbox" aria-describedby="managed-pg-auto-checks-hint"> 自动检查与取证
            </label>
            <p id="managed-pg-auto-checks-hint" class="field-hint">核对疑似敏感字段与直接 SELECT 授权，登记依据和待核对线索。</p>
          </div>
          <div class="field">
            <label class="managed-pg-check" for="managed-pg-auto-reports">
              <input id="managed-pg-auto-reports" v-model="autoReports" type="checkbox" aria-describedby="managed-pg-auto-reports-hint"> 自动归档报告
            </label>
            <p id="managed-pg-auto-reports-hint" class="field-hint">采集成功后保存本次识别和关联检查快照，可导出 HTML 或 JSON。</p>
          </div>
        </fieldset>
      </fieldset>
      <p v-if="feedback" class="message managed-pg-feedback" :class="'message-' + feedback.kind"
         :role="feedback.kind === 'error' ? 'alert' : 'status'">{{ feedback.message }}</p>
      <div class="form-actions">
        <button type="button" class="button button-secondary" :disabled="Boolean(pending)" @click="testConnection">
          {{ pending === 'test' ? '正在测试…' : '测试连接' }}
        </button>
        <button type="submit" class="button button-primary" :disabled="Boolean(pending)">
          {{ pending === 'save' ? '正在保存…' : '保存任务' }}
        </button>
        <button type="button" class="button button-secondary" :disabled="Boolean(pending)" @click="emit('cancel')">取消</button>
      </div>
    </form><aside class="managed-context"><section class="panel"><div class="panel-heading"><h3>采用范围</h3></div><dl><div><dt>配置修订</dt><dd>{{original?.revision ? 'v'+original.revision : '待创建'}}</dd></div><div><dt>主机</dt><dd>{{host||'待填写'}}:{{port}}</dd></div><div><dt>数据库</dt><dd>{{database||'待填写'}}</dd></div><div><dt>schema</dt><dd>{{schemaText||'待填写'}}</dd></div><div><dt>采集频率</dt><dd>每 {{intervalMinutes}} 分钟</dd></div></dl></section><section class="panel"><div class="panel-heading"><h3>连接诊断</h3></div><ol class="managed-diagnostics"><li><strong>连接与元数据权限</strong><span :class="{'diagnostic-known':feedback?.kind==='success'}">{{feedback?.kind==='success'?'连接测试成功':pending==='test'?'正在测试':'尚未确认'}}</span></li><li><strong>可见目录范围</strong><p>{{feedback?.kind==='success'?feedback.message:'测试成功后返回当前账号可读的表元数据数量。'}}</p></li><li><strong>内容与权限覆盖</strong><p>以实际采集批次的结果为准，连接成功不代表已采集或检查通过。</p></li></ol></section></aside></div>
  </section>
</template>

<style scoped>
.managed-pg-form{padding:0!important;border:0!important;background:transparent!important;max-width:none!important}.managed-pg-form>h2{font-size:22px;margin:0 0 18px}.managed-workbench{display:grid;grid-template-columns:170px minmax(0,1fr) 280px;gap:16px;align-items:start}.managed-outline{display:grid;gap:22px;padding:20px;font-size:12px}.managed-connection-form{padding:20px;min-width:0}.managed-pg-fields{padding:0;margin:0;border:0;display:block}.managed-pg-pair{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:18px}.managed-pg-form .field{min-width:0;margin-bottom:18px}.managed-pg-form .field input{width:100%;min-width:0}.managed-pg-form .field input[type=checkbox]{width:auto}.managed-pg-automation{margin:0;padding:16px;border:1px solid var(--line);border-radius:8px}.managed-context{display:grid;gap:16px}.managed-context dl{margin:0;padding:8px 18px;font-size:12px}.managed-context dl>div{display:grid;grid-template-columns:70px minmax(0,1fr);gap:10px;padding:11px 0;border-bottom:1px solid var(--line)}.managed-context dt{color:var(--secondary)}.managed-context dd{margin:0;overflow-wrap:anywhere}.managed-diagnostics{list-style:none;margin:0;padding:18px;display:grid;gap:20px;font-size:12px}.managed-diagnostics li{display:grid;gap:6px}.managed-diagnostics span,.managed-diagnostics p{font-size:12px;color:var(--secondary)}.managed-diagnostics .diagnostic-known{color:var(--success)}.managed-connection-form .form-actions{justify-content:flex-end;border-top:1px solid var(--line);padding-top:16px;position:sticky;bottom:0;background:white}.managed-pg-feedback{margin-top:15px}@media(max-width:1320px){.managed-workbench{grid-template-columns:140px minmax(0,1fr) 240px}.managed-connection-form{padding:17px}}
</style>

<style scoped>nav button { display:block; width:100%; text-align:left; border:0; background:transparent; color:var(--muted); padding:10px 8px; cursor:pointer; font:inherit; border-radius:6px }nav button:hover { color:var(--blue); background:var(--surface-muted) }</style>
