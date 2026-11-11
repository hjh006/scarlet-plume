<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { activityKindLabels } from './activity'
import { ApiError, describeError, formatDate, request, workspacePath, type Workspace } from './api'
import { templateKinds, type CheckTemplate } from './template'
import TemplateCriteria from './TemplateCriteria.vue'
import Icon from './Icon.vue'

const props = defineProps<{ workspace: Workspace; revisionId?: string }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const pageUrl = `#/${encodeURIComponent(props.workspace.slug)}/templates`
const apiUrl = workspacePath(props.workspace.slug) + '/templates'
const templates = ref<CheckTemplate[]>([])
const revision = ref<CheckTemplate | null>(null)
const kind = ref('')
const loading = ref(true)
const errorMessage = ref('')
const selectedId = ref<number>()
const selected = computed(() => templates.value.find(t => t.id === selectedId.value) ?? templates.value[0])
function scrollItem(key: string) { document.getElementById('template-item-' + key)?.scrollIntoView({ block: 'start', behavior: 'smooth' }) }
let active: AbortController | undefined

async function load() {
  active?.abort()
  const operation = new AbortController()
  active = operation
  loading.value = true
  errorMessage.value = ''
  try {
    const options = { signal: operation.signal }
    if (props.revisionId) {
      revision.value = await request<CheckTemplate>(apiUrl + '/' + encodeURIComponent(props.revisionId), options)
    } else {
      const query = kind.value ? '?kind=' + encodeURIComponent(kind.value) : ''
      const result = await request<{ items: CheckTemplate[] }>(apiUrl + query, options)
      if (!operation.signal.aborted) templates.value = result.items
    }
  } catch (error) {
    if (operation.signal.aborted) return
    if (error instanceof ApiError && error.status === 401) emit('sessionExpired')
    else errorMessage.value = describeError(error)
  } finally {
    if (!operation.signal.aborted) loading.value = false
  }
}

onMounted(() => { void load() })
onBeforeUnmount(() => active?.abort())
</script>

<template>
  <a v-if="revisionId" class="back-link" :href="pageUrl"><Icon name="back" />返回检查模板</a>
  <header class="page-header">
    <div>
      <h1>{{ revision?.title || '检查模板' }}</h1>
      <p v-if="revision">{{ activityKindLabels[revision.kind] }} · 版本 {{ revision.version }} · {{ revision.builtIn ? '内置模板' : '工作区模板' }}</p>
      <p v-else>为对外共享、数据出境和 AI 使用定义可复用的检查项。</p>
    </div>
    <div v-if="workspace.role === 'owner' && (!revisionId || revision)" class="page-actions">
      <a v-if="!revisionId" class="button button-primary" :href="pageUrl + '/new'"><Icon name="plus" />新建模板</a>
      <template v-else-if="revision">
        <a v-if="!revision.builtIn" class="button button-primary" :href="pageUrl + '/revise/' + revision.id"><Icon name="plus" />发布新版本</a>
        <a :class="['button', revision.builtIn ? 'button-primary' : 'button-secondary']" :href="pageUrl + '/new/' + revision.id">
          从此版本创建模板
        </a>
      </template>
    </div>
  </header>
  <div v-if="errorMessage" class="message message-error page-feedback">
    <p role="alert">{{ errorMessage }}</p>
    <button type="button" class="button button-secondary" @click="load">重新读取</button>
  </div>
  <p v-else-if="revisionId && loading" class="message" role="status">正在读取模板版本…</p>
  <div v-else-if="revision" class="template-version-workbench"><nav class="panel template-outline" aria-label="检查项目录"><h2>检查项目录</h2><button v-for="(item,index) in revision.items" :key="item.key" @click="scrollItem(item.key)"><span>{{ String(index+1).padStart(2,'0') }}</span>{{ item.title }}</button></nav><section class="panel" aria-labelledby="template-items-title">
    <div class="panel-heading">
      <div><h2 id="template-items-title">检查项</h2><p>选择适用于本次活动的检查项，并在取证时记录实际范围。</p></div>
      <span class="badge">{{ revision.items.length }} 项</span>
    </div>
    <ol class="template-items">
      <li v-for="item in revision.items" :key="item.key" :id="'template-item-' + item.key">
        <div class="template-item-heading"><h3>{{ item.title }}</h3><span class="badge">{{ item.requirementCode }}</span></div>
        <TemplateCriteria :item="item" />
      </li>
    </ol>
  </section><aside class="template-version-rail"><section class="panel"><h2>采用版本</h2><span class="badge">v{{ revision.version }}</span><dl><div><dt>活动类型</dt><dd>{{ activityKindLabels[revision.kind] }}</dd></div><div><dt>模板来源</dt><dd>{{ revision.builtIn ? '内置模板' : '工作区模板' }}</dd></div><div><dt>发布记录</dt><dd>#{{ revision.id }}</dd></div><div><dt>发布时间</dt><dd>{{ formatDate(revision.createdAt) }}</dd></div><div><dt>检查项</dt><dd>{{ revision.items.length }} 项</dd></div></dl></section><section class="panel"><h2>版本留存</h2><p>检查创建时固定采用的模板版本。适用条件、取证要求和验收标准随检查留存，可在证据和报告中追溯。</p><p>发布新版本后，既有检查继续采用原版本。</p></section></aside></div>
  <div v-else-if="!revisionId && !errorMessage" class="template-catalog-workbench"><section class="panel" aria-labelledby="template-catalog-title" :aria-busy="loading">
    <div class="panel-heading">
      <div><h2 id="template-catalog-title">模板目录</h2><p>在新建关联检查时选择模板项。已创建的检查保留所选版本。</p></div>
    </div>
    <div class="filter-bar">
      <label for="template-kind-filter">活动类型</label>
      <select id="template-kind-filter" v-model="kind" @change="load">
        <option value="">全部类型</option>
        <option v-for="entry in templateKinds" :key="entry" :value="entry">{{ activityKindLabels[entry] }}</option>
      </select>
    </div>
    <p v-if="loading" class="empty-state" role="status">正在读取模板目录…</p>
    <ul v-else-if="templates.length" class="template-catalog">
      <li v-for="entry in templates" :key="entry.id" @mouseenter="selectedId = entry.id" @focusin="selectedId = entry.id">
        <a class="template-catalog-link" :href="pageUrl + '/' + entry.id">
          <div class="template-catalog-copy">
            <strong>{{ entry.title }}</strong>
            <p>{{ activityKindLabels[entry.kind] }} · {{ entry.builtIn ? '内置模板' : '工作区模板' }} · {{ entry.items.length }} 项</p>
          </div>
          <div class="template-catalog-meta"><span class="badge">版本 {{ entry.version }}</span><time class="wide-date" :datetime="entry.createdAt">{{ formatDate(entry.createdAt) }}</time></div>
          <Icon name="chevron" />
        </a>
      </li>
    </ul>
    <div v-else class="empty-state"><strong>此场景暂无模板</strong><p>可切换活动类型查看其他模板。</p></div>
  </section><aside class="template-version-rail"><section v-if="selected" class="panel"><p class="eyebrow">当前模板</p><h2>{{ selected.title }}</h2><span class="badge">版本 {{ selected.version }}</span><dl><div><dt>活动类型</dt><dd>{{ activityKindLabels[selected.kind] }}</dd></div><div><dt>来源</dt><dd>{{ selected.builtIn ? '内置模板' : '工作区模板' }}</dd></div><div><dt>检查项</dt><dd>{{ selected.items.length }} 项</dd></div></dl><a class="button button-primary" :href="pageUrl + '/' + selected.id">查看检查项</a></section><section class="panel"><h2>采用流程</h2><ol><li>选择活动场景与模板版本</li><li>按适用条件创建关联检查</li><li>采集证据并提交独立复核</li></ol><p>目录展示 {{ templates.length }} 个模板，具体适用条件以模板检查项为准。</p></section></aside></div>
