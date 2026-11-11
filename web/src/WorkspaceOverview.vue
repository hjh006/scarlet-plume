<script setup lang="ts">
import { computed, ref, watch, onBeforeUnmount } from 'vue'
import { ApiError, assetBusinessLevelLabels, assetCategoryLabels, describeError, formatDate, providerLabel, request, workspacePath, type Overview } from './api'
import { checkStatusLabels } from './workflow'
import { runtimeRuleLabels, runtimeRiskSeverityLabels } from './runtime-findings'
import Icon from './Icon.vue'
import FeatherGraph from './FeatherGraph.vue'
import { dataFlowNodeLabels, dataFlowEvidenceLabels, type DataFlowGraph, type DataFlowNode } from './data-flows'
import './overview.css'

const props = defineProps<{ home: string; overview: Overview | null; loading?: boolean }>()
const emit = defineEmits<{ refresh: []; sessionExpired: [] }>()
const metrics = computed(() => [
  { label: '已配置来源', value: props.overview?.counts.activeSources, route: 'automation', icon: 'assets', tone: 'teal' },
  { label: '数据资产', value: props.overview?.counts.assets, route: 'assets', icon: 'classification', tone: 'violet' },
  { label: '待处置发现', value: props.overview?.counts.openRuntimeFindings, route: 'api-security/runtime', icon: 'warning', tone: 'coral' },
  { label: '待复核检查', value: props.overview?.counts.inReview, route: 'checks', icon: 'checks', tone: 'blue' },
])
const sources = computed(() => props.overview?.collectionSources ?? [])
const findings = computed(() => props.overview?.recentFindings ?? [])
const sourceStates: Record<string, string> = {
  collecting: '采集中', queued: '已排队', paused: '已暂停', enabled: '已启用', failed: '采集失败', never_run: '待首次记录', observing: '已接入',
}
function sourceTarget(kind: string) { return props.home + (kind === 'runtime' ? '/api-security/runtime' : kind === 'discovery' ? '/discovery' : kind === 'operations' ? '/operations' : '/automation') }
function sourceIcon(provider: string) { return ['otlp', 'gateway'].includes(provider) ? 'api-security' : ['postgresql','mysql','mariadb','mongodb'].includes(provider) ? 'assets' : 'integrations' }
function sourceProvider(provider: string) { return provider === 'otlp' ? '应用探针 · OTLP' : provider === 'gateway' ? '独立 Agent / 网关' : provider === 'feishu' ? '飞书' : providerLabel(provider) }
const graph=ref<DataFlowGraph|null>(null), graphLoading=ref(false), graphError=ref(''), selectedId=ref('')
let graphRequest:AbortController|undefined
const selectedNode=computed(()=>graph.value?.nodes.find(node=>node.id===selectedId.value)??graph.value?.nodes[0]??null)
const selectedEdges=computed(()=>graph.value?.edges.filter(edge=>edge.from===selectedNode.value?.id||edge.to===selectedNode.value?.id)??[])
function selectNode(node:DataFlowNode){selectedId.value=node.id}
async function loadGraph(){graphRequest?.abort();const current=new AbortController();graphRequest=current;graphLoading.value=true;graphError.value='';try{const slug=decodeURIComponent(props.home.split('/')[1]||'');const result=await request<DataFlowGraph>(workspacePath(slug)+'/data-flows',{signal:current.signal});if(!current.signal.aborted){graph.value=result;if(!result.nodes.some(node=>node.id===selectedId.value))selectedId.value=result.nodes[0]?.id||''}}catch(error){if(!current.signal.aborted){if(error instanceof ApiError&&error.status===401)emit('sessionExpired');else graphError.value=describeError(error)}}finally{if(!current.signal.aborted)graphLoading.value=false}}
watch(()=>[props.home,props.overview?.generatedAt],()=>void loadGraph(),{immediate:true});onBeforeUnmount(()=>graphRequest?.abort())
</script>

