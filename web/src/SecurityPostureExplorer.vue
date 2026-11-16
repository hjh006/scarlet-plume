<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ApiError, describeError, downloadFile, formatDate, request, workspacePath, type Workspace } from './api'

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ sessionExpired: [] }>()
interface Finding { key:string; assetId:number; assetName:string; severity:string; code:string; title:string; detail:string; sourceRef:string; observedAt?:string; evidenceState:string }
interface PosturePage { items:Finding[]; assets:{id:number;name:string;sourceRef:string;findingCount:number}[]; matchingAssets:number; watermarkId:number; generatedAt:string; hasMore:boolean; nextCursor?:string; coverage:string }
const base = workspacePath(props.workspace.slug) + '/security-platform/posture'
const home = '#/' + encodeURIComponent(props.workspace.slug)
const controller = new AbortController()
const filters = reactive({ q:'', severity:'', code:'' })
const applied = ref({ ...filters })
const data = ref<PosturePage>()
const cursor = ref(''), previous = ref<string[]>([])
const loading = ref(false), exporting = ref(false), error = ref('')
let sequence = 0
const codes:Record<string,string> = { classification_review:'分类需要确认', missing_owner:'缺少责任团队', permission_coverage:'缺少权限来源', stale_permission_source:'权限证据待更新', public_select:'PUBLIC 读取条件', delegable_select:'可转授读取权限' }
const evidence:Record<string,string> = { needs_verification:'待核验', registered:'人工登记', unknown:'尚未取得', historical:'历史证据', observed:'来源已观察' }
function query(target:string, values:typeof filters) {
  const q = new URLSearchParams({ limit:'50', ...values })
  if (target) q.set('cursor', target)
  return base + '?' + q
}
function failure(value:unknown) {
  if (controller.signal.aborted) return
  if (value instanceof ApiError && value.status === 401) emit('sessionExpired')
  else error.value = describeError(value)
}
async function load(target='', values={...applied.value}, trail:string[]=[]) {
  const current = ++sequence
  loading.value = true; error.value = ''
  try {
    const result = await request<PosturePage>(query(target, values), { signal:controller.signal })
    if (current !== sequence) return
    data.value = result; applied.value = {...values}; cursor.value = target; previous.value = trail
  } catch (value) { if (current === sequence) failure(value) }
  finally { if (current === sequence) loading.value = false }
}
async function exportPage() {
  if (!data.value || loading.value || exporting.value) return
  exporting.value = true; error.value = ''
  try { await downloadFile(query(cursor.value,applied.value), 'scarlet-posture-page.json', controller.signal) }
  catch (value) { failure(value) }
  finally { exporting.value = false }
}
onMounted(() => load())
onBeforeUnmount(() => { sequence++; controller.abort() })
</script>