</template>

<style scoped>
.template-version-workbench{display:grid;grid-template-columns:170px minmax(0,1fr) 280px;gap:16px;align-items:start}.template-catalog-workbench{display:grid;grid-template-columns:minmax(0,1fr) 300px;gap:16px;align-items:start}.template-outline{padding:18px 10px;position:sticky;top:84px}.template-outline h2{font-size:13px;padding:0 8px}.template-outline button{display:flex;gap:8px;text-align:left;font:inherit;font-size:12px;line-height:1.7;color:var(--muted);width:100%;padding:12px 8px;background:none;border:0;cursor:pointer}.template-outline button span{color:var(--blue);font-size:10px;margin-top:3px}.template-outline button:hover{background:#eff7fc;color:var(--blue)}.template-version-rail{display:grid;gap:16px}.template-version-rail .panel{padding:20px}.template-version-rail h2{font-size:15px;margin:0 0 14px}.template-version-rail p,.template-version-rail li{font-size:12px;color:var(--muted);line-height:1.9}.template-version-rail ol{padding-left:18px}.template-version-rail dl{display:grid;gap:14px;margin:20px 0}.template-version-rail dl>div{display:grid;grid-template-columns:75px minmax(0,1fr);gap:8px;font-size:12px}.template-version-rail dt{color:var(--muted)}.template-version-rail dd{margin:0;overflow-wrap:anywhere}.template-items{list-style:none;padding:0 22px;margin:0}.template-items>li{scroll-margin-top:90px;padding:22px 0;border-bottom:1px solid var(--line)}.template-items>li:last-child{border:0}.template-item-heading{display:flex;justify-content:space-between;gap:12px;align-items:start}.template-item-heading h3{font-size:15px;margin:0 0 16px}.template-catalog{list-style:none;margin:0;padding:0 20px}.template-catalog>li{border-bottom:1px solid var(--line)}.template-catalog-link{display:flex;align-items:center;gap:14px;padding:20px 4px;color:inherit;text-decoration:none}.template-catalog-link:hover{background:#f8fbff}.template-catalog-copy{flex:1}.template-catalog-copy strong{font-size:14px;color:#174c75}.template-catalog-copy p{font-size:12px;color:var(--muted);margin:8px 0 0}.template-catalog-meta{display:grid;gap:10px;text-align:right;font-size:11px;color:var(--muted)}@media(max-width:1400px){.template-version-workbench{grid-template-columns:145px minmax(0,1fr) 250px;gap:12px}.template-catalog-workbench{grid-template-columns:minmax(0,1fr) 270px}}
</style>
