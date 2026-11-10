<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
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
  <section v-else-if="revision" class="panel" aria-labelledby="template-items-title">
    <div class="panel-heading">
      <div><h2 id="template-items-title">检查项</h2><p>选择适用于本次活动的检查项，并在取证时记录实际范围。</p></div>
      <span class="badge">{{ revision.items.length }} 项</span>
    </div>
    <ol class="template-items">
      <li v-for="item in revision.items" :key="item.key">
        <div class="template-item-heading"><h3>{{ item.title }}</h3><span class="badge">{{ item.requirementCode }}</span></div>
        <TemplateCriteria :item="item" />
      </li>
    </ol>
  </section>
  <section v-else-if="!revisionId && !errorMessage" class="panel" aria-labelledby="template-catalog-title" :aria-busy="loading">
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
      <li v-for="entry in templates" :key="entry.id">
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
  </section>
</template>
