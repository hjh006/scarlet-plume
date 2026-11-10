<script setup lang="ts">
import { computed } from 'vue'
import { assetBusinessLevelLabels, assetCategoryLabels, formatDate, providerLabel, type Asset, type Overview } from './api'
import { checkStatusLabels } from './workflow'
import Icon from './Icon.vue'
import './overview.css'

const props = defineProps<{ home: string; overview: Overview | null }>()
const metrics = [
  { key: 'assets', label: '数据资产', route: 'assets', icon: 'assets', tone: 'blue' },
  { key: 'activities', label: '处理活动', route: 'activities', icon: 'activities', tone: 'teal' },
  { key: 'checks', label: '监管检查', route: 'checks', icon: 'checks', tone: 'violet' },
  { key: 'inReview', label: '待独立复核', route: 'checks', icon: 'reports', tone: 'coral' },
] as const
const statuses = [
  { key: 'draft', label: checkStatusLabels.draft },
  { key: 'inReview', label: checkStatusLabels.in_review },
  { key: 'needsWork', label: checkStatusLabels.needs_work },
  { key: 'verified', label: checkStatusLabels.verified },
] as const
const entrances = [
  { route: 'automation', icon: 'automation', label: '自动采集', detail: '定期发现、识别并登记数据资产' },
  { route: 'api-security', icon: 'api-security', label: 'API 安全', detail: '识别接口、鉴权声明与疑似敏感字段' },
  { route: 'reports', icon: 'reports', label: '合规报告', detail: '查看检查汇总、取证记录和自动归档' },
] as const
const flow = ['数据源', '识别', '取证', '归档']

const statusScale = computed(() => Math.max(4, ...statuses.map(status => props.overview?.counts[status.key] ?? 0)))

function assetKind(asset: Asset) {
  if (asset.sourceProvider === 'postgresql') return '数据库表'
  if (asset.sourceProvider === 'openapi') return 'API 操作'
  return asset.sourceProvider ? providerLabel(asset.sourceProvider) : '手工登记'
}
</script>

