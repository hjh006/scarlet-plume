<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { ApiError, describeError, downloadFile, formatDate, request } from './api'
import { aiHistoryLabels, aiHistoryQuery, aiHistoryScopeLabel, aiHistoryStatuses, type AIHistoryCollection, type AIHistoryFilters, type AIHistoryPage } from './ai-security-history'

const props=defineProps<{base:string;collection:AIHistoryCollection}>()
const emit=defineEmits<{sessionExpired:[];loaded:[page:AIHistoryPage];loadStart:[];loadError:[]}>()
const controller=new AbortController()
const page=ref<AIHistoryPage|null>(null),before=ref(''),history=ref<string[]>([]),loading=ref(false),exporting=ref(false),error=ref(''),feedback=ref('')
const filters=reactive<AIHistoryFilters>({kind:'',outcome:'',status:''}),applied=ref<AIHistoryFilters>({...filters})
const readAt=ref('')
const filterDraft=computed(()=>aiHistoryQuery(props.collection,filters)!==aiHistoryQuery(props.collection,applied.value))
let generation=0,pending:AbortController|undefined
function fail(cause:unknown){if(controller.signal.aborted)return;if(cause instanceof ApiError && cause.status===401)emit('sessionExpired');error.value=describeError(cause)}
async function load(cursor=before.value,remember=false,nextFilters:AIHistoryFilters=applied.value){
  const version=++generation;pending?.abort();pending=new AbortController()
  loading.value=true;error.value='';feedback.value='';emit('loadStart')
  try{const out=await request<AIHistoryPage>(props.base+'/'+props.collection+'?'+aiHistoryQuery(props.collection,nextFilters,cursor),{signal:AbortSignal.any([controller.signal,pending.signal])});if(controller.signal.aborted || version!==generation)return;if(!out || !Array.isArray(out.items))throw new Error('历史响应缺少记录数组，原分页范围保持不变。');if(remember)history.value.push(before.value);else if(!cursor)history.value=[];page.value={...out,items:out.items || []};before.value=cursor;applied.value={...nextFilters};readAt.value=new Date().toISOString();emit('loaded',page.value);return true}catch(cause){if(version===generation){fail(cause);emit('loadError')}}finally{if(version===generation)loading.value=false}
}
async function previous(){const cursor=history.value.at(-1);if(cursor!==undefined && await load(cursor))history.value.pop()}
async function reset(){Object.assign(filters,{kind:'',outcome:'',status:''});await load('',false,{...filters})}
async function exportPage(){
  if(!page.value || exporting.value || loading.value)return
  exporting.value=true;error.value='';feedback.value=''
  try{await downloadFile(props.base+'/'+props.collection+'/export?'+aiHistoryQuery(props.collection,applied.value,before.value),'scarlet-ai-'+props.collection+'-filtered-page.json',controller.signal);feedback.value='已准备该筛选分页的导出。服务端重新读取导出时点内容，文件含分页范围与继续游标；不是当前页面冻结快照或全量历史。'}catch(cause){fail(cause)}finally{exporting.value=false}
}
defineExpose({refresh:()=>load(before.value),latest:()=>load(''),unfiltered:()=>{Object.assign(filters,{kind:'',outcome:'',status:''});return load('',false,{...filters})}})
onMounted(()=>{void load('')})
onUnmounted(()=>{controller.abort();pending?.abort()})
</script>

<template>
  <div class="ai-security-history" :aria-busy="loading || exporting"><div class="ai-security-heading"><div><h2>{{ aiHistoryLabels[collection] }}</h2><p>按真实保存记录从新到旧查询。历史中缺失的版本保持未记录，不用当前配置补算历史结果。</p></div><div class="page-actions"><button class="button button-secondary" :disabled="loading || exporting" @click="load('')">最新一页</button><button class="button button-secondary" :disabled="loading || exporting || !page" @click="exportPage">{{ exporting?'正在准备导出…':'导出筛选分页' }}</button></div></div>
    <form v-if="collection!=='regression-packs'" class="ai-security-filter" @submit.prevent="load('',false,{...filters})"><template v-if="collection==='decisions'"><label class="field">请求类型<select v-model="filters.kind" :disabled="loading || exporting"><option value="">全部类型</option><option value="rag">知识检索</option><option value="model">模型请求</option><option value="tool">工具动作</option><option value="unknown">未记录类型</option></select></label><label class="field">策略决策<select v-model="filters.outcome" :disabled="loading || exporting"><option value="">全部决策</option><option value="allow">允许</option><option value="deny">拒绝</option><option value="approval_required">需要批准</option></select></label></template><label v-else class="field">保存状态<select v-model="filters.status" :disabled="loading || exporting"><option value="">全部状态</option><option v-for="(label,status) in aiHistoryStatuses[collection] || {}" :key="status" :value="status">{{ label }}</option></select></label><button class="button button-primary" :disabled="loading || exporting">查询历史</button><button class="button button-secondary" type="button" :disabled="loading || exporting" @click="reset">重置筛选</button></form>
    <p v-if="error" class="message message-error" role="alert">{{ error }}{{ page?' 下方保留上次成功载入的历史范围，本次查询未更新。':'' }}<button class="button button-secondary" :disabled="loading" @click="load()">重试读取此页</button></p><p v-if="feedback" class="message message-success" role="status">{{ feedback }}</p><p v-if="loading" class="ai-security-body" role="status">正在读取{{ aiHistoryLabels[collection] }}…</p>
    <div v-if="page" class="ai-security-body history-scope"><p>已应用：{{ aiHistoryScopeLabel(collection,applied) }} · 读取于 <time :datetime="readAt" :title="readAt">{{formatDate(readAt,true)}}</time>。</p><p v-if="filterDraft" role="status">上方筛选尚未应用；下方记录、分页与导出保留已读取范围。</p></div>
    <slot v-if="page?.items.length" :items="page.items" :page="page"></slot><div v-else-if="page && !loading && !error" class="ai-security-empty"><h3>当前筛选范围没有历史记录</h3><p>可重置筛选或返回最新一页。没有记录不表示控制已接入或实际执行成功。</p><button class="button button-secondary" @click="reset">重置并读取最新历史</button></div>
    <div v-if="page" class="ai-security-pagination"><button class="button button-secondary" :disabled="loading || exporting || !history.length" @click="previous">较新一页</button><button class="button button-secondary" :disabled="loading || exporting || !page.hasMore || !page.nextCursor" @click="load(page.nextCursor,true)">较早一页</button><p>第 {{ history.length+1 }} 页 · 本页 {{ page.items.length }} 条 · 每页最多 {{ page.limit || 100 }} 条 · {{ page.hasMore?'仍有较早记录':'当前筛选已到最早记录' }}</p><p>已应用：{{ aiHistoryScopeLabel(collection,applied) }}。分页与导出都保留该范围。</p></div>
  </div>
</template>

<style scoped>
.history-scope{border-bottom:1px solid var(--line);background:var(--surface-subtle)}.history-scope p{margin:0;font-size:13px;color:var(--secondary)}.history-scope p+p{margin-top:8px}.ai-security-history .message button{display:block;margin-top:10px}
</style>
