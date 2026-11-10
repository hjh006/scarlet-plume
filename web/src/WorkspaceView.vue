<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import {
  ApiError, assetBusinessLevelLabels as businessLevels, assetCategoryLabels as assetCategories,
  describeError, documentationUrl, formatDate, request, workspacePath,
  type Asset, type AssetDraft, type AssetPage, type Integration, type Overview, type Workspace,
} from './api'
import Icon from './Icon.vue'
import ImportPanel from './ImportPanel.vue'
import WorkspaceOverview from './WorkspaceOverview.vue'

const props = defineProps<{ workspace: Workspace; page: 'overview' | 'assets' | 'integrations' }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const workspaceUrl = workspacePath(props.workspace.slug)
const home = `#/${encodeURIComponent(props.workspace.slug)}`
const canEdit = computed(() => ['owner', 'operator'].includes(props.workspace.role))
const overview = ref<Overview | null>(null)
const assets = ref<Asset[]>([])
const integrations = ref<Integration[]>([])
const cursor = ref<number | null>(null)
const loading = ref(true)
const loadError = ref('')
const loaded = ref(false)
let active: AbortController | null = null

const showAssetForm = ref(false)
const openFormButton = ref<HTMLButtonElement | null>(null)
const assetNameInput = ref<HTMLInputElement | null>(null)
const draft = reactive<AssetDraft>({ name: '', system: '', ownerTeam: '' })
const saving = ref(false)
const formError = ref('')
const notice = ref('')
const statutoryCategories: Record<string, string> = { general: '一般数据', important: '重要数据', core: '核心数据' }

function knownClassification(value?: string) {
  return Boolean(value && value !== 'unknown')
}

function hasAssetMetadata(asset: Asset) {
  return [asset.category, asset.businessLevel, asset.statutoryCategory].some(knownClassification) ||
    Boolean(asset.classificationBasis || asset.description)
}

function reportError(error: unknown, output: { value: string }) {
  if (error instanceof ApiError && error.status === 401) emit('sessionExpired')
  else output.value = describeError(error)
}

async function load(append = false) {
  active?.abort()
  const operation = new AbortController()
  active = operation
  const options = { signal: operation.signal }
  loading.value = true
  loadError.value = ''
  try {
    if (props.page === 'overview') {
      overview.value = await request<Overview>(workspaceUrl + '/overview', options)
    } else if (props.page === 'assets') {
      const query = append && cursor.value !== null ? `?after=${encodeURIComponent(cursor.value)}` : ''
      const result = await request<AssetPage>(workspaceUrl + '/assets' + query, options)
      assets.value = append ? [...assets.value, ...result.items] : result.items
      cursor.value = result.nextCursor
    } else {
      const result = await request<{ items: Integration[] }>(workspaceUrl + '/integrations', options)
      integrations.value = result.items
    }
    loaded.value = true
  } catch (error) {
    if (!operation.signal.aborted) reportError(error, loadError)
  } finally {
    if (!operation.signal.aborted) loading.value = false
  }
}

async function openAssetForm() {
  showAssetForm.value = true
  await nextTick()
  assetNameInput.value?.focus()
}

async function closeAssetForm() {
  Object.assign(draft, { name: '', system: '', ownerTeam: '' })
  formError.value = ''
  showAssetForm.value = false
  await nextTick()
  openFormButton.value?.focus()
}

async function createAsset() {
  if (saving.value || !canEdit.value) return
  saving.value = true
  formError.value = ''
  notice.value = ''
  try {
    const asset = await request<Asset>(workspaceUrl + '/assets', {
      method: 'POST',
      body: JSON.stringify({
        name: draft.name.trim(), system: draft.system.trim(), ownerTeam: draft.ownerTeam.trim(),
      }),
    })
    await closeAssetForm()
    notice.value = `已登记资产「${asset.name}」。`
    await load()
  } catch (error) {
    reportError(error, formError)
  } finally {
    saving.value = false
  }
}

onMounted(() => { void load() })
onBeforeUnmount(() => active?.abort())
</script>

