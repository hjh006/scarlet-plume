<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, documentationUrl, request, workspacePath, type Workspace } from './api'
import { isReferencePublication, standardKindLabels, standardStatusLabels, standardsByIndustry, type ClassificationStandards } from './classification-standards'
import Icon from './Icon.vue'

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const endpoint = workspacePath(props.workspace.slug) + '/classification/standards'
const owner = computed(() => props.workspace.role === 'owner')
const catalog = ref<ClassificationStandards>()
const industry = ref('')
const kind = ref('')
const chosen = ref('')
const loading = ref(true)
const saving = ref(false)
const loadError = ref('')
const saveError = ref('')
const notice = ref('')
const active = new AbortController()
const industries = computed(() => [...new Set(catalog.value?.items.map(standard => standard.industry) ?? [])])
const visible = computed(() => catalog.value?.items.filter(standard =>
  (!industry.value || standard.industry === industry.value) && (!kind.value || standard.kind === kind.value),
) ?? [])
const selectable = computed(() => catalog.value?.items.filter(standard => standard.status === 'effective') ?? [])
const selectableGroups = computed(() => standardsByIndustry(selectable.value))
const current = computed(() => catalog.value?.items.find(standard => standard.id === catalog.value?.defaultStandardId))

function errorText(error: unknown) {
  if (active.signal.aborted) return ''
  if (error instanceof ApiError && error.status === 401) {
    emit('sessionExpired')
    return ''
  }
  return describeError(error)
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    catalog.value = await request<ClassificationStandards>(endpoint, { signal: active.signal })
    chosen.value = selectable.value.some(standard => standard.id === catalog.value?.defaultStandardId)
      ? catalog.value.defaultStandardId
      : ''
  } catch (error) {
    loadError.value = errorText(error)
  } finally {
    loading.value = false
  }
}

async function saveDefault() {
  if (!owner.value || !catalog.value || saving.value || !selectable.value.some(standard => standard.id === chosen.value)) return
  saving.value = true
  saveError.value = ''
  notice.value = ''
  try {
    const updated = await request<{ defaultStandardId: string }>(endpoint + '/default', {
      method: 'PUT',
      body: JSON.stringify({ standardId: chosen.value }),
      signal: active.signal,
    })
    catalog.value = { ...catalog.value, defaultStandardId: updated.defaultStandardId }
    chosen.value = updated.defaultStandardId
    notice.value = '工作区默认标准已更新。'
  } catch (error) {
    saveError.value = errorText(error)
  } finally {
    saving.value = false
  }
}

onMounted(() => { void load() })
onBeforeUnmount(() => active.abort())
</script>

