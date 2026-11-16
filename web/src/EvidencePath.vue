<script setup lang="ts">
defineProps<{ items: { label: string; value: string; note?: string; tone?: 'known' | 'pending' | 'error' }[]; label?: string }>()
</script>
<template>
  <ol class="evidence-path" :aria-label="label || '实际阶段与证据'">
    <li v-for="(item,index) in items" :key="item.label" :class="item.tone || 'pending'">
      <span class="path-number">{{ index + 1 }}</span><div><strong>{{ item.label }}</strong><span>{{ item.value }}</span><small v-if="item.note">{{ item.note }}</small></div><span v-if="index < items.length - 1" class="path-arrow" aria-hidden="true">→</span>
    </li>
  </ol>
</template>
<style scoped>
.evidence-path{list-style:none;padding:16px;margin:0 0 18px;display:flex;gap:26px;background:var(--surface,#fff);border:1px solid var(--line,#d2e2ee);border-radius:9px}.evidence-path li{position:relative;flex:1;min-width:0;display:flex;gap:10px;align-items:flex-start;padding:12px;background:var(--surface-subtle,#f3f7fb);border:1px solid var(--line);border-radius:7px}.path-number{display:grid;place-items:center;flex:none;width:26px;height:26px;background:#dce7f0;color:#425c74;border-radius:50%;font-size:13px;font-weight:700}.evidence-path strong{display:block;font-size:14px;line-height:1.6}.evidence-path li div>span{display:block;font-size:13px;color:var(--secondary);margin-top:3px;overflow-wrap:anywhere}.evidence-path small{display:block;font-size:12px;color:var(--secondary);margin-top:5px;line-height:1.6}.evidence-path .known{background:#eef9f3;border-color:#c7e5d5}.known .path-number{background:#d7efdf;color:#246944}.evidence-path .error{background:#fff1f0;border-color:#edc7c4}.error .path-number{background:#f8d9d5;color:#a2392f}.path-arrow{position:absolute;left:calc(100% + 7px);top:25px;color:#68869f;font-size:20px}@media(max-width:1100px){.evidence-path{gap:14px;padding:12px}.evidence-path li{padding:10px;gap:7px}.path-arrow{left:calc(100% + 3px);font-size:16px}.evidence-path strong{font-size:13px}.evidence-path li div>span{font-size:12px}}@media(max-width:720px){.evidence-path{flex-wrap:wrap;gap:10px}.evidence-path li{flex-basis:40%}.path-arrow{display:none}}
</style>
