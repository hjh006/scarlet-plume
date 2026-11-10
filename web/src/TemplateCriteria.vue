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