<template>
  <div class="workspace-overview" :aria-busy="loading">
    <header class="overview-heading">
      <div>
        <p class="eyebrow">数据治理 / SCARLET PLUME</p><h1>数据安全全景</h1>
        <p v-if="overview?.generatedAt">工作区数据更新于 <time :datetime="overview.generatedAt">{{ formatDate(overview.generatedAt) }}</time></p>
        <p v-else>查看当前工作区的数据接入、治理进度和待办事项。</p>
      </div>
      <div class="page-actions">
        <button type="button" class="button button-secondary" :disabled="loading" @click="emit('refresh')"><Icon name="automation" />{{ loading ? '刷新中…' : '刷新数据' }}</button>
        <a class="button button-primary" :href="home + '/discovery'"><Icon name="plus" />接入数据源</a>
      </div>
    </header>

    <ul class="overview-metrics" aria-label="工作区治理指标">
      <li v-for="metric in metrics" :key="metric.label">
        <a :href="home + '/' + metric.route" class="overview-metric-link">

          <span class="overview-metric-copy" :data-tone="metric.tone"><span>{{metric.label}}</span><strong>{{metric.value??'—'}}</strong><small>当前授权工作区</small></span>
          <Icon name="chevron" />
        </a>
      </li>
    </ul>

    <div class="overview-primary-grid">
      <section class="panel overview-flow-panel" aria-labelledby="overview-flow-title"><div class="overview-panel-heading"><div><h2 id="overview-flow-title">赤翎 · 数据关系</h2><p v-if="graph">{{graph.summary.nodes}} 个节点 · {{graph.summary.edges}} 条关系</p></div><a class="button button-secondary" :href="home+'/data-flows'">查看图谱与历史</a></div><p v-if="graphError" class="message message-error overview-graph-error" role="alert">{{graphError}}<button class="button button-secondary" type="button" @click="loadGraph">重新读取</button></p><FeatherGraph :graph="graph" :loading="graphLoading" :selected="selectedNode?.id" @select="selectNode"/><div class="overview-flow-legend"><span>有依据的关系 · 仅覆盖当前已观察来源</span><time v-if="graph" :datetime="graph.generatedAt">{{formatDate(graph.generatedAt)}}</time></div></section>
      <aside class="panel overview-relation-detail"><div class="overview-panel-heading"><h2>关系详情</h2></div><div v-if="selectedNode" class="overview-relation-body"><h3>{{selectedNode.label}}</h3><div class="overview-relation-tags"><span class="badge">{{dataFlowNodeLabels[selectedNode.kind]}}</span><span v-for="category in selectedNode.categories" :key="category" class="badge status-in_review">{{assetCategoryLabels[category]||category}}</span></div><dl><div><dt>关联服务</dt><dd>{{selectedNode.service||'未声明'}}</dd></div><div><dt>来源记录</dt><dd>{{selectedNode.evidence.length}} 项</dd></div><div><dt>直接关系</dt><dd>{{selectedEdges.length}} 条</dd></div></dl><ol class="overview-proof-spine"><li v-for="(proof,index) in selectedNode.evidence.slice(0,4)" :key="index"><strong>{{dataFlowEvidenceLabels[proof.kind]||proof.kind}}</strong><span>{{proof.source}}</span><time v-if="proof.observedAt" :datetime="proof.observedAt">{{formatDate(proof.observedAt)}}</time></li></ol><p v-if="!selectedNode.evidence.length" class="field-hint">此节点没有附带来源记录，具体覆盖需继续核对。</p><a class="button button-primary" :href="selectedNode.assetId?home+'/classification/'+selectedNode.assetId:home+'/data-flows'">{{selectedNode.assetId?'查看资产与依据':'查看关系与证据'}}</a></div><p v-else class="empty-state">接入有效来源后，可选择图中节点查看实际依据。</p></aside>
    </div>

    <div class="overview-secondary-grid">
      <section class="panel overview-findings" aria-labelledby="overview-findings-title">
        <div class="overview-panel-heading"><h2 id="overview-findings-title">最新运行时发现</h2><a class="quiet-link" :href="home + '/api-security/runtime'">查看全部 <Icon name="chevron" /></a></div>
        <div v-if="findings.length" class="table-scroll" tabindex="0" role="region" aria-labelledby="overview-findings-title">
          <table class="data-table overview-findings-table">
            <thead><tr><th scope="col">发现与接口</th><th scope="col">服务</th><th scope="col">检查状态</th><th scope="col"><span class="visually-hidden">操作</span></th></tr></thead>
            <tbody>
            <tr v-for="finding in findings" :key="finding.id"><th scope="row"><strong>{{ runtimeRuleLabels[finding.rule] }}</strong><span class="table-secondary mono">{{ finding.method }} {{ finding.route }}</span></th><td>{{ finding.service }}<span v-if="finding.risk" class="table-secondary">{{ runtimeRiskSeverityLabels[finding.risk.severity] }}风险</span><span v-else class="table-secondary">风险待研判</span></td><td><span class="badge status" :class="'status-' + finding.checkStatus">{{ checkStatusLabels[finding.checkStatus] }}</span></td><td><a class="overview-row-action" :href="home + '/checks/' + finding.checkId" :aria-label="'查看关联检查 #' + finding.checkId">查看 <Icon name="chevron" /></a></td></tr>
          </tbody>
          </table>
        </div>
        <div v-else class="overview-empty"><Icon name="shield" /><strong>{{ overview ? '暂无运行时发现' : '正在读取运行时发现…' }}</strong><p>接入运行时来源并导入对应服务契约后，查看实际观测到的差异。</p><a :href="home + '/api-security/runtime'">进入运行时观测 <Icon name="chevron" /></a></div>
      </section>
      <section class="panel overview-collections" aria-labelledby="overview-collections-title">
        <div class="overview-panel-heading"><h2 id="overview-collections-title">采集来源状态</h2><a class="quiet-link" :href="home + '/automation'">管理来源 <Icon name="chevron" /></a></div>
        <ul v-if="sources.length" class="overview-source-list"><li v-for="source in sources" :key="source.kind + ':' + source.id"><Icon :name="sourceIcon(source.provider)" /><span class="overview-source-name"><a :href="sourceTarget(source.kind)">{{ source.name }}</a><small>{{ sourceProvider(source.provider) }}</small></span><span class="overview-source-status" :data-state="source.state">{{ sourceStates[source.state] || '状态待确认' }}</span><time v-if="source.lastObservedAt || source.lastRunAt" :datetime="source.lastObservedAt || source.lastRunAt">{{ formatDate(source.lastObservedAt || source.lastRunAt || '') }}</time><span v-else class="overview-source-time">尚无采集记录</span></li></ul>
        <div v-else class="overview-empty"><Icon name="automation" /><strong>尚无接入来源</strong><p>配置 PostgreSQL 自动采集，或接入独立 Agent 与应用探针。</p><a :href="home + '/automation'">添加采集来源 <Icon name="chevron" /></a></div>
        <p v-if="sources.length" class="overview-source-note">来源数按配置统计，包含暂停的发现任务及尚未撤销的集成；此处最多预览6项。状态来自最近记录，不代表实时连通性。</p>
      </section>
    </div>

    <details v-if="overview" class="panel overview-records"><summary><span>最近登记资产</span><span>{{ overview.recentAssets.length }} 项记录 <Icon name="chevron" /></span></summary><div v-if="overview.recentAssets.length" class="table-scroll" tabindex="0" role="region" aria-label="最近登记资产"><table class="data-table overview-asset-table"><thead><tr><th scope="col">资产名称</th><th scope="col">所属系统</th><th scope="col">数据类别</th><th scope="col">业务分级</th><th scope="col">登记时间</th></tr></thead><tbody><tr v-for="asset in overview.recentAssets" :key="asset.id"><th scope="row"><a class="table-link" :href="home + '/classification/' + asset.id">{{ asset.name }}</a><span class="table-secondary">{{ asset.sourceProvider ? providerLabel(asset.sourceProvider) : '手工登记' }}</span></th><td>{{ asset.system || '—' }}</td><td>{{ assetCategoryLabels[asset.category || ''] || '待分类' }}</td><td>{{ assetBusinessLevelLabels[asset.businessLevel || ''] || '待分级' }}</td><td class="date-text">{{ formatDate(asset.createdAt) }}</td></tr></tbody></table></div><p v-else class="empty-state">完成采集或登记首项资产后，这里会显示近期记录。</p></details>
    <details v-if="overview" class="panel overview-records"><summary><span>近期导入批次</span><span>{{ overview.recentImports.length }} 项记录 <Icon name="chevron" /></span></summary><div v-if="overview.recentImports.length" class="table-scroll" role="region" tabindex="0" aria-label="近期导入批次"><table class="data-table overview-import-table"><thead><tr><th scope="col">来源与批次</th><th scope="col">新增</th><th scope="col">已有</th><th scope="col">导入时间</th></tr></thead><tbody><tr v-for="run in overview.recentImports" :key="run.id"><th scope="row">{{ run.source }}<span class="table-secondary">{{ providerLabel(run.provider) }} · {{ run.batchId }}</span></th><td>{{ run.created }}</td><td>{{ run.existing }}</td><td class="date-text">{{ formatDate(run.createdAt) }}</td></tr></tbody></table></div><p v-else class="empty-state">完成首次采集后，这里会显示导入批次和写入数量。</p></details>
  </div>
</template>
