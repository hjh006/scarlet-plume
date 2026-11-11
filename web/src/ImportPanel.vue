<script setup lang="ts">
import { ref } from 'vue'
import { ApiError, describeError, providerLabel, request, workspacePath, type ImportManifest, type ImportRun } from './api'
import Icon from './Icon.vue'

const props = defineProps<{ slug: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const maxBytes = 512 * 1024
const selected = ref<File | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)
const working = ref(false)
const validating = ref(false)
const errorMessage = ref('')
const result = ref<ImportRun | null>(null)
const validated = ref<ImportManifest | null>(null)

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function text(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0
}

function parseManifest(value: unknown): ImportManifest | undefined {
  if (!record(value) || Object.keys(value).length !== 5 || value.version !== 1
      || (value.provider !== 'postgresql' && value.provider !== 'openmetadata')
      || !text(value.source) || !text(value.batchId)
      || !Array.isArray(value.assets)) return undefined
  const assets = []
  for (const entry of value.assets) {
    if (!record(entry) || Object.keys(entry).length !== 2 || !text(entry.sourceRef) || !text(entry.name)) return undefined
    assets.push({ sourceRef: entry.sourceRef, name: entry.name })
  }
  return { version: 1, provider: value.provider, source: value.source, batchId: value.batchId, assets }
}

function chooseFile(event: Event) {
  selected.value = (event.target as HTMLInputElement).files?.[0] || null
  errorMessage.value = selected.value && selected.value.size > maxBytes ? '文件不能超过 512 KB。' : ''
  result.value = null
  validated.value = null
}

async function validateFile() {
  if (working.value || validating.value || !selected.value || selected.value.size > maxBytes) return false
  const file = selected.value
  validating.value = true
  errorMessage.value = ''
  validated.value = null
  try {
    const manifest = parseManifest(JSON.parse(await file.text()))
    if (selected.value !== file) return false
    if (!manifest) { errorMessage.value = '清单格式不匹配，请使用 PostgreSQL 或 OpenMetadata 采集器生成的第 1 版清单。'; return false }
    validated.value = manifest
    return true
  } catch { errorMessage.value = '无法读取有效的 JSON，请选择采集器生成的清单。'; return false } finally { validating.value = false }
}
async function submitImport() {
  if (working.value || !validated.value) return
  working.value = true
  errorMessage.value = ''
  result.value = null
  try {
    result.value = await request<ImportRun>(workspacePath(props.slug) + '/imports', { method: 'POST', body: JSON.stringify(validated.value) })
    selected.value = null
    validated.value = null
    if (fileInput.value) fileInput.value.value = ''
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) emit('sessionExpired')
    else errorMessage.value = describeError(error)
  } finally { working.value = false }
}
</script>

<template>
  <section class="panel" aria-labelledby="import-title">
    <div class="panel-heading">
      <div>
        <h2 id="import-title">导入采集清单</h2>
        <p>将已授权采集的表目录登记到当前工作区。</p>
      </div>
    </div>
    <div class="import-workbench"><div class="import-panel-body">
      <form @submit.prevent="submitImport">
        <div class="field">
          <label for="manifest-file">JSON 清单</label>
          <input id="manifest-file" ref="fileInput" type="file" accept=".json,application/json"
                 aria-describedby="manifest-hint" :disabled="working || validating" required @change="chooseFile">
          <p id="manifest-hint" class="field-hint">选择采集器生成的 PostgreSQL 或 OpenMetadata 清单，文件上限 512 KB。</p>
        </div>
        <p v-if="errorMessage" class="message message-error page-feedback" role="alert">{{ errorMessage }}</p>
        <div class="import-actions"><button class="button button-secondary" type="button" :disabled="working || validating || !selected || selected.size > maxBytes" @click="validateFile">{{ validating ? '正在校验…' : '校验清单' }}</button><button class="button button-primary" type="submit" :disabled="working || validating || !validated">
          <Icon name="upload" />{{ working ? '导入中…' : '确认导入' }}
        </button></div>
      </form>
      <p v-if="result" class="message message-success page-feedback" role="status">
        {{ providerLabel(result.provider) }} · 批次 {{ result.batchId }}
        <template v-if="result.replayed">此前已导入，本次未重复写入。</template>
        <template v-else>已处理，新增 {{ result.created }} 项，已有 {{ result.existing }} 项。</template>
      </p>
    </div><aside class="import-scope"><h3>导入范围</h3><dl><div><dt>选中文件</dt><dd>{{ selected?.name || '未选择' }}</dd></div><div v-if="selected"><dt>文件大小</dt><dd>{{ (selected.size / 1024).toFixed(1) }} KB</dd></div><template v-if="validated"><div><dt>清单来源</dt><dd>{{ providerLabel(validated.provider) }} · {{ validated.source }}</dd></div><div><dt>批次</dt><dd>{{ validated.batchId }}</dd></div><div><dt>目录条目</dt><dd>{{ validated.assets.length }} 项</dd></div></template></dl><p v-if="validated" class="import-validated" role="status">清单格式校验通过。确认导入后登记资产目录。</p><p v-else>选择清单后先在本地校验格式，再确认写入工作区。</p><p>导入目录仅登记元数据；字段、内容和权限的检查依据由后续授权采集提供。</p></aside></div>
  </section>
</template>

<style scoped>.import-workbench{display:grid;grid-template-columns:minmax(0,1fr) 300px;border-top:1px solid var(--line)}.import-panel-body{padding:24px}.import-panel-body input[type=file]{padding:30px 18px;background:#f8fbff;border:1px dashed #b1c9dd;border-radius:9px;width:100%;font-size:12px}.import-actions{display:flex;gap:10px;margin-top:20px}.import-scope{padding:24px;border-left:1px solid var(--line);background:#fcfdff}.import-scope h3{font-size:14px;margin:0 0 20px}.import-scope dl{display:grid;gap:14px}.import-scope dl>div{display:grid;grid-template-columns:68px minmax(0,1fr);gap:8px;font-size:12px}.import-scope dt,.import-scope p{color:var(--muted)}.import-scope dd{margin:0;overflow-wrap:anywhere}.import-scope p{font-size:12px;line-height:1.8}.import-scope .import-validated{color:#007c75;border-left:2px solid #00889d;padding-left:10px}@media(max-width:1400px){.import-workbench{grid-template-columns:minmax(0,1fr) 270px}}</style>

<style scoped>section[aria-labelledby=import-title]{container-type:inline-size}@container(max-width:650px){.import-workbench{grid-template-columns:minmax(0,1fr)}.import-scope{border-left:0;border-top:1px solid var(--line)}.import-panel-body{padding:20px}.import-panel-body input[type=file]{padding:20px 12px}.import-actions{flex-wrap:wrap}.import-actions .button{flex:1;min-width:110px}}</style>
