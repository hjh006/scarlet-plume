<script setup lang="ts">
import { computed, ref } from 'vue'
import { ApiError, describeError, providerLabel, request, workspacePath, type ImportManifest, type ImportRun } from './api'
import Icon from './Icon.vue'
import EvidencePath from './EvidencePath.vue'

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
const importStages=computed(()=>[
 {label:'选择来源清单',value:selected.value?.name||(result.value?'已完成来源清单':'尚未选择'),tone:selected.value||result.value?'known':'pending'},
 {label:'本地格式校验',value:validated.value?'已通过本地格式校验':result.value?'已校验并提交':'尚未校验',tone:validated.value||result.value?'known':'pending'},
 {label:'确认采用',value:working.value?'正在写入工作区':result.value?result.value.replayed?'历史批次，未重复写入':'已返回导入结果':'等待明确确认',tone:result.value?'known':'pending'},
] as {label:string;value:string;tone:'known'|'pending'}[])

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
        <h2 id="import-title">先核对字段，再确认采用</h2>
        <p>将已授权采集的表目录登记到当前工作区。</p>
      </div>
    </div>
    <div class="import-stage-wrap"><EvidencePath :items="importStages" label="本次采集清单导入阶段" /></div>
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
    </div><section class="import-mapping" aria-label="实际字段与采用预览"><h3><Icon name="templates"/>字段映射校验</h3><template v-if="validated"><p>{{providerLabel(validated.provider)}} · {{validated.source}} · 版本 {{validated.version}}</p><div class="table-scroll" tabindex="0" role="region" aria-label="已校验清单中的目录字段"><table class="data-table"><thead><tr><th>来源引用</th><th>资产名称</th><th>采用身份</th></tr></thead><tbody><tr v-for="entry in validated.assets.slice(0,20)" :key="entry.sourceRef"><td>{{entry.sourceRef}}</td><td>{{entry.name}}</td><td><span class="badge">目录登记</span></td></tr></tbody></table></div><p class="field-hint">清单共 {{validated.assets.length}} 项，预览前 {{Math.min(20,validated.assets.length)}} 项；确认导入提交完整已校验清单。</p></template><template v-else-if="result"><div class="import-result-summary"><Icon name="checks"/><h3>{{result.replayed?'此批次此前已导入':'已收到工作区导入结果'}}</h3><p>{{providerLabel(result.provider)}} · {{result.batchId}}</p><p v-if="!result.replayed">新增 {{result.created}} 项 · 已有 {{result.existing}} 项</p><p>资产目录登记已返回，来源内容与权限仍需授权采集。</p></div></template><div v-else class="import-empty-map"><Icon name="reports"/><h3>选择并校验真实来源清单</h3><p>校验后显示 sourceRef 与 name 的实际映射。没有清单时不生成示例资产或默认通过结果。</p></div><div class="import-mapping-boundary"><strong>采用边界</strong><p>当前清单版本只登记目录标识与名称，不包含内容观察、分类确认或源数据库授权事实。</p></div></section><aside class="import-scope"><h3>导入范围</h3><dl><div><dt>选中文件</dt><dd>{{ selected?.name || '未选择' }}</dd></div><div v-if="selected"><dt>文件大小</dt><dd>{{ (selected.size / 1024).toFixed(1) }} KB</dd></div><template v-if="validated"><div><dt>清单来源</dt><dd>{{ providerLabel(validated.provider) }} · {{ validated.source }}</dd></div><div><dt>批次</dt><dd>{{ validated.batchId }}</dd></div><div><dt>目录条目</dt><dd>{{ validated.assets.length }} 项</dd></div></template></dl><p v-if="validated" class="import-validated" role="status">清单格式校验通过。确认导入后登记资产目录。</p><p v-else>选择清单后先在本地校验格式，再确认写入工作区。</p><p>导入目录仅登记元数据；字段、内容和权限的检查依据由后续授权采集提供。</p></aside></div>
  </section>
</template>

<style scoped>.import-workbench>*{min-width:0}.import-workbench{display:grid;grid-template-columns:minmax(0,1fr) 300px;border-top:1px solid var(--line)}.import-panel-body{padding:24px}.import-panel-body input[type=file]{padding:30px 18px;background:var(--surface-subtle);border:1px dashed var(--input-line);border-radius:9px;width:100%;font-size:12px}.import-actions{display:flex;flex-wrap:wrap;gap:10px;margin-top:20px}.import-scope{padding:24px;border-left:1px solid var(--line);background:var(--surface-subtle)}.import-scope h3{font-size:14px;margin:0 0 20px}.import-scope dl{display:grid;gap:14px}.import-scope dl>div{display:grid;grid-template-columns:68px minmax(0,1fr);gap:8px;font-size:12px}.import-scope dt,.import-scope p{color:var(--muted)}.import-scope dd{margin:0;overflow-wrap:anywhere}.import-scope p{font-size:12px;line-height:1.8}.import-scope .import-validated{color:var(--success);border-left:1px solid var(--success);padding-left:10px}@media(max-width:1400px){.import-workbench{grid-template-columns:minmax(0,1fr) 270px}}</style>

