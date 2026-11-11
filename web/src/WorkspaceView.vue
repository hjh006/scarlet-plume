<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import {
  ApiError, assetBusinessLevelLabels as businessLevels, assetCategoryLabels as assetCategories,
  describeError, documentationUrl, formatDate, providerLabel, request, workspacePath,
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
const assetQuery = ref('')
const assetScope = ref('')
const selectedAssetId = ref<string>('')
const providerScopes = [ { id: '', label: '全部资产' }, { id: 'database', label: '数据库' }, { id: 'api', label: 'API' }, { id: 'other', label: '其他来源' } ]
function assetScopeOf(asset: Asset) {
  if (['postgresql','mysql','mariadb','mongodb','sqlserver','oracle','clickhouse','db2','redis'].includes(asset.sourceProvider || '')) return 'database'
  if (['openapi', 'runtime-api'].includes(asset.sourceProvider || '')) return 'api'
  return 'other'
}
const visibleAssets = computed(() => assets.value.filter(asset => (!assetScope.value || assetScopeOf(asset) === assetScope.value) &&
  (!assetQuery.value.trim() || [asset.name, asset.system, asset.ownerTeam, asset.source, asset.sourceRef].some(value => value?.toLocaleLowerCase().includes(assetQuery.value.trim().toLocaleLowerCase())))))
const selectedAsset = computed(() => visibleAssets.value.find(asset => String(asset.id) === selectedAssetId.value) || visibleAssets.value[0])
const selectedIntegrationId = ref('')
const selectedIntegration = computed(() => integrations.value.find(item => item.id === selectedIntegrationId.value) || integrations.value[0])
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
  <WorkspaceOverview v-if="page === 'overview'" :home="home" :overview="overview" :loading="loading" @refresh="load()" @session-expired="emit('sessionExpired')" />

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
    <div v-if="loaded" class="asset-workbench"><section class="panel" aria-labelledby="assets-title" :aria-busy="loading">
      <div class="panel-heading"><div><h2 id="assets-title">资产台账</h2><p>包含自动采集、目录导入和人工登记的资产。</p></div><span class="badge status-draft">已加载 {{ assets.length }} 项</span></div>
      <div class="asset-scope-tabs" role="group" aria-label="筛选已加载资产的来源"><button v-for="scope in providerScopes" :key="scope.id" class="asset-scope-tab" :class="{ active: assetScope === scope.id }" type="button" :aria-pressed="assetScope === scope.id" @click="assetScope = scope.id">{{ scope.label }}</button></div>
      <div class="filter-bar asset-search"><label for="asset-search">搜索已加载资产</label><input id="asset-search" v-model="assetQuery" type="search" placeholder="资产名称 / 系统 / 责任团队"><a class="table-link" :href="home + '/asset-catalog'">业务资产目录</a><button type="button" class="button button-secondary" :disabled="loading" @click="load()">刷新台账</button></div>
      <p v-if="cursor !== null" class="table-hint">筛选作用于已加载记录，继续加载可扩大查看范围。</p>
      <div v-if="visibleAssets.length" class="table-scroll asset-table-scroll" role="region" tabindex="0" aria-labelledby="assets-title">
        <table class="data-table asset-table"><colgroup><col class="asset-col-name"><col class="asset-col-system"><col class="asset-col-source"><col class="asset-col-owner"><col class="asset-col-date"></colgroup>
          <thead>
            <tr><th scope="col">资产名称</th><th scope="col">所属系统</th><th scope="col">来源与引用</th><th scope="col">责任团队</th><th scope="col">登记时间</th></tr>
          </thead>
          <tbody>
            <tr v-for="asset in visibleAssets" :key="asset.id" :class="{ 'asset-selected': asset.id === selectedAsset?.id }">
              <th scope="row">
                <button class="asset-name" type="button" :title="asset.name" :aria-pressed="asset.id === selectedAsset?.id" @click="selectedAssetId = String(asset.id)">{{ asset.name }}</button>
                <a v-if="asset.sourceProvider === 'postgresql'"
                   class="table-link postgres-inspection-link" :href="home + '/assets/' + asset.id"
                   :aria-label="'查看 ' + asset.name + ' 的字段与权限'">字段与权限</a>

              </th>
              <td class="asset-system-cell" :title="asset.system">{{ asset.system }}</td>
              <td>
                <span class="asset-source-name" :title="asset.source || '人工登记'">{{ asset.source || '人工登记' }}</span>
                <span v-if="asset.sourceProvider === 'openapi'" class="table-secondary">OpenAPI 契约</span>
                <span v-else-if="asset.sourceProvider === 'runtime-api'" class="table-secondary">API 运行时</span>
                <span v-if="asset.sourceRef" class="table-secondary mono" :title="asset.sourceRef">{{ asset.sourceRef }}</span>
              </td>
              <td>{{ asset.ownerTeam || '—' }}</td>
              <td class="date-text"><time :datetime="asset.createdAt">{{ formatDate(asset.createdAt) }}</time></td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-else class="empty-state">
        <strong>{{ assets.length ? '没有匹配的资产' : '暂无资产' }}</strong>
        <p>{{ assets.length ? '调整搜索条件或来源筛选后重试。' : canEdit ? '可登记首个资产，或导入客户侧采集清单。' : '工作区登记资产后将在此显示。' }}</p>
      </div>
      <div v-if="cursor !== null" class="panel-footer">
        <button type="button" class="button button-secondary" :disabled="loading" @click="load(true)">
          {{ loading ? '正在加载…' : '加载更多' }}
        </button>
      </div>
      <div v-else-if="assets.length" class="panel-footer"><span class="field-hint">显示 {{ visibleAssets.length }} / 已加载 {{ assets.length }} 项</span></div>
    </section>
    <aside class="context-rail asset-context"><section class="panel" aria-labelledby="asset-summary-title"><div class="panel-heading"><div><p class="eyebrow">当前资产</p><h2 id="asset-summary-title">{{ selectedAsset?.name || '资产详情' }}</h2></div></div><template v-if="selectedAsset"><dl class="detail-list"><div><dt>所属系统</dt><dd>{{ selectedAsset.system || '未记录' }}</dd></div><div><dt>责任团队</dt><dd>{{ selectedAsset.ownerTeam || '未记录' }}</dd></div><div><dt>来源类型</dt><dd>{{ providerLabel(selectedAsset.sourceProvider || '') || selectedAsset.source || '人工登记' }}</dd></div><div><dt>来源引用</dt><dd class="mono">{{ selectedAsset.sourceRef || '未记录' }}</dd></div><div v-if="hasAssetMetadata(selectedAsset)"><dt>数据属性</dt><dd>{{ assetCategories[selectedAsset.category || ''] || '待判定' }}</dd></div><div><dt>内部敏感级别</dt><dd>{{ businessLevels[selectedAsset.businessLevel || ''] || '待判定' }}</dd></div><div><dt>法定类别</dt><dd>{{ statutoryCategories[selectedAsset.statutoryCategory || ''] || '待认定' }}</dd></div><div><dt>登记时间</dt><dd><time :datetime="selectedAsset.createdAt">{{ formatDate(selectedAsset.createdAt) }}</time></dd></div><div v-if="selectedAsset.classificationBasis"><dt>判定依据</dt><dd class="text-block">{{selectedAsset.classificationBasis}}</dd></div><div v-if="selectedAsset.description"><dt>说明</dt><dd class="text-block">{{selectedAsset.description}}</dd></div></dl><div class="asset-context-actions"><a v-if="selectedAsset.sourceProvider === 'postgresql'" class="button button-primary" :href="home + '/assets/' + selectedAsset.id">字段与权限</a><a class="button button-secondary" :href="home + '/classification/' + selectedAsset.id">查看分类与依据</a><a class="table-link" :href="home + '/activities'">查看处理活动</a></div></template><p v-else class="empty-state">选择一项资产查看其来源和分类。</p></section><section class="panel scope-explanation"><h3>来源与确认</h3><p>来源引用用于追溯自动采集与导入。分类判定保留采用标准、规则依据和变更版本。</p><a class="table-link" :href="home + '/automation'">查看自动采集 <Icon name="chevron" /></a></section></aside></div>
  </template>

  <div v-else-if="page === 'integrations'" class="integration-workbench">
    <div class="section-stack"><section v-if="loaded" class="panel" aria-labelledby="integrations-title"><div class="panel-heading"><div><h2 id="integrations-title">集成目录</h2><p>按支持范围选择接入方式，连接运行状态请查看自动采集。</p></div><span class="badge status-draft">{{ integrations.length }} 项能力</span></div>
      <div v-if="integrations.length" class="table-scroll" role="region" tabindex="0" aria-labelledby="integrations-title"><table class="data-table integration-table"><thead><tr><th scope="col">集成</th><th scope="col">支持范围</th><th scope="col">能力状态</th><th scope="col">文档</th></tr></thead><tbody><tr v-for="item in integrations" :key="item.id" :class="{ 'asset-selected': selectedIntegration?.id === item.id }"><th scope="row"><button class="asset-name" type="button" @click="selectedIntegrationId = item.id">{{ item.label }}</button><span class="table-secondary">{{ item.category }}</span></th><td>{{ item.capability }}</td><td><span class="badge" :class="item.status === 'available' ? 'status-verified' : 'status-draft'">{{ item.status === 'available' ? '可用' : '规划中' }}</span></td><td><a v-if="documentationUrl(item.docs)" class="table-link" :href="documentationUrl(item.docs)" target="_blank" rel="noopener noreferrer">文档 <Icon name="external" /></a><span v-else>—</span></td></tr></tbody></table></div><div v-else class="empty-state"><strong>暂无集成条目</strong><p>当前工作区的接入能力将在此显示。</p></div>
    </section><section v-if="selectedIntegration" class="panel integration-scope"><div class="panel-heading"><div><p class="eyebrow">所选接入能力</p><h2>{{ selectedIntegration.label }}</h2></div><span class="badge status-draft">{{ selectedIntegration.category }}</span></div><div class="integration-scope-copy"><p>{{ selectedIntegration.capability }}</p><p class="field-hint">“可用”表示平台支持该接入能力；实例连接、采集范围和本次运行结果需在自动采集中核对。</p><a class="table-link" :href="home + '/automation'">进入自动采集 <Icon name="chevron" /></a></div></section></div>
    <ImportPanel v-if="loaded && canEdit" :slug="workspace.slug" @session-expired="emit('sessionExpired')" />
    <aside v-else-if="loaded" class="panel readonly-import"><h2>资产导入</h2><p class="field-hint">当前角色可查看集成能力，资产导入由工作区所有者或操作员执行。</p></aside>
  </div>
</template>

<style scoped>
.asset-context{display:grid;gap:16px}.asset-search input{border:1px solid var(--input-line);border-radius:6px;padding:7px 10px;background:white;color:var(--ink)}.asset-table .mono{white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:100%}.asset-workbench{display:grid;grid-template-columns:minmax(0,1fr) 300px;align-items:start;gap:16px}.asset-workbench>*{min-width:0}.asset-scope-tabs{display:flex;gap:22px;border-bottom:1px solid var(--line);padding:0 20px}.asset-scope-tab{padding:12px 0;border:0;border-bottom:2px solid transparent;background:none;color:var(--secondary);font:inherit;cursor:pointer;white-space:nowrap}.asset-scope-tab.active{color:var(--accent);border-bottom-color:var(--accent);font-weight:700}.asset-search{gap:10px}.asset-search input{max-width:300px;min-width:130px;flex:1}.asset-table{min-width:980px;table-layout:fixed}.asset-col-name{width:280px}.asset-col-system{width:180px}.asset-col-source{width:230px}.asset-col-owner{width:110px}.asset-col-date{width:180px}.asset-table .asset-name,.asset-system-cell,.asset-source-name{white-space:nowrap;overflow:hidden;text-overflow:ellipsis;overflow-wrap:normal;word-break:normal;max-width:100%}.asset-source-name{display:block}.asset-table-scroll{max-height:calc(100vh - 410px);min-height:300px;overflow:auto}.asset-name{display:block;border:0;padding:0;background:none;font:inherit;font-weight:650;text-align:left;color:var(--accent);cursor:pointer;overflow-wrap:anywhere}.asset-selected{background:var(--surface-subtle)}.asset-context .detail-list{grid-template-columns:minmax(0,1fr);gap:14px}.asset-context dd{overflow-wrap:anywhere}.asset-context-actions{display:flex;flex-direction:column;align-items:stretch;gap:8px;padding:4px 20px 20px}.asset-context-actions>.table-link{padding-top:6px}.scope-explanation{padding:20px}.scope-explanation h3{font-size:14px}.scope-explanation p{font-size:12px;color:var(--secondary);line-height:1.75;margin:10px 0}.scope-explanation .table-link{display:inline-flex;align-items:center;gap:8px}.integration-workbench{display:grid;grid-template-columns:minmax(0,1fr) minmax(330px,410px);align-items:start;gap:16px}.integration-workbench>*{min-width:0}.integration-table{min-width:590px}.integration-table th:first-child{width:23%}.integration-table td:nth-child(2){max-width:340px}.integration-scope-copy{padding:16px 20px 20px}.integration-scope-copy p{margin-bottom:12px}.readonly-import{padding:20px}.asset-form form{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:16px}.asset-form .field{margin:0}.asset-form .form-actions{grid-column:1/-1;margin:0}@media(max-width:1399px){.asset-workbench{grid-template-columns:minmax(0,1fr) 270px}.integration-workbench{grid-template-columns:minmax(0,1fr) 340px}}
</style>
