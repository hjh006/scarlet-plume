<script setup lang="ts">
defineProps<{ labels: string[]; current: number }>()
import Icon from './Icon.vue'
</script>
<template><div class="governance-steps" role="group" aria-label="工作步骤"><ol><li v-for="(label,index) in labels" :key="label" :class="{active:index===current}" :aria-current="index===current?'step':undefined"><span>{{index+1}}</span><strong>{{label}}</strong><Icon v-if="index<labels.length-1" name="arrow"/></li></ol></div></template>
