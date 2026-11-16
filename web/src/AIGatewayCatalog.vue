<script setup lang="ts">
import {computed,onMounted,onUnmounted,ref} from 'vue'
import {ApiError,describeError,request,workspacePath,type Workspace} from './api'
import {aiRecords,aiString,type AISecurityBundle} from './ai-security'
const props=defineProps<{workspace:Workspace;kind:'applications'|'models'}>()
const emit=defineEmits<{sessionExpired:[]}>()
const bundle=ref<AISecurityBundle|null>(null),loading=ref(true),error=ref('')
const controller=new AbortController()
const records=computed(()=>bundle.value?aiRecords(bundle.value.configuration,props.kind):[])
const home='#/'+encodeURIComponent(props.workspace.slug)
async function load(){loading.value=true;error.value='';try{bundle.value=await request<AISecurityBundle>(workspacePath(props.workspace.slug)+'/ai-security',{signal:controller.signal})}catch(cause){if(controller.signal.aborted)return;if(cause instanceof ApiError&&cause.status===401)emit('sessionExpired');error.value=describeError(cause)}finally{loading.value=false}}
onMounted(load)
onUnmounted(()=>controller.abort())
</script>
<template>
  <section class="panel" :aria-busy="loading"><div class="ai-security-heading"><div><h2>{{ kind==='applications'?'统一应用授权目录':'统一模型目标目录' }}</h2><p>复用 AI 运行时安全的可信目录，网关不会另建一套可绕过原授权的对象。目录登记与实际路由接入分别验收。</p></div><div class="page-actions"><button class="button button-secondary" :disabled="loading" @click="load">重新读取目录</button><a class="button button-secondary" :href="home+'/ai-security?tab=configuration'">打开可信目录配置</a></div></div>
    <p v-if="error" class="message message-error" role="alert">{{ error }}{{ bundle?' 下方保留上次成功读取的目录，本次刷新未更新。':'' }}</p><p v-if="loading" class="ai-security-body" role="status">正在读取已有可信目录…</p>
    <div v-if="bundle" class="ai-security-body"><p class="table-secondary">已保存修订 {{ bundle.revision }} · 已发布修订 {{ bundle.publishedRevision || '尚未发布' }}。{{ bundle.revision!==bundle.publishedRevision?'当前显示包含尚未发布的配置；运行授权以客户侧实际采用版本为准。':'仍需核对客户侧实际采用的目录摘要。' }}</p></div>
    <div v-if="records.length" class="table-scroll" role="region" :aria-label="kind==='applications'?'应用授权目录':'模型目标目录'" tabindex="0"><table class="data-table"><thead><tr><th scope="col">名称与标识</th><th scope="col">目录状态</th><th scope="col">{{ kind==='applications'?'主体授权':'固定上游映射' }}</th><th scope="col">{{ kind==='applications'?'模型与工具授权':'地域及允许应用' }}</th></tr></thead><tbody><tr v-for="item in records" :key="aiString(item,'id')"><th scope="row">{{ aiString(item,'name','id') }}<span class="table-secondary">{{ aiString(item,'id') }}</span></th><td>{{ item.enabled?'已启用':'未启用' }}</td><td v-if="kind==='applications'">{{ aiString(item,'principalIds') || '未授权主体' }}</td><td v-else>{{ aiString(item,'provider') }} · {{ aiString(item,'upstreamModel','id') }}<span class="table-secondary">端点引用 {{ aiString(item,'endpointRef') }}</span></td><td v-if="kind==='applications'">模型 {{ aiString(item,'modelIds') || '无' }}<span class="table-secondary">工具 {{ aiString(item,'toolIds') || '无' }}</span></td><td v-else>{{ aiString(item,'region') }}<span class="table-secondary">允许应用 {{ aiString(item,'allowedApplicationIds') || '无' }}</span></td></tr></tbody></table></div>
    <p v-else-if="!loading && !error" class="empty-state">尚无{{ kind==='applications'?'应用授权':'模型目标' }}记录，请在可信目录配置中添加并完成发布。</p>
  </section>
</template>