<template>
  <section class="panel posture-explorer" :aria-busy="loading">
    <div class="ai-security-heading"><div><h2>逐页核对全部资产的数据风险</h2><p>按资产范围读取当前分类与权限依据，可继续检查概览前 250 项以外的资产。</p></div><a class="button button-secondary" :href="home+'/classification'">核对分类依据</a></div>
    <form class="filter-bar posture-filters" @submit.prevent="load('',{...filters})">
      <label class="field">资产、系统或责任团队<input v-model.trim="filters.q" maxlength="120" placeholder="搜索资产范围"></label>
      <label class="field">风险等级<select v-model="filters.severity"><option value="">全部等级</option><option value="high">高</option><option value="medium">中</option></select></label>
      <label class="field">风险类型<select v-model="filters.code"><option value="">全部类型</option><option v-for="(label,code) in codes" :key="code" :value="code">{{ label }}</option></select></label>
      <button type="submit" class="button button-primary" :disabled="loading">{{ loading?'正在核对…':'查询并重置范围' }}</button>
    </form>
    <div v-if="error" class="message message-error" role="alert"><p>{{ error }}{{ data?' 下方保留上次成功读取的结果。':'' }}</p><button type="button" class="button button-secondary" :disabled="loading" @click="load(cursor,{...applied},[...previous])">重试当前页</button></div>
    <p v-if="loading && !data" class="empty-state" role="status">正在核对资产与来源证据…</p>
    <template v-if="data">
      <div class="scope-strip" role="status"><span>所选范围 {{ data.matchingAssets.toLocaleString() }} 项资产</span><span>本页核对 {{ data.assets.length }} 项</span><span>本页匹配 {{ data.items.length }} 条风险</span><span>读取于 {{ formatDate(data.generatedAt) }}</span></div>
      <div v-if="data.items.length" class="table-scroll" tabindex="0" role="region" aria-label="本页数据风险与来源证据"><table class="data-table"><thead><tr><th scope="col">等级</th><th scope="col">资产与来源</th><th scope="col">发现与依据</th><th scope="col">证据状态</th></tr></thead><tbody><tr v-for="item in data.items" :key="item.key"><td><span class="badge" :class="item.severity==='high'?'status-needs-work':'status-draft'">{{ item.severity==='high'?'高':'中' }}</span></td><th scope="row"><a :href="home+'/assets/'+item.assetId">{{ item.assetName }}</a><span class="table-secondary">{{ item.sourceRef || '缺少来源引用' }}</span></th><td><strong>{{ item.title }}</strong><span class="table-secondary">{{ item.detail }}</span><span class="table-secondary">{{ codes[item.code] || item.code }}</span></td><td>{{ evidence[item.evidenceState] || item.evidenceState }}<span class="table-secondary">{{ item.observedAt?formatDate(item.observedAt):'未提供观察时点' }}</span></td></tr></tbody></table></div>
      <div v-else class="empty-state"><h3>{{ data.assets.length?'本页没有匹配筛选条件的风险':'此范围暂无资产' }}</h3><p>{{ data.hasMore?'仍有后续资产待核对，请继续下一页。':'未列示发现不能证明所有来源已接入或数据安全。' }}</p><a v-if="!data.assets.length" :href="home+'/discovery'">接入数据来源</a></div>
      <details v-if="data.assets.length" class="posture-assets"><summary>本页实际核对的 {{ data.assets.length }} 项资产</summary><ul><li v-for="asset in data.assets" :key="asset.id"><a :href="home+'/assets/'+asset.id">{{ asset.name }}</a><span> · 全部已列示风险 {{ asset.findingCount }} 条（筛选前）</span></li></ul></details>
      <div class="posture-footer"><p>{{ data.coverage }}<span class="table-secondary">本轮资产上界 #{{ data.watermarkId }}；刷新范围后纳入新增资产。此处导出仅含当前页。</span></p><div class="actions"><button type="button" class="button button-secondary" :disabled="loading || !previous.length" @click="load(previous[previous.length-1]||'',{...applied},previous.slice(0,-1))">上一页</button><button type="button" class="button button-secondary" :disabled="loading || !data.hasMore || !data.nextCursor" @click="load(data.nextCursor,{...applied},[...previous,cursor])">下一页</button><button type="button" class="button button-secondary" :disabled="loading || exporting" @click="exportPage">{{ exporting?'正在导出…':'导出本页当前证据' }}</button></div></div>
    </template>
  </section>
</template>

<style scoped>
.posture-explorer { min-width:0; overflow:hidden; }
.posture-filters { align-items:end; }
.posture-filters .field { margin:0; flex:1 1 180px; min-width:0; }
.posture-filters input,.posture-filters select { width:100%; }
.posture-explorer>.message { margin:16px 18px; }
.posture-explorer .data-table { min-width:680px; }
.posture-explorer :is(td,th) { vertical-align:top; overflow-wrap:anywhere; }
.posture-explorer th:nth-child(2) { min-width:160px; width:24%; }
.posture-assets { padding:16px 18px; border-top:1px solid var(--line); font-size:14px; }
.posture-assets summary { min-height:30px; cursor:pointer; color:var(--accent); }
.posture-assets ul { display:grid; gap:8px; padding:12px 0 0 20px; overflow-wrap:anywhere; }
.posture-footer { display:flex; flex-wrap:wrap; align-items:center; gap:16px; padding:18px; border-top:1px solid var(--line); }
.posture-footer>p { flex:1 1 340px; margin:0; color:var(--secondary); font-size:13px; }
.posture-footer .actions { display:flex; flex-wrap:wrap; gap:8px; }
</style>