<template>
  <div class="workspace-overview">
    <header class="overview-hero">
      <div class="overview-hero-copy">
        <p class="overview-hero-kicker">Scarlet Plume / Workspace</p>
        <h1>数据安全工作台</h1>
        <p class="overview-hero-subtitle">从资产识别到监管取证，追踪每一次变化。</p>
        <a class="button overview-hero-action" :href="home + '/checks'">查看监管检查 <Icon name="chevron" /></a>
      </div>
      <ol class="overview-hero-flow" aria-label="数据治理流程示意">
        <li v-for="stage in flow" :key="stage">{{ stage }}</li>
      </ol>
    </header>

    <template v-if="overview">
      <ul class="overview-metrics" aria-label="治理指标">
        <li v-for="metric in metrics" :key="metric.key" class="overview-metric">
          <a :href="home + '/' + metric.route" class="overview-metric-link">
            <span>{{ metric.label }}<strong>{{ overview.counts[metric.key] }}</strong></span>
            <span class="overview-metric-icon" :data-tone="metric.tone"><Icon :name="metric.icon" /></span>
          </a>
        </li>
      </ul>

      <div class="overview-columns">
        <section class="panel overview-panel" aria-labelledby="overview-checks-title">
          <div class="overview-panel-heading">
            <h2 id="overview-checks-title">监管检查</h2>
            <a class="quiet-link" :href="home + '/checks'" aria-label="查看全部监管检查">查看全部 <Icon name="chevron" /></a>
          </div>
          <ul class="overview-statuses" aria-label="监管检查状态分布">
            <li v-for="status in statuses" :key="status.key">
              <span>{{ status.label }}</span>
              <span class="overview-status-track" aria-hidden="true">
                <span :data-state="status.key" :style="{ width: (overview.counts[status.key] / statusScale * 100) + '%' }"></span>
              </span>
              <strong>{{ overview.counts[status.key] }}</strong>
            </li>
          </ul>
        </section>

        <section class="panel overview-panel overview-entrances" aria-labelledby="overview-entrances-title">
          <div class="overview-panel-heading"><h2 id="overview-entrances-title">自动化入口</h2></div>
          <ul>
            <li v-for="entrance in entrances" :key="entrance.route">
              <a :href="home + '/' + entrance.route">
                <span class="overview-entry-icon"><Icon :name="entrance.icon" /></span>
                <span class="overview-entry-copy"><strong>{{ entrance.label }}</strong><span>{{ entrance.detail }}</span></span>
                <Icon name="chevron" />
              </a>
            </li>
          </ul>
        </section>
      </div>

      <section class="panel overview-panel overview-assets" aria-labelledby="overview-assets-title">
        <div class="overview-panel-heading">
          <h2 id="overview-assets-title">最近登记资产</h2>
          <a class="quiet-link" :href="home + '/assets'">查看全部资产 <Icon name="chevron" /></a>
        </div>
        <p v-if="overview.recentAssets?.length" class="table-hint">横向滑动查看完整资产信息。</p>
        <div v-if="overview.recentAssets?.length" class="table-scroll" tabindex="0" role="region" aria-labelledby="overview-assets-title">
          <table class="data-table overview-asset-table">
            <thead>
              <tr>
                <th scope="col">资产名称</th><th scope="col">资产类型</th><th scope="col">所属系统</th>
                <th scope="col">数据类别</th><th scope="col">业务分级</th><th scope="col">登记时间</th><th scope="col">状态</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="asset in overview.recentAssets" :key="asset.id">
                <th scope="row">
                  <span class="overview-asset-name">
                    <Icon :name="asset.sourceProvider === 'openapi' ? 'api-security' : 'assets'" />
                    <a v-if="asset.sourceProvider === 'postgresql'" class="table-link" :href="home + '/assets/' + asset.id">{{ asset.name }}</a>
                    <span v-else>{{ asset.name }}</span>
                  </span>
                </th>
                <td>{{ assetKind(asset) }}</td>
                <td>{{ asset.system || '—' }}</td>
                <td>{{ assetCategoryLabels[asset.category || ''] || '待分类' }}</td>
                <td>
                  <span v-if="assetBusinessLevelLabels[asset.businessLevel || '']" class="overview-asset-level" :data-level="asset.businessLevel">
                    {{ assetBusinessLevelLabels[asset.businessLevel || ''] }}
                  </span>
                  <span v-else class="overview-unclassified">待分级</span>
                </td>
                <td class="date-text"><time :datetime="asset.createdAt">{{ formatDate(asset.createdAt) }}</time></td>
                <td><span class="overview-registered">已登记</span></td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="empty-state">
          <strong>尚未登记资产</strong>
          <p>配置自动采集，或在资产台账登记首项数据资产。</p>
        </div>
      </section>

      <details class="panel overview-panel overview-imports">
        <summary class="overview-panel-heading"><strong>近期导入</strong><span>查看采集批次与写入记录 <Icon name="chevron" /></span></summary>
        <div v-if="overview.recentImports.length" class="table-scroll" role="region" tabindex="0" aria-label="近期导入记录">
          <table class="data-table recent-table overview-import-table">
            <thead>
              <tr><th scope="col">来源与批次</th><th scope="col">新增</th><th scope="col">已有</th><th scope="col">导入时间</th></tr>
            </thead>
            <tbody>
              <tr v-for="run in overview.recentImports" :key="run.id">
                <th scope="row">{{ run.source }}<span class="table-secondary">{{ providerLabel(run.provider) }} · {{ run.batchId }}</span></th>
                <td>{{ run.created }}</td>
                <td>{{ run.existing }}</td>
                <td class="date-text"><time :datetime="run.createdAt">{{ formatDate(run.createdAt) }}</time></td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">完成首次采集后，这里会显示导入批次和写入数量。</p>
      </details>
    </template>
  </div>
</template>