<template>
  <div class="standards-view">
    <a class="back-link" :href="home + '/classification'"><Icon name="back" />返回分类台账</a>
    <header class="page-header">
      <div>
        <h1>分类分级标准库</h1>
        <p>按行业和类型查阅内置标准，核对适用范围、实施日期、等级和规则依据。</p>
      </div>
    </header>
    <p v-if="loading" class="message" role="status">正在读取标准库…</p>
    <div v-else-if="loadError" class="message message-error page-feedback">
      <p role="alert">{{ loadError }}</p>
      <button type="button" class="button button-secondary" @click="load">重新读取</button>
    </div>
    <template v-else-if="catalog">
      <section class="panel" aria-labelledby="standard-default-title">
        <div class="panel-heading">
          <div><h2 id="standard-default-title">工作区默认标准</h2><p>为尚未选定标准的资产预选此版本。确认分类时可按资产选择。</p></div>
        </div>
        <form v-if="owner" class="standards-default" @submit.prevent="saveDefault">
          <div class="field">
            <label for="classification-default-standard">默认标准</label>
            <select id="classification-default-standard" v-model="chosen" required :disabled="saving || !selectable.length">
              <option value="" disabled>请选择现行标准</option>
              <optgroup v-for="[domain, standards] in selectableGroups" :key="domain" :label="domain">
                <option v-for="standard in standards" :key="standard.id" :value="standard.id">{{ standard.code }} · {{ standard.title }}</option>
              </optgroup>
            </select>
            <p v-if="current && current.status !== 'effective'" class="field-hint">当前默认版本为 {{ current.code }}，请选择现行版本。</p>
          </div>
          <button type="submit" class="button button-primary" :disabled="saving || !chosen || chosen === catalog.defaultStandardId">
            {{ saving ? '正在保存…' : '保存默认标准' }}
          </button>
          <p v-if="notice" class="message message-success standards-feedback" role="status">{{ notice }}</p>
          <p v-if="saveError" class="message message-error standards-feedback" role="alert">{{ saveError }}</p>
        </form>
        <p v-else class="standards-default-name">{{ current ? current.code + ' · ' + current.title : '尚未设置工作区默认标准' }}</p>
      </section>

      <section class="panel standards-catalog" aria-labelledby="standards-catalog-title">
        <div class="panel-heading">
          <div>
            <h2 id="standards-catalog-title">内置标准</h2>
            <p v-if="catalog.catalogAsOf">核验日期 <time :datetime="catalog.catalogAsOf">{{ catalog.catalogAsOf }}</time></p>
          </div>
          <span class="field-hint">{{ visible.length }} 项</span>
        </div>
        <div class="filter-bar standards-filters">
          <label for="standard-industry-filter">行业领域</label>
          <select id="standard-industry-filter" v-model="industry">
            <option value="">全部领域</option><option v-for="value in industries" :key="value" :value="value">{{ value }}</option>
          </select>
          <label for="standard-kind-filter">标准类型</label>
          <select id="standard-kind-filter" v-model="kind">
            <option value="">全部类型</option><option v-for="(label, value) in standardKindLabels" :key="value" :value="value">{{ label }}</option>
          </select>
        </div>
        <ul v-if="visible.length" class="standards-list">
          <li v-for="standard in visible" :key="standard.id">
            <article>
              <div class="standard-card-heading">
                <div><p class="standard-code mono">{{ standard.code }}</p><h3>{{ standard.title }}</h3></div>
                <div class="standard-badges">
                  <span class="badge" :class="standard.status === 'effective' ? 'status-verified' : 'status-draft'">{{ standardStatusLabels[standard.status] }}</span>
                  <span v-if="isReferencePublication(standard)" class="badge status-draft">发布资料，仅供参考</span>
                  <span v-if="standard.id === catalog.defaultStandardId" class="badge status-in_review">工作区默认</span>
                </div>
              </div>
              <p class="standard-meta">
                {{ standard.industry }} · {{ standardKindLabels[standard.kind] }}
                <template v-if="standard.publishedOn"> · 发布 <time :datetime="standard.publishedOn">{{ standard.publishedOn }}</time></template>
                <template v-if="standard.effectiveOn"> · 实施 <time :datetime="standard.effectiveOn">{{ standard.effectiveOn }}</time></template>
              </p>
              <p>{{ standard.scope }}</p>
              <p class="standard-meta">{{ standard.authority }}</p>
              <a v-if="documentationUrl(standard.url)" class="table-link standard-source"
                 :href="documentationUrl(standard.url)" target="_blank" rel="noopener noreferrer">查看官方来源 <Icon name="external" /></a>
              <details v-if="standard.levels?.length || standard.rules?.length" class="standard-rule-details">
                <summary>等级与规则 · {{ standard.levels?.length ?? 0 }} 个等级 · {{ standard.rules?.length ?? 0 }} 条规则</summary>
                <p v-if="standard.levels?.length">标准等级 {{ standard.levels.map(level => level.label).join('、') }}</p>
                <dl v-if="standard.rules?.length">
                  <div v-for="rule in standard.rules" :key="rule.id">
                    <dt>{{ rule.clause }} · {{ rule.title }}</dt>
                    <dd>{{ rule.criterion }}</dd>
                    <dd v-if="rule.evidence" class="field-hint">取证要求 {{ rule.evidence }}</dd>
                  </div>
                </dl>
              </details>
              <p v-if="!standard.rules?.length" class="field-hint">已收录目录信息，条款规则待补充。</p>
            </article>
          </li>
        </ul>
        <p v-else class="empty-state">暂无符合筛选条件的标准。</p>
      </section>
    </template>
  </div>
</template>

<style scoped>
.standards-catalog { margin-top: 1rem; }
.standards-default { display: flex; flex-wrap: wrap; align-items: end; gap: .65rem; padding: .85rem 1.1rem; }
.standards-default .field { flex: 1 1 19rem; min-width: 0; max-width: 38rem; margin: 0; }
.standards-default .standards-feedback { flex-basis: 100%; }
.standards-default-name { padding: .85rem 1.1rem; }
.standards-filters select { min-width: 0; }
.standards-list { margin: 0; padding: 0; list-style: none; }
.standards-list li { padding: 1rem 1.1rem; }
.standards-list li + li { border-top: 1px solid var(--line); }
.standard-card-heading { display: flex; flex-wrap: wrap; align-items: start; justify-content: space-between; gap: .5rem 1rem; }
.standard-card-heading h3 { font-size: 1.02rem; }
.standard-code { margin-bottom: .22rem; color: var(--accent); font-size: .8rem; font-weight: 700; }
.standard-badges { display: flex; flex-wrap: wrap; gap: .35rem; }
.standard-meta { margin-block: .42rem; color: var(--secondary); font-size: .82rem; }
.standard-source { display: inline-flex; align-items: center; gap: .25rem; padding-block: .3rem; }
.standard-source .icon { inline-size: .9rem; block-size: .9rem; }
.standard-rule-details { margin-top: .45rem; padding: .55rem .75rem; border-radius: .4rem; background: var(--surface-subtle); }
.standard-rule-details summary { color: var(--accent); font-weight: 700; cursor: pointer; }
.standard-rule-details p, .standard-rule-details dl { margin-top: .55rem; }
.standard-rule-details dt { font-weight: 700; }
.standard-rule-details dl > div + div { margin-top: .55rem; }
</style>
