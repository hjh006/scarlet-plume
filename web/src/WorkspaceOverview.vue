<script setup lang="ts">
import { computed } from 'vue'
import { assetBusinessLevelLabels, assetCategoryLabels, formatDate, providerLabel, type Overview } from './api'
import { checkStatusLabels } from './workflow'
import { runtimeRuleLabels, runtimeRiskSeverityLabels } from './runtime-findings'
import Icon from './Icon.vue'
import './overview.css'

const props = defineProps<{ home: string; overview: Overview | null; loading?: boolean }>()
const emit = defineEmits<{ refresh: [] }>()
const metrics = computed(() => [
  { label: '接入来源', value: props.overview?.counts.activeSources, route: 'automation', icon: 'assets', tone: 'teal' },
  { label: '数据资产', value: props.overview?.counts.assets, route: 'assets', icon: 'classification', tone: 'violet' },
  { label: '待处置发现', value: props.overview?.counts.openRuntimeFindings, route: 'api-security/runtime', icon: 'warning', tone: 'coral' },
  { label: '待复核检查', value: props.overview?.counts.inReview, route: 'checks', icon: 'checks', tone: 'blue' },
])
const tasks = computed(() => [
  { title: '处置运行时发现', count: props.overview?.counts.openRuntimeFindings, detail: '核对成功响应与接口契约的差异', route: 'api-security/runtime', icon: 'warning', tone: 'coral' },
  { title: '独立复核监管检查', count: props.overview?.counts.inReview, detail: '检查已提交，等待独立复核', route: 'checks', icon: 'checks', tone: 'blue' },
  { title: '确认数据分类分级', count: props.overview?.counts.pendingClassification, detail: '复核识别结果并补充分级依据', route: 'classification', icon: 'classification', tone: 'violet' },
])
const sources = computed(() => props.overview?.collectionSources ?? [])
const findings = computed(() => props.overview?.recentFindings ?? [])
const sourceStates: Record<string, string> = {
  collecting: '采集中', queued: '已排队', paused: '已暂停', enabled: '已启用', failed: '采集失败', never_run: '待首次采集', observing: '已接入',
}
const destinations = computed(() => [
  { label: '分类识别', detail: `${props.overview?.counts.assets ?? 0} 项资产`, icon: 'classification', route: 'classification', tone: 'violet' },
  { label: '监管取证', detail: `${props.overview?.counts.checks ?? 0} 项检查`, icon: 'checks', route: 'checks', tone: 'blue' },
  { label: '报告归档', detail: '查看真实检查与证据', icon: 'reports', route: 'reports/archives', tone: 'teal' },
])
function sourceTarget(kind: string) { return props.home + (kind === 'runtime' ? '/api-security/runtime' : '/automation') }
function sourceIcon(provider: string) { return ['otlp', 'gateway'].includes(provider) ? 'api-security' : provider === 'postgresql' ? 'assets' : 'integrations' }
function sourceProvider(provider: string) { return provider === 'otlp' ? '应用探针 · OTLP' : provider === 'gateway' ? '独立 Agent / 网关' : provider === 'feishu' ? '飞书' : providerLabel(provider) }
</script>

