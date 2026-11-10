<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ApiError, describeError, formatDate, request } from './api'

interface Counts {
  requests: number
  responses4xx: number
  responses5xx: number
}

interface Monitor {
  hours: number
  startAt: string
  endAt: string
  totals: Counts
  points: (Counts & { at: string })[]
  topRoutes: (Counts & { method: string; route: string })[]
  topRoutesPartial?: boolean
}

const props = defineProps<{ apiPath: string; auditHref: string; sources: { id: number; name: string; service: string }[]; refresh: number }>()
const emit = defineEmits<{ sessionExpired: [] }>()
const hours = ref(1)
const sourceId = ref('')
const data = ref<Monitor | null>(null)
const loading = ref(false)
const error = ref('')
let pending: AbortController | undefined
const series = [
  { key: 'requests', label: '观测请求', tone: 'requests' },
  { key: 'responses4xx', label: 'HTTP 4xx', tone: 'client' },
  { key: 'responses5xx', label: 'HTTP 5xx', tone: 'server' },
] as const
const maximum = computed(() => Math.max(1, ...(data.value?.points.map(point => point.requests) ?? [])))
const lines = computed(() => {
  const points = data.value?.points ?? []
  return series.map(item => ({
    ...item,
    points: points.map((point, index) => {
      const x = points.length === 1 ? 320 : index / (points.length - 1) * 640
      const y = 145 - point[item.key] / maximum.value * 135
      return `${x},${y}`
    }).join(' '),
  }))
})

async function load() {
  pending?.abort()
  const operation = new AbortController()
  pending = operation
  loading.value = true
  error.value = ''
  data.value = null
  const query = new URLSearchParams({ hours: String(hours.value) })
  if (sourceId.value) query.set('sourceId', sourceId.value)
  try {
    const updated = await request<Monitor>(props.apiPath + '/monitor?' + query, { signal: operation.signal })
    if (!operation.signal.aborted) data.value = updated
  } catch (failure) {
    if (operation.signal.aborted) return
    if (failure instanceof ApiError && failure.status === 401) emit('sessionExpired')
    else error.value = describeError(failure)
  } finally {
    if (!operation.signal.aborted) loading.value = false
  }
}

watch([hours, sourceId, () => props.refresh], () => { void load() }, { immediate: true })
onBeforeUnmount(() => pending?.abort())
</script>

