<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { activityKindLabels } from './activity'
import { ApiError, describeError, request, workspacePath, type Workspace } from './api'
import { templateKinds, type CheckTemplate, type TemplateItem, type TemplateKind } from './template'
import type { Requirement } from './workflow'
import Icon from './Icon.vue'

const props = defineProps<{ workspace: Workspace; sourceId?: string; revise: boolean }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const apiUrl = workspacePath(props.workspace.slug)
const pageUrl = `#/${encodeURIComponent(props.workspace.slug)}/templates`
const backUrl = props.sourceId ? pageUrl + '/' + encodeURIComponent(props.sourceId) : pageUrl
const source = ref<CheckTemplate | null>(null)
const kind = ref<TemplateKind>('sharing')
const templateKey = ref('')
const title = ref('')
const items = ref<(TemplateItem & { rowId: number })[]>([])
const requirements = ref<Requirement[]>([])
const loading = ref(true)
const saving = ref(false)
const loadError = ref('')
const formError = ref('')
const active = new AbortController()
let nextRowId = 0

function errorText(error: unknown) {
  if (active.signal.aborted) return ''
  if (error instanceof ApiError && error.status === 401) {
    emit('sessionExpired')
    return ''
  }
  return describeError(error)
}

function addItem(focus = false) {
  if (items.value.length >= 25) return
  const rowId = ++nextRowId
  let number = rowId
  while (items.value.some(item => item.key === 'item-' + number)) number++
  items.value.push({ rowId, key: 'item-' + number, title: '', requirementCode: '', applicability: '', evidenceRequirements: '', acceptanceCriteria: '' })
  if (focus) void nextTick(() => document.getElementById('item-title-' + rowId)?.focus())
}

function removeItem(index: number) {
  if (items.value.length <= 1) return
  items.value.splice(index, 1)
  const row = items.value[Math.min(index, items.value.length - 1)]
  void nextTick(() => document.getElementById('item-title-' + row.rowId)?.focus())
}

async function load() {
  if (props.workspace.role !== 'owner') return
  loading.value = true
  loadError.value = ''
  try {
    const options = { signal: active.signal }
    const [rules, existing] = await Promise.all([
      request<{ items: Requirement[] }>(apiUrl + '/requirements', options),
      props.sourceId
        ? request<CheckTemplate>(apiUrl + '/templates/' + encodeURIComponent(props.sourceId), options)
        : undefined,
    ])
    if (active.signal.aborted) return
    if (props.revise && (!existing || existing.builtIn)) {
      loadError.value = '只有工作区模板可以发布新版本。'
      return
    }
    requirements.value = rules.items
    if (existing) {
      source.value = existing
      kind.value = existing.kind
      templateKey.value = props.revise ? existing.key : ''
      title.value = existing.title
      items.value = existing.items.map(item => ({ ...item, rowId: ++nextRowId }))
    } else if (!items.value.length) addItem()
  } catch (error) {
    loadError.value = errorText(error)
  } finally {
    loading.value = false
  }
}

async function publish() {
  if (saving.value || loading.value || props.workspace.role !== 'owner') return
  formError.value = ''
  const keys = items.value.map(item => item.key.trim())
  if (new Set(keys).size !== keys.length) {
    formError.value = '同一版本内，每个检查项的标识必须唯一。'
    return
  }
  saving.value = true
  try {
    const result = await request<CheckTemplate>(apiUrl + '/templates', {
      method: 'POST',
      body: JSON.stringify({
        key: templateKey.value.trim(), title: title.value.trim(), kind: kind.value,
        baseRevisionId: props.revise ? source.value?.id : undefined,
        items: items.value.map(item => ({
          key: item.key.trim(), title: item.title.trim(), requirementCode: item.requirementCode,
          applicability: item.applicability.trim(), evidenceRequirements: item.evidenceRequirements.trim(),
          acceptanceCriteria: item.acceptanceCriteria.trim(),
        })),
      }),
    })
    if (!active.signal.aborted) window.location.hash = pageUrl + '/' + result.id
  } catch (error) {
    formError.value = errorText(error)
  } finally {
    saving.value = false
  }
}

onMounted(() => { void load() })
onBeforeUnmount(() => active.abort())
</script>

