<script setup lang="ts">
import { computed } from 'vue'
import type { CatalogEntry } from './discovery'
import { catalogKinds } from './discovery'
import Icon from './Icon.vue'
const props=defineProps<{entries:CatalogEntry[];selected:CatalogEntry|null}>()
const emit=defineEmits<{select:[entry:CatalogEntry]}>()
const parent=computed(()=>props.selected?.parentRef?props.entries.find(entry=>entry.source===props.selected?.source&&entry.sourceRef===props.selected?.parentRef):undefined)
const root=computed(()=>parent.value??props.selected)
const children=computed(()=>root.value?.sourceRef?props.entries.filter(entry=>entry.source===root.value?.source&&entry.parentRef===root.value?.sourceRef&&entry.id!==root.value?.id):[])
const displayed=computed(()=>children.value.slice(0,6))
const owner=(entry:CatalogEntry)=>String(entry.confirmedMetadata.ownerTeam||entry.metadata.ownerTeam||'责任团队待确认')
</script>
<template><section class="panel governance-hierarchy"><div class="panel-heading"><div><h2><Icon name="classification"/>业务责任层级</h2><p>仅连接来源明确提供的 parentRef；责任登记与来源观察分别核对。</p></div></div><div v-if="root" class="governance-hierarchy-canvas"><div class="governance-hierarchy-intro"><h3>{{catalogKinds[root.kind]||root.kind}}</h3><p>来源：{{root.source}}</p><p>当前已加载 {{children.length}} 个直接下级{{children.length>6?'，图中预览6个':''}}。</p></div><button type="button" class="governance-hierarchy-root" :aria-pressed="selected?.id===root.id" @click="emit('select',root)"><Icon name="workspace"/><span><strong>{{root.name}}</strong><small>{{owner(root)}}</small></span></button><div v-if="displayed.length" class="governance-hierarchy-children"><button v-for="entry in displayed" :key="entry.id" type="button" :class="{selected:selected?.id===entry.id}" :aria-pressed="selected?.id===entry.id" @click="emit('select',entry)"><strong><Icon name="assets"/>{{entry.name}}</strong><span>{{catalogKinds[entry.kind]||entry.kind}}</span><span>{{owner(entry)}}</span><span class="badge" :class="Object.keys(entry.conflicts).length?'status-needs_work':'status-draft'">{{Object.keys(entry.conflicts).length?'来源与确认有差异':entry.current?'当前批次记录':'来源当前性待核实'}}</span></button></div><p v-else class="governance-hierarchy-empty"><Icon name="info"/>当前已加载记录中未取得明确下级关系。保留此对象，不按名称补造层级。</p><p v-if="selected?.parentRef&&!parent" class="field-hint">父引用 {{selected.parentRef}} 未在本次已加载记录中解析；可能需调整筛选或补全目录。</p></div><p v-else class="empty-state">采集或导入目录后，选择对象核对其明确层级。</p></section></template>
