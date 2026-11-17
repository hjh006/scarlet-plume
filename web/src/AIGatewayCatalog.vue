<script setup lang="ts">
import {computed,onMounted,onUnmounted,ref} from 'vue'
import {ApiError,describeError,formatDate,request,workspacePath,type Workspace} from './api'
import {aiRecords,aiString,type AISecurityBundle} from './ai-security'
const props=defineProps<{workspace:Workspace;kind:'applications'|'models'}>()
const emit=defineEmits<{sessionExpired:[]}>()
const bundle=ref<AISecurityBundle|null>(null),loading=ref(true),error=ref(''),readAt=ref('')
const controller=new AbortController()
const records=computed(()=>bundle.value?aiRecords(bundle.value.configuration,props.kind):[])
const home='#/'+encodeURIComponent(props.workspace.slug)
async function load(){loading.value=true;error.value='';try{bundle.value=await request<AISecurityBundle>(workspacePath(props.workspace.slug)+'/ai-security',{signal:controller.signal});if(!controller.signal.aborted)readAt.value=new Date().toISOString()}catch(cause){if(controller.signal.aborted)return;if(cause instanceof ApiError&&cause.status===401)emit('sessionExpired');error.value=describeError(cause)}finally{loading.value=false}}
function values(item:Record<string,unknown>,key:string):string[]{return Array.isArray(item[key])?(item[key] as unknown[]).filter((value):value is string=>typeof value==='string'):[]}
onMounted(load)
onUnmounted(()=>controller.abort())
</script>
<template>
  <section class="panel" :aria-busy="loading"><div class="ai-security-heading"><div><h2>{{ kind==='applications'?'统一应用授权目录':'统一模型目标目录' }}</h2><p>复用 AI 运行时安全的可信目录，网关不会另建一套可绕过原授权的对象。目录登记与实际路由接入分别验收。</p></div><div class="page-actions"><button class="button button-secondary" :disabled="loading" @click="load">重新读取目录</button><a class="button button-secondary" :href="home+'/ai-security?tab=configuration'">打开可信目录配置</a></div></div>
    <p v-if="error" class="message message-error" role="alert">{{ error }}{{ bundle?' 下方保留上次成功读取的目录，本次刷新未更新。':'' }}</p><p v-if="loading" class="ai-security-body" role="status">正在读取已有可信目录…</p>
    <div v-if="bundle" class="ai-security-body"><p v-if="readAt" class="table-secondary">目录读取于 {{formatDate(readAt,true)}}。</p><p class="table-secondary">已保存修订 {{ bundle.revision }} · 已发布修订 {{ bundle.publishedRevision || '尚未发布' }}。{{ bundle.revision!==bundle.publishedRevision?'当前显示包含尚未发布的配置；运行授权以客户侧实际采用版本为准。':'仍需核对客户侧实际采用的目录摘要。' }}</p></div>
    <div v-if="records.length" class="table-scroll" role="region" :aria-label="kind==='applications'?'应用授权目录':'模型目标目录'" tabindex="0"><table class="data-table ai-catalog-table"><thead><tr><th scope="col">名称与标识</th><th scope="col">目录状态</th><th scope="col">{{ kind==='applications'?'主体授权':'固定上游映射' }}</th><th scope="col">{{ kind==='applications'?'模型与工具授权':'地域及允许应用' }}</th></tr></thead><tbody><tr v-for="item in records" :key="aiString(item,'id')"><th scope="row">{{ aiString(item,'name','id') }}<span class="table-secondary">{{ aiString(item,'id') }}</span></th><td>{{ item.enabled?'已启用':'未启用' }}</td><td v-if="kind==='applications'"><details v-if="values(item,'principalIds').length"><summary>{{ values(item,'principalIds').length }} 个登记主体</summary><div class="catalog-scope-scroll" role="region" :aria-label="aiString(item,'id')+' 的登记主体'" tabindex="0"><ol><li v-for="value in values(item,'principalIds')" :key="value"><code>{{value}}</code></li></ol></div></details><span v-else>未登记授权主体</span></td><td v-else>{{ aiString(item,'provider') }} · {{ aiString(item,'upstreamModel','id') }}<span class="table-secondary">端点引用 {{ aiString(item,'endpointRef') }}</span></td><td v-if="kind==='applications'"><details v-for="[key,label] in [['modelIds','模型'],['toolIds','工具']]" :key="key"><summary>{{ values(item,key!).length }} 个登记{{label}}</summary><div v-if="values(item,key!).length" class="catalog-scope-scroll" role="region" :aria-label="aiString(item,'id')+' 的登记'+label" tabindex="0"><ol><li v-for="value in values(item,key!)" :key="value"><code>{{value}}</code></li></ol></div><p v-else class="table-secondary">未登记相应授权。</p></details></td><td v-else>{{ aiString(item,'region') }}<details><summary>{{ values(item,'allowedApplicationIds').length }} 个登记允许应用</summary><div v-if="values(item,'allowedApplicationIds').length" class="catalog-scope-scroll" role="region" :aria-label="aiString(item,'id')+' 的允许应用'" tabindex="0"><ol><li v-for="value in values(item,'allowedApplicationIds')" :key="value"><code>{{value}}</code></li></ol></div><p v-else class="table-secondary">未登记允许应用。</p></details></td></tr></tbody></table></div>
    <p v-else-if="!loading && !error" class="empty-state">尚无{{ kind==='applications'?'应用授权':'模型目标' }}记录，请在可信目录配置中添加并完成发布。</p>
  </section>
</template>

<style scoped>
.ai-catalog-table{min-width:900px;table-layout:fixed}.ai-catalog-table th:first-child{width:23%}.ai-catalog-table th:nth-child(2){width:13%}.ai-catalog-table th:nth-child(3){width:30%}.ai-catalog-table :is(th,td){overflow-wrap:anywhere}.ai-catalog-table summary{min-height:44px;padding:9px 0;cursor:pointer;color:var(--accent)}.catalog-scope-scroll{max-height:240px;overflow:auto;border:1px solid var(--line);border-radius:6px}.catalog-scope-scroll ol{margin:0;padding:0 12px 0 36px}.catalog-scope-scroll li{padding:8px 0;border-bottom:1px solid var(--line);overflow-wrap:anywhere}.catalog-scope-scroll li:last-child{border:0}.catalog-scope-scroll code{font-size:13px;white-space:pre-wrap}
</style>
