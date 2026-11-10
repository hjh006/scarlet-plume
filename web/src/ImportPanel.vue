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
const errorMessage = ref('')
const result = ref<ImportRun | null>(null)

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
}

async function submitImport() {
  if (working.value || !selected.value || selected.value.size > maxBytes) return
  working.value = true
  errorMessage.value = ''
  result.value = null
  try {
    let parsed: unknown
    try {
      parsed = JSON.parse(await selected.value.text())
    } catch {
      errorMessage.value = '无法读取有效的 JSON，请选择采集器生成的清单。'
      return
    }
    const manifest = parseManifest(parsed)
    if (!manifest) {
      errorMessage.value = '清单格式不匹配，请使用 PostgreSQL 或 OpenMetadata 采集器生成的第 1 版清单。'
      return
    }
    result.value = await request<ImportRun>(workspacePath(props.slug) + '/imports', {
      method: 'POST',
      body: JSON.stringify(manifest),
    })
    selected.value = null
    if (fileInput.value) fileInput.value.value = ''
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) emit('sessionExpired')
    else errorMessage.value = describeError(error)
  } finally {
    working.value = false
  }
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
    <div class="import-panel-body">
      <form @submit.prevent="submitImport">
        <div class="field">
          <label for="manifest-file">JSON 清单</label>
          <input id="manifest-file" ref="fileInput" type="file" accept=".json,application/json"
                 aria-describedby="manifest-hint" :disabled="working" required @change="chooseFile">
          <p id="manifest-hint" class="field-hint">选择采集器生成的 PostgreSQL 或 OpenMetadata 清单，文件上限 512 KB。</p>
        </div>
        <p v-if="errorMessage" class="message message-error page-feedback" role="alert">{{ errorMessage }}</p>
        <button class="button button-primary" type="submit" :disabled="working || !selected || selected.size > maxBytes">
          <Icon name="upload" />{{ working ? '导入中…' : '导入清单' }}
        </button>
      </form>
      <p v-if="result" class="message message-success page-feedback" role="status">
        {{ providerLabel(result.provider) }} · 批次 {{ result.batchId }}
        <template v-if="result.replayed">此前已导入，本次未重复写入。</template>
        <template v-else>已处理，新增 {{ result.created }} 项，已有 {{ result.existing }} 项。</template>
      </p>
    </div>
  </section>
</template>