<template>
  <section class="panel api-panel runtime-monitor" aria-labelledby="runtime-monitor-title" :aria-busy="loading">
    <div class="api-panel-heading">
      <div>
        <h2 id="runtime-monitor-title">API 流量监测</h2>
        <p>按时间和来源汇总实际观测。多处接入可能重复观测同一次请求。</p>
      </div>
      <div class="page-actions">
        <a class="button button-secondary" :href="auditHref">查看请求审计</a>
        <button type="button" class="button button-secondary" :disabled="loading" @click="load">刷新监测</button>
      </div>
    </div>
    <div class="runtime-monitor-filters">
      <div class="field">
        <label for="runtime-monitor-hours">监测范围</label>
        <select id="runtime-monitor-hours" v-model.number="hours">
          <option :value="1">最近 1 小时</option><option :value="6">最近 6 小时</option><option :value="24">最近 24 小时</option>
        </select>
      </div>
      <div class="field">
        <label for="runtime-monitor-source">监测来源</label>
        <select id="runtime-monitor-source" v-model="sourceId">
          <option value="">全部来源</option>
          <option v-for="source in sources" :key="source.id" :value="String(source.id)">{{ source.name }} · {{ source.service }}</option>
        </select>
      </div>
    </div>
    <p v-if="error" class="message message-error" role="alert">{{ error }}</p>
    <p v-else-if="loading" class="empty-state" role="status">正在读取监测数据…</p>
    <template v-else-if="data">
      <dl class="report-counts runtime-monitor-counts" aria-label="所选时段请求统计">
        <div v-for="item in series" :key="item.key"><dt>{{ item.label }}</dt><dd>{{ data.totals[item.key] }}</dd></div>
      </dl>
      <p class="runtime-monitor-period">
        <time :datetime="data.startAt">{{ formatDate(data.startAt) }}</time>
        至 <time :datetime="data.endAt">{{ formatDate(data.endAt) }}</time>
      </p>
      <template v-if="data.totals.requests && data.points.length">
        <figure class="runtime-monitor-chart">
          <figcaption>请求趋势</figcaption>
          <ul aria-label="图例">
            <li v-for="item in series" :key="item.key"><span :data-series="item.tone"></span>{{ item.label }}</li>
          </ul>
          <div class="runtime-monitor-plot" aria-hidden="true">
            <div class="runtime-monitor-axis"><span>{{ maximum }}</span><span>0</span></div>
            <svg viewBox="0 0 640 155" preserveAspectRatio="none">
              <path d="M0 10h640M0 77.5h640M0 145h640" class="runtime-chart-grid" vector-effect="non-scaling-stroke" />
              <polyline v-for="line in lines" :key="line.key" :points="line.points" :data-series="line.tone" vector-effect="non-scaling-stroke" />
            </svg>
          </div>
        </figure>
        <details class="runtime-monitor-details">
          <summary>查看分时数据</summary>
          <div class="table-scroll" role="region" aria-label="分时请求统计" tabindex="0">
            <table class="data-table runtime-monitor-table">
              <thead><tr><th scope="col">时间区间起点</th><th scope="col">观测请求</th><th scope="col">HTTP 4xx</th><th scope="col">HTTP 5xx</th></tr></thead>
              <tbody>
                <tr v-for="point in data.points" :key="point.at">
                  <th scope="row"><time :datetime="point.at">{{ formatDate(point.at) }}</time></th>
                  <td>{{ point.requests }}</td><td>{{ point.responses4xx }}</td><td>{{ point.responses5xx }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </details>
      </template>
      <p v-else class="empty-state">所选时段暂无请求观测。</p>
      <template v-if="data.topRoutes.length">
        <h3 id="runtime-monitor-top">请求较多的路由</h3>
        <p v-if="data.topRoutesPartial" class="runtime-monitor-period">按仍在保留范围内的请求记录汇总路由。</p>
        <div class="api-table-frame" role="region" aria-labelledby="runtime-monitor-top" tabindex="0">
          <table class="api-table" role="table">
            <thead role="rowgroup"><tr role="row">
              <th scope="col" role="columnheader">路由模板</th><th scope="col" role="columnheader">观测请求</th>
              <th scope="col" role="columnheader">HTTP 4xx</th><th scope="col" role="columnheader">HTTP 5xx</th>
            </tr></thead>
            <tbody role="rowgroup">
              <tr v-for="route in data.topRoutes" :key="route.method + ' ' + route.route" role="row">
                <th scope="row" role="rowheader"><span class="api-endpoint"><span class="api-method mono">{{ route.method }}</span><code class="api-path mono">{{ route.route }}</code></span></th>
                <td role="cell"><span class="api-cell-label" aria-hidden="true">观测请求</span>{{ route.requests }}</td>
                <td role="cell"><span class="api-cell-label" aria-hidden="true">HTTP 4xx</span>{{ route.responses4xx }}</td>
                <td role="cell"><span class="api-cell-label" aria-hidden="true">HTTP 5xx</span>{{ route.responses5xx }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </template>
  </section>
</template>

<style scoped>
.runtime-monitor-filters { display: grid; grid-template-columns: minmax(8rem, .4fr) minmax(0, 1fr); gap: .65rem; max-width: 34rem; margin-bottom: .75rem; }
.runtime-monitor-filters .field { min-width: 0; margin: 0; }
.runtime-monitor-counts { border: 1px solid var(--api-line); border-radius: .4rem; background: var(--surface-subtle); }
.runtime-monitor-counts div { border-bottom: 0; }
.runtime-monitor-period { margin: .6rem 0; color: var(--secondary); font-size: .81rem; }
.runtime-monitor-chart { margin: .7rem 0; padding: .75rem; border: 1px solid var(--api-line); border-radius: .4rem; }
.runtime-monitor-chart figcaption, .runtime-monitor-details summary { font-weight: 700; }
.runtime-monitor-chart ul { display: flex; flex-wrap: wrap; gap: .3rem 1rem; margin: .5rem 0; padding: 0; list-style: none; }
.runtime-monitor-chart li { display: flex; align-items: center; gap: .32rem; color: var(--secondary); font-size: .81rem; }
.runtime-monitor-chart li span { width: .75rem; height: .18rem; background: var(--series-ink); }
.runtime-monitor-chart [data-series="requests"] { --series-ink: #1756c4; }
.runtime-monitor-chart [data-series="client"] { --series-ink: #a75026; }
.runtime-monitor-chart [data-series="server"] { --series-ink: #7955b4; }
.runtime-monitor-plot { display: grid; grid-template-columns: auto minmax(0, 1fr); align-items: stretch; gap: .45rem; }
.runtime-monitor-axis { display: flex; flex-direction: column; justify-content: space-between; color: var(--secondary); font-size: .75rem; font-variant-numeric: tabular-nums; }
.runtime-monitor-chart svg { display: block; width: 100%; height: 8.2rem; overflow: visible; }
.runtime-monitor-chart polyline { fill: none; stroke: var(--series-ink); stroke-width: 2; stroke-linejoin: round; stroke-linecap: round; }
.runtime-chart-grid { fill: none; stroke: var(--line); stroke-width: 1; }
.runtime-monitor-details { margin: .65rem 0 1rem; }
.runtime-monitor-details summary { min-height: 2.5rem; padding: .5rem 0; color: var(--accent); cursor: pointer; }
.runtime-monitor-table { min-width: 27rem; }
.runtime-monitor h3 { margin: .9rem 0 .55rem; }
@media (max-width: 43.99rem) {
  .runtime-monitor-filters { grid-template-columns: minmax(0, 1fr); }
  .runtime-monitor-counts { grid-template-columns: repeat(3, minmax(0, 1fr)); }
  .runtime-monitor-counts div { padding-inline: .45rem; }
}
</style>
