<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, describeError, request, workspacePath, type Workspace } from './api'
import type { ClassificationRecord } from './classification-standards'
import type { ClassificationWorkbenchPage } from './classification-workbench'
import { readScenarioContext } from './scenario-context'
const props=defineProps<{workspace:Workspace}>();const emit=defineEmits<{change:[asset:ClassificationRecord|undefined];sessionExpired:[]}>()
const query=ref(''),id=ref(readScenarioContext(props.workspace.slug).assetId||''),items=ref<ClassificationRecord[]>([]),error=ref(''),busy=ref(false);const controller=new AbortController();let sequence=0
async function load(){const seq=++sequence;busy.value=true;error.value='';try{const page=await request<ClassificationWorkbenchPage>(workspacePath(props.workspace.slug)+'/classification/workbench?pageSize=50&q='+encodeURIComponent(query.value),{signal:controller.signal});if(seq!==sequence)return;items.value=page.items;if(id.value&&!items.value.some(a=>String(a.asset.id)===id.value)){const exact=await request<ClassificationRecord>(workspacePath(props.workspace.slug)+'/classification/assets/'+encodeURIComponent(id.value),{signal:controller.signal});if(seq!==sequence)return;items.value.unshift(exact)}emit('change',items.value.find(a=>String(a.asset.id)===id.value))}catch(e){if(!controller.signal.aborted&&seq===sequence){emit('change',undefined);if(e instanceof ApiError&&e.status===401)emit('sessionExpired');else error.value=describeError(e)}}finally{if(seq===sequence)busy.value=false}}
function change(){emit('change',items.value.find(a=>String(a.asset.id)===id.value))}
onMounted(load);onBeforeUnmount(()=>controller.abort())
</script>
<template><div class="capability-asset"><form @submit.prevent="load"><label>查找资产<input v-model="query" type="search" maxlength="100" placeholder="资产名称或系统"></label><button class="button" :disabled="busy">查询</button><label>选择实际资产<select v-model="id" @change="change"><option value="">请选择资产</option><option v-for="item in items" :key="item.asset.id" :value="String(item.asset.id)">{{item.asset.name}} · #{{item.asset.id}} · 分类修订{{item.revision}}</option></select></label></form><p v-if="error" class="message message-error" role="alert">{{error}}</p></div></template>
<style scoped>.capability-asset form{display:flex;flex-wrap:wrap;gap:12px;align-items:end}.capability-asset label{display:grid;gap:7px;font-size:12px;flex:1;min-width:180px}.capability-asset input,.capability-asset select{padding:9px 10px;border:1px solid var(--input-line);border-radius:6px;font:inherit;color:var(--ink);background:var(--surface);min-width:0;max-width:100%}</style>
