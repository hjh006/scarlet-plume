<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import {
  ApiError, assetBusinessLevelLabels, assetCategoryLabels, describeError, formatDate, providerLabel, request,
  workspacePath, type Asset, type Workspace,
} from './api'
import { postgresSensitivityLabel } from './postgresql'
import Icon from './Icon.vue'

interface Candidate {
  name: string; category: string; source: 'postgresql' | 'openapi'; location?: string
  observedAt: string; runId?: string; scanId?: number
}
interface Classification {
  asset: Asset; revision: number; candidates: Candidate[]; candidateCount: number
  suggestedCategory?: string; suggestedBusinessLevel?: string
}
interface Values { category: string; businessLevel: string; statutoryCategory: string; basis: string }
interface History extends Values {
  id: number; revision: number; actor: string; createdAt: string
  previousCategory: string; previousBusinessLevel: string; previousStatutoryCategory: string; previousBasis: string
}
interface Detail extends Classification { history: History[]; historyTruncated?: boolean }
interface Counts { total: number; pending: number; reviewed: number; importantOrCore: number }
interface Page { items: Classification[]; nextCursor?: number | null; counts: Counts }

const props = defineProps<{ workspace: Workspace; assetId?: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const apiPath = workspacePath(props.workspace.slug) + '/classification/assets'
const detailPath = props.assetId ? apiPath + '/' + encodeURIComponent(props.assetId) : ''
const canEdit = computed(() => props.workspace.role === 'owner' || props.workspace.role === 'operator')
const active = new AbortController()
const page = ref<Page | null>(null)
const detail = ref<Detail | null>(null)
const loading = ref(false)
const saving = ref(false)
const conflict = ref(false)
const loadError = ref('')
const saveError = ref('')
const notice = ref('')
const status = ref('')
const statutoryFilter = ref('')
let appliedFilters = ''
const draft = reactive<Values>({ category: 'unknown', businessLevel: 'unknown', statutoryCategory: 'unknown', basis: '' })
const categories: Record<string, string> = { unknown: '待判定', ...assetCategoryLabels }
const levels: Record<string, string> = { unknown: '待判定', ...assetBusinessLevelLabels }
const statutory: Record<string, string> = { unknown: '待认定', general: '一般数据', important: '重要数据', core: '核心数据' }
const locations: Record<string, string> = { parameter: '参数', request: '请求体', response: '响应体' }
const metrics = [
  { key: 'total', label: '资产总数' }, { key: 'pending', label: '待确认' },
  { key: 'reviewed', label: '已确认' }, { key: 'importantOrCore', label: '重要或核心数据' },
] as const

function errorText(error: unknown) {
  if (active.signal.aborted) return ''
  if (error instanceof ApiError && error.status === 401) {
    emit('sessionExpired')
    return ''
  }
  return describeError(error)
}

function setDetail(value: Detail) {
  detail.value = value
  Object.assign(draft, {
    category: value.asset.category || 'unknown',
    businessLevel: value.asset.businessLevel || 'unknown',
    statutoryCategory: value.asset.statutoryCategory || 'unknown',
    basis: value.asset.classificationBasis || '',
  })
}

async function load(append = false) {
  if (loading.value || saving.value) return
  loading.value = true
  loadError.value = ''
  try {
    if (detailPath) {
      setDetail(await request<Detail>(detailPath, { signal: active.signal }))
      saveError.value = ''
      conflict.value = false
    } else {
      const query = new URLSearchParams(append ? appliedFilters : '')
      if (!append) {
        if (status.value) query.set('status', status.value)
        if (statutoryFilter.value) query.set('statutoryCategory', statutoryFilter.value)
      }
      const cursor = page.value?.nextCursor
      if (append && cursor) query.set('after', String(cursor))
      const result = await request<Page>(apiPath + '?' + query, { signal: active.signal })
      page.value = append && page.value ? { ...result, items: [...page.value.items, ...result.items] } : result
      if (!append) appliedFilters = query.toString()
    }
  } catch (error) {
    loadError.value = errorText(error)
  } finally {
    loading.value = false
  }
}

function fillSuggestions() {
  if (!detail.value) return
  if (detail.value.suggestedCategory) draft.category = detail.value.suggestedCategory
  if (detail.value.suggestedBusinessLevel) draft.businessLevel = detail.value.suggestedBusinessLevel
  notice.value = '已填入识别建议。请核对分类并补充判定依据。'
}

async function save() {
  if (!detail.value || !canEdit.value || loading.value || saving.value || conflict.value) return
  saveError.value = ''
  notice.value = ''
  const basis = draft.basis.trim()
  if (!basis || basis.length > 400) {
    saveError.value = '请填写 1 到 400 字的分类判定依据。'
    return
  }
  saving.value = true
  try {
    const updated = await request<Detail>(detailPath, {
      method: 'PUT', body: JSON.stringify({ revision: detail.value.revision, ...draft, basis }), signal: active.signal,
    })
    setDetail(updated)
    notice.value = '分类已确认，变更已登记到历史和平台审计。'
  } catch (error) {
    if (error instanceof ApiError && error.status === 409) {
      conflict.value = true
      saveError.value = '此资产已有更新。读取最新版本后可重新确认。'
    } else saveError.value = errorText(error)
  } finally {
    saving.value = false
  }
}

onMounted(() => { void load() })
onBeforeUnmount(() => active.abort())
</script>

<template>
  <div class="classification-view">
    <a v-if="assetId" class="back-link" :href="home + '/classification'"><Icon name="back" />返回分类台账</a>
    <header class="page-header">
      <div>
        <p class="eyebrow">数据分类分级</p>
        <h1>{{ assetId ? detail?.asset.name || '资产分类' : '数据分类台账' }}</h1>
        <p>{{ assetId ? '核对自动识别线索，记录分类决定和判定依据。' : '查看采集器的字段识别线索、已确认分类和变更历史。' }}</p>
      </div>
      <div class="page-actions">
        <a v-if="assetId && detail?.asset.sourceProvider === 'postgresql'" class="button button-secondary" :href="home + '/assets/' + assetId">查看字段与权限</a>
        <a v-if="!assetId" class="button button-secondary" :href="home + '/assets'">查看数据资产</a>
      </div>
    </header>
    <p v-if="loadError" class="message message-error page-feedback" role="alert">{{ loadError }}</p>
    <p v-if="assetId && loading && !detail" class="empty-state" role="status">正在读取资产分类…</p>
    <button v-if="assetId && loadError" type="button" class="button button-secondary" :disabled="loading" @click="load()">重新读取</button>

    <template v-if="!assetId">
      <dl v-if="page" class="panel report-counts classification-counts" aria-label="工作区分类总览">
        <div v-for="metric in metrics" :key="metric.key"><dt>{{ metric.label }}</dt><dd>{{ page.counts[metric.key] }}</dd></div>
      </dl>
      <section class="panel" aria-labelledby="classification-list-title" :aria-busy="loading">
        <div class="panel-heading">
          <div><h2 id="classification-list-title">资产分类</h2><p>识别建议供经办人核对，法定类别由经办人填写依据并确认。</p></div>
        </div>
        <form class="filter-bar classification-filter" @submit.prevent="load()">
          <label for="classification-filter-status">确认状态</label>
          <select id="classification-filter-status" v-model="status" :disabled="loading">
            <option value="">全部</option><option value="pending">待确认</option><option value="reviewed">已确认</option>
          </select>
          <label for="classification-filter-statutory">法定类别</label>
          <select id="classification-filter-statutory" v-model="statutoryFilter" :disabled="loading">
            <option value="">全部</option><option v-for="(label, value) in statutory" :key="value" :value="value">{{ label }}</option>
          </select>
          <button type="submit" class="button button-secondary" :disabled="loading">{{ loading ? '正在查询…' : '查询' }}</button>
        </form>
        <p v-if="loading && !page" class="empty-state" role="status">正在读取分类台账…</p>
        <div v-else-if="page?.items.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="classification-list-title">
          <table class="data-table classification-table">
            <thead><tr><th scope="col">资产</th><th scope="col">自动识别</th><th scope="col">当前分类</th><th scope="col">法定类别</th><th scope="col">确认状态</th></tr></thead>
            <tbody>
              <tr v-for="item in page.items" :key="item.asset.id">
                <th scope="row">
                  <a class="table-link" :href="home + '/classification/' + item.asset.id">{{ item.asset.name }}</a>
                  <span class="table-secondary">{{ item.asset.system }}</span>
                </th>
                <td>
                  {{ item.candidateCount ? item.candidateCount + ' 项线索' : '暂无线索' }}
                  <span v-if="item.suggestedCategory" class="table-secondary">建议 {{ categories[item.suggestedCategory] }}</span>
                </td>
                <td>{{ categories[item.asset.category || 'unknown'] }}<span class="table-secondary">{{ levels[item.asset.businessLevel || 'unknown'] }}</span></td>
                <td>{{ statutory[item.asset.statutoryCategory || 'unknown'] }}</td>
                <td>
                  <span class="badge" :class="item.revision ? 'status-verified' : 'status-draft'">{{ item.revision ? '已确认' : '待确认' }}</span>
                  <span v-if="item.revision" class="table-secondary">版本 {{ item.revision }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else-if="page && !loading" class="empty-state">暂无符合条件的资产。</p>
        <div v-if="page?.nextCursor != null" class="panel-footer">
          <button type="button" class="button button-secondary" :disabled="loading" @click="load(true)">加载更多资产</button>
        </div>
      </section>
    </template>

    <div v-else-if="detail" class="classification-detail">
      <section class="panel" aria-labelledby="classification-current-title">
        <div class="panel-heading">
          <div><h2 id="classification-current-title">当前分类</h2><p>{{ detail.asset.system }}<template v-if="detail.asset.sourceProvider"> · {{ providerLabel(detail.asset.sourceProvider) }}</template></p></div>
          <span class="badge" :class="detail.revision ? 'status-verified' : 'status-draft'">
            {{ detail.revision ? '已确认 · 版本 ' + detail.revision : '待确认' }}
          </span>
        </div>
        <dl class="detail-list">
          <div><dt>数据属性</dt><dd>{{ categories[detail.asset.category || 'unknown'] }}</dd></div>
          <div><dt>企业内部敏感级别</dt><dd>{{ levels[detail.asset.businessLevel || 'unknown'] }}</dd></div>
          <div><dt>法定类别</dt><dd>{{ statutory[detail.asset.statutoryCategory || 'unknown'] }}</dd></div>
          <div class="detail-wide"><dt>判定依据</dt><dd class="text-block">{{ detail.asset.classificationBasis || '—' }}</dd></div>
        </dl>
        <p v-if="!detail.revision" class="field-hint classification-note">已有分类值可来自资产历史记录。完成本页确认后，平台开始记录分类版本。</p>
      </section>

      <section class="panel" aria-labelledby="classification-candidates-title">
        <div class="panel-heading">
          <div>
            <h2 id="classification-candidates-title">自动识别线索</h2>
            <p>基于 PostgreSQL 字段元数据和 OpenAPI 契约识别。当前共有 {{ detail.candidateCount }} 项线索。</p>
          </div>
        </div>
        <dl v-if="detail.suggestedCategory || detail.suggestedBusinessLevel" class="detail-list">
          <div v-if="detail.suggestedCategory"><dt>建议数据属性</dt><dd>{{ categories[detail.suggestedCategory] }}</dd></div>
          <div v-if="detail.suggestedBusinessLevel"><dt>建议内部敏感级别</dt><dd>{{ levels[detail.suggestedBusinessLevel] }}</dd></div>
        </dl>
        <p v-if="detail.candidateCount > detail.candidates.length" class="field-hint classification-note">当前展示 {{ detail.candidates.length }} 项，可到对应来源核对其余线索。</p>
        <div v-if="detail.candidates.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="classification-candidates-title">
          <table class="data-table classification-candidates">
            <thead><tr><th scope="col">字段或参数</th><th scope="col">识别线索</th><th scope="col">来源</th><th scope="col">观测时间</th></tr></thead>
            <tbody>
              <tr v-for="(candidate, index) in detail.candidates" :key="index">
                <th scope="row" class="mono">{{ candidate.name }}</th>
                <td>{{ postgresSensitivityLabel(candidate.category) }}</td>
                <td>
                  <a v-if="candidate.scanId" class="table-link" :href="home + '/api-security/' + candidate.scanId">OpenAPI 扫描 #{{ candidate.scanId }}</a>
                  <span v-else>{{ providerLabel(candidate.source) }}</span>
                  <span v-if="candidate.location" class="table-secondary">{{ locations[candidate.location] || candidate.location }}</span>
                  <span v-if="candidate.runId" class="table-secondary mono">{{ candidate.runId }}</span>
                </td>
                <td><time :datetime="candidate.observedAt">{{ formatDate(candidate.observedAt) }}</time></td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">此资产暂无自动识别线索，可结合实际业务数据和分类依据确认。</p>
      </section>

      <section v-if="canEdit" class="panel" aria-labelledby="classification-form-title">
        <div class="panel-heading">
          <div><h2 id="classification-form-title">确认分类</h2><p>依据组织采用的分类规则填写。保存后会记录经办人、依据和版本。</p></div>
          <button v-if="detail.suggestedCategory || detail.suggestedBusinessLevel" type="button" class="button button-secondary"
                  :disabled="loading || saving || conflict" @click="fillSuggestions">填入识别建议</button>
        </div>
        <form class="classification-form" @submit.prevent="save">
          <fieldset class="classification-fields" :disabled="loading || saving || conflict">
            <div class="field">
              <label for="classification-category">数据属性</label>
              <select id="classification-category" v-model="draft.category"><option v-for="(label, value) in categories" :key="value" :value="value">{{ label }}</option></select>
            </div>
            <div class="field">
              <label for="classification-level">企业内部敏感级别</label>
              <select id="classification-level" v-model="draft.businessLevel"><option v-for="(label, value) in levels" :key="value" :value="value">{{ label }}</option></select>
            </div>
            <div class="field">
              <label for="classification-statutory">法定类别</label>
              <select id="classification-statutory" v-model="draft.statutoryCategory"><option v-for="(label, value) in statutory" :key="value" :value="value">{{ label }}</option></select>
            </div>
          </fieldset>
          <div class="field">
            <label for="classification-basis">判定依据</label>
            <textarea id="classification-basis" v-model="draft.basis" required maxlength="400" :disabled="loading || saving || conflict" aria-describedby="classification-basis-hint"></textarea>
            <p id="classification-basis-hint" class="field-hint">填写规则编号、审批记录或脱敏判定摘要。</p>
          </div>
          <p v-if="notice" class="message message-success classification-feedback" role="status">{{ notice }}</p>
          <p v-if="saveError" class="message message-error classification-feedback" role="alert">{{ saveError }}</p>
          <div class="form-actions">
            <button type="submit" class="button button-primary" :disabled="loading || saving || conflict">{{ saving ? '正在保存…' : '保存分类' }}</button>
            <button v-if="conflict" type="button" class="button button-secondary" :disabled="loading" @click="load()">读取最新版本</button>
          </div>
        </form>
      </section>

      <section class="panel" aria-labelledby="classification-history-title">
        <div class="panel-heading"><div><h2 id="classification-history-title">分类变更历史</h2><p>按版本追溯经办人和判定依据。</p></div></div>
        <p v-if="detail.historyTruncated" class="field-hint classification-note">这里展示最近的分类版本。</p>
        <ol v-if="detail.history.length" class="timeline">
          <li v-for="entry in detail.history" :key="entry.id">
            <strong>版本 {{ entry.revision }}</strong>
            <div class="event-meta">{{ entry.actor }} · <time :datetime="entry.createdAt">{{ formatDate(entry.createdAt) }}</time></div>
            <p>{{ categories[entry.category] }} · {{ levels[entry.businessLevel] }} · {{ statutory[entry.statutoryCategory] }}</p>
            <p class="text-block">{{ entry.basis }}</p>
            <details v-if="entry.previousCategory || entry.previousBusinessLevel || entry.previousStatutoryCategory || entry.previousBasis">
              <summary class="table-link">查看变更前记录</summary>
              <p>{{ categories[entry.previousCategory || 'unknown'] }} · {{ levels[entry.previousBusinessLevel || 'unknown'] }} · {{ statutory[entry.previousStatutoryCategory || 'unknown'] }}</p>
              <p class="text-block">{{ entry.previousBasis || '—' }}</p>
            </details>
          </li>
        </ol>
        <p v-else class="empty-state">尚未登记分类确认版本。</p>
      </section>
    </div>
  </div>
</template>

<style scoped>
.classification-counts { margin-bottom: 1rem; }
.classification-counts div { border-bottom: 0; }
.classification-filter select { min-width: 0; }
.classification-detail { display: grid; gap: 1rem; }
.classification-note { margin: .7rem 1.1rem; }
.classification-table { min-width: 35rem; }
.classification-candidates { min-width: 33rem; }
.classification-table :is(th, td), .classification-candidates :is(th, td), .classification-view .text-block { overflow-wrap: anywhere; }
.classification-form { padding: .9rem 1.1rem 1.1rem; }
.classification-fields { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: .75rem; min-width: 0; margin: 0; padding: 0; border: 0; }
.classification-fields .field { min-width: 0; margin: 0; }
.classification-feedback { margin-top: .75rem; }
.classification-detail summary { cursor: pointer; }
@media (max-width: 43.99rem) {
  .classification-fields { grid-template-columns: minmax(0, 1fr); }
}
</style>