<style scoped>section[aria-labelledby=import-title]{container-type:inline-size}@container(max-width:650px){.import-workbench{grid-template-columns:minmax(0,1fr)}.import-scope{border-left:0;border-top:1px solid var(--line)}.import-panel-body{padding:20px}.import-panel-body input[type=file]{padding:20px 12px}.import-actions{flex-wrap:wrap}.import-actions .button{flex:1;min-width:110px}}</style>

<style scoped>.import-stage-wrap{padding:18px 20px 0}.import-stage-wrap :deep(.evidence-path){margin-bottom:0}.import-workbench{grid-template-columns:260px minmax(0,1fr) 280px;margin-top:18px}.import-panel-body{padding:20px;border-right:1px solid var(--line)}.import-panel-body input[type=file]{font-size:12px;padding:22px 10px}.import-actions{display:grid;grid-template-columns:1fr;gap:10px}.import-mapping{padding:22px;min-width:0}.import-mapping>h3{display:flex;align-items:center;gap:10px;font-size:18px;margin:0 0 18px}.import-mapping>h3 .icon{color:var(--accent);width:26px;height:26px}.import-mapping>p{font-size:12px;line-height:1.8;color:var(--secondary);overflow-wrap:anywhere}.import-mapping .data-table{min-width:360px}.import-mapping .data-table td{overflow-wrap:anywhere;font-size:13px}.import-mapping-boundary{padding:14px;border:1px solid #edd8b1;border-radius:7px;background:#fff6e7;margin-top:20px;font-size:12px;line-height:1.9;color:#80531c}.import-mapping-boundary p{margin:6px 0 0}.import-empty-map{padding:35px 15px;text-align:center;color:var(--secondary);border:1px dashed var(--line);border-radius:8px}.import-empty-map>.icon{width:42px;height:42px;color:var(--accent);margin:0 auto 15px}.import-empty-map h3,.import-result-summary h3{font-size:16px;line-height:1.7}.import-empty-map p,.import-result-summary p{font-size:12px;line-height:1.9;margin:10px 0}.import-result-summary{padding:20px;border:1px solid #bfddcb;background:#f0faf4;border-radius:8px}.import-result-summary>.icon{width:28px;height:28px;color:#2d7849}.import-scope{background:#f6faff}.import-scope h3{font-size:17px}.import-scope dl>div{font-size:13px;line-height:1.7}.import-scope p{padding:12px;border:1px solid var(--line);background:white;border-radius:6px;font-size:12px}@media(max-width:1250px){.import-workbench{grid-template-columns:220px minmax(0,1fr) 240px}.import-mapping{padding:18px}}@media(max-width:1000px){.import-workbench{grid-template-columns:220px minmax(0,1fr)}.import-scope{grid-column:1/-1;border-left:0;border-top:1px solid var(--line)}}@media(max-width:720px){.import-workbench{grid-template-columns:1fr}.import-panel-body{border-right:0;border-bottom:1px solid var(--line)}}
</style>

<style scoped>
section[aria-labelledby=import-title]{grid-column:1/-1;grid-row:1;min-width:0;max-width:100%}
.import-workbench,.import-workbench>*{min-width:0;max-width:100%}
.import-panel-body .field{min-width:0}
.import-panel-body input[type=file]{box-sizing:border-box;width:100%;min-width:0;max-width:100%}
.import-mapping .table-scroll{min-width:0;max-width:100%;overflow-x:auto}
.import-scope dd,.import-mapping td{overflow-wrap:anywhere}
@container(max-width:950px){.import-workbench{grid-template-columns:minmax(0,1fr) minmax(0,1.35fr)}.import-scope{grid-column:1/-1;border-left:0;border-top:1px solid var(--line)}.import-stage-wrap :deep(.evidence-path){gap:12px;padding:12px}.import-stage-wrap :deep(.evidence-path li){padding:10px}.import-stage-wrap :deep(.path-arrow){left:calc(100% + 2px);font-size:15px}}
@container(max-width:650px){.import-workbench{grid-template-columns:minmax(0,1fr)}.import-panel-body{border-right:0;border-bottom:1px solid var(--line)}.import-stage-wrap :deep(.evidence-path){flex-direction:column;gap:8px}.import-stage-wrap :deep(.evidence-path li){width:100%;min-width:0;box-sizing:border-box}.import-stage-wrap :deep(.path-arrow){display:none}.import-stage-wrap :deep(.evidence-path li>div){min-width:0}.import-scope{grid-column:auto}.import-mapping{padding:18px}}
</style>
