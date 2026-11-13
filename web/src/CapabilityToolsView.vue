<script setup lang="ts">
import { defineAsyncComponent, h, type Component, onBeforeUnmount, onMounted, ref } from 'vue'
import type { Workspace } from './api'
import { scenarioContextHref } from './scenario-context'
function panel(loader:()=>Promise<{default:Component}>){return defineAsyncComponent({loader,delay:0,loadingComponent:()=>h('p',{class:'message',role:'status'},'正在载入工具…'),errorComponent:()=>h('p',{class:'message message-error',role:'alert'},'工具资源载入失败，请刷新页面重试。')})}
const BrowserAnnotationsPanel=panel(()=>import('./BrowserAnnotationsPanel.vue'))
const FileDiscoveryPanel=panel(()=>import('./FileDiscoveryPanel.vue'))
const ValueSearchPanel=panel(()=>import('./ValueSearchPanel.vue'))
const ClassificationKnowledgePanel=panel(()=>import('./ClassificationKnowledgePanel.vue'))
const OperationsReportsPanel=panel(()=>import('./OperationsReportsPanel.vue'))
const ClassificationDeliveryPanel=panel(()=>import('./ClassificationDeliveryPanel.vue'))
const ClassificationTrainingPanel=panel(()=>import('./ClassificationTrainingPanel.vue'))
const DataSecurityPanel=panel(()=>import('./DataSecurityPanel.vue'))
const props=defineProps<{workspace:Workspace}>();const emit=defineEmits<{sessionExpired:[]}>()
const tabs=[{id:'browser',label:'浏览器字段标注'},{id:'files',label:'文件接入'},{id:'value-search',label:'数据指纹溯源'},{id:'knowledge',label:'分类知识'},{id:'training',label:'本地模型训练'},{id:'delivery',label:'发布交付'},{id:'reports',label:'运营报告'},{id:'data-security',label:'数据外发与调查'}]
function read(){const value=new URLSearchParams(location.hash.split('?')[1]||'').get('tab');return tabs.find(t=>t.id===value)?.id||'browser'}
const tab=ref(read());function changed(){tab.value=read()}
function choose(id:string){
  const oldURL=window.location.href
  const url=new URL(oldURL),[path,query='']=url.hash.split('?'),params=new URLSearchParams(query)
  params.set('tab',id);params.delete('annotation');url.hash=path+'?'+params
  window.history.replaceState(window.history.state,'',url)
  tab.value=id
  window.dispatchEvent(new HashChangeEvent('hashchange',{oldURL,newURL:url.href}))
}
onMounted(()=>window.addEventListener('hashchange',changed));onBeforeUnmount(()=>window.removeEventListener('hashchange',changed))
</script>
<template><section class="capability-tools"><header class="page-header"><div><p class="eyebrow">{{tab==='data-security'?'场景结果 / 数据安全运营':'专业工具与配置 / 识别与证据能力'}}</p><h1>{{tab==='data-security'?'数据安全运营':'识别与证据能力'}}</h1><p>{{tab==='data-security'?'核对已接入来源的外发与审计事件，冻结调查证据，保留研判反馈。':'配置业务页面标注、文件发现、指纹查询和自有识别能力。结果保留实际来源、人工关联与版本依据。'}}</p></div><a class="button" :href="scenarioContextHref(workspace.slug,tab==='data-security'?'operations':'classification')">{{tab==='data-security'?'查看运营与处置':'返回分类分级'}}</a></header><nav class="capability-tabs" aria-label="识别与证据工具"><button v-for="item in tabs" :key="item.id" type="button" :class="{active:tab===item.id}" :aria-pressed="tab===item.id" @click="choose(item.id)">{{item.label}}</button></nav><BrowserAnnotationsPanel v-if="tab==='browser'" :workspace="workspace" @session-expired="emit('sessionExpired')"/><FileDiscoveryPanel v-else-if="tab==='files'" :workspace="workspace" @session-expired="emit('sessionExpired')"/><ValueSearchPanel v-else-if="tab==='value-search'" :workspace="workspace" @session-expired="emit('sessionExpired')"/><ClassificationKnowledgePanel v-else-if="tab==='knowledge'" :workspace="workspace" @session-expired="emit('sessionExpired')"/><ClassificationTrainingPanel v-else-if="tab==='training'" :workspace="workspace" @session-expired="emit('sessionExpired')"/><ClassificationDeliveryPanel v-else-if="tab==='delivery'" :workspace="workspace" @session-expired="emit('sessionExpired')"/><DataSecurityPanel v-else-if="tab==='data-security'" :workspace="workspace" @session-expired="emit('sessionExpired')"/><OperationsReportsPanel v-else-if="tab==='reports'" :workspace="workspace" @session-expired="emit('sessionExpired')"/></section></template>
<style scoped>.capability-tools{display:grid;gap:20px;min-width:0}.capability-tools .page-header{margin:0}.capability-tabs{display:flex;flex-wrap:wrap;border-bottom:1px solid var(--line);gap:4px}.capability-tabs button{border:0;border-bottom:2px solid transparent;background:none;color:var(--secondary);padding:13px 14px;font:inherit;font-size:12px;cursor:pointer}.capability-tabs button.active{border-bottom-color:var(--accent);color:var(--accent);font-weight:700}</style>
