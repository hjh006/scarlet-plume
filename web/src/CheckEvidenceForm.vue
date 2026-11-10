<script setup lang="ts">
import { computed, ref } from 'vue'
import { ApiError, describeError, request } from './api'
import { evidenceKindLabels } from './workflow'

const props = defineProps<{
  apiUrl: string
  retest: boolean
  automaticRetest?: boolean
  targetDigest?: string
}>()
const emit = defineEmits<{ saved: [message: string]; sessionExpired: [] }>()
const selected = ref<'manual' | 'feishu'>('manual')
const kind = ref<keyof typeof evidenceKindLabels>(
  props.retest ? (props.targetDigest || props.automaticRetest ? 'remediation' : 'retest') : 'observation',
)
const evidenceOptions = computed(() => props.automaticRetest
  ? { observation: evidenceKindLabels.observation, remediation: evidenceKindLabels.remediation }
  : evidenceKindLabels)
const sourceSystem = ref('')
const sourceRecord = ref('')
const summary = ref('')
const observedAt = ref(new Date(Date.now() + 8 * 60 * 60 * 1000).toISOString().slice(0, 19))
const file = ref<File | null>(null)
const errorMessage = ref('')
const saving = ref(false)
const maxFileBytes = 8 * 1024

function readFeishuManifest(value: unknown) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
  const fields = ['version', 'targetDigest', 'observedAt', 'present', 'permission']
  const entry = value as Record<string, unknown>
  if (Object.keys(entry).some(key => !fields.includes(key)) || entry.version !== 1
      || typeof entry.targetDigest !== 'string' || !/^[a-f0-9]{64}$/.test(entry.targetDigest)
      || typeof entry.observedAt !== 'string' || !Number.isFinite(Date.parse(entry.observedAt))
      || typeof entry.present !== 'boolean') return undefined
  const allowedPermission = entry.permission === 'view' || entry.permission === 'edit' || entry.permission === 'full_access'
  if (entry.present ? !allowedPermission : entry.permission !== undefined) return undefined
  return {
    version: 1, targetDigest: entry.targetDigest, observedAt: entry.observedAt,
    present: entry.present, permission: entry.permission,
  }
}

function changeMode() {
  errorMessage.value = ''
  file.value = null
}

function selectFile(event: Event) {
  file.value = (event.target as HTMLInputElement).files?.[0] ?? null
  errorMessage.value = file.value && file.value.size > maxFileBytes ? '清单不能超过 8 KiB。' : ''
}

async function save() {
  if (saving.value) return
  saving.value = true
  errorMessage.value = ''
  let payload: unknown
  try {
    if (selected.value === 'feishu') {
      if (!file.value || file.value.size > maxFileBytes) {
        errorMessage.value = '请选择不超过 8 KiB 的飞书权限清单。'
        return
      }
      let raw: unknown
      try {
        raw = JSON.parse(await file.value.text())
      } catch {
        errorMessage.value = '无法读取清单，请选择采集器生成的 JSON 文件。'
        return
      }
      const manifest = readFeishuManifest(raw)
      if (!manifest) {
        errorMessage.value = '清单字段无效，请重新导出飞书权限清单。'
        return
      }
      if (manifest.targetDigest !== props.targetDigest) {
        errorMessage.value = '此清单的核验目标与检查绑定的摘要不一致。'
        return
      }
      payload = manifest
    } else {
      if (props.automaticRetest && kind.value === 'retest') {
        errorMessage.value = '运行时复测由原观测来源自动登记。'
        return
      }
      const time = new Date(observedAt.value + '+08:00')
      if (Number.isNaN(time.valueOf())) {
        errorMessage.value = '请输入有效的北京时间。'
        return
      }
      payload = {
        kind: kind.value, sourceSystem: sourceSystem.value.trim(), sourceRecord: sourceRecord.value.trim(),
        summary: summary.value.trim(), observedAt: time.toISOString(),
      }
    }
    await request(props.apiUrl + (selected.value === 'feishu' ? '/feishu' : '/evidence'), {
      method: 'POST',
      body: JSON.stringify(payload),
    })
    emit('saved', selected.value === 'feishu' ? '已登记飞书权限采集清单。' : '已登记证据。')
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) emit('sessionExpired')
    else errorMessage.value = describeError(error)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <details class="evidence-forms">
    <summary>{{ targetDigest ? '登记证据或导入飞书清单' : '登记证据' }}</summary>
    <form class="evidence-form" @submit.prevent="save">
      <div v-if="targetDigest" class="field">
        <label for="check-evidence-method">证据来源</label>
        <select id="check-evidence-method" v-model="selected" :disabled="saving" @change="changeMode">
          <option value="manual">手工登记</option>
          <option value="feishu">飞书权限清单</option>
        </select>
      </div>
      <template v-if="selected === 'manual'">
        <div class="field">
          <label for="check-evidence-kind">证据类型</label>
          <select id="check-evidence-kind" v-model="kind" required>
            <option v-for="(label, value) in evidenceOptions" :key="value" :value="value">{{ label }}</option>
          </select>
        </div>
        <div class="field"><label for="check-evidence-system">证据来源系统</label><input id="check-evidence-system" v-model="sourceSystem" maxlength="100" required></div>
        <div class="field">
          <label for="check-evidence-ref">原始记录引用</label>
          <input id="check-evidence-ref" v-model="sourceRecord" maxlength="160" aria-describedby="evidence-ref-hint" required>
          <p id="evidence-ref-hint" class="field-hint">填写源系统的记录编号。凭据、链接签名和业务原文留在源系统。</p>
        </div>
        <div class="field">
          <label for="check-evidence-summary">脱敏证据摘要</label>
          <textarea id="check-evidence-summary" v-model="summary" maxlength="600" required></textarea>
        </div>
        <div class="field">
          <label for="check-evidence-time">证据发生时间（北京时间）</label>
          <input id="check-evidence-time" v-model="observedAt" type="datetime-local" step="1" required>
          <p v-if="targetDigest && kind === 'remediation'" class="field-hint">填写实际撤权时间。保存整改记录后，再重新采集飞书权限清单。</p>
          <p v-else-if="automaticRetest && kind === 'remediation'" class="field-hint">填写实际整改生效时间。保存后再使用原观测来源复测。</p>
        </div>
      </template>
      <template v-else>
        <div class="field">
          <label for="check-feishu-file">客户侧采集清单</label>
          <input id="check-feishu-file" type="file" accept=".json,application/json" aria-describedby="feishu-file-hint" :disabled="saving" required @change="selectFile">
          <p id="feishu-file-hint" class="field-hint">采集范围为指定协作者的文档直接授权。链接、继承和群组授权等访问路径需单独核验。</p>
        </div>
      </template>
      <p v-if="errorMessage" class="message message-error form-feedback" role="alert">{{ errorMessage }}</p>
      <div class="form-actions">
        <button type="submit" class="button button-primary" :disabled="saving || (selected === 'feishu' && (!file || file.size > maxFileBytes))">
          {{ saving ? '正在保存…' : selected === 'feishu' ? '导入飞书权限清单' : '保存证据' }}
        </button>
      </div>
    </form>
  </details>
</template>