<template>
  <div class="workspace-overview" :aria-busy="loading">
    <header class="overview-heading">
      <div>
        <h1>数据安全工作台</h1>
        <p v-if="overview?.generatedAt">工作区数据更新于 <time :datetime="overview.generatedAt">{{ formatDate(overview.generatedAt) }}</time></p>
        <p v-else>查看当前工作区的数据接入、治理进度和待办事项。</p>
      </div>
      <div class="page-actions">
        <button type="button" class="button button-secondary" :disabled="loading" @click="emit('refresh')"><Icon name="automation" />{{ loading ? '刷新中…' : '刷新数据' }}</button>
        <a class="button button-primary" :href="home + '/reports'"><Icon name="reports" />查看报告</a>
      </div>
    </header>

    <ul class="overview-metrics" aria-label="工作区治理指标">
      <li v-for="metric in metrics" :key="metric.label">
        <a :href="home + '/' + metric.route" class="overview-metric-link">
          <span class="overview-tone-icon" :data-tone="metric.tone"><Icon :name="metric.icon" /></span>
          <span class="overview-metric-copy"><strong>{{ metric.value ?? '—' }}</strong><span>{{ metric.label }}</span></span>
          <Icon name="chevron" />
        </a>
      </li>
    </ul>

    <div class="overview-primary-grid">
      <section class="panel overview-flow-panel" aria-labelledby="overview-flow-title">
        <div class="overview-panel-heading"><h2 id="overview-flow-title">数据治理链路</h2><a class="quiet-link" :href="home + '/data-flows'">查看数据流转 <Icon name="chevron" /></a></div>
        <div v-if="overview" class="overview-flow-canvas">
          <div class="overview-feather" aria-hidden="true"></div>
          <ul class="overview-flow-sources" aria-label="已配置的采集来源">
            <li v-for="source in sources.slice(0, 4)" :key="source.kind + ':' + source.id">
              <a class="overview-flow-node" :href="sourceTarget(source.kind)"><Icon :name="sourceIcon(source.provider)" /><span><strong>{{ source.name }}</strong><small>{{ sourceProvider(source.provider) }}</small></span><span class="overview-flow-port" aria-hidden="true"></span></a>
            </li>
            <li v-if="!sources.length" class="overview-flow-empty"><Icon name="assets" /><span>尚无接入来源<a :href="home + '/automation'">配置自动采集</a></span></li>
          </ul>
          <div class="overview-flow-core"><Icon name="data-flows" /><span>赤翎治理引擎<small>识别 · 检查 · 取证</small></span></div>
          <ul class="overview-flow-destinations" aria-label="治理业务入口">
            <li v-for="destination in destinations" :key="destination.route"><a class="overview-flow-node" :data-tone="destination.tone" :href="home + '/' + destination.route"><span class="overview-flow-port" aria-hidden="true"></span><Icon :name="destination.icon" /><span><strong>{{ destination.label }}</strong><small>{{ destination.detail }}</small></span></a></li>
          </ul>
        </div>
        <div v-else class="overview-flow-loading" role="status">正在读取治理链路…</div>
        <div class="overview-flow-legend"><span class="overview-legend-node">已配置来源</span><span class="overview-legend-line">治理流程</span><p>流程示意；实际观测的接口与数据库关联请查看数据流转。</p></div>
      </section>

      <section class="panel overview-tasks" aria-labelledby="overview-tasks-title">
        <div class="overview-panel-heading"><h2 id="overview-tasks-title">待办事项</h2><span class="overview-caption">当前工作区</span></div>
        <ul>
          <li v-for="task in tasks" :key="task.route"><a :href="home + '/' + task.route"><span class="overview-tone-icon" :data-tone="task.tone"><Icon :name="task.icon" /></span><span class="overview-task-copy"><strong>{{ task.title }}</strong><span><b>{{ task.count ?? '—' }}</b> 项 · {{ task.detail }}</span></span><Icon name="chevron" /></a></li>
        </ul>
        <div v-if="overview" class="overview-review-summary"><span>检查闭环</span><strong>{{ overview.counts.verified }} <small>/ {{ overview.counts.checks }}</small></strong><a :href="home + '/checks'">查看检查台账 <Icon name="chevron" /></a></div>
      </section>
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
        <p v-if="sources.length" class="overview-source-note">状态来自任务配置与最近记录，不代表实时连通性。</p>
      </section>
    </div>

    <details v-if="overview" class="panel overview-records"><summary><span>最近登记资产</span><span>{{ overview.recentAssets.length }} 项记录 <Icon name="chevron" /></span></summary><div v-if="overview.recentAssets.length" class="table-scroll" tabindex="0" role="region" aria-label="最近登记资产"><table class="data-table overview-asset-table"><thead><tr><th scope="col">资产名称</th><th scope="col">所属系统</th><th scope="col">数据类别</th><th scope="col">业务分级</th><th scope="col">登记时间</th></tr></thead><tbody><tr v-for="asset in overview.recentAssets" :key="asset.id"><th scope="row"><a class="table-link" :href="home + '/classification/' + asset.id">{{ asset.name }}</a><span class="table-secondary">{{ asset.sourceProvider ? providerLabel(asset.sourceProvider) : '手工登记' }}</span></th><td>{{ asset.system || '—' }}</td><td>{{ assetCategoryLabels[asset.category || ''] || '待分类' }}</td><td>{{ assetBusinessLevelLabels[asset.businessLevel || ''] || '待分级' }}</td><td class="date-text">{{ formatDate(asset.createdAt) }}</td></tr></tbody></table></div><p v-else class="empty-state">完成采集或登记首项资产后，这里会显示近期记录。</p></details>
    <details v-if="overview" class="panel overview-records"><summary><span>近期导入批次</span><span>{{ overview.recentImports.length }} 项记录 <Icon name="chevron" /></span></summary><div v-if="overview.recentImports.length" class="table-scroll" role="region" tabindex="0" aria-label="近期导入批次"><table class="data-table overview-import-table"><thead><tr><th scope="col">来源与批次</th><th scope="col">新增</th><th scope="col">已有</th><th scope="col">导入时间</th></tr></thead><tbody><tr v-for="run in overview.recentImports" :key="run.id"><th scope="row">{{ run.source }}<span class="table-secondary">{{ providerLabel(run.provider) }} · {{ run.batchId }}</span></th><td>{{ run.created }}</td><td>{{ run.existing }}</td><td class="date-text">{{ formatDate(run.createdAt) }}</td></tr></tbody></table></div><p v-else class="empty-state">完成首次采集后，这里会显示导入批次和写入数量。</p></details>
  </div>
</template>
