<script setup lang="ts">
import type { TemplateItem } from './template'

defineProps<{ item: TemplateItem }>()
</script>

<template>
  <dl class="template-criteria">
    <div><dt>适用条件</dt><dd class="text-block">{{ item.applicability }}</dd></div>
    <div><dt>取证要求</dt><dd class="text-block">{{ item.evidenceRequirements }}</dd></div>
    <div><dt>验收标准</dt><dd class="text-block">{{ item.acceptanceCriteria }}</dd></div>
  </dl>
</template>

<style scoped>.template-criteria{display:grid;gap:14px;margin:16px 0 0}.template-criteria>div{display:grid;grid-template-columns:74px minmax(0,1fr);gap:12px;padding-bottom:14px;border-bottom:1px solid #eaf0f5}.template-criteria>div:last-child{border:0;padding:0}.template-criteria dt{font-size:11px;color:var(--blue);line-height:1.9;padding-left:9px;border-left:2px solid #9ed0e3;font-weight:600}.template-criteria dd{margin:0;font-size:12px;line-height:1.9;white-space:pre-wrap;overflow-wrap:anywhere}.template-criteria dd:empty:after{content:'待填写';color:#8797a9}</style>
