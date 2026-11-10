<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { runtimeRiskSeverityLabels, type RuntimeFinding, type RuntimeRisk } from './runtime-findings'
import { canManageChecks } from './workflow'

const props = defineProps<{ workspace: Workspace; finding: RuntimeFinding; risk?: RuntimeRisk; overdue: boolean }>()
const emit = defineEmits<{ updated: [risk: RuntimeRisk]; sessionExpired: [] }>()
const edit = ref(false)
const pending = ref(false)
const notice = ref('')
const error = ref('')
const opener = ref<HTMLButtonElement>()
const severity = ref<RuntimeRisk['severity']>('medium')
const team = ref('')
const due = ref('')
const reason = ref('')
const revision = ref(0)
const active = new AbortController()
onBeforeUnmount(() => active.abort())

function start() {
  revision.value = props.risk?.revision ?? 0
  severity.value = props.risk?.severity ?? 'medium'
  team.value = props.risk?.ownerTeam ?? ''
  due.value = props.risk?.dueDate ?? ''
  reason.value = ''
  notice.value = ''
  error.value = ''
  edit.value = true
}

function close() {
  edit.value = false
  opener.value?.focus()
}

async function save() {
  if (pending.value || !team.value.trim() || !reason.value.trim()) return
  error.value = ''
  pending.value = true
  try {
    const base = workspacePath(props.workspace.slug) + '/api-security/runtime/findings/'
    const result = await request<RuntimeRisk>(base + props.finding.id + '/triage', {
      method: 'PUT',
      body: JSON.stringify({
        revision: revision.value, severity: severity.value,
        ownerTeam: team.value.trim(), dueDate: due.value, reason: reason.value.trim(),
      }),
      signal: active.signal,
    })
    emit('updated', result)
    notice.value = '已保存研判。'
    close()
  } catch (cause) {
    if (cause instanceof ApiError && cause.status === 401) emit('sessionExpired')
    else if (cause instanceof ApiError && cause.status === 409) error.value = '风险状态或版本已变化，请刷新后重试。'
    else if (!active.signal.aborted) error.value = describeError(cause)
  } finally {
    pending.value = false
  }
}
</script>

<template>
  <div class="runtime-risk">
    <template v-if="risk">
      <strong>{{ runtimeRiskSeverityLabels[risk.severity] }}风险</strong>
      <span class="api-secondary">{{ risk.ownerTeam }}</span>
      <span v-if="risk.dueDate" class="api-secondary">
        <span v-if="overdue" class="badge status-needs_work">已逾期</span> {{ risk.dueDate }}
      </span>
      <details class="runtime-risk-basis">
        <summary>研判依据</summary>
        <p>{{ risk.reason }}</p>
        <p class="api-secondary">{{ risk.updatedBy }} · {{ formatDate(risk.updatedAt) }}</p>
      </details>
    </template>
    <span v-else class="api-secondary">待研判</span>
    <button v-if="canManageChecks(workspace.role) && finding.checkStatus !== 'verified'" ref="opener" type="button"
            class="button button-secondary runtime-risk-edit" :aria-expanded="edit" @click="edit ? close() : start()">
      {{ risk ? '修改研判' : '风险研判' }}
    </button>
    <p v-if="notice" class="api-secondary" role="status">{{ notice }}</p>
    <form v-if="edit" class="runtime-risk-form" @submit.prevent="save">
      <label>风险等级
        <select v-model="severity" :disabled="pending">
          <option v-for="(label, value) in runtimeRiskSeverityLabels" :key="value" :value="value">{{ label }}</option>
        </select>
      </label>
      <label>责任团队 <input v-model.trim="team" type="text" maxlength="80" required :disabled="pending"></label>
      <label>整改期限 <input v-model="due" type="date" min="2000-01-01" max="9999-12-31" :disabled="pending"></label>
      <label>本次研判依据 <textarea v-model.trim="reason" rows="2" maxlength="400" required :disabled="pending"></textarea></label>
      <p v-if="error" class="message message-error" role="alert">{{ error }}</p>
      <button type="submit" class="button button-primary" :disabled="pending">保存研判</button>
    </form>
  </div>
</template>

<style scoped>
.runtime-risk { min-width: 0; overflow-wrap: anywhere; }
.runtime-risk-basis { margin-top: .25rem; font-size: .8rem; }
.runtime-risk-basis summary { cursor: pointer; }
.runtime-risk-edit { margin-top: .35rem; min-height: 2rem; }
.runtime-risk-form { display: grid; gap: .5rem; margin-top: .55rem; }
.runtime-risk-form label { display: grid; gap: .2rem; font-size: .85rem; }
.runtime-risk-form input, .runtime-risk-form select, .runtime-risk-form textarea { width: 100%; min-width: 0; }
.runtime-risk-form button { justify-self: start; }
</style>