<template>
  <a class="back-link" :href="backUrl"><Icon name="back" />返回检查模板</a>
  <header class="page-header">
    <div>
      <h1>{{ revise ? '发布模板新版本' : '新建检查模板' }}</h1>
      <p v-if="revise && source">基于{{ source.title }}的版本 {{ source.version }}。既有检查继续保留原版本。</p>
      <p v-else>按活动类型配置检查项，明确适用条件、取证要求和验收标准。</p>
    </div>
  </header>
  <p v-if="workspace.role !== 'owner'" class="message message-warning" role="alert">仅工作区负责人可以发布模板。</p>
  <p v-else-if="loading" class="message" role="status">正在读取模板和监管要求…</p>
  <div v-else-if="loadError" class="message message-error page-feedback">
    <p role="alert">{{ loadError }}</p>
    <button type="button" class="button button-secondary" @click="load">重新读取</button>
  </div>
  <form v-else class="panel form-panel template-form" @submit.prevent="publish">
    <div class="field">
      <label for="template-kind">活动类型</label>
      <select id="template-kind" v-model="kind" :disabled="revise || saving">
        <option v-for="entry in templateKinds" :key="entry" :value="entry">{{ activityKindLabels[entry] }}</option>
      </select>
    </div>
    <div class="field">
      <label for="template-key">模板标识</label>
      <input id="template-key" v-model="templateKey" pattern="[a-z0-9][a-z0-9_\-]{0,59}" maxlength="60" spellcheck="false"
             :readonly="revise" required aria-describedby="template-key-hint">
      <p id="template-key-hint" class="field-hint">使用小写字母、数字、连字符或下划线，最多 60 个字符。</p>
    </div>
    <div class="field">
      <label for="template-title">模板名称</label>
      <input id="template-title" v-model="title" maxlength="100" required>
    </div>
    <fieldset v-for="(item, index) in items" :key="item.rowId" class="template-fieldset">
      <legend>检查项 {{ index + 1 }}</legend>
      <div class="field">
        <label :for="'item-title-' + item.rowId">检查项名称</label>
        <input :id="'item-title-' + item.rowId" v-model="item.title" maxlength="100" required>
      </div>
      <div class="template-field-pair">
        <div class="field">
          <label :for="'item-key-' + item.rowId">检查项标识</label>
          <input :id="'item-key-' + item.rowId" v-model="item.key" pattern="[a-z0-9][a-z0-9_\-]{0,59}"
                 maxlength="60" spellcheck="false" required>
        </div>
        <div class="field">
          <label :for="'item-rule-' + item.rowId">关联监管要求</label>
          <select :id="'item-rule-' + item.rowId" v-model="item.requirementCode" required>
            <option value="" disabled>选择监管要求</option>
            <option v-for="rule in requirements" :key="rule.code" :value="rule.code">{{ rule.code }} · {{ rule.title }}</option>
          </select>
        </div>
      </div>
      <div class="field">
        <label :for="'item-applicability-' + item.rowId">适用条件</label>
        <textarea :id="'item-applicability-' + item.rowId" v-model="item.applicability" maxlength="1000" required></textarea>
      </div>
      <div class="field">
        <label :for="'item-evidence-' + item.rowId">取证要求</label>
        <textarea :id="'item-evidence-' + item.rowId" v-model="item.evidenceRequirements" maxlength="1000" required></textarea>
      </div>
      <div class="field">
        <label :for="'item-acceptance-' + item.rowId">验收标准</label>
        <textarea :id="'item-acceptance-' + item.rowId" v-model="item.acceptanceCriteria" maxlength="1000" required></textarea>
      </div>
      <button v-if="items.length > 1" class="button button-secondary" type="button" :disabled="saving"
              :aria-label="'移除检查项 ' + (index + 1)" @click="removeItem(index)">移除此项</button>
    </fieldset>
    <button class="button button-secondary" type="button" :disabled="saving || items.length >= 25" @click="addItem(true)">
      <Icon name="plus" />添加检查项
    </button>
    <p v-if="items.length >= 25" class="field-hint">每个版本最多包含 25 个检查项。</p>
    <p v-if="formError" class="message message-error form-feedback" role="alert">{{ formError }}</p>
    <div class="form-actions">
      <button class="button button-primary" type="submit" :disabled="saving">{{ saving ? '正在发布…' : '发布模板版本' }}</button>
      <a class="button button-secondary" :href="backUrl">取消</a>
    </div>
  </form>
</template>
