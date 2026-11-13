<script setup lang="ts">
import featherMark from './assets/phoenix-feather-mark.webp'
defineProps<{ href?: string }>()
</script>

<template>
  <a class="brand" :href="href || '#/'">
    <span class="brand-mark"><img :src="featherMark" width="48" height="48" alt="" decoding="async"></span>
    <span class="brand-name">赤翎<small lang="en">Scarlet Plume</small></span>
  </a>
</template>