<template>
  <WorkspaceOverview v-if="page === 'overview'" :home="home" :overview="overview" />

  <header v-else class="page-header">
    <div>
      <p class="eyebrow">治理工作区 / {{ page === 'assets' ? '数据资产' : '集成与导入' }}</p>
      <h1>{{ page === 'assets' ? '数据资产' : '集成与导入' }}</h1>
      <p>{{ page === 'assets' ? '查看已登记的数据资产、来源引用与责任团队。' : '查看接入能力，并将客户侧采集的元数据登记到工作区。' }}</p>
    </div>
    <button v-if="page === 'assets' && canEdit" v-show="!showAssetForm" ref="openFormButton" type="button" class="button button-primary"
            aria-controls="asset-create-form" :aria-expanded="showAssetForm" @click="openAssetForm">
      <Icon name="plus" />登记资产
    </button>
    <a v-if="page === 'integrations'" class="button button-primary" :href="home + '/automation'">
      <Icon name="automation" />查看自动采集
    </a>
  </header>

  <div v-if="loadError" class="message message-error page-feedback" role="alert">
    <p>{{ loadError }}</p>
    <button class="button button-secondary" type="button" @click="load()">重新加载</button>
  </div>
  <p v-if="loading && !loaded" class="message page-feedback" role="status">正在加载工作区数据…</p>

  <template v-if="page === 'assets'">
    <p v-if="notice" class="message message-success page-feedback" role="status">{{ notice }}</p>
    <section v-if="canEdit" v-show="showAssetForm" id="asset-create-form" class="panel form-panel asset-form" aria-labelledby="asset-form-title">
      <h2 id="asset-form-title">登记新资产</h2>
      <p v-if="formError" class="message message-error page-feedback" role="alert">{{ formError }}</p>
      <form @submit.prevent="createAsset">
        <div class="field"><label for="asset-name">资产名称</label><input id="asset-name" ref="assetNameInput" v-model="draft.name" required></div>
        <div class="field"><label for="asset-system">所属系统</label><input id="asset-system" v-model="draft.system" required></div>
        <div class="field"><label for="asset-owner">责任团队</label><input id="asset-owner" v-model="draft.ownerTeam"></div>
        <div class="form-actions">
          <button class="button button-primary" type="submit" :disabled="saving">{{ saving ? '保存中…' : '保存资产' }}</button>
          <button class="button button-secondary" type="button" :disabled="saving" @click="closeAssetForm">取消</button>
        </div>
      </form>
    </section>
    <section v-if="loaded" class="panel" aria-labelledby="assets-title" :aria-busy="loading">
      <div class="panel-heading">
        <div><h2 id="assets-title">资产台账</h2><p>包含自动采集、目录导入和人工登记的资产。</p></div>
      </div>
      <p v-if="assets.length" class="table-hint">横向滑动查看完整台账。</p>
      <div v-if="assets.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="assets-title">
        <table class="data-table asset-table">
          <thead>
            <tr><th scope="col">资产名称</th><th scope="col">所属系统</th><th scope="col">来源与引用</th><th scope="col">责任团队</th><th scope="col">登记时间</th></tr>
          </thead>
          <tbody>
            <tr v-for="asset in assets" :key="asset.id">
              <th scope="row">
                {{ asset.name }}
                <a v-if="asset.sourceProvider === 'postgresql'"
                   class="table-link postgres-inspection-link" :href="home + '/assets/' + asset.id"
                   :aria-label="'查看 ' + asset.name + ' 的字段与权限'">字段与权限</a>
                <details v-if="hasAssetMetadata(asset)" class="asset-metadata">
                  <summary>分类与说明</summary>
                  <dl>
                    <template v-if="knownClassification(asset.category)">
                      <dt>数据属性</dt><dd>{{ assetCategories[asset.category!] || asset.category }}</dd>
                    </template>
                    <template v-if="knownClassification(asset.businessLevel)">
                      <dt>企业内部敏感级别</dt><dd>{{ businessLevels[asset.businessLevel!] || asset.businessLevel }}</dd>
                    </template>
                    <template v-if="knownClassification(asset.statutoryCategory)">
                      <dt>法定数据类别</dt><dd>{{ statutoryCategories[asset.statutoryCategory!] || asset.statutoryCategory }}</dd>
                    </template>
                    <template v-if="asset.classificationBasis">
                      <dt>分级依据</dt><dd class="text-block">{{ asset.classificationBasis }}</dd>
                    </template>
                    <template v-if="asset.description">
                      <dt>说明</dt><dd class="text-block">{{ asset.description }}</dd>
                    </template>
                  </dl>
                </details>
              </th>
              <td>{{ asset.system }}</td>
              <td>
                {{ asset.source || '人工登记' }}
                <span v-if="asset.sourceProvider === 'openapi'" class="table-secondary">OpenAPI 契约</span>
                <span v-else-if="asset.sourceProvider === 'runtime-api'" class="table-secondary">API 运行时</span>
                <span v-if="asset.sourceRef" class="table-secondary mono">{{ asset.sourceRef }}</span>
              </td>
              <td>{{ asset.ownerTeam || '—' }}</td>
              <td class="date-text"><time :datetime="asset.createdAt">{{ formatDate(asset.createdAt) }}</time></td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-else class="empty-state">
        <strong>暂无资产</strong>
        <p>{{ canEdit ? '可登记首个资产，或导入客户侧采集清单。' : '工作区登记资产后将在此显示。' }}</p>
      </div>
      <div v-if="cursor !== null" class="panel-footer">
        <button type="button" class="button button-secondary" :disabled="loading" @click="load(true)">
          {{ loading ? '正在加载…' : '加载更多' }}
        </button>
      </div>
    </section>
  </template>

  <div v-else-if="page === 'integrations'" class="integration-grid">
    <section v-if="loaded" class="panel" aria-labelledby="integrations-title">
      <div class="panel-heading"><div><h2 id="integrations-title">集成目录</h2><p>查看接入能力、支持状态和文档。</p></div></div>
      <ul v-if="integrations.length" class="integration-list">
        <li v-for="item in integrations" :key="item.id" class="integration-row">
          <span class="workspace-symbol"><Icon name="integrations" /></span>
          <div class="integration-copy">
            <h3>{{ item.label }}</h3>
            <span class="table-secondary">{{ item.category }}</span>
            <p>{{ item.capability }}</p>
            <a v-if="documentationUrl(item.docs)" class="source-link" :href="documentationUrl(item.docs)"
               target="_blank" rel="noopener noreferrer">接口文档 <Icon name="external" /></a>
          </div>
          <span class="badge" :class="item.status === 'available' ? 'status-verified' : 'status-draft'">
            {{ item.status === 'available' ? '可用' : '规划中' }}
          </span>
        </li>
      </ul>
      <div v-else class="empty-state"><strong>暂无集成条目</strong><p>当前工作区的接入能力将在此显示。</p></div>
    </section>
    <ImportPanel v-if="loaded && canEdit" :slug="workspace.slug" @session-expired="emit('sessionExpired')" />
  </div>
</template>
